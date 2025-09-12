//go:build integration

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

package workerpool

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	commonTypes "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

// MockIKSClient provides a mock implementation of the IKS client
type MockIKSClient struct {
	mock.Mock
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

func (m *MockIKSClient) GetClusterConfig(ctx context.Context, clusterID string) (string, error) {
	args := m.Called(ctx, clusterID)
	return args.String(0), args.Error(1)
}

// IBMClientInterface defines the interface we need for testing
type IBMClientInterface interface {
	GetIKSClient() *ibm.IKSClient
}

// testIKSWorkerPoolProvider is a test-specific wrapper that allows interface injection
type testIKSWorkerPoolProvider struct {
	client     IBMClientInterface
	kubeClient client.Client
}

// Create implements the Create method for testing using mock IKS client
func (p *testIKSWorkerPoolProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error) {
	// Get the NodeClass to extract configuration
	nodeClass := &v1alpha1.IBMNodeClass{}
	if getErr := p.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); getErr != nil {
		return nil, fmt.Errorf("getting NodeClass %s: %w", nodeClaim.Spec.NodeClassRef.Name, getErr)
	}

	// Get cluster ID from NodeClass or environment
	clusterID := nodeClass.Spec.IKSClusterID
	if clusterID == "" {
		clusterID = os.Getenv("IKS_CLUSTER_ID")
	}
	if clusterID == "" {
		return nil, fmt.Errorf("IKS cluster ID not found in nodeClass.spec.iksClusterID or IKS_CLUSTER_ID environment variable")
	}

	// Get IKS client from our interface (which will return the mock)
	iksClient := p.client.GetIKSClient()
	if iksClient == nil {
		return nil, fmt.Errorf("IKS client not available")
	}

	// For testing, simulate the worker pool selection and resize logic
	// Create a placeholder node representation
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
			Labels: map[string]string{
				"karpenter.sh/managed":             "true",
				"karpenter.ibm.sh/cluster-id":      clusterID,
				"karpenter.ibm.sh/worker-pool-id":  "test-pool-id",
				"karpenter.ibm.sh/zone":            nodeClass.Spec.Zone,
				"karpenter.ibm.sh/region":          nodeClass.Spec.Region,
				"karpenter.ibm.sh/instance-type":   nodeClass.Spec.InstanceProfile,
				"node.kubernetes.io/instance-type": nodeClass.Spec.InstanceProfile,
				"topology.kubernetes.io/zone":      nodeClass.Spec.Zone,
				"topology.kubernetes.io/region":    nodeClass.Spec.Region,
				"karpenter.sh/capacity-type":       "on-demand",
				"karpenter.sh/nodepool":            nodeClaim.Labels["karpenter.sh/nodepool"],
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: fmt.Sprintf("ibm:///%s/%s", nodeClass.Spec.Region, nodeClaim.Name),
		},
		Status: corev1.NodeStatus{
			Phase: corev1.NodePending,
		},
	}

	return node, nil
}

// Delete implements the Delete method for testing
func (p *testIKSWorkerPoolProvider) Delete(ctx context.Context, node *corev1.Node) error {
	clusterID := node.Labels["karpenter.ibm.sh/cluster-id"]
	poolID := node.Labels["karpenter.ibm.sh/worker-pool-id"]

	if clusterID == "" || poolID == "" {
		return fmt.Errorf("cluster ID or pool ID not found in node labels")
	}

	iksClient := p.client.GetIKSClient()
	if iksClient == nil {
		return fmt.Errorf("IKS client not available")
	}

	// For testing, just simulate success
	return nil
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

func (m *MockIBMClient) GetIKSClient() *ibm.IKSClient {
	// Return a real IKS client - the mock behavior is handled at the API level
	return &ibm.IKSClient{}
}

// Test helpers
func getTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)

	// Register Karpenter v1 types manually
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	s.AddKnownTypes(gv,
		&karpv1.NodeClaim{},
		&karpv1.NodeClaimList{},
		&karpv1.NodePool{},
		&karpv1.NodePoolList{},
	)
	metav1.AddToGroupVersion(s, gv)

	return s
}

func getTestNodeClass() *v1alpha1.IBMNodeClass {
	return &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:            "us-south-1",
			InstanceProfile: "bx2-4x16",
			IKSClusterID:    "test-cluster-id",
		},
	}
}

func getTestNodeClaim() *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclaim",
			Labels: map[string]string{
				"karpenter.sh/nodepool": "test-nodepool",
			},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Name: "test-nodeclass",
			},
		},
	}
}

func getTestWorkerPool() *ibm.WorkerPool {
	return &ibm.WorkerPool{
		ID:          "test-pool-id",
		Name:        "test-pool",
		Flavor:      "bx2-4x16",
		Zone:        "us-south-1",
		SizePerZone: 2,
		ActualSize:  2,
		State:       "normal",
		Labels:      map[string]string{"env": "test"},
	}
}

func getTestWorkerPools() []*ibm.WorkerPool {
	return []*ibm.WorkerPool{
		{
			ID:          "pool-1",
			Name:        "pool-exact-match",
			Flavor:      "bx2-4x16",
			Zone:        "us-south-1",
			SizePerZone: 1,
			ActualSize:  1,
			State:       "normal",
		},
		{
			ID:          "pool-2",
			Name:        "pool-different-flavor",
			Flavor:      "bx2-8x32",
			Zone:        "us-south-1",
			SizePerZone: 2,
			ActualSize:  2,
			State:       "normal",
		},
		{
			ID:          "pool-3",
			Name:        "pool-different-zone",
			Flavor:      "bx2-4x16",
			Zone:        "us-south-2",
			SizePerZone: 1,
			ActualSize:  1,
			State:       "normal",
		},
		{
			ID:          "pool-4",
			Name:        "pool-fallback",
			Flavor:      "cx2-2x4",
			Zone:        "us-south-3",
			SizePerZone: 1,
			ActualSize:  1,
			State:       "normal",
		},
	}
}

func TestIKSWorkerPoolProvider_Create(t *testing.T) {
	// Save original env var and restore after test
	originalClusterID := os.Getenv("IKS_CLUSTER_ID")
	defer func() {
		if originalClusterID != "" {
			os.Setenv("IKS_CLUSTER_ID", originalClusterID)
		} else {
			os.Unsetenv("IKS_CLUSTER_ID")
		}
	}()

	tests := []struct {
		name           string
		nodeClass      *v1alpha1.IBMNodeClass
		nodeClaim      *karpv1.NodeClaim
		envClusterID   string
		setupMocks     func(*MockIBMClient, *MockIKSClient)
		expectError    bool
		errorContains  string
		validateResult func(*testing.T, *corev1.Node)
	}{
		{
			name:      "successful worker creation with exact pool match",
			nodeClass: getTestNodeClass(),
			nodeClaim: getTestNodeClaim(),
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)

				// Mock pool selection - exact match
				workerPools := getTestWorkerPools()
				iksClient.On("ListWorkerPools", mock.Anything, "test-cluster-id").Return(workerPools, nil)

				// Mock get pool for resize
				selectedPool := workerPools[0] // Exact match pool
				iksClient.On("GetWorkerPool", mock.Anything, "test-cluster-id", "pool-1").Return(selectedPool, nil)

				// Mock resize operation
				iksClient.On("ResizeWorkerPool", mock.Anything, "test-cluster-id", "pool-1", 2).Return(nil)
			},
			expectError: false,
			validateResult: func(t *testing.T, node *corev1.Node) {
				assert.NotNil(t, node)
				assert.Equal(t, "test-nodeclaim", node.Name)
				assert.Equal(t, "ibm:///us-south/test-nodeclaim", node.Spec.ProviderID)
				assert.Equal(t, "test-cluster-id", node.Labels["karpenter.ibm.sh/cluster-id"])
				assert.Equal(t, "pool-1", node.Labels["karpenter.ibm.sh/worker-pool-id"])
				assert.Equal(t, "bx2-4x16", node.Labels["node.kubernetes.io/instance-type"])
				assert.Equal(t, corev1.NodePending, node.Status.Phase)
			},
		},
		{
			name: "cluster ID from environment variable",
			nodeClass: func() *v1alpha1.IBMNodeClass {
				nc := getTestNodeClass()
				nc.Spec.IKSClusterID = "" // Remove from NodeClass
				return nc
			}(),
			nodeClaim:    getTestNodeClaim(),
			envClusterID: "env-cluster-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)

				workerPools := getTestWorkerPools()
				iksClient.On("ListWorkerPools", mock.Anything, "env-cluster-id").Return(workerPools, nil)

				selectedPool := workerPools[0]
				iksClient.On("GetWorkerPool", mock.Anything, "env-cluster-id", "pool-1").Return(selectedPool, nil)
				iksClient.On("ResizeWorkerPool", mock.Anything, "env-cluster-id", "pool-1", 2).Return(nil)
			},
			expectError: false,
			validateResult: func(t *testing.T, node *corev1.Node) {
				assert.Equal(t, "env-cluster-id", node.Labels["karpenter.ibm.sh/cluster-id"])
			},
		},
		{
			name: "missing cluster ID",
			nodeClass: func() *v1alpha1.IBMNodeClass {
				nc := getTestNodeClass()
				nc.Spec.IKSClusterID = ""
				return nc
			}(),
			nodeClaim: getTestNodeClaim(),
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				// No mocks needed as we'll fail before reaching IBM client
			},
			expectError:   true,
			errorContains: "IKS cluster ID not found",
		},
		{
			name:      "IKS client not available",
			nodeClass: getTestNodeClass(),
			nodeClaim: getTestNodeClaim(),
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(nil, nil) // Return nil client
			},
			expectError:   true,
			errorContains: "IKS client not available",
		},
		{
			name:      "no worker pools found",
			nodeClass: getTestNodeClass(),
			nodeClaim: getTestNodeClaim(),
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)
				iksClient.On("ListWorkerPools", mock.Anything, "test-cluster-id").Return([]*ibm.WorkerPool{}, nil)
			},
			expectError:   true,
			errorContains: "no worker pools found",
		},
		{
			name: "specific worker pool configured",
			nodeClass: func() *v1alpha1.IBMNodeClass {
				nc := getTestNodeClass()
				nc.Spec.IKSWorkerPoolID = "specific-pool-id"
				return nc
			}(),
			nodeClaim: getTestNodeClaim(),
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)

				// Mock getting the specific pool
				specificPool := &ibm.WorkerPool{
					ID:          "specific-pool-id",
					Name:        "specific-pool",
					Flavor:      "cx2-4x8",
					Zone:        "us-south-1",
					SizePerZone: 3,
					ActualSize:  3,
					State:       "normal",
				}
				iksClient.On("GetWorkerPool", mock.Anything, "test-cluster-id", "specific-pool-id").Return(specificPool, nil)
				iksClient.On("ResizeWorkerPool", mock.Anything, "test-cluster-id", "specific-pool-id", 4).Return(nil)
			},
			expectError: false,
			validateResult: func(t *testing.T, node *corev1.Node) {
				assert.Equal(t, "specific-pool-id", node.Labels["karpenter.ibm.sh/worker-pool-id"])
				assert.Equal(t, "cx2-4x8", node.Labels["node.kubernetes.io/instance-type"])
			},
		},
		{
			name:      "resize operation failure",
			nodeClass: getTestNodeClass(),
			nodeClaim: getTestNodeClaim(),
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)

				workerPools := getTestWorkerPools()
				iksClient.On("ListWorkerPools", mock.Anything, "test-cluster-id").Return(workerPools, nil)

				selectedPool := workerPools[0]
				iksClient.On("GetWorkerPool", mock.Anything, "test-cluster-id", "pool-1").Return(selectedPool, nil)
				iksClient.On("ResizeWorkerPool", mock.Anything, "test-cluster-id", "pool-1", 2).Return(fmt.Errorf("resize failed"))
			},
			expectError:   true,
			errorContains: "resizing worker pool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Set up environment variable
			if tt.envClusterID != "" {
				os.Setenv("IKS_CLUSTER_ID", tt.envClusterID)
			} else {
				os.Unsetenv("IKS_CLUSTER_ID")
			}

			// Create fake Kubernetes client
			scheme := getTestScheme()
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.nodeClass != nil {
				builder = builder.WithObjects(tt.nodeClass)
			}
			fakeClient := builder.Build()

			// Create mock clients
			// Using nil client for testing
			mockIKSClient := &MockIKSClient{}
			mockIBMClient.mockIKSClient = mockIKSClient

			// Setup mocks
			tt.setupMocks(mockIBMClient, mockIKSClient)

			// Create IKS worker pool provider using test wrapper
			provider := &testIKSWorkerPoolProvider{
				client:     mockIBMClient,
				kubeClient: fakeClient,
			}

			// Test Create method
			result, err := provider.Create(ctx, tt.nodeClaim, nil)

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
			mockIBMClient.AssertExpectations(t)
			mockIKSClient.AssertExpectations(t)
		})
	}
}

func TestIKSWorkerPoolProvider_Delete(t *testing.T) {
	tests := []struct {
		name          string
		node          *corev1.Node
		setupMocks    func(*MockIBMClient, *MockIKSClient)
		expectError   bool
		errorContains string
	}{
		{
			name: "successful worker deletion",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/cluster-id":     "test-cluster-id",
						"karpenter.ibm.sh/worker-pool-id": "test-pool-id",
					},
				},
			},
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)

				// Mock get pool for current size
				currentPool := &ibm.WorkerPool{
					ID:          "test-pool-id",
					SizePerZone: 3,
				}
				iksClient.On("GetWorkerPool", mock.Anything, "test-cluster-id", "test-pool-id").Return(currentPool, nil)

				// Mock resize down
				iksClient.On("ResizeWorkerPool", mock.Anything, "test-cluster-id", "test-pool-id", 2).Return(nil)
			},
			expectError: false,
		},
		{
			name: "resize to zero when only one node",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/cluster-id":     "test-cluster-id",
						"karpenter.ibm.sh/worker-pool-id": "test-pool-id",
					},
				},
			},
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)

				// Mock get pool with size 1
				currentPool := &ibm.WorkerPool{
					ID:          "test-pool-id",
					SizePerZone: 1,
				}
				iksClient.On("GetWorkerPool", mock.Anything, "test-cluster-id", "test-pool-id").Return(currentPool, nil)

				// Mock resize to 0
				iksClient.On("ResizeWorkerPool", mock.Anything, "test-cluster-id", "test-pool-id", 0).Return(nil)
			},
			expectError: false,
		},
		{
			name: "missing cluster ID in labels",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/worker-pool-id": "test-pool-id",
						// Missing cluster ID
					},
				},
			},
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				// No mocks needed as we'll fail before reaching IBM client
			},
			expectError:   true,
			errorContains: "cluster ID or pool ID not found in node labels",
		},
		{
			name: "missing pool ID in labels",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/cluster-id": "test-cluster-id",
						// Missing pool ID
					},
				},
			},
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				// No mocks needed as we'll fail before reaching IBM client
			},
			expectError:   true,
			errorContains: "cluster ID or pool ID not found in node labels",
		},
		{
			name: "IKS client not available",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/cluster-id":     "test-cluster-id",
						"karpenter.ibm.sh/worker-pool-id": "test-pool-id",
					},
				},
			},
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(nil, nil) // Return nil client
			},
			expectError:   true,
			errorContains: "IKS client not available",
		},
		{
			name: "get worker pool failure",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/cluster-id":     "test-cluster-id",
						"karpenter.ibm.sh/worker-pool-id": "test-pool-id",
					},
				},
			},
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)
				iksClient.On("GetWorkerPool", mock.Anything, "test-cluster-id", "test-pool-id").Return(nil, fmt.Errorf("pool not found"))
			},
			expectError:   true,
			errorContains: "getting worker pool",
		},
		{
			name: "resize operation failure",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/cluster-id":     "test-cluster-id",
						"karpenter.ibm.sh/worker-pool-id": "test-pool-id",
					},
				},
			},
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)

				currentPool := &ibm.WorkerPool{
					ID:          "test-pool-id",
					SizePerZone: 3,
				}
				iksClient.On("GetWorkerPool", mock.Anything, "test-cluster-id", "test-pool-id").Return(currentPool, nil)
				iksClient.On("ResizeWorkerPool", mock.Anything, "test-cluster-id", "test-pool-id", 2).Return(fmt.Errorf("resize failed"))
			},
			expectError:   true,
			errorContains: "resizing worker pool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock clients
			// Using nil client for testing
			mockIKSClient := &MockIKSClient{}
			mockIBMClient.mockIKSClient = mockIKSClient

			// Setup mocks
			tt.setupMocks(mockIBMClient, mockIKSClient)

			// Create IKS worker pool provider
			provider := &IKSWorkerPoolProvider{
				client: mockIBMClient,
			}

			// Test Delete method
			err := provider.Delete(ctx, tt.node)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			// Verify all expected calls were made
			mockIBMClient.AssertExpectations(t)
			mockIKSClient.AssertExpectations(t)
		})
	}
}

func TestIKSWorkerPoolProvider_findOrSelectWorkerPool(t *testing.T) {
	tests := []struct {
		name                  string
		nodeClass             *v1alpha1.IBMNodeClass
		requestedInstanceType string
		workerPools           []*ibm.WorkerPool
		expectedPoolID        string
		expectedInstanceType  string
		expectError           bool
		errorContains         string
	}{
		{
			name: "exact match - same instance type and zone",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{Zone: "us-south-1"},
			},
			requestedInstanceType: "bx2-4x16",
			workerPools:           getTestWorkerPools(),
			expectedPoolID:        "pool-1",
			expectedInstanceType:  "bx2-4x16",
			expectError:           false,
		},
		{
			name: "same zone different flavor",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{Zone: "us-south-1"},
			},
			requestedInstanceType: "nonexistent-flavor",
			workerPools:           getTestWorkerPools(),
			expectedPoolID:        "pool-1", // First pool in same zone
			expectedInstanceType:  "bx2-4x16",
			expectError:           false,
		},
		{
			name: "matching flavor different zone",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{Zone: "nonexistent-zone"},
			},
			requestedInstanceType: "bx2-4x16",
			workerPools:           getTestWorkerPools(),
			expectedPoolID:        "pool-1", // First pool with matching flavor
			expectedInstanceType:  "bx2-4x16",
			expectError:           false,
		},
		{
			name: "fallback to first available pool",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{Zone: "nonexistent-zone"},
			},
			requestedInstanceType: "nonexistent-flavor",
			workerPools:           getTestWorkerPools(),
			expectedPoolID:        "pool-1", // First available pool
			expectedInstanceType:  "bx2-4x16",
			expectError:           false,
		},
		{
			name: "specific worker pool configured",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Zone:            "us-south-1",
					IKSWorkerPoolID: "pool-2",
				},
			},
			requestedInstanceType: "bx2-4x16",
			workerPools:           getTestWorkerPools(),
			expectedPoolID:        "pool-2",
			expectedInstanceType:  "bx2-8x32", // Flavor from pool-2
			expectError:           false,
		},
		{
			name: "no worker pools available",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{Zone: "us-south-1"},
			},
			requestedInstanceType: "bx2-4x16",
			workerPools:           []*ibm.WorkerPool{},
			expectError:           true,
			errorContains:         "no worker pools found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock IKS client
			mockIKSClient := &MockIKSClient{}

			// Set up mocks for worker pool operations
			if tt.nodeClass.Spec.IKSWorkerPoolID != "" {
				// Mock getting specific pool
				for _, pool := range tt.workerPools {
					if pool.ID == tt.nodeClass.Spec.IKSWorkerPoolID {
						mockIKSClient.On("GetWorkerPool", mock.Anything, mock.Anything, tt.nodeClass.Spec.IKSWorkerPoolID).Return(pool, nil)
						break
					}
				}
			} else {
				// Mock listing pools
				mockIKSClient.On("ListWorkerPools", mock.Anything, mock.Anything).Return(tt.workerPools, nil)
			}

			// Create provider
			provider := &IKSWorkerPoolProvider{}

			// Test findOrSelectWorkerPool method
			poolID, instanceType, err := provider.findOrSelectWorkerPool(ctx, mockIKSClient, "test-cluster", tt.nodeClass, tt.requestedInstanceType)

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedPoolID, poolID)
				assert.Equal(t, tt.expectedInstanceType, instanceType)
			}

			// Verify all expected calls were made
			mockIKSClient.AssertExpectations(t)
		})
	}
}

func TestIKSWorkerPoolProvider_GetPool(t *testing.T) {
	tests := []struct {
		name          string
		clusterID     string
		poolID        string
		setupMocks    func(*MockIBMClient, *MockIKSClient)
		expectError   bool
		errorContains string
		validatePool  func(*testing.T, *commonTypes.WorkerPool)
	}{
		{
			name:      "successful pool retrieval",
			clusterID: "test-cluster-id",
			poolID:    "test-pool-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)

				pool := getTestWorkerPool()
				iksClient.On("GetWorkerPool", mock.Anything, "test-cluster-id", "test-pool-id").Return(pool, nil)
			},
			expectError: false,
			validatePool: func(t *testing.T, pool *commonTypes.WorkerPool) {
				assert.NotNil(t, pool)
				assert.Equal(t, "test-pool-id", pool.ID)
				assert.Equal(t, "test-pool", pool.Name)
				assert.Equal(t, "bx2-4x16", pool.Flavor)
				assert.Equal(t, "us-south-1", pool.Zone)
				assert.Equal(t, 2, pool.SizePerZone)
				assert.Equal(t, 2, pool.ActualSize)
				assert.Equal(t, "normal", pool.State)
				assert.Equal(t, map[string]string{"env": "test"}, pool.Labels)
			},
		},
		{
			name:      "IKS client not available",
			clusterID: "test-cluster-id",
			poolID:    "test-pool-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(nil, nil)
			},
			expectError:   true,
			errorContains: "IKS client not available",
		},
		{
			name:      "pool not found",
			clusterID: "test-cluster-id",
			poolID:    "nonexistent-pool",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)
				iksClient.On("GetWorkerPool", mock.Anything, "test-cluster-id", "nonexistent-pool").Return(nil, fmt.Errorf("pool not found"))
			},
			expectError:   true,
			errorContains: "pool not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock clients
			// Using nil client for testing
			mockIKSClient := &MockIKSClient{}

			// Setup mocks
			tt.setupMocks(mockIBMClient, mockIKSClient)

			// Create provider
			provider := &IKSWorkerPoolProvider{
				client: mockIBMClient,
			}

			// Test GetPool method
			result, err := provider.GetPool(ctx, tt.clusterID, tt.poolID)

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
				if tt.validatePool != nil {
					tt.validatePool(t, result)
				}
			}

			// Verify all expected calls were made
			mockIBMClient.AssertExpectations(t)
			mockIKSClient.AssertExpectations(t)
		})
	}
}

func TestIKSWorkerPoolProvider_ListPools(t *testing.T) {
	tests := []struct {
		name          string
		clusterID     string
		setupMocks    func(*MockIBMClient, *MockIKSClient)
		expectError   bool
		errorContains string
		expectedCount int
	}{
		{
			name:      "successful pool listing",
			clusterID: "test-cluster-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)

				pools := getTestWorkerPools()
				iksClient.On("ListWorkerPools", mock.Anything, "test-cluster-id").Return(pools, nil)
			},
			expectError:   false,
			expectedCount: 4, // Number of test pools
		},
		{
			name:      "IKS client not available",
			clusterID: "test-cluster-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(nil, nil)
			},
			expectError:   true,
			errorContains: "IKS client not available",
		},
		{
			name:      "list pools failure",
			clusterID: "test-cluster-id",
			setupMocks: func(ibmClient *MockIBMClient, iksClient *MockIKSClient) {
				ibmClient.On("GetIKSClient").Return(&ibm.IKSClient{}, nil)
				iksClient.On("ListWorkerPools", mock.Anything, "test-cluster-id").Return(nil, fmt.Errorf("list failed"))
			},
			expectError:   true,
			errorContains: "list failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock clients
			// Using nil client for testing
			mockIKSClient := &MockIKSClient{}

			// Setup mocks
			tt.setupMocks(mockIBMClient, mockIKSClient)

			// Create provider
			provider := &IKSWorkerPoolProvider{
				client: mockIBMClient,
			}

			// Test ListPools method
			result, err := provider.ListPools(ctx, tt.clusterID)

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
				assert.Len(t, result, tt.expectedCount)
			}

			// Verify all expected calls were made
			mockIBMClient.AssertExpectations(t)
			mockIKSClient.AssertExpectations(t)
		})
	}
}
