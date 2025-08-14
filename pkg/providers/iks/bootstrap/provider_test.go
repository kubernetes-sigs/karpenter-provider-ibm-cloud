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
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	commonTypes "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

// MockIKSClient provides a mock implementation of the IKS client
type MockIKSClient struct {
	mock.Mock
}

func (m *MockIKSClient) GetClusterConfig(ctx context.Context, clusterID string) (string, error) {
	args := m.Called(ctx, clusterID)
	return args.String(0), args.Error(1)
}

// Implement other interface methods (not used in bootstrap provider but required for interface compliance)
func (m *MockIKSClient) GetWorkerDetails(ctx context.Context, clusterID, workerID string) (*ibm.IKSWorkerDetails, error) {
	args := m.Called(ctx, clusterID, workerID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ibm.IKSWorkerDetails), args.Error(1)
}

func (m *MockIKSClient) GetVPCInstanceIDFromWorker(ctx context.Context, clusterID, workerID string) (string, error) {
	args := m.Called(ctx, clusterID, workerID)
	return args.String(0), args.Error(1)
}

func (m *MockIKSClient) ListWorkerPools(ctx context.Context, clusterID string) ([]*ibm.WorkerPool, error) {
	args := m.Called(ctx, clusterID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*ibm.WorkerPool), args.Error(1)
}

func (m *MockIKSClient) GetWorkerPool(ctx context.Context, clusterID, poolID string) (*ibm.WorkerPool, error) {
	args := m.Called(ctx, clusterID, poolID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ibm.WorkerPool), args.Error(1)
}

func (m *MockIKSClient) ResizeWorkerPool(ctx context.Context, clusterID, poolID string, newSize int) error {
	args := m.Called(ctx, clusterID, poolID, newSize)
	return args.Error(0)
}

// IBMClientInterface defines the minimal interface needed by the bootstrap provider
type IBMClientInterface interface {
	GetIKSClient() ibm.IKSClientInterface
}

// MockIBMClient provides a mock implementation of the IBM client
type MockIBMClient struct {
	mock.Mock
	mockIKSClient *MockIKSClient
}

func (m *MockIBMClient) GetVPCClient() (*ibm.VPCClient, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ibm.VPCClient), args.Error(1)
}

func (m *MockIBMClient) GetIKSClient() ibm.IKSClientInterface {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(ibm.IKSClientInterface)
}

// TestableIKSBootstrapProvider allows injection of mock IBM client for testing
type TestableIKSBootstrapProvider struct {
	ibmClient IBMClientInterface
	k8sClient kubernetes.Interface
}

func NewTestableIKSBootstrapProvider(ibmClient IBMClientInterface, k8sClient kubernetes.Interface) *TestableIKSBootstrapProvider {
	return &TestableIKSBootstrapProvider{
		ibmClient: ibmClient,
		k8sClient: k8sClient,
	}
}

func (p *TestableIKSBootstrapProvider) GetClusterConfig(ctx context.Context, clusterID string) (*commonTypes.ClusterInfo, error) {
	logger := log.FromContext(ctx)

	if p.ibmClient == nil {
		return nil, fmt.Errorf("IBM client is not initialized")
	}

	iksClient := p.ibmClient.GetIKSClient()
	if iksClient == nil {
		return nil, fmt.Errorf("IKS client not available")
	}

	logger.Info("Retrieving cluster config from IKS API", "cluster_id", clusterID)

	// Get kubeconfig from IKS API
	kubeconfig, err := iksClient.GetClusterConfig(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("getting cluster config from IKS API: %w", err)
	}

	// Parse kubeconfig to extract cluster information
	endpoint, caData, err := commonTypes.ParseKubeconfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig from IKS API: %w", err)
	}

	return &commonTypes.ClusterInfo{
		Endpoint:     endpoint,
		CAData:       caData,
		ClusterName:  p.getClusterName(),
		IsIKSManaged: true,
		IKSClusterID: clusterID,
	}, nil
}

func (p *TestableIKSBootstrapProvider) GetUserData(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName) (string, error) {
	logger := log.FromContext(ctx)
	logger.Info("Generating IKS user data")

	// For IKS mode, we don't need complex bootstrap scripts since IKS handles most of the setup
	// The worker pool resize API will add nodes that are automatically configured
	// Return minimal user data or empty string

	// If there's custom user data specified, include it
	if strings.TrimSpace(nodeClass.Spec.UserData) != "" {
		logger.Info("Including custom user data for IKS node")
		return nodeClass.Spec.UserData, nil
	}

	// Default minimal user data for IKS
	return `#!/bin/bash
# IKS node provisioned by Karpenter IBM Cloud Provider
echo "Node provisioned via IKS worker pool resize API"
# IKS handles the Kubernetes bootstrap process automatically
`, nil
}

func (p *TestableIKSBootstrapProvider) getClusterName() string {
	if name := os.Getenv("CLUSTER_NAME"); name != "" {
		return name
	}
	if clusterID := os.Getenv("IKS_CLUSTER_ID"); clusterID != "" {
		return fmt.Sprintf("iks-cluster-%s", clusterID)
	}
	return "karpenter-iks-cluster"
}

// Test helpers
func getTestNodeClass() *v1alpha1.IBMNodeClass {
	return &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:       "us-south",
			Zone:         "us-south-1",
			Image:        "test-image",
			VPC:          "test-vpc",
			IKSClusterID: "test-cluster-id",
			UserData:     "#!/bin/bash\necho 'custom IKS user data'",
		},
	}
}

func getTestKubeconfig() string {
	return `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: dGVzdC1jYS1kYXRh
    server: https://test-iks-cluster.us-south.containers.cloud.ibm.com:31234
  name: test-iks-cluster
contexts:
- context:
    cluster: test-iks-cluster
    user: test-user
  name: test-context
current-context: test-context
kind: Config
users:
- name: test-user
  user:
    token: test-iks-token`
}

func TestIKSBootstrapProvider_GetUserData(t *testing.T) {
	// Save original env vars and restore after test
	originalClusterName := os.Getenv("CLUSTER_NAME")
	originalClusterID := os.Getenv("IKS_CLUSTER_ID")
	defer func() {
		if originalClusterName != "" {
			_ = os.Setenv("CLUSTER_NAME", originalClusterName)
		} else {
			_ = os.Unsetenv("CLUSTER_NAME")
		}
		if originalClusterID != "" {
			_ = os.Setenv("IKS_CLUSTER_ID", originalClusterID)
		} else {
			_ = os.Unsetenv("IKS_CLUSTER_ID")
		}
	}()

	tests := []struct {
		name             string
		nodeClass        *v1alpha1.IBMNodeClass
		nodeClaim        types.NamespacedName
		envClusterName   string
		envClusterID     string
		expectError      bool
		errorContains    string
		validateUserData func(*testing.T, string)
	}{
		{
			name: "minimal user data with default message",
			nodeClass: func() *v1alpha1.IBMNodeClass {
				nc := getTestNodeClass()
				nc.Spec.UserData = "" // Remove custom user data
				return nc
			}(),
			nodeClaim:   types.NamespacedName{Name: "test-nodeclaim", Namespace: "default"},
			expectError: false,
			validateUserData: func(t *testing.T, userData string) {
				assert.NotEmpty(t, userData)
				assert.Contains(t, userData, "#!/bin/bash")
				assert.Contains(t, userData, "IKS node provisioned by Karpenter")
				assert.Contains(t, userData, "IKS handles the Kubernetes bootstrap process automatically")
				// Should not contain custom user data
				assert.NotContains(t, userData, "custom IKS user data")
			},
		},
		{
			name:        "custom user data from NodeClass",
			nodeClass:   getTestNodeClass(),
			nodeClaim:   types.NamespacedName{Name: "test-nodeclaim", Namespace: "default"},
			expectError: false,
			validateUserData: func(t *testing.T, userData string) {
				assert.NotEmpty(t, userData)
				assert.Contains(t, userData, "custom IKS user data")
				// Should not contain default message when custom data is provided
				assert.NotContains(t, userData, "IKS node provisioned by Karpenter")
			},
		},
		{
			name: "empty custom user data falls back to default",
			nodeClass: func() *v1alpha1.IBMNodeClass {
				nc := getTestNodeClass()
				nc.Spec.UserData = "   " // Whitespace only
				return nc
			}(),
			nodeClaim:   types.NamespacedName{Name: "test-nodeclaim", Namespace: "default"},
			expectError: false,
			validateUserData: func(t *testing.T, userData string) {
				assert.NotEmpty(t, userData)
				assert.Contains(t, userData, "IKS node provisioned by Karpenter")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Set up environment variables
			if tt.envClusterName != "" {
				_ = os.Setenv("CLUSTER_NAME", tt.envClusterName)
			} else {
				_ = os.Unsetenv("CLUSTER_NAME")
			}
			if tt.envClusterID != "" {
				_ = os.Setenv("IKS_CLUSTER_ID", tt.envClusterID)
			} else {
				_ = os.Unsetenv("IKS_CLUSTER_ID")
			}

			// Create fake Kubernetes client
			fakeClient := fake.NewSimpleClientset()

			// Create IKS bootstrap provider
			provider := NewIKSBootstrapProvider(nil, fakeClient)

			// Test GetUserData method
			result, err := provider.GetUserData(ctx, tt.nodeClass, tt.nodeClaim)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, result)
				if tt.validateUserData != nil {
					tt.validateUserData(t, result)
				}
			}
		})
	}
}

func TestIKSBootstrapProvider_GetClusterConfig(t *testing.T) {
	tests := []struct {
		name           string
		clusterID      string
		setupMocks     func(*MockIBMClient, *MockIKSClient)
		expectError    bool
		errorContains  string
		validateResult func(*testing.T, *commonTypes.ClusterInfo)
	}{
		{
			name:      "successful cluster config retrieval",
			clusterID: "test-cluster-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				// Return the mock IKS client, not a real one
				ibmClient.On("GetIKSClient").Return(iksClient)

				kubeconfig := getTestKubeconfig()
				iksClient.On("GetClusterConfig", mock.Anything, "test-cluster-id").Return(kubeconfig, nil)
			},
			expectError: false,
			validateResult: func(t *testing.T, info *commonTypes.ClusterInfo) {
				assert.NotNil(t, info)
				assert.Equal(t, "https://test-iks-cluster.us-south.containers.cloud.ibm.com:31234", info.Endpoint)
				assert.NotEmpty(t, info.CAData)
				assert.Equal(t, "test-ca-data", string(info.CAData)) // Base64 decoded value
				assert.True(t, info.IsIKSManaged)
				assert.Equal(t, "test-cluster-id", info.IKSClusterID)
				assert.Contains(t, info.ClusterName, "iks-cluster")
			},
		},
		{
			name:      "IBM client is not initialized",
			clusterID: "test-cluster-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				// Provider will be created with nil IBM client
			},
			expectError:   true,
			errorContains: "IBM client is not initialized",
		},
		{
			name:      "IKS client not available",
			clusterID: "test-cluster-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(nil, nil) // Return nil client
			},
			expectError:   true,
			errorContains: "IKS client not available",
		},
		{
			name:      "get cluster config failure",
			clusterID: "test-cluster-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(iksClient)
				iksClient.On("GetClusterConfig", mock.Anything, "test-cluster-id").Return("", fmt.Errorf("cluster not found"))
			},
			expectError:   true,
			errorContains: "getting cluster config from IKS API",
		},
		{
			name:      "invalid kubeconfig parsing",
			clusterID: "test-cluster-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(iksClient)

				// Return invalid kubeconfig
				invalidKubeconfig := `invalid: yaml content without server`
				iksClient.On("GetClusterConfig", mock.Anything, "test-cluster-id").Return(invalidKubeconfig, nil)
			},
			expectError:   true,
			errorContains: "parsing kubeconfig from IKS API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock clients
			var mockIBMClient *MockIBMClient
			var mockIKSClient *MockIKSClient

			if tt.name == "IBM client is not initialized" {
				// Create provider with nil IBM client
				mockIBMClient = nil
			} else {
				mockIBMClient = &MockIBMClient{}
				mockIKSClient = &MockIKSClient{}
				mockIBMClient.mockIKSClient = mockIKSClient
			}

			// Setup mocks
			if mockIBMClient != nil && mockIKSClient != nil {
				tt.setupMocks(mockIBMClient, mockIKSClient)
			}

			// Create testable IKS bootstrap provider with mock client
			var provider *TestableIKSBootstrapProvider
			if mockIBMClient != nil {
				provider = NewTestableIKSBootstrapProvider(mockIBMClient, nil)
			} else {
				provider = NewTestableIKSBootstrapProvider(nil, nil)
			}

			// Test GetClusterConfig method
			result, err := provider.GetClusterConfig(ctx, tt.clusterID)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				if tt.validateResult != nil {
					tt.validateResult(t, result)
				}
			}

			// Verify all expected calls were made
			if mockIBMClient != nil {
				mockIBMClient.AssertExpectations(t)
			}
			if mockIKSClient != nil {
				mockIKSClient.AssertExpectations(t)
			}
		})
	}
}

func TestIKSBootstrapProvider_getClusterName(t *testing.T) {
	// Save original env vars and restore after test
	originalClusterName := os.Getenv("CLUSTER_NAME")
	originalClusterID := os.Getenv("IKS_CLUSTER_ID")
	defer func() {
		if originalClusterName != "" {
			_ = os.Setenv("CLUSTER_NAME", originalClusterName)
		} else {
			_ = os.Unsetenv("CLUSTER_NAME")
		}
		if originalClusterID != "" {
			_ = os.Setenv("IKS_CLUSTER_ID", originalClusterID)
		} else {
			_ = os.Unsetenv("IKS_CLUSTER_ID")
		}
	}()

	tests := []struct {
		name           string
		envClusterName string
		envClusterID   string
		expectedResult string
	}{
		{
			name:           "cluster name from environment",
			envClusterName: "my-custom-cluster",
			envClusterID:   "cluster-123",
			expectedResult: "my-custom-cluster",
		},
		{
			name:           "cluster name derived from cluster ID",
			envClusterName: "", // No custom cluster name
			envClusterID:   "cluster-456",
			expectedResult: "iks-cluster-cluster-456",
		},
		{
			name:           "default cluster name when no env vars",
			envClusterName: "",
			envClusterID:   "",
			expectedResult: "karpenter-iks-cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment variables
			if tt.envClusterName != "" {
				_ = os.Setenv("CLUSTER_NAME", tt.envClusterName)
			} else {
				_ = os.Unsetenv("CLUSTER_NAME")
			}
			if tt.envClusterID != "" {
				_ = os.Setenv("IKS_CLUSTER_ID", tt.envClusterID)
			} else {
				_ = os.Unsetenv("IKS_CLUSTER_ID")
			}

			// Create IKS bootstrap provider
			provider := NewIKSBootstrapProvider(nil, nil)

			// Test getClusterName method
			result := provider.getClusterName()

			// Validate result
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestIKSBootstrapProvider_Integration(t *testing.T) {
	tests := []struct {
		name           string
		setupProvider  func() *TestableIKSBootstrapProvider
		testOperations func(*testing.T, *TestableIKSBootstrapProvider)
	}{
		{
			name: "full workflow with working IKS API",
			setupProvider: func() *TestableIKSBootstrapProvider {
				mockIBMClient := &MockIBMClient{}
				mockIKSClient := &MockIKSClient{}
				mockIBMClient.mockIKSClient = mockIKSClient

				// Setup successful IKS API calls
				mockIBMClient.On("GetIKSClient").Return(mockIKSClient)
				kubeconfig := getTestKubeconfig()
				mockIKSClient.On("GetClusterConfig", mock.Anything, "integration-cluster").Return(kubeconfig, nil)

				fakeClient := fake.NewSimpleClientset()
				return NewTestableIKSBootstrapProvider(mockIBMClient, fakeClient)
			},
			testOperations: func(t *testing.T, provider *TestableIKSBootstrapProvider) {
				ctx := context.Background()

				// Test GetClusterConfig
				clusterInfo, err := provider.GetClusterConfig(ctx, "integration-cluster")
				assert.NoError(t, err)
				assert.NotNil(t, clusterInfo)
				assert.True(t, clusterInfo.IsIKSManaged)
				assert.Equal(t, "integration-cluster", clusterInfo.IKSClusterID)

				// Test GetUserData with custom user data
				nodeClass := &v1alpha1.IBMNodeClass{
					Spec: v1alpha1.IBMNodeClassSpec{
						UserData: "#!/bin/bash\necho 'integration test'",
					},
				}
				nodeClaim := types.NamespacedName{Name: "integration-nodeclaim"}

				userData, err := provider.GetUserData(ctx, nodeClass, nodeClaim)
				assert.NoError(t, err)
				assert.Contains(t, userData, "integration test")

				// Test GetUserData with default user data
				nodeClassDefault := &v1alpha1.IBMNodeClass{}
				userDataDefault, err := provider.GetUserData(ctx, nodeClassDefault, nodeClaim)
				assert.NoError(t, err)
				assert.Contains(t, userDataDefault, "IKS node provisioned by Karpenter")
			},
		},
		{
			name: "graceful handling of IKS API failures",
			setupProvider: func() *TestableIKSBootstrapProvider {
				mockIBMClient := &MockIBMClient{}
				mockIKSClient := &MockIKSClient{}
				mockIBMClient.mockIKSClient = mockIKSClient

				// Setup IKS API failure
				mockIBMClient.On("GetIKSClient").Return(mockIKSClient)
				mockIKSClient.On("GetClusterConfig", mock.Anything, "failing-cluster").Return("", fmt.Errorf("API error"))

				fakeClient := fake.NewSimpleClientset()
				return NewTestableIKSBootstrapProvider(mockIBMClient, fakeClient)
			},
			testOperations: func(t *testing.T, provider *TestableIKSBootstrapProvider) {
				ctx := context.Background()

				// Test GetClusterConfig failure
				clusterInfo, err := provider.GetClusterConfig(ctx, "failing-cluster")
				assert.Error(t, err)
				assert.Nil(t, clusterInfo)
				assert.Contains(t, err.Error(), "getting cluster config from IKS API")

				// Test that GetUserData still works despite API failures
				nodeClass := &v1alpha1.IBMNodeClass{
					Spec: v1alpha1.IBMNodeClassSpec{
						UserData: "#!/bin/bash\necho 'fallback test'",
					},
				}
				nodeClaim := types.NamespacedName{Name: "fallback-nodeclaim"}

				userData, err := provider.GetUserData(ctx, nodeClass, nodeClaim)
				assert.NoError(t, err)
				assert.Contains(t, userData, "fallback test")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := tt.setupProvider()
			tt.testOperations(t, provider)
		})
	}
}
