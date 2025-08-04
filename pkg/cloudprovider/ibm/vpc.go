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

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
)

// vpcClientInterface defines the interface for the VPC client
type vpcClientInterface interface {
	CreateInstanceWithContext(context.Context, *vpcv1.CreateInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error)
	DeleteInstanceWithContext(context.Context, *vpcv1.DeleteInstanceOptions) (*core.DetailedResponse, error)
	GetInstanceWithContext(context.Context, *vpcv1.GetInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error)
	ListInstancesWithContext(context.Context, *vpcv1.ListInstancesOptions) (*vpcv1.InstanceCollection, *core.DetailedResponse, error)
	UpdateInstanceWithContext(context.Context, *vpcv1.UpdateInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error)
	ListSubnetsWithContext(context.Context, *vpcv1.ListSubnetsOptions) (*vpcv1.SubnetCollection, *core.DetailedResponse, error)
	GetSubnetWithContext(context.Context, *vpcv1.GetSubnetOptions) (*vpcv1.Subnet, *core.DetailedResponse, error)
	GetVPCWithContext(context.Context, *vpcv1.GetVPCOptions) (*vpcv1.VPC, *core.DetailedResponse, error)
	GetImageWithContext(context.Context, *vpcv1.GetImageOptions) (*vpcv1.Image, *core.DetailedResponse, error)
	ListImagesWithContext(context.Context, *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, *core.DetailedResponse, error)
	ListInstanceProfilesWithContext(context.Context, *vpcv1.ListInstanceProfilesOptions) (*vpcv1.InstanceProfileCollection, *core.DetailedResponse, error)
	ListSecurityGroupsWithContext(context.Context, *vpcv1.ListSecurityGroupsOptions) (*vpcv1.SecurityGroupCollection, *core.DetailedResponse, error)
	// Volume methods
	ListVolumesWithContext(context.Context, *vpcv1.ListVolumesOptions) (*vpcv1.VolumeCollection, *core.DetailedResponse, error)
	DeleteVolumeWithContext(context.Context, *vpcv1.DeleteVolumeOptions) (*core.DetailedResponse, error)
	// Virtual Network Interface methods
	ListVirtualNetworkInterfacesWithContext(context.Context, *vpcv1.ListVirtualNetworkInterfacesOptions) (*vpcv1.VirtualNetworkInterfaceCollection, *core.DetailedResponse, error)
	DeleteVirtualNetworkInterfacesWithContext(context.Context, *vpcv1.DeleteVirtualNetworkInterfacesOptions) (*vpcv1.VirtualNetworkInterface, *core.DetailedResponse, error)
	// Load Balancer methods
	GetLoadBalancerWithContext(context.Context, *vpcv1.GetLoadBalancerOptions) (*vpcv1.LoadBalancer, *core.DetailedResponse, error)
	ListLoadBalancerPoolsWithContext(context.Context, *vpcv1.ListLoadBalancerPoolsOptions) (*vpcv1.LoadBalancerPoolCollection, *core.DetailedResponse, error)
	GetLoadBalancerPoolWithContext(context.Context, *vpcv1.GetLoadBalancerPoolOptions) (*vpcv1.LoadBalancerPool, *core.DetailedResponse, error)
	CreateLoadBalancerPoolMemberWithContext(context.Context, *vpcv1.CreateLoadBalancerPoolMemberOptions) (*vpcv1.LoadBalancerPoolMember, *core.DetailedResponse, error)
	DeleteLoadBalancerPoolMemberWithContext(context.Context, *vpcv1.DeleteLoadBalancerPoolMemberOptions) (*core.DetailedResponse, error)
	GetLoadBalancerPoolMemberWithContext(context.Context, *vpcv1.GetLoadBalancerPoolMemberOptions) (*vpcv1.LoadBalancerPoolMember, *core.DetailedResponse, error)
	ListLoadBalancerPoolMembersWithContext(context.Context, *vpcv1.ListLoadBalancerPoolMembersOptions) (*vpcv1.LoadBalancerPoolMemberCollection, *core.DetailedResponse, error)
	UpdateLoadBalancerPoolMemberWithContext(context.Context, *vpcv1.UpdateLoadBalancerPoolMemberOptions) (*vpcv1.LoadBalancerPoolMember, *core.DetailedResponse, error)
	UpdateLoadBalancerPoolWithContext(context.Context, *vpcv1.UpdateLoadBalancerPoolOptions) (*vpcv1.LoadBalancerPool, *core.DetailedResponse, error)
}

// VPCClient handles interactions with the IBM Cloud VPC API
type VPCClient struct {
	baseURL  string
	authType string
	apiKey   string
	region   string
	client   vpcClientInterface
}

func NewVPCClient(baseURL, authType, apiKey, region string) (*VPCClient, error) {
	authenticator := &core.IamAuthenticator{
		ApiKey: apiKey,
	}

	options := &vpcv1.VpcV1Options{
		Authenticator: authenticator,
		URL:           baseURL,
	}

	client, err := vpcv1.NewVpcV1(options)
	if err != nil {
		return nil, fmt.Errorf("creating VPC client: %w", err)
	}

	return &VPCClient{
		baseURL:  baseURL,
		authType: authType,
		apiKey:   apiKey,
		region:   region,
		client:   client,
	}, nil
}

// NewVPCClientWithMock creates a VPC client with a mock SDK client for testing
func NewVPCClientWithMock(mockClient vpcClientInterface) *VPCClient {
	return &VPCClient{
		baseURL:  "test",
		authType: "test",
		apiKey:   "test",
		region:   "test",
		client:   mockClient,
	}
}

func (c *VPCClient) CreateInstance(ctx context.Context, instancePrototype vpcv1.InstancePrototypeIntf) (*vpcv1.Instance, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.CreateInstanceOptions{
		InstancePrototype: instancePrototype,
	}

	instance, _, err := c.client.CreateInstanceWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("creating instance: %w", err)
	}

	return instance, nil
}

func (c *VPCClient) DeleteInstance(ctx context.Context, id string) error {
	if c.client == nil {
		return fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.DeleteInstanceOptions{
		ID: &id,
	}

	_, err := c.client.DeleteInstanceWithContext(ctx, options)
	if err != nil {
		return fmt.Errorf("deleting instance: %w", err)
	}

	return nil
}

func (c *VPCClient) GetInstance(ctx context.Context, id string) (*vpcv1.Instance, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.GetInstanceOptions{
		ID: &id,
	}

	instance, _, err := c.client.GetInstanceWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("getting instance: %w", err)
	}

	return instance, nil
}

func (c *VPCClient) ListInstances(ctx context.Context) ([]vpcv1.Instance, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.ListInstancesOptions{}

	instances, _, err := c.client.ListInstancesWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}

	return instances.Instances, nil
}

func (c *VPCClient) UpdateInstanceTags(ctx context.Context, id string, tags map[string]string) error {
	if c.client == nil {
		return fmt.Errorf("VPC client not initialized")
	}

	// Convert tags map to patch data
	patchData := make(map[string]interface{})
	tagsList := make([]string, 0, len(tags))
	for key, value := range tags {
		tagsList = append(tagsList, fmt.Sprintf("%s:%s", key, value))
	}
	patchData["user_tags"] = tagsList

	options := &vpcv1.UpdateInstanceOptions{
		ID:            &id,
		InstancePatch: patchData,
	}

	_, _, err := c.client.UpdateInstanceWithContext(ctx, options)
	if err != nil {
		return fmt.Errorf("updating instance tags: %w", err)
	}

	return nil
}

func (c *VPCClient) ListSubnets(ctx context.Context, vpcID string) (*vpcv1.SubnetCollection, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.ListSubnetsOptions{
		VPCID: &vpcID,
	}

	subnets, _, err := c.client.ListSubnetsWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("listing subnets: %w", err)
	}

	return subnets, nil
}

func (c *VPCClient) GetSubnet(ctx context.Context, subnetID string) (*vpcv1.Subnet, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.GetSubnetOptions{
		ID: &subnetID,
	}

	subnet, _, err := c.client.GetSubnetWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("getting subnet: %w", err)
	}

	return subnet, nil
}

func (c *VPCClient) GetVPC(ctx context.Context, vpcID string) (*vpcv1.VPC, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.GetVPCOptions{
		ID: &vpcID,
	}

	vpc, _, err := c.client.GetVPCWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("getting VPC: %w", err)
	}

	return vpc, nil
}

func (c *VPCClient) GetImage(ctx context.Context, imageID string) (*vpcv1.Image, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.GetImageOptions{
		ID: &imageID,
	}

	image, _, err := c.client.GetImageWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("getting image: %w", err)
	}

	return image, nil
}

// ListImages lists available images with optional filtering
func (c *VPCClient) ListImages(ctx context.Context, options *vpcv1.ListImagesOptions) (*vpcv1.ImageCollection, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	images, _, err := c.client.ListImagesWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}

	return images, nil
}

// ListSecurityGroups lists security groups with optional filtering
func (c *VPCClient) ListSecurityGroupsWithContext(ctx context.Context, options *vpcv1.ListSecurityGroupsOptions) (*vpcv1.SecurityGroupCollection, *core.DetailedResponse, error) {
	if c.client == nil {
		return nil, nil, fmt.Errorf("VPC client not initialized")
	}

	return c.client.ListSecurityGroupsWithContext(ctx, options)
}

// ListVolumes lists volumes in the VPC
func (c *VPCClient) ListVolumes(ctx context.Context, options *vpcv1.ListVolumesOptions) (*vpcv1.VolumeCollection, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}
	
	volumes, _, err := c.client.ListVolumesWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("listing volumes: %w", err)
	}
	
	return volumes, nil
}

// DeleteVolume deletes a volume by ID
func (c *VPCClient) DeleteVolume(ctx context.Context, volumeID string) error {
	if c.client == nil {
		return fmt.Errorf("VPC client not initialized")
	}
	
	options := &vpcv1.DeleteVolumeOptions{
		ID: &volumeID,
	}
	
	_, err := c.client.DeleteVolumeWithContext(ctx, options)
	if err != nil {
		return fmt.Errorf("deleting volume %s: %w", volumeID, err)
	}
	
	return nil
}

// ListVirtualNetworkInterfaces lists virtual network interfaces
func (c *VPCClient) ListVirtualNetworkInterfaces(ctx context.Context, options *vpcv1.ListVirtualNetworkInterfacesOptions) (*vpcv1.VirtualNetworkInterfaceCollection, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}
	
	vnis, _, err := c.client.ListVirtualNetworkInterfacesWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("listing virtual network interfaces: %w", err)
	}
	
	return vnis, nil
}

// DeleteVirtualNetworkInterface deletes a virtual network interface by ID
func (c *VPCClient) DeleteVirtualNetworkInterface(ctx context.Context, vniID string) error {
	if c.client == nil {
		return fmt.Errorf("VPC client not initialized")
	}
	
	options := &vpcv1.DeleteVirtualNetworkInterfacesOptions{
		ID: &vniID,
	}
	
	_, _, err := c.client.DeleteVirtualNetworkInterfacesWithContext(ctx, options)
	if err != nil {
		return fmt.Errorf("deleting virtual network interface %s: %w", vniID, err)
	}
	
	return nil
}

// ListSubnetsWithContext lists subnets with context
func (c *VPCClient) ListSubnetsWithContext(ctx context.Context, options *vpcv1.ListSubnetsOptions) (*vpcv1.SubnetCollection, *core.DetailedResponse, error) {
	if c.client == nil {
		return nil, nil, fmt.Errorf("VPC client not initialized")
	}

	return c.client.ListSubnetsWithContext(ctx, options)
}

// ListInstanceProfiles lists available instance profiles
func (c *VPCClient) ListInstanceProfiles(options *vpcv1.ListInstanceProfilesOptions) (*vpcv1.InstanceProfileCollection, *core.DetailedResponse, error) {
	if c.client == nil {
		return nil, nil, fmt.Errorf("VPC client not initialized")
	}

	return c.client.ListInstanceProfilesWithContext(context.Background(), options)
}

// GetLoadBalancer retrieves a load balancer by ID
func (c *VPCClient) GetLoadBalancer(ctx context.Context, loadBalancerID string) (*vpcv1.LoadBalancer, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.GetLoadBalancerOptions{
		ID: &loadBalancerID,
	}

	loadBalancer, _, err := c.client.GetLoadBalancerWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("getting load balancer %s: %w", loadBalancerID, err)
	}

	return loadBalancer, nil
}

// GetLoadBalancerPool retrieves a load balancer pool by ID
func (c *VPCClient) GetLoadBalancerPool(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPool, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.GetLoadBalancerPoolOptions{
		LoadBalancerID: &loadBalancerID,
		ID:             &poolID,
	}

	pool, _, err := c.client.GetLoadBalancerPoolWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("getting load balancer pool %s/%s: %w", loadBalancerID, poolID, err)
	}

	return pool, nil
}

// ListLoadBalancerPools lists pools for a load balancer
func (c *VPCClient) ListLoadBalancerPools(ctx context.Context, loadBalancerID string) (*vpcv1.LoadBalancerPoolCollection, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.ListLoadBalancerPoolsOptions{
		LoadBalancerID: &loadBalancerID,
	}

	pools, _, err := c.client.ListLoadBalancerPoolsWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("listing load balancer pools for %s: %w", loadBalancerID, err)
	}

	return pools, nil
}

// CreateLoadBalancerPoolMember adds a member to a load balancer pool
func (c *VPCClient) CreateLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID string, target vpcv1.LoadBalancerPoolMemberTargetPrototypeIntf, port int64, weight int64) (*vpcv1.LoadBalancerPoolMember, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.CreateLoadBalancerPoolMemberOptions{
		LoadBalancerID: &loadBalancerID,
		PoolID:         &poolID,
		Target:         target,
		Port:           &port,
		Weight:         &weight,
	}

	member, _, err := c.client.CreateLoadBalancerPoolMemberWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("creating load balancer pool member in %s/%s: %w", loadBalancerID, poolID, err)
	}

	return member, nil
}

// DeleteLoadBalancerPoolMember removes a member from a load balancer pool
func (c *VPCClient) DeleteLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID, memberID string) error {
	if c.client == nil {
		return fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.DeleteLoadBalancerPoolMemberOptions{
		LoadBalancerID: &loadBalancerID,
		PoolID:         &poolID,
		ID:             &memberID,
	}

	_, err := c.client.DeleteLoadBalancerPoolMemberWithContext(ctx, options)
	if err != nil {
		return fmt.Errorf("deleting load balancer pool member %s from %s/%s: %w", memberID, loadBalancerID, poolID, err)
	}

	return nil
}

// GetLoadBalancerPoolMember retrieves a specific pool member
func (c *VPCClient) GetLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID, memberID string) (*vpcv1.LoadBalancerPoolMember, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.GetLoadBalancerPoolMemberOptions{
		LoadBalancerID: &loadBalancerID,
		PoolID:         &poolID,
		ID:             &memberID,
	}

	member, _, err := c.client.GetLoadBalancerPoolMemberWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("getting load balancer pool member %s from %s/%s: %w", memberID, loadBalancerID, poolID, err)
	}

	return member, nil
}

// ListLoadBalancerPoolMembers lists all members in a pool
func (c *VPCClient) ListLoadBalancerPoolMembers(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPoolMemberCollection, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.ListLoadBalancerPoolMembersOptions{
		LoadBalancerID: &loadBalancerID,
		PoolID:         &poolID,
	}

	members, _, err := c.client.ListLoadBalancerPoolMembersWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("listing load balancer pool members for %s/%s: %w", loadBalancerID, poolID, err)
	}

	return members, nil
}

// UpdateLoadBalancerPool updates a load balancer pool configuration
func (c *VPCClient) UpdateLoadBalancerPool(ctx context.Context, loadBalancerID, poolID string, updates map[string]interface{}) (*vpcv1.LoadBalancerPool, error) {
	if c.client == nil {
		return nil, fmt.Errorf("VPC client not initialized")
	}

	options := &vpcv1.UpdateLoadBalancerPoolOptions{
		LoadBalancerID:        &loadBalancerID,
		ID:                    &poolID,
		LoadBalancerPoolPatch: updates,
	}

	pool, _, err := c.client.UpdateLoadBalancerPoolWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("updating load balancer pool %s/%s: %w", loadBalancerID, poolID, err)
	}

	return pool, nil
}
