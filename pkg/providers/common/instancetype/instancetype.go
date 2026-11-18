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
	"math"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"golang.org/x/sync/singleflight"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1alpha1 "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	ibmcache "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/constants"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/operator/options"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/capacitytype"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
)

// ExtendedInstanceType adds fields needed for automatic placement
type ExtendedInstanceType struct {
	*cloudprovider.InstanceType
	Architecture string
	Price        float64
}

type IBMCloudClient interface {
	GetRegion() string
	GetVPCClient(ctx context.Context) (*ibm.VPCClient, error)
}

type IBMInstanceTypeProvider struct {
	client               IBMCloudClient
	pricingProvider      pricing.Provider
	vpcClientManager     *vpcclient.Manager
	zonesMu              sync.RWMutex
	zonesCache           map[string][]string // Cache zones by region
	zonesCacheTime       map[string]time.Time
	unavailableOfferings *ibmcache.UnavailableOfferings
	profilesMu           sync.Mutex
	profiles             map[string]profileSnapshot
	profileFlights       singleflight.Group
	lifecycle            context.Context
}

func NewProvider(client *ibm.Client, pricingProvider pricing.Provider, unavailableOfferings *ibmcache.UnavailableOfferings, contexts ...context.Context) Provider {
	lifecycle := context.Background()
	if len(contexts) > 0 {
		lifecycle = contexts[0]
	}
	return &IBMInstanceTypeProvider{
		client:               client,
		lifecycle:            lifecycle,
		pricingProvider:      pricingProvider,
		vpcClientManager:     vpcclient.NewManager(client, constants.DefaultVPCClientCacheTTL),
		zonesCache:           make(map[string][]string),
		zonesCacheTime:       make(map[string]time.Time),
		unavailableOfferings: unavailableOfferings,
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
	if p.client == nil {
		return nil, fmt.Errorf("IBM client not initialized")
	}
	profiles, err := p.rawProfiles(ctx, p.regionForClass(nodeClass))
	if err != nil {
		return nil, err
	}
	for _, profile := range profiles {
		if profile.Name != nil && *profile.Name == name {
			return p.convertVPCProfileToInstanceType(ctx, profile, nodeClass)
		}
	}
	return nil, fmt.Errorf("instance profile %s not found", name)
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
	if nodeClass != nil {
		if nodeClass.Spec.InstanceProfile != "" {
			for _, instanceType := range instanceTypes {
				if instanceType.Name == nodeClass.Spec.InstanceProfile {
					return []*cloudprovider.InstanceType{instanceType}, nil
				}
			}
			return nil, nil
		}
		if nodeClass.Spec.InstanceRequirements != nil {
			return p.filterCatalog(instanceTypes, nodeClass.Spec.InstanceRequirements)
		}
	}
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
	class := &v1alpha1.IBMNodeClass{}
	if nodeClass != nil {
		class = nodeClass.DeepCopy()
	}
	class.Spec.InstanceProfile = ""
	class.Spec.InstanceRequirements = requirements
	return p.List(ctx, class)
}

func (p *IBMInstanceTypeProvider) filterCatalog(instanceTypes []*cloudprovider.InstanceType, requirements *v1alpha1.InstanceTypeRequirements) ([]*cloudprovider.InstanceType, error) {
	var maxPrice float64
	priceLimited := requirements.MaximumHourlyPrice != ""
	if priceLimited {
		var err error
		maxPrice, err = strconv.ParseFloat(requirements.MaximumHourlyPrice, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid MaximumHourlyPrice value: %w", err)
		}
		if math.IsNaN(maxPrice) || math.IsInf(maxPrice, 0) || maxPrice < 0 {
			return nil, fmt.Errorf("MaximumHourlyPrice must be a finite nonnegative number")
		}
	}

	var filtered []*ExtendedInstanceType
	for _, it := range instanceTypes {
		if requirements.Architecture != "" && getArchitecture(it) != requirements.Architecture {
			continue
		}

		if requirements.MinimumCPU > 0 && it.Capacity.Cpu().Value() < int64(requirements.MinimumCPU) {
			continue
		}

		if requirements.MinimumMemory > 0 {
			memoryGB := float64(it.Capacity.Memory().Value()) / (1024 * 1024 * 1024)
			if memoryGB < float64(requirements.MinimumMemory) {
				continue
			}
		}

		if priceLimited {
			var offerings cloudprovider.Offerings
			for _, offering := range it.Offerings {
				if offering.Price >= 0 && offering.Price <= maxPrice {
					offerings = append(offerings, offering)
				}
			}
			if len(offerings) == 0 {
				continue
			}
			copy := it.DeepCopy()
			copy.Offerings = offerings
			it = copy
		}

		price := math.Inf(1)
		for _, offering := range it.Offerings {
			price = math.Min(price, offering.Price)
		}
		if math.IsInf(price, 1) {
			price = 0
		}
		filtered = append(filtered, &ExtendedInstanceType{InstanceType: it, Architecture: getArchitecture(it), Price: price})
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
		var price float64
		if available := it.Offerings.Available(); len(available) > 0 {
			price = available.Cheapest().Price
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
	profiles, err := p.rawProfiles(ctx, p.regionForClass(nodeClass))
	if err != nil {
		return nil, err
	}
	var result []*cloudprovider.InstanceType
	for _, profile := range profiles {
		it, err := p.convertVPCProfileToInstanceType(ctx, profile, nodeClass)
		if err != nil {
			log.FromContext(ctx).Error(err, "Skipping unsupported instance profile")
			continue
		}
		result = append(result, it)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no instance types returned from VPC API")
	}
	return result, nil
}

type profileSnapshot struct {
	profiles []vpcv1.InstanceProfile
	updated  time.Time
	// refreshErr is set while a previous catalog is served because the last refetch failed.
	refreshErr error
}

// staleProfileRetry is how long a previous catalog is served after a failed refetch.
const staleProfileRetry = 5 * time.Minute

func (p *IBMInstanceTypeProvider) regionForClass(nc *v1alpha1.IBMNodeClass) string {
	if nc != nil && nc.Spec.Region != "" {
		return nc.Spec.Region
	}
	if p.client != nil {
		return p.client.GetRegion()
	}
	return ""
}
func (p *IBMInstanceTypeProvider) regionalClient(ctx context.Context, region string) (*ibm.VPCClient, error) {
	var base *ibm.VPCClient
	var err error
	if p.vpcClientManager != nil {
		base, err = p.vpcClientManager.GetVPCClient(ctx)
	} else if p.client != nil {
		base, err = p.client.GetVPCClient(ctx)
	} else {
		return nil, fmt.Errorf("IBM client not initialized")
	}
	if err != nil {
		return nil, err
	}
	return base.ForRegion(region)
}
func (p *IBMInstanceTypeProvider) rawProfiles(ctx context.Context, region string) ([]vpcv1.InstanceProfile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.profilesMu.Lock()
	cached, ok := p.profiles[region]
	p.profilesMu.Unlock()
	if ok && time.Since(cached.updated) < time.Hour {
		return cached.profiles, nil
	}
	result := p.profileFlights.DoChan(region, func() (interface{}, error) {
		p.profilesMu.Lock()
		cached, ok := p.profiles[region]
		p.profilesMu.Unlock()
		if ok && time.Since(cached.updated) < time.Hour {
			return cached.profiles, nil
		}
		lifecycle := p.lifecycle
		if lifecycle == nil {
			lifecycle = context.WithoutCancel(ctx)
		}
		refreshCtx, cancel := context.WithTimeout(lifecycle, 2*time.Minute)
		defer cancel()
		var profiles []vpcv1.InstanceProfile
		err := wait.ExponentialBackoffWithContext(refreshCtx, wait.Backoff{Duration: time.Second, Factor: 2, Jitter: .1, Steps: 7, Cap: 15 * time.Second}, func(callCtx context.Context) (bool, error) {
			vpc, err := p.regionalClient(callCtx, region)
			if err != nil {
				return false, fmt.Errorf("listing VPC instance profiles: %w", err)
			}
			collection, response, err := vpc.ListInstanceProfiles(callCtx, &vpcv1.ListInstanceProfilesOptions{})
			if err != nil {
				code := 0
				if response != nil {
					code = response.StatusCode
				}
				if isRetryableError(err, code) {
					return false, nil
				}
				return false, fmt.Errorf("listing VPC instance profiles: %w", err)
			}
			if collection == nil || len(collection.Profiles) == 0 {
				return false, fmt.Errorf("no instance profiles returned from VPC API")
			}
			profiles = collection.Profiles
			return true, nil
		})
		if err != nil {
			if !ok {
				return nil, err
			}
			log.FromContext(ctx).Error(err, "Serving previous VPC instance profiles", "region", region)
			p.profilesMu.Lock()
			p.profiles[region] = profileSnapshot{profiles: cached.profiles, updated: time.Now().Add(staleProfileRetry - time.Hour), refreshErr: err}
			p.profilesMu.Unlock()
			return cached.profiles, nil
		}
		p.profilesMu.Lock()
		if p.profiles == nil {
			p.profiles = map[string]profileSnapshot{}
		}
		p.profiles[region] = profileSnapshot{profiles: profiles, updated: time.Now()}
		p.profilesMu.Unlock()
		return profiles, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.([]vpcv1.InstanceProfile), nil
	}
}
func (p *IBMInstanceTypeProvider) Refresh(ctx context.Context) error {
	regions := map[string]bool{p.regionForClass(nil): true}
	p.profilesMu.Lock()
	for region, snapshot := range p.profiles {
		regions[region] = true
		snapshot.updated = time.Time{}
		p.profiles[region] = snapshot
	}
	p.profilesMu.Unlock()
	p.zonesMu.Lock()
	p.zonesCache = map[string][]string{}
	p.zonesCacheTime = map[string]time.Time{}
	p.zonesMu.Unlock()
	for region := range regions {
		if _, err := p.rawProfiles(ctx, region); err != nil {
			return err
		}
		p.profilesMu.Lock()
		refreshErr := p.profiles[region].refreshErr
		p.profilesMu.Unlock()
		if refreshErr != nil {
			return fmt.Errorf("refreshing VPC instance profiles for %s: %w", region, refreshErr)
		}
	}
	return nil
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
	p.zonesMu.RLock()
	if zones, ok := p.zonesCache[region]; ok && time.Since(p.zonesCacheTime[region]) < time.Hour {
		p.zonesMu.RUnlock()
		return zones, nil
	}
	p.zonesMu.RUnlock()

	// Get the SDK client directly for zone listing
	vpcClient, err := p.regionalClient(ctx, region)
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
	p.zonesMu.Lock()
	p.zonesCache[region] = zones
	p.zonesCacheTime[region] = time.Now()
	p.zonesMu.Unlock()

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
	// Memory is in GiB from IBM VPC profiles, convert to bytes
	memoryResource := resource.NewQuantity(memoryGB*1024*1024*1024, resource.BinarySI)
	gpuResource := resource.NewQuantity(gpuCount, resource.DecimalSI)

	var kubelet *v1alpha1.KubeletConfiguration
	if nodeClass != nil {
		kubelet = nodeClass.Spec.Kubelet
	}
	podResource := resource.NewQuantity(commonTypes.EffectiveMaxPods(kubelet, cpuCount), resource.DecimalSI)

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

	region := p.regionForClass(nodeClass)
	zones, err := p.getZonesForRegion(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("failed to get zones for region %s: %w", region, err)
	}
	if len(zones) == 0 {
		return nil, fmt.Errorf("no zones found for region %s", region)
	}

	supportedCapacityTypes := capacitytype.GetSupportedCapacityTypes(ctx, profile.AvailabilityClass)

	spotDiscountPercent := options.FromContext(ctx).SpotDiscountPercent
	if spotDiscountPercent == 0 {
		spotDiscountPercent = 60
	}

	var offerings cloudprovider.Offerings
	for _, zone := range zones {
		for _, capacityType := range supportedCapacityTypes {
			if p.pricingProvider == nil {
				continue
			}
			price, priceErr := p.pricingProvider.GetPrice(ctx, *profile.Name, zone)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if priceErr != nil || math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 {
				continue
			}
			if capacityType == karpv1.CapacityTypeSpot {
				price = price * float64(spotDiscountPercent) / 100.0
			}

			cacheKey := *profile.Name + ":" + zone + ":" + capacityType
			available := true
			if p.unavailableOfferings != nil {
				available = !p.unavailableOfferings.IsUnavailable(cacheKey)
			}

			offerings = append(offerings, &cloudprovider.Offering{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
					scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, capacityType),
				),
				Price:     price,
				Available: available,
			})
		}
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

// getInstanceFamily extracts family from instance type name (e.g., "bx2" from "bx2-2x8", "bx3d" from "bx3d-2x8")
func getInstanceFamily(instanceType string) string {
	parts := strings.SplitN(instanceType, "-", 2)
	if len(parts) > 0 && parts[0] != "" {
		return parts[0]
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
