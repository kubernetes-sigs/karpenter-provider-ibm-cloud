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

package instancetype

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1alpha1 "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
)

// ExtendedInstanceType adds fields needed for automatic placement
type ExtendedInstanceType struct {
	*cloudprovider.InstanceType
	Architecture string
	Price        float64
}

type IBMInstanceTypeProvider struct {
	client           *ibm.Client
	pricingProvider  pricing.Provider
	vpcClientManager *vpcclient.Manager
	zonesCache       map[string][]string // Cache zones by region
	zonesCacheTime   time.Time
}

func NewProvider(client *ibm.Client, pricingProvider pricing.Provider) Provider {
	return &IBMInstanceTypeProvider{
		client:           client,
		pricingProvider:  pricingProvider,
		vpcClientManager: vpcclient.NewManager(client, 30*time.Minute),
		zonesCache:       make(map[string][]string),
	}
}

// instanceTypeRanking holds data for ranking instance types
type instanceTypeRanking struct {
	instanceType *ExtendedInstanceType
	score        float64
}

// calculateInstanceTypeScore computes a ranking score for an instance type
// Lower scores are better (more cost-efficient)
func calculateInstanceTypeScore(instanceType *ExtendedInstanceType) float64 {
	cpuCount := float64(instanceType.Capacity.Cpu().Value())
	// Use Kubernetes resource API for proper unit conversion
	memoryGB := float64(instanceType.Capacity.Memory().ScaledValue(resource.Giga))
	hourlyPrice := instanceType.Price

	// Handle cases where pricing is unavailable
	if hourlyPrice <= 0 {
		// When price is unavailable, rank by resource efficiency (prefer smaller instances)
		// This ensures instances with no pricing data are still ranked reasonably
		return cpuCount + memoryGB
	}

	// Calculate cost efficiency score (price per CPU and GB of memory)
	cpuEfficiency := hourlyPrice / cpuCount
	memoryEfficiency := hourlyPrice / memoryGB

	// Combine scores with weights
	// We weight CPU and memory equally in this implementation
	return (cpuEfficiency + memoryEfficiency) / 2
}

// getArchitecture extracts the architecture from instance type requirements
func getArchitecture(it *cloudprovider.InstanceType) string {
	if req := it.Requirements.Get(corev1.LabelArchStable); req != nil {
		values := req.Values()
		if len(values) > 0 {
			return values[0]
		}
	}
	return "amd64" // default to amd64 if not specified
}

func (p *IBMInstanceTypeProvider) Get(ctx context.Context, name string, nodeClass *v1alpha1.IBMNodeClass) (*cloudprovider.InstanceType, error) {
	logger := log.FromContext(ctx)

	if p.client == nil {
		return nil, fmt.Errorf("IBM client not initialized")
	}

	var instanceType *cloudprovider.InstanceType
	var lastErr error

	// Use same retry logic as List
	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    10,
		Cap:      15 * time.Second,
	}

	retryErr := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		attemptStart := time.Now()

		// Get VPC client
		vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
		if err != nil {
			lastErr = fmt.Errorf("failed to get VPC client: %w", err)
			logger.V(1).Info("Failed to get VPC client, will retry",
				"error", err,
				"duration", time.Since(attemptStart))
			return false, nil // Retry
		}

		// List all profiles
		// Note: VPCClient wrapper doesn't support context, uses internal timeout
		profiles, response, err := vpcClient.ListInstanceProfiles(&vpcv1.ListInstanceProfilesOptions{})
		if err != nil {
			lastErr = fmt.Errorf("failed to list instance profiles: %w", err)

			statusCode := 0
			if response != nil && response.StatusCode != 0 {
				statusCode = response.StatusCode
			}

			if isRetryableError(err, statusCode) {
				logger.Info("Retryable error getting instance type",
					"name", name,
					"error", err,
					"status_code", statusCode,
					"duration", time.Since(attemptStart),
					"will_retry", true)
				return false, nil // Retry
			}

			logger.Error(err, "Non-retryable error getting instance type",
				"name", name,
				"status_code", statusCode,
				"duration", time.Since(attemptStart))
			return false, err // Don't retry
		}

		if profiles == nil || profiles.Profiles == nil {
			lastErr = fmt.Errorf("no instance profiles returned from VPC API")
			return false, lastErr // Don't retry for empty response
		}

		// Find the requested profile
		for _, profile := range profiles.Profiles {
			if profile.Name != nil && *profile.Name == name {
				it, err := p.convertVPCProfileToInstanceType(ctx, profile, nodeClass)
				if err != nil {
					lastErr = fmt.Errorf("failed to convert profile %s: %w", name, err)
					return false, lastErr // Don't retry conversion errors
				}
				instanceType = it
				logger.V(1).Info("Successfully retrieved instance type",
					"name", name,
					"duration", time.Since(attemptStart))
				return true, nil // Success
			}
		}

		lastErr = fmt.Errorf("instance profile %s not found in VPC API", name)
		return false, lastErr // Don't retry if not found
	})

	if retryErr != nil {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, retryErr
	}

	if instanceType == nil {
		return nil, fmt.Errorf("instance profile %s not found", name)
	}

	return instanceType, nil
}

func (p *IBMInstanceTypeProvider) List(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass) ([]*cloudprovider.InstanceType, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("Listing instance types")

	if p.client == nil {
		err := fmt.Errorf("IBM client not initialized")
		logger.Error(err, "Failed to list instance types")
		return nil, err
	}

	// Use VPC API - this is the only source of truth
	instanceTypes, err := p.listFromVPC(ctx, nodeClass)
	if err != nil {
		logger.Error(err, "Failed to list instance types from VPC API")
		return nil, fmt.Errorf("failed to list instance types from VPC API: %w", err)
	}

	if len(instanceTypes) == 0 {
		err := fmt.Errorf("no instance types found from VPC API")
		logger.Error(err, "No instance types available")
		return nil, err
	}

	logger.Info("Successfully listed instance types", "count", len(instanceTypes))
	return instanceTypes, nil
}

func (p *IBMInstanceTypeProvider) Create(ctx context.Context, instanceType *cloudprovider.InstanceType) error {
	// Instance types are predefined in IBM Cloud, so this is a no-op
	return nil
}

func (p *IBMInstanceTypeProvider) Delete(ctx context.Context, instanceType *cloudprovider.InstanceType) error {
	// Instance types are predefined in IBM Cloud, so this is a no-op
	return nil
}

// FilterInstanceTypes returns instance types that meet requirements
func (p *IBMInstanceTypeProvider) FilterInstanceTypes(ctx context.Context, requirements *v1alpha1.InstanceTypeRequirements, nodeClass *v1alpha1.IBMNodeClass) ([]*cloudprovider.InstanceType, error) {
	if p.client == nil {
		return nil, fmt.Errorf("IBM client not initialized")
	}
	// Get all instance types
	allTypes, err := p.List(ctx, nodeClass)
	if err != nil {
		return nil, err
	}

	// Convert to extended instance types with pricing
	var extendedTypes []*ExtendedInstanceType
	for _, it := range allTypes {
		// Get price for this instance type (use first zone from client's region)
		if p.client == nil {
			return nil, fmt.Errorf("IBM client not initialized - cannot determine region for pricing")
		}

		region := p.client.GetRegion()
		zones, err := p.getZonesForRegion(ctx, region)
		if err != nil {
			return nil, fmt.Errorf("failed to get zones for region %s: %w", region, err)
		}
		if len(zones) == 0 {
			return nil, fmt.Errorf("no zones found for region %s", region)
		}

		// Use any available zone for pricing since IBM Cloud pricing is uniform across zones in a region
		// This avoids confusion in logs where zone might not match the NodeClass zone
		zone := zones[0] // Pricing is region-level, zone selection doesn't affect price

		price, err := p.pricingProvider.GetPrice(ctx, it.Name, zone)
		if err != nil {
			// Log warning but continue with 0 price to avoid breaking functionality
			// Note: This is just for pricing info, doesn't affect provisioning zone
			log.FromContext(ctx).V(1).Info("Could not get pricing for instance type, using fallback price",
				"instance_type", it.Name, "pricing_zone", zone, "error", err, "fallback_price", 0.0)
			price = 0.0
		}

		ext := &ExtendedInstanceType{
			InstanceType: it,
			Architecture: getArchitecture(it),
			Price:        price,
		}
		extendedTypes = append(extendedTypes, ext)
	}

	var filtered []*ExtendedInstanceType

	// Parse MaximumHourlyPrice if set
	var maxPrice float64
	if requirements.MaximumHourlyPrice != "" {
		var err error
		maxPrice, err = strconv.ParseFloat(requirements.MaximumHourlyPrice, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid MaximumHourlyPrice value: %w", err)
		}
	}

	for _, it := range extendedTypes {
		// Check architecture requirement
		if requirements.Architecture != "" && it.Architecture != requirements.Architecture {
			continue
		}

		// Check CPU requirement
		if requirements.MinimumCPU > 0 && it.Capacity.Cpu().Value() < int64(requirements.MinimumCPU) {
			continue
		}

		// Check memory requirement
		if requirements.MinimumMemory > 0 {
			memoryGB := float64(it.Capacity.Memory().Value()) / (1024 * 1024 * 1024)
			if memoryGB < float64(requirements.MinimumMemory) {
				continue
			}
		}

		// Check price requirement
		if requirements.MaximumHourlyPrice != "" && maxPrice > 0 && it.Price > maxPrice {
			continue
		}

		filtered = append(filtered, it)
	}

	// Rank the filtered instances by cost efficiency
	ranked := p.rankInstanceTypes(filtered)

	// Convert back to regular instance types
	result := make([]*cloudprovider.InstanceType, len(ranked))
	for i, r := range ranked {
		result[i] = r.InstanceType
	}

	return result, nil
}

// rankInstanceTypes sorts instance types by cost efficiency
func (p *IBMInstanceTypeProvider) rankInstanceTypes(instanceTypes []*ExtendedInstanceType) []*ExtendedInstanceType {
	// Create ranking slice
	rankings := make([]instanceTypeRanking, len(instanceTypes))
	for i, it := range instanceTypes {
		rankings[i] = instanceTypeRanking{
			instanceType: it,
			score:        calculateInstanceTypeScore(it),
		}
	}

	// Sort by score (lower is better)
	sort.Slice(rankings, func(i, j int) bool {
		return rankings[i].score < rankings[j].score
	})

	// Extract sorted instance types
	result := make([]*ExtendedInstanceType, len(rankings))
	for i, r := range rankings {
		result[i] = r.instanceType
	}

	return result
}

// RankInstanceTypes implements the Provider interface
func (p *IBMInstanceTypeProvider) RankInstanceTypes(instanceTypes []*cloudprovider.InstanceType) []*cloudprovider.InstanceType {
	// Convert to extended instance types with pricing
	extended := make([]*ExtendedInstanceType, len(instanceTypes))
	for i, it := range instanceTypes {
		// Get price for this instance type (use default zone for ranking)
		var price float64
		if p.pricingProvider != nil {
			// Get zone from client's region for pricing
			if p.client == nil {
				// Cannot get pricing without knowing the region
				price = 0.0
			} else {
				region := p.client.GetRegion()
				// IBM Cloud pricing is uniform across zones in a region, so any zone works for pricing
				zone := region + "-1" // Pricing is region-level, zone selection doesn't affect price
				priceVal, err := p.pricingProvider.GetPrice(context.Background(), it.Name, zone)
				if err != nil {
					// Log warning but continue with 0 price - use background context since we don't have the original
					log.Log.WithName("instancetype").V(1).Info("Could not get pricing for instance type ranking, using fallback price",
						"instance_type", it.Name, "pricing_zone", zone, "error", err, "fallback_price", 0.0)
					price = 0.0
				} else {
					price = priceVal
				}
			}
		} else {
			price = 0.0
		}

		extended[i] = &ExtendedInstanceType{
			InstanceType: it,
			Architecture: getArchitecture(it),
			Price:        price,
		}
	}

	// Rank the extended types
	ranked := p.rankInstanceTypes(extended)

	// Convert back to regular instance types
	result := make([]*cloudprovider.InstanceType, len(ranked))
	for i, r := range ranked {
		result[i] = r.InstanceType
	}

	return result
}

// listFromVPC lists instance types using VPC API with exponential backoff retry
func (p *IBMInstanceTypeProvider) listFromVPC(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass) ([]*cloudprovider.InstanceType, error) {
	logger := log.FromContext(ctx)

	var instanceTypes []*cloudprovider.InstanceType
	var lastErr error

	// Exponential backoff configuration
	backoff := wait.Backoff{
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
		Steps:    10, // Will retry up to 4 times
		Cap:      15 * time.Second,
	}

	retryErr := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		attemptStart := time.Now()

		vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
		if err != nil {
			lastErr = fmt.Errorf("getting VPC client: %w", err)
			logger.V(1).Info("Failed to get VPC client, will retry",
				"error", err,
				"duration", time.Since(attemptStart))
			return false, nil // Retry
		}

		// List instance profiles from VPC
		// Note: VPCClient wrapper doesn't support context, uses internal timeout
		options := &vpcv1.ListInstanceProfilesOptions{}
		result, response, err := vpcClient.ListInstanceProfiles(options)

		if err != nil {
			lastErr = fmt.Errorf("listing VPC instance profiles: %w", err)

			// Log detailed error information
			statusCode := 0
			if response != nil && response.StatusCode != 0 {
				statusCode = response.StatusCode
			}

			// Determine if error is retryable
			if isRetryableError(err, statusCode) {
				logger.Info("Retryable error listing instance types",
					"error", err,
					"status_code", statusCode,
					"duration", time.Since(attemptStart),
					"will_retry", true)
				return false, nil // Retry
			}

			// Non-retryable error
			logger.Error(err, "Non-retryable error listing instance types",
				"status_code", statusCode,
				"duration", time.Since(attemptStart))
			return false, err // Don't retry
		}

		// Success - process the results
		var types []*cloudprovider.InstanceType
		for _, profile := range result.Profiles {
			// Log each profile for debugging
			profileName := "<nil>"
			if profile.Name != nil {
				profileName = *profile.Name
			}

			instanceType, err := p.convertVPCProfileToInstanceType(ctx, profile, nodeClass)
			if err != nil {
				logger.Error(err, "Failed to convert VPC profile",
					"profile_name", profileName,
					"profile_details", fmt.Sprintf("%+v", profile))
				continue
			}

			// Validate the converted instance type has a valid name
			if instanceType.Name == "" {
				logger.Error(fmt.Errorf("converted instance type has empty name"),
					"Converted instance type validation failed",
					"original_profile_name", profileName,
					"converted_name", instanceType.Name)
				continue
			}

			types = append(types, instanceType)
		}

		instanceTypes = types
		logger.V(1).Info("Successfully listed instance types from VPC API",
			"count", len(instanceTypes),
			"duration", time.Since(attemptStart))
		return true, nil // Success, stop retrying
	})

	if retryErr != nil {
		if lastErr != nil {
			return nil, fmt.Errorf("failed after retries: %w", lastErr)
		}
		return nil, fmt.Errorf("failed to list instance types: %w", retryErr)
	}

	if len(instanceTypes) == 0 {
		return nil, fmt.Errorf("no instance types returned from VPC API")
	}

	return instanceTypes, nil
}

// isRetryableError determines if an error should trigger a retry
func isRetryableError(err error, statusCode int) bool {
	if err == nil {
		return false
	}

	// Check HTTP status codes
	switch statusCode {
	case 500, 502, 503, 504, 522, 524: // Server errors and timeouts
		return true
	case 429: // Rate limiting
		return true
	}

	// Check for network errors (timeout is the primary concern)
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	// Check for context errors
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Check error message for known retryable patterns
	errStr := strings.ToLower(err.Error())
	retryablePatterns := []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"temporary failure",
		"eof",
		"broken pipe",
		"no such host",
		"internal server error",
		"service unavailable",
		"gateway timeout",
		"bad gateway",
		"cloudflare", // Cloudflare errors
		"timeout",
		"deadline exceeded",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// getZonesForRegion fetches available zones for a region from VPC API
func (p *IBMInstanceTypeProvider) getZonesForRegion(ctx context.Context, region string) ([]string, error) {
	// Check cache first (cache for 1 hour)
	if zones, ok := p.zonesCache[region]; ok && time.Since(p.zonesCacheTime) < time.Hour {
		return zones, nil
	}

	// Get the SDK client directly for zone listing
	vpcClient, err := p.client.GetVPCClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get VPC client: %w", err)
	}

	// List zones for the region
	listOptions := &vpcv1.ListRegionZonesOptions{
		RegionName: &region,
	}

	// Use the SDK client directly as VPCClient wrapper doesn't have this method
	sdkClient := vpcClient.GetSDKClient()
	if sdkClient == nil {
		return nil, fmt.Errorf("VPC SDK client not available")
	}

	result, _, err := sdkClient.ListRegionZonesWithContext(ctx, listOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list zones for region %s: %w", region, err)
	}

	if result == nil || result.Zones == nil {
		return nil, fmt.Errorf("no zones found for region %s", region)
	}

	// Extract zone names
	var zones []string
	for _, zone := range result.Zones {
		if zone.Name != nil {
			zones = append(zones, *zone.Name)
		}
	}

	if len(zones) == 0 {
		return nil, fmt.Errorf("no valid zones found for region %s", region)
	}

	// Update cache
	p.zonesCache[region] = zones
	p.zonesCacheTime = time.Now()

	return zones, nil
}

// GetRegion returns the region this provider is configured for
func (p *IBMInstanceTypeProvider) GetRegion() string {
	if p.client != nil {
		return p.client.GetRegion()
	}
	return "unknown"
}

// convertVPCProfileToInstanceType converts VPC instance profile to Karpenter instance type
func (p *IBMInstanceTypeProvider) convertVPCProfileToInstanceType(ctx context.Context, profile vpcv1.InstanceProfile, nodeClass *v1alpha1.IBMNodeClass) (*cloudprovider.InstanceType, error) {
	if profile.Name == nil {
		return nil, fmt.Errorf("instance profile name is nil")
	}

	// Additional validation to ensure name is not empty
	if *profile.Name == "" {
		return nil, fmt.Errorf("instance profile has empty name")
	}

	var cpuCount int64
	if profile.VcpuCount != nil {
		if vcpuSpec, ok := profile.VcpuCount.(*vpcv1.InstanceProfileVcpu); ok && vcpuSpec.Value != nil {
			cpuCount = *vcpuSpec.Value
		} else {
			return nil, fmt.Errorf("instance profile %s has unsupported CPU count type", *profile.Name)
		}
	} else {
		return nil, fmt.Errorf("instance profile %s has no CPU count", *profile.Name)
	}

	var memoryGB int64
	if profile.Memory != nil {
		if memorySpec, ok := profile.Memory.(*vpcv1.InstanceProfileMemory); ok && memorySpec.Value != nil {
			memoryGB = *memorySpec.Value
		} else {
			return nil, fmt.Errorf("instance profile %s has unsupported memory type", *profile.Name)
		}
	} else {
		return nil, fmt.Errorf("instance profile %s has no memory", *profile.Name)
	}

	// Get architecture
	arch := "amd64" // Default
	if profile.VcpuArchitecture != nil && profile.VcpuArchitecture.Value != nil {
		arch = *profile.VcpuArchitecture.Value
	}

	// Get GPU count
	var gpuCount int64
	if profile.GpuCount != nil {
		if gpuSpec, ok := profile.GpuCount.(*vpcv1.InstanceProfileGpu); ok && gpuSpec.Value != nil {
			gpuCount = *gpuSpec.Value
		}
	}

	// Convert to Kubernetes resource quantities
	cpuResource := resource.NewQuantity(cpuCount, resource.DecimalSI)
	// Memory is in GB from the profile, convert to bytes using standard GB (not GiB)
	memoryResource := resource.NewQuantity(memoryGB*1000*1000*1000, resource.DecimalSI) // Convert GB to bytes
	gpuResource := resource.NewQuantity(gpuCount, resource.DecimalSI)

	// Calculate pod capacity (rough estimate: 110 pods per node for most instance types)
	var podCount int64 = 110
	if cpuCount <= 2 {
		podCount = 30
	} else if cpuCount <= 4 {
		podCount = 60
	}
	podResource := resource.NewQuantity(podCount, resource.DecimalSI)

	// Create requirements
	requirements := scheduling.NewRequirements(
		scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, *profile.Name),
		scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, arch),
		scheduling.NewRequirement("karpenter-ibm.sh/instance-family", corev1.NodeSelectorOpIn, getInstanceFamily(*profile.Name)),
		scheduling.NewRequirement("karpenter-ibm.sh/instance-size", corev1.NodeSelectorOpIn, getInstanceSize(*profile.Name)),
	)

	// Get zones dynamically for the current region
	if p.client == nil {
		return nil, fmt.Errorf("IBM client not initialized - cannot determine zones for instance offerings")
	}

	region := p.client.GetRegion()
	zones, err := p.getZonesForRegion(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("failed to get zones for region %s: %w", region, err)
	}
	if len(zones) == 0 {
		return nil, fmt.Errorf("no zones found for region %s", region)
	}

	// Create offerings with on-demand capacity in all supported zones
	offerings := cloudprovider.Offerings{
		{
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zones...),
				scheduling.NewRequirement("karpenter.sh/capacity-type", corev1.NodeSelectorOpIn, "on-demand"),
			),
			Price:     0.1, // Default price when pricing provider unavailable
			Available: true,
		},
	}

	// Calculate overhead from kubelet configuration
	overhead := p.calculateOverhead(ctx, nodeClass)

	return &cloudprovider.InstanceType{
		Name: *profile.Name,
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    *cpuResource,
			corev1.ResourceMemory: *memoryResource,
			corev1.ResourcePods:   *podResource,
			"nvidia.com/gpu":      *gpuResource, // Standard Kubernetes GPU resource name
		},
		Overhead:     overhead,
		Requirements: requirements,
		Offerings:    offerings,
	}, nil
}

func (p *IBMInstanceTypeProvider) calculateOverhead(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass) *cloudprovider.InstanceTypeOverhead {
	logger := log.FromContext(ctx)

	// Default values if kubelet config not specified
	kubeReservedCPU := resource.MustParse("100m")
	kubeReservedMemory := resource.MustParse("1Gi")
	systemReservedCPU := resource.MustParse("100m")
	systemReservedMemory := resource.MustParse("1Gi")
	evictionThreshold := resource.MustParse("500Mi")

	if nodeClass != nil && nodeClass.Spec.Kubelet != nil {
		if cpu, ok := nodeClass.Spec.Kubelet.KubeReserved["cpu"]; ok {
			if parsed, err := resource.ParseQuantity(cpu); err == nil {
				kubeReservedCPU = parsed
			} else {
				logger.Error(err, "Invalid kubeReserved.cpu quantity, using default",
					"value", cpu)
			}
		}
		if mem, ok := nodeClass.Spec.Kubelet.KubeReserved["memory"]; ok {
			if parsed, err := resource.ParseQuantity(mem); err == nil {
				kubeReservedMemory = parsed
			} else {
				logger.Error(err, "Invalid kubeReserved.memory quantity, using default",
					"value", mem)
			}
		}
		if cpu, ok := nodeClass.Spec.Kubelet.SystemReserved["cpu"]; ok {
			if parsed, err := resource.ParseQuantity(cpu); err == nil {
				systemReservedCPU = parsed
			} else {
				logger.Error(err, "Invalid systemReserved.cpu quantity, using default",
					"value", cpu)
			}
		}
		if mem, ok := nodeClass.Spec.Kubelet.SystemReserved["memory"]; ok {
			if parsed, err := resource.ParseQuantity(mem); err == nil {
				systemReservedMemory = parsed
			} else {
				logger.Error(err, "Invalid systemReserved.memory quantity, using default",
					"value", mem)
			}
		}
		if mem, ok := nodeClass.Spec.Kubelet.EvictionHard["memory.available"]; ok {
			if parsed, err := resource.ParseQuantity(mem); err == nil {
				evictionThreshold = parsed
			} else {
				logger.Error(err, "Invalid evictionHard.memory.available quantity, using default",
					"value", mem)
			}
		}
	}

	return &cloudprovider.InstanceTypeOverhead{
		KubeReserved: corev1.ResourceList{
			corev1.ResourceCPU:    kubeReservedCPU,
			corev1.ResourceMemory: kubeReservedMemory,
		},
		SystemReserved: corev1.ResourceList{
			corev1.ResourceCPU:    systemReservedCPU,
			corev1.ResourceMemory: systemReservedMemory,
		},
		EvictionThreshold: corev1.ResourceList{
			corev1.ResourceMemory: evictionThreshold,
		},
	}
}

// getInstanceFamily extracts family from instance type name (e.g., "bx2" from "bx2-2x8")
func getInstanceFamily(instanceType string) string {
	if len(instanceType) >= 3 {
		return instanceType[:3]
	}
	return "balanced"
}

// getInstanceSize extracts size from instance type name (e.g., "2x8" from "bx2-2x8")
func getInstanceSize(instanceType string) string {
	for i, c := range instanceType {
		if c == '-' && i+1 < len(instanceType) {
			return instanceType[i+1:]
		}
	}
	return "small"
}
