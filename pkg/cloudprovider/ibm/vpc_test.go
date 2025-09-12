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
package ibm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
)

// =================
// Mock Implementations
// =================

type mockVPCClient struct {
	err                        error
	createInstanceResponse     *vpcv1.Instance
	getInstanceResponse        *vpcv1.Instance
	listInstancesResponse      *vpcv1.InstanceCollection
	listSubnetsResponse        *vpcv1.SubnetCollection
	getSubnetResponse          *vpcv1.Subnet
	getInstanceProfileResponse *vpcv1.InstanceProfile
}

func (m *mockVPCClient) CreateInstanceWithContext(_ context.Context, _ *vpcv1.CreateInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return m.createInstanceResponse, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) DeleteInstanceWithContext(_ context.Context, _ *vpcv1.DeleteInstanceOptions) (*core.DetailedResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) GetInstanceWithContext(_ context.Context, _ *vpcv1.GetInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return m.getInstanceResponse, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) ListInstancesWithContext(_ context.Context, _ *vpcv1.ListInstancesOptions) (*vpcv1.InstanceCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return m.listInstancesResponse, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) UpdateInstanceWithContext(_ context.Context, _ *vpcv1.UpdateInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.Instance{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) ListSubnetsWithContext(_ context.Context, _ *vpcv1.ListSubnetsOptions) (*vpcv1.SubnetCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return m.listSubnetsResponse, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) GetSubnetWithContext(_ context.Context, _ *vpcv1.GetSubnetOptions) (*vpcv1.Subnet, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return m.getSubnetResponse, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) GetVPCWithContext(_ context.Context, _ *vpcv1.GetVPCOptions) (*vpcv1.VPC, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	vpcID := "test-vpc"
	vpcName := "test-vpc-name"
	return &vpcv1.VPC{
		ID:   &vpcID,
		Name: &vpcName,
	}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) GetImageWithContext(_ context.Context, _ *vpcv1.GetImageOptions) (*vpcv1.Image, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	imageID := "test-image"
	imageName := "test-image-name"
	return &vpcv1.Image{
		ID:   &imageID,
		Name: &imageName,
	}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) ListInstanceProfilesWithContext(_ context.Context, _ *vpcv1.ListInstanceProfilesOptions) (*vpcv1.InstanceProfileCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.InstanceProfileCollection{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) GetInstanceProfileWithContext(_ context.Context, _ *vpcv1.GetInstanceProfileOptions) (*vpcv1.InstanceProfile, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return m.getInstanceProfileResponse, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) ListImagesWithContext(_ context.Context, _ *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.ImageCollection{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) ListSecurityGroupsWithContext(_ context.Context, _ *vpcv1.ListSecurityGroupsOptions) (*vpcv1.SecurityGroupCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.SecurityGroupCollection{}, &core.DetailedResponse{}, nil
}

// Load Balancer methods
func (m *mockVPCClient) GetLoadBalancerWithContext(_ context.Context, _ *vpcv1.GetLoadBalancerOptions) (*vpcv1.LoadBalancer, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.LoadBalancer{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) ListLoadBalancerPoolsWithContext(_ context.Context, _ *vpcv1.ListLoadBalancerPoolsOptions) (*vpcv1.LoadBalancerPoolCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.LoadBalancerPoolCollection{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) GetLoadBalancerPoolWithContext(_ context.Context, _ *vpcv1.GetLoadBalancerPoolOptions) (*vpcv1.LoadBalancerPool, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.LoadBalancerPool{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) CreateLoadBalancerPoolMemberWithContext(_ context.Context, _ *vpcv1.CreateLoadBalancerPoolMemberOptions) (*vpcv1.LoadBalancerPoolMember, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.LoadBalancerPoolMember{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) DeleteLoadBalancerPoolMemberWithContext(_ context.Context, _ *vpcv1.DeleteLoadBalancerPoolMemberOptions) (*core.DetailedResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) GetLoadBalancerPoolMemberWithContext(_ context.Context, _ *vpcv1.GetLoadBalancerPoolMemberOptions) (*vpcv1.LoadBalancerPoolMember, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.LoadBalancerPoolMember{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) ListLoadBalancerPoolMembersWithContext(_ context.Context, _ *vpcv1.ListLoadBalancerPoolMembersOptions) (*vpcv1.LoadBalancerPoolMemberCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.LoadBalancerPoolMemberCollection{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) UpdateLoadBalancerPoolMemberWithContext(_ context.Context, _ *vpcv1.UpdateLoadBalancerPoolMemberOptions) (*vpcv1.LoadBalancerPoolMember, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.LoadBalancerPoolMember{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) UpdateLoadBalancerPoolWithContext(_ context.Context, _ *vpcv1.UpdateLoadBalancerPoolOptions) (*vpcv1.LoadBalancerPool, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.LoadBalancerPool{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) ListRegionZonesWithContext(_ context.Context, _ *vpcv1.ListRegionZonesOptions) (*vpcv1.ZoneCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	// Return default zones for testing
	zone1 := "us-south-1"
	zone2 := "us-south-2"
	zone3 := "us-south-3"
	return &vpcv1.ZoneCollection{
		Zones: []vpcv1.Zone{
			{Name: &zone1},
			{Name: &zone2},
			{Name: &zone3},
		},
	}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) ListRegions(_ *vpcv1.ListRegionsOptions) (*vpcv1.RegionCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	// Return default regions for testing
	region1 := "us-south"
	region2 := "us-east"
	region3 := "eu-de"
	status := "available"
	return &vpcv1.RegionCollection{
		Regions: []vpcv1.Region{
			{Name: &region1, Status: &status},
			{Name: &region2, Status: &status},
			{Name: &region3, Status: &status},
		},
	}, &core.DetailedResponse{}, nil
}

// Volume methods
func (m *mockVPCClient) ListVolumesWithContext(_ context.Context, _ *vpcv1.ListVolumesOptions) (*vpcv1.VolumeCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.VolumeCollection{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) DeleteVolumeWithContext(_ context.Context, _ *vpcv1.DeleteVolumeOptions) (*core.DetailedResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &core.DetailedResponse{}, nil
}

// Virtual Network Interface methods
func (m *mockVPCClient) ListVirtualNetworkInterfacesWithContext(_ context.Context, _ *vpcv1.ListVirtualNetworkInterfacesOptions) (*vpcv1.VirtualNetworkInterfaceCollection, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.VirtualNetworkInterfaceCollection{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) DeleteVirtualNetworkInterfacesWithContext(_ context.Context, _ *vpcv1.DeleteVirtualNetworkInterfacesOptions) (*vpcv1.VirtualNetworkInterface, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.VirtualNetworkInterface{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) GetSecurityGroupWithContext(_ context.Context, _ *vpcv1.GetSecurityGroupOptions) (*vpcv1.SecurityGroup, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.SecurityGroup{}, &core.DetailedResponse{}, nil
}

func (m *mockVPCClient) GetKeyWithContext(_ context.Context, _ *vpcv1.GetKeyOptions) (*vpcv1.Key, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return &vpcv1.Key{}, &core.DetailedResponse{}, nil
}

func TestNewVPCClient(t *testing.T) {
	baseURL := "https://test.vpc.url"
	authType := "iam"
	apiKey := "test-key"
	region := "us-south"

	client, err := NewVPCClient(baseURL, authType, apiKey, region, "")
	if err != nil {
		t.Fatalf("failed to create VPC client: %v", err)
	}

	if client.baseURL != baseURL {
		t.Errorf("expected baseURL %s, got %s", baseURL, client.baseURL)
	}
	if client.authType != authType {
		t.Errorf("expected authType %s, got %s", authType, client.authType)
	}
	if client.apiKey != apiKey {
		t.Errorf("expected apiKey %s, got %s", apiKey, client.apiKey)
	}
	if client.region != region {
		t.Errorf("expected region %s, got %s", region, client.region)
	}
	if client.client == nil {
		t.Error("expected client to be initialized")
	}
}

// =================
// Instance Tests
// =================

func TestCreateInstance(t *testing.T) {
	ctx := context.Background()
	instanceID := "test-instance"
	instanceName := "test-instance-name"

	tests := []struct {
		name         string
		mockVPC      vpcClientInterface
		wantErr      bool
		wantInstance *vpcv1.Instance
	}{
		{
			name: "successful instance creation",
			mockVPC: &mockVPCClient{
				createInstanceResponse: &vpcv1.Instance{
					ID:   &instanceID,
					Name: &instanceName,
				},
			},
			wantErr: false,
			wantInstance: &vpcv1.Instance{
				ID:   &instanceID,
				Name: &instanceName,
			},
		},
		{
			name: "API error",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			wantErr:      true,
			wantInstance: nil,
		},
		{
			name:         "uninitialized client",
			mockVPC:      nil,
			wantErr:      true,
			wantInstance: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			prototype := &vpcv1.InstancePrototypeInstanceByImage{
				Name: &instanceName,
				Image: &vpcv1.ImageIdentity{
					ID: &[]string{"test-image-id"}[0],
				},
				Zone: &vpcv1.ZoneIdentity{
					Name: &[]string{"us-south-1"}[0],
				},
				Profile: &vpcv1.InstanceProfileIdentity{
					Name: &[]string{"bx2-2x8"}[0],
				},
				VPC: &vpcv1.VPCIdentity{
					ID: &[]string{"test-vpc-id"}[0],
				},
			}

			instance, err := client.CreateInstance(ctx, prototype)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				if instance != nil {
					t.Error("expected nil instance")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if instance == nil {
					t.Error("expected instance but got nil")
				} else if *instance.ID != *tt.wantInstance.ID {
					t.Errorf("got instance ID %s, want %s", *instance.ID, *tt.wantInstance.ID)
				}
			}
		})
	}
}

func TestDeleteInstance(t *testing.T) {
	ctx := context.Background()
	instanceID := "test-instance"

	tests := []struct {
		name         string
		mockVPC      vpcClientInterface
		instanceID   string
		wantErr      bool
		uninitClient bool
	}{
		{
			name:       "successful instance deletion",
			mockVPC:    &mockVPCClient{},
			instanceID: instanceID,
			wantErr:    false,
		},
		{
			name: "API error",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			instanceID: instanceID,
			wantErr:    true,
		},
		{
			name:         "uninitialized client",
			mockVPC:      nil,
			instanceID:   instanceID,
			wantErr:      true,
			uninitClient: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if !tt.uninitClient && tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			err := client.DeleteInstance(ctx, tt.instanceID)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestGetInstance(t *testing.T) {
	ctx := context.Background()
	instanceID := "test-instance"
	instanceName := "test-instance-name"

	tests := []struct {
		name         string
		mockVPC      vpcClientInterface
		instanceID   string
		wantErr      bool
		wantInstance *vpcv1.Instance
	}{
		{
			name: "successful instance retrieval",
			mockVPC: &mockVPCClient{
				getInstanceResponse: &vpcv1.Instance{
					ID:   &instanceID,
					Name: &instanceName,
				},
			},
			instanceID: instanceID,
			wantErr:    false,
			wantInstance: &vpcv1.Instance{
				ID:   &instanceID,
				Name: &instanceName,
			},
		},
		{
			name: "API error",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			instanceID:   instanceID,
			wantErr:      true,
			wantInstance: nil,
		},
		{
			name:         "uninitialized client",
			mockVPC:      nil,
			instanceID:   instanceID,
			wantErr:      true,
			wantInstance: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			instance, err := client.GetInstance(ctx, tt.instanceID)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				if instance != nil {
					t.Error("expected nil instance")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if instance == nil {
					t.Error("expected instance but got nil")
				} else if *instance.ID != *tt.wantInstance.ID {
					t.Errorf("got instance ID %s, want %s", *instance.ID, *tt.wantInstance.ID)
				}
			}
		})
	}
}

func TestListInstances(t *testing.T) {
	ctx := context.Background()
	instanceID := "test-instance"
	instanceName := "test-instance-name"

	mockInstances := []vpcv1.Instance{
		{
			ID:   &instanceID,
			Name: &instanceName,
		},
	}

	tests := []struct {
		name          string
		mockVPC       vpcClientInterface
		wantErr       bool
		wantInstances int
		uninitClient  bool
	}{
		{
			name: "successful instances listing",
			mockVPC: &mockVPCClient{
				listInstancesResponse: &vpcv1.InstanceCollection{
					Instances: mockInstances,
				},
			},
			wantInstances: 1,
		},
		{
			name: "API error",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			wantErr: true,
		},
		{
			name:         "uninitialized client",
			uninitClient: true,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if !tt.uninitClient && tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			instances, err := client.ListInstances(ctx)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				if instances != nil {
					t.Error("expected nil instances")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if instances == nil {
					t.Error("expected instances but got nil")
				} else if len(instances) != tt.wantInstances {
					t.Errorf("got %d instances, want %d", len(instances), tt.wantInstances)
				}
			}
		})
	}
}

func TestUpdateInstanceTags(t *testing.T) {
	ctx := context.Background()
	instanceID := "test-instance"
	tags := map[string]string{
		"env":  "test",
		"team": "karpenter",
	}

	tests := []struct {
		name    string
		mockVPC vpcClientInterface
		id      string
		tags    map[string]string
		wantErr bool
	}{
		{
			name:    "successful tags update",
			mockVPC: &mockVPCClient{},
			id:      instanceID,
			tags:    tags,
			wantErr: false,
		},
		{
			name: "API error",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			id:      instanceID,
			tags:    tags,
			wantErr: true,
		},
		{
			name:    "uninitialized client",
			mockVPC: nil,
			id:      instanceID,
			tags:    tags,
			wantErr: true,
		},
		{
			name:    "empty tags",
			mockVPC: &mockVPCClient{},
			id:      instanceID,
			tags:    map[string]string{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			err := client.UpdateInstanceTags(ctx, tt.id, tt.tags)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// =================
// Subnet Tests
// =================

func TestListSubnets(t *testing.T) {
	ctx := context.Background()
	vpcID := "test-vpc"
	subnetID := "test-subnet"
	subnetName := "test-subnet-name"
	zoneName := "us-south-1"

	mockSubnets := []vpcv1.Subnet{
		{
			ID:   &subnetID,
			Name: &subnetName,
			Zone: &vpcv1.ZoneReference{
				Name: &zoneName,
			},
		},
	}

	tests := []struct {
		name         string
		mockVPC      vpcClientInterface
		vpcID        string
		wantErr      bool
		wantSubnets  int
		uninitClient bool
	}{
		{
			name: "successful subnet listing",
			mockVPC: &mockVPCClient{
				listSubnetsResponse: &vpcv1.SubnetCollection{
					Subnets: mockSubnets,
				},
			},
			vpcID:       vpcID,
			wantSubnets: 1,
		},
		{
			name: "API error",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			vpcID:   vpcID,
			wantErr: true,
		},
		{
			name:         "uninitialized client",
			uninitClient: true,
			vpcID:        vpcID,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if !tt.uninitClient && tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			subnets, err := client.ListSubnets(ctx, tt.vpcID)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				if subnets != nil {
					t.Error("expected nil subnets")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if subnets == nil {
					t.Error("expected subnets but got nil")
				} else if len(subnets.Subnets) != tt.wantSubnets {
					t.Errorf("got %d subnets, want %d", len(subnets.Subnets), tt.wantSubnets)
				}
			}
		})
	}
}

func TestGetSubnet(t *testing.T) {
	ctx := context.Background()
	subnetID := "test-subnet"
	subnetName := "test-subnet-name"
	zoneName := "us-south-1"

	tests := []struct {
		name       string
		mockVPC    vpcClientInterface
		subnetID   string
		wantErr    bool
		wantSubnet *vpcv1.Subnet
	}{
		{
			name: "successful subnet retrieval",
			mockVPC: &mockVPCClient{
				getSubnetResponse: &vpcv1.Subnet{
					ID:   &subnetID,
					Name: &subnetName,
					Zone: &vpcv1.ZoneReference{
						Name: &zoneName,
					},
				},
			},
			subnetID: subnetID,
			wantErr:  false,
			wantSubnet: &vpcv1.Subnet{
				ID:   &subnetID,
				Name: &subnetName,
				Zone: &vpcv1.ZoneReference{
					Name: &zoneName,
				},
			},
		},
		{
			name: "API error",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			subnetID:   subnetID,
			wantErr:    true,
			wantSubnet: nil,
		},
		{
			name:       "uninitialized client",
			mockVPC:    nil,
			subnetID:   subnetID,
			wantErr:    true,
			wantSubnet: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			subnet, err := client.GetSubnet(ctx, tt.subnetID)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				if subnet != nil {
					t.Error("expected nil subnet")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if subnet == nil || tt.wantSubnet == nil {
					if subnet != tt.wantSubnet {
						t.Errorf("got subnet %v, want %v", subnet, tt.wantSubnet)
					}
				} else if *subnet.ID != *tt.wantSubnet.ID || *subnet.Name != *tt.wantSubnet.Name {
					t.Errorf("got subnet ID=%s Name=%s, want ID=%s Name=%s",
						*subnet.ID, *subnet.Name, *tt.wantSubnet.ID, *tt.wantSubnet.Name)
				}
			}
		})
	}
}

// =================
// VPC Tests
// =================

func TestGetVPC(t *testing.T) {
	ctx := context.Background()
	vpcID := "test-vpc-id"

	tests := []struct {
		name    string
		mockVPC vpcClientInterface
		vpcID   string
		wantErr bool
		wantVPC *vpcv1.VPC
	}{
		{
			name:    "successful VPC retrieval",
			mockVPC: &mockVPCClient{
				// Mock will return VPC from GetVPCWithContext
			},
			vpcID:   vpcID,
			wantErr: false,
			wantVPC: &vpcv1.VPC{
				ID:   &[]string{"test-vpc"}[0],
				Name: &[]string{"test-vpc-name"}[0],
			},
		},
		{
			name: "API error",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			vpcID:   vpcID,
			wantErr: true,
			wantVPC: nil,
		},
		{
			name:    "uninitialized client",
			mockVPC: nil,
			vpcID:   vpcID,
			wantErr: true,
			wantVPC: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			vpc, err := client.GetVPC(ctx, tt.vpcID, "")

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if vpc == nil {
					t.Error("expected VPC but got nil")
				} else if *vpc.ID != *tt.wantVPC.ID {
					t.Errorf("got VPC ID %s, want %s", *vpc.ID, *tt.wantVPC.ID)
				}
			}
		})
	}
}

// =================
// Image Tests
// =================

func TestGetImage(t *testing.T) {
	ctx := context.Background()
	imageID := "test-image-id"

	tests := []struct {
		name      string
		mockVPC   vpcClientInterface
		imageID   string
		wantErr   bool
		wantImage *vpcv1.Image
	}{
		{
			name:    "successful image retrieval",
			mockVPC: &mockVPCClient{
				// Mock will return Image from GetImageWithContext
			},
			imageID: imageID,
			wantErr: false,
			wantImage: &vpcv1.Image{
				ID:   &[]string{"test-image"}[0],
				Name: &[]string{"test-image-name"}[0],
			},
		},
		{
			name: "API error",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			imageID:   imageID,
			wantErr:   true,
			wantImage: nil,
		},
		{
			name:      "uninitialized client",
			mockVPC:   nil,
			imageID:   imageID,
			wantErr:   true,
			wantImage: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			image, err := client.GetImage(ctx, tt.imageID)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if image == nil {
					t.Error("expected image but got nil")
				} else if *image.ID != *tt.wantImage.ID {
					t.Errorf("got image ID %s, want %s", *image.ID, *tt.wantImage.ID)
				}
			}
		})
	}
}

// =================
// Instance Profile Tests
// =================

func TestListInstanceProfiles(t *testing.T) {
	tests := []struct {
		name         string
		mockVPC      vpcClientInterface
		options      *vpcv1.ListInstanceProfilesOptions
		wantErr      bool
		wantProfiles int
	}{
		{
			name:    "successful profile listing",
			mockVPC: &mockVPCClient{
				// Mock will return empty collection from ListInstanceProfilesWithContext
			},
			options:      &vpcv1.ListInstanceProfilesOptions{},
			wantErr:      false,
			wantProfiles: 0,
		},
		{
			name: "API error",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			options: &vpcv1.ListInstanceProfilesOptions{},
			wantErr: true,
		},
		{
			name:    "uninitialized client",
			mockVPC: nil,
			options: &vpcv1.ListInstanceProfilesOptions{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			profiles, _, err := client.ListInstanceProfiles(tt.options)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if profiles == nil {
					t.Error("expected profiles but got nil")
				}
			}
		})
	}
}

func TestGetInstanceProfile(t *testing.T) {
	profileName := "bx2-2x8"

	tests := []struct {
		name        string
		mockVPC     vpcClientInterface
		profileName string
		wantErr     bool
	}{
		{
			name: "successful instance profile retrieval",
			mockVPC: &mockVPCClient{
				getInstanceProfileResponse: &vpcv1.InstanceProfile{
					Name: &profileName,
					VcpuArchitecture: &vpcv1.InstanceProfileVcpuArchitecture{
						Type:  stringPtr("fixed"),
						Value: stringPtr("amd64"),
					},
				},
			},
			profileName: profileName,
			wantErr:     false,
		},
		{
			name: "API error response",
			mockVPC: &mockVPCClient{
				err: fmt.Errorf("API error"),
			},
			profileName: profileName,
			wantErr:     true,
		},
		{
			name:        "uninitialized client",
			mockVPC:     nil,
			profileName: profileName,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var client *VPCClient
			if tt.mockVPC != nil {
				client = &VPCClient{
					client: tt.mockVPC,
				}
			} else {
				client = &VPCClient{}
			}

			profile, err := client.GetInstanceProfile(context.Background(), tt.profileName)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if profile == nil {
					t.Error("expected profile but got nil")
				}
				if profile != nil && profile.Name != nil && *profile.Name != tt.profileName {
					t.Errorf("expected profile name %s, got %s", tt.profileName, *profile.Name)
				}
				if profile != nil && profile.VcpuArchitecture != nil && profile.VcpuArchitecture.Value != nil {
					if *profile.VcpuArchitecture.Value != "amd64" {
						t.Errorf("expected architecture amd64, got %s", *profile.VcpuArchitecture.Value)
					}
				}
			}
		})
	}
}

// =================
// Error Handling Tests
// =================

func TestVPCClientErrorHandling(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		setupClient func() *VPCClient
		operation   func(*VPCClient) error
		wantErr     bool
		errContains string
	}{
		{
			name: "uninitialized client - create instance",
			setupClient: func() *VPCClient {
				return &VPCClient{}
			},
			operation: func(c *VPCClient) error {
				_, err := c.CreateInstance(ctx, &vpcv1.InstancePrototypeInstanceByImage{})
				return err
			},
			wantErr:     true,
			errContains: "VPC client not initialized",
		},
		{
			name: "uninitialized client - list subnets",
			setupClient: func() *VPCClient {
				return &VPCClient{}
			},
			operation: func(c *VPCClient) error {
				_, err := c.ListSubnets(ctx, "test-vpc")
				return err
			},
			wantErr:     true,
			errContains: "VPC client not initialized",
		},
		{
			name: "timeout scenario",
			setupClient: func() *VPCClient {
				return &VPCClient{
					client: &mockVPCClient{
						err: fmt.Errorf("context deadline exceeded"),
					},
				}
			},
			operation: func(c *VPCClient) error {
				timeoutCtx, cancel := context.WithTimeout(ctx, 1*time.Nanosecond)
				defer cancel()
				_, err := c.GetInstance(timeoutCtx, "test-id")
				return err
			},
			wantErr:     true,
			errContains: "context deadline exceeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := tt.setupClient()
			err := tt.operation(client)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error to contain %q, got %q", tt.errContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
