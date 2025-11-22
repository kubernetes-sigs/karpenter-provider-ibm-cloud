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
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/IBM/platform-services-go-sdk/resourcemanagerv2"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/metrics"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/image"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/bootstrap"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
)

type QuotaInfo struct {
	InstanceUtilization float64
	VCPUUtilization     float64
}

// VPCInstanceProvider implements VPC-specific instance provisioning
type VPCInstanceProvider struct {
	client                 *ibm.Client
	kubeClient             client.Client
	k8sClient              kubernetes.Interface
	bootstrapProvider      *bootstrap.VPCBootstrapProvider
	subnetProvider         subnet.Provider
	vpcClientManager       *vpcclient.Manager
	resourceManagerService *resourcemanagerv2.ResourceManagerV2
}

// Option configures the VPCInstanceProvider
type Option func(*VPCInstanceProvider) error

// WithKubernetesClient sets the Kubernetes client for the provider
func WithKubernetesClient(k8sClient kubernetes.Interface) Option {
	return func(p *VPCInstanceProvider) error {
		if k8sClient == nil {
			return fmt.Errorf("kubernetes client cannot be nil when provided")
		}
		p.k8sClient = k8sClient
		// Create bootstrap provider immediately with proper dependency injection
		p.bootstrapProvider = bootstrap.NewVPCBootstrapProvider(p.client, k8sClient, p.kubeClient)
		// Set Kubernetes client on subnet provider for cluster awareness
		p.subnetProvider.SetKubernetesClient(k8sClient)
		return nil
	}
}

// WithBootstrapProvider sets a custom bootstrap provider
func WithBootstrapProvider(bootstrapProvider *bootstrap.VPCBootstrapProvider) Option {
	return func(p *VPCInstanceProvider) error {
		if bootstrapProvider == nil {
			return fmt.Errorf("bootstrap provider cannot be nil when provided")
		}
		p.bootstrapProvider = bootstrapProvider
		return nil
	}
}

// WithVPCClientManager sets a custom VPC client manager
func WithVPCClientManager(manager *vpcclient.Manager) Option {
	return func(p *VPCInstanceProvider) error {
		if manager == nil {
			return fmt.Errorf("VPC client manager cannot be nil when provided")
		}
		p.vpcClientManager = manager
		return nil
	}
}

// NewVPCInstanceProvider creates a new VPC instance provider with optional configuration
func NewVPCInstanceProvider(client *ibm.Client, kubeClient client.Client, opts ...Option) (commonTypes.VPCInstanceProvider, error) {
	if client == nil {
		return nil, fmt.Errorf("IBM client cannot be nil")
	}
	if kubeClient == nil {
		return nil, fmt.Errorf("kubernetes client cannot be nil")
	}

	// Create base provider with defaults
	provider := &VPCInstanceProvider{
		client:                 client,
		kubeClient:             kubeClient,
		k8sClient:              nil, // Will be set via options if provided
		bootstrapProvider:      nil, // Will be lazily initialized or set via options
		subnetProvider:         subnet.NewProvider(client),
		vpcClientManager:       vpcclient.NewManager(client, 30*time.Minute),
		resourceManagerService: nil, // Will be initialized after applying options
	}

	// Apply options
	for _, opt := range opts {
		if err := opt(provider); err != nil {
			return nil, fmt.Errorf("applying option: %w", err)
		}
	}

	// Create Resource Manager service if not provided via options
	if provider.resourceManagerService == nil {
		apiKey := os.Getenv("IBMCLOUD_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("IBMCLOUD_API_KEY environment variable is required")
		}

		authenticator := &core.IamAuthenticator{
			ApiKey: apiKey,
		}
		resourceManagerServiceOptions := &resourcemanagerv2.ResourceManagerV2Options{
			Authenticator: authenticator,
		}
		resourceManagerService, err := resourcemanagerv2.NewResourceManagerV2(resourceManagerServiceOptions)
		if err != nil {
			return nil, fmt.Errorf("failed to create resource manager service: %w", err)
		}
		provider.resourceManagerService = resourceManagerService
	}

	return provider, nil
}

// Deprecated: Use NewVPCInstanceProvider with WithKubernetesClient option instead
// NewVPCInstanceProviderWithKubernetesClient creates a new VPC instance provider with kubernetes client
func NewVPCInstanceProviderWithKubernetesClient(client *ibm.Client, kubeClient client.Client, kubernetesClient kubernetes.Interface) (commonTypes.VPCInstanceProvider, error) {
	return NewVPCInstanceProvider(client, kubeClient, WithKubernetesClient(kubernetesClient))
}

// Create provisions a new VPC instance
func (p *VPCInstanceProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error) {
	logger := log.FromContext(ctx)

	// Start timing for provisioning duration
	start := time.Now()
	var instanceType string
	if len(instanceTypes) > 0 {
		instanceType = instanceTypes[0].Name
	}

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
		metrics.ErrorsByType.WithLabelValues("vpc_client_error", "instance_provider", nodeClass.Spec.Region).Inc()
		return nil, err
	}

	// Select an instance type from the compatible types provided by Karpenter
	if len(instanceTypes) == 0 {
		return nil, fmt.Errorf("no compatible instance types provided for nodeclaim %s", nodeClaim.Name)
	}

	// Use the first compatible instance type (Karpenter has already ranked them by preference)
	selectedInstanceType := instanceTypes[0]
	if selectedInstanceType == nil {
		return nil, fmt.Errorf("first instance type in slice is nil for nodeclaim %s, available types: %d", nodeClaim.Name, len(instanceTypes))
	}

	instanceProfile := selectedInstanceType.Name
	if instanceProfile == "" {
		return nil, fmt.Errorf("selected instance type has empty name: %+v, available types: %d. "+
			"This will cause IBM VPC oneOf constraint errors. "+
			"Ensure IBMNodeClass has a valid instanceProfile specified", selectedInstanceType, len(instanceTypes))
	}

	// Additional validation for oneOf constraint compliance
	if strings.TrimSpace(instanceProfile) == "" {
		return nil, fmt.Errorf("instance profile is empty or whitespace-only: '%s'. "+
			"This will cause IBM VPC oneOf constraint validation to fail", instanceProfile)
	}

	logger.Info("Selected instance type",
		"instanceType", instanceProfile,
		"availableTypes", len(instanceTypes),
		"selectedInstanceTypeDetails", fmt.Sprintf("%+v", selectedInstanceType),
		"nodeClaim", nodeClaim.Name)

	// Determine zone and subnet - support both explicit and dynamic selection
	zone := nodeClass.Spec.Zone
	subnet := nodeClass.Spec.Subnet

	if zone == "" && subnet == "" {
		// Neither zone nor subnet specified - use placement strategy for multi-AZ
		if nodeClass.Spec.PlacementStrategy == nil {
			return nil, fmt.Errorf("zone selection requires either explicit zone/subnet or placement strategy")
		}

		// First, check if the controller has already selected subnets for us
		if len(nodeClass.Status.SelectedSubnets) > 0 {
			// Use pre-selected subnets from the autoplacement controller
			selectedSubnetID := p.selectSubnetFromStatusList(nodeClass.Status.SelectedSubnets)

			// Get subnet info to retrieve zone
			subnetInfo, subnetErr := p.subnetProvider.GetSubnet(ctx, selectedSubnetID)
			if subnetErr != nil {
				return nil, fmt.Errorf("getting subnet info for selected subnet %s: %w", selectedSubnetID, subnetErr)
			}

			zone = subnetInfo.Zone
			subnet = selectedSubnetID

			logger.Info("Using pre-selected subnet from status",
				"zone", zone, "subnet", subnet, "selectedSubnets", nodeClass.Status.SelectedSubnets)
		} else {
			// Fallback: Select subnets directly if status not populated
			// This handles backward compatibility and cases where autoplacement controller hasn't run yet
			selectedSubnets, selectErr := p.subnetProvider.SelectSubnets(ctx, nodeClass.Spec.VPC, nodeClass.Spec.PlacementStrategy)
			if selectErr != nil {
				return nil, fmt.Errorf("selecting subnets with placement strategy: %w", selectErr)
			}

			if len(selectedSubnets) == 0 {
				return nil, fmt.Errorf("no subnets selected by placement strategy")
			}

			// Select subnet using round-robin across zones for balanced distribution
			selectedSubnet := p.selectSubnetFromMultiZoneList(selectedSubnets)
			zone = selectedSubnet.Zone
			subnet = selectedSubnet.ID

			logger.Info("Selected zone and subnet using placement strategy (fallback)",
				"zone", zone, "subnet", subnet, "strategy", nodeClass.Spec.PlacementStrategy.ZoneBalance)
		}

	} else if zone == "" && subnet != "" {
		// Subnet specified but no zone - derive zone from subnet
		subnetInfo, subnetErr := p.subnetProvider.GetSubnet(ctx, subnet)
		if subnetErr != nil {
			return nil, fmt.Errorf("getting subnet info for zone derivation: %w", subnetErr)
		}
		zone = subnetInfo.Zone
		logger.Info("Derived zone from subnet", "zone", zone, "subnet", subnet)

	} else if zone != "" && subnet == "" {
		// Zone specified but no subnet - select subnet within zone
		allSubnets, listErr := p.subnetProvider.ListSubnets(ctx, nodeClass.Spec.VPC)
		if listErr != nil {
			return nil, fmt.Errorf("listing subnets for zone-based selection: %w", listErr)
		}

		// Find best subnet in the specified zone
		var bestSubnetID string
		var bestSubnetAvailableIPs int32 = -1
		for _, s := range allSubnets {
			if s.Zone == zone && s.State == "available" {
				if s.AvailableIPs > bestSubnetAvailableIPs {
					bestSubnetID = s.ID
					bestSubnetAvailableIPs = s.AvailableIPs
				}
			}
		}

		if bestSubnetID == "" {
			return nil, fmt.Errorf("no available subnet found in zone %s", zone)
		}

		subnet = bestSubnetID
		logger.Info("Selected subnet within specified zone", "zone", zone, "subnet", subnet)
	}

	// Both zone and subnet specified - use them directly (existing behavior)
	if zone == "" || subnet == "" {
		return nil, fmt.Errorf("both zone and subnet must be specified")
	}

	logger.Info("Creating VPC instance with VNI", "instance_profile", instanceProfile, "zone", zone, "subnet", subnet)

	// Create virtual network interface for proper VPC service network access
	vniPrototype := &vpcv1.InstanceNetworkAttachmentPrototypeVirtualNetworkInterfaceVirtualNetworkInterfacePrototypeInstanceNetworkAttachmentContext{
		Subnet: &vpcv1.SubnetIdentityByID{
			ID: &subnet,
		},
		// Enable infrastructure NAT for proper VPC service network routing
		EnableInfrastructureNat: &[]bool{true}[0],
		// Allow IP spoofing set to false for security
		AllowIPSpoofing: &[]bool{false}[0],
		// Set protocol state filtering to auto for proper instance network attachment
		ProtocolStateFilteringMode: &[]string{"auto"}[0],
		// Set explicit name
		Name: &[]string{fmt.Sprintf("%s-vni", nodeClaim.Name)}[0],
		// Auto-delete when instance is deleted
		AutoDelete: &[]bool{true}[0],
	}

	// Add resource group to VNI (required for oneOf validation)
	// The VNI prototype requires a resource group to satisfy oneOf constraint
	if nodeClass.Spec.ResourceGroup != "" {
		resourceGroupID, rgErr := p.resolveResourceGroupID(ctx, nodeClass.Spec.ResourceGroup)
		if rgErr != nil {
			return nil, fmt.Errorf("resolving resource group for VNI %s: %w", nodeClass.Spec.ResourceGroup, rgErr)
		}
		vniPrototype.ResourceGroup = &vpcv1.ResourceGroupIdentityByID{
			ID: &resourceGroupID,
		}
		logger.Info("VNI resource group set", "input", nodeClass.Spec.ResourceGroup, "resolved_id", resourceGroupID)
	} else {
		// If no resource group specified, the VNI will use the account default
		// This satisfies the oneOf constraint by explicitly not setting the ResourceGroup field
		logger.Info("No resource group specified for VNI, using account default")
	}

	// Add security groups if specified, otherwise use default
	if len(nodeClass.Spec.SecurityGroups) > 0 {
		var securityGroups []vpcv1.SecurityGroupIdentityIntf
		for _, sg := range nodeClass.Spec.SecurityGroups {
			securityGroups = append(securityGroups, &vpcv1.SecurityGroupIdentityByID{ID: &sg})
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
			&vpcv1.SecurityGroupIdentityByID{ID: defaultSG.ID},
		}
		logger.Info("Using default security group for VNI", "security_group", *defaultSG.ID)
	}

	// Create primary network attachment with VNI
	primaryNetworkAttachment := &vpcv1.InstanceNetworkAttachmentPrototype{
		Name:                    &[]string{fmt.Sprintf("%s-primary-attachment", nodeClaim.Name)}[0],
		VirtualNetworkInterface: vniPrototype,
	}

	// Resolve image identifier to image ID
	// First, try to use the cached resolved image from NodeClass status (populated by status controller)
	// This eliminates duplicate VPC API calls and ensures consistency
	var imageID string

	if nodeClass.Status.ResolvedImageID != "" {
		// Use cached resolved image from status
		imageID = nodeClass.Status.ResolvedImageID
		logger.Info("Using cached resolved image from NodeClass status",
			"resolvedImageID", imageID,
			"hasImageSelector", nodeClass.Spec.ImageSelector != nil,
			"hasExplicitImage", nodeClass.Spec.Image != "")
	} else {
		// Fall back to inline resolution for backwards compatibility
		// This handles cases where status controller hasn't populated the field yet
		logger.V(1).Info("NodeClass status does not have cached resolved image, performing inline resolution")

		imageResolver := image.NewResolver(vpcClient, nodeClass.Spec.Region, logger)

		// Use explicit image if specified, otherwise use imageSelector
		if nodeClass.Spec.Image != "" {
			logger.Info("Resolving explicit image inline", "image", nodeClass.Spec.Image)
			imageID, err = imageResolver.ResolveImage(ctx, nodeClass.Spec.Image)
			if err != nil {
				logger.Error(err, "Failed to resolve explicit image", "image", nodeClass.Spec.Image)
				return nil, fmt.Errorf("resolving image %s: %w", nodeClass.Spec.Image, err)
			}
			logger.Info("Successfully resolved explicit image inline", "image", nodeClass.Spec.Image, "imageID", imageID)
		} else if nodeClass.Spec.ImageSelector != nil {
			logger.Info("Resolving image using selector inline",
				"os", nodeClass.Spec.ImageSelector.OS,
				"majorVersion", nodeClass.Spec.ImageSelector.MajorVersion,
				"minorVersion", nodeClass.Spec.ImageSelector.MinorVersion,
				"architecture", nodeClass.Spec.ImageSelector.Architecture,
				"variant", nodeClass.Spec.ImageSelector.Variant)
			imageID, err = imageResolver.ResolveImageBySelector(ctx, nodeClass.Spec.ImageSelector)
			if err != nil {
				logger.Error(err, "Failed to resolve image using selector",
					"os", nodeClass.Spec.ImageSelector.OS,
					"majorVersion", nodeClass.Spec.ImageSelector.MajorVersion,
					"minorVersion", nodeClass.Spec.ImageSelector.MinorVersion,
					"architecture", nodeClass.Spec.ImageSelector.Architecture,
					"variant", nodeClass.Spec.ImageSelector.Variant)
				return nil, fmt.Errorf("resolving image using selector (os=%s, majorVersion=%s, minorVersion=%s, architecture=%s, variant=%s): %w",
					nodeClass.Spec.ImageSelector.OS,
					nodeClass.Spec.ImageSelector.MajorVersion,
					nodeClass.Spec.ImageSelector.MinorVersion,
					nodeClass.Spec.ImageSelector.Architecture,
					nodeClass.Spec.ImageSelector.Variant,
					err)
			}
			logger.Info("Successfully resolved image using selector inline",
				"os", nodeClass.Spec.ImageSelector.OS,
				"majorVersion", nodeClass.Spec.ImageSelector.MajorVersion,
				"minorVersion", nodeClass.Spec.ImageSelector.MinorVersion,
				"architecture", nodeClass.Spec.ImageSelector.Architecture,
				"variant", nodeClass.Spec.ImageSelector.Variant,
				"resolvedImageID", imageID)
		} else {
			logger.Error(nil, "Neither image nor imageSelector specified in NodeClass")
			return nil, fmt.Errorf("neither image nor imageSelector specified in NodeClass")
		}
	}

	// Validate the resolved imageID is not empty
	if imageID == "" {
		logger.Error(nil, "Image resolution returned empty imageID",
			"hasImage", nodeClass.Spec.Image != "",
			"hasImageSelector", nodeClass.Spec.ImageSelector != nil,
			"hasStatusResolvedImage", nodeClass.Status.ResolvedImageID != "",
			"explicitImage", nodeClass.Spec.Image)
		return nil, fmt.Errorf("image resolution returned empty imageID")
	}

	// Create boot volume attachment based on block device mappings or use default
	bootVolumeAttachment, additionalVolumes, err := p.buildVolumeAttachments(nodeClass, nodeClaim.Name, nodeClass.Spec.Zone)
	if err != nil {
		return nil, fmt.Errorf("building volume attachments: %w", err)
	}

	// Debug log the instance profile value before VPC instance creation
	logger.Info("DEBUG: Creating VPC instance with profile",
		"instanceProfile", instanceProfile,
		"instanceProfile-ptr", &instanceProfile,
		"instanceProfile-empty", instanceProfile == "",
		"selectedInstanceType", selectedInstanceType.Name,
		"selectedInstanceType-ptr", &selectedInstanceType.Name,
		"availableTypes", len(instanceTypes))

	sdkClient, ok := vpcClient.GetSDKClient().(*vpcv1.VpcV1)
	if !ok {
		return nil, fmt.Errorf("failed to get VPC SDK client for builder function")
	}

	// Use the SDK builder to create the prototype with required fields
	instancePrototype, err := sdkClient.NewInstancePrototypeInstanceByImageInstanceByImageInstanceByNetworkAttachment(
		&vpcv1.ImageIdentityByID{ID: &imageID},
		&vpcv1.ZoneIdentityByName{Name: &zone},
		primaryNetworkAttachment,
	)
	if err != nil {
		return nil, fmt.Errorf("creating instance prototype with SDK builder: %w", err)
	}

	// Set additional optional fields
	instancePrototype.VPC = &vpcv1.VPCIdentityByID{
		ID: &nodeClass.Spec.VPC,
	}
	instancePrototype.Name = &nodeClaim.Name
	instancePrototype.Profile = &vpcv1.InstanceProfileIdentityByName{
		Name: &instanceProfile,
	}
	instancePrototype.BootVolumeAttachment = bootVolumeAttachment
	instancePrototype.AvailabilityPolicy = &vpcv1.InstanceAvailabilityPolicyPrototype{
		HostFailure: &[]string{"restart"}[0],
	}

	// Add placement target if specified
	if nodeClass.Spec.PlacementTarget != "" {
		instancePrototype.PlacementTarget = &vpcv1.InstancePlacementTargetPrototype{
			ID: &nodeClass.Spec.PlacementTarget,
		}
	}

	// Add resource group if specified
	if nodeClass.Spec.ResourceGroup != "" {
		logger.Info("Starting instance resource group resolution",
			"resourceGroup", nodeClass.Spec.ResourceGroup,
			"instanceProfile", instanceProfile,
			"selectionType", "dynamic")

		resourceGroupID, rgErr := p.resolveResourceGroupID(ctx, nodeClass.Spec.ResourceGroup)
		if rgErr != nil {
			logger.Error(rgErr, "Instance resource group resolution failed",
				"resourceGroup", nodeClass.Spec.ResourceGroup,
				"instanceProfile", instanceProfile)
			return nil, fmt.Errorf("resolving resource group %s: %w", nodeClass.Spec.ResourceGroup, rgErr)
		}

		if resourceGroupID == "" {
			logger.Error(nil, "Instance resource group resolved to empty string",
				"resourceGroup", nodeClass.Spec.ResourceGroup,
				"instanceProfile", instanceProfile)
			return nil, fmt.Errorf("resource group %s resolved to empty ID", nodeClass.Spec.ResourceGroup)
		}

		instancePrototype.ResourceGroup = &vpcv1.ResourceGroupIdentityByID{
			ID: &resourceGroupID,
		}
		logger.Info("Instance resource group successfully set",
			"input", nodeClass.Spec.ResourceGroup,
			"resolved_id", resourceGroupID,
			"instanceProfile", instanceProfile,
			"instance_has_resource_group", instancePrototype.ResourceGroup != nil)
	}

	// Add SSH keys if specified
	if len(nodeClass.Spec.SSHKeys) > 0 {
		var sshKeys []vpcv1.KeyIdentityIntf
		for _, key := range nodeClass.Spec.SSHKeys {
			sshKeys = append(sshKeys, &vpcv1.KeyIdentityByID{ID: &key})
		}
		instancePrototype.Keys = sshKeys
	}

	// Generate bootstrap user data using the bootstrap provider with selected instance type
	userData, err := p.generateBootstrapUserDataWithType(ctx, nodeClass, types.NamespacedName{
		Name:      nodeClaim.Name,
		Namespace: nodeClaim.Namespace,
	}, instanceProfile)
	if err != nil {
		return nil, fmt.Errorf("generating bootstrap user data: %w", err)
	}

	// Set user data
	instancePrototype.UserData = &userData

	// Add additional volume attachments if specified
	if len(additionalVolumes) > 0 {
		instancePrototype.VolumeAttachments = additionalVolumes
	}

	// Enable metadata service for instance ID retrieval
	instancePrototype.MetadataService = &vpcv1.InstanceMetadataServicePrototype{
		Enabled:          &[]bool{true}[0],
		Protocol:         &[]string{"http"}[0],
		ResponseHopLimit: &[]int64{2}[0],
	}

	// Debug logging: COMPREHENSIVE struct validation
	logger.Info("COMPREHENSIVE VPC instance prototype validation",
		"instance_name", nodeClaim.Name,
		"instanceProfile", instanceProfile,
		"imageID", imageID,
		"zone", zone,
		"subnet", subnet,
		"vpc", nodeClass.Spec.VPC,
		"PlacementTarget", nodeClass.Spec.PlacementTarget,
		// Check all required and optional fields
		"hasImage", instancePrototype.Image != nil,
		"hasZone", instancePrototype.Zone != nil,
		"hasProfile", instancePrototype.Profile != nil,
		"hasPrimaryNetworkAttachment", instancePrototype.PrimaryNetworkAttachment != nil,
		"hasVPC", instancePrototype.VPC != nil,
		"hasBootVolumeAttachment", instancePrototype.BootVolumeAttachment != nil,
		"hasPlacementTarget", instancePrototype.PlacementTarget != nil,
		"hasName", instancePrototype.Name != nil,
		"hasAvailabilityPolicy", instancePrototype.AvailabilityPolicy != nil)

	// Pre-API call validation to prevent oneOf constraint errors
	if instancePrototype.Profile == nil {
		return nil, fmt.Errorf("CRITICAL: instance prototype Profile field is nil - this violates IBM VPC oneOf constraint requirements")
	}

	// Validate the profile name in the prototype
	if profileIdentity, ok := instancePrototype.Profile.(*vpcv1.InstanceProfileIdentityByName); ok {
		if profileIdentity.Name == nil || *profileIdentity.Name == "" {
			return nil, fmt.Errorf("CRITICAL: instance profile Name is nil or empty in prototype - this violates IBM VPC oneOf constraint requirements")
		}
		logger.Info("Profile validation passed for oneOf compliance",
			"profileName", *profileIdentity.Name,
			"profileType", fmt.Sprintf("%T", instancePrototype.Profile))
	} else {
		logger.Info("Profile field details for oneOf debugging",
			"profileType", fmt.Sprintf("%T", instancePrototype.Profile),
			"profileName", instanceProfile,
			"profilePtr", fmt.Sprintf("%p", instancePrototype.Profile))
	}

	// Create the instance
	logger.Info("Creating VPC instance",
		"instance_name", nodeClaim.Name,
		"instance_profile", instanceProfile,
		"zone", zone)

	// DETAILED REQUEST LOGGING: Log the full instance prototype details
	logger.Info("VPC CreateInstance request details",
		"prototype_type", fmt.Sprintf("%T", instancePrototype),
		"name", instancePrototype.Name,
		"image_id", instancePrototype.Image,
		"zone", instancePrototype.Zone,
		"profile", instancePrototype.Profile,
		"vpc", instancePrototype.VPC,
		"primary_network_attachment", instancePrototype.PrimaryNetworkAttachment != nil,
		"boot_volume_attachment", instancePrototype.BootVolumeAttachment != nil,
		"volume_attachments_count", len(instancePrototype.VolumeAttachments),
		"availability_policy", instancePrototype.AvailabilityPolicy != nil,
		"metadata_service", instancePrototype.MetadataService != nil,
		"placement_target", instancePrototype.PlacementTarget,
		"resource_group", instancePrototype.ResourceGroup,
		"user_data_length", len(*instancePrototype.UserData))

	// DEBUG: Marshal the entire instancePrototype to JSON to see exactly what will be sent to the API
	// This helps debug oneOf constraint violations
	prototypeJSON, jsonErr := json.MarshalIndent(instancePrototype, "", "  ")
	if jsonErr != nil {
		logger.Info("WARN: Failed to marshal instancePrototype for debugging", "error", jsonErr)
	} else {
		// Sanitize user data before logging (it contains sensitive bootstrap tokens)
		var prototypeMap map[string]interface{}
		if unmarshalErr := json.Unmarshal(prototypeJSON, &prototypeMap); unmarshalErr == nil {
			if prototypeMap["user_data"] != nil {
				prototypeMap["user_data"] = "[REDACTED - contains bootstrap token]"
			}
			sanitizedJSON, _ := json.MarshalIndent(prototypeMap, "", "  ")
			logger.Info("DEBUG: InstancePrototype JSON payload", "json", string(sanitizedJSON))
		} else {
			logger.Info("DEBUG: InstancePrototype JSON payload (raw)", "json", string(prototypeJSON))
		}
	}

	// Log volume attachments details for block device troubleshooting
	if len(instancePrototype.VolumeAttachments) > 0 {
		for i, va := range instancePrototype.VolumeAttachments {
			logger.Info("VolumeAttachment details",
				"index", i,
				"name", va.Name,
				"volume_type", fmt.Sprintf("%T", va.Volume),
				"delete_on_termination", va.DeleteVolumeOnInstanceDelete)

			// Log detailed volume fields for oneOf debugging
			if volumeByCapacity, ok := va.Volume.(*vpcv1.VolumeAttachmentPrototypeVolumeVolumePrototypeInstanceContextVolumePrototypeInstanceContextVolumeByCapacity); ok {
				logger.Info("Volume by capacity details",
					"index", i,
					"volume_name", volumeByCapacity.Name,
					"volume_capacity", volumeByCapacity.Capacity,
					"volume_profile", volumeByCapacity.Profile,
					"has_name", volumeByCapacity.Name != nil,
					"has_capacity", volumeByCapacity.Capacity != nil,
					"has_profile", volumeByCapacity.Profile != nil,
					"has_iops", volumeByCapacity.Iops != nil,
					"has_bandwidth", volumeByCapacity.Bandwidth != nil,
					"has_user_tags", volumeByCapacity.UserTags != nil,
					"has_encryption_key", volumeByCapacity.EncryptionKey != nil)
			}
		}
	}

	// Create the instance
	instance, err := vpcClient.CreateInstance(ctx, instancePrototype)
	if err != nil {
		// Check if this is a partial failure that might have created resources
		ibmErr := ibm.ParseError(err)

		// Track error metrics BEFORE existing logging
		if isTimeoutError(err) {
			metrics.TimeoutErrors.WithLabelValues("CreateInstance", nodeClass.Spec.Region).Inc()
			metrics.ErrorsByType.WithLabelValues("timeout", "instance_provider", nodeClass.Spec.Region).Inc()
		} else if isQuotaError(err) {
			metrics.ErrorsByType.WithLabelValues("quota_exceeded", "instance_provider", nodeClass.Spec.Region).Inc()
		} else if isAuthError(err) {
			metrics.ErrorsByType.WithLabelValues("authentication", "instance_provider", nodeClass.Spec.Region).Inc()
		} else if ibmErr.StatusCode >= 400 && ibmErr.StatusCode < 500 {
			metrics.ErrorsByType.WithLabelValues("client_error", "instance_provider", nodeClass.Spec.Region).Inc()
		} else if ibmErr.StatusCode >= 500 {
			metrics.ErrorsByType.WithLabelValues("server_error", "instance_provider", nodeClass.Spec.Region).Inc()
		} else {
			metrics.ErrorsByType.WithLabelValues("api_error", "instance_provider", nodeClass.Spec.Region).Inc()
		}

		// Enhanced error logging with FULL error details for oneOf debugging
		logger.Error(err, "VPC instance creation error - FULL ERROR MESSAGE",
			"status_code", ibmErr.StatusCode,
			"error_code", ibmErr.Code,
			"retryable", ibmErr.Retryable,
			"full_error_string", fmt.Sprintf("%s", err),
			"full_error_details", fmt.Sprintf("%+v", err),
			"error_type", fmt.Sprintf("%T", err),
			"instance_prototype_type", fmt.Sprintf("%T", instancePrototype),
			"ibm_error_message", ibmErr.Message,
			"ibm_error_more_info", ibmErr.MoreInfo,
			"raw_error", err.Error())

		// ENHANCED DEBUGGING: Try to extract full HTTP response for oneOf errors
		if detailedErr, ok := err.(*core.SDKProblem); ok {
			logger.Error(err, "IBM VPC SDK detailed error response",
				"instance_name", nodeClaim.Name,
				"sdk_problem", fmt.Sprintf("%+v", detailedErr),
				"problem_summary", detailedErr.Summary)
		}

		// Log specific oneOf error pattern analysis
		errorString := err.Error()
		isOneOfError := strings.Contains(errorString, "oneOf") || strings.Contains(errorString, "Expected only one")
		logger.Info("OneOf error analysis",
			"instance_name", nodeClaim.Name,
			"is_oneof_error", isOneOfError,
			"error_contains_oneof", strings.Contains(errorString, "oneOf"),
			"error_contains_expected_only_one", strings.Contains(errorString, "Expected only one"),
			"selected_instance_type", instanceProfile,
			"zone", zone,
			"nodeclass_instance_profile", nodeClass.Spec.InstanceProfile,
			"is_dynamic_selection", nodeClass.Spec.InstanceProfile == "")

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

		// Create detailed error message for better debugging in NodeClaim conditions
		detailedErr := fmt.Errorf("creating VPC instance failed: %s (code: %s, status: %d, instance_profile: %s, zone: %s, resolvedImageID: %s)",
			err.Error(), ibmErr.Code, ibmErr.StatusCode, instanceProfile, zone, imageID)

		// Still use HandleVPCError for consistent logging but return our detailed error
		_ = vpcclient.HandleVPCError(err, logger, "creating VPC instance",
			"instance_profile", instanceProfile, "zone", zone, "resolvedImageID", imageID)
		return nil, detailedErr
	}

	// DETAILED RESPONSE LOGGING: Log the full VPC response details
	logger.Info("VPC instance created successfully",
		"instance_id", *instance.ID,
		"name", *instance.Name,
		"status", instance.Status,
		"lifecycle_state", instance.LifecycleState,
		"zone", instance.Zone,
		"vpc", instance.VPC,
		"image", instance.Image,
		"profile", instance.Profile)

	// Log network attachment details
	if len(instance.NetworkAttachments) > 0 {
		for i, na := range instance.NetworkAttachments {
			logger.Info("Network attachment in response",
				"index", i,
				"attachment_id", na.ID,
				"attachment_type", fmt.Sprintf("%T", na))
		}
	}

	// Log volume attachment details in response
	if len(instance.VolumeAttachments) > 0 {
		for i, va := range instance.VolumeAttachments {
			logger.Info("Volume attachment in response",
				"index", i,
				"attachment_id", va.ID,
				"volume_id", va.Volume,
				"attachment_name", va.Name,
				"device_name", va.Device)
		}
	}

	// Verify network attachment was applied correctly
	if len(instance.NetworkAttachments) > 0 && instance.NetworkAttachments[0].ID != nil {
		logger.Info("Instance created with VNI network attachment", "attachment_id", *instance.NetworkAttachments[0].ID)
		// Note: Security groups information may not be available in the instance creation response
		// This would require a separate GetInstance call to verify security groups
	} else {
		logger.Info("Instance created but network attachment information not available in response")
	}

	// Create Node representation
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
			Labels: map[string]string{
				"karpenter.sh/managed":             "true",
				"karpenter-ibm.sh/vpc-id":          nodeClass.Spec.VPC,
				"karpenter-ibm.sh/zone":            zone,
				"karpenter-ibm.sh/region":          nodeClass.Spec.Region,
				"karpenter-ibm.sh/instance-type":   instanceProfile,
				"node.kubernetes.io/instance-type": instanceProfile,
				"topology.kubernetes.io/zone":      zone,
				"topology.kubernetes.io/region":    nodeClass.Spec.Region,
				"karpenter.sh/capacity-type":       "on-demand",
				"karpenter.sh/nodepool":            nodeClaim.Labels["karpenter.sh/nodepool"],
			},
		},
		Spec: corev1.NodeSpec{
			// Use the full instance ID including the zone prefix (e.g., 02u7_uuid)
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

	// Add Karpenter-specific tags to the instance for orphan cleanup identification
	if err := p.addKarpenterTags(ctx, vpcClient, *instance.ID, nodeClass, nodeClaim); err != nil {
		logger.Error(err, "failed to add Karpenter tags to instance, continuing anyway", "instance-id", *instance.ID)
		// Don't fail instance creation due to tagging issues
	}

	// Record successful provisioning metrics
	duration := time.Since(start).Seconds()
	metrics.ProvisioningDuration.WithLabelValues(instanceType, zone).Observe(duration)
	metrics.InstanceLifecycle.WithLabelValues("running", instanceType).Set(1)
	metrics.ApiRequests.WithLabelValues("CreateInstance", "200", nodeClass.Spec.Region).Inc()
	// Track quota utilization with actual data
	if quotaInfo, err := p.getQuotaInfo(ctx, nodeClass.Spec.Region); err == nil {
		metrics.QuotaUtilization.WithLabelValues("instances", nodeClass.Spec.Region).Set(quotaInfo.InstanceUtilization)
		metrics.QuotaUtilization.WithLabelValues("vCPU", nodeClass.Spec.Region).Set(quotaInfo.VCPUUtilization)
	} else {
		// Log the error but don't fail the instance creation
		logger.Info("Failed to get quota information", "error", err, "region", nodeClass.Spec.Region)
	}

	return node, nil
}

func (p *VPCInstanceProvider) getQuotaInfo(ctx context.Context, region string) (*QuotaInfo, error) {
	// Create authenticator
	authenticator := &core.IamAuthenticator{
		ApiKey: os.Getenv("IBMCLOUD_API_KEY"),
	}

	// Create Resource Manager service with authenticator in options
	resourceManagerServiceOptions := &resourcemanagerv2.ResourceManagerV2Options{
		Authenticator: authenticator,
	}

	resourceManagerService, err := resourcemanagerv2.NewResourceManagerV2(resourceManagerServiceOptions)
	if err != nil {
		metrics.ErrorsByType.WithLabelValues("service_init", "quota_provider", region).Inc()
		return nil, fmt.Errorf("failed to create resource manager service: %w", err)
	}

	// List quota definitions
	listQuotaDefinitionsOptions := resourceManagerService.NewListQuotaDefinitionsOptions()
	quotaDefinitionList, _, err := resourceManagerService.ListQuotaDefinitions(listQuotaDefinitionsOptions)
	if err != nil {
		if isTimeoutError(err) {
			metrics.TimeoutErrors.WithLabelValues("ListQuotaDefinitions", region).Inc()
			metrics.ErrorsByType.WithLabelValues("timeout", "quota_provider", region).Inc()
		} else if isAuthError(err) {
			metrics.ErrorsByType.WithLabelValues("authentication", "quota_provider", region).Inc()
		} else {
			metrics.ErrorsByType.WithLabelValues("api_error", "quota_provider", region).Inc()
		}
		return nil, fmt.Errorf("failed to list quota definitions: %w", err)
	}

	// Get current VPC usage
	currentInstances, currentVCPUs, err := p.getCurrentVPCUsage(ctx)
	if err != nil {
		metrics.ErrorsByType.WithLabelValues("vpc_usage", "quota_provider", region).Inc()
		return nil, fmt.Errorf("failed to get current VPC usage: %w", err)
	}

	quotaInfo := &QuotaInfo{
		InstanceUtilization: 0.0,
		VCPUUtilization:     0.0,
	}

	// Process quota definitions
	if quotaDefinitionList.Resources != nil {
		for _, quota := range quotaDefinitionList.Resources {
			if quota.Name == nil {
				continue
			}

			switch *quota.Name {
			case "vpc-instances", "instances":
				maxInstances := 100.0
				quotaInfo.InstanceUtilization = float64(currentInstances) / maxInstances
			case "vpc-vcpu", "vcpu":
				maxVCPUs := 500.0
				quotaInfo.VCPUUtilization = float64(currentVCPUs) / maxVCPUs
			}
		}
	}

	return quotaInfo, nil
}

func (p *VPCInstanceProvider) getCurrentVPCUsage(ctx context.Context) (int, int, error) {
	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return 0, 0, err
	}

	instances, err := vpcClient.ListInstances(ctx)
	if err != nil {
		return 0, 0, err
	}

	instanceCount := len(instances)
	vcpuCount := 0

	for _, instance := range instances {
		if instance.Vcpu != nil && instance.Vcpu.Count != nil {
			vcpuCount += int(*instance.Vcpu.Count)
		}
	}

	return instanceCount, vcpuCount, nil
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

	// Extract instance type from node labels for metrics
	instanceType := "unknown"
	region := "unknown"
	if node.Labels != nil {
		if r, exists := node.Labels["topology.kubernetes.io/region"]; exists {
			region = r
		}
		if it, exists := node.Labels["node.kubernetes.io/instance-type"]; exists {
			instanceType = it
		}
	}

	// First attempt to delete the instance
	err = vpcClient.DeleteInstance(ctx, instanceID)
	if err != nil && !isIBMInstanceNotFoundError(err) {
		// Track specific error types
		if isTimeoutError(err) {
			metrics.TimeoutErrors.WithLabelValues("DeleteInstance", region).Inc()
			metrics.ErrorsByType.WithLabelValues("timeout", "instance_provider", region).Inc()
		} else if isAuthError(err) {
			metrics.ErrorsByType.WithLabelValues("authentication", "instance_provider", region).Inc()
		} else {
			metrics.ErrorsByType.WithLabelValues("api_error", "instance_provider", region).Inc()
		}

		metrics.ApiRequests.WithLabelValues("DeleteInstance", "500", region).Inc()
		return vpcclient.HandleVPCError(err, logger, "deleting VPC instance", "instance_id", instanceID)
	}

	// Check if the instance actually exists to confirm deletion status
	// This is critical for proper Karpenter finalizer management
	_, getErr := vpcClient.GetInstance(ctx, instanceID)
	if isIBMInstanceNotFoundError(getErr) {
		logger.Info("VPC instance confirmed deleted", "instance_id", instanceID)
		metrics.ApiRequests.WithLabelValues("DeleteInstance", "200", region).Inc()
		metrics.InstanceLifecycle.WithLabelValues("terminated", instanceType).Set(0)
		return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance %s not found", instanceID))
	}
	if getErr != nil {
		// If we can't determine instance status due to API error, assume deletion in progress
		logger.Info("Unable to verify instance status, assuming deletion in progress", "instance_id", instanceID, "error", getErr)
		metrics.ApiRequests.WithLabelValues("DeleteInstance", "200", region).Inc()
		metrics.InstanceLifecycle.WithLabelValues("terminated", instanceType).Set(0)
		return nil
	}

	// Instance still exists, deletion was triggered but is in progress
	logger.Info("VPC instance deletion triggered, still in progress", "instance_id", instanceID)
	metrics.ApiRequests.WithLabelValues("DeleteInstance", "200", region).Inc()
	metrics.InstanceLifecycle.WithLabelValues("terminated", instanceType).Set(0)
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
	// Instance ID includes zone prefix (e.g., 02u7_uuid)
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
// buildVolumeAttachments creates volume attachments based on block device mappings or uses defaults
func (p *VPCInstanceProvider) buildVolumeAttachments(nodeClass *v1alpha1.IBMNodeClass, instanceName, zone string) (*vpcv1.VolumeAttachmentPrototypeInstanceByImageContext, []vpcv1.VolumeAttachmentPrototype, error) {
	// If no block device mappings specified, use default configuration
	if len(nodeClass.Spec.BlockDeviceMappings) == 0 {
		// Default boot volume: 100GB general-purpose
		defaultBootVolume := &vpcv1.VolumeAttachmentPrototypeInstanceByImageContext{
			Volume: &vpcv1.VolumePrototypeInstanceByImageContext{
				Name: &[]string{fmt.Sprintf("%s-boot", instanceName)}[0],
				Profile: &vpcv1.VolumeProfileIdentityByName{
					Name: &[]string{"general-purpose"}[0],
				},
				Capacity: &[]int64{100}[0],
			},
			DeleteVolumeOnInstanceDelete: &[]bool{true}[0],
		}
		return defaultBootVolume, nil, nil
	}

	// Process block device mappings
	var bootVolumeAttachment *vpcv1.VolumeAttachmentPrototypeInstanceByImageContext
	var additionalVolumes []vpcv1.VolumeAttachmentPrototype

	for _, mapping := range nodeClass.Spec.BlockDeviceMappings {
		if mapping.RootVolume {
			// Build boot volume from mapping
			bootVolume := &vpcv1.VolumePrototypeInstanceByImageContext{
				Name: &[]string{fmt.Sprintf("%s-boot", instanceName)}[0],
			}

			// Set volume spec if provided
			if mapping.VolumeSpec != nil {
				// Set capacity if specified
				if mapping.VolumeSpec.Capacity != nil {
					bootVolume.Capacity = mapping.VolumeSpec.Capacity
				} else {
					// Default to 100GB if not specified
					bootVolume.Capacity = &[]int64{100}[0]
				}

				// Set profile if specified
				if mapping.VolumeSpec.Profile != nil {
					bootVolume.Profile = &vpcv1.VolumeProfileIdentityByName{
						Name: mapping.VolumeSpec.Profile,
					}
				} else {
					// Default to general-purpose
					bootVolume.Profile = &vpcv1.VolumeProfileIdentityByName{
						Name: &[]string{"general-purpose"}[0],
					}
				}

				// Set IOPS if specified (for custom profiles)
				if mapping.VolumeSpec.IOPS != nil {
					bootVolume.Iops = mapping.VolumeSpec.IOPS
				}

				// Set bandwidth if specified
				if mapping.VolumeSpec.Bandwidth != nil {
					bootVolume.Bandwidth = mapping.VolumeSpec.Bandwidth
				}

				// Set encryption key if specified
				if mapping.VolumeSpec.EncryptionKeyID != nil {
					bootVolume.EncryptionKey = &vpcv1.EncryptionKeyIdentityByCRN{
						CRN: mapping.VolumeSpec.EncryptionKeyID,
					}
				}

				// Set user tags if specified
				if len(mapping.VolumeSpec.Tags) > 0 {
					bootVolume.UserTags = mapping.VolumeSpec.Tags
				}
			} else {
				// Use defaults if no volume spec
				bootVolume.Capacity = &[]int64{100}[0]
				bootVolume.Profile = &vpcv1.VolumeProfileIdentityByName{
					Name: &[]string{"general-purpose"}[0],
				}
			}

			// Set delete on termination (default true)
			deleteOnTermination := true
			if mapping.VolumeSpec != nil && mapping.VolumeSpec.DeleteOnTermination != nil {
				deleteOnTermination = *mapping.VolumeSpec.DeleteOnTermination
			}

			bootVolumeAttachment = &vpcv1.VolumeAttachmentPrototypeInstanceByImageContext{
				Volume:                       bootVolume,
				DeleteVolumeOnInstanceDelete: &deleteOnTermination,
			}

			// Set device name if specified
			if mapping.DeviceName != nil {
				bootVolumeAttachment.Name = mapping.DeviceName
			}
		} else {
			// Build additional data volume
			if mapping.VolumeSpec == nil {
				continue // Skip if no volume spec for data volume
			}

			volumeName := fmt.Sprintf("%s-data-%d", instanceName, len(additionalVolumes))
			if mapping.DeviceName != nil {
				volumeName = *mapping.DeviceName
			}

			// Set delete on termination (default true)
			deleteOnTermination := true
			if mapping.VolumeSpec.DeleteOnTermination != nil {
				deleteOnTermination = *mapping.VolumeSpec.DeleteOnTermination
			}

			// Use concrete oneOf type for volume creation by capacity
			// This is required for proper JSON marshaling with discriminator
			volumeProto := &vpcv1.VolumeAttachmentPrototypeVolumeVolumePrototypeInstanceContextVolumePrototypeInstanceContextVolumeByCapacity{
				Name: &volumeName,
			}

			// Set capacity (required field for byCapacity variant)
			if mapping.VolumeSpec.Capacity != nil {
				volumeProto.Capacity = mapping.VolumeSpec.Capacity
			} else {
				// Default to 100GB for data volumes
				volumeProto.Capacity = &[]int64{100}[0]
			}

			// Set profile (required field)
			if mapping.VolumeSpec.Profile != nil {
				volumeProto.Profile = &vpcv1.VolumeProfileIdentityByName{
					Name: mapping.VolumeSpec.Profile,
				}
			} else {
				volumeProto.Profile = &vpcv1.VolumeProfileIdentityByName{
					Name: &[]string{"general-purpose"}[0],
				}
			}

			// Set optional fields
			if mapping.VolumeSpec.IOPS != nil {
				volumeProto.Iops = mapping.VolumeSpec.IOPS
			}
			if mapping.VolumeSpec.Bandwidth != nil {
				volumeProto.Bandwidth = mapping.VolumeSpec.Bandwidth
			}
			if mapping.VolumeSpec.EncryptionKeyID != nil {
				volumeProto.EncryptionKey = &vpcv1.EncryptionKeyIdentityByCRN{
					CRN: mapping.VolumeSpec.EncryptionKeyID,
				}
			}
			if len(mapping.VolumeSpec.Tags) > 0 {
				volumeProto.UserTags = mapping.VolumeSpec.Tags
			}

			// Create volume attachment - volumeProto implements VolumeAttachmentPrototypeVolumeIntf
			volumeAttachment := vpcv1.VolumeAttachmentPrototype{
				Name:                         &volumeName,
				Volume:                       volumeProto,
				DeleteVolumeOnInstanceDelete: &deleteOnTermination,
			}

			additionalVolumes = append(additionalVolumes, volumeAttachment)
		}
	}

	// If no root volume was specified in mappings, use default
	if bootVolumeAttachment == nil {
		bootVolumeAttachment = &vpcv1.VolumeAttachmentPrototypeInstanceByImageContext{
			Volume: &vpcv1.VolumePrototypeInstanceByImageContext{
				Name: &[]string{fmt.Sprintf("%s-boot", instanceName)}[0],
				Profile: &vpcv1.VolumeProfileIdentityByName{
					Name: &[]string{"general-purpose"}[0],
				},
				Capacity: &[]int64{100}[0],
			},
			DeleteVolumeOnInstanceDelete: &[]bool{true}[0],
		}
	}

	return bootVolumeAttachment, additionalVolumes, nil
}

func (p *VPCInstanceProvider) generateBootstrapUserData(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName) (string, error) {
	return p.generateBootstrapUserDataWithInstanceID(ctx, nodeClass, nodeClaim, "")
}

// generateBootstrapUserDataWithType generates bootstrap user data with the selected instance type
func (p *VPCInstanceProvider) generateBootstrapUserDataWithType(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName, selectedInstanceType string) (string, error) {
	return p.generateBootstrapUserDataWithInstanceIDAndType(ctx, nodeClass, nodeClaim, "", selectedInstanceType)
}

// generateBootstrapUserDataWithInstanceID generates bootstrap user data with a specific instance ID
func (p *VPCInstanceProvider) generateBootstrapUserDataWithInstanceID(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName, instanceID string) (string, error) {
	return p.generateBootstrapUserDataWithInstanceIDAndType(ctx, nodeClass, nodeClaim, instanceID, "")
}

// generateBootstrapUserDataWithInstanceIDAndType generates bootstrap user data with instance ID and type
func (p *VPCInstanceProvider) generateBootstrapUserDataWithInstanceIDAndType(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName, instanceID, selectedInstanceType string) (string, error) {
	logger := log.FromContext(ctx)

	// Use manual userData if provided
	if nodeClass.Spec.UserData != "" {
		logger.Info("Using manual userData from IBMNodeClass")
		// Inject BOOTSTRAP_* environment variables into custom userData
		return bootstrap.InjectBootstrapEnvVars(nodeClass.Spec.UserData), nil
	}

	// Initialize bootstrap provider if not already done
	if p.bootstrapProvider == nil {
		if p.k8sClient != nil {
			// Use properly injected kubernetes client
			p.bootstrapProvider = bootstrap.NewVPCBootstrapProvider(p.client, p.k8sClient, p.kubeClient)
		} else {
			// Create kubernetes client
			k8sClient, err := p.createKubernetesClient(ctx)
			if err != nil {
				return "", fmt.Errorf("failed to create kubernetes client: %w", err)
			}

			p.k8sClient = k8sClient
			p.bootstrapProvider = bootstrap.NewVPCBootstrapProvider(p.client, k8sClient, p.kubeClient)
		}
	}

	// Generate dynamic bootstrap script with instance ID and selected type
	logger.Info("Generating dynamic bootstrap script with automatic cluster discovery",
		"instanceID", instanceID,
		"selectedInstanceType", selectedInstanceType)
	userData, err := p.bootstrapProvider.GetUserDataWithInstanceIDAndType(ctx, nodeClass, nodeClaim, instanceID, selectedInstanceType)
	if err != nil {
		return "", fmt.Errorf("failed to generate bootstrap user data: %w", err)
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

// resolveResourceGroupID resolves a resource group name or ID to a proper resource group ID
func (p *VPCInstanceProvider) resolveResourceGroupID(ctx context.Context, resourceGroupInput string) (string, error) {
	logger := log.FromContext(ctx)

	// If the input is already a UUID-like ID (32 hex characters), return it as-is
	if len(resourceGroupInput) == 32 && isHexString(resourceGroupInput) {
		logger.Info("Resource group input is already an ID", "resource_group_id", resourceGroupInput)
		return resourceGroupInput, nil
	}

	// Otherwise, treat it as a name and resolve to ID using IBM Platform Services
	logger.Info("Resolving resource group name to ID", "resource_group_name", resourceGroupInput)

	// Get resource groups from IBM Platform Services
	resourceGroupID, err := p.client.GetResourceGroupIDByName(ctx, resourceGroupInput)
	if err != nil {
		return "", fmt.Errorf("failed to resolve resource group name '%s' to ID: %w", resourceGroupInput, err)
	}

	logger.Info("Successfully resolved resource group name to ID",
		"resource_group_name", resourceGroupInput,
		"resource_group_id", resourceGroupID)

	return resourceGroupID, nil
}

// isHexString checks if a string contains only hexadecimal characters
func isHexString(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// Helper functions to classify errors
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "context deadline exceeded") ||
		strings.Contains(errStr, "i/o timeout")
}

func isQuotaError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "quota") ||
		strings.Contains(errStr, "limit exceeded") ||
		strings.Contains(errStr, "insufficient capacity")
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "unauthorized") ||
		strings.Contains(errStr, "forbidden") ||
		strings.Contains(errStr, "authentication failed") ||
		strings.Contains(errStr, "401") ||
		strings.Contains(errStr, "403")
}

// selectSubnetFromStatusList selects a subnet from the pre-selected list in status using round-robin
func (p *VPCInstanceProvider) selectSubnetFromStatusList(subnetIDs []string) string {
	if len(subnetIDs) == 0 {
		return ""
	}

	if len(subnetIDs) == 1 {
		return subnetIDs[0]
	}

	// Simple round-robin based on current time for stateless distribution
	index := int(time.Now().UnixNano()) % len(subnetIDs)
	return subnetIDs[index]
}

// selectSubnetFromMultiZoneList selects a subnet from a list using round-robin across zones
// to ensure balanced distribution when multiple subnets are available across zones
func (p *VPCInstanceProvider) selectSubnetFromMultiZoneList(subnets []subnet.SubnetInfo) subnet.SubnetInfo {
	if len(subnets) == 0 {
		// This should not happen as caller checks length, but return empty for safety
		return subnet.SubnetInfo{}
	}

	if len(subnets) == 1 {
		return subnets[0]
	}

	// Group subnets by zone
	zoneSubnets := make(map[string][]subnet.SubnetInfo)
	var zones []string
	for _, s := range subnets {
		if _, exists := zoneSubnets[s.Zone]; !exists {
			zones = append(zones, s.Zone)
		}
		zoneSubnets[s.Zone] = append(zoneSubnets[s.Zone], s)
	}

	// Simple round-robin based on current time for stateless distribution
	// This ensures different instances get distributed across zones
	zoneIndex := int(time.Now().UnixNano()) % len(zones)
	selectedZone := zones[zoneIndex]

	// Select the best subnet in the chosen zone (highest available IPs)
	zoneSubnetList := zoneSubnets[selectedZone]
	bestSubnet := zoneSubnetList[0]
	for _, s := range zoneSubnetList {
		if s.AvailableIPs > bestSubnet.AvailableIPs {
			bestSubnet = s
		}
	}

	return bestSubnet
}

// addKarpenterTags adds Karpenter-specific tags to an instance for identification during orphan cleanup
func (p *VPCInstanceProvider) addKarpenterTags(ctx context.Context, vpcClient *ibm.VPCClient, instanceID string, nodeClass *v1alpha1.IBMNodeClass, nodeClaim *karpv1.NodeClaim) error {
	logger := log.FromContext(ctx)

	// Get cluster identifier (use cluster name from environment or nodeclass)
	clusterName := os.Getenv("CLUSTER_NAME")
	if clusterName == "" {
		clusterName = "default"
	}

	// Create Karpenter-specific tags with cluster identifier
	karpenterTags := map[string]string{
		"karpenter.sh/managed":    "true",
		"karpenter.sh/cluster":    clusterName,
		"karpenter.sh/nodepool":   nodeClaim.Labels["karpenter.sh/nodepool"],
		"karpenter.sh/provider":   "ibm-cloud",
		"karpenter.sh/version":    "v0.3.69", // TODO: Get this from build info
		"karpenter.sh/node-claim": nodeClaim.Name,
		"managed-by":              fmt.Sprintf("karpenter-%s", clusterName),
	}

	// Add nodeclass-specific tags if available
	if nodeClass.Spec.Tags != nil {
		for key, value := range nodeClass.Spec.Tags {
			// Don't override Karpenter system tags
			if !strings.HasPrefix(key, "karpenter.sh/") && key != "managed-by" {
				karpenterTags[key] = value
			}
		}
	}

	logger.Info("Adding Karpenter tags to instance",
		"instance-id", instanceID,
		"cluster", clusterName,
		"nodepool", nodeClaim.Labels["karpenter.sh/nodepool"],
		"nodeclaim", nodeClaim.Name)

	// Add tags to the instance
	if err := vpcClient.UpdateInstanceTags(ctx, instanceID, karpenterTags); err != nil {
		return fmt.Errorf("updating instance tags: %w", err)
	}

	logger.V(1).Info("Successfully added Karpenter tags to instance", "instance-id", instanceID, "tags", len(karpenterTags))
	return nil
}
