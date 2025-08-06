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

//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;watch;create;update;patch
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=get;list;watch;create
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/nodeclient,verbs=create
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/selfnodeclient,verbs=create

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// TokenController manages bootstrap tokens for node registration
type TokenController struct {
	client kubernetes.Interface
}

// NewTokenController creates a new bootstrap token controller
func NewTokenController(mgr manager.Manager) *TokenController {
	return &TokenController{
		client: kubernetes.NewForConfigOrDie(mgr.GetConfig()),
	}
}

// SetupWithManager sets up the controller with the Manager
func (c *TokenController) SetupWithManager(mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}).
		Complete(c)
}

// Reconcile ensures bootstrap RBAC and manages token lifecycle
func (c *TokenController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling bootstrap tokens", "request", req)

	// Ensure bootstrap RBAC exists
	if err := c.ensureBootstrapRBAC(ctx); err != nil {
		logger.Error(err, "Failed to ensure bootstrap RBAC")
		return reconcile.Result{}, err
	}

	// Clean up expired tokens
	if err := c.cleanupExpiredTokens(ctx); err != nil {
		logger.Error(err, "Failed to cleanup expired tokens")
		return reconcile.Result{}, err
	}

	// Requeue every 5 minutes to check for expired tokens
	return reconcile.Result{RequeueAfter: 5 * time.Minute}, nil
}

// ensureBootstrapRBAC creates the necessary RBAC permissions for bootstrap tokens
func (c *TokenController) ensureBootstrapRBAC(ctx context.Context) error {
	logger := log.FromContext(ctx)

	// Create ClusterRoleBinding for node bootstrapping
	_, err := c.client.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "karpenter-ibm-bootstrap-nodes",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "Group",
				Name:     "system:bootstrappers:karpenter:ibm-cloud",
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:node-bootstrapper",
		},
	}, metav1.CreateOptions{})

	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating bootstrap nodes ClusterRoleBinding: %w", err)
	}

	// Auto-approve CSRs for bootstrap tokens
	_, err = c.client.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "karpenter-ibm-auto-approve-csrs",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "Group",
				Name:     "system:bootstrappers:karpenter:ibm-cloud",
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:certificates.k8s.io:certificatesigningrequests:nodeclient",
		},
	}, metav1.CreateOptions{})

	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating auto-approve CSRs ClusterRoleBinding: %w", err)
	}

	// Create ClusterRoleBinding for renewal
	_, err = c.client.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "karpenter-ibm-auto-approve-renewals",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "Group",
				Name:     "system:nodes",
				APIGroup: "rbac.authorization.k8s.io",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:certificates.k8s.io:certificatesigningrequests:selfnodeclient",
		},
	}, metav1.CreateOptions{})

	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating auto-approve renewals ClusterRoleBinding: %w", err)
	}

	logger.Info("Bootstrap RBAC permissions ensured")
	return nil
}

// cleanupExpiredTokens removes expired bootstrap tokens
func (c *TokenController) cleanupExpiredTokens(ctx context.Context) error {
	logger := log.FromContext(ctx)

	// List all bootstrap token secrets
	secrets, err := c.client.CoreV1().Secrets("kube-system").List(ctx, metav1.ListOptions{
		LabelSelector: "karpenter.sh/bootstrap-token=true",
	})
	if err != nil {
		return fmt.Errorf("listing bootstrap tokens: %w", err)
	}

	cleaned := 0
	for _, secret := range secrets.Items {
		if secret.Type == "bootstrap.kubernetes.io/token" {
			// Check if token is expired
			if expirationBytes, exists := secret.Data["expiration"]; exists {
				if expiration, parseErr := time.Parse(time.RFC3339, string(expirationBytes)); parseErr == nil {
					if time.Now().After(expiration) {
						// Token is expired, delete it
						if err := c.client.CoreV1().Secrets("kube-system").Delete(ctx, secret.Name, metav1.DeleteOptions{}); err != nil {
							logger.Error(err, "Failed to delete expired token", "token", secret.Name)
						} else {
							logger.Info("Deleted expired bootstrap token", "token", secret.Name)
							cleaned++
						}
					}
				}
			}
		}
	}

	if cleaned > 0 {
		logger.Info("Cleaned up expired bootstrap tokens", "count", cleaned)
	}

	return nil
}

// CreateBootstrapToken creates a new bootstrap token with proper RBAC groups
func (c *TokenController) CreateBootstrapToken(ctx context.Context, name string) (string, error) {
	tokenID := generateRandomString(6)
	tokenSecret := generateRandomString(16)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("bootstrap-token-%s", tokenID),
			Namespace: "kube-system",
			Labels: map[string]string{
				"karpenter.sh/bootstrap-token": "true",
			},
		},
		Type: "bootstrap.kubernetes.io/token",
		StringData: map[string]string{
			"token-id":                       tokenID,
			"token-secret":                   tokenSecret,
			"usage-bootstrap-authentication": "true",
			"usage-bootstrap-signing":        "true",
			"auth-extra-groups":              "system:bootstrappers:karpenter:ibm-cloud",
			"description":                    fmt.Sprintf("Token for Karpenter node %s", name),
			"ttl":                            "24h",
		},
	}

	_, err := c.client.CoreV1().Secrets("kube-system").Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s.%s", tokenID, tokenSecret), nil
}

// generateRandomString generates a random hex string of specified length
func generateRandomString(length int) string {
	bytes := make([]byte, length/2)
	if _, err := rand.Read(bytes); err != nil {
		return ""
	}
	return hex.EncodeToString(bytes)[:length]
}
