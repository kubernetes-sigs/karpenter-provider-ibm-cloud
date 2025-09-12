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

package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-openapi/strfmt"
	"github.com/samber/lo"
	"sigs.k8s.io/karpenter/pkg/utils/atomic"
)

// VPCBehavior controls the fake VPC API behavior for testing
type VPCBehavior struct {
	CreateInstanceBehavior MockedFunction[CreateInstanceInput, vpcv1.Instance]
	DeleteInstanceBehavior MockedFunction[DeleteInstanceInput, struct{}]
	GetInstanceBehavior    MockedFunction[GetInstanceInput, vpcv1.Instance]
	ListInstancesBehavior  MockedFunction[ListInstancesInput, vpcv1.InstanceCollection]

	GetSubnetBehavior   MockedFunction[GetSubnetInput, vpcv1.Subnet]
	ListSubnetsBehavior MockedFunction[ListSubnetsInput, vpcv1.SubnetCollection]

	GetImageBehavior   MockedFunction[GetImageInput, vpcv1.Image]
	ListImagesBehavior MockedFunction[ListImagesInput, vpcv1.ImageCollection]

	GetVPCBehavior   MockedFunction[GetVPCInput, vpcv1.VPC]
	ListVPCsBehavior MockedFunction[ListVPCsInput, vpcv1.VPCCollection]

	GetInstanceProfileBehavior   MockedFunction[GetInstanceProfileInput, vpcv1.InstanceProfile]
	ListInstanceProfilesBehavior MockedFunction[ListInstanceProfilesInput, vpcv1.InstanceProfileCollection]

	Instances        atomic.Slice[*vpcv1.Instance]
	Subnets          atomic.Slice[*vpcv1.Subnet]
	Images           atomic.Slice[*vpcv1.Image]
	VPCs             atomic.Slice[*vpcv1.VPC]
	InstanceProfiles atomic.Slice[*vpcv1.InstanceProfile]
	NextError        AtomicError
}

// Input types for VPC operations
type CreateInstanceInput struct {
	InstancePrototype vpcv1.InstancePrototypeIntf
}

type DeleteInstanceInput struct {
	ID string
}

type GetInstanceInput struct {
	ID string
}

type ListInstancesInput struct {
	VPC  string
	Name string
}

type GetSubnetInput struct {
	ID string
}

type ListSubnetsInput struct {
	VPC string
}

type GetImageInput struct {
	ID string
}

type ListImagesInput struct {
	Name       string
	Visibility string
}

type GetVPCInput struct {
	ID string
}

type ListVPCsInput struct {
	Name string
}

type GetInstanceProfileInput struct {
	Name string
}

type ListInstanceProfilesInput struct{}

// VPCAPI implements a fake VPC API for testing
type VPCAPI struct {
	*VPCBehavior
	mu sync.RWMutex
}

// NewVPCAPI creates a new fake VPC API
func NewVPCAPI() *VPCAPI {
	return &VPCAPI{
		VPCBehavior: &VPCBehavior{},
	}
}

// CreateInstance creates a VPC instance
func (f *VPCAPI) CreateInstance(ctx context.Context, prototype vpcv1.InstancePrototypeIntf) (*vpcv1.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.CreateInstanceBehavior.CalledWithInput.Add(CreateInstanceInput{
		InstancePrototype: prototype,
	})

	// Return mocked output if set
	if output := f.CreateInstanceBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: create a new instance
	instanceID := fmt.Sprintf("instance-%d", f.Instances.Len()+1)
	instanceName := fmt.Sprintf("test-instance-%d", f.Instances.Len()+1)
	zoneName := "us-south-1"
	vpcID := "vpc-test"
	subnetID := "subnet-test"
	imageID := "image-test"
	profileName := "bx2-2x8"
	status := "running"

	instance := &vpcv1.Instance{
		ID:      &instanceID,
		Name:    &instanceName,
		Zone:    &vpcv1.ZoneReference{Name: &zoneName},
		VPC:     &vpcv1.VPCReference{ID: &vpcID},
		Image:   &vpcv1.ImageReference{ID: &imageID},
		Profile: &vpcv1.InstanceProfileReference{Name: &profileName},
		Status:  &status,
		PrimaryNetworkInterface: &vpcv1.NetworkInterfaceInstanceContextReference{
			ID:     lo.ToPtr("eni-" + instanceID),
			Subnet: &vpcv1.SubnetReference{ID: &subnetID},
		},
		CreatedAt: lo.ToPtr(strfmt.DateTime(time.Now())),
	}

	f.Instances.Add(instance)
	return instance, nil
}

// DeleteInstance deletes a VPC instance
func (f *VPCAPI) DeleteInstance(ctx context.Context, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.NextError.Get(); err != nil {
		return err
	}

	// Track the call
	f.DeleteInstanceBehavior.CalledWithInput.Add(DeleteInstanceInput{ID: instanceID})

	// Default behavior: remove the instance
	var instancesToKeep []*vpcv1.Instance
	f.Instances.Range(func(inst *vpcv1.Instance) bool {
		if inst.ID == nil || *inst.ID != instanceID {
			instancesToKeep = append(instancesToKeep, inst)
		}
		return true
	})

	f.Instances.Reset()
	for _, inst := range instancesToKeep {
		f.Instances.Add(inst)
	}
	return nil
}

// GetInstance gets a VPC instance
func (f *VPCAPI) GetInstance(ctx context.Context, instanceID string) (*vpcv1.Instance, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.GetInstanceBehavior.CalledWithInput.Add(GetInstanceInput{ID: instanceID})

	// Return mocked output if set
	if output := f.GetInstanceBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: find the instance
	var foundInstance *vpcv1.Instance
	f.Instances.Range(func(inst *vpcv1.Instance) bool {
		if inst.ID != nil && *inst.ID == instanceID {
			foundInstance = inst
			return false // stop iteration
		}
		return true
	})

	if foundInstance != nil {
		return foundInstance, nil
	}
	return nil, fmt.Errorf("instance %s not found", instanceID)
}

// ListInstances lists VPC instances
func (f *VPCAPI) ListInstances(ctx context.Context, vpcID, name string) (*vpcv1.InstanceCollection, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.ListInstancesBehavior.CalledWithInput.Add(ListInstancesInput{
		VPC:  vpcID,
		Name: name,
	})

	// Return mocked output if set
	if output := f.ListInstancesBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: filter instances
	var filteredInstances []*vpcv1.Instance

	f.Instances.Range(func(inst *vpcv1.Instance) bool {
		// Filter by VPC if specified
		if vpcID != "" && (inst.VPC == nil || inst.VPC.ID == nil || *inst.VPC.ID != vpcID) {
			return true // continue
		}
		// Filter by name if specified
		if name != "" && (inst.Name == nil || *inst.Name != name) {
			return true // continue
		}
		filteredInstances = append(filteredInstances, inst)
		return true
	})

	// Convert slice of pointers to slice of values
	instanceValues := make([]vpcv1.Instance, len(filteredInstances))
	for i, inst := range filteredInstances {
		instanceValues[i] = *inst
	}

	return &vpcv1.InstanceCollection{
		Instances: instanceValues,
	}, nil
}

// GetSubnet gets a VPC subnet
func (f *VPCAPI) GetSubnet(ctx context.Context, subnetID string) (*vpcv1.Subnet, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.GetSubnetBehavior.CalledWithInput.Add(GetSubnetInput{ID: subnetID})

	// Return mocked output if set
	if output := f.GetSubnetBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: find the subnet
	var foundSubnet *vpcv1.Subnet
	f.Subnets.Range(func(subnet *vpcv1.Subnet) bool {
		if subnet.ID != nil && *subnet.ID == subnetID {
			foundSubnet = subnet
			return false // stop iteration
		}
		return true
	})

	if foundSubnet != nil {
		return foundSubnet, nil
	}
	return nil, fmt.Errorf("subnet %s not found", subnetID)
}

// ListSubnets lists VPC subnets
func (f *VPCAPI) ListSubnets(ctx context.Context, vpcID string) (*vpcv1.SubnetCollection, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.ListSubnetsBehavior.CalledWithInput.Add(ListSubnetsInput{VPC: vpcID})

	// Return mocked output if set
	if output := f.ListSubnetsBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: filter subnets by VPC
	var filteredSubnets []*vpcv1.Subnet

	f.Subnets.Range(func(subnet *vpcv1.Subnet) bool {
		if vpcID == "" || (subnet.VPC != nil && subnet.VPC.ID != nil && *subnet.VPC.ID == vpcID) {
			filteredSubnets = append(filteredSubnets, subnet)
		}
		return true
	})

	// Convert slice of pointers to slice of values
	subnetValues := make([]vpcv1.Subnet, len(filteredSubnets))
	for i, subnet := range filteredSubnets {
		subnetValues[i] = *subnet
	}

	return &vpcv1.SubnetCollection{
		Subnets: subnetValues,
	}, nil
}

// GetImage gets a VPC image
func (f *VPCAPI) GetImage(ctx context.Context, imageID string) (*vpcv1.Image, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.GetImageBehavior.CalledWithInput.Add(GetImageInput{ID: imageID})

	// Return mocked output if set
	if output := f.GetImageBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: find the image
	var foundImage *vpcv1.Image
	f.Images.Range(func(img *vpcv1.Image) bool {
		if img.ID != nil && *img.ID == imageID {
			foundImage = img
			return false // stop iteration
		}
		return true
	})

	if foundImage != nil {
		return foundImage, nil
	}
	return nil, fmt.Errorf("image %s not found", imageID)
}

// ListImages lists VPC images
func (f *VPCAPI) ListImages(ctx context.Context, name, visibility string) (*vpcv1.ImageCollection, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.ListImagesBehavior.CalledWithInput.Add(ListImagesInput{
		Name:       name,
		Visibility: visibility,
	})

	// Return mocked output if set
	if output := f.ListImagesBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: filter images
	var filteredImages []*vpcv1.Image

	f.Images.Range(func(img *vpcv1.Image) bool {
		// Filter by name if specified
		if name != "" && (img.Name == nil || *img.Name != name) {
			return true // continue
		}
		// Filter by visibility if specified
		if visibility != "" && (img.Visibility == nil || *img.Visibility != visibility) {
			return true // continue
		}
		filteredImages = append(filteredImages, img)
		return true
	})

	// Convert slice of pointers to slice of values
	imageValues := make([]vpcv1.Image, len(filteredImages))
	for i, img := range filteredImages {
		imageValues[i] = *img
	}

	return &vpcv1.ImageCollection{
		Images: imageValues,
	}, nil
}

// GetInstanceProfile gets an instance profile
func (f *VPCAPI) GetInstanceProfile(ctx context.Context, name string) (*vpcv1.InstanceProfile, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.GetInstanceProfileBehavior.CalledWithInput.Add(GetInstanceProfileInput{Name: name})

	// Return mocked output if set
	if output := f.GetInstanceProfileBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: find the profile
	var foundProfile *vpcv1.InstanceProfile
	f.InstanceProfiles.Range(func(profile *vpcv1.InstanceProfile) bool {
		if profile.Name != nil && *profile.Name == name {
			foundProfile = profile
			return false // stop iteration
		}
		return true
	})

	if foundProfile != nil {
		return foundProfile, nil
	}
	return nil, fmt.Errorf("instance profile %s not found", name)
}

// Reset resets the fake API state
func (f *VPCAPI) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.Instances.Reset()
	f.Subnets.Reset()
	f.Images.Reset()
	f.VPCs.Reset()
	f.InstanceProfiles.Reset()
	f.NextError.Store(nil)

	// Reset all behavior mocks
	f.CreateInstanceBehavior.Reset()
	f.DeleteInstanceBehavior.Reset()
	f.GetInstanceBehavior.Reset()
	f.ListInstancesBehavior.Reset()
	f.GetSubnetBehavior.Reset()
	f.ListSubnetsBehavior.Reset()
	f.GetImageBehavior.Reset()
	f.ListImagesBehavior.Reset()
	f.GetVPCBehavior.Reset()
	f.ListVPCsBehavior.Reset()
	f.GetInstanceProfileBehavior.Reset()
	f.ListInstanceProfilesBehavior.Reset()
}
