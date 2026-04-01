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
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/bootstrap"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
)

func ptrBool(b bool) *bool { return &b }

// -- Option functions nil validation --

func TestWithKubernetesClient_Nil(t *testing.T) {
	opt := WithKubernetesClient(nil)
	err := opt(&VPCInstanceProvider{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kubernetes client cannot be nil")
}

func TestWithBootstrapProvider_Nil(t *testing.T) {
	opt := WithBootstrapProvider(nil)
	err := opt(&VPCInstanceProvider{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "bootstrap provider cannot be nil")
}

func TestWithVPCClientManager_Nil(t *testing.T) {
	opt := WithVPCClientManager(nil)
	err := opt(&VPCInstanceProvider{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "VPC client manager cannot be nil")
}

func TestWithInstanceCache_Nil(t *testing.T) {
	opt := WithInstanceCache(nil)
	err := opt(&VPCInstanceProvider{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "instance cache cannot be nil")
}

// -- Option functions success paths --

func TestWithBootstrapProvider_NonNil(t *testing.T) {
	bp := &bootstrap.VPCBootstrapProvider{}
	provider := &VPCInstanceProvider{}
	opt := WithBootstrapProvider(bp)
	err := opt(provider)
	assert.NoError(t, err)
	assert.Equal(t, bp, provider.bootstrapProvider)
}

func TestWithVPCClientManager_NonNil(t *testing.T) {
	mgr := &vpcclient.Manager{}
	provider := &VPCInstanceProvider{}
	opt := WithVPCClientManager(mgr)
	err := opt(provider)
	assert.NoError(t, err)
	assert.Equal(t, mgr, provider.vpcClientManager)
}

func TestWithInstanceCache_NonNil(t *testing.T) {
	c := cache.New(5 * time.Minute)
	defer c.Stop()
	provider := &VPCInstanceProvider{}
	opt := WithInstanceCache(c)
	err := opt(provider)
	assert.NoError(t, err)
	assert.Equal(t, c, provider.instanceCache)
}

// -- NewVPCInstanceProvider error paths --

func TestNewVPCInstanceProvider_NilClient(t *testing.T) {
	_, err := NewVPCInstanceProvider(nil, &mockKubeClient{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "IBM client cannot be nil")
}

func TestNewVPCInstanceProvider_NilKubeClient(t *testing.T) {
	_, err := NewVPCInstanceProvider(&ibm.Client{}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kubernetes client cannot be nil")
}

func TestNewVPCInstanceProvider_OptionError(t *testing.T) {
	badOpt := func(p *VPCInstanceProvider) error {
		return fmt.Errorf("bad option")
	}
	_, err := NewVPCInstanceProvider(&ibm.Client{}, &mockKubeClient{}, badOpt)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "applying option")
	assert.Contains(t, err.Error(), "bad option")
}

func TestNewVPCInstanceProvider_MissingAPIKey(t *testing.T) {
	old := os.Getenv("IBMCLOUD_API_KEY")
	_ = os.Unsetenv("IBMCLOUD_API_KEY")
	defer func() {
		if old != "" {
			_ = os.Setenv("IBMCLOUD_API_KEY", old)
		}
	}()

	_, err := NewVPCInstanceProvider(&ibm.Client{}, &mockKubeClient{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "IBMCLOUD_API_KEY")
}

// -- buildVolumeAttachments with custom BlockDeviceMappings --

func TestBuildVolumeAttachments_BootVolumeFullSpec(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					DeviceName: ptrString("custom-boot"),
					RootVolume: true,
					VolumeSpec: &v1alpha1.VolumeSpec{
						Capacity:            ptrInt64(200),
						Profile:             ptrString("custom"),
						IOPS:                ptrInt64(6000),
						Bandwidth:           ptrInt64(1000),
						EncryptionKeyID:     ptrString("crn:v1:bluemix:public:kms:us-south:a/abc123"),
						DeleteOnTermination: ptrBool(false),
						Tags:                []string{"env:prod", "team:platform"},
					},
				},
			},
		},
	}

	boot, additional, err := provider.buildVolumeAttachments(nodeClass, "my-instance", "us-south-1")
	assert.NoError(t, err)
	assert.NotNil(t, boot)
	assert.Nil(t, additional)

	// Boot volume name is always auto-generated from instance name
	assert.Equal(t, "my-instance-boot", *boot.Volume.Name)
	assert.Equal(t, int64(200), *boot.Volume.Capacity)
	assert.Equal(t, "custom", *boot.Volume.Profile.(*vpcv1.VolumeProfileIdentityByName).Name)
	assert.Equal(t, int64(6000), *boot.Volume.Iops)
	assert.Equal(t, int64(1000), *boot.Volume.Bandwidth)
	assert.Equal(t, "crn:v1:bluemix:public:kms:us-south:a/abc123", *boot.Volume.EncryptionKey.(*vpcv1.EncryptionKeyIdentityByCRN).CRN)
	assert.Equal(t, []string{"env:prod", "team:platform"}, boot.Volume.UserTags)

	// DeleteOnTermination = false
	assert.False(t, *boot.DeleteVolumeOnInstanceDelete)

	// DeviceName used as attachment Name
	assert.Equal(t, "custom-boot", *boot.Name)
}

func TestBuildVolumeAttachments_BootVolumeNoVolumeSpec(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: true,
					// VolumeSpec is nil: defaults should be applied
				},
			},
		},
	}

	boot, additional, err := provider.buildVolumeAttachments(nodeClass, "default-node", "us-south-1")
	assert.NoError(t, err)
	assert.NotNil(t, boot)
	assert.Nil(t, additional)
	assert.Equal(t, int64(100), *boot.Volume.Capacity)
	assert.Equal(t, "general-purpose", *boot.Volume.Profile.(*vpcv1.VolumeProfileIdentityByName).Name)
	assert.True(t, *boot.DeleteVolumeOnInstanceDelete)
}

func TestBuildVolumeAttachments_BootVolumeSpecWithoutCapacityOrProfile(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: true,
					VolumeSpec: &v1alpha1.VolumeSpec{
						// Capacity nil, Profile nil: both default
					},
				},
			},
		},
	}

	boot, _, err := provider.buildVolumeAttachments(nodeClass, "node", "us-south-1")
	assert.NoError(t, err)
	assert.Equal(t, int64(100), *boot.Volume.Capacity)
	assert.Equal(t, "general-purpose", *boot.Volume.Profile.(*vpcv1.VolumeProfileIdentityByName).Name)
}

func TestBuildVolumeAttachments_DataVolumeFullSpec(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: false,
					DeviceName: ptrString("data-disk-0"),
					VolumeSpec: &v1alpha1.VolumeSpec{
						Capacity:            ptrInt64(500),
						Profile:             ptrString("10iops-tier"),
						IOPS:                ptrInt64(10000),
						Bandwidth:           ptrInt64(2000),
						EncryptionKeyID:     ptrString("crn:v1:bluemix:public:kms:eu-de:a/key123"),
						DeleteOnTermination: ptrBool(false),
						Tags:                []string{"purpose:data"},
					},
				},
			},
		},
	}

	boot, additional, err := provider.buildVolumeAttachments(nodeClass, "my-node", "eu-de-1")
	assert.NoError(t, err)
	// No root volume in mappings -> default boot volume
	assert.NotNil(t, boot)
	assert.Equal(t, "my-node-boot", *boot.Volume.Name)
	assert.Equal(t, int64(100), *boot.Volume.Capacity)
	assert.True(t, *boot.DeleteVolumeOnInstanceDelete)

	assert.Len(t, additional, 1)
	vol := additional[0]
	assert.Equal(t, "data-disk-0", *vol.Name)
	assert.False(t, *vol.DeleteVolumeOnInstanceDelete)

	proto := vol.Volume.(*vpcv1.VolumeAttachmentPrototypeVolumeVolumePrototypeInstanceContextVolumePrototypeInstanceContextVolumeByCapacity)
	assert.Equal(t, int64(500), *proto.Capacity)
	assert.Equal(t, "10iops-tier", *proto.Profile.(*vpcv1.VolumeProfileIdentityByName).Name)
	assert.Equal(t, int64(10000), *proto.Iops)
	assert.Equal(t, int64(2000), *proto.Bandwidth)
	assert.Equal(t, "crn:v1:bluemix:public:kms:eu-de:a/key123", *proto.EncryptionKey.(*vpcv1.EncryptionKeyIdentityByCRN).CRN)
	assert.Equal(t, []string{"purpose:data"}, proto.UserTags)
}

func TestBuildVolumeAttachments_DataVolumeNoVolumeSpec_Skipped(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: false,
					DeviceName: ptrString("skip-me"),
					// VolumeSpec nil -> should be skipped
				},
			},
		},
	}

	boot, additional, err := provider.buildVolumeAttachments(nodeClass, "node", "us-south-1")
	assert.NoError(t, err)
	assert.NotNil(t, boot)
	assert.Empty(t, additional)
}

func TestBuildVolumeAttachments_DataVolumeDefaultsCapacityAndProfile(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: false,
					VolumeSpec: &v1alpha1.VolumeSpec{
						// no capacity, no profile: both default
					},
				},
			},
		},
	}

	_, additional, err := provider.buildVolumeAttachments(nodeClass, "node", "us-south-1")
	assert.NoError(t, err)
	assert.Len(t, additional, 1)

	proto := additional[0].Volume.(*vpcv1.VolumeAttachmentPrototypeVolumeVolumePrototypeInstanceContextVolumePrototypeInstanceContextVolumeByCapacity)
	assert.Equal(t, int64(100), *proto.Capacity)
	assert.Equal(t, "general-purpose", *proto.Profile.(*vpcv1.VolumeProfileIdentityByName).Name)
	// auto-generated name: node-data-0
	assert.Equal(t, "node-data-0", *additional[0].Name)
	// default delete on termination = true
	assert.True(t, *additional[0].DeleteVolumeOnInstanceDelete)
}

func TestBuildVolumeAttachments_MultipleDataVolumes(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: true,
					VolumeSpec: &v1alpha1.VolumeSpec{Capacity: ptrInt64(100)},
				},
				{
					RootVolume: false,
					VolumeSpec: &v1alpha1.VolumeSpec{Capacity: ptrInt64(200)},
				},
				{
					RootVolume: false,
					VolumeSpec: &v1alpha1.VolumeSpec{Capacity: ptrInt64(300)},
				},
				{
					RootVolume: false,
					// VolumeSpec nil, should be skipped
				},
			},
		},
	}

	boot, additional, err := provider.buildVolumeAttachments(nodeClass, "multi", "us-south-1")
	assert.NoError(t, err)
	assert.NotNil(t, boot)
	assert.Len(t, additional, 2)

	// First data volume: auto-generated name multi-data-0
	assert.Equal(t, "multi-data-0", *additional[0].Name)
	proto0 := additional[0].Volume.(*vpcv1.VolumeAttachmentPrototypeVolumeVolumePrototypeInstanceContextVolumePrototypeInstanceContextVolumeByCapacity)
	assert.Equal(t, int64(200), *proto0.Capacity)

	// Second data volume: auto-generated name multi-data-1
	assert.Equal(t, "multi-data-1", *additional[1].Name)
	proto1 := additional[1].Volume.(*vpcv1.VolumeAttachmentPrototypeVolumeVolumePrototypeInstanceContextVolumePrototypeInstanceContextVolumeByCapacity)
	assert.Equal(t, int64(300), *proto1.Capacity)
}

func TestBuildVolumeAttachments_NoRootVolumeInMappings(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: false,
					VolumeSpec: &v1alpha1.VolumeSpec{Capacity: ptrInt64(50)},
				},
			},
		},
	}

	boot, additional, err := provider.buildVolumeAttachments(nodeClass, "noboot", "us-south-1")
	assert.NoError(t, err)
	// Should get default boot volume
	assert.NotNil(t, boot)
	assert.Equal(t, "noboot-boot", *boot.Volume.Name)
	assert.Equal(t, int64(100), *boot.Volume.Capacity)
	assert.Equal(t, "general-purpose", *boot.Volume.Profile.(*vpcv1.VolumeProfileIdentityByName).Name)
	assert.True(t, *boot.DeleteVolumeOnInstanceDelete)
	assert.Len(t, additional, 1)
}

func TestBuildVolumeAttachments_BootDeleteOnTerminationDefault(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: true,
					VolumeSpec: &v1alpha1.VolumeSpec{
						Capacity: ptrInt64(100),
						// DeleteOnTermination not set: defaults to true
					},
				},
			},
		},
	}

	boot, _, err := provider.buildVolumeAttachments(nodeClass, "node", "us-south-1")
	assert.NoError(t, err)
	assert.True(t, *boot.DeleteVolumeOnInstanceDelete)
}

func TestBuildVolumeAttachments_BootWithoutDeviceName(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: true,
					// DeviceName nil: boot.Name should not be set
					VolumeSpec: &v1alpha1.VolumeSpec{Capacity: ptrInt64(100)},
				},
			},
		},
	}

	boot, _, err := provider.buildVolumeAttachments(nodeClass, "node", "us-south-1")
	assert.NoError(t, err)
	assert.Nil(t, boot.Name) // DeviceName was not provided
}

func TestBuildVolumeAttachments_DataVolumeWithDeviceName(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: false,
					DeviceName: ptrString("my-data-vol"),
					VolumeSpec: &v1alpha1.VolumeSpec{Capacity: ptrInt64(100)},
				},
			},
		},
	}

	_, additional, err := provider.buildVolumeAttachments(nodeClass, "node", "us-south-1")
	assert.NoError(t, err)
	assert.Len(t, additional, 1)
	assert.Equal(t, "my-data-vol", *additional[0].Name)
}

func TestBuildVolumeAttachments_DataVolumeWithoutDeviceName(t *testing.T) {
	provider := &VPCInstanceProvider{}
	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{
				{
					RootVolume: false,
					VolumeSpec: &v1alpha1.VolumeSpec{Capacity: ptrInt64(100)},
				},
			},
		},
	}

	_, additional, err := provider.buildVolumeAttachments(nodeClass, "inst", "us-south-1")
	assert.NoError(t, err)
	assert.Equal(t, "inst-data-0", *additional[0].Name)
}

// -- isPartialFailure additional coverage --

func TestIsPartialFailure_SecurityGroupNotFound(t *testing.T) {
	p := &VPCInstanceProvider{}
	assert.True(t, p.isPartialFailure(&ibm.IBMError{Code: "vpc_security_group_not_found", StatusCode: 404}))
}

func TestIsPartialFailure_BootVolumeCreationFailed(t *testing.T) {
	p := &VPCInstanceProvider{}
	assert.True(t, p.isPartialFailure(&ibm.IBMError{Code: "vpc_boot_volume_creation_failed", StatusCode: 500}))
}

func TestIsPartialFailure_5xxUnknownCode(t *testing.T) {
	p := &VPCInstanceProvider{}
	assert.True(t, p.isPartialFailure(&ibm.IBMError{Code: "some_random_error", StatusCode: 500}))
	assert.True(t, p.isPartialFailure(&ibm.IBMError{Code: "internal_error", StatusCode: 502}))
	assert.True(t, p.isPartialFailure(&ibm.IBMError{Code: "unknown", StatusCode: 599}))
}

func TestIsPartialFailure_4xxUnknownCode(t *testing.T) {
	p := &VPCInstanceProvider{}
	assert.False(t, p.isPartialFailure(&ibm.IBMError{Code: "some_random_error", StatusCode: 400}))
	assert.False(t, p.isPartialFailure(&ibm.IBMError{Code: "bad_request", StatusCode: 422}))
}

func TestIsPartialFailure_ZeroStatusUnknownCode(t *testing.T) {
	p := &VPCInstanceProvider{}
	assert.False(t, p.isPartialFailure(&ibm.IBMError{Code: "unknown_code", StatusCode: 0}))
}

func TestIsPartialFailure_BoundaryStatusCodes(t *testing.T) {
	p := &VPCInstanceProvider{}
	// 499 is not 5xx
	assert.False(t, p.isPartialFailure(&ibm.IBMError{Code: "x", StatusCode: 499}))
	// 500 is 5xx
	assert.True(t, p.isPartialFailure(&ibm.IBMError{Code: "x", StatusCode: 500}))
	// 600 is not 5xx
	assert.False(t, p.isPartialFailure(&ibm.IBMError{Code: "x", StatusCode: 600}))
}
