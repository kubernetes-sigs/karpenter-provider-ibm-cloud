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

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

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
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "configured-pool-id", poolID)
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
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "pool-1", poolID)
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
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "pool-2", poolID)
	assert.Equal(t, "bx2-8x32", flavor) // Uses pool's flavor, not requested
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
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "pool-2", poolID)
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
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "bx2-4x16")

	assert.NoError(t, err)
	assert.Equal(t, "pool-1", poolID)  // First pool
	assert.Equal(t, "cx2-2x4", flavor) // First pool's flavor
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
	poolID, flavor, err := provider.findOrSelectWorkerPool(ctx, mockIKS, "test-cluster", nodeClass, "")

	assert.NoError(t, err)
	assert.Equal(t, "pool-1", poolID)
	assert.Equal(t, "bx2-4x16", flavor)
}
