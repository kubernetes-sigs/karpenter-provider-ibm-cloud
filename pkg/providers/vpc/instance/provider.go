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
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
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
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/constants"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/httpclient"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/metrics"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/capacitytype"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/image"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/bootstrap"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/nodeclass"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
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
	bootstrapMu            sync.Mutex
	subnetProvider         subnet.Provider
	vpcClientManager       *vpcclient.Manager
	resourceManagerService *resourcemanagerv2.ResourceManagerV2
	instanceCache          *cache.Cache
	apiReader              client.Reader
	accountResolver        func(context.Context) (string, error)
	metricsMu              sync.Mutex
	metricsRegions         map[string]bool
	metricsSnapshots       map[string]map[string]float64
	metricsProfiles        map[string]bool
	metricsCtx             context.Context
	metricsStarted         bool
}

// Option configures the VPCInstanceProvider
type Option func(*VPCInstanceProvider) error

func WithAccountResolver(resolver func(context.Context) (string, error)) Option {
	return func(p *VPCInstanceProvider) error {
		if resolver == nil {
			return fmt.Errorf("account resolver cannot be nil")
		}
		p.accountResolver = resolver
		return nil
	}
}

func WithAPIReader(reader client.Reader) Option {
	return func(p *VPCInstanceProvider) error {
		if reader == nil {
			return fmt.Errorf("API reader cannot be nil")
		}
		p.apiReader = reader
		return nil
	}
}

// WithKubernetesClient sets the Kubernetes client for the provider
func WithKubernetesClient(k8sClient kubernetes.Interface) Option {
	return func(p *VPCInstanceProvider) error {
		if k8sClient == nil {
			return fmt.Errorf("kubernetes client cannot be nil when provided")
		}
		p.k8sClient = k8sClient

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

// WithInstanceCache sets a custom instance cache (for testing)
func WithInstanceCache(c *cache.Cache) Option {
	return func(p *VPCInstanceProvider) error {
		if c == nil {
			return fmt.Errorf("instance cache cannot be nil when provided")
		}
		p.instanceCache = c
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
		apiReader:              kubeClient,
		k8sClient:              nil, // Will be set via options if provided
		bootstrapProvider:      nil, // Will be lazily initialized or set via options
		subnetProvider:         subnet.NewProvider(client),
		vpcClientManager:       vpcclient.NewManager(client, constants.DefaultVPCClientCacheTTL),
		resourceManagerService: nil, // Will be initialized after applying options
		instanceCache:          cache.NewNamed("instances", constants.DefaultVPCClientCacheTTL),
	}

	// Apply options
	for _, opt := range opts {
		if err := opt(provider); err != nil {
			return nil, fmt.Errorf("applying option: %w", err)
		}
	}

	if provider.bootstrapProvider == nil && provider.k8sClient != nil {
		provider.bootstrapProvider = bootstrap.NewVPCBootstrapProvider(provider.client, provider.k8sClient, provider.reader())
	}

	// Create Resource Manager service if not provided via options
	if provider.resourceManagerService == nil {
		apiKey := os.Getenv("IBMCLOUD_API_KEY")
		if apiKey == "" {
			return nil, fmt.Errorf("IBMCLOUD_API_KEY environment variable is required")
		}

		authenticator := ibm.NewIAMAuthenticator(apiKey)
		resourceManagerServiceOptions := &resourcemanagerv2.ResourceManagerV2Options{
			Authenticator: authenticator,
		}
		resourceManagerService, err := resourcemanagerv2.NewResourceManagerV2(resourceManagerServiceOptions)
		if err != nil {
			return nil, fmt.Errorf("failed to create resource manager service: %w", err)
		}
		resourceManagerService.Service.SetHTTPClient(httpclient.BoundedHTTPClient(resourceManagerService.Service.GetHTTPClient(), "global"))
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
	if nodeClaim == nil || nodeClaim.Spec.NodeClassRef == nil || nodeClaim.UID == "" {
		return nil, fmt.Errorf("VPC launch requires a persisted NodeClaim")
	}
	reader := p.reader()
	freshClaim := &karpv1.NodeClaim{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(nodeClaim), freshClaim); err != nil {
		return nil, err
	}
	if freshClaim.UID != nodeClaim.UID || !freshClaim.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("NodeClaim changed or is terminating")
	}
	if value := freshClaim.Annotations[LaunchAnnotation]; value != "" {
		config, err := decodeLaunch(value)
		if err != nil {
			return nil, err
		}
		if config.Submitted && !config.Rejected {
			node, recoverErr := p.recoverLaunch(ctx, freshClaim, config)
			if !errors.Is(recoverErr, errLaunchUnresolved) || !config.abandoned(time.Now()) {
				return node, recoverErr
			}
		}
		// The checkpoint was bound to instance types chosen for an earlier attempt, so the
		// launch restarts from a fresh Create call rather than reusing them.
		if err := p.resetPreparedLaunch(ctx, freshClaim); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("discarded launch checkpoint %s that created no instance; retrying", config.Name)
	}

	// Start timing for provisioning duration
	start := time.Now()
	var instanceType string

	if p.kubeClient == nil {
		return nil, fmt.Errorf("kubernetes client not set")
	}

	// Get the NodeClass to extract configuration
	nodeClass := &v1alpha1.IBMNodeClass{}
	if getErr := reader.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); getErr != nil {
		return nil, fmt.Errorf("getting NodeClass %s: %w", nodeClaim.Spec.NodeClassRef.Name, getErr)
	}

	if nodeClass.UID == "" {
		return nil, fmt.Errorf("VPC launch requires a persisted NodeClass")
	}
	ready := false
	for _, condition := range nodeClass.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue && condition.ObservedGeneration == nodeClass.Generation {
			ready = true
		}
	}
	if !ready {
		return nil, fmt.Errorf("NodeClass generation %d has not been validated", nodeClass.Generation)
	}
	clusterUID, err := ownership.ClusterUID(ctx, reader)
	if err != nil {
		return nil, err
	}
	cloudName := ownership.InstanceName(clusterUID, string(nodeClaim.UID))
	resourceName := cloudName
	if len(resourceName) > 55 {
		resourceName = resourceName[:55]
	}
	vpcClient, err := p.clientForRegion(ctx, nodeClass.Spec.Region)
	if err != nil {
		metrics.ErrorsByType.WithLabelValues("vpc_client_error", "instance_provider", nodeClass.Spec.Region).Inc()
		return nil, err
	}

	// Select an instance type from the compatible types provided by Karpenter
	if len(instanceTypes) == 0 {
		return nil, fmt.Errorf("no compatible instance types provided for nodeclaim %s", nodeClaim.Name)
	}

	if nodeClass.Spec.InstanceProfile != "" {
		matches := false
		for _, profile := range instanceTypes {
			if profile != nil && profile.Name == nodeClass.Spec.InstanceProfile {
				matches = true
			}
		}
		if !matches {
			name := ""
			if instanceTypes[0] != nil {
				name = instanceTypes[0].Name
			}
			return nil, fmt.Errorf("selected instance profile %s differs from the current NodeClass profile %s", name, nodeClass.Spec.InstanceProfile)
		}
	}
	selectedInstanceType, selectedSubnet, err := p.selectPlacement(ctx, freshClaim, nodeClass, clusterUID, instanceTypes)
	if err != nil {
		return nil, err
	}
	instanceProfile := selectedInstanceType.Name
	instanceType = instanceProfile
	zone := selectedSubnet.Zone
	subnet := selectedSubnet.ID
	capacityType := capacitytype.ResolveCapacityType(freshClaim, []*cloudprovider.InstanceType{selectedInstanceType})
	capacityAvailable := false
	for _, offering := range selectedInstanceType.Offerings {
		if offering.Requirements.Get(karpv1.CapacityTypeLabelKey).Has(capacityType) {
			capacityAvailable = true
		}
	}
	if !capacityAvailable {
		if len(selectedInstanceType.Offerings) == 0 {
			return nil, fmt.Errorf("selected placement has no available capacity")
		}
		values := selectedInstanceType.Offerings[0].Requirements.Get(karpv1.CapacityTypeLabelKey).Values()
		if len(values) != 1 {
			return nil, fmt.Errorf("selected offering has ambiguous capacity type")
		}
		capacityType = values[0]
	}

	logger.Info("Initiated VPC instance creation with VNI", "instance_profile", instanceProfile, "zone", zone, "subnet", subnet)

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
		Name: &[]string{fmt.Sprintf("%s-vni", resourceName)}[0],
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
		logger.Info("Used account default for VNI as no resource group was specified")
	}

	var actualSecurityGroups []string

	// Add security groups if specified, otherwise use default
	if len(nodeClass.Spec.SecurityGroups) > 0 {
		var securityGroups []vpcv1.SecurityGroupIdentityIntf
		for _, sg := range nodeClass.Spec.SecurityGroups {
			securityGroups = append(securityGroups, &vpcv1.SecurityGroupIdentityByID{ID: &sg})
		}
		vniPrototype.SecurityGroups = securityGroups
		actualSecurityGroups = nodeClass.Spec.SecurityGroups
		logger.Info("Applied security groups to VNI", "security_groups", nodeClass.Spec.SecurityGroups, "count", len(securityGroups))
	} else {
		// Get default security group for VPC
		defaultSG, sgErr := vpcClient.GetDefaultSecurityGroup(ctx, nodeClass.Spec.VPC)
		if sgErr != nil {
			return nil, fmt.Errorf("getting default security group for VPC %s: %w", nodeClass.Spec.VPC, sgErr)
		}
		vniPrototype.SecurityGroups = []vpcv1.SecurityGroupIdentityIntf{
			&vpcv1.SecurityGroupIdentityByID{ID: defaultSG.ID},
		}
		actualSecurityGroups = []string{*defaultSG.ID}
		logger.Info("Used default security group for VNI", "security_group", *defaultSG.ID)
	}

	// Create primary network attachment with VNI
	// VPC resource names have a max length of 63 characters
	// The suffix "-primary" is 8 chars, leaving 55 chars for the instance name
	attachmentName := cloudName
	if len(attachmentName) > 55 {
		attachmentName = attachmentName[:55]
	}
	primaryNetworkAttachment := &vpcv1.InstanceNetworkAttachmentPrototype{
		Name:                    &[]string{fmt.Sprintf("%s-primary", attachmentName)}[0],
		VirtualNetworkInterface: vniPrototype,
	}

	// Resolve image identifier to image ID
	// First, try to use the cached resolved image from NodeClass status (populated by status controller)
	// This eliminates duplicate VPC API calls and ensures consistency
	var imageID string

	if nodeClass.Status.ResolvedImageID != "" && ready {
		// Use cached resolved image from status
		imageID = nodeClass.Status.ResolvedImageID
		logger.Info("Used cached resolved image from NodeClass status",
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
			logger.Info("Resolved explicit image inline", "image", nodeClass.Spec.Image)
			imageID, err = imageResolver.ResolveImage(ctx, nodeClass.Spec.Image)
			if err != nil {
				logger.Error(err, "Failed to resolve explicit image", "image", nodeClass.Spec.Image)
				return nil, fmt.Errorf("resolving image %s: %w", nodeClass.Spec.Image, err)
			}
			logger.Info("Successfully resolved explicit image inline", "image", nodeClass.Spec.Image, "imageID", imageID)
		} else if nodeClass.Spec.ImageSelector != nil {
			logger.Info("Resolved image using selector inline",
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
	bootVolumeAttachment, additionalVolumes, err := p.buildVolumeAttachments(nodeClass, resourceName, zone)
	if err != nil {
		return nil, fmt.Errorf("building volume attachments: %w", err)
	}
	if tagErr := tagVolumeAttachments(bootVolumeAttachment, additionalVolumes, ownership.VPCTags(clusterUID, string(nodeClaim.UID), string(nodeClass.UID))); tagErr != nil {
		return nil, tagErr
	}
	cloudTags, tagErr := instanceCloudTags(nodeClass, nodeClaim, clusterUID)
	if tagErr != nil {
		return nil, tagErr
	}

	// Debug log the instance profile value before VPC instance creation
	logger.V(1).Info("Logged VPC instance creation profile details",
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
	instancePrototype.Name = &cloudName
	instancePrototype.Profile = &vpcv1.InstanceProfileIdentityByName{
		Name: &instanceProfile,
	}
	instancePrototype.BootVolumeAttachment = bootVolumeAttachment

	switch capacityType {
	case karpv1.CapacityTypeSpot:
		instancePrototype.Availability = &vpcv1.InstanceAvailabilityPrototype{
			Class: &[]string{karpv1.CapacityTypeSpot}[0],
		}
		instancePrototype.AvailabilityPolicy = &vpcv1.InstanceAvailabilityPolicyPrototype{
			Preemption: &[]string{"stop"}[0],
		}
		instancePrototype.ReservationAffinity = &vpcv1.InstanceReservationAffinityPrototype{
			Policy: &[]string{"disabled"}[0],
		}
	case karpv1.CapacityTypeOnDemand:
		instancePrototype.AvailabilityPolicy = &vpcv1.InstanceAvailabilityPolicyPrototype{
			HostFailure: &[]string{"restart"}[0],
		}
	default:
		logger.Info("Unknown capacity type, defaulting to on-demand", "capacityType", capacityType)
		instancePrototype.AvailabilityPolicy = &vpcv1.InstanceAvailabilityPolicyPrototype{
			HostFailure: &[]string{"restart"}[0],
		}
	}

	// Add placement target if specified
	if nodeClass.Spec.PlacementTarget != "" {
		instancePrototype.PlacementTarget = &vpcv1.InstancePlacementTargetPrototype{
			ID: &nodeClass.Spec.PlacementTarget,
		}
	}

	resourceGroup := ""
	// Add resource group if specified
	if nodeClass.Spec.ResourceGroup != "" {
		logger.Info("Started instance resource group resolution",
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
		resourceGroup = resourceGroupID
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
	if validationErr := vpcClient.ValidateCreateInstance(instancePrototype); validationErr != nil {
		return nil, validationErr
	}

	// Debug logging: COMPREHENSIVE struct validation
	logger.Info("Validated VPC instance prototype",
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
		logger.Info("Logged profile field details for oneOf debugging",
			"profileType", fmt.Sprintf("%T", instancePrototype.Profile),
			"profileName", instanceProfile,
			"profilePtr", fmt.Sprintf("%p", instancePrototype.Profile))
	}

	hash, err := nodeclass.ProvisioningHash(nodeClass)
	if err != nil {
		return nil, err
	}
	accountID, err := p.resolveAccountID(ctx, vpcClient)
	if err != nil {
		return nil, err
	}
	config := &launchConfig{Name: cloudName, ClusterUID: clusterUID, ClaimUID: string(nodeClaim.UID), ClassUID: string(nodeClass.UID), AccountID: accountID, Region: nodeClass.Spec.Region, ResourceGroup: resourceGroup, VPC: nodeClass.Spec.VPC, Profile: instanceProfile, Zone: zone, Subnet: subnet, Image: imageID, SecurityGroups: actualSecurityGroups, CapacityType: capacityType, Hash: hash, HashVersion: v1alpha1.IBMNodeClassHashVersion, Capacity: selectedInstanceType.Capacity.DeepCopy(), Allocatable: selectedInstanceType.Allocatable().DeepCopy(), Tags: cloudTags}
	if checkpointErr := p.checkpointLaunch(ctx, freshClaim, config); checkpointErr != nil {
		return nil, checkpointErr
	}
	if adopted, lookupErr := p.findLaunch(ctx, vpcClient, config); lookupErr != nil {
		return nil, lookupErr
	} else if adopted != nil {
		return p.recoverLaunch(ctx, freshClaim, config)
	}
	config.Submitted = true
	config.SubmittedAt = time.Now().UTC()
	if checkpointErr := p.updateLaunch(ctx, freshClaim, config); checkpointErr != nil {
		return nil, checkpointErr
	}

	// Create the instance
	logger.Info("Initiated VPC instance creation",
		"instance_name", nodeClaim.Name,
		"instance_profile", instanceProfile,
		"zone", zone)

	// DETAILED REQUEST LOGGING: Log the full instance prototype details
	logger.Info("Logged VPC CreateInstance request details",
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
		logger.Info("Warning: Failed to marshal instancePrototype for debugging", "error", jsonErr)
	} else {
		// Sanitize user data before logging (it contains sensitive bootstrap tokens)
		var prototypeMap map[string]interface{}
		if unmarshalErr := json.Unmarshal(prototypeJSON, &prototypeMap); unmarshalErr == nil {
			if prototypeMap["user_data"] != nil {
				prototypeMap["user_data"] = "[REDACTED - contains bootstrap token]"
			}
			sanitizedJSON, _ := json.MarshalIndent(prototypeMap, "", "  ")
			logger.V(1).Info("Logged InstancePrototype JSON payload", "json", string(sanitizedJSON))
		} else {
			logger.V(1).Info("Logged InstancePrototype JSON payload (raw)", "json", string(prototypeJSON))
		}
	}

	// Log volume attachments details for block device troubleshooting
	if len(instancePrototype.VolumeAttachments) > 0 {
		for i, va := range instancePrototype.VolumeAttachments {
			logger.Info("Logged VolumeAttachment details",
				"index", i,
				"name", va.Name,
				"volume_type", fmt.Sprintf("%T", va.Volume),
				"delete_on_termination", va.DeleteVolumeOnInstanceDelete)

			// Log detailed volume fields for oneOf debugging
			if volumeByCapacity, ok := va.Volume.(*vpcv1.VolumeAttachmentPrototypeVolumeVolumePrototypeInstanceContextVolumePrototypeInstanceContextVolumeByCapacity); ok {
				logger.Info("Logged volume by capacity details",
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
		logger.Info("Analyzed OneOf error",
			"instance_name", nodeClaim.Name,
			"is_oneof_error", isOneOfError,
			"error_contains_oneof", strings.Contains(errorString, "oneOf"),
			"error_contains_expected_only_one", strings.Contains(errorString, "Expected only one"),
			"selected_instance_type", instanceProfile,
			"zone", zone,
			"nodeclass_instance_profile", nodeClass.Spec.InstanceProfile,
			"is_dynamic_selection", nodeClass.Spec.InstanceProfile == "")

		if ibm.IsCreateInstanceNotSent(err) || rejectedCreate(ibmErr) {
			if checkpointErr := p.markLaunchRejected(ctx, freshClaim, config); checkpointErr != nil {
				return nil, fmt.Errorf("recording rejected launch after %w: %v", err, checkpointErr)
			}
		}
		if recovered, recoverErr := p.recoverLaunch(ctx, freshClaim, config); recoverErr == nil {
			return recovered, nil
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
			logger.Info("Logged network attachment in response",
				"index", i,
				"attachment_id", na.ID,
				"attachment_type", fmt.Sprintf("%T", na))
		}
	}

	// Log volume attachment details in response
	if len(instance.VolumeAttachments) > 0 {
		for i, va := range instance.VolumeAttachments {
			logger.Info("Logged volume attachment in response",
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

	if instance == nil || instance.ID == nil {
		return nil, fmt.Errorf("create returned no instance identity")
	}
	if err := verifyInstanceAccount(instance, config.AccountID); err != nil {
		return nil, err
	}
	node := config.node(freshClaim, *instance.ID)
	if err := vpcClient.UpdateInstanceTags(ctx, *instance.ID, config.Tags); err != nil {
		return nil, fmt.Errorf("attaching instance ownership tags: %w", err)
	}

	// Record successful provisioning metrics
	duration := time.Since(start).Seconds()
	metrics.ProvisioningDuration.WithLabelValues(instanceType, zone).Observe(duration)
	p.scheduleInventoryMetrics(nodeClass.Spec.Region)

	return node, nil
}

func (p *VPCInstanceProvider) getQuotaInfo(ctx context.Context, region string) (*QuotaInfo, error) {
	// Create authenticator
	authenticator := ibm.NewIAMAuthenticator(os.Getenv("IBMCLOUD_API_KEY"))

	// Create Resource Manager service with authenticator in options
	resourceManagerServiceOptions := &resourcemanagerv2.ResourceManagerV2Options{
		Authenticator: authenticator,
	}

	resourceManagerService, err := resourcemanagerv2.NewResourceManagerV2(resourceManagerServiceOptions)
	if err != nil {
		metrics.ErrorsByType.WithLabelValues("service_init", "quota_provider", region).Inc()
		return nil, fmt.Errorf("failed to create resource manager service: %w", err)
	}
	resourceManagerService.Service.SetHTTPClient(httpclient.BoundedHTTPClient(resourceManagerService.Service.GetHTTPClient(), "global"))

	// List quota definitions
	listQuotaDefinitionsOptions := resourceManagerService.NewListQuotaDefinitionsOptions()
	quotaDefinitionList, _, err := resourceManagerService.ListQuotaDefinitionsWithContext(ctx, listQuotaDefinitionsOptions)
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
	currentInstances, currentVCPUs, err := p.getCurrentVPCUsage(ctx, region)
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

func (p *VPCInstanceProvider) getCurrentVPCUsage(ctx context.Context, regions ...string) (int, int, error) {
	region := ""
	if p.client != nil {
		region = p.client.GetRegion()
	}
	if len(regions) > 0 {
		region = regions[0]
	}
	vpcClient, err := p.clientForRegion(ctx, region)
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

	vpcClient, err := p.clientForRegion(ctx, providerRegion(node.Spec.ProviderID))
	if err != nil {
		return err
	}
	birthAccount := node.Annotations[ownership.AccountIDAnnotation]
	if value := node.Annotations[LaunchAnnotation]; value != "" {
		config, decodeErr := decodeLaunch(value)
		if decodeErr != nil {
			return decodeErr
		}
		birthAccount = config.AccountID
	}
	if targetErr := p.validateAccountTarget(ctx, vpcClient, birthAccount); targetErr != nil {
		return targetErr
	}
	claim := &karpv1.NodeClaim{ObjectMeta: *node.ObjectMeta.DeepCopy(), Status: karpv1.NodeClaimStatus{ProviderID: node.Spec.ProviderID}}
	var verified *vpcv1.Instance
	var verifyErr error
	if node.Annotations[LaunchAnnotation] == "" {
		verified, verifyErr = p.verifyLegacyInstance(ctx, vpcClient, claim, instanceID, birthAccount)
	} else {
		verified, verifyErr = p.verifyLaunchInstance(ctx, claim, true)
	}
	if verifyErr != nil {
		if cloudprovider.IsNodeClaimNotFoundError(verifyErr) {
			p.instanceCache.Delete(instanceID)
			p.scheduleInventoryMetrics(providerRegion(node.Spec.ProviderID))
		}
		return verifyErr
	}
	if verified.ID == nil || *verified.ID != instanceID {
		return fmt.Errorf("deletion target differs from verified launch")
	}

	logger.Info("Initiated VPC instance deletion", "instance_id", instanceID, "node", node.Name)

	region := providerRegion(node.Spec.ProviderID)

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

		return vpcclient.HandleVPCError(err, logger, "deleting VPC instance", "instance_id", instanceID)
	}

	p.instanceCache.Delete(instanceID)
	// Check if the instance actually exists to confirm deletion status
	// This is critical for proper Karpenter finalizer management
	_, getErr := vpcClient.GetInstance(ctx, instanceID)
	if isIBMInstanceNotFoundError(getErr) {
		logger.Info("VPC instance confirmed deleted", "instance_id", instanceID)
		p.scheduleInventoryMetrics(region)
		return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance %s not found", instanceID))
	}
	if getErr != nil {
		// If we can't determine instance status due to API error, assume deletion in progress
		logger.Info("Unable to verify instance status, assuming deletion in progress", "instance_id", instanceID, "error", getErr)
		p.scheduleInventoryMetrics(region)
		return fmt.Errorf("verifying instance deletion: %w", getErr)
	}

	// Instance still exists, deletion was triggered but is in progress
	logger.Info("VPC instance deletion triggered, still in progress", "instance_id", instanceID)
	p.scheduleInventoryMetrics(region)
	return nil
}

// Get retrieves information about a VPC instance
func (p *VPCInstanceProvider) Get(ctx context.Context, providerID string) (*corev1.Node, error) {
	return p.get(ctx, providerID, true)
}

func (p *VPCInstanceProvider) get(ctx context.Context, providerID string, useCache bool) (*corev1.Node, error) {
	instanceID := extractInstanceIDFromProviderID(providerID)
	if instanceID == "" {
		return nil, fmt.Errorf("could not extract instance ID from provider ID: %s", providerID)
	}

	if useCache {
		if cached, exists := p.instanceCache.Get(instanceID); exists {
			if node, ok := cached.(*corev1.Node); ok {
				return node.DeepCopy(), nil
			}
			p.instanceCache.Delete(instanceID)
		}
	}

	vpcClient, err := p.clientForRegion(ctx, providerRegion(providerID))
	if err != nil {
		return nil, err
	}
	accountID, err := p.resolveAccountID(ctx, vpcClient)
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
	if instance == nil || instance.ID == nil || *instance.ID != instanceID || instance.Name == nil {
		return nil, fmt.Errorf("instance response has unexpected identity")
	}
	if err := verifyInstanceAccount(instance, accountID); err != nil {
		return nil, err
	}

	// Determine capacity type from availability class
	availabilityClass := ""
	if instance.Availability != nil && instance.Availability.Class != nil {
		availabilityClass = *instance.Availability.Class
	}
	capacityType := capacitytype.GetCapacityTypeFromAvailabilityClass(ctx, availabilityClass)

	labels := map[string]string{
		karpv1.CapacityTypeLabelKey: capacityType,
	}
	if instance.Profile != nil && instance.Profile.Name != nil {
		labels[corev1.LabelInstanceTypeStable] = *instance.Profile.Name
	}
	if instance.Zone != nil && instance.Zone.Name != nil {
		labels[corev1.LabelTopologyZone] = *instance.Zone.Name
	}

	// Convert VPC instance to Node representation
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        *instance.Name,
			Labels:      labels,
			Annotations: map[string]string{ownership.AccountIDAnnotation: accountID},
		},
		Spec: corev1.NodeSpec{
			ProviderID: providerID,
		},
	}

	if instance.Status == nil || *instance.Status != vpcv1.InstanceStatusDeletingConst {
		p.instanceCache.Set(instanceID, node.DeepCopy())
	}

	return node, nil
}

func (p *VPCInstanceProvider) GetFresh(ctx context.Context, providerID string) (*corev1.Node, error) {
	return p.get(ctx, providerID, false)
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
		if instance.ID != nil && instance.Name != nil && instance.Zone != nil && instance.Zone.Name != nil {
			region := ibm.ExtractRegionFromZone(*instance.Zone.Name)
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: *instance.Name,
				},
				Spec: corev1.NodeSpec{
					ProviderID: fmt.Sprintf("ibm:///%s/%s", region, *instance.ID),
				},
			}
			if instance.Status == nil || *instance.Status != vpcv1.InstanceStatusDeletingConst {
				p.instanceCache.Set(*instance.ID, node.DeepCopy())
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

// isIBMInstanceNotFoundError checks if the error indicates an instance was not found in IBM Cloud VPC
func isIBMInstanceNotFoundError(err error) bool {
	return ibm.IsNotFound(err)
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

	roots := 0
	names := map[string]bool{}
	pattern := regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)
	for _, mapping := range nodeClass.Spec.BlockDeviceMappings {
		if mapping.DeviceName != nil {
			name := *mapping.DeviceName
			if len(name) > 63 || !pattern.MatchString(name) || names[name] {
				return nil, nil, fmt.Errorf("invalid or duplicate attachment name %q", name)
			}
			names[name] = true
		}
		if mapping.RootVolume {
			roots++
		}
	}
	if roots > 1 {
		return nil, nil, fmt.Errorf("at most one root volume mapping is allowed")
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

			if mapping.DeviceName != nil {
				volumeAttachment.Name = mapping.DeviceName
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
		logger.Info("Used manual userData from IBMNodeClass")
		// Inject BOOTSTRAP_* environment variables into custom userData
		return bootstrap.InjectBootstrapEnvVars(ctx, nodeClass.Spec.UserData), nil
	}

	// Initialize bootstrap provider if not already done (mutex allows retry on transient failure)
	p.bootstrapMu.Lock()
	if p.bootstrapProvider == nil {
		if p.k8sClient != nil {
			p.bootstrapProvider = bootstrap.NewVPCBootstrapProvider(p.client, p.k8sClient, p.reader())
		} else {
			k8sClient, err := p.createKubernetesClient(ctx)
			if err != nil {
				p.bootstrapMu.Unlock()
				return "", fmt.Errorf("failed to create kubernetes client: %w", err)
			}
			p.k8sClient = k8sClient
			p.bootstrapProvider = bootstrap.NewVPCBootstrapProvider(p.client, k8sClient, p.reader())
		}
	}
	p.bootstrapMu.Unlock()

	// Generate dynamic bootstrap script with instance ID and selected type
	logger.Info("Generated dynamic bootstrap script with automatic cluster discovery",
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
	logger.Info("Resolved resource group name to ID", "resource_group_name", resourceGroupInput)

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
	if s == "" {
		return false
	}
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

func instanceCloudTags(nodeClass *v1alpha1.IBMNodeClass, nodeClaim *karpv1.NodeClaim, clusterUID string) (map[string]string, error) {
	tags := ownership.VPCTags(clusterUID, string(nodeClaim.UID), string(nodeClass.UID))
	for key, value := range nodeClass.Spec.Tags {
		if !ownership.ReservedTag(key) {
			tags[key] = value
		}
	}
	if _, err := ownership.FormatTags(tags); err != nil {
		return nil, err
	}
	return tags, nil
}
