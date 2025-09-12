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
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

// Test provider creation - this tests the NewIKSWorkerPoolProvider function directly
func TestNewIKSWorkerPoolProvider_Unit(t *testing.T) {
	t.Run("successful creation", func(t *testing.T) {
		scheme := runtime.NewScheme()
		require.NoError(t, v1alpha1.AddToScheme(scheme))
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		client := &ibm.Client{}
		provider, err := NewIKSWorkerPoolProvider(client, kubeClient)

		assert.NoError(t, err)
		assert.NotNil(t, provider)
	})

	t.Run("nil client", func(t *testing.T) {
		scheme := runtime.NewScheme()
		require.NoError(t, v1alpha1.AddToScheme(scheme))
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		provider, err := NewIKSWorkerPoolProvider(nil, kubeClient)

		assert.Error(t, err)
		assert.Nil(t, provider)
		assert.Contains(t, err.Error(), "IBM client cannot be nil")
	})

	t.Run("nil kube client", func(t *testing.T) {
		client := &ibm.Client{}
		provider, err := NewIKSWorkerPoolProvider(client, nil)

		// The constructor allows nil kubeClient, error checking happens in methods
		assert.NoError(t, err)
		assert.NotNil(t, provider)
	})
}

// Test Create method early error cases
func TestIKSWorkerPoolProvider_Create_ErrorCases(t *testing.T) {
	ctx := context.Background()

	t.Run("missing kubernetes client", func(t *testing.T) {
		provider := &IKSWorkerPoolProvider{
			client:     &ibm.Client{},
			kubeClient: nil,
		}

		nodeClaim := &karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{Name: "test-nodeclass"},
			},
		}

		result, err := provider.Create(ctx, nodeClaim, nil)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "kubernetes client not set")
	})

	t.Run("nodeclass not found", func(t *testing.T) {
		// Create fake kubernetes client without the nodeclass
		scheme := runtime.NewScheme()
		require.NoError(t, v1alpha1.AddToScheme(scheme))
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		provider := &IKSWorkerPoolProvider{
			client:     &ibm.Client{},
			kubeClient: kubeClient,
		}

		nodeClaim := &karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{Name: "nonexistent-nodeclass"},
			},
		}

		result, err := provider.Create(ctx, nodeClaim, nil)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "getting NodeClass nonexistent-nodeclass")
	})

	t.Run("missing cluster ID", func(t *testing.T) {
		// Create nodeclass without cluster ID
		nodeClass := &v1alpha1.IBMNodeClass{
			ObjectMeta: metav1.ObjectMeta{Name: "test-nodeclass"},
			Spec:       v1alpha1.IBMNodeClassSpec{
				// No IKSClusterID specified
			},
		}

		scheme := runtime.NewScheme()
		require.NoError(t, v1alpha1.AddToScheme(scheme))
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nodeClass).Build()

		provider := &IKSWorkerPoolProvider{
			client:     &ibm.Client{},
			kubeClient: kubeClient,
		}

		nodeClaim := &karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{Name: "test-nodeclass"},
			},
		}

		// Clear environment variable to ensure it's not used
		require.NoError(t, os.Unsetenv("IKS_CLUSTER_ID"))

		result, err := provider.Create(ctx, nodeClaim, nil)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "IKS cluster ID not found")
	})

	t.Run("cluster ID from environment variable", func(t *testing.T) {
		// Set environment variable
		require.NoError(t, os.Setenv("IKS_CLUSTER_ID", "env-cluster-id"))
		defer func() { _ = os.Unsetenv("IKS_CLUSTER_ID") }()

		// Create nodeclass without cluster ID
		nodeClass := &v1alpha1.IBMNodeClass{
			ObjectMeta: metav1.ObjectMeta{Name: "test-nodeclass"},
			Spec: v1alpha1.IBMNodeClassSpec{
				// No IKSClusterID specified, should use env var
				InstanceProfile: "bx2.4x16",
				Zone:            "us-south-1",
			},
		}

		scheme := runtime.NewScheme()
		require.NoError(t, v1alpha1.AddToScheme(scheme))
		kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nodeClass).Build()

		// Use nil client to avoid panic from empty struct
		provider := &IKSWorkerPoolProvider{
			client:     nil,
			kubeClient: kubeClient,
		}

		nodeClaim := &karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{Name: "test-nodeclass"},
			},
		}

		result, err := provider.Create(ctx, nodeClaim, nil)

		// Should error due to nil client
		assert.Error(t, err)
		assert.Nil(t, result)
		// Should not error due to missing cluster ID - the env var should be found
		assert.NotContains(t, err.Error(), "IKS cluster ID not found")
		// Should fail at the nil client check
		assert.Contains(t, err.Error(), "IBM client is not initialized")
	})
}

// Test Delete method error cases
func TestIKSWorkerPoolProvider_Delete_ErrorCases(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		nodeLabels    map[string]string
		expectedError string
	}{
		{
			name:          "missing cluster ID label",
			nodeLabels:    map[string]string{"karpenter.ibm.sh/worker-pool-id": "pool-123"},
			expectedError: "cluster ID or pool ID not found in node labels",
		},
		{
			name:          "missing pool ID label",
			nodeLabels:    map[string]string{"karpenter.ibm.sh/cluster-id": "cluster-123"},
			expectedError: "cluster ID or pool ID not found in node labels",
		},
		{
			name:          "both labels missing",
			nodeLabels:    map[string]string{},
			expectedError: "cluster ID or pool ID not found in node labels",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, v1alpha1.AddToScheme(scheme))
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			provider := &IKSWorkerPoolProvider{
				client:     nil,
				kubeClient: kubeClient,
			}

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: tt.nodeLabels,
				},
			}

			err := provider.Delete(ctx, node)

			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

// Test ResizePool method with nil client
func TestIKSWorkerPoolProvider_ResizePool_NilClient(t *testing.T) {
	ctx := context.Background()

	provider := &IKSWorkerPoolProvider{
		client: nil,
	}

	err := provider.ResizePool(ctx, "test-cluster", "pool-1", 5)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "IBM client is not initialized")
}

// Test GetPool method with nil client
func TestIKSWorkerPoolProvider_GetPool_NilClient(t *testing.T) {
	ctx := context.Background()

	provider := &IKSWorkerPoolProvider{
		client: nil,
	}

	pool, err := provider.GetPool(ctx, "test-cluster", "pool-1")
	assert.Error(t, err)
	assert.Nil(t, pool)
	assert.Contains(t, err.Error(), "IBM client is not initialized")
}

// Test ListPools method with nil client
func TestIKSWorkerPoolProvider_ListPools_NilClient(t *testing.T) {
	ctx := context.Background()

	provider := &IKSWorkerPoolProvider{
		client: nil,
	}

	pools, err := provider.ListPools(ctx, "test-cluster")
	assert.Error(t, err)
	assert.Nil(t, pools)
	assert.Contains(t, err.Error(), "IBM client is not initialized")
}

// Test Get method - not implemented
func TestIKSWorkerPoolProvider_Get_NotImplemented(t *testing.T) {
	provider := &IKSWorkerPoolProvider{}

	node, err := provider.Get(context.Background(), "test-provider-id")
	assert.Error(t, err)
	assert.Nil(t, node)
	assert.Contains(t, err.Error(), "not implemented")
}

// Test List method - not implemented
func TestIKSWorkerPoolProvider_List_NotImplemented(t *testing.T) {
	provider := &IKSWorkerPoolProvider{}

	nodes, err := provider.List(context.Background())
	assert.Error(t, err)
	assert.Nil(t, nodes)
	assert.Contains(t, err.Error(), "not implemented")
}

// Test node label based deletion logic
func TestIKSWorkerPoolProvider_NodeLabelDeletion(t *testing.T) {
	tests := []struct {
		name          string
		nodeLabels    map[string]string
		expectsError  bool
		expectedError string
	}{
		{
			name: "valid node labels",
			nodeLabels: map[string]string{
				"karpenter.ibm.sh/cluster-id":     "cluster-123",
				"karpenter.ibm.sh/worker-pool-id": "pool-456",
			},
			expectsError:  true, // Will error due to nil client
			expectedError: "IBM client is not initialized",
		},
		{
			name:          "missing cluster label",
			nodeLabels:    map[string]string{"karpenter.ibm.sh/worker-pool-id": "pool-456"},
			expectsError:  true,
			expectedError: "cluster ID or pool ID not found in node labels",
		},
		{
			name:          "missing pool label",
			nodeLabels:    map[string]string{"karpenter.ibm.sh/cluster-id": "cluster-123"},
			expectsError:  true,
			expectedError: "cluster ID or pool ID not found in node labels",
		},
		{
			name:          "no labels",
			nodeLabels:    map[string]string{},
			expectsError:  true,
			expectedError: "cluster ID or pool ID not found in node labels",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, v1alpha1.AddToScheme(scheme))
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

			provider := &IKSWorkerPoolProvider{
				client:     nil,
				kubeClient: kubeClient,
			}

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: tt.nodeLabels,
				},
			}

			err := provider.Delete(context.Background(), node)

			if tt.expectsError {
				assert.Error(t, err)
				if tt.expectedError != "" {
					assert.Contains(t, err.Error(), tt.expectedError)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Test cluster ID resolution logic
func TestClusterIDResolution_Unit(t *testing.T) {
	tests := []struct {
		name        string
		nodeClassID string
		envID       string
		expectError bool
	}{
		{
			name:        "use nodeclass cluster ID",
			nodeClassID: "nodeclass-cluster",
			envID:       "env-cluster",
			expectError: false,
		},
		{
			name:        "use env cluster ID when nodeclass empty",
			nodeClassID: "",
			envID:       "env-cluster",
			expectError: false,
		},
		{
			name:        "error when both empty",
			nodeClassID: "",
			envID:       "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment
			if tt.envID != "" {
				require.NoError(t, os.Setenv("IKS_CLUSTER_ID", tt.envID))
			} else {
				_ = os.Unsetenv("IKS_CLUSTER_ID")
			}
			defer func() { _ = os.Unsetenv("IKS_CLUSTER_ID") }()

			// Create nodeclass
			nodeClass := &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-nodeclass"},
				Spec: v1alpha1.IBMNodeClassSpec{
					IKSClusterID:    tt.nodeClassID,
					InstanceProfile: "bx2.4x16",
					Zone:            "us-south-1",
				},
			}

			scheme := runtime.NewScheme()
			require.NoError(t, v1alpha1.AddToScheme(scheme))
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nodeClass).Build()

			provider := &IKSWorkerPoolProvider{
				client:     nil,
				kubeClient: kubeClient,
			}

			nodeClaim := &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
				Spec: karpv1.NodeClaimSpec{
					NodeClassRef: &karpv1.NodeClassReference{Name: "test-nodeclass"},
				},
			}

			_, err := provider.Create(context.Background(), nodeClaim, nil)

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "IKS cluster ID not found")
			} else {
				// Should not error due to cluster ID resolution, but may error later
				if err != nil {
					assert.NotContains(t, err.Error(), "IKS cluster ID not found")
				}
			}
		})
	}
}
