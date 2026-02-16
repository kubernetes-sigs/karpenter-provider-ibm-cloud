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
package ibm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
)

// TestRealVPCConnection tests actual connection to IBM Cloud VPC API
func TestRealVPCConnection(t *testing.T) {
	// Skip if no real credentials provided
	vpcAPIKey := os.Getenv("VPC_API_KEY")
	region := os.Getenv("IBM_REGION")
	testVPCID := os.Getenv("TEST_VPC_ID")

	if vpcAPIKey == "" || region == "" {
		t.Skip("Skipping real API test - VPC_API_KEY or IBM_REGION not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create VPC client with real credentials
	client, err := NewVPCClient("", "iam", vpcAPIKey, region, "")
	if err != nil {
		t.Fatalf("Failed to create VPC client: %v", err)
	}

	t.Logf("VPC client created successfully")

	// Test ListInstances
	t.Run("list_instances", func(t *testing.T) {
		instances, err := client.ListInstances(ctx)
		if err != nil {
			t.Errorf("Failed to list instances: %v", err)
			return
		}
		t.Logf("Successfully listed %d instances", len(instances))

		if len(instances) > 0 {
			t.Logf("First instance: ID=%s, Name=%s",
				getStringValue(instances[0].ID),
				getStringValue(instances[0].Name))
		}
	})

	// Test GetVPC if VPC ID provided
	if testVPCID != "" {
		t.Run("get_vpc", func(t *testing.T) {
			vpc, err := client.GetVPC(ctx, testVPCID, "")
			if err != nil {
				t.Errorf("Failed to get VPC %s: %v", testVPCID, err)
				return
			}
			t.Logf("Successfully retrieved VPC: ID=%s, Name=%s",
				getStringValue(vpc.ID),
				getStringValue(vpc.Name))
		})

		t.Run("list_subnets", func(t *testing.T) {
			subnets, err := client.ListSubnets(ctx, testVPCID)
			if err != nil {
				t.Errorf("Failed to list subnets for VPC %s: %v", testVPCID, err)
				return
			}
			t.Logf("Successfully listed %d subnets for VPC %s", len(subnets.Subnets), testVPCID)

			if len(subnets.Subnets) > 0 {
				subnet := subnets.Subnets[0]
				t.Logf("First subnet: ID=%s, Name=%s, Zone=%s",
					getStringValue(subnet.ID),
					getStringValue(subnet.Name),
					getStringValue(subnet.Zone.Name))
			}
		})
	}

	// Test ListInstanceProfiles
	t.Run("list_instance_profiles", func(t *testing.T) {
		options := &vpcv1.ListInstanceProfilesOptions{}
		profiles, _, err := client.ListInstanceProfiles(options)
		if err != nil {
			t.Errorf("Failed to list instance profiles: %v", err)
			return
		}

		profileCount := 0
		if profiles != nil && profiles.Profiles != nil {
			profileCount = len(profiles.Profiles)
		}

		t.Logf("Successfully listed %d instance profiles", profileCount)

		if profileCount > 0 {
			profile := profiles.Profiles[0]
			t.Logf("First profile: Name=%s, Family=%s",
				getStringValue(profile.Name),
				getStringValue(profile.Family))
		}
	})
}

// Helper function to safely get string value from pointer
func getStringValue(ptr *string) string {
	if ptr == nil {
		return "<nil>"
	}
	return *ptr
}

// TestRealGlobalCatalogConnection tests actual connection to IBM Cloud Global Catalog API
func TestRealGlobalCatalogConnection(t *testing.T) {
	// Skip if no real credentials provided
	ibmAPIKey := os.Getenv("IBM_API_KEY")
	region := os.Getenv("IBM_REGION")

	if ibmAPIKey == "" || region == "" {
		t.Skip("Skipping real API test - IBM_API_KEY or IBM_REGION not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create IBM client with real credentials
	client, err := NewClient()
	if err != nil {
		t.Fatalf("Failed to create IBM client: %v", err)
	}

	t.Logf("IBM client created successfully")

	// Test Global Catalog client
	t.Run("get_catalog_client", func(t *testing.T) {
		catalogClient, err := client.GetGlobalCatalogClient()
		if err != nil {
			t.Errorf("Failed to get Global Catalog client: %v", err)
			return
		}
		t.Logf("Global Catalog client created successfully")

		// Test listing instance types
		instanceTypes, err := catalogClient.ListInstanceTypes(ctx)
		if err != nil {
			t.Errorf("Failed to list instance types: %v", err)
			return
		}
		t.Logf("Successfully listed %d instance types from catalog", len(instanceTypes))

		if len(instanceTypes) > 0 {
			entry := instanceTypes[0]
			t.Logf("First instance type: Name=%s, ID=%s",
				getStringValue(entry.Name),
				getStringValue(entry.ID))
		}
	})
}
