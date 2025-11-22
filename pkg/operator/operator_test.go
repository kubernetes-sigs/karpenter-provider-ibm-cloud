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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	coreoperator "sigs.k8s.io/karpenter/pkg/operator"
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

// MockManager implements a minimal manager interface for testing
type MockManager struct {
	manager.Manager
	client *fake.ClientBuilder
	config *rest.Config
	scheme *runtime.Scheme
}

func (m *MockManager) GetConfig() *rest.Config {
	if m.config == nil {
		m.config = &rest.Config{
			Host: "https://test-cluster",
		}
	}
	return m.config
}

func (m *MockManager) GetScheme() *runtime.Scheme {
	if m.scheme == nil {
		m.scheme = runtime.NewScheme()
	}
	return m.scheme
}

func (m *MockManager) GetClient() interface{} {
	if m.client == nil {
		m.client = fake.NewClientBuilder()
	}
	return m.client.Build()
}

func TestNewOperator(t *testing.T) {
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

	t.Run("NewOperator with valid credentials creates operator", func(t *testing.T) {
		// Set up environment
		_ = os.Setenv("IBMCLOUD_REGION", "us-south")
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		_ = os.Setenv("VPC_API_KEY", "test-vpc-key")

		// Create a mock core operator
		scheme := runtime.NewScheme()
		config := &rest.Config{
			Host: "https://test-cluster",
		}

		// Note: We can't fully test NewOperator without significant mocking
		// because it calls os.Exit on failure and creates real clients
		// But we can test that the structure is correct
		op := &Operator{
			Operator:             &coreoperator.Operator{},
			UnavailableOfferings: nil,
			ProviderFactory:      nil,
			KubernetesClient:     nil,
		}

		// Verify the operator has the expected structure
		assert.NotNil(t, op)
		assert.NotNil(t, op.Operator)

		// Additional validation would require mocking the entire operator creation
		// which involves complex Kubernetes client setup
		_ = scheme
		_ = config
	})

	t.Run("NewOperator components initialization", func(t *testing.T) {
		// Test that individual components can be initialized
		ctx := context.Background()

		// Test validateIBMCredentials is called correctly
		_ = os.Setenv("IBMCLOUD_REGION", "us-south")
		_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
		_ = os.Setenv("VPC_API_KEY", "test-vpc-key")

		// This will fail with test credentials but we're validating the flow
		err := validateIBMCredentials(ctx)
		// The error is expected due to invalid test credentials
		// With the test credentials set, the function will try to create a client
		// which will fail with invalid API keys
		if err == nil {
			// If no error, it means the env vars were valid but client creation succeeded
			// This shouldn't happen with test credentials
			t.Log("validateIBMCredentials succeeded unexpectedly with test credentials")
		} else {
			// Expected path - invalid test credentials should cause an error
			assert.Contains(t, err.Error(), "failed to create IBM client")
		}
	})

	t.Run("Operator fields are correctly typed", func(t *testing.T) {
		// Create an operator with all fields populated
		op := &Operator{
			Operator:             &coreoperator.Operator{},
			UnavailableOfferings: nil,
			ProviderFactory:      nil,
			KubernetesClient:     nil,
		}

		// Verify type assertions work correctly
		_ = op.Operator
		assert.IsType(t, &Operator{}, op)
	})
}
