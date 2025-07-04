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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/image"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/subnet"
)

// Controller reconciles an IBMNodeClass object to update its status
type Controller struct {
	kubeClient     client.Client
	ibmClient      *ibm.Client
	subnetProvider subnet.Provider
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client) (*Controller, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("kubeClient cannot be nil")
	}

	// Create IBM client for validation
	ibmClient, err := ibm.NewClient()
	if err != nil {
		return nil, fmt.Errorf("creating IBM client: %w", err)
	}

	// Create subnet provider for validation
	subnetProvider, err := subnet.NewProvider()
	if err != nil {
		return nil, fmt.Errorf("creating subnet provider: %w", err)
	}

	return &Controller{
		kubeClient:     kubeClient,
		ibmClient:      ibmClient,
		subnetProvider: subnetProvider,
	}, nil
}

// NewTestController constructs a controller instance for testing without requiring IBM Cloud credentials
func NewTestController(kubeClient client.Client) *Controller {
	return &Controller{
		kubeClient: kubeClient,
		// ibmClient and subnetProvider are nil for testing
		// The controller handles nil clients gracefully by skipping IBM Cloud validation
	}
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	nc := &v1alpha1.IBMNodeClass{}
	if err := c.kubeClient.Get(ctx, req.NamespacedName, nc); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Store original for patching
	patch := client.MergeFrom(nc.DeepCopy())

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
		
		if err := c.kubeClient.Status().Patch(ctx, nc, patch); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
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
	
	if err := c.kubeClient.Status().Patch(ctx, nc, patch); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
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
		logger.V(1).Info("IBM Cloud resource validation failed", "error", err)
		return fmt.Errorf("IBM Cloud resource validation failed: %w", err)
	}

	// Phase 4: Business logic validation
	if err := c.validateBusinessLogic(nc); err != nil {
		return fmt.Errorf("business logic validation failed: %w", err)
	}

	logger.V(1).Info("NodeClass validation succeeded")
	return nil
}

// validateRequiredFields checks that all required fields are present
func (c *Controller) validateRequiredFields(nc *v1alpha1.IBMNodeClass) error {
	var missingFields []string

	if strings.TrimSpace(nc.Spec.Region) == "" {
		missingFields = append(missingFields, "region")
	}
	if strings.TrimSpace(nc.Spec.Image) == "" {
		missingFields = append(missingFields, "image")
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
	validRegions := []string{"us-south", "us-east", "eu-gb", "eu-de", "jp-tok", "au-syd", "ca-tor", "br-sao"}
	isValidRegion := false
	for _, validRegion := range validRegions {
		if region == validRegion {
			isValidRegion = true
			break
		}
	}
	if !isValidRegion {
		return fmt.Errorf("invalid region: %s, must be one of: %s", region, strings.Join(validRegions, ", "))
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

// validateIBMCloudResources validates that IBM Cloud resources exist and are accessible
func (c *Controller) validateIBMCloudResources(ctx context.Context, nc *v1alpha1.IBMNodeClass) error {
	// Skip IBM Cloud validation if clients are not available (e.g., in unit tests)
	if c.ibmClient == nil || c.subnetProvider == nil {
		logger := log.FromContext(ctx).WithValues("nodeclass", nc.Name)
		logger.V(1).Info("Skipping IBM Cloud resource validation - clients not available")
		return nil
	}

	// Validate VPC exists and is accessible
	if err := c.validateVPC(ctx, nc.Spec.VPC); err != nil {
		return fmt.Errorf("VPC validation failed: %w", err)
	}

	// Validate subnet if specified
	if nc.Spec.Subnet != "" {
		if err := c.validateSubnet(ctx, nc.Spec.Subnet, nc.Spec.VPC); err != nil {
			return fmt.Errorf("subnet validation failed: %w", err)
		}
	} else {
		// If no specific subnet, validate that subnets are available in the VPC
		if err := c.validateSubnetsAvailable(ctx, nc.Spec.VPC, nc.Spec.Zone); err != nil {
			return fmt.Errorf("subnet availability validation failed: %w", err)
		}
	}

	// Validate image exists and is accessible
	if err := c.validateImage(ctx, nc.Spec.Image, nc.Spec.Region); err != nil {
		return fmt.Errorf("image validation failed: %w", err)
	}

	return nil
}

// validateBusinessLogic checks business rules and constraints
func (c *Controller) validateBusinessLogic(nc *v1alpha1.IBMNodeClass) error {
	// If both zone and subnet are specified, ensure they are compatible
	// For now, we skip zone-subnet compatibility checks since we validate 
	// actual subnet resources via API calls in validateIBMCloudResources
	// TODO: Add zone-subnet compatibility validation

	// Validate placement strategy if specified
	if nc.Spec.PlacementStrategy != nil {
		if err := c.validatePlacementStrategy(nc.Spec.PlacementStrategy); err != nil {
			return fmt.Errorf("placement strategy validation failed: %w", err)
		}
	}

	return nil
}

// validateVPC checks if the VPC exists and is accessible
func (c *Controller) validateVPC(ctx context.Context, vpcID string) error {
	vpcClient, err := c.ibmClient.GetVPCClient()
	if err != nil {
		return fmt.Errorf("getting VPC client: %w", err)
	}

	_, err = vpcClient.GetVPC(ctx, vpcID)
	if err != nil {
		return fmt.Errorf("VPC %s not found or not accessible: %w", vpcID, err)
	}

	return nil
}

// validateSubnet checks if the subnet exists and is in the correct VPC
func (c *Controller) validateSubnet(ctx context.Context, subnetID, vpcID string) error {
	subnetInfo, err := c.subnetProvider.GetSubnet(ctx, subnetID)
	if err != nil {
		return fmt.Errorf("subnet %s not found or not accessible: %w", subnetID, err)
	}

	// Additional validation can be added here
	// For example, checking if subnet has available IPs, correct state, etc.
	if subnetInfo.State != "available" {
		return fmt.Errorf("subnet %s is not in available state: %s", subnetID, subnetInfo.State)
	}

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

// validateImage checks if the image exists and is accessible
func (c *Controller) validateImage(ctx context.Context, imageIdentifier, region string) error {
	vpcClient, err := c.ibmClient.GetVPCClient()
	if err != nil {
		return fmt.Errorf("getting VPC client: %w", err)
	}

	// Use image resolver to handle both IDs and names
	imageResolver := image.NewResolver(vpcClient, region)
	_, err = imageResolver.ResolveImage(ctx, imageIdentifier)
	if err != nil {
		return fmt.Errorf("image %s not found or not accessible in region %s: %w", imageIdentifier, region, err)
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

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("nodeclass.status").
		For(&v1alpha1.IBMNodeClass{}).
		Complete(c)
}
