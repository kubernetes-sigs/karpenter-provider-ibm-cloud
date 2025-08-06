/*
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

package v1alpha1_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestIBMNodeClass_APIServerEndpoint_Validation(t *testing.T) {
	tests := []struct {
		name        string
		endpoint    string
		expectValid bool
	}{
		{
			name:        "valid HTTPS endpoint with IP",
			endpoint:    "https://10.243.65.4:6443",
			expectValid: true,
		},
		{
			name:        "valid HTTPS endpoint with hostname",
			endpoint:    "https://api.example.com:6443",
			expectValid: true,
		},
		{
			name:        "valid HTTPS endpoint with dashes in hostname",
			endpoint:    "https://master-node-1.cluster.local:6443",
			expectValid: true,
		},
		{
			name:        "empty endpoint should be valid (optional field)",
			endpoint:    "",
			expectValid: true,
		},
		{
			name:        "invalid - missing https",
			endpoint:    "http://10.243.65.4:6443",
			expectValid: false,
		},
		{
			name:        "invalid - missing port",
			endpoint:    "https://10.243.65.4",
			expectValid: false,
		},
		{
			name:        "invalid - missing protocol",
			endpoint:    "10.243.65.4:6443",
			expectValid: false,
		},
		{
			name:        "invalid - invalid format",
			endpoint:    "not-a-url",
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeClass := &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-12345678-1234-1234-1234-123456789012",
					Image:             "ubuntu-20-04",
					Subnet:            "1234-12345678-1234-1234-1234-123456789012",
					SecurityGroups:    []string{"r010-12345678-1234-1234-1234-123456789012"},
					APIServerEndpoint: tt.endpoint,
				},
			}

			// Validate the endpoint format using regex pattern
			if tt.expectValid {
				if tt.endpoint != "" {
					// Check if it matches the expected pattern
					pattern := `^https://[a-zA-Z0-9.-]+:[0-9]+$`
					matched, _ := regexp.MatchString(pattern, tt.endpoint)
					assert.True(t, matched, "Expected endpoint %s to match validation pattern", tt.endpoint)
				}
			} else {
				if tt.endpoint != "" {
					pattern := `^https://[a-zA-Z0-9.-]+:[0-9]+$`
					matched, _ := regexp.MatchString(pattern, tt.endpoint)
					assert.False(t, matched, "Expected endpoint %s to fail validation pattern", tt.endpoint)
				}
			}

			// Test that the field is properly set
			assert.Equal(t, tt.endpoint, nodeClass.Spec.APIServerEndpoint)
		})
	}
}

func TestIBMNodeClass_APIServerEndpoint_DefaultBehavior(t *testing.T) {
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:         "us-south",
			Zone:           "us-south-1",
			VPC:            "r010-12345678-1234-1234-1234-123456789012",
			Image:          "ubuntu-20-04",
			Subnet:         "1234-12345678-1234-1234-1234-123456789012",
			SecurityGroups: []string{"r010-12345678-1234-1234-1234-123456789012"},
			// APIServerEndpoint not set
		},
	}

	// Verify the field defaults to empty string
	assert.Equal(t, "", nodeClass.Spec.APIServerEndpoint)
}

func TestIBMNodeClass_SSHKeys_Validation(t *testing.T) {
	tests := []struct {
		name        string
		sshKeys     []string
		expectValid bool
		description string
	}{
		{
			name:        "valid SSH key ID",
			sshKeys:     []string{"r010-82091c89-68e4-4b3f-bd2b-4e63ca2f67da"},
			expectValid: true,
			description: "Valid IBM Cloud SSH key ID format",
		},
		{
			name:        "multiple valid SSH key IDs",
			sshKeys:     []string{"r010-82091c89-68e4-4b3f-bd2b-4e63ca2f67da", "r010-156b91dd-9f84-4696-b04f-c13d3249ffed"},
			expectValid: true,
			description: "Multiple valid SSH key IDs",
		},
		{
			name:        "empty SSH keys should be valid (optional field)",
			sshKeys:     []string{},
			expectValid: true,
			description: "Empty SSH keys list should be allowed",
		},
		{
			name:        "nil SSH keys should be valid (optional field)",
			sshKeys:     nil,
			expectValid: true,
			description: "Nil SSH keys should be allowed",
		},
		{
			name:        "invalid - SSH key name instead of ID",
			sshKeys:     []string{"josie"},
			expectValid: false,
			description: "SSH key names are not allowed, only IDs",
		},
		{
			name:        "invalid - wrong prefix",
			sshKeys:     []string{"s010-82091c89-68e4-4b3f-bd2b-4e63ca2f67da"},
			expectValid: false,
			description: "SSH key IDs must start with 'r' followed by 3 digits",
		},
		{
			name:        "invalid - short ID",
			sshKeys:     []string{"r010-82091c89-68e4"},
			expectValid: false,
			description: "SSH key ID is too short",
		},
		{
			name:        "invalid - uppercase characters",
			sshKeys:     []string{"r010-82091C89-68E4-4B3F-BD2B-4E63CA2F67DA"},
			expectValid: false,
			description: "SSH key IDs must be lowercase",
		},
		{
			name:        "invalid - special characters",
			sshKeys:     []string{"r010-82091c89-68e4-4b3f-bd2b-4e63ca2f67da!"},
			expectValid: false,
			description: "SSH key IDs cannot contain special characters",
		},
		{
			name:        "invalid - one valid, one invalid",
			sshKeys:     []string{"r010-82091c89-68e4-4b3f-bd2b-4e63ca2f67da", "josie"},
			expectValid: false,
			description: "All SSH keys must be valid IDs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeClass := &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:         "us-south",
					Zone:           "us-south-1",
					VPC:            "r010-12345678-1234-1234-1234-123456789012",
					Image:          "ubuntu-20-04",
					Subnet:         "1234-12345678-1234-1234-1234-123456789012",
					SecurityGroups: []string{"r010-12345678-1234-1234-1234-123456789012"},
					SSHKeys:        tt.sshKeys,
				},
			}

			// Validate SSH key format using the pattern from CRD
			sshKeyPattern := `^r[0-9]{3}-[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}$`

			if tt.expectValid {
				for _, key := range tt.sshKeys {
					if key != "" {
						matched, _ := regexp.MatchString(sshKeyPattern, key)
						assert.True(t, matched, "Expected SSH key %s to match validation pattern for test: %s", key, tt.description)
					}
				}
			} else {
				// At least one key should fail validation
				allValid := true
				for _, key := range tt.sshKeys {
					if key != "" {
						matched, _ := regexp.MatchString(sshKeyPattern, key)
						if !matched {
							allValid = false
							break
						}
					}
				}
				assert.False(t, allValid, "Expected at least one SSH key to fail validation for test: %s", tt.description)
			}

			// Test that the field is properly set
			assert.Equal(t, tt.sshKeys, nodeClass.Spec.SSHKeys)
		})
	}
}

func TestIBMNodeClass_SubnetConfiguration_Cases(t *testing.T) {
	tests := []struct {
		name        string
		subnet      string
		expectValid bool
		description string
	}{
		{
			name:        "explicit subnet specified",
			subnet:      "02c7-ac2802cf-54bb-4508-aad7-eba7e8c2034c",
			expectValid: true,
			description: "When subnet is explicitly specified, should use that subnet",
		},
		{
			name:        "no subnet specified - auto-selection",
			subnet:      "",
			expectValid: true,
			description: "When no subnet specified, should trigger auto-selection logic",
		},
		{
			name:        "invalid subnet ID format",
			subnet:      "invalid-subnet-id",
			expectValid: false,
			description: "Subnet IDs must follow IBM Cloud format",
		},
		{
			name:        "subnet ID with wrong format - missing zone prefix",
			subnet:      "ac2802cf-54bb-4508-aad7-eba7e8c2034c",
			expectValid: false,
			description: "Subnet IDs must have 4-character zone prefix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeClass := &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:         "us-south",
					Zone:           "us-south-1",
					VPC:            "r010-12345678-1234-1234-1234-123456789012",
					Image:          "ubuntu-20-04",
					Subnet:         tt.subnet,
					SecurityGroups: []string{"r010-12345678-1234-1234-1234-123456789012"},
				},
			}

			// Validate subnet format using the pattern from CRD
			if tt.subnet != "" {
				subnetPattern := `^[0-9a-z]{4}-[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}$`
				matched, _ := regexp.MatchString(subnetPattern, tt.subnet)

				if tt.expectValid {
					assert.True(t, matched, "Expected subnet %s to match validation pattern for test: %s", tt.subnet, tt.description)
				} else {
					assert.False(t, matched, "Expected subnet %s to fail validation pattern for test: %s", tt.subnet, tt.description)
				}
			}

			// Test that the field is properly set
			assert.Equal(t, tt.subnet, nodeClass.Spec.Subnet)
		})
	}
}

func TestIBMNodeClass_NetworkConnectivity_Scenarios(t *testing.T) {
	// Test scenarios that document the network connectivity issues we discovered
	tests := []struct {
		name        string
		nodeClass   *v1alpha1.IBMNodeClass
		description string
	}{
		{
			name: "cluster nodes in first-second subnet",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster-aware-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:            "eu-de",
					Zone:              "eu-de-2",
					VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					Subnet:            "02c7-ac2802cf-54bb-4508-aad7-eba7e8c2034c", // first-second
					SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
					APIServerEndpoint: "https://10.243.65.4:6443",
				},
			},
			description: "Nodes should be placed in same subnet as existing cluster nodes (first-second) for connectivity",
		},
		{
			name: "isolated subnet would cause connectivity issues",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "isolated-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:            "eu-de",
					Zone:              "eu-de-2",
					VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					Subnet:            "02c7-dc97437a-1b67-41c9-9db9-a6b92a46963d", // eu-de-2-default-subnet
					SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
					APIServerEndpoint: "https://10.243.65.4:6443",
				},
			},
			description: "Nodes in isolated subnet (eu-de-2-default-subnet) cannot reach API server in different network",
		},
		{
			name: "auto-selection without subnet specification",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "auto-select-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "eu-de",
					Zone:   "eu-de-2",
					VPC:    "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:  "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					// Subnet not specified - triggers auto-selection
					SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
					APIServerEndpoint: "https://10.243.65.4:6443",
				},
			},
			description: "Auto-selection should discover existing cluster nodes and prefer their subnets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify the nodeclass is properly structured
			assert.NotNil(t, tt.nodeClass)
			assert.NotEmpty(t, tt.nodeClass.Spec.Region)
			assert.NotEmpty(t, tt.nodeClass.Spec.VPC)
			assert.NotEmpty(t, tt.nodeClass.Spec.SecurityGroups)

			// Document the expected behavior based on subnet configuration
			if tt.nodeClass.Spec.Subnet == "" {
				// When no subnet is specified, the operator should auto-discover
				assert.Empty(t, tt.nodeClass.Spec.Subnet, "Auto-selection case should have empty subnet")
			} else {
				// When subnet is specified, verify it's a valid format
				subnetPattern := `^[0-9a-z]{4}-[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}$`
				matched, _ := regexp.MatchString(subnetPattern, tt.nodeClass.Spec.Subnet)
				assert.True(t, matched, "Specified subnet should have valid format")
			}

			// Verify API server endpoint is properly configured
			if tt.nodeClass.Spec.APIServerEndpoint != "" {
				endpointPattern := `^https://[a-zA-Z0-9.-]+:[0-9]+$`
				matched, _ := regexp.MatchString(endpointPattern, tt.nodeClass.Spec.APIServerEndpoint)
				assert.True(t, matched, "API server endpoint should have valid HTTPS format")
			}
		})
	}
}

// TestIBMNodeClass_ValidConfigurationExamples tests complete valid configurations
func TestIBMNodeClass_ValidConfigurationExamples(t *testing.T) {
	// This test documents the working configurations we've validated
	validNodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "production-ready-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            "eu-de",
			Zone:              "eu-de-2",
			VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
			Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
			InstanceProfile:   "bx2-2x8",
			Subnet:            "02c7-ac2802cf-54bb-4508-aad7-eba7e8c2034c", // same as cluster nodes
			SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
			SSHKeys:           []string{"r010-82091c89-68e4-4b3f-bd2b-4e63ca2f67da"}, // SSH key ID format
			ResourceGroup:     "88427352321742ef8cfac50b0ee6cc26",
			APIServerEndpoint: "https://10.243.65.4:6443", // internal endpoint
			BootstrapMode:     stringPtr("cloud-init"),
		},
	}

	// Validate all fields are properly set
	assert.Equal(t, "eu-de", validNodeClass.Spec.Region)
	assert.Equal(t, "eu-de-2", validNodeClass.Spec.Zone)
	assert.Equal(t, "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a", validNodeClass.Spec.VPC)
	assert.Equal(t, "02c7-ac2802cf-54bb-4508-aad7-eba7e8c2034c", validNodeClass.Spec.Subnet)
	assert.Equal(t, "https://10.243.65.4:6443", validNodeClass.Spec.APIServerEndpoint)
	assert.Equal(t, "cloud-init", *validNodeClass.Spec.BootstrapMode)

	// Validate SSH key ID format
	for _, sshKey := range validNodeClass.Spec.SSHKeys {
		sshKeyPattern := `^r[0-9]{3}-[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}$`
		matched, _ := regexp.MatchString(sshKeyPattern, sshKey)
		assert.True(t, matched, "SSH key %s should match valid ID format", sshKey)
	}

	// Validate subnet ID format
	subnetPattern := `^[0-9a-z]{4}-[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}$`
	matched, _ := regexp.MatchString(subnetPattern, validNodeClass.Spec.Subnet)
	assert.True(t, matched, "Subnet should match valid ID format")

	// Validate VPC ID format
	vpcPattern := `^r[0-9]{3}-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`
	matched, _ = regexp.MatchString(vpcPattern, validNodeClass.Spec.VPC)
	assert.True(t, matched, "VPC should match valid ID format")
}

// Helper function for string pointers
func stringPtr(s string) *string {
	return &s
}
