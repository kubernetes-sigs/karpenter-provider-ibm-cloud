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
package status

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/constants"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/image"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
)

const nodeClassStatusRefreshInterval = 24 * time.Hour

// Controller reconciles an IBMNodeClass object to update its status
type Controller struct {
	kubeClient       client.Client
	apiReader        client.Reader
	ibmClient        *ibm.Client
	subnetProvider   subnet.Provider
	cache            *cache.Cache
	vpcClientManager *vpcclient.Manager
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client, apiReader client.Reader) (*Controller, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("kubeClient cannot be nil")
	}
	if apiReader == nil {
		return nil, fmt.Errorf("apiReader cannot be nil")
	}

	// Create IBM client for validation
	ibmClient, err := ibm.NewClient()
	if err != nil {
		return nil, fmt.Errorf("creating IBM client: %w", err)
	}

	// Create subnet provider for validation
	subnetProvider := subnet.NewProvider(ibmClient)

	// Create cache for zone-subnet mappings
	zoneSubnetCache := cache.New(15 * time.Minute)

	return &Controller{
		kubeClient:       kubeClient,
		apiReader:        apiReader,
		ibmClient:        ibmClient,
		subnetProvider:   subnetProvider,
		cache:            zoneSubnetCache,
		vpcClientManager: vpcclient.NewManager(ibmClient, constants.DefaultVPCClientCacheTTL),
	}, nil
}

// NewTestController constructs a controller instance for testing without requiring IBM Cloud credentials
func NewTestController(kubeClient client.Client) *Controller {
	return &Controller{
		kubeClient: kubeClient,
		apiReader:  kubeClient, // Use the same client for testing
		// ibmClient and subnetProvider are nil for testing
		// The controller handles nil clients gracefully by skipping IBM Cloud validation
	}
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("nodeclass", req.NamespacedName)

	nc := &v1alpha1.IBMNodeClass{}

	// First try cache for performance
	err := c.kubeClient.Get(ctx, req.NamespacedName, nc)
	if err != nil && errors.IsNotFound(err) && c.apiReader != nil {
		// Cache miss - try direct API read to handle race conditions
		logger.V(2).Info("NodeClass not found in cache, trying direct API read")
		err = c.apiReader.Get(ctx, req.NamespacedName, nc)
		if err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
		logger.V(2).Info("NodeClass found via direct API read", "resourceVersion", nc.ResourceVersion)
	} else if err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Store original for patching
	stored := nc.DeepCopy()

	// Validate the nodeclass configuration
	if err := c.validateNodeClass(ctx, nc); err != nil {
		nc.Status.LastValidationTime = metav1.Now()
		nc.Status.ValidationError = err.Error()

		// Set Ready condition to False with validation error
		nc.Status.Conditions = []metav1.Condition{
			{
				Type:               "Ready",
				Status:             metav1.ConditionFalse,
				LastTransitionTime: metav1.Now(),
				Reason:             "ValidationFailed",
				Message:            err.Error(),
			},
		}

		if err := c.kubeClient.Status().Patch(ctx, nc, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
			if errors.IsConflict(err) {
				return reconcile.Result{Requeue: true}, nil
			}
			return reconcile.Result{}, err
		}
		return reconcile.Result{RequeueAfter: nodeClassStatusRefreshInterval}, nil
	}

	// Validation passed - clear any previous validation error and set Ready condition
	nc.Status.LastValidationTime = metav1.Now()
	nc.Status.ValidationError = ""

	// Set Ready condition to True
	nc.Status.Conditions = []metav1.Condition{
		{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             "Ready",
			Message:            "NodeClass is ready",
		},
	}

	if err := c.patchNodeClassStatus(ctx, nc, stored); err != nil {
		if errors.IsConflict(err) {
			return reconcile.Result{Requeue: true}, nil
		}
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: nodeClassStatusRefreshInterval}, nil
}

// validateNodeClass performs comprehensive validation of the IBMNodeClass configuration
func (c *Controller) validateNodeClass(ctx context.Context, nc *v1alpha1.IBMNodeClass) error {
	logger := log.FromContext(ctx).WithValues("nodeclass", nc.Name)

	// Phase 1: Basic field validation
	if err := c.validateRequiredFields(nc); err != nil {
		return fmt.Errorf("field validation failed: %w", err)
	}

	// Phase 2: Format validation
	if err := c.validateFieldFormats(nc); err != nil {
		return fmt.Errorf("format validation failed: %w", err)
	}

	// Phase 3: IBM Cloud resource validation
	if err := c.validateIBMCloudResources(ctx, nc); err != nil {
		logger.V(1).Info("Failing IBM Cloud resource validation", "error", err)
		return fmt.Errorf("IBM Cloud resource validation failed: %w", err)
	}

	// Phase 4: Business logic validation
	if err := c.validateBusinessLogic(ctx, nc); err != nil {
		return fmt.Errorf("business logic validation failed: %w", err)
	}

	logger.V(1).Info("Passing NodeClass validation")
	return nil
}

// validateRequiredFields checks that all required fields are present
func (c *Controller) validateRequiredFields(nc *v1alpha1.IBMNodeClass) error {
	var missingFields []string

	if strings.TrimSpace(nc.Spec.Region) == "" {
		missingFields = append(missingFields, "region")
	}
	// Either image or imageSelector must be specified (but not both - validated elsewhere)
	if strings.TrimSpace(nc.Spec.Image) == "" && nc.Spec.ImageSelector == nil {
		missingFields = append(missingFields, "image or imageSelector")
	}
	if strings.TrimSpace(nc.Spec.VPC) == "" {
		missingFields = append(missingFields, "vpc")
	}

	if len(missingFields) > 0 {
		return fmt.Errorf("required fields missing: %s", strings.Join(missingFields, ", "))
	}

	return nil
}

// validateFieldFormats checks that field values have correct formats
func (c *Controller) validateFieldFormats(nc *v1alpha1.IBMNodeClass) error {
	// Get trimmed values for validation
	vpcID := strings.TrimSpace(nc.Spec.VPC)
	subnetID := strings.TrimSpace(nc.Spec.Subnet)
	imageID := strings.TrimSpace(nc.Spec.Image)
	region := strings.TrimSpace(nc.Spec.Region)
	zone := strings.TrimSpace(nc.Spec.Zone)

	// Validate VPC ID format (IBM Cloud VPCs start with region code like "r006-")
	if vpcID != "" {
		// IBM Cloud VPC IDs have format: r<digits>-<uuid>
		// Example: r006-a8efb117-fd5e-4f63-ae16-4fb9faafa4ff
		if !strings.Contains(vpcID, "-") || len(vpcID) < 10 {
			return fmt.Errorf("VPC ID format invalid, expected format like 'r006-<uuid>', got: %s", vpcID)
		}
	}

	// Validate subnet ID format if specified (IBM Cloud subnets start with zone code)
	if subnetID != "" {
		// IBM Cloud subnet IDs have format: <zone>-<uuid>
		// Example: 0717-197e06f4-b500-426c-bc0f-900b215f996c
		if !strings.Contains(subnetID, "-") || len(subnetID) < 10 {
			return fmt.Errorf("subnet ID format invalid, expected format like '0717-<uuid>', got: %s", subnetID)
		}
	}

	// Validate image ID format (starts with "image-" or can be a name)
	if imageID != "" && strings.HasPrefix(imageID, "image-") {
		// This is an image ID - validate format
		if len(imageID) < 10 {
			return fmt.Errorf("image ID appears invalid: %s", imageID)
		}
	}

	// Validate region format
	if region != "" {
		if err := validateRegionFormat(region); err != nil {
			return err
		}
	}

	// Validate zone format if specified
	if zone != "" {
		expectedPrefix := region + "-"
		if !strings.HasPrefix(zone, expectedPrefix) {
			return fmt.Errorf("zone %s must start with region prefix %s", zone, expectedPrefix)
		}
	}

	return nil
}

// validateRegionFormat validates the basic format of a region string
func validateRegionFormat(region string) error {
	// IBM Cloud regions follow pattern: xx-xxxx (e.g., us-south, eu-de, jp-tok)
	// Basic format validation - must contain hyphen and be reasonable length
	if !strings.Contains(region, "-") || len(region) < 5 || len(region) > 10 {
		return fmt.Errorf("invalid region format: %s, expected format like 'us-south' or 'eu-de'", region)
	}

	// Check for valid characters (lowercase letters and hyphen only)
	for _, char := range region {
		if (char < 'a' || char > 'z') && char != '-' {
			return fmt.Errorf("invalid region format: %s, regions must contain only lowercase letters and hyphens", region)
		}
	}

	return nil
}

// validateIBMCloudResources validates that IBM Cloud resources exist and are accessible
func (c *Controller) validateIBMCloudResources(ctx context.Context, nc *v1alpha1.IBMNodeClass) error {
	// Skip IBM Cloud validation if clients are not available (e.g., in unit tests)
	if c.ibmClient == nil || c.subnetProvider == nil {
		logger := log.FromContext(ctx).WithValues("nodeclass", nc.Name)
		logger.V(1).Info("Skipping IBM Cloud resource validation - clients not available")
		return nil
	}

	// Validate region exists
	if err := c.validateRegion(ctx, nc.Spec.Region); err != nil {
		return fmt.Errorf("region validation failed: %w", err)
	}

	// Validate VPC exists and is accessible in the specified region
	if err := c.validateVPCInRegion(ctx, nc.Spec.VPC, nc.Spec.ResourceGroup, nc.Spec.Region); err != nil {
		return fmt.Errorf("VPC validation failed: %w", err)
	}

	// Validate subnet if specified
	if nc.Spec.Subnet != "" {
		if err := c.validateSubnet(ctx, nc.Spec.Subnet, nc.Spec.VPC, nc.Spec.Region); err != nil {
			return fmt.Errorf("subnet validation failed: %w", err)
		}
	} else {
		// If no specific subnet, validate that subnets are available in the VPC
		if err := c.validateSubnetsAvailable(ctx, nc.Spec.VPC, nc.Spec.Zone); err != nil {
			return fmt.Errorf("subnet availability validation failed: %w", err)
		}
	}

	// Validate image configuration - either explicit image or imageSelector
	if err := c.validateImageConfiguration(ctx, nc); err != nil {
		return fmt.Errorf("image validation failed: %w", err)
	}

	// Validate and resolve security groups
	if len(nc.Spec.SecurityGroups) > 0 {
		if err := c.validateSecurityGroups(ctx, nc.Spec.SecurityGroups, nc.Spec.VPC, nc.Spec.Region); err != nil {
			return fmt.Errorf("security group validation failed: %w", err)
		}
		nc.Status.ResolvedSecurityGroups = nc.Spec.SecurityGroups
	} else {
		// No explicit SGs - resolve default security group from VPC
		defaultSGID, err := c.resolveDefaultSecurityGroup(ctx, nc.Spec.VPC)
		if err != nil {
			return fmt.Errorf("failed to resolve default security group: %w", err)
		}
		nc.Status.ResolvedSecurityGroups = []string{defaultSGID}
	}

	// Validate SSH keys exist and are accessible
	if len(nc.Spec.SSHKeys) > 0 {
		if err := c.validateSSHKeys(ctx, nc.Spec.SSHKeys, nc.Spec.Region); err != nil {
			return fmt.Errorf("SSH key validation failed: %w", err)
		}
	}

	return nil
}

// resolveDefaultSecurityGroup gets the default security group ID for a VPC
func (c *Controller) resolveDefaultSecurityGroup(ctx context.Context, vpcID string) (string, error) {
	vpcClient, err := c.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return "", fmt.Errorf("getting VPC client: %w", err)
	}
	defaultSG, err := vpcClient.GetDefaultSecurityGroup(ctx, vpcID)
	if err != nil {
		return "", err
	}
	return *defaultSG.ID, nil
}

// validateRegion validates that the region exists and is accessible
func (c *Controller) validateRegion(ctx context.Context, region string) error {
	if region == "" {
		return fmt.Errorf("region is required")
	}

	// First validate format
	if err := validateRegionFormat(region); err != nil {
		return err
	}

	// Get VPC client to query regions
	vpcClient, err := c.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return fmt.Errorf("getting VPC client: %w", err)
	}

	// Get SDK client
	sdkClient := vpcClient.GetSDKClient()
	if sdkClient == nil {
		return fmt.Errorf("VPC SDK client not available")
	}

	// List all regions and check if our region exists
	regionsResult, _, err := sdkClient.ListRegions(&vpcv1.ListRegionsOptions{})
	if err != nil {
		return fmt.Errorf("listing regions from VPC API: %w", err)
	}

	if regionsResult == nil || regionsResult.Regions == nil {
		return fmt.Errorf("no regions found in VPC API")
	}

	// Check if the region exists
	for _, r := range regionsResult.Regions {
		if r.Name != nil && *r.Name == region {
			// Region found, also verify it's available
			if r.Status != nil && *r.Status != "available" {
				return fmt.Errorf("region %s exists but is not available (status: %s)", region, *r.Status)
			}
			return nil
		}
	}

	return fmt.Errorf("region %s not found in VPC API", region)
}

// validateBusinessLogic checks business rules and constraints that require external API calls
// Static validation (like mutual exclusivity) is handled by CRD validation
func (c *Controller) validateBusinessLogic(ctx context.Context, nc *v1alpha1.IBMNodeClass) error {
	// Note: instanceProfile/instanceRequirements validation is handled by CRD validation
	// Both fields are optional - when neither is specified, NodePool requirements control instance selection

	// Validate zone-subnet compatibility if both are specified
	if nc.Spec.Zone != "" && nc.Spec.Subnet != "" {
		if err := c.validateZoneSubnetCompatibility(ctx, nc.Spec.Zone, nc.Spec.Subnet); err != nil {
			return fmt.Errorf("zone-subnet compatibility validation failed: %w", err)
		}
	}

	// Validate placement strategy if specified
	if nc.Spec.PlacementStrategy != nil {
		if err := c.validatePlacementStrategy(nc.Spec.PlacementStrategy); err != nil {
			return fmt.Errorf("placement strategy validation failed: %w", err)
		}
	}

	return nil
}

// retryWithExponentialBackoff retries a function with exponential backoff
func retryWithExponentialBackoff(ctx context.Context, maxRetries int, baseDelay time.Duration, fn func() error) error {
	var err error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}

		// Don't retry on final attempt
		if attempt == maxRetries-1 {
			break
		}

		// Calculate delay with exponential backoff and jitter
		delay := time.Duration(float64(baseDelay) * math.Pow(2, float64(attempt)))
		if delay > 30*time.Second {
			delay = 30 * time.Second // Cap at 30 seconds
		}

		// Add jitter (±25%)
		jitter := time.Duration(float64(delay) * 0.25 * (2*math.Floor(math.Mod(float64(time.Now().UnixNano()), 2)) - 1))
		delay += jitter

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
			continue
		}
	}
	return err
}

// validateVPCInRegion validates that the VPC exists in the specified region
func (c *Controller) validateVPCInRegion(ctx context.Context, vpcID, resourceGroupID, region string) error {
	// Build region-specific VPC URL
	vpcURL := fmt.Sprintf("https://%s.iaas.cloud.ibm.com/v1", region)

	// Create a new region-specific VPC client following the same pattern as GetVPCClient
	// but with the correct regional endpoint
	regionVPCClient, err := c.createRegionVPCClient(ctx, vpcURL, region)
	if err != nil {
		return fmt.Errorf("creating region-specific VPC client for %s: %w", region, err)
	}

	// Validate VPC exists in this region with retry logic
	err = retryWithExponentialBackoff(ctx, 3, 1*time.Second, func() error {
		_, vpcErr := regionVPCClient.GetVPCWithResourceGroup(ctx, vpcID, resourceGroupID)
		return vpcErr
	})

	if err != nil {
		// Provide specific error messages for common issues
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("VPC %s does not exist in region %s. Please verify the VPC ID and ensure it exists in the correct region", vpcID, region)
		}
		if strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "unauthorized") {
			return fmt.Errorf("VPC %s exists but is not accessible in region %s. Please check IAM permissions and resource group access", vpcID, region)
		}
		if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "connection") {
			return fmt.Errorf("VPC validation failed due to network issues accessing VPC %s in region %s: %w", vpcID, region, err)
		}
		// Generic error fallback
		return fmt.Errorf("VPC %s validation failed in region %s: %w", vpcID, region, err)
	}

	return nil
}

// createRegionVPCClient creates a VPC client for a specific region
func (c *Controller) createRegionVPCClient(ctx context.Context, vpcURL, region string) (*ibm.VPCClient, error) {
	// If we don't have an IBM client, we can't create a VPC client
	if c.ibmClient == nil {
		return nil, fmt.Errorf("IBM client not available")
	}

	// Create a temporary client with the region-specific URL
	// This follows the same pattern as the main client but uses the specified region's endpoint
	vpcClient, err := c.ibmClient.GetVPCClient(ctx)
	if err != nil {
		// If we can't get a VPC client from the main client,
		// it likely means credentials are not available
		return nil, fmt.Errorf("getting VPC client: %w", err)
	}

	// Note: The current implementation returns the default VPC client
	// In a production environment, you would want to create a new VPC client
	// with the region-specific URL. For now, we'll use the existing client
	// but log a warning if the regions don't match
	currentRegion := c.ibmClient.GetRegion()
	if currentRegion != region {
		log.Log.V(1).Info("Warning: VPC validation using default region client",
			"default_region", currentRegion, "requested_region", region)
	}

	return vpcClient, nil
}

// validateVPC checks if the VPC exists and is accessible
func (c *Controller) validateVPC(ctx context.Context, vpcID, resourceGroupID string) error {
	vpcClient, err := c.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return err
	}

	// Retry VPC validation with exponential backoff to handle transient API failures
	err = retryWithExponentialBackoff(ctx, 3, 1*time.Second, func() error {
		_, vpcErr := vpcClient.GetVPCWithResourceGroup(ctx, vpcID, resourceGroupID)
		return vpcErr
	})

	if err != nil {
		// Provide more specific error messages for common issues
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("VPC %s does not exist in the specified region/resource group. Please verify the VPC ID and ensure it exists in the correct region", vpcID)
		}
		if strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "unauthorized") {
			return fmt.Errorf("VPC %s exists but is not accessible. Please check IAM permissions and resource group access", vpcID)
		}
		if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "connection") {
			return fmt.Errorf("VPC validation failed due to network issues accessing VPC %s: %w", vpcID, err)
		}
		// Generic error fallback
		return fmt.Errorf("VPC %s validation failed: %w", vpcID, err)
	}

	return nil
}

// validateSubnet checks if the subnet exists and is in the correct VPC
func (c *Controller) validateSubnet(ctx context.Context, subnetID, vpcID, expectedRegion string) error {
	subnetInfo, err := c.subnetProvider.GetSubnet(ctx, subnetID)
	if err != nil {
		return fmt.Errorf("subnet %s not found or not accessible: %w", subnetID, err)
	}

	// Extract region from subnet zone (e.g., "br-sao-1" -> "br-sao", "eu-de-2" -> "eu-de")
	subnetRegion := extractRegionFromZone(subnetInfo.Zone)
	if subnetRegion != expectedRegion {
		return fmt.Errorf("subnet %s is in region %s but NodeClass expects region %s (subnet zone: %s). Cross-region subnet references are not supported",
			subnetID, subnetRegion, expectedRegion, subnetInfo.Zone)
	}

	// Validate subnet state
	if subnetInfo.State != "available" {
		return fmt.Errorf("subnet %s is not in available state: %s", subnetID, subnetInfo.State)
	}

	// Validate sufficient available IPs
	if subnetInfo.AvailableIPs < 10 {
		return fmt.Errorf("subnet %s has insufficient available IPs (%d)", subnetID, subnetInfo.AvailableIPs)
	}

	return nil
}

// validateSubnetsAvailable checks if subnets are available in the VPC/zone
func (c *Controller) validateSubnetsAvailable(ctx context.Context, vpcID, zone string) error {
	subnets, err := c.subnetProvider.ListSubnets(ctx, vpcID)
	if err != nil {
		return fmt.Errorf("failed to list subnets in VPC %s: %w", vpcID, err)
	}

	availableSubnets := 0
	for _, subnet := range subnets {
		if subnet.State == "available" && subnet.AvailableIPs > 10 {
			if zone == "" || subnet.Zone == zone {
				availableSubnets++
			}
		}
	}

	if availableSubnets == 0 {
		if zone != "" {
			return fmt.Errorf("no available subnets found in VPC %s for zone %s", vpcID, zone)
		}
		return fmt.Errorf("no available subnets found in VPC %s", vpcID)
	}

	return nil
}

// validateZoneSubnetCompatibility validates that a subnet exists in the specified zone
func (c *Controller) validateZoneSubnetCompatibility(ctx context.Context, zone, subnetID string) error {
	// Skip validation if subnet provider is not available (testing scenario)
	if c.subnetProvider == nil {
		return nil
	}

	// Use cached subnet info if available
	cacheKey := fmt.Sprintf("subnet-zone-%s", subnetID)
	if cachedZone, found := c.cache.Get(cacheKey); found {
		if cachedZone.(string) != zone {
			return fmt.Errorf("subnet %s is in zone %s, but requested zone is %s",
				subnetID, cachedZone.(string), zone)
		}
		return nil
	}

	// Get subnet information
	subnetInfo, err := c.subnetProvider.GetSubnet(ctx, subnetID)
	if err != nil {
		return fmt.Errorf("failed to get subnet information: %w", err)
	}

	// Cache the zone information for future use
	c.cache.SetWithTTL(cacheKey, subnetInfo.Zone, 15*time.Minute)

	// Check if the subnet is in the requested zone
	if subnetInfo.Zone != zone {
		return fmt.Errorf("subnet %s is in zone %s, but requested zone is %s. Subnets cannot span multiple zones in IBM Cloud VPC",
			subnetID, subnetInfo.Zone, zone)
	}

	// Additional validation: ensure subnet is in available state
	if subnetInfo.State != "available" {
		return fmt.Errorf("subnet %s in zone %s is not in available state (current state: %s)",
			subnetID, zone, subnetInfo.State)
	}

	return nil
}

// validateImageConfiguration validates image configuration (either explicit image or imageSelector)
// and stores the resolved image ID in the NodeClass status for use during provisioning
func (c *Controller) validateImageConfiguration(ctx context.Context, nc *v1alpha1.IBMNodeClass) error {
	logger := log.FromContext(ctx)

	vpcClient, err := c.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return err
	}

	imageResolver := image.NewResolver(vpcClient, nc.Spec.Region, logger)

	// Validate explicit image if specified
	if nc.Spec.Image != "" {
		resolvedImageID, err := imageResolver.ResolveImage(ctx, nc.Spec.Image)
		if err != nil {
			return fmt.Errorf("image %s not found or not accessible in region %s: %w", nc.Spec.Image, nc.Spec.Region, err)
		}
		// Store resolved image ID in status for use during provisioning
		nc.Status.ResolvedImageID = resolvedImageID
		logger.V(1).Info("Resolving and caching explicit image",
			"image", nc.Spec.Image,
			"resolvedImageID", resolvedImageID)
		return nil
	}

	// Validate imageSelector if specified
	if nc.Spec.ImageSelector != nil {
		resolvedImageID, err := imageResolver.ResolveImageBySelector(ctx, nc.Spec.ImageSelector)
		if err != nil {
			return fmt.Errorf("no images found matching selector (os=%s, majorVersion=%s, minorVersion=%s, architecture=%s, variant=%s) in region %s: %w",
				nc.Spec.ImageSelector.OS,
				nc.Spec.ImageSelector.MajorVersion,
				nc.Spec.ImageSelector.MinorVersion,
				nc.Spec.ImageSelector.Architecture,
				nc.Spec.ImageSelector.Variant,
				nc.Spec.Region,
				err)
		}
		// Store resolved image ID in status for use during provisioning
		nc.Status.ResolvedImageID = resolvedImageID
		logger.Info("Resolved and cached image using selector",
			"os", nc.Spec.ImageSelector.OS,
			"majorVersion", nc.Spec.ImageSelector.MajorVersion,
			"minorVersion", nc.Spec.ImageSelector.MinorVersion,
			"architecture", nc.Spec.ImageSelector.Architecture,
			"variant", nc.Spec.ImageSelector.Variant,
			"resolvedImageID", resolvedImageID)
		return nil
	}

	// This should not happen due to CRD validation, but handle gracefully
	return fmt.Errorf("either image or imageSelector must be specified")
}

// validateImage checks if the image exists and is accessible (legacy method)
func (c *Controller) validateImage(ctx context.Context, imageIdentifier, region string) error {
	logger := log.FromContext(ctx)

	vpcClient, err := c.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return err
	}

	// Use image resolver to handle both IDs and names
	imageResolver := image.NewResolver(vpcClient, region, logger)
	_, err = imageResolver.ResolveImage(ctx, imageIdentifier)
	if err != nil {
		return fmt.Errorf("image %s not found or not accessible in region %s: %w", imageIdentifier, region, err)
	}

	return nil
}

// validateSecurityGroups checks if the security groups exist and are accessible in the VPC
func (c *Controller) validateSecurityGroups(ctx context.Context, securityGroupIDs []string, vpcID, region string) error {
	// Skip validation if VPC client manager is not available (testing scenario)
	if c.vpcClientManager == nil {
		return nil
	}

	vpcClient, err := c.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return fmt.Errorf("getting VPC client: %w", err)
	}

	sdkClient := vpcClient.GetSDKClient()
	if sdkClient == nil {
		return fmt.Errorf("VPC SDK client not available")
	}

	for _, sgID := range securityGroupIDs {
		sgID = strings.TrimSpace(sgID)
		if sgID == "" {
			continue
		}

		// Validate security group with retry logic
		err = retryWithExponentialBackoff(ctx, 3, 1*time.Second, func() error {
			_, _, sgErr := sdkClient.GetSecurityGroupWithContext(ctx, &vpcv1.GetSecurityGroupOptions{
				ID: &sgID,
			})
			return sgErr
		})

		if err != nil {
			// Provide specific error messages for common issues
			if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
				return fmt.Errorf("security group %s does not exist in region %s. Please verify the security group ID", sgID, region)
			}
			if strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "unauthorized") {
				return fmt.Errorf("security group %s exists but is not accessible in region %s. Please check IAM permissions", sgID, region)
			}
			if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "connection") {
				return fmt.Errorf("security group validation failed due to network issues accessing %s in region %s: %w", sgID, region, err)
			}
			return fmt.Errorf("security group %s validation failed in region %s: %w", sgID, region, err)
		}

		// Additional validation: check if security group is in the correct VPC
		sgDetails, _, err := sdkClient.GetSecurityGroupWithContext(ctx, &vpcv1.GetSecurityGroupOptions{
			ID: &sgID,
		})
		if err != nil {
			return fmt.Errorf("failed to get security group details for %s: %w", sgID, err)
		}

		if sgDetails.VPC != nil && sgDetails.VPC.ID != nil && *sgDetails.VPC.ID != vpcID {
			return fmt.Errorf("security group %s belongs to VPC %s but NodeClass specifies VPC %s. Security groups must be in the same VPC as instances", sgID, *sgDetails.VPC.ID, vpcID)
		}
	}

	return nil
}

// validateSSHKeys checks if the SSH keys exist and are accessible
func (c *Controller) validateSSHKeys(ctx context.Context, sshKeyIDs []string, region string) error {
	// Skip validation if VPC client manager is not available (testing scenario)
	if c.vpcClientManager == nil {
		return nil
	}

	vpcClient, err := c.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return fmt.Errorf("getting VPC client: %w", err)
	}

	sdkClient := vpcClient.GetSDKClient()
	if sdkClient == nil {
		return fmt.Errorf("VPC SDK client not available")
	}

	for _, keyID := range sshKeyIDs {
		keyID = strings.TrimSpace(keyID)
		if keyID == "" {
			continue
		}

		// Validate SSH key with retry logic
		err = retryWithExponentialBackoff(ctx, 3, 1*time.Second, func() error {
			_, _, keyErr := sdkClient.GetKeyWithContext(ctx, &vpcv1.GetKeyOptions{
				ID: &keyID,
			})
			return keyErr
		})

		if err != nil {
			// Provide specific error messages for common issues
			if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
				return fmt.Errorf("SSH key %s does not exist in region %s. Please verify the SSH key ID", keyID, region)
			}
			if strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "unauthorized") {
				return fmt.Errorf("SSH key %s exists but is not accessible in region %s. Please check IAM permissions", keyID, region)
			}
			if strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "connection") {
				return fmt.Errorf("SSH key validation failed due to network issues accessing %s in region %s: %w", keyID, region, err)
			}
			return fmt.Errorf("SSH key %s validation failed in region %s: %w", keyID, region, err)
		}
	}

	return nil
}

// validatePlacementStrategy validates the placement strategy configuration
func (c *Controller) validatePlacementStrategy(strategy *v1alpha1.PlacementStrategy) error {
	// If no strategy is specified, that's fine
	if strategy == nil {
		return nil
	}

	// Validate zone balance strategy
	validZoneBalances := []string{"Balanced", "AvailabilityFirst", "CostOptimized"}
	isValidZoneBalance := false
	for _, valid := range validZoneBalances {
		if strategy.ZoneBalance == valid {
			isValidZoneBalance = true
			break
		}
	}
	if !isValidZoneBalance {
		return fmt.Errorf("invalid ZoneBalance: %s, must be one of: %s",
			strategy.ZoneBalance, strings.Join(validZoneBalances, ", "))
	}

	// Validate subnet selection criteria if specified
	if strategy.SubnetSelection != nil {
		if strategy.SubnetSelection.MinimumAvailableIPs < 0 {
			return fmt.Errorf("MinimumAvailableIPs must be non-negative: %d", strategy.SubnetSelection.MinimumAvailableIPs)
		}
	}

	return nil
}

// patchNodeClassStatus patches the nodeclass status using optimistic locking
func (c *Controller) patchNodeClassStatus(ctx context.Context, nc *v1alpha1.IBMNodeClass, stored *v1alpha1.IBMNodeClass) error {
	return c.kubeClient.Status().Patch(ctx, nc, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{}))
}

// extractRegionFromZone extracts the region from an IBM Cloud zone name
// Examples: "br-sao-1" -> "br-sao", "eu-de-2" -> "eu-de", "us-south-3" -> "us-south"
func extractRegionFromZone(zone string) string {
	if zone == "" {
		return ""
	}

	// IBM Cloud zone format is typically "{region}-{zone-number}"
	// Find the last hyphen and take everything before it
	lastHyphen := strings.LastIndex(zone, "-")
	if lastHyphen == -1 {
		// If no hyphen found, return the zone as-is (shouldn't happen in normal cases)
		return zone
	}

	return zone[:lastHyphen]
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("nodeclass.status").
		For(&v1alpha1.IBMNodeClass{}).
		Complete(c)
}
