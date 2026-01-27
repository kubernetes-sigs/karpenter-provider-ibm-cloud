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

	"github.com/stretchr/testify/assert"
)

func TestNewController(t *testing.T) {
	// Save original environment variables
	originalRegion := os.Getenv("IBMCLOUD_REGION")
	originalAPIKey := os.Getenv("IBMCLOUD_API_KEY")
	originalVPCKey := os.Getenv("VPC_API_KEY")

	// Restore them after test
	defer func() {
		_ = os.Setenv("IBMCLOUD_REGION", originalRegion)
		_ = os.Setenv("IBMCLOUD_API_KEY", originalAPIKey)
		_ = os.Setenv("VPC_API_KEY", originalVPCKey)
	}()

	tests := []struct {
		name          string
		setupEnv      func()
		expectError   bool
		errorContains string
	}{
		{
			name: "successful creation with valid env",
			setupEnv: func() {
				_ = os.Setenv("IBMCLOUD_REGION", "us-south")
				_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
				_ = os.Setenv("VPC_API_KEY", "test-vpc-key")
			},
			expectError:   false, // With env vars set, NewController succeeds (doesn't validate creds)
			errorContains: "",
		},
		{
			name: "missing IBMCLOUD_REGION",
			setupEnv: func() {
				_ = os.Unsetenv("IBMCLOUD_REGION")
				_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
				_ = os.Setenv("VPC_API_KEY", "test-vpc-key")
			},
			expectError:   true,
			errorContains: "creating IBM client",
		},
		{
			name: "missing IBMCLOUD_API_KEY",
			setupEnv: func() {
				_ = os.Setenv("IBMCLOUD_REGION", "us-south")
				_ = os.Unsetenv("IBMCLOUD_API_KEY")
				_ = os.Setenv("VPC_API_KEY", "test-vpc-key")
			},
			expectError:   true,
			errorContains: "creating IBM client",
		},
		{
			name: "missing VPC_API_KEY",
			setupEnv: func() {
				_ = os.Setenv("IBMCLOUD_REGION", "us-south")
				_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
				_ = os.Unsetenv("VPC_API_KEY")
			},
			expectError:   true,
			errorContains: "creating IBM client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnv()

			controller, err := NewController(context.Background())

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" && err != nil {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Nil(t, controller)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, controller)
				if controller != nil {
					assert.NotNil(t, controller.instanceTypeProvider)
				}
			}
		})
	}
}

func TestControllerStructure(t *testing.T) {
	// Test that the Controller struct can be created
	controller := &Controller{}
	assert.NotNil(t, controller)
	assert.Nil(t, controller.instanceTypeProvider)

	// Test that we can set the provider
	controller = &Controller{
		instanceTypeProvider: nil, // Would normally be a real provider
	}
	assert.NotNil(t, controller)
}
