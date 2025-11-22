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

package v1alpha1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIBMNodeClass_ValidateCreate(t *testing.T) {
	tests := []struct {
		name         string
		nodeClass    *IBMNodeClass
		wantErr      bool
		errContains  []string
		wantWarnings []string
	}{
		{
			name: "customer configuration - missing api server endpoint",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region:         "br-sao",
					Zone:           "br-sao-2",
					VPC:            "r042-4225852b-4846-4a4a-88c4-9966471337c6",
					Image:          "ibm-ubuntu-22-04-5-minimal-amd64-6",
					Subnet:         "02u7-718345b5-2de1-4a9a-b1de-fa7e307ee8c5",
					SecurityGroups: []string{"sg-k8s-workers"}, // Name instead of ID
					ResourceGroup:  "karpenter-rg",
					// Missing APIServerEndpoint
				},
			},
			wantErr: true,
			errContains: []string{
				"apiServerEndpoint is required",
				"security group 'sg-k8s-workers' appears to be a name",
			},
			wantWarnings: []string{
				"image 'ibm-ubuntu-22-04-5-minimal-amd64-6' appears to be a name",
				"bootstrapMode not specified",
			},
		},
		{
			name: "valid configuration with IDs",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region:            "eu-de",
					Zone:              "eu-de-2",
					VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					Subnet:            "02c7-ac2802cf-54bb-4508-aad7-eba7e8c2034c",
					SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
					APIServerEndpoint: "https://10.243.65.4:6443",
					BootstrapMode:     stringPtr("cloud-init"),
				},
			},
			wantErr:      false,
			errContains:  []string{},
			wantWarnings: []string{},
		},
		{
			name: "invalid security group format - too short",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:             "ubuntu-20-04",
					SecurityGroups:    []string{"r010-short"},
					APIServerEndpoint: "https://10.0.0.1:6443",
				},
			},
			wantErr: true,
			errContains: []string{
				"security group 'r010-short' is not a valid IBM Cloud resource ID",
			},
			wantWarnings: []string{
				"image 'ubuntu-20-04' appears to be a name",
			},
		},
		{
			name: "invalid api server endpoint format",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
					APIServerEndpoint: "10.0.0.1:6443", // Missing https://
				},
			},
			wantErr: true,
			errContains: []string{
				"apiServerEndpoint '10.0.0.1:6443' is not a valid URL",
			},
		},
		{
			name: "invalid subnet format",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					Subnet:            "my-subnet", // Invalid format
					SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
					APIServerEndpoint: "https://10.0.0.1:6443",
				},
			},
			wantErr: true,
			errContains: []string{
				"subnet 'my-subnet' is not a valid IBM Cloud subnet ID",
			},
		},
		{
			name: "invalid bootstrap mode",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
					APIServerEndpoint: "https://10.0.0.1:6443",
					BootstrapMode:     stringPtr("invalid-mode"),
				},
			},
			wantErr: true,
			errContains: []string{
				"invalid bootstrapMode 'invalid-mode'",
			},
		},
		{
			name: "missing VPC",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region: "us-south",
					Zone:   "us-south-1",
					// Missing VPC
					Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
					APIServerEndpoint: "https://10.0.0.1:6443",
				},
			},
			wantErr: true,
			errContains: []string{
				"vpc is required",
			},
		},
		{
			name: "no security groups - should warn",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					SecurityGroups:    []string{}, // Empty
					APIServerEndpoint: "https://10.0.0.1:6443",
				},
			},
			wantErr: false,
			wantWarnings: []string{
				"no security groups specified",
			},
		},
		{
			name: "invalid SSH key format",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
					SSHKeys:           []string{"my-ssh-key"}, // Invalid format
					APIServerEndpoint: "https://10.0.0.1:6443",
				},
			},
			wantErr: true,
			errContains: []string{
				"SSH key 'my-ssh-key' is not a valid IBM Cloud resource ID",
			},
		},
		{
			name: "valid configuration with different region number lengths",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region:            "br-sao",
					Zone:              "br-sao-2",
					VPC:               "r042-4225852b-4846-4a4a-88c4-9966471337c6", // r042 (4 digits)
					Image:             "r006-dd3c20fa-71d3-4dc0-913f-2f097bf3e500", // r006 (3 digits)
					Subnet:            "02c7-ac2802cf-54bb-4508-aad7-eba7e8c2034c",
					SecurityGroups:    []string{"r050-36f045e2-86a1-4af8-917e-b17a41f8abe3"}, // r050 (3 digits)
					APIServerEndpoint: "https://172.21.0.1:6443",
					BootstrapMode:     stringPtr("cloud-init"),
				},
			},
			wantErr:      false,
			errContains:  []string{},
			wantWarnings: []string{},
		},
		{
			name: "http endpoint should be allowed",
			nodeClass: &IBMNodeClass{
				Spec: IBMNodeClassSpec{
					Region:            "us-south",
					Zone:              "us-south-1",
					VPC:               "r010-2b1c3cdc-a678-4eda-86af-731130de1c0a",
					Image:             "r010-dd3c20fa-71d3-4dc0-913f-2f097bf3e500",
					SecurityGroups:    []string{"r010-36f045e2-86a1-4af8-917e-b17a41f8abe3"},
					APIServerEndpoint: "http://10.0.0.1:6443", // HTTP should be allowed
				},
			},
			wantErr:      false,
			errContains:  []string{},
			wantWarnings: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			warnings, err := tt.nodeClass.ValidateCreate(ctx, tt.nodeClass)

			if tt.wantErr {
				assert.Error(t, err)
				for _, contains := range tt.errContains {
					assert.Contains(t, err.Error(), contains)
				}
			} else {
				assert.NoError(t, err)
			}

			// Check warnings
			assert.Equal(t, len(tt.wantWarnings), len(warnings))
			for i, warning := range tt.wantWarnings {
				if i < len(warnings) {
					assert.Contains(t, string(warnings[i]), warning)
				}
			}
		})
	}
}

func TestIBMNodeClass_ValidateUpdate(t *testing.T) {
	ctx := context.Background()
	nodeClass := &IBMNodeClass{
		Spec: IBMNodeClassSpec{
			Region:         "br-sao",
			Zone:           "br-sao-2",
			VPC:            "r042-4225852b-4846-4a4a-88c4-9966471337c6",
			Image:          "ibm-ubuntu-22-04-5-minimal-amd64-5",
			Subnet:         "02u7-718345b5-2de1-4a9a-b1de-fa7e307ee8c5",
			SecurityGroups: []string{"sg-k8s-workers"},
			ResourceGroup:  "karpenter-rg",
			// Missing APIServerEndpoint
		},
	}

	warnings, err := nodeClass.ValidateUpdate(ctx, nil, nodeClass)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "apiServerEndpoint is required")
	assert.Contains(t, err.Error(), "security group 'sg-k8s-workers' is not a valid IBM Cloud resource ID")
	assert.True(t, len(warnings) > 0)
}

func TestIBMNodeClass_ValidateDelete(t *testing.T) {
	ctx := context.Background()
	nodeClass := &IBMNodeClass{}
	warnings, err := nodeClass.ValidateDelete(ctx, nodeClass)
	assert.NoError(t, err)
	assert.Empty(t, warnings)
}

// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}
