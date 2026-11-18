/*
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
	"crypto/rand"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

const (
	BootstrapGroup           = "system:bootstrappers:karpenter:ibm-cloud"
	BootstrapClaimAnnotation = "karpenter-ibm.sh/bootstrap-nodeclaim"
	BootstrapTokenLabel      = "karpenter.sh/bootstrap-token"
	BootstrapTokenTTL        = time.Hour
)

var tokenIDPattern = regexp.MustCompile(`^[a-z0-9]{6}$`)
var tokenSecretPattern = regexp.MustCompile(`^[a-z0-9]{16}$`)
var bootstrapClaimGroupVersion = schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}

func randomTokenPart(length int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	result := make([]byte, length)
	for i := range result {
		value, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		result[i] = alphabet[value.Int64()]
	}
	return string(result), nil
}

func BootstrapTokenOwnedBy(secret *corev1.Secret, claim *karpv1.NodeClaim, clusterUID string) bool {
	if secret == nil || claim == nil || clusterUID == "" || claim.UID == "" ||
		secret.Namespace != "kube-system" || secret.Type != corev1.SecretTypeBootstrapToken ||
		secret.Labels[BootstrapTokenLabel] != "true" || secret.Labels[ownership.ClaimUIDLabel] != string(claim.UID) ||
		secret.Labels[ownership.ClusterUIDLabel] != clusterUID || secret.Annotations[BootstrapClaimAnnotation] != claim.Name ||
		len(secret.OwnerReferences) != 1 {
		return false
	}
	owner := secret.OwnerReferences[0]
	return owner.APIVersion == bootstrapClaimGroupVersion.String() && owner.Kind == "NodeClaim" &&
		owner.Name == claim.Name && owner.UID == claim.UID &&
		secret.Name == "bootstrap-token-"+string(secret.Data["token-id"]) &&
		tokenIDPattern.Match(secret.Data["token-id"]) && tokenSecretPattern.Match(secret.Data["token-secret"]) &&
		string(secret.Data["auth-extra-groups"]) == BootstrapGroup &&
		string(secret.Data["usage-bootstrap-authentication"]) == "true"
}

// CheckBootstrapApprovalPolicy prevents Kubernetes' generic approver from bypassing claim verification.
func CheckBootstrapApprovalPolicy(ctx context.Context, client kubernetes.Interface, tokenID string) error {
	review, err := client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   "system:bootstrap:" + tokenID,
			Groups: []string{"system:authenticated", "system:bootstrappers", BootstrapGroup},
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Group: "certificates.k8s.io", Resource: "certificatesigningrequests", Subresource: "nodeclient", Verb: "create",
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("checking bootstrap approval policy: %w", err)
	}
	if review.Status.EvaluationError != "" || review.Status.Allowed {
		return fmt.Errorf("bootstrap nodeclient autoapproval must be disabled for provider tokens before provisioning")
	}
	return nil
}

func FindOrCreateClaimBootstrapToken(ctx context.Context, client kubernetes.Interface, claim *karpv1.NodeClaim, clusterUID string) (string, error) {
	if claim == nil || claim.Name == "" || claim.UID == "" || clusterUID == "" || !claim.DeletionTimestamp.IsZero() {
		return "", fmt.Errorf("bootstrap credentials require a live NodeClaim and cluster UID")
	}
	for _, condition := range claim.Status.Conditions {
		if condition.Type == karpv1.ConditionTypeRegistered && condition.Status == metav1.ConditionTrue {
			return "", fmt.Errorf("registered NodeClaims cannot obtain bootstrap credentials")
		}
	}
	owner := *metav1.NewControllerRef(claim, bootstrapClaimGroupVersion.WithKind("NodeClaim"))
	recordName := "karpenter-bootstrap-" + string(claim.UID)
	var tokenID string
	reservationErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		record, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, recordName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			id, randomErr := randomTokenPart(6)
			if randomErr != nil {
				return randomErr
			}
			record = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: recordName, Namespace: "kube-system", OwnerReferences: []metav1.OwnerReference{owner}},
				Data:       map[string]string{"version": "1", "claimUID": string(claim.UID), "clusterUID": clusterUID, "tokenID": id},
			}
			if _, err = client.CoreV1().ConfigMaps("kube-system").Create(ctx, record, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(corev1.Resource("configmaps"), recordName, err)
			} else if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if record.Data["version"] != "1" || record.Data["claimUID"] != string(claim.UID) || record.Data["clusterUID"] != clusterUID ||
			len(record.OwnerReferences) != 1 || record.OwnerReferences[0].UID != claim.UID ||
			record.OwnerReferences[0].Name != claim.Name || record.OwnerReferences[0].Kind != "NodeClaim" ||
			record.OwnerReferences[0].APIVersion != bootstrapClaimGroupVersion.String() || !tokenIDPattern.MatchString(record.Data["tokenID"]) {
			return fmt.Errorf("bootstrap credential record has unsupported format or different ownership")
		}
		tokenID = record.Data["tokenID"]
		secret, err := client.CoreV1().Secrets("kube-system").Get(ctx, "bootstrap-token-"+tokenID, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if record.Data["secretUID"] == "" {
				return nil
			}
			id, randomErr := randomTokenPart(6)
			if randomErr != nil {
				return randomErr
			}
			tokenID = id
			record.Data["tokenID"] = id
			delete(record.Data, "secretUID")
			_, err = client.CoreV1().ConfigMaps("kube-system").Update(ctx, record, metav1.UpdateOptions{})
			return err
		} else if err != nil {
			return err
		}
		if !BootstrapTokenOwnedBy(secret, claim, clusterUID) {
			return fmt.Errorf("bootstrap token ID is already owned by another identity")
		}
		expiration, err := time.Parse(time.RFC3339, string(secret.Data["expiration"]))
		if err != nil {
			return fmt.Errorf("bootstrap token expiration is invalid")
		}
		if time.Until(expiration) > 5*time.Minute {
			return nil
		}
		tokenID, err = randomTokenPart(6)
		if err != nil {
			return err
		}
		record.Data["tokenID"] = tokenID
		delete(record.Data, "secretUID")
		_, err = client.CoreV1().ConfigMaps("kube-system").Update(ctx, record, metav1.UpdateOptions{})
		return err
	})
	if reservationErr != nil {
		return "", fmt.Errorf("reserving claim bootstrap credential: %w", reservationErr)
	}
	if err := CheckBootstrapApprovalPolicy(ctx, client, tokenID); err != nil {
		return "", err
	}
	secretPart, err := randomTokenPart(16)
	if err != nil {
		return "", err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bootstrap-token-" + tokenID, Namespace: "kube-system", OwnerReferences: []metav1.OwnerReference{owner},
			Labels:      map[string]string{BootstrapTokenLabel: "true", ownership.ClaimUIDLabel: string(claim.UID), ownership.ClusterUIDLabel: clusterUID},
			Annotations: map[string]string{BootstrapClaimAnnotation: claim.Name},
		},
		Type: corev1.SecretTypeBootstrapToken,
		Data: map[string][]byte{
			"token-id": []byte(tokenID), "token-secret": []byte(secretPart),
			"usage-bootstrap-authentication": []byte("true"), "auth-extra-groups": []byte(BootstrapGroup),
			"expiration": []byte(time.Now().Add(BootstrapTokenTTL).UTC().Format(time.RFC3339)),
		},
	}
	secret, err = client.CoreV1().Secrets("kube-system").Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		secret, err = client.CoreV1().Secrets("kube-system").Get(ctx, "bootstrap-token-"+tokenID, metav1.GetOptions{})
	}
	if err != nil {
		return "", fmt.Errorf("creating claim bootstrap token: %w", err)
	}
	if secret.UID == "" || !BootstrapTokenOwnedBy(secret, claim, clusterUID) {
		return "", fmt.Errorf("created bootstrap credential ownership changed")
	}
	expiration, err := time.Parse(time.RFC3339, string(secret.Data["expiration"]))
	if err != nil || !expiration.After(time.Now()) || expiration.After(time.Now().Add(BootstrapTokenTTL+time.Minute)) {
		return "", fmt.Errorf("bootstrap credential expiration is invalid")
	}
	if pinErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		record, recordErr := client.CoreV1().ConfigMaps("kube-system").Get(ctx, recordName, metav1.GetOptions{})
		if recordErr != nil {
			return recordErr
		}
		if record.Data["tokenID"] != tokenID || record.Data["claimUID"] != string(claim.UID) || record.Data["clusterUID"] != clusterUID || record.Data["version"] != "1" {
			return fmt.Errorf("bootstrap credential record changed during issuance")
		}
		if uid := record.Data["secretUID"]; uid != "" {
			if uid != string(secret.UID) {
				return fmt.Errorf("bootstrap credential Secret was replaced")
			}
			return nil
		}
		record.Data["secretUID"] = string(secret.UID)
		_, updateErr := client.CoreV1().ConfigMaps("kube-system").Update(ctx, record, metav1.UpdateOptions{})
		return updateErr
	}); pinErr != nil {
		return "", pinErr
	}
	if bindingErr := ensureBootstrapStatusRBAC(ctx, client, claim, recordName, tokenID); bindingErr != nil {
		return "", bindingErr
	}
	record, err := client.CoreV1().ConfigMaps("kube-system").Get(ctx, recordName, metav1.GetOptions{})
	if err != nil || record.Data["tokenID"] != tokenID {
		return "", fmt.Errorf("bootstrap credential changed while being issued")
	}
	return tokenID + "." + string(secret.Data["token-secret"]), nil
}

func ensureBootstrapStatusRBAC(ctx context.Context, client kubernetes.Interface, claim *karpv1.NodeClaim, recordName, tokenID string) error {
	owner := *metav1.NewControllerRef(claim, bootstrapClaimGroupVersion.WithKind("NodeClaim"))
	rules := []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{recordName}, Verbs: []string{"get", "patch"}}}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: recordName, Namespace: "kube-system", OwnerReferences: []metav1.OwnerReference{owner}}, Rules: rules}
	_, err := client.RbacV1().Roles("kube-system").Create(ctx, role, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		role, err = client.RbacV1().Roles("kube-system").Get(ctx, recordName, metav1.GetOptions{})
	}
	if err != nil {
		return fmt.Errorf("creating bootstrap status role: %w", err)
	}
	if len(role.OwnerReferences) != 1 || !reflect.DeepEqual(role.OwnerReferences[0], owner) || !reflect.DeepEqual(role.Rules, rules) {
		return fmt.Errorf("bootstrap status role has different ownership or permissions")
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		subject := rbacv1.Subject{Kind: "User", APIGroup: rbacv1.GroupName, Name: "system:bootstrap:" + tokenID}
		binding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: recordName, Namespace: "kube-system", OwnerReferences: []metav1.OwnerReference{owner}},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: recordName}, Subjects: []rbacv1.Subject{subject},
		}
		_, err := client.RbacV1().RoleBindings("kube-system").Create(ctx, binding, metav1.CreateOptions{})
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing, err := client.RbacV1().RoleBindings("kube-system").Get(ctx, recordName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if len(existing.OwnerReferences) != 1 || !reflect.DeepEqual(existing.OwnerReferences[0], owner) || existing.RoleRef != binding.RoleRef ||
			len(existing.Subjects) != 1 || existing.Subjects[0].Kind != "User" || existing.Subjects[0].APIGroup != rbacv1.GroupName ||
			!strings.HasPrefix(existing.Subjects[0].Name, "system:bootstrap:") ||
			!tokenIDPattern.MatchString(strings.TrimPrefix(existing.Subjects[0].Name, "system:bootstrap:")) {
			return fmt.Errorf("bootstrap status binding has different ownership")
		}
		if existing.Subjects[0] == subject {
			return nil
		}
		existing.Subjects = binding.Subjects
		_, err = client.RbacV1().RoleBindings("kube-system").Update(ctx, existing, metav1.UpdateOptions{})
		return err
	})
}
