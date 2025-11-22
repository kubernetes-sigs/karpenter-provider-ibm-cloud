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
	"github.com/go-logr/logr"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	mock_ibm "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
)

func TestIsPartialFailure(t *testing.T) {
	provider := &VPCInstanceProvider{}

	tests := []struct {
		name     string
		ibmErr   *ibm.IBMError
		expected bool
	}{
		{
			name:     "nil error",
			ibmErr:   nil,
			expected: false,
		},
		{
			name: "quota exceeded error",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_instance_quota_exceeded",
				Message:    "Instance quota exceeded",
			},
			expected: true,
		},
		{
			name: "resource not found error",
			ibmErr: &ibm.IBMError{
				StatusCode: 404,
				Code:       "not_found",
				Message:    "Instance not found",
			},
			expected: false,
		},
		{
			name: "internal server error",
			ibmErr: &ibm.IBMError{
				StatusCode: 500,
				Code:       "internal_error",
				Message:    "Internal server error",
			},
			expected: true, // 5xx errors are considered partial failures (retryable)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.isPartialFailure(tt.ibmErr)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCleanupOrphanedResources(t *testing.T) {
	provider := &VPCInstanceProvider{}
	ctx := context.Background()

	instanceName := "test-instance"
	vpcID := "test-vpc-id"
	logger := logr.Discard()

	t.Run("successful cleanup with no orphaned resources", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		// Mock VNI listing - no orphaned VNIs found
		emptyVNICollection := &vpcv1.VirtualNetworkInterfaceCollection{
			VirtualNetworkInterfaces: []vpcv1.VirtualNetworkInterface{},
		}
		mockVPC.EXPECT().
			ListVirtualNetworkInterfacesWithContext(gomock.Any(), gomock.Any()).
			Return(emptyVNICollection, &core.DetailedResponse{}, nil)

		// Mock volume listing - no orphaned volumes found
		emptyVolumeCollection := &vpcv1.VolumeCollection{
			Volumes: []vpcv1.Volume{},
		}
		mockVPC.EXPECT().
			ListVolumesWithContext(gomock.Any(), gomock.Any()).
			Return(emptyVolumeCollection, &core.DetailedResponse{}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := provider.cleanupOrphanedResources(ctx, vpcClient, instanceName, vpcID, logger)

		assert.NoError(t, err)
	})

	t.Run("cleanup with orphaned resources found and deleted", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		// Mock VNI listing - orphaned VNI found
		vniID := "vni-12345"
		vniName := "test-instance-vni"
		orphanedVNI := vpcv1.VirtualNetworkInterface{
			ID:   &vniID,
			Name: &vniName,
		}
		vniCollection := &vpcv1.VirtualNetworkInterfaceCollection{
			VirtualNetworkInterfaces: []vpcv1.VirtualNetworkInterface{orphanedVNI},
		}
		mockVPC.EXPECT().
			ListVirtualNetworkInterfacesWithContext(gomock.Any(), gomock.Any()).
			Return(vniCollection, &core.DetailedResponse{}, nil)

		// Mock VNI deletion
		mockVPC.EXPECT().
			DeleteVirtualNetworkInterfacesWithContext(gomock.Any(), gomock.Any()).
			Return(&vpcv1.VirtualNetworkInterface{}, &core.DetailedResponse{}, nil)

		// Mock volume listing - orphaned volume found
		volumeID := "vol-67890"
		volumeName := "test-instance-boot"
		orphanedVolume := vpcv1.Volume{
			ID:   &volumeID,
			Name: &volumeName,
		}
		volumeCollection := &vpcv1.VolumeCollection{
			Volumes: []vpcv1.Volume{orphanedVolume},
		}
		mockVPC.EXPECT().
			ListVolumesWithContext(gomock.Any(), gomock.Any()).
			Return(volumeCollection, &core.DetailedResponse{}, nil)

		// Mock volume deletion
		mockVPC.EXPECT().
			DeleteVolumeWithContext(gomock.Any(), gomock.Any()).
			Return(&core.DetailedResponse{}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := provider.cleanupOrphanedResources(ctx, vpcClient, instanceName, vpcID, logger)

		assert.NoError(t, err)
	})
}

func TestCleanupOrphanedVNI(t *testing.T) {
	provider := &VPCInstanceProvider{}
	ctx := context.Background()

	vniName := "test-instance-vni"
	vpcID := "test-vpc-id"
	logger := logr.Discard()

	t.Run("no orphaned VNI found", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		// Mock VNI listing - no matching VNI found
		emptyVNICollection := &vpcv1.VirtualNetworkInterfaceCollection{
			VirtualNetworkInterfaces: []vpcv1.VirtualNetworkInterface{},
		}
		mockVPC.EXPECT().
			ListVirtualNetworkInterfacesWithContext(gomock.Any(), gomock.Any()).
			Return(emptyVNICollection, &core.DetailedResponse{}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := provider.cleanupOrphanedVNI(ctx, vpcClient, vniName, vpcID, logger)

		assert.NoError(t, err)
	})

	t.Run("orphaned VNI found and deleted", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		// Mock VNI listing - matching VNI found
		vniID := "vni-12345"
		orphanedVNI := vpcv1.VirtualNetworkInterface{
			ID:   &vniID,
			Name: &vniName,
		}
		vniCollection := &vpcv1.VirtualNetworkInterfaceCollection{
			VirtualNetworkInterfaces: []vpcv1.VirtualNetworkInterface{orphanedVNI},
		}
		mockVPC.EXPECT().
			ListVirtualNetworkInterfacesWithContext(gomock.Any(), gomock.Any()).
			Return(vniCollection, &core.DetailedResponse{}, nil)

		// Mock VNI deletion
		mockVPC.EXPECT().
			DeleteVirtualNetworkInterfacesWithContext(gomock.Any(), gomock.Any()).
			Return(&vpcv1.VirtualNetworkInterface{}, &core.DetailedResponse{}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := provider.cleanupOrphanedVNI(ctx, vpcClient, vniName, vpcID, logger)

		assert.NoError(t, err)
	})

	t.Run("VNI deletion fails - already deleted", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		// Mock VNI listing - matching VNI found
		vniID := "vni-12345"
		orphanedVNI := vpcv1.VirtualNetworkInterface{
			ID:   &vniID,
			Name: &vniName,
		}
		vniCollection := &vpcv1.VirtualNetworkInterfaceCollection{
			VirtualNetworkInterfaces: []vpcv1.VirtualNetworkInterface{orphanedVNI},
		}
		mockVPC.EXPECT().
			ListVirtualNetworkInterfacesWithContext(gomock.Any(), gomock.Any()).
			Return(vniCollection, &core.DetailedResponse{}, nil)

		// Mock VNI deletion - not found error (already deleted)
		mockVPC.EXPECT().
			DeleteVirtualNetworkInterfacesWithContext(gomock.Any(), gomock.Any()).
			Return(nil, nil, fmt.Errorf("VNI not found: 404"))

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := provider.cleanupOrphanedVNI(ctx, vpcClient, vniName, vpcID, logger)

		// Should not error when VNI is already deleted
		assert.NoError(t, err)
	})
}

func TestCleanupOrphanedVolume(t *testing.T) {
	provider := &VPCInstanceProvider{}
	ctx := context.Background()

	volumeName := "test-instance-boot"
	logger := logr.Discard()

	t.Run("no orphaned volume found", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		// Mock volume listing - no matching volume found
		emptyVolumeCollection := &vpcv1.VolumeCollection{
			Volumes: []vpcv1.Volume{},
		}
		mockVPC.EXPECT().
			ListVolumesWithContext(gomock.Any(), gomock.Any()).
			Return(emptyVolumeCollection, &core.DetailedResponse{}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := provider.cleanupOrphanedVolume(ctx, vpcClient, volumeName, logger)

		assert.NoError(t, err)
	})

	t.Run("orphaned volume found and deleted", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		// Mock volume listing - matching volume found
		volumeID := "vol-67890"
		orphanedVolume := vpcv1.Volume{
			ID:   &volumeID,
			Name: &volumeName,
		}
		volumeCollection := &vpcv1.VolumeCollection{
			Volumes: []vpcv1.Volume{orphanedVolume},
		}
		mockVPC.EXPECT().
			ListVolumesWithContext(gomock.Any(), gomock.Any()).
			Return(volumeCollection, &core.DetailedResponse{}, nil)

		// Mock volume deletion
		mockVPC.EXPECT().
			DeleteVolumeWithContext(gomock.Any(), gomock.Any()).
			Return(&core.DetailedResponse{}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := provider.cleanupOrphanedVolume(ctx, vpcClient, volumeName, logger)

		assert.NoError(t, err)
	})

	t.Run("volume deletion fails - already deleted", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		// Mock volume listing - matching volume found
		volumeID := "vol-67890"
		orphanedVolume := vpcv1.Volume{
			ID:   &volumeID,
			Name: &volumeName,
		}
		volumeCollection := &vpcv1.VolumeCollection{
			Volumes: []vpcv1.Volume{orphanedVolume},
		}
		mockVPC.EXPECT().
			ListVolumesWithContext(gomock.Any(), gomock.Any()).
			Return(volumeCollection, &core.DetailedResponse{}, nil)

		// Mock volume deletion - not found error (already deleted)
		mockVPC.EXPECT().
			DeleteVolumeWithContext(gomock.Any(), gomock.Any()).
			Return(nil, fmt.Errorf("volume not found: 404"))

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)

		err := provider.cleanupOrphanedVolume(ctx, vpcClient, volumeName, logger)

		// Should not error when volume is already deleted
		assert.NoError(t, err)
	})
}

func TestResourceCleanupPatterns(t *testing.T) {
	tests := []struct {
		name          string
		errorCode     string
		statusCode    int
		shouldCleanup bool
		description   string
	}{
		{
			name:          "quota exceeded after VNI creation",
			errorCode:     "vpc_instance_quota_exceeded",
			statusCode:    400,
			shouldCleanup: true,
			description:   "Instance quota exceeded after network interface creation",
		},
		{
			name:          "profile unavailable after network setup",
			errorCode:     "vpc_instance_profile_not_available",
			statusCode:    400,
			shouldCleanup: true,
			description:   "Instance profile became unavailable after network setup",
		},
		{
			name:          "volume creation failure",
			errorCode:     "vpc_boot_volume_creation_failed",
			statusCode:    500,
			shouldCleanup: true,
			description:   "Boot volume creation failed, network resources may exist",
		},
		{
			name:          "authentication failure",
			errorCode:     "unauthorized",
			statusCode:    401,
			shouldCleanup: false,
			description:   "Authentication failure - no partial resources expected",
		},
		{
			name:          "simple validation error",
			errorCode:     "invalid_parameter",
			statusCode:    400,
			shouldCleanup: false,
			description:   "Parameter validation error - no resources created",
		},
	}

	provider := &VPCInstanceProvider{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ibmErr := &ibm.IBMError{
				StatusCode: tt.statusCode,
				Code:       tt.errorCode,
				Message:    tt.description,
			}

			shouldCleanup := provider.isPartialFailure(ibmErr)
			assert.Equal(t, tt.shouldCleanup, shouldCleanup,
				"Error %s (status %d) cleanup decision should be %v",
				tt.errorCode, tt.statusCode, tt.shouldCleanup)
		})
	}
}
