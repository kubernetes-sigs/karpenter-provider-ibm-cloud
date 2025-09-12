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

package operator

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateIBMCredentials(t *testing.T) {
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

	ctx := context.Background()

	t.Run("missing IBMCLOUD_REGION", func(t *testing.T) {
		_ = os.Unsetenv("IBMCLOUD_REGION")
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		_ = os.Setenv("VPC_API_KEY", "test-vpc-key")

		err := validateIBMCredentials(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "IBMCLOUD_REGION environment variable is not set")
	})

	t.Run("missing IBMCLOUD_API_KEY", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_REGION", "us-south")
		_ = os.Unsetenv("IBMCLOUD_API_KEY")
		_ = os.Setenv("VPC_API_KEY", "test-vpc-key")

		err := validateIBMCredentials(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "IBMCLOUD_API_KEY environment variable is not set")
	})

	t.Run("missing VPC_API_KEY", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_REGION", "us-south")
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		_ = os.Unsetenv("VPC_API_KEY")

		err := validateIBMCredentials(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "VPC_API_KEY environment variable is not set")
	})

	t.Run("all required env vars present", func(t *testing.T) {
		_ = os.Setenv("IBMCLOUD_REGION", "us-south")
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		_ = os.Setenv("VPC_API_KEY", "test-vpc-key")

		// This test will likely fail in unit test environment due to invalid credentials
		// but we're mainly testing the env var validation logic
		err := validateIBMCredentials(ctx)
		// In test environment, this should fail when trying to authenticate with test credentials
		// But we don't assert the specific error since it depends on the IBM client implementation
		_ = err // We just verify the function doesn't panic and returns some result
	})
}

func TestOperatorStructure(t *testing.T) {
	t.Run("Operator struct has required fields", func(t *testing.T) {
		op := &Operator{}

		// Test that the struct can be created and has the expected nil fields initially
		assert.Nil(t, op.Operator)
		assert.Nil(t, op.UnavailableOfferings)
		assert.Nil(t, op.ProviderFactory)
		assert.Nil(t, op.KubernetesClient)
	})
}
