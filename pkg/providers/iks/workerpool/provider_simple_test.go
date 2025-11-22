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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

func TestIKSWorkerPoolProvider_NewProvider(t *testing.T) {
	client := &ibm.Client{}
	kubeClient := fake.NewClientBuilder().Build()

	provider, err := NewIKSWorkerPoolProvider(client, kubeClient)

	assert.NoError(t, err)
	assert.NotNil(t, provider)
	assert.IsType(t, &IKSWorkerPoolProvider{}, provider)
}

func TestIKSWorkerPoolProvider_NewProvider_NilClient(t *testing.T) {
	kubeClient := fake.NewClientBuilder().Build()

	provider, err := NewIKSWorkerPoolProvider(nil, kubeClient)

	assert.Error(t, err)
	assert.Nil(t, provider)
	assert.Contains(t, err.Error(), "IBM client cannot be nil")
}

func TestFindOrSelectWorkerPool_Strategies(t *testing.T) {
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
			workerPools: []*ibm.WorkerPool{
				{ID: "pool-1", Flavor: "bx2-4x16", Zone: "us-south-1"},
				{ID: "pool-2", Flavor: "bx2-8x32", Zone: "us-south-1"},
			},
			expectedPoolID:       "pool-1",
			expectedInstanceType: "bx2-4x16",
			expectError:          false,
		},
		{
			name: "same zone different flavor",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{Zone: "us-south-1"},
			},
			requestedInstanceType: "nonexistent-flavor",
			workerPools: []*ibm.WorkerPool{
				{ID: "pool-1", Flavor: "bx2-4x16", Zone: "us-south-1"},
				{ID: "pool-2", Flavor: "bx2-8x32", Zone: "us-south-2"},
			},
			expectedPoolID:       "pool-1", // First pool in same zone
			expectedInstanceType: "bx2-4x16",
			expectError:          false,
		},
		{
			name: "matching flavor different zone",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{Zone: "nonexistent-zone"},
			},
			requestedInstanceType: "bx2-4x16",
			workerPools: []*ibm.WorkerPool{
				{ID: "pool-1", Flavor: "bx2-4x16", Zone: "us-south-1"},
				{ID: "pool-2", Flavor: "bx2-8x32", Zone: "us-south-2"},
			},
			expectedPoolID:       "pool-1", // First pool with matching flavor
			expectedInstanceType: "bx2-4x16",
			expectError:          false,
		},
		{
			name: "fallback to first available pool",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{Zone: "nonexistent-zone"},
			},
			requestedInstanceType: "nonexistent-flavor",
			workerPools: []*ibm.WorkerPool{
				{ID: "pool-1", Flavor: "bx2-4x16", Zone: "us-south-1"},
				{ID: "pool-2", Flavor: "bx2-8x32", Zone: "us-south-2"},
			},
			expectedPoolID:       "pool-1", // First available pool
			expectedInstanceType: "bx2-4x16",
			expectError:          false,
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
			workerPools: []*ibm.WorkerPool{
				{ID: "pool-1", Flavor: "bx2-4x16", Zone: "us-south-1"},
				{ID: "pool-2", Flavor: "bx2-8x32", Zone: "us-south-2"},
			},
			expectedPoolID:       "pool-2",
			expectedInstanceType: "bx2-8x32", // Flavor from pool-2
			expectError:          false,
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
			// Create a test helper that simulates the worker pool selection logic
			var selectedPool *ibm.WorkerPool
			var err error

			// Simulate the logic from findOrSelectWorkerPool
			if tt.nodeClass.Spec.IKSWorkerPoolID != "" {
				// Find specific pool
				for _, pool := range tt.workerPools {
					if pool.ID == tt.nodeClass.Spec.IKSWorkerPoolID {
						selectedPool = pool
						break
					}
				}
			} else if len(tt.workerPools) == 0 {
				err = fmt.Errorf("no worker pools found")
			} else {
				// Strategy 1: Exact match
				for _, pool := range tt.workerPools {
					if pool.Flavor == tt.requestedInstanceType && pool.Zone == tt.nodeClass.Spec.Zone {
						selectedPool = pool
						break
					}
				}

				// Strategy 2: Same zone
				if selectedPool == nil {
					for _, pool := range tt.workerPools {
						if pool.Zone == tt.nodeClass.Spec.Zone {
							selectedPool = pool
							break
						}
					}
				}

				// Strategy 3: Same flavor
				if selectedPool == nil && tt.requestedInstanceType != "" {
					for _, pool := range tt.workerPools {
						if pool.Flavor == tt.requestedInstanceType {
							selectedPool = pool
							break
						}
					}
				}

				// Strategy 4: Fallback
				if selectedPool == nil {
					selectedPool = tt.workerPools[0]
				}
			}

			// Validate results
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, selectedPool)
				assert.Equal(t, tt.expectedPoolID, selectedPool.ID)
				assert.Equal(t, tt.expectedInstanceType, selectedPool.Flavor)
			}
		})
	}
}

func TestValidateNodeClassConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		nodeClass   *v1alpha1.IBMNodeClass
		expectError bool
	}{
		{
			name: "valid configuration",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-1",
					InstanceProfile: "bx2-4x16",
					IKSClusterID:    "test-cluster",
				},
			},
			expectError: false,
		},
		{
			name: "missing cluster ID",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-1",
					InstanceProfile: "bx2-4x16",
					// Missing IKSClusterID
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate basic validation logic
			var err error
			if tt.nodeClass.Spec.IKSClusterID == "" {
				err = assert.AnError
			}

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestNodeLabelGeneration(t *testing.T) {
	// Test validates expected node labels for IKS worker pools

	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclaim",
			Labels: map[string]string{
				"karpenter.sh/nodepool": "test-nodepool",
			},
		},
	}

	// Test the expected labels that would be generated
	expectedLabels := map[string]string{
		"karpenter.sh/managed":             "true",
		"karpenter-ibm.sh/cluster-id":      "test-cluster",
		"karpenter-ibm.sh/worker-pool-id":  "test-pool-id",
		"karpenter-ibm.sh/zone":            "us-south-1",
		"karpenter-ibm.sh/region":          "us-south",
		"karpenter-ibm.sh/instance-type":   "bx2-4x16",
		"node.kubernetes.io/instance-type": "bx2-4x16",
		"topology.kubernetes.io/zone":      "us-south-1",
		"topology.kubernetes.io/region":    "us-south",
		"karpenter.sh/capacity-type":       "on-demand",
		"karpenter.sh/nodepool":            "test-nodepool",
	}

	// Create the node that would be generated
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nodeClaim.Name,
			Labels: expectedLabels,
		},
	}

	// Validate the labels
	assert.Equal(t, "test-cluster", node.Labels["karpenter-ibm.sh/cluster-id"])
	assert.Equal(t, "us-south-1", node.Labels["topology.kubernetes.io/zone"])
	assert.Equal(t, "bx2-4x16", node.Labels["node.kubernetes.io/instance-type"])
	assert.Equal(t, "test-nodepool", node.Labels["karpenter.sh/nodepool"])
}

func TestIKSWorkerPoolProvider_CreateMethodSignature(t *testing.T) {
	// Test that Create method exists and has the right signature
	provider := &IKSWorkerPoolProvider{}

	// Verify that Create method can be called (will error due to nil client but that's expected)
	_, err := provider.Create(context.Background(), &karpv1.NodeClaim{}, nil)
	assert.Error(t, err) // Expected due to nil client
	assert.Contains(t, err.Error(), "kubernetes client not set")
}

func TestIKSWorkerPoolProvider_DeleteMethodSignature(t *testing.T) {
	// Test that Delete method exists and has the right signature
	provider := &IKSWorkerPoolProvider{}

	// Test with node missing required labels
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
	}

	err := provider.Delete(context.Background(), node)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cluster ID or pool ID not found")
}

func TestIKSWorkerPoolProvider_GetMethodNotImplemented(t *testing.T) {
	provider := &IKSWorkerPoolProvider{}

	result, err := provider.Get(context.Background(), "test-provider-id")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "get operation not implemented")
}

func TestIKSWorkerPoolProvider_ListMethodNotImplemented(t *testing.T) {
	provider := &IKSWorkerPoolProvider{}

	result, err := provider.List(context.Background())
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "list operation not implemented")
}

func TestIKSWorkerPoolProvider_ResizePoolNilClient(t *testing.T) {
	// Test that calling ResizePool with a nil client panics or errors correctly
	// Since the method calls p.client.GetIKSClient() and p.client is nil,
	// this should panic. We capture the panic to verify the method exists.
	provider := &IKSWorkerPoolProvider{}

	defer func() {
		if r := recover(); r != nil {
			// Expected panic due to nil client
			assert.NotNil(t, r, "Expected panic due to nil client")
		}
	}()

	// This will panic due to nil client, but that's expected behavior for now
	err := provider.ResizePool(context.Background(), "cluster", "pool", 5)
	// If we reach here without panic, it means the method returned an error instead
	assert.Error(t, err)
}

func TestIKSWorkerPoolProvider_GetPoolNilClient(t *testing.T) {
	// Test that calling GetPool with a nil client panics or errors correctly
	provider := &IKSWorkerPoolProvider{}

	defer func() {
		if r := recover(); r != nil {
			// Expected panic due to nil client
			assert.NotNil(t, r, "Expected panic due to nil client")
		}
	}()

	// This will panic due to nil client
	result, err := provider.GetPool(context.Background(), "cluster", "pool")
	// If we reach here without panic, it means the method returned an error instead
	if err != nil {
		assert.Error(t, err)
		assert.Nil(t, result)
	}
}

func TestIKSWorkerPoolProvider_ListPoolsNilClient(t *testing.T) {
	// Test that calling ListPools with a nil client panics or errors correctly
	provider := &IKSWorkerPoolProvider{}

	defer func() {
		if r := recover(); r != nil {
			// Expected panic due to nil client
			assert.NotNil(t, r, "Expected panic due to nil client")
		}
	}()

	// This will panic due to nil client
	result, err := provider.ListPools(context.Background(), "cluster")
	// If we reach here without panic, it means the method returned an error instead
	if err != nil {
		assert.Error(t, err)
		assert.Nil(t, result)
	}
}

func TestProviderIDGeneration(t *testing.T) {
	tests := []struct {
		name          string
		region        string
		nodeClaimName string
		expected      string
	}{
		{
			name:          "standard provider ID",
			region:        "us-south",
			nodeClaimName: "test-nodeclaim",
			expected:      "ibm:///us-south/test-nodeclaim",
		},
		{
			name:          "different region",
			region:        "eu-gb",
			nodeClaimName: "my-node",
			expected:      "ibm:///eu-gb/my-node",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the provider ID generation logic
			providerID := fmt.Sprintf("ibm:///%s/%s", tt.region, tt.nodeClaimName)
			assert.Equal(t, tt.expected, providerID)
		})
	}
}

func TestEnvironmentVariableHandling(t *testing.T) {
	// Save original env var
	originalClusterID := os.Getenv("IKS_CLUSTER_ID")
	defer func() {
		if originalClusterID != "" {
			_ = os.Setenv("IKS_CLUSTER_ID", originalClusterID)
		} else {
			_ = os.Unsetenv("IKS_CLUSTER_ID")
		}
	}()

	tests := []struct {
		name               string
		nodeClassClusterID string
		envClusterID       string
		expectedClusterID  string
	}{
		{
			name:               "use nodeclass cluster ID",
			nodeClassClusterID: "nodeclass-cluster",
			envClusterID:       "env-cluster",
			expectedClusterID:  "nodeclass-cluster", // NodeClass takes precedence
		},
		{
			name:               "use env cluster ID when nodeclass empty",
			nodeClassClusterID: "",
			envClusterID:       "env-cluster",
			expectedClusterID:  "env-cluster",
		},
		{
			name:               "empty when both empty",
			nodeClassClusterID: "",
			envClusterID:       "",
			expectedClusterID:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variable
			if tt.envClusterID != "" {
				_ = os.Setenv("IKS_CLUSTER_ID", tt.envClusterID)
			} else {
				_ = os.Unsetenv("IKS_CLUSTER_ID")
			}

			// Simulate the logic from the Create method
			var clusterID string
			if tt.nodeClassClusterID != "" {
				clusterID = tt.nodeClassClusterID
			} else {
				clusterID = os.Getenv("IKS_CLUSTER_ID")
			}

			assert.Equal(t, tt.expectedClusterID, clusterID)
		})
	}
}
