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
	"fmt"
	"testing"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
)

func TestMultiAZNodeProvisioning(t *testing.T) {
	tests := []struct {
		name               string
		nodeClass          *v1alpha1.IBMNodeClass
		subnets            map[string]*vpcv1.Subnet
		expectedZones      []string
		expectedError      string
		shouldUsePlacement bool
	}{
		{
			name: "multi-AZ provisioning when zone not specified - uses subnet provider to select zones",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "multi-az-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					// Zone is NOT specified - should use subnet provider
					VPC:             "r042-12345678-1234-1234-1234-123456789012",
					Image:           "ubuntu-24-04-amd64",
					InstanceProfile: "bx2-2x8",
					SecurityGroups:  []string{"sg-1"},
					PlacementStrategy: &v1alpha1.PlacementStrategy{
						ZoneBalance: "Balanced",
					},
				},
			},
			subnets:            createMockSubnetsForZones([]string{"subnet-us-south-1", "subnet-us-south-2", "subnet-us-south-3"}),
			expectedZones:      []string{"us-south-1", "us-south-2", "us-south-3"},
			shouldUsePlacement: true,
		},
		{
			name: "single zone provisioning when zone is specified",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "single-zone-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-2",        // Specific zone requested
					Subnet:          "subnet-us-south-2", // Must match zone
					VPC:             "r042-12345678-1234-1234-1234-123456789012",
					Image:           "ubuntu-24-04-amd64",
					InstanceProfile: "bx2-2x8",
					SecurityGroups:  []string{"sg-1"},
				},
			},
			subnets:            createMockSubnetsForZones([]string{"subnet-us-south-2"}),
			expectedZones:      []string{"us-south-2"}, // Only the specified zone
			shouldUsePlacement: false,
		},
		{
			name: "error when no zone and no placement strategy",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "invalid-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					// No zone AND no placement strategy - should fail
					VPC:             "r042-12345678-1234-1234-1234-123456789012",
					Image:           "ubuntu-24-04-amd64",
					InstanceProfile: "bx2-2x8",
					SecurityGroups:  []string{"sg-1"},
				},
			},
			subnets:       createMockSubnetsForZones([]string{"subnet-us-south-1"}),
			expectedError: "zone selection requires either explicit zone or placement strategy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock clients
			mockClient := &mockIBMClient{
				vpcClient: &mockVPCClient{
					subnets:   tt.subnets,
					instances: make(map[string]*vpcv1.Instance),
				},
			}

			mockSubnetProvider := &mockSubnetProvider{
				subnets: tt.subnets,
			}

			// Create a mock instance provider that uses subnet provider for zone selection
			provider := &testMultiAZInstanceProvider{
				client:         mockClient,
				subnetProvider: mockSubnetProvider,
			}

			// Note: nodeClaim not needed for zone selection test

			// Test zone selection logic
			selectedZone, selectedSubnet, err := provider.selectZoneAndSubnet(ctx, tt.nodeClass)

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				return
			}

			require.NoError(t, err)
			assert.Contains(t, tt.expectedZones, selectedZone, "Selected zone should be in expected zones")
			assert.NotEmpty(t, selectedSubnet, "Should select a subnet")

			// Verify subnet matches zone
			subnetInfo, exists := tt.subnets[selectedSubnet]
			require.True(t, exists, "Selected subnet should exist")
			assert.Equal(t, selectedZone, *subnetInfo.Zone.Name, "Selected subnet should match selected zone")

			// For multi-AZ tests, verify multiple zone selections distribute properly
			if len(tt.expectedZones) > 1 && !tt.shouldUsePlacement {
				// Skip this test for placement strategy cases as they have more complex logic
				return
			}

			if len(tt.expectedZones) > 1 {
				// Test that multiple selections distribute across zones
				zoneDistribution := make(map[string]int)
				for i := 0; i < 10; i++ {
					zone, subnet, err := provider.selectZoneAndSubnet(ctx, tt.nodeClass)
					require.NoError(t, err)
					assert.Contains(t, tt.expectedZones, zone)
					assert.NotEmpty(t, subnet)
					zoneDistribution[zone]++
				}

				// Verify we got nodes in multiple zones
				assert.Greater(t, len(zoneDistribution), 1, "Should distribute nodes across multiple zones")
			}
		})
	}
}

// testMultiAZInstanceProvider is a test implementation that demonstrates proper multi-AZ zone selection
type testMultiAZInstanceProvider struct {
	client         *mockIBMClient
	subnetProvider *mockSubnetProvider
	zoneIndex      int // Track zone selection for round-robin distribution
}

// selectZoneAndSubnet implements the core zone selection logic for multi-AZ provisioning
func (p *testMultiAZInstanceProvider) selectZoneAndSubnet(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass) (string, string, error) {
	// Case 1: Zone explicitly specified - use it directly
	if nodeClass.Spec.Zone != "" {
		if nodeClass.Spec.Subnet != "" {
			// Verify subnet exists in the specified zone
			subnet, exists := p.subnetProvider.subnets[nodeClass.Spec.Subnet]
			if !exists {
				return "", "", fmt.Errorf("subnet %s not found", nodeClass.Spec.Subnet)
			}
			if *subnet.Zone.Name != nodeClass.Spec.Zone {
				return "", "", fmt.Errorf("subnet %s is in zone %s, but nodeClass specifies zone %s",
					nodeClass.Spec.Subnet, *subnet.Zone.Name, nodeClass.Spec.Zone)
			}
			return nodeClass.Spec.Zone, nodeClass.Spec.Subnet, nil
		}

		// Find a subnet in the specified zone
		for subnetID, subnet := range p.subnetProvider.subnets {
			if *subnet.Zone.Name == nodeClass.Spec.Zone {
				return nodeClass.Spec.Zone, subnetID, nil
			}
		}
		return "", "", fmt.Errorf("no subnet found in zone %s", nodeClass.Spec.Zone)
	}

	// Case 2: No zone specified - need placement strategy for multi-AZ
	if nodeClass.Spec.PlacementStrategy == nil {
		return "", "", fmt.Errorf("zone selection requires either explicit zone or placement strategy")
	}

	// Use subnet provider to select subnets based on placement strategy
	selectedSubnets, err := p.subnetProvider.ListSubnets(ctx, nodeClass.Spec.VPC)
	if err != nil {
		return "", "", fmt.Errorf("listing subnets: %w", err)
	}

	if len(selectedSubnets) == 0 {
		return "", "", fmt.Errorf("no eligible subnets found")
	}

	// For balanced placement, rotate through zones using round-robin
	zoneSubnets := make(map[string][]string)
	var zones []string
	for _, subnetInfo := range selectedSubnets {
		zone := subnetInfo.Zone
		if _, exists := zoneSubnets[zone]; !exists {
			zones = append(zones, zone)
		}
		zoneSubnets[zone] = append(zoneSubnets[zone], subnetInfo.ID)
	}

	if len(zones) == 0 {
		return "", "", fmt.Errorf("no zones available")
	}

	// Implement round-robin zone selection for multi-AZ distribution
	selectedZone := zones[p.zoneIndex%len(zones)]
	p.zoneIndex++ // Increment for next call

	subnets := zoneSubnets[selectedZone]
	if len(subnets) == 0 {
		return "", "", fmt.Errorf("no subnets available in zone %s", selectedZone)
	}

	return selectedZone, subnets[0], nil
}

// Mock IBM client for testing
type mockIBMClient struct {
	vpcClient *mockVPCClient
}

func (m *mockIBMClient) GetVPCClient() (*ibm.VPCClient, error) {
	// Return a mock that satisfies the interface
	return nil, nil // This mock doesn't implement the full VPCClient interface
}

// Mock VPC client for testing
type mockVPCClient struct {
	subnets   map[string]*vpcv1.Subnet
	instances map[string]*vpcv1.Instance
}

// Helper to create mock subnets for testing
func createMockSubnetsForZones(subnetNames []string) map[string]*vpcv1.Subnet {
	subnets := make(map[string]*vpcv1.Subnet)

	zoneMapping := map[string]string{
		"subnet-us-south-1": "us-south-1",
		"subnet-us-south-2": "us-south-2",
		"subnet-us-south-3": "us-south-3",
		"subnet-eu-de-1":    "eu-de-1",
		"subnet-eu-de-2":    "eu-de-2",
		"subnet-jp-tok-1a":  "jp-tok-1",
		"subnet-jp-tok-1b":  "jp-tok-1",
		"subnet-jp-tok-2":   "jp-tok-2",
		"subnet-jp-tok-3":   "jp-tok-3",
	}

	for i, name := range subnetNames {
		zone := zoneMapping[name]
		id := name
		status := "available"
		availableIPs := int64(100)
		totalIPs := int64(256)
		cidr := fmt.Sprintf("10.%d.0.0/24", i+1)

		subnets[name] = &vpcv1.Subnet{
			ID: &id,
			Zone: &vpcv1.ZoneReference{
				Name: &zone,
			},
			Status:                    &status,
			AvailableIpv4AddressCount: &availableIPs,
			TotalIpv4AddressCount:     &totalIPs,
			Ipv4CIDRBlock:             &cidr,
		}
	}

	return subnets
}

// Mock subnet provider for testing
type mockSubnetProvider struct {
	subnets map[string]*vpcv1.Subnet
}

func (m *mockSubnetProvider) GetSubnetInfo(ctx context.Context, subnetID string) (*subnet.SubnetInfo, error) {
	sub, ok := m.subnets[subnetID]
	if !ok {
		return nil, fmt.Errorf("subnet not found: %s", subnetID)
	}

	return &subnet.SubnetInfo{
		ID:           *sub.ID,
		Zone:         *sub.Zone.Name,
		State:        *sub.Status,
		AvailableIPs: int32(*sub.AvailableIpv4AddressCount),
	}, nil
}

func (m *mockSubnetProvider) ListSubnets(ctx context.Context, vpcID string) ([]subnet.SubnetInfo, error) {
	var result []subnet.SubnetInfo
	for _, sub := range m.subnets {
		result = append(result, subnet.SubnetInfo{
			ID:           *sub.ID,
			Zone:         *sub.Zone.Name,
			State:        *sub.Status,
			AvailableIPs: int32(*sub.AvailableIpv4AddressCount),
			TotalIPCount: int32(*sub.TotalIpv4AddressCount),
		})
	}
	return result, nil
}

func (m *mockSubnetProvider) SelectSubnets(ctx context.Context, vpcID string, strategy *v1alpha1.PlacementStrategy) ([]subnet.SubnetInfo, error) {
	subnets, err := m.ListSubnets(ctx, vpcID)
	if err != nil {
		return nil, err
	}

	// Apply placement strategy logic
	if strategy != nil && strategy.ZoneBalance == "Balanced" {
		// Group by zone and select one per zone
		zoneSubnets := make(map[string]subnet.SubnetInfo)
		for _, s := range subnets {
			if _, exists := zoneSubnets[s.Zone]; !exists {
				zoneSubnets[s.Zone] = s
			}
		}

		var result []subnet.SubnetInfo
		for _, s := range zoneSubnets {
			result = append(result, s)
		}
		return result, nil
	}

	return subnets, nil
}

func (m *mockSubnetProvider) GetSubnet(ctx context.Context, subnetID string) (*subnet.SubnetInfo, error) {
	return m.GetSubnetInfo(ctx, subnetID)
}

func (m *mockSubnetProvider) SetKubernetesClient(kubeClient kubernetes.Interface) {
	// No-op for mock
}
