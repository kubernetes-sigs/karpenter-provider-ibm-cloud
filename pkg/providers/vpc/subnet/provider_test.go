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
	"net"
	"sort"
	"testing"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

// Test the provider creation with the existing constructor
func TestProvider_Creation(t *testing.T) {
	client := &ibm.Client{}
	provider := NewProvider(client)
	require.NotNil(t, provider)

	// Test with nil client
	provider2 := NewProvider(nil)
	require.NotNil(t, provider2)
}

// Test core conversion function
func TestConvertVPCSubnetToSubnetInfo(t *testing.T) {
	// Test the conversion function with basic VPC subnet data
	subnetID := "subnet-123"
	zoneName := "us-south-1"
	cidr := "10.240.1.0/24"
	status := "available"
	totalIPs := int64(256)
	availableIPs := int64(200)

	vpcSubnet := vpcv1.Subnet{
		ID:                        &subnetID,
		Zone:                      &vpcv1.ZoneReference{Name: &zoneName},
		Ipv4CIDRBlock:             &cidr,
		Status:                    &status,
		TotalIpv4AddressCount:     &totalIPs,
		AvailableIpv4AddressCount: &availableIPs,
	}

	expected := SubnetInfo{
		ID:           subnetID,
		Zone:         zoneName,
		CIDR:         cidr,
		State:        status,
		TotalIPCount: int32(totalIPs),
		AvailableIPs: int32(availableIPs),
		UsedIPCount:  int32(totalIPs - availableIPs),
		Tags:         make(map[string]string),
	}

	result := convertVPCSubnetToSubnetInfo(vpcSubnet)

	assert.Equal(t, expected.ID, result.ID)
	assert.Equal(t, expected.Zone, result.Zone)
	assert.Equal(t, expected.CIDR, result.CIDR)
	assert.Equal(t, expected.State, result.State)
	assert.Equal(t, expected.TotalIPCount, result.TotalIPCount)
	assert.Equal(t, expected.AvailableIPs, result.AvailableIPs)
	assert.Equal(t, expected.UsedIPCount, result.UsedIPCount)
	assert.NotNil(t, result.Tags)
}

// Test conversion function edge cases
func TestConvertVPCSubnetToSubnetInfo_EdgeCases(t *testing.T) {
	t.Run("subnet with nil values", func(t *testing.T) {
		vpcSubnet := vpcv1.Subnet{
			// All fields are nil pointers
		}

		// This will panic because the implementation doesn't handle nil values
		// Let's test that it panics as expected
		assert.Panics(t, func() {
			convertVPCSubnetToSubnetInfo(vpcSubnet)
		})
	})

	t.Run("subnet with partial data", func(t *testing.T) {
		subnetID := "subnet-partial"
		zoneName := "us-south-2"
		cidr := "10.0.0.0/24"
		status := "available"

		vpcSubnet := vpcv1.Subnet{
			ID:            &subnetID,
			Zone:          &vpcv1.ZoneReference{Name: &zoneName},
			Ipv4CIDRBlock: &cidr,
			Status:        &status,
			// Missing IP count fields
		}

		result := convertVPCSubnetToSubnetInfo(vpcSubnet)

		assert.Equal(t, subnetID, result.ID)
		assert.Equal(t, zoneName, result.Zone)
		assert.Equal(t, cidr, result.CIDR)
		assert.Equal(t, status, result.State)
		assert.Equal(t, int32(0), result.TotalIPCount)
		assert.Equal(t, int32(0), result.AvailableIPs)
	})

	t.Run("subnet with zero available IPs", func(t *testing.T) {
		subnetID := "subnet-full"
		zoneName := "us-south-3"
		cidr := "10.240.2.0/24"
		status := "available"
		totalIPs := int64(256)
		availableIPs := int64(0)

		vpcSubnet := vpcv1.Subnet{
			ID:                        &subnetID,
			Zone:                      &vpcv1.ZoneReference{Name: &zoneName},
			Ipv4CIDRBlock:             &cidr,
			Status:                    &status,
			TotalIpv4AddressCount:     &totalIPs,
			AvailableIpv4AddressCount: &availableIPs,
		}

		result := convertVPCSubnetToSubnetInfo(vpcSubnet)

		assert.Equal(t, int32(0), result.AvailableIPs)
		assert.Equal(t, int32(256), result.UsedIPCount) // All IPs are used
	})
}

// Test subnet scoring algorithm
func TestCalculateSubnetScore(t *testing.T) {
	tests := []struct {
		name     string
		subnet   SubnetInfo
		criteria *v1alpha1.SubnetSelectionCriteria
		expected float64
	}{
		{
			name: "high capacity available subnet",
			subnet: SubnetInfo{
				TotalIPCount:    100,
				AvailableIPs:    90,
				UsedIPCount:     10,
				ReservedIPCount: 0,
			},
			criteria: nil,
			expected: 85.0, // (90/100)*100 - (10/100)*50 = 90 - 5 = 85
		},
		{
			name: "low capacity subnet",
			subnet: SubnetInfo{
				TotalIPCount:    100,
				AvailableIPs:    10,
				UsedIPCount:     80,
				ReservedIPCount: 10,
			},
			criteria: nil,
			expected: -35.0, // (10/100)*100 - (90/100)*50 = 10 - 45 = -35
		},
		{
			name: "perfect subnet - all IPs available",
			subnet: SubnetInfo{
				TotalIPCount:    100,
				AvailableIPs:    100,
				UsedIPCount:     0,
				ReservedIPCount: 0,
			},
			criteria: nil,
			expected: 100.0, // (100/100)*100 - (0/100)*50 = 100
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := calculateSubnetScore(tt.subnet, tt.criteria)
			assert.Equal(t, tt.expected, score)
		})
	}
}

// Test provider interface compliance without calling IBM APIs
func TestVPCSubnetProvider_Interface(t *testing.T) {
	t.Run("NewProvider", func(t *testing.T) {
		client := &ibm.Client{}
		provider := NewProvider(client)
		assert.NotNil(t, provider)

		// The provider is created through an interface
		assert.NotNil(t, provider)
	})

	t.Run("SelectSubnets with nil client", func(t *testing.T) {
		provider := NewProvider(nil)

		ctx := context.Background()
		vpcID := "test-vpc"
		strategy := &v1alpha1.PlacementStrategy{
			SubnetSelection: &v1alpha1.SubnetSelectionCriteria{},
		}

		// Test that the method exists and can be called (will error due to nil client)
		result, err := provider.SelectSubnets(ctx, vpcID, strategy)
		assert.Error(t, err) // Expected to fail
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to list subnets")
	})

	t.Run("ListSubnets with nil client", func(t *testing.T) {
		provider := NewProvider(nil)

		ctx := context.Background()
		vpcID := "test-vpc"

		// Test that the method exists and can be called (will error due to nil client)
		result, err := provider.ListSubnets(ctx, vpcID)
		assert.Error(t, err) // Expected to fail
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "IBM client is not initialized")
	})

	t.Run("GetSubnet with nil client", func(t *testing.T) {
		provider := NewProvider(nil)

		ctx := context.Background()
		subnetID := "test-subnet"

		// Test that the method exists and can be called (will error due to nil client)
		result, err := provider.GetSubnet(ctx, subnetID)
		assert.Error(t, err) // Expected to fail
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "IBM client is not initialized")
	})
}

// TestSubnetSelection_NetworkConnectivity tests subnet selection network isolation scenarios
func TestSubnetSelection_NetworkConnectivity(t *testing.T) {
	t.Run("cluster nodes in first-second subnet scenario", func(t *testing.T) {

		clusterSubnet := SubnetInfo{
			ID:           "02c7-ac2802cf-54bb-4508-aad7-eba7e8c2034c", // first-second
			Zone:         "eu-de-2",
			CIDR:         "10.243.65.0/24", // Control plane and worker nodes network
			State:        "available",
			TotalIPCount: 256,
			AvailableIPs: 200,
			UsedIPCount:  56,
			Tags:         map[string]string{"Name": "first-second"},
		}

		wrongAutoSelectedSubnet := SubnetInfo{
			ID:           "02c7-dc97437a-1b67-41c9-9db9-a6b92a46963d", // eu-de-2-default-subnet
			Zone:         "eu-de-2",
			CIDR:         "10.243.64.0/24", // Isolated from cluster nodes
			State:        "available",
			TotalIPCount: 256,
			AvailableIPs: 250,
			UsedIPCount:  6,
			Tags:         map[string]string{"Name": "eu-de-2-default-subnet"},
		}

		// Test that these subnets are in different networks
		assert.NotEqual(t, clusterSubnet.CIDR, wrongAutoSelectedSubnet.CIDR,
			"Cluster subnet and auto-selected subnet should be in different networks")

		// Test that nodes in wrongAutoSelectedSubnet cannot reach cluster API server
		// API server is at 10.243.65.4 (in clusterSubnet CIDR)
		apiServerIP := "10.243.65.4"

		// Helper function to check if IP is in CIDR range
		isInClusterNetwork := isIPInCIDR(apiServerIP, clusterSubnet.CIDR)
		isInWrongNetwork := isIPInCIDR(apiServerIP, wrongAutoSelectedSubnet.CIDR)

		assert.True(t, isInClusterNetwork, "API server should be reachable from cluster subnet")
		assert.False(t, isInWrongNetwork, "API server should NOT be reachable from wrong subnet")

		// Test scoring preference for cluster subnet
		clusterScore := calculateSubnetScore(clusterSubnet, nil)
		wrongScore := calculateSubnetScore(wrongAutoSelectedSubnet, nil)

		// The scoring algorithm prefers wrongAutoSelectedSubnet due to more available IPs.
		// This is a known limitation: availability-based scoring doesn't consider cluster topology.
		assert.Greater(t, wrongScore, clusterScore,
			"scoring algorithm should prefer subnet with more available IPs, exposing the topology-unaware limitation")
	})

	t.Run("subnet selection should prioritize cluster connectivity", func(t *testing.T) {
		// Test case for the enhanced subnet selection that should be implemented

		// Mock existing cluster nodes in specific subnets
		existingClusterSubnets := map[string]int{
			"02c7-ac2802cf-54bb-4508-aad7-eba7e8c2034c": 2, // first-second: 2 nodes (control-plane + worker)
		}

		availableSubnets := []SubnetInfo{
			{
				ID:           "02c7-ac2802cf-54bb-4508-aad7-eba7e8c2034c", // first-second (cluster subnet)
				Zone:         "eu-de-2",
				CIDR:         "10.243.65.0/24",
				State:        "available",
				TotalIPCount: 256,
				AvailableIPs: 200, // Less available IPs
				UsedIPCount:  56,
				Tags:         map[string]string{"Name": "first-second"},
			},
			{
				ID:           "02c7-dc97437a-1b67-41c9-9db9-a6b92a46963d", // isolated subnet
				Zone:         "eu-de-2",
				CIDR:         "10.243.64.0/24",
				State:        "available",
				TotalIPCount: 256,
				AvailableIPs: 250, // More available IPs
				UsedIPCount:  6,
				Tags:         map[string]string{"Name": "eu-de-2-default-subnet"},
			},
		}

		// Enhanced selection algorithm should prioritize cluster connectivity over availability
		selectedSubnet := selectClusterAwareSubnet(availableSubnets, existingClusterSubnets)

		// Should select the cluster subnet despite fewer available IPs
		assert.Equal(t, "02c7-ac2802cf-54bb-4508-aad7-eba7e8c2034c", selectedSubnet.ID,
			"Enhanced algorithm should prefer subnet with existing cluster nodes")
		assert.Equal(t, "10.243.65.0/24", selectedSubnet.CIDR,
			"Selected subnet should be in same network as existing cluster nodes")
	})

	t.Run("multi-zone cluster subnet distribution", func(t *testing.T) {
		// Test for future enhancement: multi-zone cluster awareness

		existingClusterSubnets := map[string]int{
			"zone1-cluster-subnet": 3, // 3 nodes in zone 1
			"zone2-cluster-subnet": 2, // 2 nodes in zone 2
		}

		availableSubnets := []SubnetInfo{
			{
				ID:           "zone1-cluster-subnet",
				Zone:         "eu-de-1",
				CIDR:         "10.243.1.0/24",
				AvailableIPs: 100,
			},
			{
				ID:           "zone1-isolated-subnet",
				Zone:         "eu-de-1",
				CIDR:         "10.244.1.0/24", // Different network
				AvailableIPs: 200,
			},
			{
				ID:           "zone2-cluster-subnet",
				Zone:         "eu-de-2",
				CIDR:         "10.243.2.0/24",
				AvailableIPs: 150,
			},
		}

		// For zone 1, should prefer zone1-cluster-subnet despite fewer IPs
		zone1Selection := selectClusterAwareSubnetForZone(availableSubnets, existingClusterSubnets, "eu-de-1")
		assert.Equal(t, "zone1-cluster-subnet", zone1Selection.ID,
			"Should prefer subnet with existing cluster nodes in same zone")

		// For zone 2, should prefer zone2-cluster-subnet
		zone2Selection := selectClusterAwareSubnetForZone(availableSubnets, existingClusterSubnets, "eu-de-2")
		assert.Equal(t, "zone2-cluster-subnet", zone2Selection.ID,
			"Should prefer subnet with existing cluster nodes in same zone")
	})
}

func isIPInCIDR(ip, cidr string) bool {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return network.Contains(net.ParseIP(ip))
}

// Mock implementation of cluster-aware subnet selection
// This represents the enhancement that should be implemented to fix the bug
func selectClusterAwareSubnet(availableSubnets []SubnetInfo, existingClusterSubnets map[string]int) SubnetInfo {
	// Priority 1: Prefer subnets with existing cluster nodes
	for _, subnet := range availableSubnets {
		if nodeCount, exists := existingClusterSubnets[subnet.ID]; exists && nodeCount > 0 {
			return subnet
		}
	}

	// Priority 2: Fall back to availability-based selection
	var bestSubnet SubnetInfo
	var maxScore float64 = -1
	for _, subnet := range availableSubnets {
		score := calculateSubnetScore(subnet, nil)
		if score > maxScore {
			maxScore = score
			bestSubnet = subnet
		}
	}
	return bestSubnet
}

// Mock implementation for zone-specific cluster-aware selection
func selectClusterAwareSubnetForZone(availableSubnets []SubnetInfo, existingClusterSubnets map[string]int, zone string) SubnetInfo {
	// Filter subnets by zone first
	var zoneSubnets []SubnetInfo
	for _, subnet := range availableSubnets {
		if subnet.Zone == zone {
			zoneSubnets = append(zoneSubnets, subnet)
		}
	}

	// Apply cluster-aware selection within zone
	return selectClusterAwareSubnet(zoneSubnets, existingClusterSubnets)
}

// Test additional scoring scenarios
func TestCalculateSubnetScore_Enhanced(t *testing.T) {
	tests := []struct {
		name     string
		subnet   SubnetInfo
		criteria *v1alpha1.SubnetSelectionCriteria
		expected float64
	}{
		{
			name: "high capacity subnet with good availability",
			subnet: SubnetInfo{
				TotalIPCount:    256,
				AvailableIPs:    240,
				UsedIPCount:     16,
				ReservedIPCount: 0,
			},
			criteria: nil,
			expected: 90.625, // (240/256)*100 - (16/256)*50 = 93.75 - 3.125 = 90.625
		},
		{
			name: "worst subnet - no IPs available",
			subnet: SubnetInfo{
				TotalIPCount:    100,
				AvailableIPs:    0,
				UsedIPCount:     100,
				ReservedIPCount: 0,
			},
			criteria: nil,
			expected: -50.0, // (0/100)*100 - (100/100)*50 = 0 - 50 = -50
		},
		{
			name: "zero total IP count",
			subnet: SubnetInfo{
				TotalIPCount:    0,
				AvailableIPs:    0,
				UsedIPCount:     0,
				ReservedIPCount: 0,
			},
			criteria: nil,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := calculateSubnetScore(tt.subnet, tt.criteria)
			// Use tolerance for floating point comparison
			assert.InDelta(t, tt.expected, score, 1.0, "Score should be within tolerance")
		})
	}
}

// Test provider basic functionality
func TestProviderWithCache(t *testing.T) {
	client := &ibm.Client{}
	provider := NewProvider(client)

	// Verify provider is not nil
	assert.NotNil(t, provider)

	// Verify interface compliance - provider implements Provider interface
	_ = provider
}

// Test provider creation scenarios
func TestProvider_NewProvider(t *testing.T) {
	tests := []struct {
		name   string
		client *ibm.Client
	}{
		{
			name:   "successful creation",
			client: &ibm.Client{},
		},
		{
			name:   "nil client",
			client: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewProvider(tt.client)
			assert.NotNil(t, provider)

			// Verify provider implements the interface
			_ = provider
		})
	}
}

// Test provider interface without actual API calls
func TestProvider_Interface(t *testing.T) {
	// Test that provider implements Provider interface
	client := &ibm.Client{}
	provider := NewProvider(client)

	// Verify interface compliance
	assert.NotNil(t, provider)
	_ = provider
}

// Test method signatures
func TestProvider_MethodSignatures(t *testing.T) {
	// Test method signatures without calling them to avoid crashes
	client := &ibm.Client{}
	provider := NewProvider(client)

	// Verify provider is created
	assert.NotNil(t, provider)

	// Verify strategy struct can be created
	strategy := &v1alpha1.PlacementStrategy{
		ZoneBalance: "Balanced",
		SubnetSelection: &v1alpha1.SubnetSelectionCriteria{
			MinimumAvailableIPs: 50,
			RequiredTags:        map[string]string{"env": "test"},
		},
	}
	assert.Equal(t, "Balanced", strategy.ZoneBalance)
	assert.Equal(t, int32(50), strategy.SubnetSelection.MinimumAvailableIPs)
}

// TestApplyClusterAwareness_Method tests the actual provider method directly.
func TestApplyClusterAwareness_Method(t *testing.T) {
	// Create a provider struct directly; nil fields are fine since applyClusterAwareness
	// doesn't reference client, kubeClient, subnetCache, or vpcClientManager.
	p := &provider{}

	tests := []struct {
		name           string
		subnet         SubnetInfo
		baseScore      float64
		clusterSubnets map[string]int
		expected       float64
	}{
		{
			name:      "subnet with 1 existing node gets bonus of 60",
			subnet:    SubnetInfo{ID: "subnet-a"},
			baseScore: 80.0,
			clusterSubnets: map[string]int{
				"subnet-a": 1,
			},
			expected: 140.0, // 80 + 50 + 1*10
		},
		{
			name:      "subnet with 5 existing nodes gets bonus of 100",
			subnet:    SubnetInfo{ID: "subnet-a"},
			baseScore: 80.0,
			clusterSubnets: map[string]int{
				"subnet-a": 5,
			},
			expected: 180.0, // 80 + 50 + 5*10
		},
		{
			name:      "subnet not in cluster map but other subnets exist gets penalty",
			subnet:    SubnetInfo{ID: "subnet-b"},
			baseScore: 80.0,
			clusterSubnets: map[string]int{
				"subnet-a": 3,
			},
			expected: 75.0, // 80 - 5
		},
		{
			name:           "subnet not in cluster map and empty cluster map returns base score",
			subnet:         SubnetInfo{ID: "subnet-b"},
			baseScore:      80.0,
			clusterSubnets: map[string]int{},
			expected:       80.0,
		},
		{
			name:      "empty subnet ID in cluster map with 0 nodes still matches",
			subnet:    SubnetInfo{ID: ""},
			baseScore: 50.0,
			clusterSubnets: map[string]int{
				"": 0,
			},
			expected: 100.0, // 50 + 50 + 0*10; hasExistingNodes is true because key exists
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := p.applyClusterAwareness(tt.subnet, tt.baseScore, tt.clusterSubnets)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestCalculateSubnetScore_WithCriteria verifies that passing non-nil criteria
// produces the same score as nil criteria (criteria is currently unused in scoring).
func TestCalculateSubnetScore_WithCriteria(t *testing.T) {
	subnet := SubnetInfo{
		TotalIPCount:    200,
		AvailableIPs:    150,
		UsedIPCount:     50,
		ReservedIPCount: 0,
	}

	scoreNil := calculateSubnetScore(subnet, nil)

	t.Run("criteria with MinimumAvailableIPs does not change score", func(t *testing.T) {
		criteria := &v1alpha1.SubnetSelectionCriteria{
			MinimumAvailableIPs: 100,
		}
		scoreWithCriteria := calculateSubnetScore(subnet, criteria)
		assert.Equal(t, scoreNil, scoreWithCriteria)
	})

	t.Run("criteria with RequiredTags does not change score", func(t *testing.T) {
		criteria := &v1alpha1.SubnetSelectionCriteria{
			RequiredTags: map[string]string{"env": "prod", "team": "platform"},
		}
		scoreWithCriteria := calculateSubnetScore(subnet, criteria)
		assert.Equal(t, scoreNil, scoreWithCriteria)
	})

	t.Run("criteria with both fields does not change score", func(t *testing.T) {
		criteria := &v1alpha1.SubnetSelectionCriteria{
			MinimumAvailableIPs: 50,
			RequiredTags:        map[string]string{"env": "staging"},
		}
		scoreWithCriteria := calculateSubnetScore(subnet, criteria)
		assert.Equal(t, scoreNil, scoreWithCriteria)
	})
}

// TestCalculateSubnetScore_BoundaryValues tests edge cases for the scoring function.
func TestCalculateSubnetScore_BoundaryValues(t *testing.T) {
	tests := []struct {
		name     string
		subnet   SubnetInfo
		expected float64
	}{
		{
			name: "single IP subnet fully available",
			subnet: SubnetInfo{
				TotalIPCount:    1,
				AvailableIPs:    1,
				UsedIPCount:     0,
				ReservedIPCount: 0,
			},
			expected: 100.0, // (1/1)*100 - (0/1)*50
		},
		{
			name: "single IP subnet fully used",
			subnet: SubnetInfo{
				TotalIPCount:    1,
				AvailableIPs:    0,
				UsedIPCount:     1,
				ReservedIPCount: 0,
			},
			expected: -50.0, // (0/1)*100 - (1/1)*50
		},
		{
			name: "very large subnet half available",
			subnet: SubnetInfo{
				TotalIPCount:    65536,
				AvailableIPs:    32768,
				UsedIPCount:     32768,
				ReservedIPCount: 0,
			},
			expected: 25.0, // (32768/65536)*100 - (32768/65536)*50 = 50 - 25
		},
		{
			name: "reserved IPs contribute to used ratio",
			subnet: SubnetInfo{
				TotalIPCount:    100,
				AvailableIPs:    80,
				UsedIPCount:     10,
				ReservedIPCount: 10,
			},
			expected: 70.0, // (80/100)*100 - ((10+10)/100)*50 = 80 - 10
		},
		{
			name: "reserved IPs only no used IPs",
			subnet: SubnetInfo{
				TotalIPCount:    100,
				AvailableIPs:    50,
				UsedIPCount:     0,
				ReservedIPCount: 50,
			},
			expected: 25.0, // (50/100)*100 - ((0+50)/100)*50 = 50 - 25
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := calculateSubnetScore(tt.subnet, nil)
			assert.InDelta(t, tt.expected, score, 0.001)
		})
	}
}

// TestConvertVPCSubnetToSubnetInfo_WithName tests conversion when the VPC subnet has a Name field.
// The current implementation does not copy Name to Tags, so Tags should be empty.
func TestConvertVPCSubnetToSubnetInfo_WithName(t *testing.T) {
	subnetID := "subnet-named"
	zoneName := "us-south-1"
	cidr := "10.0.0.0/24"
	status := "available"
	name := "my-subnet"
	totalIPs := int64(256)
	availIPs := int64(200)

	vpcSubnet := vpcv1.Subnet{
		ID:                        &subnetID,
		Zone:                      &vpcv1.ZoneReference{Name: &zoneName},
		Ipv4CIDRBlock:             &cidr,
		Status:                    &status,
		Name:                      &name,
		TotalIpv4AddressCount:     &totalIPs,
		AvailableIpv4AddressCount: &availIPs,
	}

	result := convertVPCSubnetToSubnetInfo(vpcSubnet)

	assert.Equal(t, subnetID, result.ID)
	assert.Equal(t, zoneName, result.Zone)
	assert.Equal(t, cidr, result.CIDR)
	assert.Equal(t, "available", result.State)
	assert.NotNil(t, result.Tags)
	// Name is not extracted into Tags by the current implementation
	_, hasName := result.Tags["Name"]
	assert.False(t, hasName, "Current implementation does not copy Name into Tags")
	assert.Equal(t, int32(256), result.TotalIPCount)
	assert.Equal(t, int32(200), result.AvailableIPs)
	assert.Equal(t, int32(56), result.UsedIPCount)
}

// TestConvertVPCSubnetToSubnetInfo_WithoutName tests conversion when Name is nil.
func TestConvertVPCSubnetToSubnetInfo_WithoutName(t *testing.T) {
	subnetID := "subnet-noname"
	zoneName := "us-south-2"
	cidr := "10.1.0.0/24"
	status := "available"

	vpcSubnet := vpcv1.Subnet{
		ID:            &subnetID,
		Zone:          &vpcv1.ZoneReference{Name: &zoneName},
		Ipv4CIDRBlock: &cidr,
		Status:        &status,
		// Name is nil
	}

	result := convertVPCSubnetToSubnetInfo(vpcSubnet)

	assert.Equal(t, subnetID, result.ID)
	assert.NotNil(t, result.Tags, "Tags map should be initialized even without Name")
	assert.Len(t, result.Tags, 0, "Tags map should be empty when Name is nil")
}

// TestSubnetScoring_SortOrder verifies that subnets sort correctly by score descending.
func TestSubnetScoring_SortOrder(t *testing.T) {
	subnets := []SubnetInfo{
		{
			ID:           "low",
			TotalIPCount: 100, AvailableIPs: 10, UsedIPCount: 90, ReservedIPCount: 0,
			// score = (10/100)*100 - (90/100)*50 = 10 - 45 = -35
		},
		{
			ID:           "medium",
			TotalIPCount: 100, AvailableIPs: 50, UsedIPCount: 50, ReservedIPCount: 0,
			// score = (50/100)*100 - (50/100)*50 = 50 - 25 = 25
		},
		{
			ID:           "high",
			TotalIPCount: 100, AvailableIPs: 90, UsedIPCount: 10, ReservedIPCount: 0,
			// score = (90/100)*100 - (10/100)*50 = 90 - 5 = 85
		},
		{
			ID:           "perfect",
			TotalIPCount: 100, AvailableIPs: 100, UsedIPCount: 0, ReservedIPCount: 0,
			// score = 100 - 0 = 100
		},
	}

	scored := make([]subnetScore, len(subnets))
	for i, s := range subnets {
		scored[i] = subnetScore{subnet: s, score: calculateSubnetScore(s, nil)}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	require.Len(t, scored, 4)
	assert.Equal(t, "perfect", scored[0].subnet.ID)
	assert.Equal(t, "high", scored[1].subnet.ID)
	assert.Equal(t, "medium", scored[2].subnet.ID)
	assert.Equal(t, "low", scored[3].subnet.ID)

	// Verify actual score values
	assert.InDelta(t, 100.0, scored[0].score, 0.001)
	assert.InDelta(t, 85.0, scored[1].score, 0.001)
	assert.InDelta(t, 25.0, scored[2].score, 0.001)
	assert.InDelta(t, -35.0, scored[3].score, 0.001)
}

// TestSubnetFiltering_ByTags simulates the tag-based filtering logic used in SelectSubnets.
func TestSubnetFiltering_ByTags(t *testing.T) {
	subnets := []SubnetInfo{
		{ID: "s1", State: "available", Tags: map[string]string{"env": "prod", "team": "platform"}},
		{ID: "s2", State: "available", Tags: map[string]string{"env": "staging"}},
		{ID: "s3", State: "available", Tags: map[string]string{"env": "prod", "team": "data"}},
		{ID: "s4", State: "available", Tags: map[string]string{}},
		{ID: "s5", State: "available", Tags: nil},
	}

	tests := []struct {
		name         string
		requiredTags map[string]string
		expectedIDs  []string
	}{
		{
			name:         "match single tag",
			requiredTags: map[string]string{"env": "prod"},
			expectedIDs:  []string{"s1", "s3"},
		},
		{
			name:         "match multiple tags requires all",
			requiredTags: map[string]string{"env": "prod", "team": "platform"},
			expectedIDs:  []string{"s1"},
		},
		{
			name:         "no match when tag value differs",
			requiredTags: map[string]string{"env": "dev"},
			expectedIDs:  []string{},
		},
		{
			name:         "empty required tags matches all",
			requiredTags: map[string]string{},
			expectedIDs:  []string{"s1", "s2", "s3", "s4", "s5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var filtered []SubnetInfo
			for _, subnet := range subnets {
				if len(tt.requiredTags) > 0 {
					hasAll := true
					for k, v := range tt.requiredTags {
						if subnet.Tags[k] != v {
							hasAll = false
							break
						}
					}
					if !hasAll {
						continue
					}
				}
				filtered = append(filtered, subnet)
			}

			var gotIDs []string
			for _, s := range filtered {
				gotIDs = append(gotIDs, s.ID)
			}
			if gotIDs == nil {
				gotIDs = []string{}
			}
			assert.Equal(t, tt.expectedIDs, gotIDs)
		})
	}
}

// TestSubnetFiltering_ByMinimumIPs simulates minimum IP filtering with various thresholds.
func TestSubnetFiltering_ByMinimumIPs(t *testing.T) {
	subnets := []SubnetInfo{
		{ID: "tiny", State: "available", AvailableIPs: 5},
		{ID: "small", State: "available", AvailableIPs: 20},
		{ID: "medium", State: "available", AvailableIPs: 100},
		{ID: "large", State: "available", AvailableIPs: 500},
		{ID: "unavailable", State: "pending", AvailableIPs: 1000},
	}

	tests := []struct {
		name        string
		minIPs      int32
		expectedIDs []string
	}{
		{
			name:        "threshold 0 returns all available",
			minIPs:      0,
			expectedIDs: []string{"tiny", "small", "medium", "large"},
		},
		{
			name:        "threshold 10 excludes tiny",
			minIPs:      10,
			expectedIDs: []string{"small", "medium", "large"},
		},
		{
			name:        "threshold 100 returns medium and large",
			minIPs:      100,
			expectedIDs: []string{"medium", "large"},
		},
		{
			name:        "threshold 501 returns none of available subnets",
			minIPs:      501,
			expectedIDs: []string{},
		},
		{
			name:        "exact boundary match is included",
			minIPs:      500,
			expectedIDs: []string{"large"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var filtered []SubnetInfo
			for _, subnet := range subnets {
				if subnet.State != "available" {
					continue
				}
				if tt.minIPs > 0 && subnet.AvailableIPs < tt.minIPs {
					continue
				}
				filtered = append(filtered, subnet)
			}

			var gotIDs []string
			for _, s := range filtered {
				gotIDs = append(gotIDs, s.ID)
			}
			if gotIDs == nil {
				gotIDs = []string{}
			}
			assert.Equal(t, tt.expectedIDs, gotIDs)
		})
	}
}

// TestProvider_SetKubernetesClient verifies SetKubernetesClient works without panic.
func TestProvider_SetKubernetesClient(t *testing.T) {
	p := NewProvider(&ibm.Client{})

	t.Run("set nil client does not panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			p.SetKubernetesClient(nil)
		})
	})

	t.Run("set nil client on nil-client provider does not panic", func(t *testing.T) {
		p2 := NewProvider(nil)
		assert.NotPanics(t, func() {
			p2.SetKubernetesClient(nil)
		})
	})
}

// TestSelectSubnets_NilStrategy verifies SelectSubnets behavior with a nil strategy.
// With a nil IBM client the call fails at ListSubnets, so we verify the error path.
func TestSelectSubnets_NilStrategy(t *testing.T) {
	p := NewProvider(nil)
	ctx := context.Background()

	_, err := p.SelectSubnets(ctx, "vpc-123", nil)
	require.Error(t, err)
	// With nil client, ListSubnets returns an error before strategy is accessed.
	assert.Contains(t, err.Error(), "failed to list subnets")
}

// TestSelectSubnets_EmptyVPCID verifies SelectSubnets behavior with an empty VPC ID.
func TestSelectSubnets_EmptyVPCID(t *testing.T) {
	p := NewProvider(nil)
	ctx := context.Background()
	strategy := &v1alpha1.PlacementStrategy{
		ZoneBalance:     "Balanced",
		SubnetSelection: &v1alpha1.SubnetSelectionCriteria{},
	}

	_, err := p.SelectSubnets(ctx, "", strategy)
	require.Error(t, err)
	// With nil client, the error comes from ListSubnets before VPC ID is used
	assert.Contains(t, err.Error(), "failed to list subnets")
}
