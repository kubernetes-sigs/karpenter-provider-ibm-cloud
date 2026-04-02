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
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestNewIKSBootstrapProvider(t *testing.T) {
	tests := []struct {
		name      string
		k8sClient bool
		ibmClient bool
	}{
		{
			name:      "nil client and nil k8sClient",
			k8sClient: false,
			ibmClient: false,
		},
		{
			name:      "nil client with k8sClient",
			k8sClient: true,
			ibmClient: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var k8s *fake.Clientset
			if tt.k8sClient {
				//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
				k8s = fake.NewSimpleClientset()
			}

			provider := NewIKSBootstrapProvider(nil, k8s)

			assert.NotNil(t, provider)
			assert.NotNil(t, provider.httpClient, "httpClient should be initialized by constructor")
			assert.Nil(t, provider.client, "client should be nil when nil is passed")

			if tt.k8sClient {
				assert.NotNil(t, provider.k8sClient)
			} else {
				assert.Nil(t, provider.k8sClient)
			}
		})
	}
}

func TestSetIKSHeaders(t *testing.T) {
	provider := NewIKSBootstrapProvider(nil, nil)

	tests := []struct {
		name  string
		token string
	}{
		{
			name:  "standard token",
			token: "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.test",
		},
		{
			name:  "empty token",
			token: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "https://example.com/test", nil)
			assert.NoError(t, err)

			provider.setIKSHeaders(req, tt.token)

			assert.Equal(t, "Bearer "+tt.token, req.Header.Get("Authorization"))
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, "application/json", req.Header.Get("Accept"))
			assert.Equal(t, "karpenter-provider-ibm-cloud/1.0", req.Header.Get("User-Agent"))
		})
	}
}

func TestSetIKSHeaders_Overwrite(t *testing.T) {
	provider := NewIKSBootstrapProvider(nil, nil)

	req, err := http.NewRequest(http.MethodPost, "https://example.com/test", nil)
	assert.NoError(t, err)

	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Basic old")

	provider.setIKSHeaders(req, "new-token")

	assert.Equal(t, "Bearer new-token", req.Header.Get("Authorization"))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
}

func TestGetUserDataWithInstanceID(t *testing.T) {
	//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
	provider := NewIKSBootstrapProvider(nil, fake.NewSimpleClientset())
	ctx := context.Background()

	tests := []struct {
		name       string
		nodeClass  *v1alpha1.IBMNodeClass
		instanceID string
		contains   string
		notContain string
	}{
		{
			name: "custom user data ignores instance ID",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: v1alpha1.IBMNodeClassSpec{
					UserData: "#!/bin/bash\necho 'custom'",
				},
			},
			instanceID: "i-abc123",
			contains:   "custom",
			notContain: "IKS node provisioned",
		},
		{
			name: "default user data ignores instance ID",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec:       v1alpha1.IBMNodeClassSpec{},
			},
			instanceID: "i-def456",
			contains:   "IKS node provisioned by Karpenter",
		},
		{
			name: "empty instance ID",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec:       v1alpha1.IBMNodeClassSpec{},
			},
			instanceID: "",
			contains:   "IKS node provisioned by Karpenter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeClaim := types.NamespacedName{Name: "test-claim", Namespace: "default"}

			result, err := provider.GetUserDataWithInstanceID(ctx, tt.nodeClass, nodeClaim, tt.instanceID)
			assert.NoError(t, err)
			assert.Contains(t, result, tt.contains)
			if tt.notContain != "" {
				assert.NotContains(t, result, tt.notContain)
			}

			// Verify it returns the same result as GetUserData (instance ID is ignored)
			directResult, directErr := provider.GetUserData(ctx, tt.nodeClass, nodeClaim)
			assert.NoError(t, directErr)
			assert.Equal(t, directResult, result, "GetUserDataWithInstanceID should delegate to GetUserData")
		})
	}
}

func TestRealProvider_GetUserData(t *testing.T) {
	//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
	provider := NewIKSBootstrapProvider(nil, fake.NewSimpleClientset())
	ctx := context.Background()

	tests := []struct {
		name      string
		nodeClass *v1alpha1.IBMNodeClass
		validate  func(*testing.T, string)
	}{
		{
			name: "default user data via real provider",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{},
			},
			validate: func(t *testing.T, userData string) {
				assert.Contains(t, userData, "#!/bin/bash")
				assert.Contains(t, userData, "IKS node provisioned by Karpenter IBM Cloud Provider")
				assert.Contains(t, userData, "IKS handles the Kubernetes bootstrap process automatically")
			},
		},
		{
			name: "custom user data via real provider",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					UserData: "#!/bin/bash\necho 'real provider custom'",
				},
			},
			validate: func(t *testing.T, userData string) {
				assert.Equal(t, "#!/bin/bash\necho 'real provider custom'", userData)
			},
		},
		{
			name: "whitespace-only user data falls back to default",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					UserData: "  \n\t  ",
				},
			},
			validate: func(t *testing.T, userData string) {
				assert.Contains(t, userData, "IKS node provisioned by Karpenter")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeClaim := types.NamespacedName{Name: "real-claim", Namespace: "default"}
			result, err := provider.GetUserData(ctx, tt.nodeClass, nodeClaim)
			assert.NoError(t, err)
			assert.NotEmpty(t, result)
			tt.validate(t, result)
		})
	}
}

func TestRealProvider_getClusterName(t *testing.T) {
	originalClusterName := os.Getenv("CLUSTER_NAME")
	originalClusterID := os.Getenv("IKS_CLUSTER_ID")
	defer func() {
		restoreEnv("CLUSTER_NAME", originalClusterName)
		restoreEnv("IKS_CLUSTER_ID", originalClusterID)
	}()

	tests := []struct {
		name           string
		envClusterName string
		envClusterID   string
		expected       string
	}{
		{
			name:           "CLUSTER_NAME takes precedence",
			envClusterName: "my-cluster",
			envClusterID:   "cid-999",
			expected:       "my-cluster",
		},
		{
			name:           "falls back to IKS_CLUSTER_ID",
			envClusterName: "",
			envClusterID:   "abc-123",
			expected:       "iks-cluster-abc-123",
		},
		{
			name:           "default when no env vars set",
			envClusterName: "",
			envClusterID:   "",
			expected:       "karpenter-iks-cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setOrUnsetEnv("CLUSTER_NAME", tt.envClusterName)
			setOrUnsetEnv("IKS_CLUSTER_ID", tt.envClusterID)

			provider := NewIKSBootstrapProvider(nil, nil)
			result := provider.getClusterName()

			assert.Equal(t, tt.expected, result)
		})
	}
}

func setOrUnsetEnv(key, value string) {
	if value != "" {
		_ = os.Setenv(key, value)
	} else {
		_ = os.Unsetenv(key)
	}
}

func restoreEnv(key, original string) {
	if original != "" {
		_ = os.Setenv(key, original)
	} else {
		_ = os.Unsetenv(key)
	}
}
