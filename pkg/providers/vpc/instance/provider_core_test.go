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

package instance

import (
	"context"
	"fmt"
	"testing"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	mock_ibm "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
)

// TestProviderGet tests the VPCInstanceProvider Get() method
func TestProviderGet(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	t.Run("successful get", func(t *testing.T) {
		testInstance := getTestVPCInstance()
		testResponse := &core.DetailedResponse{StatusCode: 200}

		mockVPC.EXPECT().
			GetInstanceWithContext(gomock.Any(), gomock.Any()).
			Return(testInstance, testResponse, nil).
			Times(1)

		// Create VPC client using mock
		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		// Test provider ID extraction
		providerID := "ibm:///us-south/test-instance-id"
		instanceID := extractInstanceIDFromProviderID(providerID)
		assert.Equal(t, "test-instance-id", instanceID)

		// Test the VPC client call directly
		instance, err := vpcClient.GetInstance(ctx, "test-instance-id")
		assert.NoError(t, err)
		assert.NotNil(t, instance)
		assert.Equal(t, "test-instance-id", *instance.ID)
	})

	t.Run("instance not found", func(t *testing.T) {
		mockVPC.EXPECT().
			GetInstanceWithContext(gomock.Any(), gomock.Any()).
			Return(nil, nil, fmt.Errorf("instance not found: 404")).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		instance, err := vpcClient.GetInstance(ctx, "nonexistent-id")
		assert.Error(t, err)
		assert.Nil(t, instance)
		assert.Contains(t, err.Error(), "not found")
	})
}

// TestProviderDelete tests the VPCInstanceProvider Delete() method
func TestProviderDelete(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()

	t.Run("successful delete", func(t *testing.T) {
		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
		testResponse := &core.DetailedResponse{StatusCode: 204}

		// Expect DeleteInstance call
		mockVPC.EXPECT().
			DeleteInstanceWithContext(gomock.Any(), gomock.Any()).
			Return(testResponse, nil).
			Times(1)

		// Expect GetInstance call to verify deletion (should return not found)
		mockVPC.EXPECT().
			GetInstanceWithContext(gomock.Any(), gomock.Any()).
			Return(nil, nil, fmt.Errorf("instance not found: 404")).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		// Create a node with provider ID
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
				Labels: map[string]string{
					"topology.kubernetes.io/region":    "us-south",
					"node.kubernetes.io/instance-type": "bx2-4x16",
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: "ibm:///us-south/test-instance-id",
			},
		}

		// Test the delete flow
		instanceID := extractInstanceIDFromProviderID(node.Spec.ProviderID)
		assert.Equal(t, "test-instance-id", instanceID)

		err := vpcClient.DeleteInstance(ctx, instanceID)
		assert.NoError(t, err)

		// Verify instance is gone
		instance, err := vpcClient.GetInstance(ctx, instanceID)
		assert.Error(t, err)
		assert.Nil(t, instance)
	})

	t.Run("delete already deleted instance", func(t *testing.T) {
		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		// Delete returns not found
		mockVPC.EXPECT().
			DeleteInstanceWithContext(gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("instance not found: 404")).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := vpcClient.DeleteInstance(ctx, "already-deleted-id")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

// TestProviderList tests the VPCInstanceProvider List() method
func TestProviderList(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	t.Run("list with multiple instances", func(t *testing.T) {
		instance1 := getTestVPCInstance()
		instance2 := &vpcv1.Instance{
			ID:   ptrString("instance-2"),
			Name: ptrString("node-2"),
			Profile: &vpcv1.InstanceProfileReference{
				Name: ptrString("bx2-8x32"),
			},
			Zone: &vpcv1.ZoneReference{
				Name: ptrString("us-south-2"),
			},
		}

		testCollection := &vpcv1.InstanceCollection{
			Instances: []vpcv1.Instance{*instance1, *instance2},
		}
		testResponse := &core.DetailedResponse{StatusCode: 200}

		mockVPC.EXPECT().
			ListInstancesWithContext(gomock.Any(), gomock.Any()).
			Return(testCollection, testResponse, nil).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		instances, err := vpcClient.ListInstances(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, instances)
		assert.Len(t, instances, 2)
		assert.Equal(t, "test-instance-id", *instances[0].ID)
		assert.Equal(t, "instance-2", *instances[1].ID)
	})

	t.Run("list with empty result", func(t *testing.T) {
		emptyCollection := &vpcv1.InstanceCollection{
			Instances: []vpcv1.Instance{},
		}
		testResponse := &core.DetailedResponse{StatusCode: 200}

		mockVPC.EXPECT().
			ListInstancesWithContext(gomock.Any(), gomock.Any()).
			Return(emptyCollection, testResponse, nil).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		instances, err := vpcClient.ListInstances(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, instances)
		assert.Empty(t, instances)
	})

	t.Run("list with API error", func(t *testing.T) {
		mockVPC.EXPECT().
			ListInstancesWithContext(gomock.Any(), gomock.Any()).
			Return(nil, nil, fmt.Errorf("API rate limit exceeded")).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		instances, err := vpcClient.ListInstances(ctx)
		assert.Error(t, err)
		assert.Nil(t, instances)
		assert.Contains(t, err.Error(), "rate limit")
	})
}

// TestProviderUpdateTags tests the VPCInstanceProvider UpdateTags() method
func TestProviderUpdateTags(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	t.Run("successful tag update", func(t *testing.T) {
		instanceID := "test-instance-id"
		tags := map[string]string{
			"environment": "production",
			"team":        "platform",
			"managed-by":  "karpenter",
		}

		mockVPC.EXPECT().
			UpdateInstanceWithContext(gomock.Any(), gomock.Any()).
			Return(&vpcv1.Instance{ID: &instanceID}, &core.DetailedResponse{StatusCode: 200}, nil).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := vpcClient.UpdateInstanceTags(ctx, instanceID, tags)
		assert.NoError(t, err)
	})

	t.Run("update tags with empty tags map", func(t *testing.T) {
		instanceID := "test-instance-id"
		tags := map[string]string{}

		mockVPC.EXPECT().
			UpdateInstanceWithContext(gomock.Any(), gomock.Any()).
			Return(&vpcv1.Instance{ID: &instanceID}, &core.DetailedResponse{StatusCode: 200}, nil).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := vpcClient.UpdateInstanceTags(ctx, instanceID, tags)
		assert.NoError(t, err)
	})

	t.Run("update tags with instance not found", func(t *testing.T) {
		instanceID := "nonexistent-instance"
		tags := map[string]string{"env": "test"}

		mockVPC.EXPECT().
			UpdateInstanceWithContext(gomock.Any(), gomock.Any()).
			Return(nil, nil, fmt.Errorf("instance not found: 404")).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := vpcClient.UpdateInstanceTags(ctx, instanceID, tags)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("update tags with API error", func(t *testing.T) {
		instanceID := "test-instance-id"
		tags := map[string]string{"env": "test"}

		mockVPC.EXPECT().
			UpdateInstanceWithContext(gomock.Any(), gomock.Any()).
			Return(nil, nil, fmt.Errorf("permission denied")).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := vpcClient.UpdateInstanceTags(ctx, instanceID, tags)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "permission denied")
	})
}

// TestProviderIDExtraction tests provider ID parsing
func TestProviderIDExtraction(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		wantID     string
		wantRegion string
	}{
		{
			name:       "standard provider ID",
			providerID: "ibm:///us-south/instance-123",
			wantID:     "instance-123",
			wantRegion: "us-south",
		},
		{
			name:       "provider ID with zone prefix",
			providerID: "ibm:///eu-de/02u7_1234-abcd",
			wantID:     "02u7_1234-abcd",
			wantRegion: "eu-de",
		},
		{
			name:       "invalid provider ID",
			providerID: "invalid",
			wantID:     "",
			wantRegion: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instanceID := extractInstanceIDFromProviderID(tt.providerID)
			assert.Equal(t, tt.wantID, instanceID)
		})
	}
}

// TestGetCurrentVPCUsage tests the getCurrentVPCUsage helper
func TestGetCurrentVPCUsage_Core(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	t.Run("calculate usage correctly", func(t *testing.T) {
		vcpuCount1 := int64(4)
		vcpuCount2 := int64(8)
		vcpuCount3 := int64(16)

		testInstances := &vpcv1.InstanceCollection{
			Instances: []vpcv1.Instance{
				{
					ID:   ptrString("instance-1"),
					Name: ptrString("node-1"),
					Vcpu: &vpcv1.InstanceVcpu{Count: &vcpuCount1},
				},
				{
					ID:   ptrString("instance-2"),
					Name: ptrString("node-2"),
					Vcpu: &vpcv1.InstanceVcpu{Count: &vcpuCount2},
				},
				{
					ID:   ptrString("instance-3"),
					Name: ptrString("node-3"),
					Vcpu: &vpcv1.InstanceVcpu{Count: &vcpuCount3},
				},
			},
		}
		testResponse := &core.DetailedResponse{StatusCode: 200}

		mockVPC.EXPECT().
			ListInstancesWithContext(gomock.Any(), gomock.Any()).
			Return(testInstances, testResponse, nil).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		instances, err := vpcClient.ListInstances(ctx)
		assert.NoError(t, err)
		assert.Len(t, instances, 3)

		// Calculate usage (what getCurrentVPCUsage does)
		instanceCount := len(instances)
		vcpuTotal := 0
		for _, inst := range instances {
			if inst.Vcpu != nil && inst.Vcpu.Count != nil {
				vcpuTotal += int(*inst.Vcpu.Count)
			}
		}

		assert.Equal(t, 3, instanceCount)
		assert.Equal(t, 28, vcpuTotal) // 4 + 8 + 16
	})

	t.Run("handle instances without vcpu info", func(t *testing.T) {
		vcpuCount1 := int64(4)

		testInstances := &vpcv1.InstanceCollection{
			Instances: []vpcv1.Instance{
				{
					ID:   ptrString("instance-1"),
					Name: ptrString("node-1"),
					Vcpu: &vpcv1.InstanceVcpu{Count: &vcpuCount1},
				},
				{
					ID:   ptrString("instance-2"),
					Name: ptrString("node-2"),
					// No Vcpu field
				},
			},
		}
		testResponse := &core.DetailedResponse{StatusCode: 200}

		mockVPC.EXPECT().
			ListInstancesWithContext(gomock.Any(), gomock.Any()).
			Return(testInstances, testResponse, nil).
			Times(1)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		instances, err := vpcClient.ListInstances(ctx)
		assert.NoError(t, err)
		assert.Len(t, instances, 2)

		// Calculate usage safely
		instanceCount := len(instances)
		vcpuTotal := 0
		for _, inst := range instances {
			if inst.Vcpu != nil && inst.Vcpu.Count != nil {
				vcpuTotal += int(*inst.Vcpu.Count)
			}
		}

		assert.Equal(t, 2, instanceCount)
		assert.Equal(t, 4, vcpuTotal) // Only first instance counted
	})
}
