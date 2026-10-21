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

package bootstrap

//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;create;delete,namespace=kube-system
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;create;update;patch;delete,namespace=kube-system
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=create
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,resourceNames=karpenter-ibm-bootstrap-nodes;karpenter-ibm-auto-approve-renewals;karpenter-ibm-auto-approve-csrs;karpenter-ibm-auto-approve-kubelet-serving-csrs,verbs=get;update;patch;delete
//+kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// Binding system:node-bootstrapper and the nodeclient roles requires holding their permissions.
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;list;watch
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/nodeclient;certificatesigningrequests/selfnodeclient,verbs=create

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

type TokenController struct {
	client   kubernetes.Interface
	reader   client.Reader
	recorder record.EventRecorder
	observed map[k8stypes.UID]string
}

var bootstrapPhasePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)

func NewTokenController(mgr manager.Manager) *TokenController {
	return &TokenController{
		client: kubernetes.NewForConfigOrDie(mgr.GetConfig()), reader: mgr.GetAPIReader(),
		recorder: eventRecorder{mgr.GetEventRecorder("bootstrap")}, observed: map[k8stypes.UID]string{},
	}
}

func (c *TokenController) SetupWithManager(mgr manager.Manager) error {
	return mgr.Add(c)
}

func (*TokenController) NeedLeaderElection() bool { return true }

func (c *TokenController) Start(ctx context.Context) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if _, err := c.Reconcile(ctx, reconcile.Request{}); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.FromContext(ctx).Error(err, "Bootstrap maintenance failed")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (c *TokenController) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	if err := c.ensureBootstrapRBAC(ctx, logger); err != nil {
		return reconcile.Result{}, err
	}
	if err := c.cleanupExpiredTokens(ctx, logger); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
}

func (c *TokenController) ensureBootstrapRBAC(ctx context.Context, _ logr.Logger) error {
	for _, legacy := range []struct{ name, role, group string }{
		{"karpenter-ibm-auto-approve-csrs", "system:certificates.k8s.io:certificatesigningrequests:nodeclient", commonTypes.BootstrapGroup},
		{"karpenter-ibm-auto-approve-kubelet-serving-csrs", "system:certificates.k8s.io:kubelet-serving-approver", "system:nodes"},
	} {
		if err := c.removeLegacySubject(ctx, legacy.name, legacy.role, legacy.group); err != nil {
			return err
		}
	}
	for _, binding := range []struct{ name, role, group string }{
		{"karpenter-ibm-bootstrap-nodes", "system:node-bootstrapper", commonTypes.BootstrapGroup},
		{"karpenter-ibm-auto-approve-renewals", "system:certificates.k8s.io:certificatesigningrequests:selfnodeclient", "system:nodes"},
	} {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			expected := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: binding.name},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: binding.role},
				Subjects:   []rbacv1.Subject{{Kind: "Group", APIGroup: rbacv1.GroupName, Name: binding.group}},
			}
			_, err := c.client.RbacV1().ClusterRoleBindings().Create(ctx, expected, metav1.CreateOptions{})
			if !apierrors.IsAlreadyExists(err) {
				return err
			}
			existing, err := c.client.RbacV1().ClusterRoleBindings().Get(ctx, binding.name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if existing.RoleRef != expected.RoleRef {
				return fmt.Errorf("bootstrap binding %s has unexpected role", binding.name)
			}
			for _, subject := range existing.Subjects {
				if subject == expected.Subjects[0] {
					return nil
				}
			}
			existing.Subjects = append(existing.Subjects, expected.Subjects[0])
			_, err = c.client.RbacV1().ClusterRoleBindings().Update(ctx, existing, metav1.UpdateOptions{})
			return err
		}); err != nil {
			return fmt.Errorf("ensuring bootstrap binding: %w", err)
		}
	}
	return nil
}

func (c *TokenController) removeLegacySubject(ctx context.Context, name, role, group string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		binding, err := c.client.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		remaining := make([]rbacv1.Subject, 0, len(binding.Subjects))
		for _, subject := range binding.Subjects {
			if subject.Kind == "Group" && subject.Name == group && subject.APIGroup == rbacv1.GroupName {
				continue
			}
			remaining = append(remaining, subject)
		}
		if len(remaining) == len(binding.Subjects) {
			return nil
		}
		if binding.RoleRef != (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}) {
			return fmt.Errorf("legacy bootstrap binding %s has an unexpected role; reconcile its provider subject explicitly", name)
		}
		if len(remaining) == 0 {
			err = c.client.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &binding.UID, ResourceVersion: &binding.ResourceVersion}})
		} else {
			binding.Subjects = remaining
			_, err = c.client.RbacV1().ClusterRoleBindings().Update(ctx, binding, metav1.UpdateOptions{})
		}
		return client.IgnoreNotFound(err)
	})
}

func (c *TokenController) cleanupExpiredTokens(ctx context.Context, logger logr.Logger) error {
	secrets, err := c.client.CoreV1().Secrets("kube-system").List(ctx, metav1.ListOptions{LabelSelector: commonTypes.BootstrapTokenLabel + "=true"})
	if err != nil {
		return fmt.Errorf("listing bootstrap tokens: %w", err)
	}
	claims := map[string]*karpv1.NodeClaim{}
	claimUIDs := map[k8stypes.UID]bool{}
	var clusterUID string
	if c.reader != nil {
		list := &karpv1.NodeClaimList{}
		if listErr := c.reader.List(ctx, list); listErr != nil {
			return listErr
		}
		for i := range list.Items {
			claims[list.Items[i].Name] = &list.Items[i]
			claimUIDs[list.Items[i].UID] = true
		}
		clusterUID, err = ownership.ClusterUID(ctx, c.reader)
		if err != nil {
			return err
		}
	}
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		if secret.Type != corev1.SecretTypeBootstrapToken {
			continue
		}
		expires, parseErr := time.Parse(time.RFC3339, string(secret.Data["expiration"]))
		expired := parseErr == nil && !time.Now().Before(expires)
		remove := false
		if secret.Labels[ownership.ClaimUIDLabel] == "" && secret.Labels[ownership.ClusterUIDLabel] == "" && len(secret.OwnerReferences) == 0 {
			remove = expired && string(secret.Data["auth-extra-groups"]) == commonTypes.BootstrapGroup &&
				secret.Name == "bootstrap-token-"+string(secret.Data["token-id"])
		} else if c.reader != nil && secret.Labels[ownership.ClusterUIDLabel] == clusterUID {
			identity := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{
				Name: secret.Annotations[commonTypes.BootstrapClaimAnnotation], UID: k8stypes.UID(secret.Labels[ownership.ClaimUIDLabel]),
			}}
			if commonTypes.BootstrapTokenOwnedBy(secret, identity, clusterUID) {
				claim := claims[identity.Name]
				remove = expired || claim == nil || claim.UID != identity.UID || !claim.DeletionTimestamp.IsZero() ||
					claim.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue()
			}
		}
		if remove {
			if err := c.client.CoreV1().Secrets("kube-system").Delete(ctx, secret.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &secret.UID, ResourceVersion: &secret.ResourceVersion}}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("revoking bootstrap token: %w", err)
			}
		}
	}
	if c.recorder == nil {
		return nil
	}
	if c.observed == nil {
		c.observed = map[k8stypes.UID]string{}
	}
	for _, claim := range claims {
		if !claim.DeletionTimestamp.IsZero() || claim.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue() {
			continue
		}
		record := &corev1.ConfigMap{}
		if err := c.reader.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "karpenter-bootstrap-" + string(claim.UID)}, record); err != nil {
			if !apierrors.IsNotFound(err) {
				logger.Error(err, "Reading bootstrap status", "nodeClaim", claim.Name)
			}
			continue
		}
		phase := record.Data["phase"]
		if record.Data["claimUID"] != string(claim.UID) || record.Data["clusterUID"] != clusterUID || record.Data["status"] != "failed" ||
			!bootstrapPhasePattern.MatchString(phase) {
			continue
		}
		if c.observed[claim.UID] != phase {
			c.recorder.Eventf(claim, corev1.EventTypeWarning, "BootstrapFailed", "Worker bootstrap failed during %s; inspect ConfigMap kube-system/%s", phase, record.Name)
			c.observed[claim.UID] = phase
		}
	}
	for uid := range c.observed {
		if !claimUIDs[uid] {
			delete(c.observed, uid)
		}
	}
	return nil
}
