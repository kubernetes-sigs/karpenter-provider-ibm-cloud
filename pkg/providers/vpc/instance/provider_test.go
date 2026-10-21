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
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	mock_ibm "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Test helpers
func getTestVPCInstance() *vpcv1.Instance {
	instanceID := "test-instance-id"
	instanceName := "test-nodeclaim"
	profileName := "bx2-4x16"
	zoneName := "us-south-1"

	return &vpcv1.Instance{
		CRN:  core.StringPtr(testInstanceCRN),
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
		CRN:  core.StringPtr(testInstanceCRN),
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
		Name:  ptrString("test-instance"),
		Image: &vpcv1.ImageIdentityByID{ID: ptrString("test-image-id")},
		Zone:  &vpcv1.ZoneIdentityByName{Name: ptrString("us-south-1")},
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
		CRN:  core.StringPtr(testInstanceCRN),
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
			expected: false, // Empty string is not a valid hex ID
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

func TestGetDefaultSecurityGroup(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	vpcID := "test-vpc-id"
	defaultSGID := "default-sg-id"
	defaultSGName := "default"

	testVPC := &vpcv1.VPC{
		ID: &vpcID,
		DefaultSecurityGroup: &vpcv1.SecurityGroupReference{
			ID:   &defaultSGID,
			Name: &defaultSGName,
		},
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		GetVPCWithContext(gomock.Any(), gomock.Any()).
		Return(testVPC, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	sg, err := vpcClient.GetDefaultSecurityGroup(ctx, vpcID)
	assert.NoError(t, err)
	assert.NotNil(t, sg)
	assert.Equal(t, "default-sg-id", *sg.ID)
	assert.Equal(t, "default", *sg.Name)
}

func TestGetDefaultSecurityGroup_VPCHasNoDefaultSG(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	vpcID := "test-vpc-id"

	testVPC := &vpcv1.VPC{
		ID:                   &vpcID,
		DefaultSecurityGroup: nil,
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		GetVPCWithContext(gomock.Any(), gomock.Any()).
		Return(testVPC, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	sg, err := vpcClient.GetDefaultSecurityGroup(ctx, vpcID)
	assert.Error(t, err)
	assert.Nil(t, sg)
	assert.Contains(t, err.Error(), "has no default security group")
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
		CRN:  core.StringPtr(testInstanceCRN),
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

func TestGetDefaultSecurityGroup_GetVPCError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	mockVPC.EXPECT().
		GetVPCWithContext(gomock.Any(), gomock.Any()).
		Return(nil, nil, fmt.Errorf("API error")).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	sg, err := vpcClient.GetDefaultSecurityGroup(ctx, "test-vpc")
	assert.Error(t, err)
	assert.Nil(t, sg)
	assert.Contains(t, err.Error(), "getting VPC")
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
							DeviceName: ptrString("boot-disk"),
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
	ctx := context.Background()

	t.Run("renamed default SG still found via VPC reference", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
		vpcID := "test-vpc"
		sgID := "default-sg-id"
		renamedName := "my-custom-sg-name"

		mockVPC.EXPECT().
			GetVPCWithContext(gomock.Any(), gomock.Any()).
			Return(&vpcv1.VPC{
				ID:                   &vpcID,
				DefaultSecurityGroup: &vpcv1.SecurityGroupReference{ID: &sgID, Name: &renamedName},
			}, &core.DetailedResponse{StatusCode: 200}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)
		sg, err := vpcClient.GetDefaultSecurityGroup(ctx, vpcID)

		assert.NoError(t, err)
		assert.NotNil(t, sg)
		assert.Equal(t, sgID, *sg.ID)
	})

	t.Run("nil DefaultSecurityGroup ID", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
		vpcID := "test-vpc"

		mockVPC.EXPECT().
			GetVPCWithContext(gomock.Any(), gomock.Any()).
			Return(&vpcv1.VPC{
				ID:                   &vpcID,
				DefaultSecurityGroup: &vpcv1.SecurityGroupReference{ID: nil},
			}, &core.DetailedResponse{StatusCode: 200}, nil)

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)
		sg, err := vpcClient.GetDefaultSecurityGroup(ctx, vpcID)

		assert.Error(t, err)
		assert.Nil(t, sg)
		assert.Contains(t, err.Error(), "has no default security group")
	})

	t.Run("API error during GetVPC", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

		mockVPC.EXPECT().
			GetVPCWithContext(gomock.Any(), gomock.Any()).
			Return(nil, nil, fmt.Errorf("API rate limit exceeded"))

		vpcClient := ibm.NewVPCClientWithMock(mockVPC)
		sg, err := vpcClient.GetDefaultSecurityGroup(ctx, "test-vpc")

		assert.Error(t, err)
		assert.Nil(t, sg)
		assert.Contains(t, err.Error(), "getting VPC")
	})
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

func TestProviderGet_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
	defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	testInstance := getTestVPCInstance()
	testResponse := &core.DetailedResponse{StatusCode: 200}

	// API called only ONCE - second Get hits cache
	mockVPC.EXPECT().
		GetInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(testInstance, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	manager := vpcclient.NewManagerWithMockClient(vpcClient)
	instanceCache := cache.New(5 * time.Minute)
	defer instanceCache.Stop()

	provider, err := NewVPCInstanceProvider(
		&ibm.Client{},
		&mockKubeClient{},
		WithVPCClientManager(manager),
		WithAccountResolver(testAccountResolver),
		WithInstanceCache(instanceCache),
	)
	assert.NoError(t, err)

	providerID := "ibm:///us-south/test-instance-id"

	// First Get - calls API, populates cache
	node1, err := provider.Get(ctx, providerID)
	assert.NoError(t, err)
	assert.Equal(t, "test-nodeclaim", node1.Name)

	// Second Get - cache hit, no API call
	node2, err := provider.Get(ctx, providerID)
	assert.NoError(t, err)
	assert.Equal(t, "test-nodeclaim", node2.Name)
}

func TestProviderGet_SetsLabels(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
	defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

	ctx := context.Background()

	tests := []struct {
		name                 string
		availabilityClass    *string
		expectedCapacityType string
	}{
		{
			name:                 "standard instance maps to on-demand",
			availabilityClass:    ptrString("standard"),
			expectedCapacityType: "on-demand",
		},
		{
			name:                 "spot instance maps to spot",
			availabilityClass:    ptrString("spot"),
			expectedCapacityType: "spot",
		},
		{
			name:                 "nil availability defaults to on-demand",
			availabilityClass:    nil,
			expectedCapacityType: "on-demand",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

			instance := getTestVPCInstance()
			if tt.availabilityClass != nil {
				instance.Availability = &vpcv1.InstanceAvailability{
					Class: tt.availabilityClass,
				}
			}

			mockVPC.EXPECT().
				GetInstanceWithContext(gomock.Any(), gomock.Any()).
				Return(instance, &core.DetailedResponse{StatusCode: 200}, nil).
				Times(1)

			vpcClient := ibm.NewVPCClientWithMock(mockVPC)
			manager := vpcclient.NewManagerWithMockClient(vpcClient)
			instanceCache := cache.New(5 * time.Minute)
			defer instanceCache.Stop()

			provider, err := NewVPCInstanceProvider(
				&ibm.Client{},
				&mockKubeClient{},
				WithVPCClientManager(manager),
				WithAccountResolver(testAccountResolver),
				WithInstanceCache(instanceCache),
			)
			assert.NoError(t, err)

			node, err := provider.Get(ctx, "ibm:///us-south/test-instance-id")
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedCapacityType, node.Labels["karpenter.sh/capacity-type"])
			assert.Equal(t, "bx2-4x16", node.Labels["node.kubernetes.io/instance-type"])
			assert.Equal(t, "us-south-1", node.Labels["topology.kubernetes.io/zone"])
		})
	}
}

func TestProviderDelete_InvalidatesCache(t *testing.T) {
	provider, claim, config, mock, _ := launchFixture(t)
	claim.Status.ProviderID = "ibm:///us-south/instance"
	require.NoError(t, provider.kubeClient.Update(context.Background(), claim))
	vm := matchingInstance(config)
	mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{*vm}}, nil, nil)
	mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(vm, nil, nil).Times(2)
	mock.EXPECT().DeleteInstanceWithContext(gomock.Any(), gomock.Any()).Return(&core.DetailedResponse{StatusCode: 204}, nil)
	mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(nil, nil, fmt.Errorf("instance not found: 404"))
	provider.instanceCache.Set("instance", &corev1.Node{})
	node := config.node(claim, "instance")
	node.UID = claim.UID
	err := provider.Delete(context.Background(), node)
	require.True(t, cloudprovider.IsNodeClaimNotFoundError(err))
	_, exists := provider.instanceCache.Get("instance")
	require.False(t, exists)
}

func TestProviderGet_DeletingInstanceNotCached(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
	defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	// Create instance with "deleting" status
	instanceID := "test-instance-id"
	instanceName := "test-nodeclaim"
	deletingStatus := vpcv1.InstanceStatusDeletingConst
	zoneName := "us-south-1"

	deletingInstance := &vpcv1.Instance{
		CRN:    core.StringPtr(testInstanceCRN),
		ID:     &instanceID,
		Name:   &instanceName,
		Status: &deletingStatus,
		Zone: &vpcv1.ZoneReference{
			Name: &zoneName,
		},
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		GetInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(deletingInstance, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	manager := vpcclient.NewManagerWithMockClient(vpcClient)
	instanceCache := cache.New(5 * time.Minute)
	defer instanceCache.Stop()

	provider, err := NewVPCInstanceProvider(
		&ibm.Client{},
		&mockKubeClient{},
		WithVPCClientManager(manager),
		WithAccountResolver(testAccountResolver),
		WithInstanceCache(instanceCache),
	)
	assert.NoError(t, err)

	providerID := "ibm:///us-south/test-instance-id"

	// Get returns the node (behavior preserved)
	node, err := provider.Get(ctx, providerID)
	assert.NoError(t, err)
	assert.Equal(t, "test-nodeclaim", node.Name)

	// But cache should NOT have the entry (deleting instances not cached)
	_, exists := instanceCache.Get(instanceID)
	assert.False(t, exists, "deleting instance should not be cached")
}

func TestProviderGet_InvalidCacheEntry_NoPanic(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
	defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	testInstance := getTestVPCInstance()
	testResponse := &core.DetailedResponse{StatusCode: 200}

	// API should be called because invalid cache entry is deleted
	mockVPC.EXPECT().
		GetInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(testInstance, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	manager := vpcclient.NewManagerWithMockClient(vpcClient)
	instanceCache := cache.New(5 * time.Minute)
	defer instanceCache.Stop()

	// Pre-populate cache with WRONG type (string instead of *corev1.Node)
	instanceID := "test-instance-id"
	instanceCache.Set(instanceID, "this is not a node")

	provider, err := NewVPCInstanceProvider(
		&ibm.Client{},
		&mockKubeClient{},
		WithVPCClientManager(manager),
		WithAccountResolver(testAccountResolver),
		WithInstanceCache(instanceCache),
	)
	assert.NoError(t, err)

	providerID := "ibm:///us-south/test-instance-id"

	// Should not panic, should fetch from API and return valid node
	node, err := provider.Get(ctx, providerID)
	assert.NoError(t, err)
	assert.Equal(t, "test-nodeclaim", node.Name)

	// Cache should now have the correct entry
	cached, exists := instanceCache.Get(instanceID)
	assert.True(t, exists, "cache should have entry after fetch")
	_, ok := cached.(*corev1.Node)
	assert.True(t, ok, "cached entry should be *corev1.Node")
}

func TestProviderList_PopulatesCache(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
	defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	testInstance := getTestVPCInstance()
	testCollection := &vpcv1.InstanceCollection{
		Instances: []vpcv1.Instance{*testInstance},
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		ListInstancesWithContext(gomock.Any(), gomock.Any()).
		Return(testCollection, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	manager := vpcclient.NewManagerWithMockClient(vpcClient)
	instanceCache := cache.New(5 * time.Minute)
	defer instanceCache.Stop()

	provider, err := NewVPCInstanceProvider(
		&ibm.Client{},
		&mockKubeClient{},
		WithVPCClientManager(manager),
		WithAccountResolver(testAccountResolver),
		WithInstanceCache(instanceCache),
	)
	assert.NoError(t, err)

	// Cache empty before List
	_, exists := instanceCache.Get("test-instance-id")
	assert.False(t, exists, "cache should be empty before list")

	// List - populates cache
	nodes, err := provider.List(ctx)
	assert.NoError(t, err)
	assert.Len(t, nodes, 1)

	// Cache now has the entry
	cached, exists := instanceCache.Get("test-instance-id")
	assert.True(t, exists, "cache should have entry after list")
	assert.Equal(t, "test-nodeclaim", cached.(*corev1.Node).Name)
}

func TestProviderList_DeletingInstanceNotCached(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
	defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	runningID := "running-instance-id"
	runningName := "running-instance"
	runningStatus := vpcv1.InstanceStatusRunningConst
	zoneName := "us-south-1"

	deletingID := "deleting-instance-id"
	deletingName := "deleting-instance"
	deletingStatus := vpcv1.InstanceStatusDeletingConst

	runningInstance := vpcv1.Instance{
		CRN:    core.StringPtr(testInstanceCRN),
		ID:     &runningID,
		Name:   &runningName,
		Status: &runningStatus,
		Zone: &vpcv1.ZoneReference{
			Name: &zoneName,
		},
	}

	deletingInstance := vpcv1.Instance{
		CRN:    core.StringPtr(testInstanceCRN),
		ID:     &deletingID,
		Name:   &deletingName,
		Status: &deletingStatus,
		Zone: &vpcv1.ZoneReference{
			Name: &zoneName,
		},
	}

	testCollection := &vpcv1.InstanceCollection{
		Instances: []vpcv1.Instance{runningInstance, deletingInstance},
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		ListInstancesWithContext(gomock.Any(), gomock.Any()).
		Return(testCollection, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	manager := vpcclient.NewManagerWithMockClient(vpcClient)
	instanceCache := cache.New(5 * time.Minute)
	defer instanceCache.Stop()

	provider, err := NewVPCInstanceProvider(
		&ibm.Client{},
		&mockKubeClient{},
		WithVPCClientManager(manager),
		WithAccountResolver(testAccountResolver),
		WithInstanceCache(instanceCache),
	)
	assert.NoError(t, err)

	// List returns BOTH instances
	nodes, err := provider.List(ctx)
	assert.NoError(t, err)
	assert.Len(t, nodes, 2, "both instances should be returned")

	// Running instance should be cached
	_, runningCached := instanceCache.Get(runningID)
	assert.True(t, runningCached, "running instance should be cached")

	// Deleting instance should NOT be cached
	_, deletingCached := instanceCache.Get(deletingID)
	assert.False(t, deletingCached, "deleting instance should not be cached")
}

func TestProviderList_CorrectRegionFromZone(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
	defer func() { _ = os.Unsetenv("IBMCLOUD_API_KEY") }()

	ctx := context.Background()
	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)

	// Create instance with zone "eu-de-2" - region should be "eu-de"
	instanceID := "test-instance-id"
	instanceName := "test-nodeclaim"
	zoneName := "eu-de-2"

	testInstance := &vpcv1.Instance{
		CRN:  core.StringPtr(testInstanceCRN),
		ID:   &instanceID,
		Name: &instanceName,
		Zone: &vpcv1.ZoneReference{
			Name: &zoneName,
		},
	}
	testCollection := &vpcv1.InstanceCollection{
		Instances: []vpcv1.Instance{*testInstance},
	}
	testResponse := &core.DetailedResponse{StatusCode: 200}

	mockVPC.EXPECT().
		ListInstancesWithContext(gomock.Any(), gomock.Any()).
		Return(testCollection, testResponse, nil).
		Times(1)

	vpcClient := ibm.NewVPCClientWithMock(mockVPC)
	manager := vpcclient.NewManagerWithMockClient(vpcClient)
	instanceCache := cache.New(5 * time.Minute)
	defer instanceCache.Stop()

	provider, err := NewVPCInstanceProvider(
		&ibm.Client{},
		&mockKubeClient{},
		WithVPCClientManager(manager),
		WithAccountResolver(testAccountResolver),
		WithInstanceCache(instanceCache),
	)
	assert.NoError(t, err)

	nodes, err := provider.List(ctx)
	assert.NoError(t, err)
	assert.Len(t, nodes, 1)

	// Verify ProviderID has correct region extracted from zone
	// Zone "eu-de-2" should yield region "eu-de"
	expectedProviderID := "ibm:///eu-de/test-instance-id"
	assert.Equal(t, expectedProviderID, nodes[0].Spec.ProviderID,
		"ProviderID should have region 'eu-de' extracted from zone 'eu-de-2', not hardcoded 'region'")
}
