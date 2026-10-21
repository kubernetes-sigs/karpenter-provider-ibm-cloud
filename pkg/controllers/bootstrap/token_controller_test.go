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

package bootstrap

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

func maintenanceClaim() *karpv1.NodeClaim {
	return &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim-a", UID: "claim-uid"}}
}

func maintenanceSecret(claim *karpv1.NodeClaim) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bootstrap-token-abcdef", Namespace: "kube-system", UID: "secret-uid", ResourceVersion: "7",
			Labels:          map[string]string{commonTypes.BootstrapTokenLabel: "true", ownership.ClaimUIDLabel: string(claim.UID), ownership.ClusterUIDLabel: "cluster-uid"},
			Annotations:     map[string]string{commonTypes.BootstrapClaimAnnotation: claim.Name},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: claim.Name, UID: claim.UID}},
		},
		Type: corev1.SecretTypeBootstrapToken,
		Data: map[string][]byte{
			"token-id": []byte("abcdef"), "token-secret": []byte("abcdefghijklmnop"),
			"expiration":        []byte(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)),
			"auth-extra-groups": []byte(commonTypes.BootstrapGroup), "usage-bootstrap-authentication": []byte("true"),
		},
	}
}

func maintenanceReader(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}})
	return crfake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&karpv1.NodeClaim{}).WithObjects(objects...).Build()
}

func bootstrapBinding(name, role string, subjects ...rbacv1.Subject) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: k8stypes.UID(name + "-uid"), ResourceVersion: "3"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}, Subjects: subjects,
	}
}

func groupSubject(name string) rbacv1.Subject {
	return rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "Group", Name: name}
}

func TestBootstrapMaintenanceCreatesOnlyInitialAccessAndRenewalBindings(t *testing.T) {
	kube := fake.NewClientset()
	controller := &TokenController{client: kube, reader: maintenanceReader(t)}
	result, err := controller.Reconcile(context.Background(), reconcile.Request{})
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, result.RequeueAfter)
	require.True(t, controller.NeedLeaderElection())
	bindings, err := kube.RbacV1().ClusterRoleBindings().List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, bindings.Items, 2)
	expected := map[string]struct{ role, group string }{
		"karpenter-ibm-bootstrap-nodes":       {"system:node-bootstrapper", commonTypes.BootstrapGroup},
		"karpenter-ibm-auto-approve-renewals": {"system:certificates.k8s.io:certificatesigningrequests:selfnodeclient", "system:nodes"},
	}
	for _, binding := range bindings.Items {
		spec, ok := expected[binding.Name]
		require.True(t, ok)
		require.Equal(t, rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: spec.role}, binding.RoleRef)
		require.Equal(t, []rbacv1.Subject{groupSubject(spec.group)}, binding.Subjects)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, controller.Start(ctx))
}

func TestLegacyApprovalRemovalPreservesUnrelatedSubjects(t *testing.T) {
	for _, legacy := range []struct{ name, role, group string }{
		{"karpenter-ibm-auto-approve-csrs", "system:certificates.k8s.io:certificatesigningrequests:nodeclient", commonTypes.BootstrapGroup},
		{"karpenter-ibm-auto-approve-kubelet-serving-csrs", "system:certificates.k8s.io:kubelet-serving-approver", "system:nodes"},
	} {
		t.Run(legacy.name, func(t *testing.T) {
			retained := []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: "operator"}, groupSubject("unrelated-group")}
			subjects := append([]rbacv1.Subject{groupSubject(legacy.group)}, retained...)
			binding := bootstrapBinding(legacy.name, legacy.role, subjects...)
			kube := fake.NewClientset(binding)
			controller := &TokenController{client: kube}
			require.NoError(t, controller.ensureBootstrapRBAC(context.Background(), log.Log))
			updated, err := kube.RbacV1().ClusterRoleBindings().Get(context.Background(), binding.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, retained, updated.Subjects)
			require.Equal(t, binding.RoleRef, updated.RoleRef)
			require.NoError(t, controller.ensureBootstrapRBAC(context.Background(), log.Log))
		})
	}
}

func TestLegacyApprovalDeletePinsBindingIdentity(t *testing.T) {
	binding := bootstrapBinding("karpenter-ibm-auto-approve-csrs", "system:certificates.k8s.io:certificatesigningrequests:nodeclient", groupSubject(commonTypes.BootstrapGroup))
	kube := fake.NewClientset(binding)
	deletes := 0
	kube.PrependReactor("delete", "clusterrolebindings", func(action ktesting.Action) (bool, runtime.Object, error) {
		options := action.(ktesting.DeleteAction).GetDeleteOptions()
		require.NotNil(t, options.Preconditions)
		require.Equal(t, binding.UID, *options.Preconditions.UID)
		require.Equal(t, binding.ResourceVersion, *options.Preconditions.ResourceVersion)
		deletes++
		return false, nil, nil
	})
	controller := &TokenController{client: kube}
	require.NoError(t, controller.ensureBootstrapRBAC(context.Background(), log.Log))
	require.Equal(t, 1, deletes)
	_, err := kube.RbacV1().ClusterRoleBindings().Get(context.Background(), binding.Name, metav1.GetOptions{})
	require.True(t, apierrors.IsNotFound(err))
}

func TestBootstrapBindingRoleRefChangesAreRejected(t *testing.T) {
	for _, name := range []string{"karpenter-ibm-auto-approve-csrs", "karpenter-ibm-bootstrap-nodes", "karpenter-ibm-auto-approve-renewals"} {
		t.Run(name, func(t *testing.T) {
			binding := bootstrapBinding(name, "cluster-admin", groupSubject(commonTypes.BootstrapGroup))
			kube := fake.NewClientset(binding)
			controller := &TokenController{client: kube}
			require.Error(t, controller.ensureBootstrapRBAC(context.Background(), log.Log))
			actual, err := kube.RbacV1().ClusterRoleBindings().Get(context.Background(), name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Equal(t, binding, actual)
		})
	}
}

func TestBootstrapBindingsPreserveExistingOperatorSubjects(t *testing.T) {
	operator := rbacv1.Subject{Kind: "User", APIGroup: rbacv1.GroupName, Name: "operator"}
	binding := bootstrapBinding("karpenter-ibm-bootstrap-nodes", "system:node-bootstrapper", operator)
	kube := fake.NewClientset(binding)
	controller := &TokenController{client: kube}
	require.NoError(t, controller.ensureBootstrapRBAC(context.Background(), log.Log))
	actual, err := kube.RbacV1().ClusterRoleBindings().Get(context.Background(), binding.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.ElementsMatch(t, []rbacv1.Subject{operator, groupSubject(commonTypes.BootstrapGroup)}, actual.Subjects)
}

func TestTokenRevocationRequiresOwnedIdentity(t *testing.T) {
	for _, scenario := range []string{
		"active", "expired", "registered", "deleting", "missing", "replaced claim",
		"foreign expired cluster", "foreign owner", "missing owner", "opaque secret", "bad expiration", "unlabelled",
		"legacy expired", "legacy active", "legacy foreign group", "legacy wrong name",
	} {
		t.Run(scenario, func(t *testing.T) {
			claim := maintenanceClaim()
			secret := maintenanceSecret(claim)
			expired := []byte(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
			remove := false
			var liveClaims []client.Object
			switch scenario {
			case "expired":
				secret.Data["expiration"], remove = expired, true
			case "registered":
				claim.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)
				remove = true
			case "deleting":
				claim.Finalizers = []string{"test-finalizer"}
				claim.DeletionTimestamp = &metav1.Time{Time: time.Now()}
				remove = true
			case "missing":
				remove = true
			case "replaced claim":
				claim.UID, remove = "new-claim-uid", true
			case "foreign expired cluster":
				secret.Labels[ownership.ClusterUIDLabel] = "foreign-cluster"
				secret.Data["expiration"] = expired
			case "foreign owner":
				secret.OwnerReferences[0].UID = "foreign-claim"
				secret.Data["expiration"] = expired
			case "missing owner":
				secret.OwnerReferences = nil
				secret.Data["expiration"] = expired
			case "opaque secret":
				secret.Type = corev1.SecretTypeOpaque
				secret.Data["expiration"] = expired
			case "bad expiration":
				secret.Data["expiration"] = []byte("not-a-time")
			case "unlabelled":
				delete(secret.Labels, commonTypes.BootstrapTokenLabel)
				secret.Data["expiration"] = expired
			case "legacy expired", "legacy active", "legacy foreign group", "legacy wrong name":
				delete(secret.Labels, ownership.ClaimUIDLabel)
				delete(secret.Labels, ownership.ClusterUIDLabel)
				secret.OwnerReferences = nil
				if scenario != "legacy active" {
					secret.Data["expiration"] = expired
				}
				switch scenario {
				case "legacy expired":
					remove = true
				case "legacy foreign group":
					secret.Data["auth-extra-groups"] = []byte("foreign-group")
				case "legacy wrong name":
					secret.Name = "different-token-name"
				}
			}
			if scenario != "missing" {
				liveClaims = append(liveClaims, claim)
			}
			kube := fake.NewClientset(secret)
			deletes := 0
			kube.PrependReactor("delete", "secrets", func(action ktesting.Action) (bool, runtime.Object, error) {
				require.Equal(t, secret.Name, action.(ktesting.DeleteAction).GetName())
				options := action.(ktesting.DeleteAction).GetDeleteOptions()
				require.NotNil(t, options.Preconditions)
				require.Equal(t, secret.UID, *options.Preconditions.UID)
				require.Equal(t, secret.ResourceVersion, *options.Preconditions.ResourceVersion)
				deletes++
				return false, nil, nil
			})
			controller := &TokenController{client: kube, reader: maintenanceReader(t, liveClaims...)}
			require.NoError(t, controller.cleanupExpiredTokens(context.Background(), log.Log))
			_, err := kube.CoreV1().Secrets("kube-system").Get(context.Background(), secret.Name, metav1.GetOptions{})
			if remove {
				require.Equal(t, 1, deletes)
				require.True(t, apierrors.IsNotFound(err))
			} else {
				require.Zero(t, deletes)
				require.NoError(t, err)
			}
		})
	}
}

func TestTokenRevocationConflictPreservesCredential(t *testing.T) {
	claim := maintenanceClaim()
	secret := maintenanceSecret(claim)
	secret.Data["expiration"] = []byte(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
	kube := fake.NewClientset(secret)
	kube.PrependReactor("delete", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(corev1.Resource("secrets"), secret.Name, fmt.Errorf("replaced credential"))
	})
	controller := &TokenController{client: kube, reader: maintenanceReader(t, claim)}
	require.Error(t, controller.cleanupExpiredTokens(context.Background(), log.Log))
	_, err := kube.CoreV1().Secrets("kube-system").Get(context.Background(), secret.Name, metav1.GetOptions{})
	require.NoError(t, err)
}

func drainBootstrapEvents(recorder *record.FakeRecorder) int {
	count := 0
	for {
		select {
		case <-recorder.Events:
			count++
		default:
			return count
		}
	}
}

func TestBootstrapFailureStatusEventsAreScopedAndDeduplicated(t *testing.T) {
	claim := maintenanceClaim()
	status := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-bootstrap-" + string(claim.UID), Namespace: "kube-system"},
		Data: map[string]string{"claimUID": string(claim.UID), "clusterUID": "cluster-uid", "status": "failed", "phase": "metadata"}}
	reader := maintenanceReader(t, claim, status)
	recorder := record.NewFakeRecorder(10)
	controller := &TokenController{client: fake.NewClientset(), reader: reader, recorder: recorder}
	for range 2 {
		require.NoError(t, controller.cleanupExpiredTokens(context.Background(), log.Log))
	}
	require.Equal(t, 1, drainBootstrapEvents(recorder))
	require.NoError(t, reader.Get(context.Background(), client.ObjectKeyFromObject(status), status))
	status.Data["phase"] = "download"
	require.NoError(t, reader.Update(context.Background(), status))
	require.NoError(t, controller.cleanupExpiredTokens(context.Background(), log.Log))
	require.Equal(t, 1, drainBootstrapEvents(recorder))
	for _, mutation := range []string{"phase", "claimUID", "clusterUID", "status"} {
		bad := status.DeepCopy()
		bad.Data[mutation] = "foreign/or-invalid"
		require.NoError(t, reader.Update(context.Background(), bad))
		require.NoError(t, controller.cleanupExpiredTokens(context.Background(), log.Log))
		require.Zero(t, drainBootstrapEvents(recorder))
		require.NoError(t, reader.Get(context.Background(), client.ObjectKeyFromObject(status), status))
		status.Data = map[string]string{"claimUID": string(claim.UID), "clusterUID": "cluster-uid", "status": "failed", "phase": "download"}
		require.NoError(t, reader.Update(context.Background(), status))
	}
	require.NoError(t, reader.Delete(context.Background(), claim))
	require.NoError(t, controller.cleanupExpiredTokens(context.Background(), log.Log))
	require.Empty(t, controller.observed)
}
