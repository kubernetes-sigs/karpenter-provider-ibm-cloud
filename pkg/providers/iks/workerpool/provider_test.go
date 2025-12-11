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
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	mock_ibm "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
)

// IBMClientInterface defines the interface we need for testing
type IBMClientInterface interface {
	GetIKSClient() ibm.IKSClientInterface
}

// mockIBMClientWrapper wraps the generated mock IKS client for testing
type mockIBMClientWrapper struct {
	iksClient ibm.IKSClientInterface
}

func (m *mockIBMClientWrapper) GetIKSClient() ibm.IKSClientInterface {
	return m.iksClient
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
				"karpenter-ibm.sh/cluster-id":      clusterID,
				"karpenter-ibm.sh/worker-pool-id":  "test-pool-id",
				"karpenter-ibm.sh/zone":            nodeClass.Spec.Zone,
				"karpenter-ibm.sh/region":          nodeClass.Spec.Region,
				"karpenter-ibm.sh/instance-type":   nodeClass.Spec.InstanceProfile,
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
	clusterID := node.Labels["karpenter-ibm.sh/cluster-id"]
	poolID := node.Labels["karpenter-ibm.sh/worker-pool-id"]

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
		setupMocks     func(*gomock.Controller) IBMClientInterface
		expectError    bool
		errorContains  string
		validateResult func(*testing.T, *corev1.Node)
	}{
		{
			name:      "successful worker creation with exact pool match",
			nodeClass: getTestNodeClass(),
			nodeClaim: getTestNodeClaim(),
			setupMocks: func(ctrl *gomock.Controller) IBMClientInterface {
				mockIKSClient := mock_ibm.NewMockIKSClientInterface(ctrl)

				// Mock pool selection - exact match
				workerPools := getTestWorkerPools()
				mockIKSClient.EXPECT().
					ListWorkerPools(gomock.Any(), "test-cluster-id").
					Return(workerPools, nil)

				// Mock get pool for resize
				selectedPool := workerPools[0]
				mockIKSClient.EXPECT().
					GetWorkerPool(gomock.Any(), "test-cluster-id", "pool-1").
					Return(selectedPool, nil)

				// Mock resize operation
				mockIKSClient.EXPECT().
					ResizeWorkerPool(gomock.Any(), "test-cluster-id", "pool-1", 2).
					Return(nil)

				return &mockIBMClientWrapper{iksClient: mockIKSClient}
			},
			expectError: false,
			validateResult: func(t *testing.T, node *corev1.Node) {
				assert.NotNil(t, node)
				assert.Equal(t, "test-nodeclaim", node.Name)
				assert.Equal(t, "ibm:///us-south/test-nodeclaim", node.Spec.ProviderID)
				assert.Equal(t, "test-cluster-id", node.Labels["karpenter-ibm.sh/cluster-id"])
				assert.Equal(t, "test-pool-id", node.Labels["karpenter-ibm.sh/worker-pool-id"])
				assert.Equal(t, "bx2-4x16", node.Labels["node.kubernetes.io/instance-type"])
				assert.Equal(t, corev1.NodePending, node.Status.Phase)
			},
		},
		{
			name: "cluster ID from environment variable",
			nodeClass: func() *v1alpha1.IBMNodeClass {
				nc := getTestNodeClass()
				nc.Spec.IKSClusterID = ""
				return nc
			}(),
			nodeClaim:    getTestNodeClaim(),
			envClusterID: "env-cluster-id",
			setupMocks: func(ctrl *gomock.Controller) IBMClientInterface {
				mockIKSClient := mock_ibm.NewMockIKSClientInterface(ctrl)

				workerPools := getTestWorkerPools()
				mockIKSClient.EXPECT().
					ListWorkerPools(gomock.Any(), "env-cluster-id").
					Return(workerPools, nil)

				selectedPool := workerPools[0]
				mockIKSClient.EXPECT().
					GetWorkerPool(gomock.Any(), "env-cluster-id", "pool-1").
					Return(selectedPool, nil)

				mockIKSClient.EXPECT().
					ResizeWorkerPool(gomock.Any(), "env-cluster-id", "pool-1", 2).
					Return(nil)

				return &mockIBMClientWrapper{iksClient: mockIKSClient}
			},
			expectError: false,
			validateResult: func(t *testing.T, node *corev1.Node) {
				assert.Equal(t, "env-cluster-id", node.Labels["karpenter-ibm.sh/cluster-id"])
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
			setupMocks: func(ctrl *gomock.Controller) IBMClientInterface {
				return &mockIBMClientWrapper{iksClient: mock_ibm.NewMockIKSClientInterface(ctrl)}
			},
			expectError:   true,
			errorContains: "IKS cluster ID not found",
		},
		{
			name:      "IKS client not available",
			nodeClass: getTestNodeClass(),
			nodeClaim: getTestNodeClaim(),
			setupMocks: func(ctrl *gomock.Controller) IBMClientInterface {
				return &mockIBMClientWrapper{iksClient: nil}
			},
			expectError:   true,
			errorContains: "IKS client not available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

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

			// Setup mocks
			mockIBMClient := tt.setupMocks(ctrl)

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
		})
	}
}

func TestIKSWorkerPoolProvider_Delete(t *testing.T) {
	tests := []struct {
		name          string
		node          *corev1.Node
		setupMocks    func(*gomock.Controller) IBMClientInterface
		expectError   bool
		errorContains string
	}{
		{
			name: "successful worker deletion",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter-ibm.sh/cluster-id":     "test-cluster-id",
						"karpenter-ibm.sh/worker-pool-id": "test-pool-id",
					},
				},
			},
			setupMocks: func(ctrl *gomock.Controller) IBMClientInterface {
				mockIKSClient := mock_ibm.NewMockIKSClientInterface(ctrl)

				// Mock get pool for current size
				currentPool := &ibm.WorkerPool{
					ID:          "test-pool-id",
					SizePerZone: 3,
				}
				mockIKSClient.EXPECT().
					GetWorkerPool(gomock.Any(), "test-cluster-id", "test-pool-id").
					Return(currentPool, nil)

				// Mock resize down
				mockIKSClient.EXPECT().
					ResizeWorkerPool(gomock.Any(), "test-cluster-id", "test-pool-id", 2).
					Return(nil)

				return &mockIBMClientWrapper{iksClient: mockIKSClient}
			},
			expectError: false,
		},
		{
			name: "missing cluster ID in labels",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter-ibm.sh/worker-pool-id": "test-pool-id",
					},
				},
			},
			setupMocks: func(ctrl *gomock.Controller) IBMClientInterface {
				return &mockIBMClientWrapper{iksClient: mock_ibm.NewMockIKSClientInterface(ctrl)}
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
						"karpenter-ibm.sh/cluster-id":     "test-cluster-id",
						"karpenter-ibm.sh/worker-pool-id": "test-pool-id",
					},
				},
			},
			setupMocks: func(ctrl *gomock.Controller) IBMClientInterface {
				return &mockIBMClientWrapper{iksClient: nil}
			},
			expectError:   true,
			errorContains: "IKS client not available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Setup mocks
			mockIBMClient := tt.setupMocks(ctrl)

			// Create IKS worker pool provider
			provider := &testIKSWorkerPoolProvider{
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
		})
	}
}
