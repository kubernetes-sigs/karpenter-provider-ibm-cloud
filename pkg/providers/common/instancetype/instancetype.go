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
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
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
}

func NewProvider(client *ibm.Client, pricingProvider pricing.Provider) Provider {
	return &IBMInstanceTypeProvider{
		client:           client,
		pricingProvider:  pricingProvider,
		vpcClientManager: vpcclient.NewManager(client, 30*time.Minute),
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

	// Try VPC API first - more reliable than catalog
	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err == nil {
		// List all profiles and find the one we want
		profiles, _, profilesErr := vpcClient.ListInstanceProfiles(&vpcv1.ListInstanceProfilesOptions{})
		err = profilesErr
		if err == nil && profiles != nil && profiles.Profiles != nil {
			for _, profile := range profiles.Profiles {
				if profile.Name != nil && *profile.Name == name {
					return p.convertVPCProfileToInstanceType(profile)
				}
			}
		}
	}

	// Fallback to Global Catalog if VPC API doesn't work
	catalogClient, err := p.client.GetGlobalCatalogClient()
	if err != nil {
		return nil, fmt.Errorf("both VPC and Global Catalog clients failed: %w", err)
	}

	// Get instance profile details from Global Catalog
	entry, err := catalogClient.GetInstanceType(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("getting instance type from catalog: %w", err)
	}

	// Convert catalog entry to instance type
	instanceType, err := convertCatalogEntryToInstanceType(entry)
	if err != nil {
		return nil, fmt.Errorf("converting catalog entry: %w", err)
	}

	return instanceType, nil
}

func (p *IBMInstanceTypeProvider) List(ctx context.Context) ([]*cloudprovider.InstanceType, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("Listing instance types")

	if p.client == nil {
		err := fmt.Errorf("IBM client not initialized")
		logger.Error(err, "Failed to list instance types")
		return nil, err
	}

	// Try VPC API first
	instanceTypes, err := p.listFromVPC(ctx)
	if err != nil {
		logger.Error(err, "Failed to list instance types from VPC API, trying Global Catalog")

		// Fallback to Global Catalog
		catalogClient, err := p.client.GetGlobalCatalogClient()
		if err != nil {
			logger.Error(err, "Failed to get Global Catalog client")
			return nil, fmt.Errorf("both VPC API and Global Catalog failed: VPC error: %v, Catalog error: %w", err, err)
		}

		// List instance profiles from Global Catalog
		entries, err := catalogClient.ListInstanceTypes(ctx)
		if err != nil {
			logger.Error(err, "Failed to list instance types from Global Catalog")
			return nil, fmt.Errorf("both VPC API and Global Catalog failed to list instance types")
		}

		// Convert catalog entries to instance types
		instanceTypes = nil
		for _, entry := range entries {
			instanceType, err := convertCatalogEntryToInstanceType(&entry)
			if err != nil {
				logger.Error(err, "Failed to convert catalog entry", "entry", entry)
				continue
			}
			instanceTypes = append(instanceTypes, instanceType)
		}
	}

	if len(instanceTypes) == 0 {
		err := fmt.Errorf("no instance types found from either VPC API or Global Catalog")
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
		// Get price for this instance type (use default zone for pricing)
		zone := "us-south-1" // Default zone for pricing

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
			priceVal, err := p.pricingProvider.GetPrice(context.Background(), it.Name, "us-south-1")
			if err != nil {
				// Log warning but continue with 0 price - use background context since we don't have the original
				log.Log.WithName("instancetype").Info("Could not get pricing for instance type ranking, using fallback price",
					"instance_type", it.Name, "zone", "us-south-1", "error", err, "fallback_price", 0.0)
				price = 0.0
			} else {
				price = priceVal
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
		instanceType, err := p.convertVPCProfileToInstanceType(profile)
		if err != nil {
			logger.Error(err, "Failed to convert VPC profile", "profile", *profile.Name)
			continue
		}
		instanceTypes = append(instanceTypes, instanceType)
	}

	logger.V(1).Info("Listed instance types from VPC API", "count", len(instanceTypes))
	return instanceTypes, nil
}

// convertVPCProfileToInstanceType converts VPC instance profile to Karpenter instance type
func (p *IBMInstanceTypeProvider) convertVPCProfileToInstanceType(profile vpcv1.InstanceProfile) (*cloudprovider.InstanceType, error) {
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

	// Convert to Kubernetes resource quantities
	cpuResource := resource.NewQuantity(cpuCount, resource.DecimalSI)
	// Memory is in GB from the profile, convert to bytes using standard GB (not GiB)
	memoryResource := resource.NewQuantity(memoryGB*1000*1000*1000, resource.DecimalSI) // Convert GB to bytes

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

	// Create offerings with on-demand capacity in all supported zones
	offerings := cloudprovider.Offerings{
		{
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "us-south-1", "us-south-2", "us-south-3"),
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

func convertCatalogEntryToInstanceType(entry *globalcatalogv1.CatalogEntry) (*cloudprovider.InstanceType, error) {
	if entry == nil {
		return nil, fmt.Errorf("catalog entry is nil")
	}

	// Extract instance type details from catalog entry metadata using reflection
	vcpuCount := 0
	memoryValue := 0
	gpuCount := 0

	// Extract values from metadata using reflection
	if entry.Metadata != nil {
		metadataValue := reflect.ValueOf(entry.Metadata).Elem()

		// Extract CPU count
		if vcpuField := metadataValue.FieldByName("VcpuCount"); vcpuField.IsValid() {
			if vcpuInterface := vcpuField.Interface(); vcpuInterface != nil {
				vcpuValue := reflect.ValueOf(vcpuInterface).Elem().FieldByName("Count")
				if vcpuValue.IsValid() && vcpuValue.Kind() == reflect.Ptr && !vcpuValue.IsNil() {
					vcpuCount = int(vcpuValue.Elem().Int())
				}
			}
		}

		// Extract memory
		if memField := metadataValue.FieldByName("Memory"); memField.IsValid() {
			if memInterface := memField.Interface(); memInterface != nil {
				memValue := reflect.ValueOf(memInterface).Elem().FieldByName("Value")
				if memValue.IsValid() && memValue.Kind() == reflect.Ptr && !memValue.IsNil() {
					memoryValue = int(memValue.Elem().Int())
				}
			}
		}

		// Extract GPU count if available
		if gpuField := metadataValue.FieldByName("GpuCount"); gpuField.IsValid() {
			if gpuInterface := gpuField.Interface(); gpuInterface != nil {
				gpuValue := reflect.ValueOf(gpuInterface).Elem().FieldByName("Count")
				if gpuValue.IsValid() && gpuValue.Kind() == reflect.Ptr && !gpuValue.IsNil() {
					gpuCount = int(gpuValue.Elem().Int())
				}
			}
		}
	}

	return &cloudprovider.InstanceType{
		Name: *entry.Name,
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewQuantity(int64(vcpuCount), resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(int64(memoryValue)*1024*1024*1024, resource.BinarySI),
			"gpu":                 *resource.NewQuantity(int64(gpuCount), resource.DecimalSI),
		},
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, *entry.Name),
			scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"), // TODO: Get from metadata
		),
	}, nil
}
