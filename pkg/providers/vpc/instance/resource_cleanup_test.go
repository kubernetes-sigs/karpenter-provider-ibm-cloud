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
	"testing"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
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
			name: "instance profile not available",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_instance_profile_not_available",
				Message:    "Instance profile not available",
			},
			expected: true,
		},
		{
			name: "security group not found",
			ibmErr: &ibm.IBMError{
				StatusCode: 404,
				Code:       "vpc_security_group_not_found",
				Message:    "Security group not found",
			},
			expected: true,
		},
		{
			name: "subnet not available",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_subnet_not_available",
				Message:    "Subnet not available",
			},
			expected: true,
		},
		{
			name: "volume capacity insufficient",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_volume_capacity_insufficient",
				Message:    "Volume capacity insufficient",
			},
			expected: true,
		},
		{
			name: "boot volume creation failed",
			ibmErr: &ibm.IBMError{
				StatusCode: 500,
				Code:       "vpc_boot_volume_creation_failed",
				Message:    "Boot volume creation failed",
			},
			expected: true,
		},
		{
			name: "500 server error - unknown code",
			ibmErr: &ibm.IBMError{
				StatusCode: 500,
				Code:       "internal_server_error",
				Message:    "Internal server error",
			},
			expected: true,
		},
		{
			name: "502 bad gateway - unknown code",
			ibmErr: &ibm.IBMError{
				StatusCode: 502,
				Code:       "bad_gateway",
				Message:    "Bad gateway",
			},
			expected: true,
		},
		{
			name: "503 service unavailable - unknown code",
			ibmErr: &ibm.IBMError{
				StatusCode: 503,
				Code:       "service_unavailable",
				Message:    "Service unavailable",
			},
			expected: true,
		},
		{
			name: "400 client error - unknown code",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "bad_request",
				Message:    "Bad request",
			},
			expected: false,
		},
		{
			name: "404 not found - unknown code",
			ibmErr: &ibm.IBMError{
				StatusCode: 404,
				Code:       "not_found",
				Message:    "Not found",
			},
			expected: false,
		},
		{
			name: "401 unauthorized",
			ibmErr: &ibm.IBMError{
				StatusCode: 401,
				Code:       "unauthorized",
				Message:    "Unauthorized",
			},
			expected: false,
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
	logger := logr.Discard() // Use discard logger for tests

	t.Run("successful cleanup with no orphaned resources", func(t *testing.T) {
		// Create mock VPC client that returns empty results
		mockVPCSDKClient := &MockVPCSDKClient{}
		mockVPCClient := ibm.NewVPCClientWithMock(mockVPCSDKClient)

		// Mock VNI listing - no orphaned VNIs found
		emptyVNICollection := &vpcv1.VirtualNetworkInterfaceCollection{
			VirtualNetworkInterfaces: []vpcv1.VirtualNetworkInterface{},
		}
		mockVPCSDKClient.On("ListVirtualNetworkInterfacesWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.ListVirtualNetworkInterfacesOptions")).
			Return(emptyVNICollection, &core.DetailedResponse{}, nil)

		// Mock volume listing - no orphaned volumes found
		emptyVolumeCollection := &vpcv1.VolumeCollection{
			Volumes: []vpcv1.Volume{},
		}
		mockVPCSDKClient.On("ListVolumesWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.ListVolumesOptions")).
			Return(emptyVolumeCollection, &core.DetailedResponse{}, nil)

		err := provider.cleanupOrphanedResources(ctx, mockVPCClient, instanceName, vpcID, logger)

		// Should not error when no orphaned resources are found
		assert.NoError(t, err)
		mockVPCSDKClient.AssertExpectations(t)
	})

	t.Run("cleanup with orphaned resources found and deleted", func(t *testing.T) {
		// Create mock VPC client
		mockVPCSDKClient := &MockVPCSDKClient{}
		mockVPCClient := ibm.NewVPCClientWithMock(mockVPCSDKClient)

		// Mock VNI listing - orphaned VNI found
		orphanedVNI := vpcv1.VirtualNetworkInterface{
			ID:   &[]string{"vni-12345"}[0],
			Name: &[]string{"test-instance-vni"}[0],
		}
		vniCollection := &vpcv1.VirtualNetworkInterfaceCollection{
			VirtualNetworkInterfaces: []vpcv1.VirtualNetworkInterface{orphanedVNI},
		}
		mockVPCSDKClient.On("ListVirtualNetworkInterfacesWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.ListVirtualNetworkInterfacesOptions")).
			Return(vniCollection, &core.DetailedResponse{}, nil)

		// Mock VNI deletion
		mockVPCSDKClient.On("DeleteVirtualNetworkInterfacesWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.DeleteVirtualNetworkInterfacesOptions")).
			Return(&vpcv1.VirtualNetworkInterface{}, &core.DetailedResponse{}, nil)

		// Mock volume listing - orphaned volume found
		orphanedVolume := vpcv1.Volume{
			ID:   &[]string{"vol-67890"}[0],
			Name: &[]string{"test-instance-boot"}[0],
		}
		volumeCollection := &vpcv1.VolumeCollection{
			Volumes: []vpcv1.Volume{orphanedVolume},
		}
		mockVPCSDKClient.On("ListVolumesWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.ListVolumesOptions")).
			Return(volumeCollection, &core.DetailedResponse{}, nil)

		// Mock volume deletion
		mockVPCSDKClient.On("DeleteVolumeWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.DeleteVolumeOptions")).
			Return(&core.DetailedResponse{}, nil)

		err := provider.cleanupOrphanedResources(ctx, mockVPCClient, instanceName, vpcID, logger)

		// Should not error when cleanup succeeds
		assert.NoError(t, err)
		mockVPCSDKClient.AssertExpectations(t)
	})
}

func TestCleanupOrphanedVNI(t *testing.T) {
	provider := &VPCInstanceProvider{}
	ctx := context.Background()

	vniName := "test-instance-vni"
	vpcID := "test-vpc-id"
	logger := logr.Discard()

	t.Run("no orphaned VNI found", func(t *testing.T) {
		mockVPCSDKClient := &MockVPCSDKClient{}
		mockVPCClient := ibm.NewVPCClientWithMock(mockVPCSDKClient)

		// Mock VNI listing - no matching VNI found
		emptyVNICollection := &vpcv1.VirtualNetworkInterfaceCollection{
			VirtualNetworkInterfaces: []vpcv1.VirtualNetworkInterface{},
		}
		mockVPCSDKClient.On("ListVirtualNetworkInterfacesWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.ListVirtualNetworkInterfacesOptions")).
			Return(emptyVNICollection, &core.DetailedResponse{}, nil)

		err := provider.cleanupOrphanedVNI(ctx, mockVPCClient, vniName, vpcID, logger)

		// Should not error when no matching VNI is found
		assert.NoError(t, err)
		mockVPCSDKClient.AssertExpectations(t)
	})

	t.Run("orphaned VNI found and deleted", func(t *testing.T) {
		mockVPCSDKClient := &MockVPCSDKClient{}
		mockVPCClient := ibm.NewVPCClientWithMock(mockVPCSDKClient)

		// Mock VNI listing - matching VNI found
		orphanedVNI := vpcv1.VirtualNetworkInterface{
			ID:   &[]string{"vni-12345"}[0],
			Name: &[]string{vniName}[0],
		}
		vniCollection := &vpcv1.VirtualNetworkInterfaceCollection{
			VirtualNetworkInterfaces: []vpcv1.VirtualNetworkInterface{orphanedVNI},
		}
		mockVPCSDKClient.On("ListVirtualNetworkInterfacesWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.ListVirtualNetworkInterfacesOptions")).
			Return(vniCollection, &core.DetailedResponse{}, nil)

		// Mock VNI deletion
		mockVPCSDKClient.On("DeleteVirtualNetworkInterfacesWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.DeleteVirtualNetworkInterfacesOptions")).
			Return(&vpcv1.VirtualNetworkInterface{}, &core.DetailedResponse{}, nil)

		err := provider.cleanupOrphanedVNI(ctx, mockVPCClient, vniName, vpcID, logger)

		// Should not error when VNI is successfully deleted
		assert.NoError(t, err)
		mockVPCSDKClient.AssertExpectations(t)
	})
}

func TestCleanupOrphanedVolume(t *testing.T) {
	provider := &VPCInstanceProvider{}
	ctx := context.Background()

	volumeName := "test-instance-boot"
	logger := logr.Discard()

	t.Run("no orphaned volume found", func(t *testing.T) {
		mockVPCSDKClient := &MockVPCSDKClient{}
		mockVPCClient := ibm.NewVPCClientWithMock(mockVPCSDKClient)

		// Mock volume listing - no matching volume found
		emptyVolumeCollection := &vpcv1.VolumeCollection{
			Volumes: []vpcv1.Volume{},
		}
		mockVPCSDKClient.On("ListVolumesWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.ListVolumesOptions")).
			Return(emptyVolumeCollection, &core.DetailedResponse{}, nil)

		err := provider.cleanupOrphanedVolume(ctx, mockVPCClient, volumeName, logger)

		// Should not error when no matching volume is found
		assert.NoError(t, err)
		mockVPCSDKClient.AssertExpectations(t)
	})

	t.Run("orphaned volume found and deleted", func(t *testing.T) {
		mockVPCSDKClient := &MockVPCSDKClient{}
		mockVPCClient := ibm.NewVPCClientWithMock(mockVPCSDKClient)

		// Mock volume listing - matching volume found
		orphanedVolume := vpcv1.Volume{
			ID:   &[]string{"vol-67890"}[0],
			Name: &[]string{volumeName}[0],
		}
		volumeCollection := &vpcv1.VolumeCollection{
			Volumes: []vpcv1.Volume{orphanedVolume},
		}
		mockVPCSDKClient.On("ListVolumesWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.ListVolumesOptions")).
			Return(volumeCollection, &core.DetailedResponse{}, nil)

		// Mock volume deletion
		mockVPCSDKClient.On("DeleteVolumeWithContext", mock.Anything, mock.AnythingOfType("*vpcv1.DeleteVolumeOptions")).
			Return(&core.DetailedResponse{}, nil)

		err := provider.cleanupOrphanedVolume(ctx, mockVPCClient, volumeName, logger)

		// Should not error when volume is successfully deleted
		assert.NoError(t, err)
		mockVPCSDKClient.AssertExpectations(t)
	})
}

func TestResourceCleanupIntegration(t *testing.T) {
	t.Skip("Integration test - requires actual VPC client with resource management methods")

	// This test would verify the complete resource cleanup flow:
	// 1. Create a VPC instance with intentional failure after partial resource creation
	// 2. Verify that cleanup is triggered
	// 3. Verify that orphaned resources are properly removed
	// 4. Verify that the error propagation works correctly

	// Implementation would require:
	// - Mock VPC client with resource creation/deletion methods
	// - Simulated partial failures at different stages
	// - Verification of cleanup calls with correct parameters
}

// TestResourceCleanupPatterns tests the overall error handling and cleanup patterns
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
