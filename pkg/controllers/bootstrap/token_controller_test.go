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

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestTokenController_ensureBootstrapRBAC(t *testing.T) {
	tests := []struct {
		name          string
		existingRBAC  []runtime.Object
		expectError   bool
		expectCreated bool
	}{
		{
			name:          "creates new RBAC when none exists",
			existingRBAC:  []runtime.Object{},
			expectError:   false,
			expectCreated: true,
		},
		{
			name: "handles existing RBAC gracefully",
			existingRBAC: []runtime.Object{
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "karpenter-ibm-bootstrap-nodes",
					},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "karpenter-ibm-auto-approve-csrs",
					},
				},
				&rbacv1.ClusterRoleBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "karpenter-ibm-auto-approve-renewals",
					},
				},
			},
			expectError:   false,
			expectCreated: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake kubernetes client
			//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
			client := fake.NewSimpleClientset(tt.existingRBAC...)

			controller := &TokenController{
				client: client,
			}

			ctx := log.IntoContext(context.Background(), log.Log)
			err := controller.ensureBootstrapRBAC(ctx, log.Log)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			// Verify ClusterRoleBindings exist
			expectedBindings := []string{
				"karpenter-ibm-bootstrap-nodes",
				"karpenter-ibm-auto-approve-csrs",
				"karpenter-ibm-auto-approve-renewals",
			}

			for _, bindingName := range expectedBindings {
				binding, err := client.RbacV1().ClusterRoleBindings().Get(ctx, bindingName, metav1.GetOptions{})
				require.NoError(t, err, "ClusterRoleBinding %s should exist", bindingName)
				assert.NotNil(t, binding)
			}
		})
	}
}

func TestTokenController_CreateBootstrapToken(t *testing.T) {
	tests := []struct {
		name        string
		nodeName    string
		expectError bool
	}{
		{
			name:        "creates valid bootstrap token",
			nodeName:    "test-node-1",
			expectError: false,
		},
		{
			name:        "creates token with empty node name",
			nodeName:    "",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
			client := fake.NewSimpleClientset()
			controller := &TokenController{
				client: client,
			}

			ctx := context.Background()
			token, err := controller.CreateBootstrapToken(ctx, tt.nodeName)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, token)

			// Token should have format: tokenID.tokenSecret
			assert.Contains(t, token, ".")
			parts := strings.Split(token, ".")
			assert.Len(t, parts, 2)
			assert.Len(t, parts[0], 6)  // tokenID should be 6 chars
			assert.Len(t, parts[1], 16) // tokenSecret should be 16 chars

			// Verify secret was created
			secrets, err := client.CoreV1().Secrets("kube-system").List(ctx, metav1.ListOptions{
				LabelSelector: "karpenter.sh/bootstrap-token=true",
			})
			require.NoError(t, err)
			assert.Len(t, secrets.Items, 1)

			secret := secrets.Items[0]
			assert.Equal(t, "bootstrap.kubernetes.io/token", string(secret.Type))
			assert.Equal(t, parts[0], secret.StringData["token-id"])
			assert.Equal(t, parts[1], secret.StringData["token-secret"])
			assert.Equal(t, "true", secret.StringData["usage-bootstrap-authentication"])
			assert.Equal(t, "true", secret.StringData["usage-bootstrap-signing"])
			assert.Equal(t, "system:bootstrappers:karpenter:ibm-cloud", secret.StringData["auth-extra-groups"])
		})
	}
}

func TestTokenController_cleanupExpiredTokens(t *testing.T) {
	tests := []struct {
		name            string
		existingSecrets []runtime.Object
		expectDeleted   int
	}{
		{
			name:            "no tokens to cleanup",
			existingSecrets: []runtime.Object{},
			expectDeleted:   0,
		},
		{
			name: "cleans up expired tokens",
			existingSecrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "bootstrap-token-abc123",
						Namespace: "kube-system",
						Labels: map[string]string{
							"karpenter.sh/bootstrap-token": "true",
						},
					},
					Type: "bootstrap.kubernetes.io/token",
					Data: map[string][]byte{
						"expiration": []byte(time.Now().Add(-1 * time.Hour).Format(time.RFC3339)),
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "bootstrap-token-def456",
						Namespace: "kube-system",
						Labels: map[string]string{
							"karpenter.sh/bootstrap-token": "true",
						},
					},
					Type: "bootstrap.kubernetes.io/token",
					Data: map[string][]byte{
						"expiration": []byte(time.Now().Add(1 * time.Hour).Format(time.RFC3339)),
					},
				},
			},
			expectDeleted: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
			client := fake.NewSimpleClientset(tt.existingSecrets...)
			controller := &TokenController{
				client: client,
			}

			ctx := log.IntoContext(context.Background(), log.Log)
			err := controller.cleanupExpiredTokens(ctx, log.Log)
			assert.NoError(t, err)

			// Verify expected number of secrets remain
			secrets, err := client.CoreV1().Secrets("kube-system").List(ctx, metav1.ListOptions{
				LabelSelector: "karpenter.sh/bootstrap-token=true",
			})
			require.NoError(t, err)

			expectedRemaining := len(tt.existingSecrets) - tt.expectDeleted
			assert.Len(t, secrets.Items, expectedRemaining)
		})
	}
}

func TestTokenController_Reconcile(t *testing.T) {
	//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
	client := fake.NewSimpleClientset()
	controller := &TokenController{
		client: client,
	}

	ctx := log.IntoContext(context.Background(), log.Log)
	req := reconcile.Request{}

	result, err := controller.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, 5*time.Minute, result.RequeueAfter)

	// Verify RBAC was created
	bindings, err := client.RbacV1().ClusterRoleBindings().List(ctx, metav1.ListOptions{})
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(bindings.Items), 3) // Should have 3 ClusterRoleBindings
}

func TestGenerateRandomString(t *testing.T) {
	tests := []struct {
		name     string
		length   int
		expected int
	}{
		{
			name:     "generates 6 character string",
			length:   6,
			expected: 6,
		},
		{
			name:     "generates 16 character string",
			length:   16,
			expected: 16,
		},
		{
			name:     "generates empty string for zero length",
			length:   0,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateRandomString(tt.length)
			assert.Len(t, result, tt.expected)

			if tt.length > 0 {
				// Should be hex characters only
				for _, char := range result {
					assert.True(t, (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f'),
						"Character %c should be hex", char)
				}
			}
		})
	}
}
