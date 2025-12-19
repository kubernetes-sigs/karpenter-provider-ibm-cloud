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
package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func TestParseInstanceID(t *testing.T) {
	tests := []struct {
		name        string
		providerID  string
		expectedID  string
		expectError bool
	}{
		{
			name:        "valid provider ID",
			providerID:  "ibm://us-south-1/r006-12345678-1234-1234-1234-123456789012",
			expectedID:  "ibm://us-south-1/r006-12345678-1234-1234-1234-123456789012",
			expectError: false,
		},
		{
			name:        "simple instance ID",
			providerID:  "r006-instance-id",
			expectedID:  "r006-instance-id",
			expectError: false,
		},
		{
			name:        "empty provider ID",
			providerID:  "",
			expectedID:  "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseInstanceID(tt.providerID)

			if tt.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "provider ID is empty")
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedID, result)
			}
		})
	}
}

func TestGetAllSingleValuedRequirementLabels(t *testing.T) {
	tests := []struct {
		name         string
		instanceType *cloudprovider.InstanceType
		expected     map[string]string
	}{
		{
			name: "empty requirements",
			instanceType: &cloudprovider.InstanceType{
				Requirements: nil,
			},
			expected: map[string]string{},
		},
		{
			name:         "nil instanceType",
			instanceType: nil,
			expected:     map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetAllSingleValuedRequirementLabels(tt.instanceType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetAllSingleValuedRequirementLabelsNil(t *testing.T) {
	// Test with nil instanceType (should not panic)
	assert.NotPanics(t, func() {
		result := GetAllSingleValuedRequirementLabels(nil)
		assert.Equal(t, map[string]string{}, result)
	})
}
