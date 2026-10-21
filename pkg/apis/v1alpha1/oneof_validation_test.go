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
)

func TestOneOfValidation(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name            string
		configure       func(*IBMNodeClass)
		errorContains   string
		warningContains string
	}{
		{name: "valid configuration with static instance profile"},
		{
			name:            "dynamic instance type selection",
			configure:       func(nc *IBMNodeClass) { nc.Spec.InstanceProfile = "" },
			warningContains: "Dynamic instance type selection detected",
		},
		{
			name:          "missing resource group",
			configure:     func(nc *IBMNodeClass) { nc.Spec.ResourceGroup = "" },
			errorContains: "resourceGroup is required",
		},
		{
			name: "multiple root volumes",
			configure: func(nc *IBMNodeClass) {
				nc.Spec.BlockDeviceMappings = []BlockDeviceMapping{
					{RootVolume: true, VolumeSpec: &VolumeSpec{Capacity: &[]int64{100}[0]}},
					{RootVolume: true, VolumeSpec: &VolumeSpec{Capacity: &[]int64{200}[0]}},
				}
			},
			errorContains: "multiple root volumes specified",
		},
		{
			name:          "zone from another region",
			configure:     func(nc *IBMNodeClass) { nc.Spec.Zone = "eu-de-1" },
			errorContains: "zone 'eu-de-1' does not match region 'us-south'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nc := validWebhookNodeClass()
			if tt.configure != nil {
				tt.configure(nc)
			}
			warnings, err := nc.ValidateCreate(ctx, nc)
			if tt.errorContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				assert.NoError(t, err)
			}
			if tt.warningContains != "" {
				require.Len(t, warnings, 1)
				assert.Contains(t, warnings[0], tt.warningContains)
			} else {
				assert.Empty(t, warnings)
			}
		})
	}
}

func TestBlockDeviceMappingValidation(t *testing.T) {
	nodeClass := validWebhookNodeClass()

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
			name: "Data volumes retain default root",
			blockDeviceMappings: []BlockDeviceMapping{
				{
					RootVolume: false,
					VolumeSpec: &VolumeSpec{
						Capacity: &[]int64{100}[0],
					},
				},
			},
			expectError: false,
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
