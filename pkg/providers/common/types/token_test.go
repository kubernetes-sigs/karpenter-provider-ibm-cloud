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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGenerateBootstrapToken(t *testing.T) {
	tests := []struct {
		name        string
		ttl         time.Duration
		expectError bool
	}{
		{
			name: "successful token generation with TTL",
			ttl:  24 * time.Hour,
		},
		{
			name: "successful token generation without TTL",
			ttl:  0,
		},
		{
			name: "successful token generation with short TTL",
			ttl:  time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
			client := fake.NewSimpleClientset()

			token, err := GenerateBootstrapToken(context.Background(), client, tt.ttl)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, token)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, token)

				// Verify token format (should be tokenID.tokenSecret)
				parts := strings.Split(token, ".")
				assert.Len(t, parts, 2, "Token should have format tokenID.tokenSecret")
				assert.Len(t, parts[0], 6, "Token ID should be 6 characters")
				assert.Len(t, parts[1], 16, "Token secret should be 16 characters")

				// Verify secret was created
				secretName := fmt.Sprintf("bootstrap-token-%s", parts[0])
				secret, err := client.CoreV1().Secrets("kube-system").Get(context.Background(), secretName, metav1.GetOptions{})
				assert.NoError(t, err)
				assert.Equal(t, "bootstrap.kubernetes.io/token", string(secret.Type))

				// Verify secret data
				assert.Equal(t, parts[0], string(secret.Data["token-id"]))
				assert.Equal(t, parts[1], string(secret.Data["token-secret"]))
				assert.Equal(t, "true", string(secret.Data["usage-bootstrap-authentication"]))
				assert.Equal(t, "true", string(secret.Data["usage-bootstrap-signing"]))
				assert.Equal(t, "system:bootstrappers:karpenter:ibm-cloud", string(secret.Data["auth-extra-groups"]))

				// Check expiration if TTL was provided
				if tt.ttl > 0 {
					assert.Contains(t, secret.Data, "expiration")
					expirationStr := string(secret.Data["expiration"])
					expiration, err := time.Parse(time.RFC3339, expirationStr)
					assert.NoError(t, err)
					assert.True(t, expiration.After(time.Now()))
				} else {
					assert.NotContains(t, secret.Data, "expiration")
				}
			}
		})
	}
}

func TestFindOrCreateBootstrapToken(t *testing.T) {
	tests := []struct {
		name           string
		existingTokens []runtime.Object
		ttl            time.Duration
		expectNewToken bool
		expectError    bool
	}{
		{
			name:           "no existing tokens - create new",
			ttl:            24 * time.Hour,
			expectNewToken: true,
		},
		{
			name: "valid existing token found",
			existingTokens: []runtime.Object{
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
						"token-id":     []byte("abc123"),
						"token-secret": []byte("def456789abcdef0"),
						"expiration":   []byte(time.Now().Add(2 * time.Hour).Format(time.RFC3339)),
					},
				},
			},
			ttl:            24 * time.Hour,
			expectNewToken: false,
		},
		{
			name: "expired token - create new",
			existingTokens: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "bootstrap-token-expired",
						Namespace: "kube-system",
						Labels: map[string]string{
							"karpenter.sh/bootstrap-token": "true",
						},
					},
					Type: "bootstrap.kubernetes.io/token",
					Data: map[string][]byte{
						"token-id":     []byte("expire"),
						"token-secret": []byte("d123456789abcdef"),
						"expiration":   []byte(time.Now().Add(-time.Hour).Format(time.RFC3339)),
					},
				},
			},
			ttl:            24 * time.Hour,
			expectNewToken: true,
		},
		{
			name: "token expiring soon - create new",
			existingTokens: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "bootstrap-token-expiring",
						Namespace: "kube-system",
						Labels: map[string]string{
							"karpenter.sh/bootstrap-token": "true",
						},
					},
					Type: "bootstrap.kubernetes.io/token",
					Data: map[string][]byte{
						"token-id":     []byte("expir1"),
						"token-secret": []byte("123456789abcdef0"),
						"expiration":   []byte(time.Now().Add(30 * time.Minute).Format(time.RFC3339)),
					},
				},
			},
			ttl:            24 * time.Hour,
			expectNewToken: true,
		},
		{
			name: "invalid token type - create new",
			existingTokens: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "bootstrap-token-invalid",
						Namespace: "kube-system",
						Labels: map[string]string{
							"karpenter.sh/bootstrap-token": "true",
						},
					},
					Type: "Opaque",
					Data: map[string][]byte{
						"token-id":     []byte("inval1"),
						"token-secret": []byte("123456789abcdef0"),
					},
				},
			},
			ttl:            24 * time.Hour,
			expectNewToken: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
			client := fake.NewSimpleClientset(tt.existingTokens...)

			initialSecrets, _ := client.CoreV1().Secrets("kube-system").List(context.Background(), metav1.ListOptions{})
			initialCount := len(initialSecrets.Items)

			token, err := FindOrCreateBootstrapToken(context.Background(), client, tt.ttl)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, token)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, token)

				// Verify token format
				parts := strings.Split(token, ".")
				assert.Len(t, parts, 2, "Token should have format tokenID.tokenSecret")

				finalSecrets, _ := client.CoreV1().Secrets("kube-system").List(context.Background(), metav1.ListOptions{})
				finalCount := len(finalSecrets.Items)

				if tt.expectNewToken {
					assert.Greater(t, finalCount, initialCount, "New token should have been created")
				} else {
					assert.Equal(t, initialCount, finalCount, "No new token should have been created")
				}
			}
		})
	}
}

func TestGetInternalAPIServerEndpoint(t *testing.T) {
	tests := []struct {
		name             string
		k8sObjects       []runtime.Object
		expectedEndpoint string
		expectError      bool
	}{
		{
			name: "endpoint from kubeadm-config",
			k8sObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeadm-config",
						Namespace: "kube-system",
					},
					Data: map[string]string{
						"ClusterConfiguration": `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
controlPlaneEndpoint: api.example.com:6443
networking:
  serviceSubnet: 10.96.0.0/12`,
					},
				},
			},
			expectedEndpoint: "https://api.example.com:6443",
		},
		{
			name: "endpoint from cluster-info fallback",
			k8sObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster-info",
						Namespace: "kube-public",
					},
					Data: map[string]string{
						"kubeconfig": `apiVersion: v1
clusters:
- cluster:
    server: https://api.cluster.local:6443
  name: cluster`,
					},
				},
			},
			expectedEndpoint: "https://api.cluster.local:6443",
		},
		{
			name: "invalid kubeadm-config, valid cluster-info",
			k8sObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeadm-config",
						Namespace: "kube-system",
					},
					Data: map[string]string{
						"ClusterConfiguration": "invalid yaml content",
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cluster-info",
						Namespace: "kube-public",
					},
					Data: map[string]string{
						"kubeconfig": `apiVersion: v1
clusters:
- cluster:
    server: https://fallback.example.com:6443
  name: cluster`,
					},
				},
			},
			expectedEndpoint: "https://fallback.example.com:6443",
		},
		{
			name:        "no config maps found - should error",
			k8sObjects:  []runtime.Object{},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
			client := fake.NewSimpleClientset(tt.k8sObjects...)

			endpoint, err := GetInternalAPIServerEndpoint(context.Background(), client)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, endpoint)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedEndpoint, endpoint)
			}
		})
	}
}

func TestParseKubeadmConfigEndpoint(t *testing.T) {
	tests := []struct {
		name             string
		clusterConfig    string
		expectedEndpoint string
		expectError      bool
	}{
		{
			name: "valid kubeadm config",
			clusterConfig: `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
controlPlaneEndpoint: api.example.com:6443
networking:
  serviceSubnet: 10.96.0.0/12`,
			expectedEndpoint: "https://api.example.com:6443",
		},
		{
			name: "kubeadm config with spaces",
			clusterConfig: `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
controlPlaneEndpoint:   api-with-spaces.com:6443
networking:
  serviceSubnet: 10.96.0.0/12`,
			expectedEndpoint: "https://api-with-spaces.com:6443",
		},
		{
			name: "kubeadm config with IP address",
			clusterConfig: `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
controlPlaneEndpoint: 192.168.1.100:6443
networking:
  serviceSubnet: 10.96.0.0/12`,
			expectedEndpoint: "https://192.168.1.100:6443",
		},
		{
			name:          "empty config",
			clusterConfig: "",
			expectError:   true,
		},
		{
			name: "config without controlPlaneEndpoint",
			clusterConfig: `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
networking:
  serviceSubnet: 10.96.0.0/12`,
			expectError: true,
		},
		{
			name: "malformed controlPlaneEndpoint line",
			clusterConfig: `apiVersion: kubeadm.k8s.io/v1beta3
kind: ClusterConfiguration
controlPlaneEndpoint: invalid-format
networking:
  serviceSubnet: 10.96.0.0/12`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint, err := parseKubeadmConfigEndpoint(tt.clusterConfig)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, endpoint)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedEndpoint, endpoint)
			}
		})
	}
}

func TestParseClusterInfoEndpoint(t *testing.T) {
	tests := []struct {
		name             string
		kubeconfig       string
		expectedEndpoint string
		expectError      bool
	}{
		{
			name: "valid kubeconfig",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    server: https://api.example.com:6443
  name: cluster`,
			expectedEndpoint: "https://api.example.com:6443",
		},
		{
			name: "kubeconfig with spaces",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    server:   https://api-spaces.com:6443
  name: cluster`,
			expectedEndpoint: "https://api-spaces.com:6443",
		},
		{
			name: "kubeconfig with IP",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    server: https://10.0.0.1:6443
  name: cluster`,
			expectedEndpoint: "https://10.0.0.1:6443",
		},
		{
			name:        "empty kubeconfig",
			kubeconfig:  "",
			expectError: true,
		},
		{
			name: "kubeconfig without server",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: LS0tLS1CRUdJTi0=
  name: cluster`,
			expectError: true,
		},
		{
			name: "kubeconfig with empty server",
			kubeconfig: `apiVersion: v1
clusters:
- cluster:
    server:
  name: cluster`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint, err := parseClusterInfoEndpoint(tt.kubeconfig)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, endpoint)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedEndpoint, endpoint)
			}
		})
	}
}

func TestGenerateRandomString(t *testing.T) {
	tests := []struct {
		name           string
		length         int
		expectedLength int
	}{
		{
			name:           "6 character string",
			length:         6,
			expectedLength: 6,
		},
		{
			name:           "16 character string",
			length:         16,
			expectedLength: 16,
		},
		{
			name:           "odd length string",
			length:         7,
			expectedLength: 7,
		},
		{
			name:           "single character",
			length:         1,
			expectedLength: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := generateRandomString(tt.length)

			assert.NoError(t, err)
			assert.Len(t, result, tt.expectedLength)

			// Verify it's hexadecimal
			for _, char := range result {
				assert.True(t, (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f'),
					"Character %c should be hexadecimal", char)
			}

			// Generate another string and verify they're different
			// For single character, allow collision due to small character space
			result2, err := generateRandomString(tt.length)
			assert.NoError(t, err)
			if tt.length > 1 {
				assert.NotEqual(t, result, result2, "Two random strings should be different")
			}
		})
	}
}

func TestGenerateRandomStringEdgeCases(t *testing.T) {
	t.Run("zero length", func(t *testing.T) {
		result, err := generateRandomString(0)
		assert.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("very long string", func(t *testing.T) {
		result, err := generateRandomString(100)
		assert.NoError(t, err)
		assert.Len(t, result, 100)
	})
}

func TestBootstrapTokenIntegration(t *testing.T) {
	t.Run("end-to-end token lifecycle", func(t *testing.T) {
		//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
		client := fake.NewSimpleClientset()
		ctx := context.Background()
		ttl := 24 * time.Hour

		// Generate initial token
		token1, err := GenerateBootstrapToken(ctx, client, ttl)
		require.NoError(t, err)
		require.NotEmpty(t, token1)

		// Find or create should return existing token if valid
		token2, err := FindOrCreateBootstrapToken(ctx, client, ttl)
		require.NoError(t, err)

		// Should create a new token since the generated one doesn't have the required label
		assert.NotEqual(t, token1, token2)

		// Create a properly labeled token
		parts := strings.Split(token2, ".")
		require.Len(t, parts, 2)

		// Add the label to make it discoverable
		secretName := fmt.Sprintf("bootstrap-token-%s", parts[0])
		secret, err := client.CoreV1().Secrets("kube-system").Get(ctx, secretName, metav1.GetOptions{})
		require.NoError(t, err)

		secret.Labels = map[string]string{
			"karpenter.sh/bootstrap-token": "true",
		}
		_, err = client.CoreV1().Secrets("kube-system").Update(ctx, secret, metav1.UpdateOptions{})
		require.NoError(t, err)

		// Now FindOrCreate should find the existing token
		token3, err := FindOrCreateBootstrapToken(ctx, client, ttl)
		require.NoError(t, err)
		assert.Equal(t, token2, token3)
	})
}
