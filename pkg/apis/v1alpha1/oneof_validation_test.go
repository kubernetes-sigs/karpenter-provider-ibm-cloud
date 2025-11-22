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
package v1alpha1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestOneOfValidation(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		nodeClass      *IBMNodeClass
		expectErrors   bool
		expectWarnings bool
		errorContains  string
	}{
		{
			name: "Valid configuration with static instance profile",
			nodeClass: &IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-static"},
				Spec: IBMNodeClassSpec{
					InstanceProfile:   "bx2-2x8",
					Image:             "r010-test-image",
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-test-vpc",
					Subnet:            "0717-test-subnet",
					ResourceGroup:     "test-resource-group-id",
					SecurityGroups:    []string{"r010-test-sg"},
					SSHKeys:           []string{"r010-test-key"},
					APIServerEndpoint: "https://test.example.com:6443",
					BootstrapMode:     "cloud-init",
				},
			},
			expectErrors:   false,
			expectWarnings: false,
		},
		{
			name: "Dynamic instance type selection (should warn)",
			nodeClass: &IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-dynamic"},
				Spec: IBMNodeClassSpec{
					// No InstanceProfile - enables dynamic selection
					Image:             "r010-test-image",
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-test-vpc",
					Subnet:            "0717-test-subnet",
					ResourceGroup:     "test-resource-group-id",
					SecurityGroups:    []string{"r010-test-sg"},
					SSHKeys:           []string{"r010-test-key"},
					APIServerEndpoint: "https://test.example.com:6443",
					BootstrapMode:     "cloud-init",
				},
			},
			expectErrors:   false,
			expectWarnings: true,
		},
		{
			name: "Missing resource group (should error)",
			nodeClass: &IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-missing-rg"},
				Spec: IBMNodeClassSpec{
					InstanceProfile: "bx2-2x8",
					Image:           "r010-test-image",
					Region:          "us-south",
					Zone:            "us-south-1",
					VPC:             "r010-test-vpc",
					Subnet:          "0717-test-subnet",
					// ResourceGroup missing
					SecurityGroups:    []string{"r010-test-sg"},
					SSHKeys:           []string{"r010-test-key"},
					APIServerEndpoint: "https://test.example.com:6443",
					BootstrapMode:     "cloud-init",
				},
			},
			expectErrors:  true,
			errorContains: "resourceGroup is required",
		},
		{
			name: "Invalid block device mapping (multiple root volumes)",
			nodeClass: &IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-invalid-bdm"},
				Spec: IBMNodeClassSpec{
					InstanceProfile:   "bx2-2x8",
					Image:             "r010-test-image",
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-test-vpc",
					Subnet:            "0717-test-subnet",
					ResourceGroup:     "test-resource-group-id",
					SecurityGroups:    []string{"r010-test-sg"},
					SSHKeys:           []string{"r010-test-key"},
					APIServerEndpoint: "https://test.example.com:6443",
					BootstrapMode:     "cloud-init",
					BlockDeviceMappings: []BlockDeviceMapping{
						{
							RootVolume: true,
							VolumeSpec: &VolumeSpec{
								Capacity: &[]int64{100}[0],
							},
						},
						{
							RootVolume: true, // ERROR: Second root volume
							VolumeSpec: &VolumeSpec{
								Capacity: &[]int64{200}[0],
							},
						},
					},
				},
			},
			expectErrors:  true,
			errorContains: "multiple root volumes specified",
		},
		{
			name: "Invalid zone format",
			nodeClass: &IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "test-invalid-zone"},
				Spec: IBMNodeClassSpec{
					InstanceProfile:   "bx2-2x8",
					Image:             "r010-test-image",
					Region:            "us-south",
					Zone:              "eu-de-1", // Wrong region for zone
					VPC:               "r010-test-vpc",
					Subnet:            "0717-test-subnet",
					ResourceGroup:     "test-resource-group-id",
					SecurityGroups:    []string{"r010-test-sg"},
					SSHKeys:           []string{"r010-test-key"},
					APIServerEndpoint: "https://test.example.com:6443",
					BootstrapMode:     "cloud-init",
				},
			},
			expectErrors:  true,
			errorContains: "zone 'eu-de-1' does not match region 'us-south'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings, err := tt.nodeClass.ValidateCreate(ctx, tt.nodeClass)

			if tt.expectErrors {
				assert.Error(t, err, "Expected validation errors but got none")
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains, "Error should contain expected text")
				}
			} else {
				assert.NoError(t, err, "Expected no validation errors but got: %v", err)
			}

			if tt.expectWarnings {
				assert.NotEmpty(t, warnings, "Expected validation warnings but got none")
			} else {
				assert.Empty(t, warnings, "Expected no validation warnings but got: %v", warnings)
			}

			t.Logf("Validation result: errors=%v, warnings=%v", err != nil, len(warnings) > 0)
			if err != nil {
				t.Logf("Error: %s", err.Error())
			}
			for i, warning := range warnings {
				t.Logf("Warning %d: %s", i+1, warning)
			}
		})
	}
}

func TestBlockDeviceMappingValidation(t *testing.T) {
	nodeClass := &IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "test-bdm"},
		Spec: IBMNodeClassSpec{
			InstanceProfile:   "bx2-2x8",
			Image:             "r010-test-image",
			Region:            "us-south",
			Zone:              "us-south-1",
			VPC:               "r010-test-vpc",
			Subnet:            "0717-test-subnet",
			ResourceGroup:     "test-resource-group-id",
			SecurityGroups:    []string{"r010-test-sg"},
			SSHKeys:           []string{"r010-test-key"},
			APIServerEndpoint: "https://test.example.com:6443",
			BootstrapMode:     "cloud-init",
		},
	}

	tests := []struct {
		name                string
		blockDeviceMappings []BlockDeviceMapping
		expectError         bool
		errorContains       string
	}{
		{
			name:                "No block device mappings (uses defaults)",
			blockDeviceMappings: nil,
			expectError:         false,
		},
		{
			name: "Valid single root volume",
			blockDeviceMappings: []BlockDeviceMapping{
				{
					RootVolume: true,
					VolumeSpec: &VolumeSpec{
						Capacity: &[]int64{100}[0],
						Profile:  &[]string{"general-purpose"}[0],
					},
				},
			},
			expectError: false,
		},
		{
			name: "Root volume with data volumes",
			blockDeviceMappings: []BlockDeviceMapping{
				{
					RootVolume: true,
					VolumeSpec: &VolumeSpec{
						Capacity: &[]int64{100}[0],
					},
				},
				{
					RootVolume: false,
					VolumeSpec: &VolumeSpec{
						Capacity: &[]int64{500}[0],
					},
				},
			},
			expectError: false,
		},
		{
			name: "No root volume marked",
			blockDeviceMappings: []BlockDeviceMapping{
				{
					RootVolume: false,
					VolumeSpec: &VolumeSpec{
						Capacity: &[]int64{100}[0],
					},
				},
			},
			expectError:   true,
			errorContains: "no root volume marked",
		},
		{
			name: "Invalid volume capacity (too large)",
			blockDeviceMappings: []BlockDeviceMapping{
				{
					RootVolume: true,
					VolumeSpec: &VolumeSpec{
						Capacity: &[]int64{20000}[0], // Too large for data volume
					},
				},
				{
					RootVolume: false,
					VolumeSpec: &VolumeSpec{
						Capacity: &[]int64{25000}[0], // Way too large
					},
				},
			},
			expectError:   true,
			errorContains: "outside valid range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set the block device mappings for this test
			testNodeClass := nodeClass.DeepCopy()
			testNodeClass.Spec.BlockDeviceMappings = tt.blockDeviceMappings

			warnings, err := testNodeClass.ValidateCreate(context.Background(), testNodeClass)

			if tt.expectError {
				require.Error(t, err, "Expected validation error but got none")
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains, "Error should contain expected text")
				}
			} else {
				assert.NoError(t, err, "Expected no validation errors but got: %v", err)
			}

			t.Logf("Block device mapping validation result: error=%v, warnings=%d", err != nil, len(warnings))
			if err != nil {
				t.Logf("Error: %s", err.Error())
			}
		})
	}
}
