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
	"os"
	"testing"
)

// MockCredentialStore implements SecureCredentialManager for testing
type MockCredentialStore struct {
	vpcAPIKey string
	ibmAPIKey string
	region    string
}

func (m *MockCredentialStore) GetVPCAPIKey(ctx context.Context) (string, error) {
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

	vpcClient, err := client.GetVPCClient()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if vpcClient == nil {
		t.Error("expected VPC client but got nil")
	}
}

func TestGetGlobalCatalogClient(t *testing.T) {
	client := &Client{
		iamClient: NewIAMClient("test-key"),
	}

	catalogClient, err := client.GetGlobalCatalogClient()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if catalogClient == nil {
		t.Error("expected Global Catalog client but got nil")
	}
}
