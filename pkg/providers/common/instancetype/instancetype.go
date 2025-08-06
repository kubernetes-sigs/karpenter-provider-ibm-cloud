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
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1alpha1 "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
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

func (p *IBMInstanceTypeProvider) Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error) {
	if p.client == nil {
		return nil, fmt.Errorf("IBM client not initialized")
	}

	// Get VPC client 
	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get VPC client: %w", err)
	}

	// List all profiles and find the one we want
	profiles, _, err := vpcClient.ListInstanceProfiles(&vpcv1.ListInstanceProfilesOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list instance profiles: %w", err)
	}

	if profiles == nil || profiles.Profiles == nil {
		return nil, fmt.Errorf("no instance profiles returned from VPC API")
	}

	for _, profile := range profiles.Profiles {
		if profile.Name != nil && *profile.Name == name {
			return p.convertVPCProfileToInstanceType(ctx, profile)
		}
	}

	return nil, fmt.Errorf("instance profile %s not found in VPC API", name)
}

func (p *IBMInstanceTypeProvider) List(ctx context.Context) ([]*cloudprovider.InstanceType, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("Listing instance types")

	if p.client == nil {
		err := fmt.Errorf("IBM client not initialized")
		logger.Error(err, "Failed to list instance types")
		return nil, err
	}

	// Use VPC API - this is the only source of truth
	instanceTypes, err := p.listFromVPC(ctx)
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
func (p *IBMInstanceTypeProvider) FilterInstanceTypes(ctx context.Context, requirements *v1alpha1.InstanceTypeRequirements) ([]*cloudprovider.InstanceType, error) {
	if p.client == nil {
		return nil, fmt.Errorf("IBM client not initialized")
	}
	// Get all instance types
	allTypes, err := p.List(ctx)
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
		
		zone := zones[0] // Use first available zone for pricing

		price, err := p.pricingProvider.GetPrice(ctx, it.Name, zone)
		if err != nil {
			// Log warning but continue with 0 price to avoid breaking functionality
			log.FromContext(ctx).Info("Could not get pricing for instance type, using fallback price",
				"instance_type", it.Name, "zone", zone, "error", err, "fallback_price", 0.0)
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
				zone := region + "-1" // Use first zone for pricing lookup
				priceVal, err := p.pricingProvider.GetPrice(context.Background(), it.Name, zone)
				if err != nil {
					// Log warning but continue with 0 price - use background context since we don't have the original
					log.Log.WithName("instancetype").Info("Could not get pricing for instance type ranking, using fallback price",
						"instance_type", it.Name, "zone", zone, "error", err, "fallback_price", 0.0)
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

// listFromVPC lists instance types using VPC API
func (p *IBMInstanceTypeProvider) listFromVPC(ctx context.Context) ([]*cloudprovider.InstanceType, error) {
	logger := log.FromContext(ctx)

	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return nil, err
	}

	// List instance profiles from VPC
	options := &vpcv1.ListInstanceProfilesOptions{}
	result, _, err := vpcClient.ListInstanceProfiles(options)
	if err != nil {
		return nil, fmt.Errorf("listing VPC instance profiles: %w", err)
	}

	var instanceTypes []*cloudprovider.InstanceType
	for _, profile := range result.Profiles {
		instanceType, err := p.convertVPCProfileToInstanceType(ctx, profile)
		if err != nil {
			logger.Error(err, "Failed to convert VPC profile", "profile", *profile.Name)
			continue
		}
		instanceTypes = append(instanceTypes, instanceType)
	}

	logger.V(1).Info("Listed instance types from VPC API", "count", len(instanceTypes))
	return instanceTypes, nil
}

// getZonesForRegion fetches available zones for a region from VPC API
func (p *IBMInstanceTypeProvider) getZonesForRegion(ctx context.Context, region string) ([]string, error) {
	// Check cache first (cache for 1 hour)
	if zones, ok := p.zonesCache[region]; ok && time.Since(p.zonesCacheTime) < time.Hour {
		return zones, nil
	}

	// Get the SDK client directly for zone listing
	vpcClient, err := p.client.GetVPCClient()
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

// convertVPCProfileToInstanceType converts VPC instance profile to Karpenter instance type
func (p *IBMInstanceTypeProvider) convertVPCProfileToInstanceType(ctx context.Context, profile vpcv1.InstanceProfile) (*cloudprovider.InstanceType, error) {
	if profile.Name == nil {
		return nil, fmt.Errorf("instance profile name is nil")
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
		scheduling.NewRequirement("karpenter.ibm.sh/instance-family", corev1.NodeSelectorOpIn, getInstanceFamily(*profile.Name)),
		scheduling.NewRequirement("karpenter.ibm.sh/instance-size", corev1.NodeSelectorOpIn, getInstanceSize(*profile.Name)),
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

	return &cloudprovider.InstanceType{
		Name: *profile.Name,
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    *cpuResource,
			corev1.ResourceMemory: *memoryResource,
			corev1.ResourcePods:   *podResource,
			"nvidia.com/gpu":      *gpuResource, // Standard Kubernetes GPU resource name
		},
		Overhead: &cloudprovider.InstanceTypeOverhead{
			KubeReserved: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			SystemReserved: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			EvictionThreshold: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("500Mi"),
			},
		},
		Requirements: requirements,
		Offerings:    offerings,
	}, nil
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

