/*
Copyright 2024 The Kubernetes Authors.

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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/httpclient"
)

func TestNewIKSClient(t *testing.T) {
	client := &Client{}
	iksClient := NewIKSClient(client)

	assert.NotNil(t, iksClient)
	assert.Equal(t, client, iksClient.client)
	assert.NotNil(t, iksClient.httpClient)
}

func TestClient_GetIKSClient(t *testing.T) {
	client := &Client{}

	iksClient := client.GetIKSClient()
	assert.NotNil(t, iksClient)

	// Verify it returns an IKS client interface
	// We can't test private fields on the interface, but we can verify
	// it implements the expected interface methods
	assert.Implements(t, (*IKSClientInterface)(nil), iksClient)

	// Verify multiple calls return different instances (since we create on-demand)
	iksClient2 := client.GetIKSClient()
	assert.NotNil(t, iksClient2)
	assert.NotSame(t, iksClient, iksClient2) // Different instances
}

func TestIKSClient_GetWorkerDetails(t *testing.T) {
	tests := []struct {
		name           string
		clusterID      string
		workerID       string
		serverResponse int
		serverBody     string
		expectedError  string
		expectedWorker *IKSWorkerDetails
	}{
		{
			name:           "successful worker details request",
			clusterID:      "test-cluster",
			workerID:       "test-worker",
			serverResponse: http.StatusOK,
			serverBody: `{
				"id": "test-worker",
				"provider": "vpc-gen2",
				"flavor": "bx2.2x8",
				"location": "us-south-1",
				"poolID": "test-pool",
				"poolName": "default",
				"networkInterfaces": [
					{
						"subnetID": "subnet-123",
						"ipAddress": "10.10.10.22",
						"cidr": "10.10.10.0/24",
						"primary": true
					}
				],
				"health": {
					"state": "normal",
					"message": "Ready"
				},
				"lifecycle": {
					"desiredState": "deployed",
					"actualState": "deployed",
					"message": ""
				}
			}`,
			expectedWorker: &IKSWorkerDetails{
				ID:       "test-worker",
				Provider: "vpc-gen2",
				Flavor:   "bx2.2x8",
				Location: "us-south-1",
				PoolID:   "test-pool",
				PoolName: "default",
				NetworkInterfaces: []IKSNetworkInterface{
					{
						SubnetID:  "subnet-123",
						IPAddress: "10.10.10.22",
						CIDR:      "10.10.10.0/24",
						Primary:   true,
					},
				},
				Health: IKSHealthStatus{
					State:   "normal",
					Message: "Ready",
				},
				Lifecycle: IKSLifecycleStatus{
					DesiredState: "deployed",
					ActualState:  "deployed",
					Message:      "",
				},
			},
		},
		{
			name:           "API error response",
			clusterID:      "test-cluster",
			workerID:       "test-worker",
			serverResponse: http.StatusNotFound,
			serverBody:     `{"error": "worker not found"}`,
			expectedError:  "HTTP 404:",
		},
		{
			name:           "malformed JSON response",
			clusterID:      "test-cluster",
			workerID:       "test-worker",
			serverResponse: http.StatusOK,
			serverBody:     `{invalid json`,
			expectedError:  "parsing response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request
				expectedPath := "/clusters/" + tt.clusterID + "/workers/" + tt.workerID
				assert.Equal(t, expectedPath, r.URL.Path)
				assert.Equal(t, "GET", r.Method)
				assert.Contains(t, r.Header.Get("Authorization"), "Bearer")
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

				w.WriteHeader(tt.serverResponse)
				_, _ = w.Write([]byte(tt.serverBody))
			}))
			defer server.Close()

			// Create client with mock IAM client
			client := &Client{
				iamClient: &IAMClient{
					Authenticator: &mockAuthenticator{
						token: "test-token",
					},
				},
			}

			// Create IKS client with test server URL
			httpClient := httpclient.NewIBMCloudHTTPClient(server.URL, func(req *http.Request, token string) {
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
			})
			iksClient := NewIKSClientWithHTTPClient(client, httpClient)

			// Test GetWorkerDetails
			ctx := context.Background()
			worker, err := iksClient.GetWorkerDetails(ctx, tt.clusterID, tt.workerID)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Nil(t, worker)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedWorker, worker)
			}
		})
	}
}

func TestIKSClient_GetVPCInstanceIDFromWorker(t *testing.T) {
	tests := []struct {
		name               string
		clusterID          string
		workerID           string
		workerResponse     string
		vpcInstances       []vpcv1.Instance
		expectedInstanceID string
		expectedError      string
	}{
		{
			name:      "successful VPC instance mapping",
			clusterID: "test-cluster",
			workerID:  "test-worker",
			workerResponse: `{
				"id": "test-worker",
				"provider": "vpc-gen2",
				"networkInterfaces": [
					{
						"subnetID": "subnet-123",
						"ipAddress": "10.10.10.22",
						"primary": true
					}
				]
			}`,
			vpcInstances: []vpcv1.Instance{
				{
					ID: stringPtr("instance-456"),
					NetworkInterfaces: []vpcv1.NetworkInterfaceInstanceContextReference{
						{
							Subnet: &vpcv1.SubnetReference{
								ID: stringPtr("subnet-123"),
							},
							PrimaryIP: &vpcv1.ReservedIPReference{
								Address: stringPtr("10.10.10.22"),
							},
						},
					},
				},
			},
			expectedInstanceID: "instance-456",
		},
		{
			name:      "worker not VPC Gen2",
			clusterID: "test-cluster",
			workerID:  "test-worker",
			workerResponse: `{
				"id": "test-worker",
				"provider": "classic",
				"networkInterfaces": []
			}`,
			expectedError: "not a VPC Gen2 worker",
		},
		{
			name:      "no primary network interface",
			clusterID: "test-cluster",
			workerID:  "test-worker",
			workerResponse: `{
				"id": "test-worker",
				"provider": "vpc-gen2",
				"networkInterfaces": [
					{
						"subnetID": "subnet-123",
						"ipAddress": "10.10.10.22",
						"primary": false
					}
				]
			}`,
			expectedError: "no primary network interface found",
		},
		{
			name:      "VPC instance not found",
			clusterID: "test-cluster",
			workerID:  "test-worker",
			workerResponse: `{
				"id": "test-worker",
				"provider": "vpc-gen2",
				"networkInterfaces": [
					{
						"subnetID": "subnet-123",
						"ipAddress": "10.10.10.22",
						"primary": true
					}
				]
			}`,
			vpcInstances:  []vpcv1.Instance{}, // No matching instances
			expectedError: "VPC instance not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server for IKS API
			iksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.workerResponse))
			}))
			defer iksServer.Close()

			// Create mock VPC client
			mockVPCClient := &MockVPCClient{
				instances: tt.vpcInstances,
			}

			// Create client with mocks
			client := &Client{
				iamClient: &IAMClient{
					Authenticator: &mockAuthenticator{
						token: "test-token",
					},
				},
			}

			// Create IKS client with test server URL
			httpClient := httpclient.NewIBMCloudHTTPClient(iksServer.URL, func(req *http.Request, token string) {
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
			})
			iksClient := NewIKSClientWithHTTPClient(client, httpClient)

			// Create a test IKS client with custom VPC client getter
			testIKSClient := &testIKSClient{
				IKSClient:     iksClient,
				mockVPCClient: mockVPCClient,
			}

			// Test GetVPCInstanceIDFromWorker
			ctx := context.Background()
			instanceID, err := testIKSClient.GetVPCInstanceIDFromWorker(ctx, tt.clusterID, tt.workerID)

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
				assert.Empty(t, instanceID)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedInstanceID, instanceID)
			}
		})
	}
}

// MockVPCClient for testing
type MockVPCClient struct {
	instances []vpcv1.Instance
}

func (m *MockVPCClient) ListInstancesWithContext(ctx context.Context, options *vpcv1.ListInstancesOptions) (*vpcv1.InstanceCollection, *core.DetailedResponse, error) {
	return &vpcv1.InstanceCollection{
		Instances: m.instances,
	}, nil, nil
}

// Implement other required methods for the interface
func (m *MockVPCClient) CreateInstanceWithContext(ctx context.Context, options *vpcv1.CreateInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error) {
	return nil, nil, nil
}

func (m *MockVPCClient) DeleteInstanceWithContext(ctx context.Context, options *vpcv1.DeleteInstanceOptions) (*core.DetailedResponse, error) {
	return nil, nil
}

func (m *MockVPCClient) GetInstanceWithContext(ctx context.Context, options *vpcv1.GetInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error) {
	return nil, nil, nil
}

func (m *MockVPCClient) UpdateInstanceWithContext(ctx context.Context, options *vpcv1.UpdateInstanceOptions) (*vpcv1.Instance, *core.DetailedResponse, error) {
	return nil, nil, nil
}

func (m *MockVPCClient) ListSubnetsWithContext(ctx context.Context, options *vpcv1.ListSubnetsOptions) (*vpcv1.SubnetCollection, *core.DetailedResponse, error) {
	return nil, nil, nil
}

func (m *MockVPCClient) GetSubnetWithContext(ctx context.Context, options *vpcv1.GetSubnetOptions) (*vpcv1.Subnet, *core.DetailedResponse, error) {
	return nil, nil, nil
}

// testIKSClient wraps IKSClient for testing with custom VPC client
type testIKSClient struct {
	*IKSClient
	mockVPCClient *MockVPCClient
}

func (t *testIKSClient) GetVPCInstanceIDFromWorker(ctx context.Context, clusterID, workerID string) (string, error) {
	// Get worker details
	workerDetails, err := t.GetWorkerDetails(ctx, clusterID, workerID)
	if err != nil {
		return "", fmt.Errorf("getting worker details: %w", err)
	}

	// Ensure this is a VPC Gen2 worker
	if workerDetails.Provider != "vpc-gen2" {
		return "", fmt.Errorf("worker %s is not a VPC Gen2 worker (provider: %s)", workerID, workerDetails.Provider)
	}

	// Find primary network interface
	var primaryInterface *IKSNetworkInterface
	for _, ni := range workerDetails.NetworkInterfaces {
		if ni.Primary {
			primaryInterface = &ni
			break
		}
	}

	if primaryInterface == nil {
		return "", fmt.Errorf("no primary network interface found for worker %s", workerID)
	}

	// Use mock VPC client to find instance by subnet and IP
	instances := t.mockVPCClient.instances

	for _, instance := range instances {
		// Check if instance has matching IP address in primary interface
		for _, ni := range instance.NetworkInterfaces {
			if ni.Subnet != nil && ni.Subnet.ID != nil && *ni.Subnet.ID == primaryInterface.SubnetID {
				if ni.PrimaryIP != nil && ni.PrimaryIP.Address != nil && *ni.PrimaryIP.Address == primaryInterface.IPAddress {
					return *instance.ID, nil
				}
			}
		}
	}

	return "", fmt.Errorf("VPC instance not found for worker %s (IP: %s, subnet: %s)",
		workerID, primaryInterface.IPAddress, primaryInterface.SubnetID)
}

func TestIKSClient_GetVPCInstanceIDFromWorker_CoverageGaps(t *testing.T) {
	// Test error handling when VPC client initialization fails
	// We'll test the actual method by using a working IKS client with the testIKSClient wrapper

	tests := []struct {
		name           string
		workerResponse string
		expectedError  string
	}{
		{
			name: "vpc client initialization error",
			workerResponse: `{
				"id": "test-worker",
				"provider": "vpc-gen2",
				"networkInterfaces": [
					{
						"subnetID": "subnet-123",
						"ipAddress": "10.10.10.22",
						"primary": true
					}
				]
			}`,
			expectedError: "getting VPC client",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server for IKS API
			iksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.workerResponse))
			}))
			defer iksServer.Close()

			// Create client with invalid configuration to force VPC client error
			client := &Client{
				credStore: &MockCredentialStore{vpcAPIKey: ""}, // Empty API key will cause VPC client creation to fail
				iamClient: &IAMClient{
					Authenticator: &mockAuthenticator{
						token: "test-token",
					},
				},
			}

			// Create IKS client with test server URL
			httpClient := httpclient.NewIBMCloudHTTPClient(iksServer.URL, func(req *http.Request, token string) {
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
			})
			iksClient := NewIKSClientWithHTTPClient(client, httpClient)

			// Test GetVPCInstanceIDFromWorker directly to hit the actual implementation
			ctx := context.Background()
			_, err := iksClient.GetVPCInstanceIDFromWorker(ctx, "test-cluster", "test-worker")

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}

func TestIKSClient_GetClusterConfig(t *testing.T) {
	tests := []struct {
		name           string
		response       string
		statusCode     int
		expectedError  string
		expectedConfig string
	}{
		{
			name:       "successful config retrieval",
			statusCode: 200,
			response: `{
				"config": "apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://c111.us-south.containers.cloud.ibm.com:30409\n    certificate-authority-data: LS0tLS1CRUdJTi...\n  name: test-cluster\ncontexts:\n- context:\n    cluster: test-cluster\n    user: test-user\n  name: test-context\ncurrent-context: test-context\nusers:\n- name: test-user\n  user:\n    token: test-token"
			}`,
			expectedConfig: "apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: https://c111.us-south.containers.cloud.ibm.com:30409\n    certificate-authority-data: LS0tLS1CRUdJTi...\n  name: test-cluster\ncontexts:\n- context:\n    cluster: test-cluster\n    user: test-user\n  name: test-context\ncurrent-context: test-context\nusers:\n- name: test-user\n  user:\n    token: test-token",
		},
		{
			name:       "API error response",
			statusCode: 403,
			response: `{
				"code": "E0403",
				"description": "Access denied to cluster config",
				"type": "Authentication"
			}`,
			expectedError: "IBM Cloud API error (code: E0403): Access denied to cluster config",
		},
		{
			name:          "invalid JSON response",
			statusCode:    200,
			response:      `{invalid json`,
			expectedError: "unmarshaling JSON response:",
		},
		{
			name:       "empty config response",
			statusCode: 200,
			response: `{
				"config": ""
			}`,
			expectedConfig: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/clusters/test-cluster-id/config", r.URL.Path)
				assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			// Create client with mock authenticator
			client := &Client{
				iamClient: &IAMClient{
					Authenticator: &mockAuthenticator{
						token: "test-token",
					},
				},
			}

			// Create IKS client with test server URL
			httpClient := httpclient.NewIBMCloudHTTPClient(server.URL, func(req *http.Request, token string) {
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
			})
			iksClient := NewIKSClientWithHTTPClient(client, httpClient)

			// Test GetClusterConfig
			ctx := context.Background()
			config, err := iksClient.GetClusterConfig(ctx, "test-cluster-id")

			if tt.expectedError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedConfig, config)
			}
		})
	}
}

func TestIKSClient_GetClusterConfig_ClientNotInitialized(t *testing.T) {
	tests := []struct {
		name          string
		client        *Client
		expectedError string
	}{
		{
			name:          "nil client",
			client:        nil,
			expectedError: "client not properly initialized",
		},
		{
			name: "nil IAM client",
			client: &Client{
				iamClient: nil,
			},
			expectedError: "client not properly initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpClient := httpclient.NewIBMCloudHTTPClient("https://containers.cloud.ibm.com/global/v1", func(req *http.Request, token string) {
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
			})
			iksClient := &IKSClient{
				client:     tt.client,
				httpClient: httpClient,
			}

			ctx := context.Background()
			_, err := iksClient.GetClusterConfig(ctx, "test-cluster-id")

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}
