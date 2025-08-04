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
	"strings"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/image"
	commonTypes "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/vpc/bootstrap"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
)

// VPCInstanceProvider implements VPC-specific instance provisioning
type VPCInstanceProvider struct {
	client            *ibm.Client
	kubeClient        client.Client
	k8sClient         kubernetes.Interface
	bootstrapProvider *bootstrap.VPCBootstrapProvider
	vpcClientManager  *vpcclient.Manager
}

// NewVPCInstanceProvider creates a new VPC instance provider
func NewVPCInstanceProvider(client *ibm.Client, kubeClient client.Client) (commonTypes.VPCInstanceProvider, error) {
	if client == nil {
		return nil, fmt.Errorf("IBM client cannot be nil")
	}

	return &VPCInstanceProvider{
		client:            client,
		kubeClient:        kubeClient,
		k8sClient:         nil, // Will be set via dependency injection
		bootstrapProvider: nil, // Will be lazily initialized when needed
		vpcClientManager:  vpcclient.NewManager(client, 30*time.Minute),
	}, nil
}

// NewVPCInstanceProviderWithKubernetesClient creates a new VPC instance provider with proper kubernetes client injection
func NewVPCInstanceProviderWithKubernetesClient(client *ibm.Client, kubeClient client.Client, kubernetesClient kubernetes.Interface) (commonTypes.VPCInstanceProvider, error) {
	if client == nil {
		return nil, fmt.Errorf("IBM client cannot be nil")
	}

	// Create bootstrap provider immediately with proper dependency injection
	bootstrapProvider := bootstrap.NewVPCBootstrapProvider(client, kubernetesClient, kubeClient)

	return &VPCInstanceProvider{
		client:            client,
		kubeClient:        kubeClient,
		k8sClient:         kubernetesClient,
		bootstrapProvider: bootstrapProvider,
		vpcClientManager:  vpcclient.NewManager(client, 30*time.Minute),
	}, nil
}

// Create provisions a new VPC instance
func (p *VPCInstanceProvider) Create(ctx context.Context, nodeClaim *v1.NodeClaim) (*corev1.Node, error) {
	logger := log.FromContext(ctx)

	if p.kubeClient == nil {
		return nil, fmt.Errorf("kubernetes client not set")
	}

	// Get the NodeClass to extract configuration
	nodeClass := &v1alpha1.IBMNodeClass{}
	if getErr := p.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); getErr != nil {
		return nil, fmt.Errorf("getting NodeClass %s: %w", nodeClaim.Spec.NodeClassRef.Name, getErr)
	}

	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return nil, err
	}

	// Extract instance profile - prefer NodeClass, fallback to labels
	instanceProfile := nodeClass.Spec.InstanceProfile
	if instanceProfile == "" {
		instanceProfile = nodeClaim.Labels["node.kubernetes.io/instance-type"]
		if instanceProfile == "" {
			return nil, fmt.Errorf("instance profile not specified in NodeClass or node claim")
		}
	}

	// Determine zone and subnet - support both explicit and dynamic selection
	zone := nodeClass.Spec.Zone
	subnet := nodeClass.Spec.Subnet
	
	if zone == "" && subnet == "" {
		// Neither zone nor subnet specified - use placement strategy for multi-AZ
		if nodeClass.Spec.PlacementStrategy == nil {
			return nil, fmt.Errorf("zone selection requires either explicit zone/subnet or placement strategy")
		}
		
		// Use subnet provider to select optimal subnet based on placement strategy
		// Note: This would require access to subnet provider - for now return error
		return nil, fmt.Errorf("dynamic zone/subnet selection not yet implemented - specify zone and subnet explicitly")
	} else if zone == "" && subnet != "" {
		// Subnet specified but no zone - derive zone from subnet
		// Note: This would require subnet lookup - for now return error
		return nil, fmt.Errorf("zone derivation from subnet not yet implemented - specify zone explicitly")  
	} else if zone != "" && subnet == "" {
		// Zone specified but no subnet - need subnet selection
		return nil, fmt.Errorf("subnet selection within zone not yet implemented - specify subnet explicitly")
	}
	
	// Both zone and subnet specified - use them directly (existing behavior)
	if zone == "" || subnet == "" {
		return nil, fmt.Errorf("both zone and subnet must be specified")
	}

	logger.Info("Creating VPC instance with VNI", "instance_profile", instanceProfile, "zone", zone, "subnet", subnet)

	// Create virtual network interface for proper VPC service network access
	vniPrototype := &vpcv1.InstanceNetworkAttachmentPrototypeVirtualNetworkInterfaceVirtualNetworkInterfacePrototypeInstanceNetworkAttachmentContext{
		Subnet: &vpcv1.SubnetIdentity{
			ID: &subnet,
		},
		// Enable infrastructure NAT for proper VPC service network routing
		EnableInfrastructureNat: &[]bool{true}[0],
		// Allow IP spoofing set to false for security
		AllowIPSpoofing: &[]bool{false}[0],
		// Set protocol state filtering to auto for proper instance network attachment
		ProtocolStateFilteringMode: &[]string{"auto"}[0],
		// Set explicit name for debugging
		Name: &[]string{fmt.Sprintf("%s-vni", nodeClaim.Name)}[0],
		// Auto-delete when instance is deleted
		AutoDelete: &[]bool{true}[0],
	}

	// Add security groups if specified, otherwise use default
	if len(nodeClass.Spec.SecurityGroups) > 0 {
		var securityGroups []vpcv1.SecurityGroupIdentityIntf
		for _, sg := range nodeClass.Spec.SecurityGroups {
			securityGroups = append(securityGroups, &vpcv1.SecurityGroupIdentity{ID: &sg})
		}
		vniPrototype.SecurityGroups = securityGroups
		logger.Info("Applying security groups to VNI", "security_groups", nodeClass.Spec.SecurityGroups, "count", len(securityGroups))
	} else {
		// Get default security group for VPC
		defaultSG, sgErr := p.getDefaultSecurityGroup(ctx, vpcClient, nodeClass.Spec.VPC)
		if sgErr != nil {
			return nil, fmt.Errorf("getting default security group for VPC %s: %w", nodeClass.Spec.VPC, sgErr)
		}
		vniPrototype.SecurityGroups = []vpcv1.SecurityGroupIdentityIntf{
			&vpcv1.SecurityGroupIdentity{ID: defaultSG.ID},
		}
		logger.Info("Using default security group for VNI", "security_group", *defaultSG.ID)
	}

	// Create primary network attachment with VNI
	primaryNetworkAttachment := &vpcv1.InstanceNetworkAttachmentPrototype{
		Name: &[]string{fmt.Sprintf("%s-primary-attachment", nodeClaim.Name)}[0],
		VirtualNetworkInterface: vniPrototype,
	}

	// Resolve image identifier to image ID
	imageResolver := image.NewResolver(vpcClient, nodeClass.Spec.Region)
	imageID, err := imageResolver.ResolveImage(ctx, nodeClass.Spec.Image)
	if err != nil {
		return nil, fmt.Errorf("resolving image %s: %w", nodeClass.Spec.Image, err)
	}

	// Create boot volume attachment for the instance
	bootVolumeAttachment := &vpcv1.VolumeAttachmentPrototypeInstanceByImageContext{
		Volume: &vpcv1.VolumePrototypeInstanceByImageContext{
			Name: &[]string{fmt.Sprintf("%s-boot", nodeClaim.Name)}[0],
			Profile: &vpcv1.VolumeProfileIdentity{
				Name: &[]string{"general-purpose"}[0], // Default boot volume profile
			},
			Capacity: &[]int64{100}[0], // Default 100GB boot volume
		},
		DeleteVolumeOnInstanceDelete: &[]bool{true}[0],
	}

	// Create instance prototype with VNI for proper VPC service network access
	instancePrototype := &vpcv1.InstancePrototypeInstanceByImage{
		Name: &nodeClaim.Name,
		Zone: &vpcv1.ZoneIdentity{
			Name: &zone,
		},
		Profile: &vpcv1.InstanceProfileIdentity{
			Name: &instanceProfile,
		},
		VPC: &vpcv1.VPCIdentity{
			ID: &nodeClass.Spec.VPC,
		},
		Image: &vpcv1.ImageIdentity{
			ID: &imageID,
		},
		// Use PrimaryNetworkAttachment with VNI for proper VPC service network routing
		PrimaryNetworkAttachment: primaryNetworkAttachment,
		BootVolumeAttachment:     bootVolumeAttachment,
		
		// Add availability policy for better instance management
		AvailabilityPolicy: &vpcv1.InstanceAvailabilityPolicyPrototype{
			HostFailure: &[]string{"restart"}[0],
		},
	}

	// Add placement target if specified
	if nodeClass.Spec.PlacementTarget != "" {
		instancePrototype.PlacementTarget = &vpcv1.InstancePlacementTargetPrototype{
			ID: &nodeClass.Spec.PlacementTarget,
		}
	}

	// Add resource group if specified
	if nodeClass.Spec.ResourceGroup != "" {
		instancePrototype.ResourceGroup = &vpcv1.ResourceGroupIdentity{
			ID: &nodeClass.Spec.ResourceGroup,
		}
	}

	// Add SSH keys if specified
	if len(nodeClass.Spec.SSHKeys) > 0 {
		var sshKeys []vpcv1.KeyIdentityIntf
		for _, key := range nodeClass.Spec.SSHKeys {
			sshKeys = append(sshKeys, &vpcv1.KeyIdentity{ID: &key})
		}
		instancePrototype.Keys = sshKeys
	}

	// Generate bootstrap user data using the bootstrap provider
	userData, err := p.generateBootstrapUserData(ctx, nodeClass, types.NamespacedName{
		Name:      nodeClaim.Name,
		Namespace: nodeClaim.Namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("generating bootstrap user data: %w", err)
	}

	// Set user data
	instancePrototype.UserData = &userData

	// Enable metadata service for instance ID retrieval
	instancePrototype.MetadataService = &vpcv1.InstanceMetadataServicePrototype{
		Enabled:         &[]bool{true}[0],
		Protocol:        &[]string{"http"}[0],
		ResponseHopLimit: &[]int64{2}[0],
	}

	// Create the instance
	instance, err := vpcClient.CreateInstance(ctx, instancePrototype)
	if err != nil {
		// Check if this is a partial failure that might have created resources
		ibmErr := ibm.ParseError(err)
		logger.Error(err, "Error creating VPC instance",
			"status_code", ibmErr.StatusCode,
			"error_code", ibmErr.Code,
			"retryable", ibmErr.Retryable)
		
		if p.isPartialFailure(ibmErr) {
			logger.Info("Instance creation failed after partial resource creation, attempting cleanup",
				"instance_name", nodeClaim.Name,
				"error_code", ibmErr.Code)
			
			// Attempt to clean up any orphaned resources
			if cleanupErr := p.cleanupOrphanedResources(ctx, vpcClient, nodeClaim.Name, nodeClass.Spec.VPC, logger); cleanupErr != nil {
				logger.Error(cleanupErr, "Failed to cleanup orphaned resources after instance creation failure",
					"instance_name", nodeClaim.Name)
				// Don't fail the original error, but log the cleanup failure
			}
		}
		
		return nil, vpcclient.HandleVPCError(err, logger, "creating VPC instance")
	}

	logger.Info("VPC instance created successfully", "instance_id", *instance.ID, "name", *instance.Name)
	
	// Verify security groups were applied correctly
	if instance.PrimaryNetworkInterface != nil && instance.PrimaryNetworkInterface.ID != nil {
		logger.Info("Instance created with primary network interface", "interface_id", *instance.PrimaryNetworkInterface.ID)
		// Note: Security groups information may not be available in the instance creation response
		// This would require a separate GetInstance call to verify security groups
	} else {
		logger.Info("Instance created but primary network interface information not available in response")
	}

	// Create Node representation
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
			Labels: map[string]string{
				"karpenter.sh/managed":             "true",
				"karpenter.ibm.sh/vpc-id":          nodeClass.Spec.VPC,
				"karpenter.ibm.sh/zone":            zone,
				"karpenter.ibm.sh/region":          nodeClass.Spec.Region,
				"karpenter.ibm.sh/instance-type":   instanceProfile,
				"node.kubernetes.io/instance-type": instanceProfile,
				"topology.kubernetes.io/zone":      zone,
				"topology.kubernetes.io/region":    nodeClass.Spec.Region,
				"karpenter.sh/capacity-type":       "on-demand",
				"karpenter.sh/nodepool":            nodeClaim.Labels["karpenter.sh/nodepool"],
			},
		},
		Spec: corev1.NodeSpec{
			// Use the full instance ID including the zone prefix (e.g., 02c7_uuid)
			// This ensures consistency with how IBM Cloud APIs expect the instance ID
			ProviderID: fmt.Sprintf("ibm:///%s/%s", nodeClass.Spec.Region, *instance.ID),
		},
		Status: corev1.NodeStatus{
			Phase: corev1.NodePending,
			Conditions: []corev1.NodeCondition{
				{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionUnknown,
					LastHeartbeatTime:  metav1.Now(),
					LastTransitionTime: metav1.Now(),
					Reason:             "NodeCreating",
					Message:            "Node is being created",
				},
			},
		},
	}

	return node, nil
}

// Delete removes a VPC instance
func (p *VPCInstanceProvider) Delete(ctx context.Context, node *corev1.Node) error {
	logger := log.FromContext(ctx)

	instanceID := extractInstanceIDFromProviderID(node.Spec.ProviderID)
	if instanceID == "" {
		return fmt.Errorf("could not extract instance ID from provider ID: %s", node.Spec.ProviderID)
	}

	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return err
	}

	logger.Info("Deleting VPC instance", "instance_id", instanceID, "node", node.Name)

	err = vpcClient.DeleteInstance(ctx, instanceID)
	if err != nil {
		// Check if this is a "not found" error, which is acceptable
		if isIBMInstanceNotFoundError(err) {
			logger.Info("VPC instance already deleted", "instance_id", instanceID)
			return nil
		}
		return vpcclient.HandleVPCError(err, logger, "deleting VPC instance", "instance_id", instanceID)
	}

	logger.Info("VPC instance deleted successfully", "instance_id", instanceID)
	return nil
}

// Get retrieves information about a VPC instance
func (p *VPCInstanceProvider) Get(ctx context.Context, providerID string) (*corev1.Node, error) {
	instanceID := extractInstanceIDFromProviderID(providerID)
	if instanceID == "" {
		return nil, fmt.Errorf("could not extract instance ID from provider ID: %s", providerID)
	}

	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return nil, err
	}

	instance, err := vpcClient.GetInstance(ctx, instanceID)
	if err != nil {
		if isIBMInstanceNotFoundError(err) {
			return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance %s not found", instanceID))
		}
		return nil, fmt.Errorf("getting VPC instance %s: %w", instanceID, err)
	}

	// Convert VPC instance to Node representation
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: *instance.Name,
		},
		Spec: corev1.NodeSpec{
			ProviderID: providerID,
		},
	}

	return node, nil
}

// List returns all VPC instances
func (p *VPCInstanceProvider) List(ctx context.Context) ([]*corev1.Node, error) {
	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return nil, err
	}

	instances, err := vpcClient.ListInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing VPC instances: %w", err)
	}

	var nodes []*corev1.Node
	for _, instance := range instances {
		if instance.ID != nil && instance.Name != nil {
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: *instance.Name,
				},
				Spec: corev1.NodeSpec{
					ProviderID: fmt.Sprintf("ibm:///%s/%s", "region", *instance.ID),
				},
			}
			nodes = append(nodes, node)
		}
	}

	return nodes, nil
}

// UpdateTags updates tags on a VPC instance
func (p *VPCInstanceProvider) UpdateTags(ctx context.Context, providerID string, tags map[string]string) error {
	instanceID := extractInstanceIDFromProviderID(providerID)
	if instanceID == "" {
		return fmt.Errorf("could not extract instance ID from provider ID: %s", providerID)
	}

	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return err
	}

	return vpcClient.UpdateInstanceTags(ctx, instanceID, tags)
}

// extractInstanceIDFromProviderID extracts the instance ID from a provider ID
func extractInstanceIDFromProviderID(providerID string) string {
	// Provider ID format: ibm:///region/instance-id
	// Instance ID includes zone prefix (e.g., 02c7_uuid)
	parts := strings.Split(providerID, "/")
	if len(parts) >= 4 {
		return parts[len(parts)-1]
	}
	return ""
}

// getDefaultSecurityGroup gets the default security group for a VPC
func (p *VPCInstanceProvider) getDefaultSecurityGroup(ctx context.Context, vpcClient *ibm.VPCClient, vpcID string) (*vpcv1.SecurityGroup, error) {
	// List security groups for the VPC to find the default one
	options := &vpcv1.ListSecurityGroupsOptions{
		VPCID: &vpcID,
	}
	
	securityGroups, _, err := vpcClient.ListSecurityGroupsWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("listing security groups for VPC %s: %w", vpcID, err)
	}
	
	// Find the default security group
	for _, sg := range securityGroups.SecurityGroups {
		if sg.Name != nil && *sg.Name == "default" {
			return &sg, nil
		}
	}
	
	// If no default security group found, return an error
	return nil, fmt.Errorf("default security group not found for VPC %s", vpcID)
}

// isIBMInstanceNotFoundError checks if the error indicates an instance was not found in IBM Cloud VPC
func isIBMInstanceNotFoundError(err error) bool {
	return ibm.IsNotFound(err)
}

// isPartialFailure determines if an instance creation error indicates partial resource creation
func (p *VPCInstanceProvider) isPartialFailure(ibmErr *ibm.IBMError) bool {
	if ibmErr == nil {
		return false
	}
	
	// Check for specific error codes that indicate partial failure
	switch ibmErr.Code {
	case "vpc_instance_quota_exceeded", "vpc_instance_profile_not_available":
		// These errors can occur after VNI creation but before instance completion
		return true
	case "vpc_security_group_not_found", "vpc_subnet_not_available":
		// These might occur after some network resources are created
		return true
	case "vpc_volume_capacity_insufficient", "vpc_boot_volume_creation_failed":
		// Volume creation failures might leave network resources
		return true
	default:
		// For unknown errors with 5xx status codes, assume potential partial failure
		return ibmErr.StatusCode >= 500 && ibmErr.StatusCode < 600
	}
}

// cleanupOrphanedResources attempts to clean up resources that might be left after instance creation failure
func (p *VPCInstanceProvider) cleanupOrphanedResources(ctx context.Context, vpcClient *ibm.VPCClient, instanceName, vpcID string, logger logr.Logger) error {
	logger.Info("Starting cleanup of potentially orphaned resources", "instance_name", instanceName)
	
	var errors []error
	
	// Look for orphaned VNIs with the expected name pattern
	vniName := fmt.Sprintf("%s-vni", instanceName)
	if err := p.cleanupOrphanedVNI(ctx, vpcClient, vniName, vpcID, logger); err != nil {
		errors = append(errors, fmt.Errorf("cleaning up VNI %s: %w", vniName, err))
	}
	
	// Look for orphaned volumes with the expected name pattern
	volumeName := fmt.Sprintf("%s-boot", instanceName)
	if err := p.cleanupOrphanedVolume(ctx, vpcClient, volumeName, logger); err != nil {
		errors = append(errors, fmt.Errorf("cleaning up volume %s: %w", volumeName, err))
	}
	
	if len(errors) > 0 {
		return fmt.Errorf("cleanup failed with %d errors: %v", len(errors), errors)
	}
	
	logger.Info("Cleanup of orphaned resources completed successfully")
	return nil
}

// cleanupOrphanedVNI removes a VNI that might have been created during failed instance creation
func (p *VPCInstanceProvider) cleanupOrphanedVNI(ctx context.Context, vpcClient *ibm.VPCClient, vniName, vpcID string, logger logr.Logger) error {
	logger.Info("Searching for orphaned VNI to cleanup", "vni_name", vniName, "vpc_id", vpcID)
	
	// List virtual network interfaces to find the orphaned one
	options := &vpcv1.ListVirtualNetworkInterfacesOptions{
		// Note: VNI listing doesn't support name filtering, so we list all and filter manually
	}
	
	vnis, err := vpcClient.ListVirtualNetworkInterfaces(ctx, options)
	if err != nil {
		return fmt.Errorf("listing virtual network interfaces for cleanup: %w", err)
	}
	
	// Look for VNI with matching name
	for _, vni := range vnis.VirtualNetworkInterfaces {
		if vni.Name != nil && *vni.Name == vniName {
			logger.Info("Found orphaned VNI, attempting deletion", "vni_id", *vni.ID, "vni_name", vniName)
			
			if err := vpcClient.DeleteVirtualNetworkInterface(ctx, *vni.ID); err != nil {
				// Check if it's already deleted (not found error is acceptable)
				if ibm.IsNotFound(err) {
					logger.Info("VNI already deleted during cleanup", "vni_id", *vni.ID)
					return nil
				}
				return fmt.Errorf("deleting orphaned VNI %s: %w", *vni.ID, err)
			}
			
			logger.Info("Successfully cleaned up orphaned VNI", "vni_id", *vni.ID, "vni_name", vniName)
			return nil
		}
	}
	
	logger.Info("No orphaned VNI found with expected name", "vni_name", vniName)
	return nil
}

// cleanupOrphanedVolume removes a volume that might have been created during failed instance creation
func (p *VPCInstanceProvider) cleanupOrphanedVolume(ctx context.Context, vpcClient *ibm.VPCClient, volumeName string, logger logr.Logger) error {
	logger.Info("Searching for orphaned volume to cleanup", "volume_name", volumeName)
	
	// List volumes to find the orphaned one
	options := &vpcv1.ListVolumesOptions{
		Name: &volumeName,
	}
	
	volumes, err := vpcClient.ListVolumes(ctx, options)
	if err != nil {
		return fmt.Errorf("listing volumes for cleanup: %w", err)
	}
	
	// Look for volume with matching name
	for _, volume := range volumes.Volumes {
		if volume.Name != nil && *volume.Name == volumeName {
			logger.Info("Found orphaned volume, attempting deletion", "volume_id", *volume.ID, "volume_name", volumeName)
			
			if err := vpcClient.DeleteVolume(ctx, *volume.ID); err != nil {
				// Check if it's already deleted (not found error is acceptable)
				if ibm.IsNotFound(err) {
					logger.Info("Volume already deleted during cleanup", "volume_id", *volume.ID)
					return nil
				}
				return fmt.Errorf("deleting orphaned volume %s: %w", *volume.ID, err)
			}
			
			logger.Info("Successfully cleaned up orphaned volume", "volume_id", *volume.ID, "volume_name", volumeName)
			return nil
		}
	}
	
	logger.Info("No orphaned volume found with expected name", "volume_name", volumeName)
	return nil
}

// generateBootstrapUserData generates bootstrap user data using the VPC bootstrap provider
func (p *VPCInstanceProvider) generateBootstrapUserData(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName) (string, error) {
	return p.generateBootstrapUserDataWithInstanceID(ctx, nodeClass, nodeClaim, "")
}

// generateBootstrapUserDataWithInstanceID generates bootstrap user data with a specific instance ID
func (p *VPCInstanceProvider) generateBootstrapUserDataWithInstanceID(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName, instanceID string) (string, error) {
	logger := log.FromContext(ctx)

	// If manual userData is provided, use it as-is (fallback behavior)
	if nodeClass.Spec.UserData != "" {
		logger.Info("Using manual userData from IBMNodeClass")
		return nodeClass.Spec.UserData, nil
	}

	// Initialize bootstrap provider if not already done
	if p.bootstrapProvider == nil {
		if p.k8sClient != nil {
			// Use properly injected kubernetes client
			p.bootstrapProvider = bootstrap.NewVPCBootstrapProvider(p.client, p.k8sClient, p.kubeClient)
		} else {
			// Fallback to creating kubernetes client (for backward compatibility)
			k8sClient, err := p.createKubernetesClient(ctx)
			if err != nil {
				logger.Error(err, "Failed to create kubernetes client, falling back to basic bootstrap")
				return p.getBasicBootstrapScript(nodeClass), nil
			}

			p.k8sClient = k8sClient
			p.bootstrapProvider = bootstrap.NewVPCBootstrapProvider(p.client, k8sClient, p.kubeClient)
		}
	}

	// Generate dynamic bootstrap script with instance ID
	logger.Info("Generating dynamic bootstrap script with automatic cluster discovery", "instanceID", instanceID)
	userData, err := p.bootstrapProvider.GetUserDataWithInstanceID(ctx, nodeClass, nodeClaim, instanceID)
	if err != nil {
		logger.Error(err, "Failed to generate bootstrap user data, falling back to basic bootstrap")
		return p.getBasicBootstrapScript(nodeClass), nil
	}

	logger.Info("Successfully generated dynamic bootstrap script")
	return userData, nil
}

// createKubernetesClient creates a kubernetes.Interface from the in-cluster config
func (p *VPCInstanceProvider) createKubernetesClient(ctx context.Context) (kubernetes.Interface, error) {
	// Since we're running inside the cluster, we can use the in-cluster config
	// This is the same config that the controller-runtime client uses
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("creating in-cluster config: %w", err)
	}

	// Create kubernetes clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes clientset: %w", err)
	}

	return clientset, nil
}

// getBasicBootstrapScript returns a basic bootstrap script when automatic generation fails
func (p *VPCInstanceProvider) getBasicBootstrapScript(nodeClass *v1alpha1.IBMNodeClass) string {
	return fmt.Sprintf(`#!/bin/bash
# Karpenter IBM Cloud Provider - Basic Bootstrap
# This is a fallback script when automatic bootstrap generation fails
echo "$(date): Basic bootstrap for region %s"

# Essential system configuration for kubeadm (CRITICAL FIX)
echo "$(date): Configuring system for kubeadm..."
echo 'net.ipv4.ip_forward = 1' >> /etc/sysctl.conf && sysctl -p
swapoff -a && sed -i '/swap/d' /etc/fstab

echo "$(date): Manual configuration required for cluster joining"
echo "$(date): Set nodeClass.spec.userData with proper bootstrap script"
# To make nodes join the cluster, you need to provide:
# 1. Bootstrap token: kubectl create token --print-join-command
# 2. Internal API endpoint (not external)
# 3. Proper hostname configuration for IBM Cloud
`, nodeClass.Spec.Region)
}
