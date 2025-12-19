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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	mock_ibm "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
)

func TestFindOrSelectWorkerPool_ConfiguredPoolID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	// When a specific pool ID is configured, it should use that pool
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone:            "us-south-1",
			IKSWorkerPoolID: "configured-pool-id",
		},
	}

	expectedPool := &ibm.WorkerPool{
		ID:          "configured-pool-id",
		Name:        "my-configured-pool",
		Flavor:      "cx2-4x8",
		Zone:        "us-south-2",
		SizePerZone: 3,
		State:       "normal",
	}

	mockIKS.EXPECT().
		GetWorkerPool(ctx, "test-cluster", "configured-pool-id").
		Return(expectedPool, nil)

	provider := &IKSWorkerPoolProvider{}
	poolName, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "my-configured-pool", poolName) // v1 API uses pool name, not ID
	assert.Equal(t, "cx2-4x8", flavor)
}

func TestFindOrSelectWorkerPool_ExactMatch_FlavorAndZone(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone: "us-south-1",
		},
	}

	pools := []*ibm.WorkerPool{
		{
			ID:          "pool-1",
			Name:        "exact-match",
			Flavor:      "bx2-4x16",
			Zone:        "us-south-1",
			SizePerZone: 2,
			State:       "normal",
		},
		{
			ID:          "pool-2",
			Name:        "different-flavor",
			Flavor:      "bx2-8x32",
			Zone:        "us-south-1",
			SizePerZone: 2,
			State:       "normal",
		},
		{
			ID:          "pool-3",
			Name:        "different-zone",
			Flavor:      "bx2-4x16",
			Zone:        "us-south-2",
			SizePerZone: 2,
			State:       "normal",
		},
	}

	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return(pools, nil)

	provider := &IKSWorkerPoolProvider{}
	poolName, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "exact-match", poolName) // v1 API uses pool name
	assert.Equal(t, "bx2-4x16", flavor)
}

func TestFindOrSelectWorkerPool_SameZone_DifferentFlavor(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone: "us-south-1",
		},
	}

	// No exact match, but pool-2 is in the same zone
	pools := []*ibm.WorkerPool{
		{
			ID:          "pool-1",
			Name:        "different-zone-and-flavor",
			Flavor:      "cx2-2x4",
			Zone:        "us-south-2",
			SizePerZone: 1,
			State:       "normal",
		},
		{
			ID:          "pool-2",
			Name:        "same-zone-different-flavor",
			Flavor:      "bx2-8x32",
			Zone:        "us-south-1",
			SizePerZone: 3,
			State:       "normal",
		},
	}

	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return(pools, nil)

	provider := &IKSWorkerPoolProvider{}
	poolName, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "same-zone-different-flavor", poolName) // v1 API uses pool name
	assert.Equal(t, "bx2-8x32", flavor)                     // Uses pool's flavor, not requested
}

func TestFindOrSelectWorkerPool_MatchingFlavor_DifferentZone(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone: "us-south-1",
		},
	}

	// No same-zone match, but pool-2 has matching flavor
	pools := []*ibm.WorkerPool{
		{
			ID:          "pool-1",
			Name:        "different-everything",
			Flavor:      "cx2-2x4",
			Zone:        "us-south-2",
			SizePerZone: 1,
			State:       "normal",
		},
		{
			ID:          "pool-2",
			Name:        "matching-flavor-different-zone",
			Flavor:      "bx2-4x16",
			Zone:        "us-south-3",
			SizePerZone: 2,
			State:       "normal",
		},
	}

	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return(pools, nil)

	provider := &IKSWorkerPoolProvider{}
	poolName, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "matching-flavor-different-zone", poolName) // v1 API uses pool name
	assert.Equal(t, "bx2-4x16", flavor)
}

func TestFindOrSelectWorkerPool_FallbackToFirstPool(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone: "us-south-1",
		},
	}

	// No matches at all - should use first pool as fallback
	pools := []*ibm.WorkerPool{
		{
			ID:          "pool-1",
			Name:        "fallback-pool",
			Flavor:      "cx2-2x4",
			Zone:        "eu-de-1",
			SizePerZone: 1,
			State:       "normal",
		},
		{
			ID:          "pool-2",
			Name:        "another-pool",
			Flavor:      "mx2-8x64",
			Zone:        "eu-de-2",
			SizePerZone: 2,
			State:       "normal",
		},
	}

	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return(pools, nil)

	provider := &IKSWorkerPoolProvider{}
	poolName, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "fallback-pool", poolName) // First pool's name
	assert.Equal(t, "cx2-2x4", flavor)         // First pool's flavor
}

func TestFindOrSelectWorkerPool_NoWorkerPools(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone: "us-south-1",
		},
	}

	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return([]*ibm.WorkerPool{}, nil)

	provider := &IKSWorkerPoolProvider{}
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no worker pools found")
	assert.Empty(t, poolID)
	assert.Empty(t, flavor)
}

func TestFindOrSelectWorkerPool_ListPoolsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone: "us-south-1",
		},
	}

	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return(nil, fmt.Errorf("API error: rate limit exceeded"))

	provider := &IKSWorkerPoolProvider{}
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "listing worker pools")
	assert.Contains(t, err.Error(), "rate limit exceeded")
	assert.Empty(t, poolID)
	assert.Empty(t, flavor)
}

func TestFindOrSelectWorkerPool_GetConfiguredPoolError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone:            "us-south-1",
			IKSWorkerPoolID: "nonexistent-pool",
		},
	}

	mockIKS.EXPECT().
		GetWorkerPool(ctx, "test-cluster", "nonexistent-pool").
		Return(nil, fmt.Errorf("pool not found"))

	provider := &IKSWorkerPoolProvider{}
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "getting configured worker pool")
	assert.Contains(t, err.Error(), "pool not found")
	assert.Empty(t, poolID)
	assert.Empty(t, flavor)
}

func TestFindOrSelectWorkerPool_EmptyRequestedInstanceType(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone: "us-south-1",
		},
	}

	// With empty requested instance type, should match by zone only
	pools := []*ibm.WorkerPool{
		{
			ID:          "pool-1",
			Name:        "zone-match",
			Flavor:      "bx2-4x16",
			Zone:        "us-south-1",
			SizePerZone: 2,
			State:       "normal",
		},
	}

	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return(pools, nil)

	provider := &IKSWorkerPoolProvider{}
	poolName, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "")

	assert.NoError(t, err)
	assert.Equal(t, "zone-match", poolName) // v1 API uses pool name
	assert.Equal(t, "bx2-4x16", flavor)
}

func TestFindOrSelectWorkerPool_DynamicPoolCreation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone:   "us-south-1",
			Subnet: "subnet-123",
			VPC:    "vpc-456",
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{
				Enabled:    true,
				NamePrefix: "karp",
				Labels: map[string]string{
					"env": "test",
				},
			},
		},
	}

	// No matching pools exist
	pools := []*ibm.WorkerPool{
		{
			ID:          "pool-1",
			Name:        "existing-pool",
			Flavor:      "cx2-2x4",
			Zone:        "us-south-2", // Different zone
			SizePerZone: 2,
			State:       "normal",
		},
	}

	createdPool := &ibm.WorkerPool{
		ID:          "new-pool-id",
		Name:        "karp-bx2-4x16-abc123",
		Flavor:      "bx2-4x16",
		Zone:        "us-south-1",
		SizePerZone: 0,
		State:       "normal",
	}

	// Expect listing pools first
	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return(pools, nil)

	// Expect pool creation with dynamic pool config
	mockIKS.EXPECT().
		CreateWorkerPool(ctx, "test-cluster", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, req *ibm.WorkerPoolCreateRequest) (*ibm.WorkerPool, error) {
			// Verify the request has correct values
			assert.Equal(t, "bx2-4x16", req.Flavor)
			assert.Equal(t, 0, req.SizePerZone)
			assert.Equal(t, "us-south-1", req.Zones[0].ID)
			assert.Equal(t, "subnet-123", req.Zones[0].SubnetID)
			assert.Equal(t, "true", req.Labels[KarpenterManagedLabel])
			assert.Equal(t, "test", req.Labels["env"])
			return createdPool, nil
		})

	provider := &IKSWorkerPoolProvider{}
	poolName, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "karp-bx2-4x16-abc123", poolName) // v1 API uses pool name
	assert.Equal(t, "bx2-4x16", flavor)
}

func TestFindOrSelectWorkerPool_DynamicPoolCreation_Disabled(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	// Dynamic pools disabled (nil config)
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone: "us-south-1",
		},
	}

	// No matching pools, but should fallback to first pool since dynamic pools disabled
	pools := []*ibm.WorkerPool{
		{
			ID:          "pool-1",
			Name:        "fallback-pool",
			Flavor:      "cx2-2x4",
			Zone:        "us-south-2",
			SizePerZone: 1,
			State:       "normal",
		},
	}

	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return(pools, nil)

	provider := &IKSWorkerPoolProvider{}
	poolName, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "fallback-pool", poolName) // v1 API uses pool name
	assert.Equal(t, "cx2-2x4", flavor)
}

func TestFindOrSelectWorkerPool_DynamicPoolCreation_AllowedTypes(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	// Only allow specific instance types
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone:   "us-south-1",
			Subnet: "subnet-123",
			VPC:    "vpc-456",
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{
				Enabled:              true,
				AllowedInstanceTypes: []string{"bx2-4x16", "bx2-8x32"},
			},
		},
	}

	// No matching pools
	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return([]*ibm.WorkerPool{}, nil)

	provider := &IKSWorkerPoolProvider{}

	// Request a type that's not allowed - should fail
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "cx2-16x32")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no worker pools found")
	assert.Empty(t, poolID)
	assert.Empty(t, flavor)
}

func TestFindOrSelectWorkerPool_DynamicPoolCreation_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockIKS := mock_ibm.NewMockIKSClientInterface(ctrl)

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Zone:   "us-south-1",
			Subnet: "subnet-123",
			VPC:    "vpc-456",
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{
				Enabled: true,
			},
		},
	}

	// No matching pools
	pools := []*ibm.WorkerPool{}

	mockIKS.EXPECT().
		ListWorkerPools(ctx, "test-cluster").
		Return(pools, nil)

	// Pool creation fails
	mockIKS.EXPECT().
		CreateWorkerPool(ctx, "test-cluster", gomock.Any()).
		Return(nil, fmt.Errorf("quota exceeded"))

	provider := &IKSWorkerPoolProvider{}
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	// With no pools and creation failure, should error
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no worker pools found")
	assert.Empty(t, poolID)
	assert.Empty(t, flavor)
}

func TestGeneratePoolName(t *testing.T) {
	tests := []struct {
		name     string
		prefix   string
		flavor   string
		validate func(t *testing.T, result string)
	}{
		{
			name:   "basic name generation",
			prefix: "karp",
			flavor: "bx2-4x16",
			validate: func(t *testing.T, result string) {
				assert.True(t, len(result) > len("karp-bx2-4x16-"))
				assert.Contains(t, result, "karp-bx2-4x16-")
			},
		},
		{
			name:   "sanitizes dots in flavor",
			prefix: "test",
			flavor: "bx2.4x16",
			validate: func(t *testing.T, result string) {
				assert.NotContains(t, result, ".")
				assert.Contains(t, result, "bx2-4x16")
			},
		},
		{
			name:   "sanitizes underscores in flavor",
			prefix: "test",
			flavor: "bx2_4x16",
			validate: func(t *testing.T, result string) {
				assert.NotContains(t, result, "_")
				assert.Contains(t, result, "bx2-4x16")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generatePoolName(tt.prefix, tt.flavor)
			tt.validate(t, result)
		})
	}
}

func TestIsInstanceTypeAllowed(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		allowedTypes []string
		expected     bool
	}{
		{
			name:         "empty list allows all",
			instanceType: "bx2-4x16",
			allowedTypes: []string{},
			expected:     true,
		},
		{
			name:         "nil list allows all",
			instanceType: "bx2-4x16",
			allowedTypes: nil,
			expected:     true,
		},
		{
			name:         "type in allowed list",
			instanceType: "bx2-4x16",
			allowedTypes: []string{"bx2-2x8", "bx2-4x16", "bx2-8x32"},
			expected:     true,
		},
		{
			name:         "type not in allowed list",
			instanceType: "cx2-4x8",
			allowedTypes: []string{"bx2-2x8", "bx2-4x16", "bx2-8x32"},
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInstanceTypeAllowed(tt.instanceType, tt.allowedTypes)
			assert.Equal(t, tt.expected, result)
		})
	}
}
