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
package subnet

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	v1alpha1 "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/constants"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// SubnetInfo contains information about a VPC subnet
type SubnetInfo struct {
	ID              string
	Zone            string
	CIDR            string
	AvailableIPs    int32
	Tags            map[string]string
	State           string
	TotalIPCount    int32
	UsedIPCount     int32
	ReservedIPCount int32
}

// Provider defines the interface for managing IBM Cloud VPC subnets
type Provider interface {
	// ListSubnets returns all subnets in the VPC
	ListSubnets(ctx context.Context, vpcID string) ([]SubnetInfo, error)

	// GetSubnet retrieves information about a specific subnet
	GetSubnet(ctx context.Context, subnetID string) (*SubnetInfo, error)

	// SelectSubnets returns subnets that meet the placement criteria
	SelectSubnets(ctx context.Context, vpcID string, strategy *v1alpha1.PlacementStrategy) ([]SubnetInfo, error)

	// SetKubernetesClient sets the Kubernetes client for cluster-aware subnet selection
	SetKubernetesClient(kubeClient kubernetes.Interface)
}

type provider struct {
	client           *ibm.Client
	kubeClient       kubernetes.Interface
	subnetCache      *cache.Cache
	vpcClientManager *vpcclient.Manager
}

// NewProvider creates a new subnet provider
func NewProvider(client *ibm.Client) Provider {
	return &provider{
		client:           client,
		kubeClient:       nil,                        // Will be set when needed via SetKubernetesClient
		subnetCache:      cache.New(5 * time.Minute), // Cache subnets for 5 minutes
		vpcClientManager: vpcclient.NewManager(client, constants.DefaultVPCClientCacheTTL),
	}
}

// SetKubernetesClient sets the Kubernetes client for cluster-aware subnet selection
func (p *provider) SetKubernetesClient(kubeClient kubernetes.Interface) {
	p.kubeClient = kubeClient
}

// subnetScore represents a subnet's suitability score
type subnetScore struct {
	subnet SubnetInfo
	score  float64
}

// calculateSubnetScore computes a ranking score for a subnet
// Higher scores are better
func calculateSubnetScore(subnet SubnetInfo, criteria *v1alpha1.SubnetSelectionCriteria) float64 {
	var score float64

	// Factor 1: Available IP capacity (0-100 points)
	capacityRatio := float64(subnet.AvailableIPs) / float64(subnet.TotalIPCount)
	score += capacityRatio * 100

	// Factor 2: IP fragmentation penalty (0-50 points penalty)
	fragmentationRatio := float64(subnet.UsedIPCount+subnet.ReservedIPCount) / float64(subnet.TotalIPCount)
	score -= fragmentationRatio * 50

	return score
}

// SelectSubnets implements subnet selection based on placement strategy
func (p *provider) SelectSubnets(ctx context.Context, vpcID string, strategy *v1alpha1.PlacementStrategy) ([]SubnetInfo, error) {
	// Get all subnets in the VPC
	allSubnets, err := p.ListSubnets(ctx, vpcID)
	if err != nil {
		return nil, fmt.Errorf("failed to list subnets: %w", err)
	}

	// Get existing cluster subnet preferences
	clusterSubnets := p.getExistingClusterSubnets(ctx)

	var eligibleSubnets []SubnetInfo

	// Filter subnets based on criteria
	for _, subnet := range allSubnets {
		// Skip subnets in bad state
		if subnet.State != "available" {
			continue
		}

		// Check minimum available IPs
		if strategy.SubnetSelection != nil &&
			strategy.SubnetSelection.MinimumAvailableIPs > 0 &&
			subnet.AvailableIPs < strategy.SubnetSelection.MinimumAvailableIPs {
			continue
		}

		// Check required tags
		if strategy.SubnetSelection != nil && len(strategy.SubnetSelection.RequiredTags) > 0 {
			hasAllTags := true
			for k, v := range strategy.SubnetSelection.RequiredTags {
				if subnet.Tags[k] != v {
					hasAllTags = false
					break
				}
			}
			if !hasAllTags {
				continue
			}
		}

		eligibleSubnets = append(eligibleSubnets, subnet)
	}

	if len(eligibleSubnets) == 0 {
		return nil, fmt.Errorf("no eligible subnets found in VPC %s", vpcID)
	}

	// Score and rank subnets with cluster awareness
	scores := make([]subnetScore, len(eligibleSubnets))
	for i, subnet := range eligibleSubnets {
		baseScore := calculateSubnetScore(subnet, strategy.SubnetSelection)
		clusterAwareScore := p.applyClusterAwareness(subnet, baseScore, clusterSubnets)
		scores[i] = subnetScore{
			subnet: subnet,
			score:  clusterAwareScore,
		}
	}

	// Sort by score (higher is better)
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	// Apply zone distribution strategy
	var selectedSubnets []SubnetInfo
	seenZones := make(map[string]bool)

	switch strategy.ZoneBalance {
	case "Balanced":
		// Select one subnet per zone, prioritizing higher scores
		for _, score := range scores {
			if !seenZones[score.subnet.Zone] {
				selectedSubnets = append(selectedSubnets, score.subnet)
				seenZones[score.subnet.Zone] = true
			}
		}

	case "AvailabilityFirst":
		// Select all eligible subnets to maximize availability
		for _, score := range scores {
			selectedSubnets = append(selectedSubnets, score.subnet)
		}

	case "CostOptimized":
		// Select fewer subnets to minimize cross-zone traffic costs
		// Start with the highest scoring subnet in each zone
		targetZones := 2 // Minimum for HA
		for _, score := range scores {
			if len(selectedSubnets) >= targetZones {
				break
			}
			if !seenZones[score.subnet.Zone] {
				selectedSubnets = append(selectedSubnets, score.subnet)
				seenZones[score.subnet.Zone] = true
			}
		}
	}

	if len(selectedSubnets) == 0 {
		return nil, fmt.Errorf("no subnets selected after applying placement strategy")
	}

	return selectedSubnets, nil
}

// getExistingClusterSubnets extracts subnet information from existing cluster nodes
func (p *provider) getExistingClusterSubnets(ctx context.Context) map[string]int {
	clusterSubnets := make(map[string]int)

	// If no Kubernetes client is available, return empty map (fall back to availability-based selection)
	if p.kubeClient == nil {
		return clusterSubnets
	}

	// Get existing cluster nodes
	nodes, err := p.kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		// Log error but don't fail - fall back to availability-based selection
		return clusterSubnets
	}

	// Extract subnet information from each node
	for _, node := range nodes.Items {
		subnetID := p.extractSubnetFromNode(node)
		if subnetID != "" {
			clusterSubnets[subnetID]++
		}
	}

	return clusterSubnets
}

// extractSubnetFromNode attempts to determine the subnet ID for a node
func (p *provider) extractSubnetFromNode(node v1.Node) string {
	// Method 1: Try to extract from provider ID
	if node.Spec.ProviderID != "" {
		if subnetID := p.parseSubnetFromProviderID(node.Spec.ProviderID); subnetID != "" {
			return subnetID
		}
	}

	// Method 2: Use internal IP to find matching subnet
	for _, addr := range node.Status.Addresses {
		if addr.Type == v1.NodeInternalIP {
			if subnetID := p.findSubnetByIP(addr.Address); subnetID != "" {
				return subnetID
			}
		}
	}

	return ""
}

// parseSubnetFromProviderID extracts subnet ID from IBM Cloud provider ID format
func (p *provider) parseSubnetFromProviderID(providerID string) string {
	// IBM Cloud provider ID format: "ibm:///region/instance_id"
	// This doesn't directly contain subnet info, so we'll need to query the instance
	parts := strings.Split(providerID, "/")
	if len(parts) >= 4 && parts[0] == "ibm:" {
		instanceID := parts[len(parts)-1]
		return p.getSubnetForInstance(instanceID)
	}
	return ""
}

// getSubnetForInstance queries IBM Cloud API to get subnet for an instance
func (p *provider) getSubnetForInstance(instanceID string) string {
	if p.client == nil {
		return ""
	}

	// Get VPC client
	ctx := context.Background()
	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return ""
	}

	// Use context with timeout for API call
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	instance, err := vpcClient.GetInstance(ctx, instanceID)
	if err != nil {
		return ""
	}

	// Extract subnet ID from primary network interface
	if instance.PrimaryNetworkInterface != nil && instance.PrimaryNetworkInterface.Subnet != nil {
		return *instance.PrimaryNetworkInterface.Subnet.ID
	}

	return ""
}

// findSubnetByIP finds which subnet contains the given IP address
func (p *provider) findSubnetByIP(ipAddress string) string {
	// Parse the IP
	ip := net.ParseIP(ipAddress)
	if ip == nil {
		return ""
	}

	// This would require querying all subnets and checking CIDR ranges
	// For now, we'll return empty and rely on provider ID method
	// In a full implementation, we'd cache subnet CIDR mappings
	return ""
}

// applyClusterAwareness modifies the subnet score based on existing cluster topology
func (p *provider) applyClusterAwareness(subnet SubnetInfo, baseScore float64, clusterSubnets map[string]int) float64 {
	nodeCount, hasExistingNodes := clusterSubnets[subnet.ID]

	// If this subnet has existing cluster nodes, give it a significant bonus
	if hasExistingNodes {
		// Bonus increases with the number of existing nodes in the subnet
		clusterBonus := 50.0 + float64(nodeCount)*10.0 // 50-100+ point bonus
		return baseScore + clusterBonus
	}

	// If no existing cluster nodes found, apply a small penalty to prefer cluster subnets
	if len(clusterSubnets) > 0 {
		return baseScore - 5.0 // Small penalty for non-cluster subnets
	}

	// If no cluster information available, return base score unchanged
	return baseScore
}

// ListSubnets returns all subnets in the VPC
func (p *provider) ListSubnets(ctx context.Context, vpcID string) ([]SubnetInfo, error) {
	if p.client == nil {
		return nil, fmt.Errorf("IBM client is not initialized")
	}

	cacheKey := fmt.Sprintf("vpc-subnets:%s", vpcID)

	// Try to get from cache first
	if cached, exists := p.subnetCache.Get(cacheKey); exists {
		return cached.([]SubnetInfo), nil
	}

	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return nil, err
	}

	// List all subnets in the VPC
	response, err := vpcClient.ListSubnets(ctx, vpcID)
	if err != nil {
		return nil, fmt.Errorf("listing subnets for VPC %s: %w", vpcID, err)
	}

	// Convert VPC subnet objects to our SubnetInfo format
	subnets := make([]SubnetInfo, 0, len(response.Subnets))
	for _, subnet := range response.Subnets {
		subnetInfo := convertVPCSubnetToSubnetInfo(subnet)
		subnets = append(subnets, subnetInfo)
	}

	// Cache the result
	p.subnetCache.Set(cacheKey, subnets)

	return subnets, nil
}

// GetSubnet retrieves information about a specific subnet
func (p *provider) GetSubnet(ctx context.Context, subnetID string) (*SubnetInfo, error) {
	if p.client == nil {
		return nil, fmt.Errorf("IBM client is not initialized")
	}

	cacheKey := fmt.Sprintf("subnet:%s", subnetID)

	// Try to get from cache first
	if cached, exists := p.subnetCache.Get(cacheKey); exists {
		result := cached.(SubnetInfo)
		return &result, nil
	}

	vpcClient, err := p.vpcClientManager.GetVPCClient(ctx)
	if err != nil {
		return nil, err
	}

	// Get subnet details
	subnet, err := vpcClient.GetSubnet(ctx, subnetID)
	if err != nil {
		return nil, fmt.Errorf("getting subnet %s: %w", subnetID, err)
	}

	subnetInfo := convertVPCSubnetToSubnetInfo(*subnet)

	// Cache the result with shorter TTL for individual subnets (may change more frequently)
	p.subnetCache.SetWithTTL(cacheKey, subnetInfo, 2*time.Minute)

	return &subnetInfo, nil
}

// convertVPCSubnetToSubnetInfo converts a VPC SDK subnet to our SubnetInfo format
func convertVPCSubnetToSubnetInfo(vpcSubnet vpcv1.Subnet) SubnetInfo {
	subnetInfo := SubnetInfo{
		ID:    *vpcSubnet.ID,
		Zone:  *vpcSubnet.Zone.Name,
		CIDR:  *vpcSubnet.Ipv4CIDRBlock,
		State: *vpcSubnet.Status,
		Tags:  make(map[string]string),
	}

	// Calculate IP counts
	if vpcSubnet.TotalIpv4AddressCount != nil {
		subnetInfo.TotalIPCount = int32(*vpcSubnet.TotalIpv4AddressCount)
	}
	if vpcSubnet.AvailableIpv4AddressCount != nil {
		subnetInfo.AvailableIPs = int32(*vpcSubnet.AvailableIpv4AddressCount)
	}

	// Calculate used IPs
	subnetInfo.UsedIPCount = subnetInfo.TotalIPCount - subnetInfo.AvailableIPs

	// Extract user tags if available (Note: UserTags may not be available in all SDK versions)
	// TODO: Implement tag extraction when IBM Cloud SDK supports user tags on subnets
	// Using empty tags map until SDK supports user tags on subnets

	return subnetInfo
}
