/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package types

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

func tokenClaim(name string) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid")}}
}

func tokenClient(t *testing.T) *fake.Clientset {
	t.Helper()
	kube := fake.NewClientset()
	var sequence atomic.Int64
	kube.PrependReactor("create", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
		secret := action.(ktesting.CreateAction).GetObject().(*corev1.Secret)
		if secret.UID == "" {
			secret.UID = types.UID(fmt.Sprintf("secret-%d", sequence.Add(1)))
		}
		secret.ResourceVersion = "1"
		return false, nil, nil
	})
	kube.PrependReactor("create", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		cm := action.(ktesting.CreateAction).GetObject().(*corev1.ConfigMap)
		cm.UID = types.UID("record-" + cm.Name)
		cm.ResourceVersion = "1"
		return false, nil, nil
	})
	kube.PrependReactor("update", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		cm := action.(ktesting.UpdateAction).GetObject().(*corev1.ConfigMap)
		current, err := kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), cm.Namespace, cm.Name)
		if err != nil {
			return true, nil, err
		}
		original := current.(*corev1.ConfigMap)
		if original.ResourceVersion != cm.ResourceVersion || original.UID != cm.UID {
			return true, nil, apierrors.NewConflict(corev1.Resource("configmaps"), cm.Name, fmt.Errorf("record changed"))
		}
		version, err := strconv.Atoi(cm.ResourceVersion)
		if err != nil {
			return true, nil, err
		}
		cm.ResourceVersion = strconv.Itoa(version + 1)
		if err := kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("configmaps"), cm, cm.Namespace); err != nil {
			return true, nil, err
		}
		return true, cm.DeepCopy(), nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		require.True(t, strings.HasPrefix(review.Spec.User, "system:bootstrap:"))
		require.ElementsMatch(t, []string{"system:authenticated", "system:bootstrappers", BootstrapGroup}, review.Spec.Groups)
		require.Equal(t, "certificates.k8s.io", review.Spec.ResourceAttributes.Group)
		require.Equal(t, "certificatesigningrequests", review.Spec.ResourceAttributes.Resource)
		require.Equal(t, "nodeclient", review.Spec.ResourceAttributes.Subresource)
		require.Equal(t, "create", review.Spec.ResourceAttributes.Verb)
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Denied: true}}, nil
	})
	return kube
}

func TestClaimBootstrapCredentialsConvergeUnderConcurrentIssuance(t *testing.T) {
	kube := tokenClient(t)
	claim := tokenClaim("claim")
	const callers = 24
	tokens := make([]string, callers)
	failures := make([]error, callers)
	var workers sync.WaitGroup
	for i := range callers {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			tokens[i], failures[i] = FindOrCreateClaimBootstrapToken(context.Background(), kube, claim, "cluster-uid")
		}(i)
	}
	workers.Wait()
	for i := range callers {
		require.NoError(t, failures[i])
		require.True(t, tokens[i] == tokens[0])
	}
	secrets, err := kube.CoreV1().Secrets("kube-system").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, secrets.Items, 1)
	secret := &secrets.Items[0]
	require.True(t, BootstrapTokenOwnedBy(secret, claim, "cluster-uid"))
	require.NotEmpty(t, secret.UID)
	require.Empty(t, secret.Data["usage-bootstrap-signing"])
	expiry, err := time.Parse(time.RFC3339, string(secret.Data["expiration"]))
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(BootstrapTokenTTL), expiry, time.Minute)
	record, err := kube.CoreV1().ConfigMaps("kube-system").Get(context.Background(), "karpenter-bootstrap-"+string(claim.UID), metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, string(secret.UID), record.Data["secretUID"])
	role, err := kube.RbacV1().Roles("kube-system").Get(context.Background(), record.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, role.Rules, 1)
	require.Equal(t, []string{record.Name}, role.Rules[0].ResourceNames)
	require.ElementsMatch(t, []string{"get", "patch"}, role.Rules[0].Verbs)
	binding, err := kube.RbacV1().RoleBindings("kube-system").Get(context.Background(), record.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "system:bootstrap:"+record.Data["tokenID"], binding.Subjects[0].Name)
}

func TestSeparateClaimsHaveSeparateCredentials(t *testing.T) {
	kube := tokenClient(t)
	one, two := tokenClaim("one"), tokenClaim("two")
	first, err := FindOrCreateClaimBootstrapToken(context.Background(), kube, one, "cluster-uid")
	require.NoError(t, err)
	second, err := FindOrCreateClaimBootstrapToken(context.Background(), kube, two, "cluster-uid")
	require.NoError(t, err)
	require.True(t, first != second)
	secrets, err := kube.CoreV1().Secrets("kube-system").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, secrets.Items, 2)
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		require.True(t, BootstrapTokenOwnedBy(secret, one, "cluster-uid") != BootstrapTokenOwnedBy(secret, two, "cluster-uid"))
	}
}

func TestCredentialRotationChangesTokenAndStatusSubject(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			kube := tokenClient(t)
			claim := tokenClaim("claim")
			previous, err := FindOrCreateClaimBootstrapToken(context.Background(), kube, claim, "cluster-uid")
			require.NoError(t, err)
			oldID := strings.Split(previous, ".")[0]
			secret, err := kube.CoreV1().Secrets("kube-system").Get(context.Background(), "bootstrap-token-"+oldID, metav1.GetOptions{})
			require.NoError(t, err)
			if missing {
				require.NoError(t, kube.CoreV1().Secrets("kube-system").Delete(context.Background(), secret.Name, metav1.DeleteOptions{}))
			} else {
				secret.Data["expiration"] = []byte(time.Now().Add(4 * time.Minute).UTC().Format(time.RFC3339))
				_, err = kube.CoreV1().Secrets("kube-system").Update(context.Background(), secret, metav1.UpdateOptions{})
				require.NoError(t, err)
			}
			current, err := FindOrCreateClaimBootstrapToken(context.Background(), kube, claim, "cluster-uid")
			require.NoError(t, err)
			require.True(t, previous != current)
			record, err := kube.CoreV1().ConfigMaps("kube-system").Get(context.Background(), "karpenter-bootstrap-"+string(claim.UID), metav1.GetOptions{})
			require.NoError(t, err)
			require.NotEqual(t, oldID, record.Data["tokenID"])
			require.NotEmpty(t, record.Data["secretUID"])
			binding, err := kube.RbacV1().RoleBindings("kube-system").Get(context.Background(), record.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, "system:bootstrap:"+record.Data["tokenID"], binding.Subjects[0].Name)
		})
	}
}

func TestCredentialUIDOwnershipAndFormatArePinned(t *testing.T) {
	for _, mutation := range []string{"secret UID", "secret owner", "foreign cluster", "record version", "record owner", "expiration"} {
		t.Run(mutation, func(t *testing.T) {
			kube := tokenClient(t)
			claim := tokenClaim("claim")
			token, err := FindOrCreateClaimBootstrapToken(context.Background(), kube, claim, "cluster-uid")
			require.NoError(t, err)
			id := strings.Split(token, ".")[0]
			secret, err := kube.CoreV1().Secrets("kube-system").Get(context.Background(), "bootstrap-token-"+id, metav1.GetOptions{})
			require.NoError(t, err)
			record, err := kube.CoreV1().ConfigMaps("kube-system").Get(context.Background(), "karpenter-bootstrap-"+string(claim.UID), metav1.GetOptions{})
			require.NoError(t, err)
			switch mutation {
			case "secret UID":
				require.NoError(t, kube.CoreV1().Secrets("kube-system").Delete(context.Background(), secret.Name, metav1.DeleteOptions{}))
				secret.UID = "replacement"
				_, err = kube.CoreV1().Secrets("kube-system").Create(context.Background(), secret, metav1.CreateOptions{})
				require.NoError(t, err)
			case "secret owner":
				secret.OwnerReferences[0].UID = "foreign"
				_, err = kube.CoreV1().Secrets("kube-system").Update(context.Background(), secret, metav1.UpdateOptions{})
				require.NoError(t, err)
			case "foreign cluster":
				secret.Labels[ownership.ClusterUIDLabel] = "foreign"
				_, err = kube.CoreV1().Secrets("kube-system").Update(context.Background(), secret, metav1.UpdateOptions{})
				require.NoError(t, err)
			case "record version":
				record.Data["version"] = "2"
				_, err = kube.CoreV1().ConfigMaps("kube-system").Update(context.Background(), record, metav1.UpdateOptions{})
				require.NoError(t, err)
			case "record owner":
				record.OwnerReferences[0].UID = "foreign"
				_, err = kube.CoreV1().ConfigMaps("kube-system").Update(context.Background(), record, metav1.UpdateOptions{})
				require.NoError(t, err)
			case "expiration":
				secret.Data["expiration"] = []byte("invalid")
				_, err = kube.CoreV1().Secrets("kube-system").Update(context.Background(), secret, metav1.UpdateOptions{})
				require.NoError(t, err)
			}
			_, err = FindOrCreateClaimBootstrapToken(context.Background(), kube, claim, "cluster-uid")
			require.Error(t, err)
			secrets, err := kube.CoreV1().Secrets("kube-system").List(context.Background(), metav1.ListOptions{})
			require.NoError(t, err)
			require.Len(t, secrets.Items, 1)
		})
	}
}

func TestRegisteredOrDeletingClaimsCannotMintCredentials(t *testing.T) {
	for _, registered := range []bool{false, true} {
		kube := tokenClient(t)
		claim := tokenClaim("claim")
		if registered {
			claim.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)
		} else {
			claim.DeletionTimestamp = &metav1.Time{Time: time.Now()}
		}
		_, err := FindOrCreateClaimBootstrapToken(context.Background(), kube, claim, "cluster-uid")
		require.Error(t, err)
		require.Empty(t, kube.Actions())
	}
}

func TestGenericCSRApprovalAuthorizationBlocksIssuance(t *testing.T) {
	for _, mode := range []string{"allowed", "evaluation error", "request error"} {
		t.Run(mode, func(t *testing.T) {
			kube := tokenClient(t)
			kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				status := authorizationv1.SubjectAccessReviewStatus{}
				switch mode {
				case "allowed":
					status.Allowed = true
				case "evaluation error":
					status.EvaluationError = "authorization unavailable"
				case "request error":
					return true, nil, fmt.Errorf("API unavailable")
				}
				return true, &authorizationv1.SubjectAccessReview{Status: status}, nil
			})
			_, err := FindOrCreateClaimBootstrapToken(context.Background(), kube, tokenClaim("claim"), "cluster-uid")
			require.Error(t, err)
			secrets, err := kube.CoreV1().Secrets("kube-system").List(context.Background(), metav1.ListOptions{})
			require.NoError(t, err)
			require.Empty(t, secrets.Items)
		})
	}
}

func TestForeignStatusPermissionsAreNeverAdopted(t *testing.T) {
	kube := tokenClient(t)
	claim := tokenClaim("claim")
	_, err := kube.RbacV1().Roles("kube-system").Create(context.Background(), &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-bootstrap-" + string(claim.UID), Namespace: "kube-system"}, Rules: []rbacv1.PolicyRule{{Resources: []string{"secrets"}, Verbs: []string{"get"}}}}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = FindOrCreateClaimBootstrapToken(context.Background(), kube, claim, "cluster-uid")
	require.Error(t, err)
	role, err := kube.RbacV1().Roles("kube-system").Get(context.Background(), "karpenter-bootstrap-"+string(claim.UID), metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"secrets"}, role.Rules[0].Resources)
}
