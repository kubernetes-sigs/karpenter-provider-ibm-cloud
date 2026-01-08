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
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// MockCredentialStore implements SecureCredentialManager for testing
type MockCredentialStore struct {
	vpcAPIKey string
	ibmAPIKey string
	region    string
	vpcErr    error
}

func (m *MockCredentialStore) GetVPCAPIKey(ctx context.Context) (string, error) {
	if m.vpcErr != nil {
		return "", m.vpcErr
	}
	return m.vpcAPIKey, nil
}

func (m *MockCredentialStore) GetIBMAPIKey(ctx context.Context) (string, error) {
	return m.ibmAPIKey, nil
}

func (m *MockCredentialStore) GetRegion(ctx context.Context) (string, error) {
	return m.region, nil
}

func (m *MockCredentialStore) RotateCredentials(ctx context.Context) error {
	return nil
}

func (m *MockCredentialStore) ClearCredentials() {
	// No-op for mock
}

func TestNewClient(t *testing.T) {
	tests := []struct {
		name      string
		envVars   map[string]string
		wantError bool
	}{
		{
			name: "valid configuration",
			envVars: map[string]string{
				"VPC_API_KEY":      "test-vpc-key",
				"IBMCLOUD_API_KEY": "test-ibm-key",
				"IBMCLOUD_REGION":  "us-south",
			},
			wantError: false,
		},
		{
			name: "valid configuration with custom VPC URL",
			envVars: map[string]string{
				"VPC_URL":          "https://custom.vpc.url/v1",
				"VPC_API_KEY":      "test-vpc-key",
				"IBMCLOUD_API_KEY": "test-ibm-key",
				"IBMCLOUD_REGION":  "us-south",
			},
			wantError: false,
		},
		{
			name: "missing VPC API key",
			envVars: map[string]string{
				"IBMCLOUD_API_KEY": "test-ibm-key",
				"IBMCLOUD_REGION":  "us-south",
			},
			wantError: true,
		},
		{
			name: "missing IBM API key",
			envVars: map[string]string{
				"VPC_API_KEY":     "test-vpc-key",
				"IBMCLOUD_REGION": "us-south",
			},
			wantError: true,
		},
		{
			name: "missing region",
			envVars: map[string]string{
				"VPC_API_KEY":      "test-vpc-key",
				"IBMCLOUD_API_KEY": "test-ibm-key",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear environment before each test
			os.Clearenv()

			// Set environment variables for this test
			for k, v := range tt.envVars {
				_ = os.Setenv(k, v)
			}

			// Run the test
			client, err := NewClient()

			// Check error expectation
			if tt.wantError && err == nil {
				t.Error("expected error but got nil")
			}
			if !tt.wantError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			// Additional checks for successful cases
			if err == nil {
				if client.vpcURL == "" {
					t.Error("vpcURL should not be empty")
				}
				if client.vpcAuthType == "" {
					t.Error("vpcAuthType should not be empty")
				}
				if client.iamClient == nil {
					t.Error("iamClient should not be nil")
				}
				if client.GetRegion() != tt.envVars["IBMCLOUD_REGION"] {
					t.Errorf("expected region %s, got %s", tt.envVars["IBMCLOUD_REGION"], client.GetRegion())
				}
			}
		})
	}
}

func TestGetVPCClient(t *testing.T) {
	// Create a mock credential store for testing
	mockCredStore := &MockCredentialStore{
		vpcAPIKey: "test-key",
		ibmAPIKey: "test-ibm-key",
		region:    "us-south",
	}

	client := &Client{
		vpcURL:      "https://test.vpc.url/v1",
		vpcAuthType: "iam",
		credStore:   mockCredStore,
		region:      "us-south",
	}

	ctx := context.Background()

	vpcClient1, err1 := client.GetVPCClient(ctx)
	assert.NoError(t, err1)
	assert.NotNil(t, vpcClient1)

	vpcClient2, err2 := client.GetVPCClient(ctx)
	assert.NoError(t, err2)
	assert.NotNil(t, vpcClient2)

	// Same cached instance (sync.Once)
	assert.Same(t, vpcClient1, vpcClient2)
}

func TestGetVPCClient_RetriesAfterCredentialFailure(t *testing.T) {
	mockCredStore := &MockCredentialStore{
		vpcErr: fmt.Errorf("transient error"),
	}

	client := &Client{
		vpcURL:      "https://test.vpc.url/v1",
		vpcAuthType: "iam",
		credStore:   mockCredStore,
		region:      "us-south",
	}

	ctx := context.Background()

	// First call fails
	vpcClient1, err1 := client.GetVPCClient(ctx)
	assert.Error(t, err1)
	assert.Nil(t, vpcClient1)

	// Flip mock to succeed
	mockCredStore.vpcErr = nil
	mockCredStore.vpcAPIKey = "test-key"

	// Second call succeeds (proves error not cached)
	vpcClient2, err2 := client.GetVPCClient(ctx)
	assert.NoError(t, err2)
	assert.NotNil(t, vpcClient2)
}

func TestClient_GetVPCClient_ConcurrentCalls_SameInstance(t *testing.T) {
	mock := &MockCredentialStore{vpcAPIKey: "test-key"}

	c := &Client{
		vpcURL:      "https://test.vpc.url/v1",
		vpcAuthType: "iam",
		credStore:   mock,
		region:      "us-south",
	}

	ctx := context.Background()

	const n = 25
	var wg sync.WaitGroup
	wg.Add(n)

	results := make([]*VPCClient, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.GetVPCClient(ctx)
		}()
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		assert.NoError(t, errs[i])
		assert.NotNil(t, results[i])
		assert.Same(t, results[0], results[i])
	}
}

func TestClient_GetGlobalCatalogClient_CachesInstance(t *testing.T) {
	client := &Client{
		iamClient: NewIAMClient("test-key"),
	}

	c1, err1 := client.GetGlobalCatalogClient()
	assert.NoError(t, err1)
	assert.NotNil(t, c1)

	c2, err2 := client.GetGlobalCatalogClient()
	assert.NoError(t, err2)
	assert.NotNil(t, c2)

	assert.Same(t, c1, c2)
}
