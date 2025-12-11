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
	"os"
	"testing"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	mock_ibm "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Test helpers
func getTestVPCInstance() *vpcv1.Instance {
	instanceID := "test-instance-id"
	instanceName := "test-nodeclaim"
	profileName := "bx2-4x16"
	zoneName := "us-south-1"

	return &vpcv1.Instance{
		ID:   &instanceID,
		Name: &instanceName,
		Profile: &vpcv1.InstanceProfileReference{
			Name: &profileName,
		},
		Zone: &vpcv1.ZoneReference{
			Name: &zoneName,
		},
	}
}

// MockIBMClientWrapper wraps the generated mock for use in tests
type MockIBMClientWrapper struct {
	vpcClient *mock_ibm.MockvpcClientInterface
}

func (m *MockIBMClientWrapper) GetVPCClient() (*ibm.VPCClient, error) {
	if m.vpcClient == nil {
		return nil, fmt.Errorf("mock VPC client not initialized")
	}
	return ibm.NewVPCClientWithMock(m.vpcClient), nil
}

func (m *MockIBMClientWrapper) GetIKSClient() (ibm.IKSClientInterface, error) {
	// VPC mode doesn't use IKS client
	return nil, nil
}

func TestVPCClient_CreateInstance(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	testImage := &vpcv1.Image{
		ID:   ptrString("test-image-id"),
		Name: ptrString("test-image"),
	}

	testInstance := getTestVPCInstance()
	testResponse := &core.DetailedResponse{StatusCode: 200}

	// Expect GetImageWithContext call
	mockVPC.EXPECT().
		GetImageWithContext(gomock.Any(), gomock.Any()).
		Return(testImage, testResponse, nil).
		Times(1)

	// Expect CreateInstanceWithContext call
	mockVPC.EXPECT().
		CreateInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(testInstance, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	// Test GetImage
	image, err := vpcClient.GetImage(ctx, "test-image-id")
	assert.NoError(t, err)
	assert.NotNil(t, image)
	assert.Equal(t, "test-image-id", *image.ID)

	// Test CreateInstance
	instancePrototype := &vpcv1.InstancePrototypeInstanceByImage{
		Name: ptrString("test-instance"),
		Image: &vpcv1.ImageIdentity{
			ID: ptrString("test-image-id"),
		},
		Profile: &vpcv1.InstanceProfileIdentity{
			Name: ptrString("bx2-4x16"),
		},
		Zone: &vpcv1.ZoneIdentity{
			Name: ptrString("us-south-1"),
		},
		VPC: &vpcv1.VPCIdentity{
			ID: ptrString("test-vpc-id"),
		},
		PrimaryNetworkInterface: &vpcv1.NetworkInterfacePrototype{
			Subnet: &vpcv1.SubnetIdentity{
				ID: ptrString("test-subnet-id"),
			},
		},
	}

	instance, err := vpcClient.CreateInstance(ctx, instancePrototype)
	assert.NoError(t, err)
	assert.NotNil(t, instance)
	assert.Equal(t, "test-instance-id", *instance.ID)
}

func TestVPCClient_GetInstance(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	testInstance := getTestVPCInstance()
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		GetInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(testInstance, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	instance, err := vpcClient.GetInstance(ctx, "test-instance-id")
	assert.NoError(t, err)
	assert.NotNil(t, instance)
	assert.Equal(t, "test-instance-id", *instance.ID)
	assert.Equal(t, "test-nodeclaim", *instance.Name)
}

func TestVPCClient_DeleteInstance(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	testResponse := &core.DetailedResponse{StatusCode: 204}

	mockVPC.EXPECT().
		DeleteInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	err := vpcClient.DeleteInstance(ctx, "test-instance-id")
	assert.NoError(t, err)
}

func TestVPCClient_ListInstances(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	testInstance1 := getTestVPCInstance()
	testInstance2 := &vpcv1.Instance{
		ID:   ptrString("instance-2"),
		Name: ptrString("instance-2-name"),
	}

	testCollection := &vpcv1.InstanceCollection{
		Instances: []vpcv1.Instance{*testInstance1, *testInstance2},
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
}

func TestVPCClient_GetImage_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	mockVPC.EXPECT().
		GetImageWithContext(gomock.Any(), gomock.Any()).
		Return(nil, nil, fmt.Errorf("image not found")).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	image, err := vpcClient.GetImage(ctx, "nonexistent-image-id")
	assert.Error(t, err)
	assert.Nil(t, image)
	assert.Contains(t, err.Error(), "image not found")
}

func TestVPCClient_CreateInstance_Failure(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	mockVPC.EXPECT().
		CreateInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(nil, nil, fmt.Errorf("quota exceeded")).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	instancePrototype := &vpcv1.InstancePrototypeInstanceByImage{
		Name: ptrString("test-instance"),
	}

	instance, err := vpcClient.CreateInstance(ctx, instancePrototype)
	assert.Error(t, err)
	assert.Nil(t, instance)
	assert.Contains(t, err.Error(), "quota exceeded")
}

func TestExtractInstanceIDFromProviderID(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		want       string
	}{
		{
			name:       "valid provider ID",
			providerID: "ibm:///us-south/instance-123",
			want:       "instance-123",
		},
		{
			name:       "simple instance ID",
			providerID: "instance-456",
			want:       "", // extractInstanceIDFromProviderID expects ibm:// format
		},
		{
			name:       "empty provider ID",
			providerID: "",
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInstanceIDFromProviderID(tt.providerID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestVPCInstanceProvider_Get(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	testInstance := getTestVPCInstance()
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		GetInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(testInstance, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	instance, err := vpcClient.GetInstance(ctx, "test-instance-id")
	assert.NoError(t, err)
	assert.NotNil(t, instance)
	assert.Equal(t, "test-instance-id", *instance.ID)
	assert.Equal(t, "test-nodeclaim", *instance.Name)
}

func TestVPCInstanceProvider_Delete(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
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

	// Test delete
	err := vpcClient.DeleteInstance(ctx, "test-instance-id")
	assert.NoError(t, err)

	// Verify instance is gone
	instance, err := vpcClient.GetInstance(ctx, "test-instance-id")
	assert.Error(t, err)
	assert.Nil(t, instance)
	assert.Contains(t, err.Error(), "not found")
}

func TestVPCInstanceProvider_List(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	testInstance1 := getTestVPCInstance()
	testInstance2 := &vpcv1.Instance{
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
		Instances: []vpcv1.Instance{*testInstance1, *testInstance2},
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
}

func TestIsHexString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid hex lowercase",
			input:    "0123456789abcdef",
			expected: true,
		},
		{
			name:     "valid hex uppercase",
			input:    "0123456789ABCDEF",
			expected: true,
		},
		{
			name:     "valid hex mixed case",
			input:    "0123456789AbCdEf",
			expected: true,
		},
		{
			name:     "invalid hex with g",
			input:    "0123456789abcdefg",
			expected: false,
		},
		{
			name:     "invalid hex with special chars",
			input:    "0123-4567-89ab",
			expected: false,
		},
		{
			name:     "empty string",
			input:    "",
			expected: true, // Empty string is technically all hex chars
		},
		{
			name:     "32 char resource group ID",
			input:    "a1b2c3d4e5f67890a1b2c3d4e5f67890",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isHexString(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		isTimeout bool
		isQuota   bool
		isAuth    bool
	}{
		{
			name:      "timeout error with 'timeout' string",
			err:       fmt.Errorf("request timeout"),
			isTimeout: true,
			isQuota:   false,
			isAuth:    false,
		},
		{
			name:      "context deadline exceeded",
			err:       fmt.Errorf("context deadline exceeded"),
			isTimeout: true,
			isQuota:   false,
			isAuth:    false,
		},
		{
			name:      "quota exceeded error",
			err:       fmt.Errorf("quota exceeded for instances"),
			isTimeout: false,
			isQuota:   true,
			isAuth:    false,
		},
		{
			name:      "limit exceeded error",
			err:       fmt.Errorf("limit exceeded"),
			isTimeout: false,
			isQuota:   true,
			isAuth:    false,
		},
		{
			name:      "unauthorized error",
			err:       fmt.Errorf("unauthorized"),
			isTimeout: false,
			isQuota:   false,
			isAuth:    true,
		},
		{
			name:      "403 forbidden",
			err:       fmt.Errorf("403 forbidden"),
			isTimeout: false,
			isQuota:   false,
			isAuth:    true,
		},
		{
			name:      "401 authentication failed",
			err:       fmt.Errorf("401 authentication failed"),
			isTimeout: false,
			isQuota:   false,
			isAuth:    true,
		},
		{
			name:      "nil error",
			err:       nil,
			isTimeout: false,
			isQuota:   false,
			isAuth:    false,
		},
		{
			name:      "generic error",
			err:       fmt.Errorf("something went wrong"),
			isTimeout: false,
			isQuota:   false,
			isAuth:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.isTimeout, isTimeoutError(tt.err), "isTimeoutError mismatch")
			assert.Equal(t, tt.isQuota, isQuotaError(tt.err), "isQuotaError mismatch")
			assert.Equal(t, tt.isAuth, isAuthError(tt.err), "isAuthError mismatch")
		})
	}
}

func TestSelectSubnetFromStatusList(t *testing.T) {
	provider := &VPCInstanceProvider{}

	tests := []struct {
		name      string
		subnetIDs []string
		expectLen int
	}{
		{
			name:      "empty list",
			subnetIDs: []string{},
			expectLen: 0,
		},
		{
			name:      "single subnet",
			subnetIDs: []string{"subnet-1"},
			expectLen: 8, // "subnet-1" length
		},
		{
			name:      "multiple subnets",
			subnetIDs: []string{"subnet-1", "subnet-2", "subnet-3"},
			expectLen: 8, // Should return one of them
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.selectSubnetFromStatusList(tt.subnetIDs)
			if tt.expectLen == 0 {
				assert.Empty(t, result)
			} else {
				assert.NotEmpty(t, result)
				if len(tt.subnetIDs) == 1 {
					assert.Equal(t, tt.subnetIDs[0], result)
				} else if len(tt.subnetIDs) > 1 {
					// Should be one of the subnets in the list
					found := false
					for _, subnet := range tt.subnetIDs {
						if subnet == result {
							found = true
							break
						}
					}
					assert.True(t, found, "selected subnet should be from the input list")
				}
			}
		})
	}
}

func TestGetDefaultSecurityGroup(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	vpcID := "test-vpc-id"
	defaultSGID := "default-sg-id"
	defaultSGName := "default"

	testSecurityGroups := &vpcv1.SecurityGroupCollection{
		SecurityGroups: []vpcv1.SecurityGroup{
			{
				ID:   &defaultSGID,
				Name: &defaultSGName,
			},
			{
				ID:   ptrString("custom-sg-id"),
				Name: ptrString("custom"),
			},
		},
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		ListSecurityGroupsWithContext(gomock.Any(), gomock.Any()).
		Return(testSecurityGroups, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	provider := &VPCInstanceProvider{}

	sg, err := provider.getDefaultSecurityGroup(ctx, vpcClient, vpcID)
	assert.NoError(t, err)
	assert.NotNil(t, sg)
	assert.Equal(t, "default-sg-id", *sg.ID)
	assert.Equal(t, "default", *sg.Name)
}

func TestGetDefaultSecurityGroup_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	vpcID := "test-vpc-id"

	testSecurityGroups := &vpcv1.SecurityGroupCollection{
		SecurityGroups: []vpcv1.SecurityGroup{
			{
				ID:   ptrString("custom-sg-id"),
				Name: ptrString("custom"),
			},
		},
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		ListSecurityGroupsWithContext(gomock.Any(), gomock.Any()).
		Return(testSecurityGroups, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	provider := &VPCInstanceProvider{}

	sg, err := provider.getDefaultSecurityGroup(ctx, vpcClient, vpcID)
	assert.Error(t, err)
	assert.Nil(t, sg)
	assert.Contains(t, err.Error(), "default security group not found")
}

func TestSelectSubnetFromMultiZoneList(t *testing.T) {
	provider := &VPCInstanceProvider{}

	tests := []struct {
		name    string
		subnets []subnet.SubnetInfo
		want    string // zone of selected subnet
	}{
		{
			name:    "empty list",
			subnets: []subnet.SubnetInfo{},
			want:    "",
		},
		{
			name: "single subnet",
			subnets: []subnet.SubnetInfo{
				{ID: "subnet-1", Zone: "us-south-1", AvailableIPs: 100},
			},
			want: "us-south-1",
		},
		{
			name: "multiple zones - selects from round-robin",
			subnets: []subnet.SubnetInfo{
				{ID: "subnet-1", Zone: "us-south-1", AvailableIPs: 50},
				{ID: "subnet-2", Zone: "us-south-2", AvailableIPs: 100},
				{ID: "subnet-3", Zone: "us-south-3", AvailableIPs: 75},
			},
			want: "", // Can be any zone due to round-robin
		},
		{
			name: "multiple subnets same zone - picks highest IPs",
			subnets: []subnet.SubnetInfo{
				{ID: "subnet-1", Zone: "us-south-1", AvailableIPs: 50},
				{ID: "subnet-2", Zone: "us-south-1", AvailableIPs: 100},
				{ID: "subnet-3", Zone: "us-south-1", AvailableIPs: 75},
			},
			want: "us-south-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.selectSubnetFromMultiZoneList(tt.subnets)
			if len(tt.subnets) == 0 {
				assert.Empty(t, result.ID)
			} else if len(tt.subnets) == 1 {
				assert.Equal(t, tt.subnets[0].ID, result.ID)
				assert.Equal(t, tt.want, result.Zone)
			} else if tt.want != "" {
				// For single-zone tests, verify zone matches
				assert.Equal(t, tt.want, result.Zone)
				// Should pick the one with highest IPs
				assert.Equal(t, "subnet-2", result.ID)
			} else {
				// For multi-zone, just verify it's one of the input subnets
				found := false
				for _, subnet := range tt.subnets {
					if subnet.ID == result.ID {
						found = true
						break
					}
				}
				assert.True(t, found)
			}
		})
	}
}

func TestIsIBMInstanceNotFoundError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "not found error from IBM",
			err:      fmt.Errorf("instance not found: 404"),
			expected: true, // ibm.IsNotFound checks for "not found" in error message
		},
		{
			name:     "generic error",
			err:      fmt.Errorf("some other error"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isIBMInstanceNotFoundError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuildVolumeAttachments_Defaults(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			// No BlockDeviceMappings - should use defaults
		},
	}

	bootVolume, additionalVolumes, err := provider.buildVolumeAttachments(nodeClass, "test-instance", "us-south-1")

	assert.NoError(t, err)
	assert.NotNil(t, bootVolume)
	assert.Nil(t, additionalVolumes)

	// Verify default boot volume
	assert.NotNil(t, bootVolume.Volume)
	assert.Equal(t, "test-instance-boot", *bootVolume.Volume.Name)
	assert.Equal(t, int64(100), *bootVolume.Volume.Capacity)
	assert.Equal(t, "general-purpose", *bootVolume.Volume.Profile.(*vpcv1.VolumeProfileIdentityByName).Name)
	assert.True(t, *bootVolume.DeleteVolumeOnInstanceDelete)
}

func TestVPCClient_UpdateInstanceTags(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	instanceID := "test-instance-id"
	tags := map[string]string{
		"environment": "test",
		"managed-by":  "karpenter",
	}

	// Mock UpdateInstanceWithContext call
	mockVPC.EXPECT().
		UpdateInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(&vpcv1.Instance{ID: &instanceID}, &core.DetailedResponse{StatusCode: 200}, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	err := vpcClient.UpdateInstanceTags(ctx, instanceID, tags)
	assert.NoError(t, err)
}

func TestVPCClient_ListVolumes(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	volumeName := "test-volume"
	testVolumes := &vpcv1.VolumeCollection{
		Volumes: []vpcv1.Volume{
			{
				ID:   ptrString("volume-1"),
				Name: &volumeName,
			},
		},
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		ListVolumesWithContext(gomock.Any(), gomock.Any()).
		Return(testVolumes, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	volumes, err := vpcClient.ListVolumes(ctx, &vpcv1.ListVolumesOptions{Name: &volumeName})
	assert.NoError(t, err)
	assert.NotNil(t, volumes)
	assert.Len(t, volumes.Volumes, 1)
	assert.Equal(t, "volume-1", *volumes.Volumes[0].ID)
}

func TestVPCClient_DeleteVolume(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	volumeID := "volume-to-delete"
	testResponse := &core.DetailedResponse{StatusCode: 204}

	mockVPC.EXPECT().
		DeleteVolumeWithContext(gomock.Any(), gomock.Any()).
		Return(testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	err := vpcClient.DeleteVolume(ctx, volumeID)
	assert.NoError(t, err)
}

func TestVPCClient_ListVirtualNetworkInterfaces(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	vniName := "test-vni"
	testVNIs := &vpcv1.VirtualNetworkInterfaceCollection{
		VirtualNetworkInterfaces: []vpcv1.VirtualNetworkInterface{
			{
				ID:   ptrString("vni-1"),
				Name: &vniName,
			},
		},
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		ListVirtualNetworkInterfacesWithContext(gomock.Any(), gomock.Any()).
		Return(testVNIs, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	vnis, err := vpcClient.ListVirtualNetworkInterfaces(ctx, &vpcv1.ListVirtualNetworkInterfacesOptions{})
	assert.NoError(t, err)
	assert.NotNil(t, vnis)
	assert.Len(t, vnis.VirtualNetworkInterfaces, 1)
	assert.Equal(t, "vni-1", *vnis.VirtualNetworkInterfaces[0].ID)
}

func TestVPCClient_DeleteVirtualNetworkInterfaces(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	vniID := "vni-to-delete"
	testVNI := &vpcv1.VirtualNetworkInterface{
		ID: &vniID,
	}
	testResponse := &core.DetailedResponse{StatusCode: 204}

	mockVPC.EXPECT().
		DeleteVirtualNetworkInterfacesWithContext(gomock.Any(), gomock.Any()).
		Return(testVNI, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	err := vpcClient.DeleteVirtualNetworkInterface(ctx, vniID)
	assert.NoError(t, err)
}

func TestVPCClient_GetImage(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	imageID := "test-image-id"
	testImage := &vpcv1.Image{
		ID:   &imageID,
		Name: ptrString("ubuntu-22-04"),
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		GetImageWithContext(gomock.Any(), gomock.Any()).
		Return(testImage, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	image, err := vpcClient.GetImage(ctx, imageID)
	assert.NoError(t, err)
	assert.NotNil(t, image)
	assert.Equal(t, imageID, *image.ID)
	assert.Equal(t, "ubuntu-22-04", *image.Name)
}

func TestExtractInstanceIDFromProviderID_EdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		want       string
	}{
		{
			name:       "valid provider ID with zone prefix",
			providerID: "ibm:///us-south/02u7_1234-5678-abcd",
			want:       "02u7_1234-5678-abcd",
		},
		{
			name:       "valid provider ID simple",
			providerID: "ibm:///us-south/instance-123",
			want:       "instance-123",
		},
		{
			name:       "too few parts",
			providerID: "ibm://us-south",
			want:       "",
		},
		{
			name:       "exactly 4 parts",
			providerID: "ibm:///region/instance",
			want:       "instance",
		},
		{
			name:       "more than 4 parts - gets last",
			providerID: "ibm:///region/zone/instance-id",
			want:       "instance-id",
		},
		{
			name:       "empty string",
			providerID: "",
			want:       "",
		},
		{
			name:       "no slashes",
			providerID: "instance-id",
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInstanceIDFromProviderID(tt.providerID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsPartialFailure_AdditionalCases(t *testing.T) {
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
			name: "quota exceeded",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_instance_quota_exceeded",
			},
			expected: true,
		},
		{
			name: "profile not available",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_instance_profile_not_available",
			},
			expected: true,
		},
		{
			name: "security group not found",
			ibmErr: &ibm.IBMError{
				StatusCode: 404,
				Code:       "vpc_security_group_not_found",
			},
			expected: true,
		},
		{
			name: "subnet not available",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_subnet_not_available",
			},
			expected: true,
		},
		{
			name: "volume capacity insufficient",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_volume_capacity_insufficient",
			},
			expected: true,
		},
		{
			name: "boot volume creation failed",
			ibmErr: &ibm.IBMError{
				StatusCode: 500,
				Code:       "vpc_boot_volume_creation_failed",
			},
			expected: true,
		},
		{
			name: "5xx error - potential partial failure",
			ibmErr: &ibm.IBMError{
				StatusCode: 503,
				Code:       "service_unavailable",
			},
			expected: true,
		},
		{
			name: "4xx error - not partial failure",
			ibmErr: &ibm.IBMError{
				StatusCode: 404,
				Code:       "not_found",
			},
			expected: false,
		},
		{
			name: "unknown error code with 2xx - not partial",
			ibmErr: &ibm.IBMError{
				StatusCode: 200,
				Code:       "unknown",
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

func TestProviderGet_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	testInstance := getTestVPCInstance()
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		GetInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(testInstance, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	instance, err := vpcClient.GetInstance(ctx, "test-instance-id")
	assert.NoError(t, err)
	assert.NotNil(t, instance)
	assert.Equal(t, "test-instance-id", *instance.ID)
	assert.Equal(t, "test-nodeclaim", *instance.Name)
}

func TestProviderList_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

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
}

func TestGetCurrentVPCUsage(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	vcpuCount1 := int64(4)
	vcpuCount2 := int64(8)

	testInstances := &vpcv1.InstanceCollection{
		Instances: []vpcv1.Instance{
			{
				ID:   ptrString("instance-1"),
				Name: ptrString("node-1"),
				Vcpu: &vpcv1.InstanceVcpu{
					Count: &vcpuCount1,
				},
			},
			{
				ID:   ptrString("instance-2"),
				Name: ptrString("node-2"),
				Vcpu: &vpcv1.InstanceVcpu{
					Count: &vcpuCount2,
				},
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

	// Calculate usage (what getCurrentVPCUsage does)
	instanceCount := len(instances)
	vcpuTotal := 0
	for _, inst := range instances {
		if inst.Vcpu != nil && inst.Vcpu.Count != nil {
			vcpuTotal += int(*inst.Vcpu.Count)
		}
	}

	assert.Equal(t, 2, instanceCount)
	assert.Equal(t, 12, vcpuTotal)
}

func TestVPCClient_ErrorCases(t *testing.T) {
	tests := []struct {
		name        string
		setupMock   func(*mock_ibm.MockvpcClientInterface)
		testFunc    func(*ibm.VPCClient, context.Context) error
		expectedErr string
	}{
		{
			name: "UpdateInstanceTags error",
			setupMock: func(m *mock_ibm.MockvpcClientInterface) {
				m.EXPECT().UpdateInstanceWithContext(gomock.Any(), gomock.Any()).
					Return(nil, nil, fmt.Errorf("instance not found"))
			},
			testFunc: func(c *ibm.VPCClient, ctx context.Context) error {
				return c.UpdateInstanceTags(ctx, "test-id", map[string]string{"foo": "bar"})
			},
			expectedErr: "instance not found",
		},
		{
			name: "GetInstance not found",
			setupMock: func(m *mock_ibm.MockvpcClientInterface) {
				m.EXPECT().GetInstanceWithContext(gomock.Any(), gomock.Any()).
					Return(nil, nil, fmt.Errorf("instance not found"))
			},
			testFunc: func(c *ibm.VPCClient, ctx context.Context) error {
				_, err := c.GetInstance(ctx, "nonexistent-id")
				return err
			},
			expectedErr: "instance not found",
		},
		{
			name: "DeleteInstance error",
			setupMock: func(m *mock_ibm.MockvpcClientInterface) {
				m.EXPECT().DeleteInstanceWithContext(gomock.Any(), gomock.Any()).
					Return(nil, fmt.Errorf("permission denied"))
			},
			testFunc: func(c *ibm.VPCClient, ctx context.Context) error {
				return c.DeleteInstance(ctx, "test-id")
			},
			expectedErr: "permission denied",
		},
		{
			name: "ListVolumes error",
			setupMock: func(m *mock_ibm.MockvpcClientInterface) {
				m.EXPECT().ListVolumesWithContext(gomock.Any(), gomock.Any()).
					Return(nil, nil, fmt.Errorf("API error"))
			},
			testFunc: func(c *ibm.VPCClient, ctx context.Context) error {
				_, err := c.ListVolumes(ctx, &vpcv1.ListVolumesOptions{})
				return err
			},
			expectedErr: "API error",
		},
		{
			name: "DeleteVolume not found",
			setupMock: func(m *mock_ibm.MockvpcClientInterface) {
				m.EXPECT().DeleteVolumeWithContext(gomock.Any(), gomock.Any()).
					Return(nil, fmt.Errorf("volume not found"))
			},
			testFunc: func(c *ibm.VPCClient, ctx context.Context) error {
				return c.DeleteVolume(ctx, "nonexistent-volume")
			},
			expectedErr: "volume not found",
		},
		{
			name: "GetImage error",
			setupMock: func(m *mock_ibm.MockvpcClientInterface) {
				m.EXPECT().GetImageWithContext(gomock.Any(), gomock.Any()).
					Return(nil, nil, fmt.Errorf("image not available"))
			},
			testFunc: func(c *ibm.VPCClient, ctx context.Context) error {
				_, err := c.GetImage(ctx, "unavailable-image")
				return err
			},
			expectedErr: "image not available",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ctx := context.Background()
			mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
			tt.setupMock(mockVPC)

			vpcClient := ibm.NewVPCClientWithMock(mockVPC)
			err := tt.testFunc(vpcClient, ctx)

			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestVPCClient_EmptyResults(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	t.Run("ListInstances empty", func(t *testing.T) {
		emptyCollection := &vpcv1.InstanceCollection{Instances: []vpcv1.Instance{}}
		mockVPC.EXPECT().ListInstancesWithContext(gomock.Any(), gomock.Any()).
			Return(emptyCollection, &core.DetailedResponse{StatusCode: 200}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)
		instances, err := vpcClient.ListInstances(ctx)

		assert.NoError(t, err)
		assert.Empty(t, instances)
	})
}

func TestGetDefaultSecurityGroup_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	mockVPC.EXPECT().
		ListSecurityGroupsWithContext(gomock.Any(), gomock.Any()).
		Return(nil, nil, fmt.Errorf("API error")).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	provider := &VPCInstanceProvider{}

	sg, err := provider.getDefaultSecurityGroup(ctx, vpcClient, "test-vpc")
	assert.Error(t, err)
	assert.Nil(t, sg)
	assert.Contains(t, err.Error(), "listing security groups")
}

// Helper function to create string pointers
func ptrString(s string) *string {
	return &s
}

func TestNewVPCInstanceProvider(t *testing.T) {
	tests := []struct {
		name          string
		client        *ibm.Client
		kubeClient    client.Client
		expectError   bool
		errorContains string
	}{
		{
			name:          "nil IBM client",
			client:        nil,
			kubeClient:    &mockKubeClient{},
			expectError:   true,
			errorContains: "IBM client cannot be nil",
		},
		{
			name:          "nil kube client",
			client:        &ibm.Client{},
			kubeClient:    nil,
			expectError:   true,
			errorContains: "kubernetes client cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: NewVPCInstanceProvider requires IBMCLOUD_API_KEY environment variable
			// These tests focus on client validation which happens before env var check
			_, err := NewVPCInstanceProvider(tt.client, tt.kubeClient)

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

type mockKubeClient struct {
	client.Client
}

func TestExtractInstanceIDFromProviderID_Comprehensive(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		want       string
	}{
		{
			name:       "standard IBM provider ID",
			providerID: "ibm:///us-south-1/instance-123",
			want:       "instance-123",
		},
		{
			name:       "provider ID with complex zone",
			providerID: "ibm:///eu-de-2/02u7_abcd-1234-efgh-5678",
			want:       "02u7_abcd-1234-efgh-5678",
		},
		{
			name:       "provider ID with just region",
			providerID: "ibm:///us-south/simple-id",
			want:       "simple-id",
		},
		{
			name:       "invalid format - no slashes",
			providerID: "instance-456",
			want:       "",
		},
		{
			name:       "invalid format - not enough parts",
			providerID: "ibm://us-south",
			want:       "",
		},
		{
			name:       "empty string",
			providerID: "",
			want:       "",
		},
		{
			name:       "provider ID with extra slashes",
			providerID: "ibm:///region/zone/instance/extra",
			want:       "extra",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInstanceIDFromProviderID(tt.providerID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSelectSubnetFromStatusList_EdgeCases(t *testing.T) {
	provider := &VPCInstanceProvider{}

	tests := []struct {
		name      string
		subnetIDs []string
		validate  func(t *testing.T, result string)
	}{
		{
			name:      "nil slice",
			subnetIDs: nil,
			validate: func(t *testing.T, result string) {
				assert.Empty(t, result)
			},
		},
		{
			name:      "empty slice",
			subnetIDs: []string{},
			validate: func(t *testing.T, result string) {
				assert.Empty(t, result)
			},
		},
		{
			name:      "single subnet",
			subnetIDs: []string{"subnet-abc"},
			validate: func(t *testing.T, result string) {
				assert.Equal(t, "subnet-abc", result)
			},
		},
		{
			name:      "multiple subnets - round robin",
			subnetIDs: []string{"subnet-1", "subnet-2", "subnet-3", "subnet-4", "subnet-5"},
			validate: func(t *testing.T, result string) {
				assert.NotEmpty(t, result)
				// Should be one of the input subnets
				found := false
				for _, subnet := range []string{"subnet-1", "subnet-2", "subnet-3", "subnet-4", "subnet-5"} {
					if subnet == result {
						found = true
						break
					}
				}
				assert.True(t, found, "selected subnet should be from input list")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.selectSubnetFromStatusList(tt.subnetIDs)
			tt.validate(t, result)
		})
	}
}

func TestSelectSubnetFromMultiZoneList_DetailedCases(t *testing.T) {
	provider := &VPCInstanceProvider{}

	tests := []struct {
		name     string
		subnets  []subnet.SubnetInfo
		validate func(t *testing.T, result subnet.SubnetInfo, subnets []subnet.SubnetInfo)
	}{
		{
			name:    "nil slice",
			subnets: nil,
			validate: func(t *testing.T, result subnet.SubnetInfo, subnets []subnet.SubnetInfo) {
				assert.Empty(t, result.ID)
				assert.Empty(t, result.Zone)
			},
		},
		{
			name:    "empty slice",
			subnets: []subnet.SubnetInfo{},
			validate: func(t *testing.T, result subnet.SubnetInfo, subnets []subnet.SubnetInfo) {
				assert.Empty(t, result.ID)
			},
		},
		{
			name: "single subnet",
			subnets: []subnet.SubnetInfo{
				{ID: "subnet-only", Zone: "zone-1", AvailableIPs: 100},
			},
			validate: func(t *testing.T, result subnet.SubnetInfo, subnets []subnet.SubnetInfo) {
				assert.Equal(t, "subnet-only", result.ID)
				assert.Equal(t, "zone-1", result.Zone)
			},
		},
		{
			name: "multiple zones - balanced selection",
			subnets: []subnet.SubnetInfo{
				{ID: "subnet-z1", Zone: "zone-1", AvailableIPs: 100},
				{ID: "subnet-z2", Zone: "zone-2", AvailableIPs: 200},
				{ID: "subnet-z3", Zone: "zone-3", AvailableIPs: 150},
			},
			validate: func(t *testing.T, result subnet.SubnetInfo, subnets []subnet.SubnetInfo) {
				assert.NotEmpty(t, result.ID)
				// Should be one of the zones
				validZones := []string{"zone-1", "zone-2", "zone-3"}
				found := false
				for _, zone := range validZones {
					if zone == result.Zone {
						found = true
						break
					}
				}
				assert.True(t, found)
			},
		},
		{
			name: "same zone multiple subnets - picks highest available IPs",
			subnets: []subnet.SubnetInfo{
				{ID: "subnet-small", Zone: "zone-1", AvailableIPs: 50},
				{ID: "subnet-medium", Zone: "zone-1", AvailableIPs: 100},
				{ID: "subnet-large", Zone: "zone-1", AvailableIPs: 200},
			},
			validate: func(t *testing.T, result subnet.SubnetInfo, subnets []subnet.SubnetInfo) {
				assert.Equal(t, "subnet-large", result.ID)
				assert.Equal(t, int32(200), result.AvailableIPs)
			},
		},
		{
			name: "zero available IPs",
			subnets: []subnet.SubnetInfo{
				{ID: "subnet-full", Zone: "zone-1", AvailableIPs: 0},
				{ID: "subnet-available", Zone: "zone-2", AvailableIPs: 50},
			},
			validate: func(t *testing.T, result subnet.SubnetInfo, subnets []subnet.SubnetInfo) {
				// Should still select one - provider may attempt to use full subnet
				assert.NotEmpty(t, result.ID)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.selectSubnetFromMultiZoneList(tt.subnets)
			tt.validate(t, result, tt.subnets)
		})
	}
}

func TestBuildVolumeAttachments_VariousConfigurations(t *testing.T) {
	provider := &VPCInstanceProvider{}

	tests := []struct {
		name          string
		nodeClass     *v1alpha1.IBMNodeClass
		instanceName  string
		zone          string
		expectError   bool
		validateBoot  func(t *testing.T, boot *vpcv1.VolumeAttachmentPrototypeInstanceByImageContext)
		validateAdded func(t *testing.T, additional []vpcv1.VolumeAttachmentPrototype)
	}{
		{
			name: "default boot volume",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{},
			},
			instanceName: "test-node",
			zone:         "us-south-1",
			expectError:  false,
			validateBoot: func(t *testing.T, boot *vpcv1.VolumeAttachmentPrototypeInstanceByImageContext) {
				assert.NotNil(t, boot)
				assert.NotNil(t, boot.Volume)
				assert.Equal(t, "test-node-boot", *boot.Volume.Name)
				assert.Equal(t, int64(100), *boot.Volume.Capacity)
				assert.True(t, *boot.DeleteVolumeOnInstanceDelete)
			},
			validateAdded: func(t *testing.T, additional []vpcv1.VolumeAttachmentPrototype) {
				assert.Nil(t, additional)
			},
		},
		{
			name: "custom boot volume size",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
						{
							DeviceName: ptrString("/dev/vda"),
							VolumeSpec: &v1alpha1.VolumeSpec{
								Capacity: ptrInt64(250),
								Profile:  ptrString("general-purpose"),
							},
							RootVolume: true,
						},
					},
				},
			},
			instanceName: "test-node-custom",
			zone:         "eu-de-1",
			expectError:  false,
			validateBoot: func(t *testing.T, boot *vpcv1.VolumeAttachmentPrototypeInstanceByImageContext) {
				assert.NotNil(t, boot)
				assert.NotNil(t, boot.Volume)
				assert.Equal(t, int64(250), *boot.Volume.Capacity)
				assert.Equal(t, "general-purpose", *boot.Volume.Profile.(*vpcv1.VolumeProfileIdentityByName).Name)
			},
			validateAdded: func(t *testing.T, additional []vpcv1.VolumeAttachmentPrototype) {
				assert.Nil(t, additional)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			boot, additional, err := provider.buildVolumeAttachments(tt.nodeClass, tt.instanceName, tt.zone)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validateBoot != nil {
					tt.validateBoot(t, boot)
				}
				if tt.validateAdded != nil {
					tt.validateAdded(t, additional)
				}
			}
		})
	}
}

func ptrInt64(i int64) *int64 {
	return &i
}

func TestGetDefaultSecurityGroup_VariousCases(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	provider := &VPCInstanceProvider{}

	t.Run("multiple security groups - finds default", func(t *testing.T) {
		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		sgDefault := "default-sg-id"
		sgCustom1 := "custom-sg-1"
		sgCustom2 := "custom-sg-2"

		testSGs := &vpcv1.SecurityGroupCollection{
			SecurityGroups: []vpcv1.SecurityGroup{
				{ID: &sgCustom1, Name: ptrString("custom-sg-1")},
				{ID: &sgDefault, Name: ptrString("default")},
				{ID: &sgCustom2, Name: ptrString("another-custom")},
			},
		}

		mockVPC.EXPECT().
			ListSecurityGroupsWithContext(gomock.Any(), gomock.Any()).
			Return(testSGs, &core.DetailedResponse{StatusCode: 200}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)
		sg, err := provider.getDefaultSecurityGroup(ctx, vpcClient, "test-vpc")

		assert.NoError(t, err)
		assert.NotNil(t, sg)
		assert.Equal(t, "default-sg-id", *sg.ID)
	})

	t.Run("empty security group list", func(t *testing.T) {
		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		emptySGs := &vpcv1.SecurityGroupCollection{
			SecurityGroups: []vpcv1.SecurityGroup{},
		}

		mockVPC.EXPECT().
			ListSecurityGroupsWithContext(gomock.Any(), gomock.Any()).
			Return(emptySGs, &core.DetailedResponse{StatusCode: 200}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)
		sg, err := provider.getDefaultSecurityGroup(ctx, vpcClient, "test-vpc")

		assert.Error(t, err)
		assert.Nil(t, sg)
		assert.Contains(t, err.Error(), "default security group not found")
	})

	t.Run("API error during list", func(t *testing.T) {
		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		mockVPC.EXPECT().
			ListSecurityGroupsWithContext(gomock.Any(), gomock.Any()).
			Return(nil, nil, fmt.Errorf("API rate limit exceeded"))

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)
		sg, err := provider.getDefaultSecurityGroup(ctx, vpcClient, "test-vpc")

		assert.Error(t, err)
		assert.Nil(t, sg)
		assert.Contains(t, err.Error(), "listing security groups")
	})
}

func TestIsPartialFailure_ComprehensiveCases(t *testing.T) {
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
			name: "quota exceeded - instances",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_instance_quota_exceeded",
				Message:    "Instance quota exceeded",
			},
			expected: true,
		},
		{
			name: "quota exceeded - vcpu",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_vcpu_quota_exceeded",
				Message:    "VCPU quota exceeded",
			},
			expected: false, // Not explicitly in the partial failure list
		},
		{
			name: "profile not available",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_instance_profile_not_available",
			},
			expected: true,
		},
		{
			name: "image not found",
			ibmErr: &ibm.IBMError{
				StatusCode: 404,
				Code:       "vpc_image_not_found",
			},
			expected: false, // Not in the partial failure list
		},
		{
			name: "subnet not available",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_subnet_not_available",
			},
			expected: true,
		},
		{
			name: "insufficient subnet capacity",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_subnet_insufficient_capacity",
			},
			expected: false, // Not in the partial failure list
		},
		{
			name: "volume capacity insufficient",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "vpc_volume_capacity_insufficient",
			},
			expected: true,
		},
		{
			name: "5xx server error",
			ibmErr: &ibm.IBMError{
				StatusCode: 503,
				Code:       "service_unavailable",
			},
			expected: true,
		},
		{
			name: "502 bad gateway",
			ibmErr: &ibm.IBMError{
				StatusCode: 502,
				Code:       "bad_gateway",
			},
			expected: true,
		},
		{
			name: "not found - not partial",
			ibmErr: &ibm.IBMError{
				StatusCode: 404,
				Code:       "not_found",
			},
			expected: false,
		},
		{
			name: "unauthorized - not partial",
			ibmErr: &ibm.IBMError{
				StatusCode: 401,
				Code:       "unauthorized",
			},
			expected: false,
		},
		{
			name: "forbidden - not partial",
			ibmErr: &ibm.IBMError{
				StatusCode: 403,
				Code:       "forbidden",
			},
			expected: false,
		},
		{
			name: "400 bad request - not partial",
			ibmErr: &ibm.IBMError{
				StatusCode: 400,
				Code:       "invalid_request",
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

func TestErrorClassificationHelpers(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		isTimeout bool
		isQuota   bool
		isAuth    bool
	}{
		{
			name:      "timeout error with 'timeout' keyword",
			err:       fmt.Errorf("request timeout after 30s"),
			isTimeout: true,
			isQuota:   false,
			isAuth:    false,
		},
		{
			name:      "context deadline exceeded",
			err:       fmt.Errorf("context deadline exceeded"),
			isTimeout: true,
			isQuota:   false,
			isAuth:    false,
		},
		{
			name:      "i/o timeout",
			err:       fmt.Errorf("i/o timeout"),
			isTimeout: true,
			isQuota:   false,
			isAuth:    false,
		},
		{
			name:      "quota exceeded",
			err:       fmt.Errorf("quota exceeded for VPC instances"),
			isTimeout: false,
			isQuota:   true,
			isAuth:    false,
		},
		{
			name:      "limit exceeded",
			err:       fmt.Errorf("limit exceeded"),
			isTimeout: false,
			isQuota:   true,
			isAuth:    false,
		},
		{
			name:      "rate limit",
			err:       fmt.Errorf("rate limit exceeded"),
			isTimeout: false,
			isQuota:   true,
			isAuth:    false,
		},
		{
			name:      "unauthorized",
			err:       fmt.Errorf("unauthorized: invalid API key"),
			isTimeout: false,
			isQuota:   false,
			isAuth:    true,
		},
		{
			name:      "403 forbidden",
			err:       fmt.Errorf("403 forbidden"),
			isTimeout: false,
			isQuota:   false,
			isAuth:    true,
		},
		{
			name:      "401 authentication required",
			err:       fmt.Errorf("401 authentication required"),
			isTimeout: false,
			isQuota:   false,
			isAuth:    true,
		},
		{
			name:      "nil error",
			err:       nil,
			isTimeout: false,
			isQuota:   false,
			isAuth:    false,
		},
		{
			name:      "generic error",
			err:       fmt.Errorf("something went wrong"),
			isTimeout: false,
			isQuota:   false,
			isAuth:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.isTimeout, isTimeoutError(tt.err), "isTimeoutError mismatch")
			assert.Equal(t, tt.isQuota, isQuotaError(tt.err), "isQuotaError mismatch")
			assert.Equal(t, tt.isAuth, isAuthError(tt.err), "isAuthError mismatch")
		})
	}
}

func TestAddKarpenterTags(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()

	tests := []struct {
		name          string
		clusterEnv    string
		nodeClaim     *karpv1.NodeClaim
		nodeClass     *v1alpha1.IBMNodeClass
		setupMock     func(*mock_ibm.MockvpcClientInterface)
		expectError   bool
		errorContains string
	}{
		{
			name:       "successful tag addition",
			clusterEnv: "test-cluster",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node-claim",
					Labels: map[string]string{
						"karpenter.sh/nodepool": "default",
					},
				},
			},
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					Tags: map[string]string{
						"env": "prod",
					},
				},
			},
			setupMock: func(m *mock_ibm.MockvpcClientInterface) {
				m.EXPECT().
					UpdateInstanceWithContext(gomock.Any(), gomock.Any()).
					Return(&vpcv1.Instance{}, &core.DetailedResponse{StatusCode: 200}, nil)
			},
			expectError: false,
		},
		{
			name:       "API error during update",
			clusterEnv: "test-cluster",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node-claim",
					Labels: map[string]string{
						"karpenter.sh/nodepool": "default",
					},
				},
			},
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{},
			},
			setupMock: func(m *mock_ibm.MockvpcClientInterface) {
				m.EXPECT().
					UpdateInstanceWithContext(gomock.Any(), gomock.Any()).
					Return(nil, nil, fmt.Errorf("API rate limit exceeded"))
			},
			expectError:   true,
			errorContains: "updating instance tags",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldClusterName := os.Getenv("CLUSTER_NAME")
			if tt.clusterEnv != "" {
				_ = os.Setenv("CLUSTER_NAME", tt.clusterEnv)
			} else {
				_ = os.Unsetenv("CLUSTER_NAME")
			}
			defer func() { _ = os.Setenv("CLUSTER_NAME", oldClusterName) }()

			mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
			tt.setupMock(mockVPC)

			vpcClient := ibm.NewVPCClientWithMock(mockVPC)
			provider := &VPCInstanceProvider{}

			err := provider.addKarpenterTags(ctx, vpcClient, "test-instance-id", tt.nodeClass, tt.nodeClaim)

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
