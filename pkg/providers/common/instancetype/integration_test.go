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
	"os"
	"testing"
	"time"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
)

// TestRealInstanceTypeProvider tests the instance type provider with real IBM Cloud API
func TestRealInstanceTypeProvider(t *testing.T) {
	// Skip if no real credentials provided
	ibmAPIKey := os.Getenv("IBM_API_KEY")
	vpcAPIKey := os.Getenv("VPC_API_KEY")
	region := os.Getenv("IBM_REGION")

	if ibmAPIKey == "" || vpcAPIKey == "" || region == "" {
		t.Skip("Skipping real API test - IBM_API_KEY, VPC_API_KEY, or IBM_REGION not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create instance type provider with real credentials
	client, err := ibm.NewClient()
	if err != nil {
		t.Fatalf("Failed to create IBM client: %v", err)
	}

	pricingProvider := pricing.NewIBMPricingProvider(client)
	provider := NewProvider(client, pricingProvider)

	t.Logf("✅ Instance type provider created successfully")

	// Test List function
	t.Run("list_instance_types", func(t *testing.T) {
		instanceTypes, err := provider.List(ctx)
		if err != nil {
			t.Errorf("Failed to list instance types: %v", err)
			return
		}

		t.Logf("✅ Successfully listed %d instance types", len(instanceTypes))

		if len(instanceTypes) == 0 {
			t.Error("Expected at least some instance types, got 0")
			return
		}

		// Log first few instance types
		maxToLog := 5
		if len(instanceTypes) < maxToLog {
			maxToLog = len(instanceTypes)
		}

		for i := 0; i < maxToLog; i++ {
			it := instanceTypes[i]
			t.Logf("Instance type %d: Name=%s, CPU=%s, Memory=%s",
				i+1, it.Name,
				it.Capacity.Cpu().String(),
				it.Capacity.Memory().String())
		}
	})

	// Test Get function with a known instance type
	t.Run("get_instance_type", func(t *testing.T) {
		// Try to get a common instance type
		knownInstanceType := "bx2-2x8"

		instanceType, err := provider.Get(ctx, knownInstanceType)
		if err != nil {
			t.Errorf("Failed to get instance type %s: %v", knownInstanceType, err)
			return
		}

		if instanceType == nil {
			t.Errorf("Got nil instance type for %s", knownInstanceType)
			return
		}

		t.Logf("✅ Successfully retrieved instance type %s", knownInstanceType)
		t.Logf("Details: CPU=%s, Memory=%s",
			instanceType.Capacity.Cpu().String(),
			instanceType.Capacity.Memory().String())

		// Verify basic properties
		if instanceType.Name != knownInstanceType {
			t.Errorf("Expected name %s, got %s", knownInstanceType, instanceType.Name)
		}

		if instanceType.Capacity.Cpu().IsZero() {
			t.Error("Expected non-zero CPU capacity")
		}

		if instanceType.Capacity.Memory().IsZero() {
			t.Error("Expected non-zero memory capacity")
		}
	})

	// Test Get function with non-existent instance type
	t.Run("get_nonexistent_instance_type", func(t *testing.T) {
		nonExistentType := "nonexistent-instance-type"

		instanceType, err := provider.Get(ctx, nonExistentType)
		if err == nil {
			t.Errorf("Expected error for non-existent instance type %s, but got success", nonExistentType)
			return
		}

		if instanceType != nil {
			t.Errorf("Expected nil instance type for non-existent type, but got: %v", instanceType)
			return
		}

		t.Logf("✅ Correctly returned error for non-existent instance type: %v", err)
	})
}
