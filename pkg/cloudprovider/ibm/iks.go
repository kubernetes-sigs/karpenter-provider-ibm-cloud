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
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/httpclient"
)

// IKSClient handles IBM Kubernetes Service API operations
type IKSClient struct {
	client     *Client
	httpClient *httpclient.IBMCloudHTTPClient
}

// setIKSHeaders sets the required headers for IKS API requests
func (c *IKSClient) setIKSHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "IBM-Cloud-CLI/1.0.0")

	// Add regional targeting for global endpoint
	region := c.client.GetRegion()
	req.Header.Set("X-Region", region)

	// Add resource group context if available
	if resourceGroupID := os.Getenv("IBM_RESOURCE_GROUP_ID"); resourceGroupID != "" {
		req.Header.Set("X-Auth-Resource-Group", resourceGroupID)
	}

	// Add headers to mimic IBM Cloud CLI behavior
	req.Header.Set("X-CLI-Request", "true")
	req.Header.Set("X-Service-Name", "containers-kubernetes")
	req.Header.Set("X-Auth-User-Token", token)
}

// IKSWorkerDetails represents the response from IKS worker API
type IKSWorkerDetails struct {
	ID                string                `json:"id"`
	Provider          string                `json:"provider"`
	Flavor            string                `json:"flavor"`
	Location          string                `json:"location"`
	PoolID            string                `json:"poolID"`
	PoolName          string                `json:"poolName"`
	NetworkInterfaces []IKSNetworkInterface `json:"networkInterfaces"`
	Health            IKSHealthStatus       `json:"health"`
	Lifecycle         IKSLifecycleStatus    `json:"lifecycle"`
}

// IKSNetworkInterface represents a worker's network interface
type IKSNetworkInterface struct {
	SubnetID  string `json:"subnetID"`
	IPAddress string `json:"ipAddress"`
	CIDR      string `json:"cidr"`
	Primary   bool   `json:"primary"`
}

// IKSHealthStatus represents worker health information
type IKSHealthStatus struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

// IKSLifecycleStatus represents worker lifecycle information
type IKSLifecycleStatus struct {
	DesiredState string `json:"desiredState"`
	ActualState  string `json:"actualState"`
	Message      string `json:"message"`
}

// NewIKSClient creates a new IKS API client
func NewIKSClient(client *Client) *IKSClient {
	// Use IBM Cloud Kubernetes Service v1 API endpoint
	// Reference: https://cloud.ibm.com/apidocs/kubernetes/containers-v1-v2
	baseURL := "https://containers.cloud.ibm.com/global/v1"

	iksClient := &IKSClient{
		client: client,
	}

	// Create HTTP client with header setter
	iksClient.httpClient = httpclient.NewIBMCloudHTTPClient(baseURL, iksClient.setIKSHeaders)

	return iksClient
}

// NewIKSClientWithHTTPClient creates a new IKS API client with a custom HTTP client for testing
func NewIKSClientWithHTTPClient(client *Client, httpClient *httpclient.IBMCloudHTTPClient) *IKSClient {
	return &IKSClient{
		client:     client,
		httpClient: httpClient,
	}
}

// GetWorkerDetails retrieves detailed information about an IKS worker
func (c *IKSClient) GetWorkerDetails(ctx context.Context, clusterID, workerID string) (*IKSWorkerDetails, error) {
	// Get IAM token for authentication
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}

	// Construct API endpoint
	endpoint := fmt.Sprintf("/clusters/%s/workers/%s", clusterID, workerID)

	// Make request using shared HTTP client
	resp, err := c.httpClient.Get(ctx, endpoint, token)
	if err != nil {
		// Handle specific IBM Cloud error codes
		if ibmErr, ok := err.(*httpclient.IBMCloudError); ok {
			switch ibmErr.Code {
			case "E3917": // Cluster provider not permitted for given operation
				return nil, fmt.Errorf("cluster %s is not configured for Karpenter management (IKS managed cluster): %s", clusterID, ibmErr.Description)
			}
		}
		return nil, err
	}

	// Parse response
	var workerDetails IKSWorkerDetails
	if err := json.Unmarshal(resp.Body, &workerDetails); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &workerDetails, nil
}

// GetVPCInstanceIDFromWorker extracts the VPC instance ID from worker details
// For VPC Gen2 workers, we need to look up the VPC instance by subnet and IP
func (c *IKSClient) GetVPCInstanceIDFromWorker(ctx context.Context, clusterID, workerID string) (string, error) {
	// Get worker details
	workerDetails, err := c.GetWorkerDetails(ctx, clusterID, workerID)
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

	// Use VPC client to find instance by subnet and IP
	vpcClient, err := c.client.GetVPCClient()
	if err != nil {
		return "", fmt.Errorf("getting VPC client: %w", err)
	}

	// List instances and find the one with matching IP in the subnet
	instances, err := vpcClient.ListInstances(ctx)
	if err != nil {
		return "", fmt.Errorf("listing VPC instances: %w", err)
	}

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

// GetClusterConfig retrieves the kubeconfig for the specified cluster
func (c *IKSClient) GetClusterConfig(ctx context.Context, clusterID string) (string, error) {
	if c.client == nil || c.client.iamClient == nil {
		return "", fmt.Errorf("client not properly initialized")
	}

	// Get IAM token for authentication
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return "", fmt.Errorf("getting IAM token: %w", err)
	}

	// Construct API endpoint for cluster config
	endpoint := fmt.Sprintf("/clusters/%s/config", clusterID)

	// Parse response - the API returns the kubeconfig as a string
	var configResp struct {
		Config string `json:"config"`
	}

	// Make request using shared HTTP client
	if err := c.httpClient.GetJSON(ctx, endpoint, token, &configResp); err != nil {
		return "", err
	}

	return configResp.Config, nil
}

// WorkerPool represents an IKS worker pool
type WorkerPool struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Flavor      string            `json:"flavor"`
	Zone        string            `json:"zone"`
	SizePerZone int               `json:"sizePerZone"`
	ActualSize  int               `json:"actualSize"`
	State       string            `json:"state"`
	Labels      map[string]string `json:"labels"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

// WorkerPoolResizeRequest represents a request to resize a worker pool
// Reference: https://cloud.ibm.com/apidocs/kubernetes#patch-workerpool
type WorkerPoolResizeRequest struct {
	SizePerZone int `json:"sizePerZone"`
}

// ListWorkerPools retrieves all worker pools for a cluster
func (c *IKSClient) ListWorkerPools(ctx context.Context, clusterID string) ([]*WorkerPool, error) {
	// Get IAM token for authentication
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}

	// Construct API endpoint
	endpoint := fmt.Sprintf("/clusters/%s/workerpools", clusterID)

	// Parse response
	var workerPools []*WorkerPool

	// Make request using shared HTTP client
	if err := c.httpClient.GetJSON(ctx, endpoint, token, &workerPools); err != nil {
		// Handle specific IBM Cloud error codes
		if ibmErr, ok := err.(*httpclient.IBMCloudError); ok {
			switch ibmErr.Code {
			case "E3917": // Cluster provider not permitted for given operation
				return nil, fmt.Errorf("cluster %s is not configured for Karpenter management (IKS managed cluster): %s", clusterID, ibmErr.Description)
			case "E0003": // Unauthorized
				return nil, fmt.Errorf("unauthorized to access IKS API: %s", ibmErr.Description)
			case "E0015": // Cluster not found
				return nil, fmt.Errorf("cluster %s not found: %s", clusterID, ibmErr.Description)
			}
		}
		return nil, err
	}

	return workerPools, nil
}

// ResizeWorkerPool resizes a worker pool by adding one node
func (c *IKSClient) ResizeWorkerPool(ctx context.Context, clusterID, poolID string, newSize int) error {
	// Get IAM token for authentication
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting IAM token: %w", err)
	}

	// Prepare request payload
	request := WorkerPoolResizeRequest{
		SizePerZone: newSize,
	}

	// Construct API endpoint
	endpoint := fmt.Sprintf("/clusters/%s/workerpools/%s", clusterID, poolID)

	// Make request using shared HTTP client (PATCH for updates)
	jsonData, err := json.Marshal(&request)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	_, err = c.httpClient.Patch(ctx, endpoint, token, strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("IKS worker pool resize failed: %w", err)
	}

	return nil
}

// GetWorkerPool retrieves a specific worker pool
func (c *IKSClient) GetWorkerPool(ctx context.Context, clusterID, poolID string) (*WorkerPool, error) {
	// Get IAM token for authentication
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}

	// Construct API endpoint
	endpoint := fmt.Sprintf("/clusters/%s/workerpools/%s", clusterID, poolID)

	// Parse response
	var workerPool WorkerPool

	// Make request using shared HTTP client
	if err := c.httpClient.GetJSON(ctx, endpoint, token, &workerPool); err != nil {
		// Handle specific IBM Cloud error codes
		if ibmErr, ok := err.(*httpclient.IBMCloudError); ok {
			switch ibmErr.Code {
			case "E3917": // Cluster provider not permitted for given operation
				return nil, fmt.Errorf("cluster %s is not configured for Karpenter management (IKS managed cluster): %s", clusterID, ibmErr.Description)
			case "E0003": // Unauthorized
				return nil, fmt.Errorf("unauthorized to access IKS API: %s", ibmErr.Description)
			case "E0015": // Cluster not found
				return nil, fmt.Errorf("cluster %s not found: %s", clusterID, ibmErr.Description)
			case "E0013": // Worker pool not found
				return nil, fmt.Errorf("worker pool %s not found in cluster %s: %s", poolID, clusterID, ibmErr.Description)
			}
		}
		return nil, err
	}

	return &workerPool, nil
}
