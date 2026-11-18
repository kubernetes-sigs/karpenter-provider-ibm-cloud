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
	"golang.org/x/time/rate"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/httpclient"
)

// IKS API configuration constants
const (
	// iksV2BaseURL is the base URL for the v2 API (pool and worker list/get/create/delete)
	iksV2BaseURL = "https://containers.cloud.ibm.com/global/v2"

	// iksV1BaseURL is the base URL for the v1 API, which serves pool resize (v2 has no resize endpoint)
	iksV1BaseURL = "https://containers.cloud.ibm.com/global/v1"
)

// IBM account ID validation pattern (32-character hex string)
var iksRequestBudget = rate.NewLimiter(rate.Every(100*time.Millisecond), 1)

var ibmAccountIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// IKSClient handles IBM Kubernetes Service API operations.
//
// Concurrency: resizeMu serializes resize operations within the process. Cross-pod
// concurrency is excluded by Karpenter's leader election.
type IKSClient struct {
	client       *Client
	httpClient   *httpclient.IBMCloudHTTPClient // v2 API
	httpClientV1 *httpclient.IBMCloudHTTPClient // v1 API for resize
	accountID    string                         // cached and validated IBM account ID
	resizeMu     sync.Mutex                     // serializes resize operations within this process
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

	// Add account ID header (required for IKS API operations)
	// Uses cached and validated account ID from NewIKSClient
	if c.accountID != "" {
		req.Header.Set("X-Auth-Resource-Account", c.accountID)
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

// NewIKSClient creates a new IKS API client.
// Returns an error if IBM_ACCOUNT_ID is not set or has invalid format.
func NewIKSClient(client *Client) (*IKSClient, error) {
	// Validate IBM_ACCOUNT_ID - required for IKS API operations
	accountID := os.Getenv("IBM_ACCOUNT_ID")
	if accountID == "" {
		return nil, fmt.Errorf("IBM_ACCOUNT_ID environment variable is required for IKS worker pool operations")
	}
	if !ibmAccountIDPattern.MatchString(accountID) {
		return nil, fmt.Errorf("invalid IBM_ACCOUNT_ID format: expected 32-character hex string, got length %d", len(accountID))
	}

	iksClient := &IKSClient{
		client:    client,
		accountID: accountID,
	}

	iksClient.httpClient = httpclient.NewIBMCloudHTTPClient(iksV2BaseURL, iksClient.setIKSHeaders)
	iksClient.httpClientV1 = httpclient.NewIBMCloudHTTPClient(iksV1BaseURL, iksClient.setIKSHeaders)
	iksClient.httpClient.SetRequestGate(iksRequestBudget.Wait)
	iksClient.httpClientV1.SetRequestGate(iksRequestBudget.Wait)

	return iksClient, nil
}

func (c *IKSClient) GetAccountID() string { return c.accountID }

func (c *IKSClient) GetRegion() string {
	if c.client == nil {
		return ""
	}
	return c.client.GetRegion()
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
	if c.client == nil || c.client.iamClient == nil || c.httpClient == nil {
		return nil, fmt.Errorf("IKS client not properly initialized")
	}

	// Get IAM token for authentication
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}

	// Construct API endpoint
	endpoint := "/vpc/getWorker?" + url.Values{"cluster": {clusterID}, "worker": {workerID}}.Encode()

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

func (c *IKSClient) ListWorkers(ctx context.Context, clusterID string) ([]*IKSWorkerDetails, error) {
	if c.client == nil || c.client.iamClient == nil || c.httpClient == nil {
		return nil, fmt.Errorf("IKS client not properly initialized")
	}
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}
	endpoint := "/vpc/getWorkers?" + url.Values{"cluster": {clusterID}, "showDeleted": {"false"}}.Encode()
	var workers []*IKSWorkerDetails
	if err := c.httpClient.GetJSON(ctx, endpoint, token, &workers); err != nil {
		return nil, fmt.Errorf("listing workers in cluster %s: %w", clusterID, err)
	}
	return workers, nil
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
	vpcClient, err := c.client.GetVPCClient(ctx)
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
	ID               string            `json:"id"`
	Name             string            `json:"poolName"` // v2 API uses poolName not name
	Flavor           string            `json:"flavor"`
	Zone             string            `json:"zone"`
	SizePerZone      int               `json:"workerCount"` // v2 API returns workerCount not sizePerZone
	ActualSize       int               `json:"actualSize"`
	State            string            `json:"state"`
	Labels           map[string]string `json:"labels"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	Zones            []WorkerPoolZone  `json:"zones"`
	AutoscaleEnabled bool              `json:"autoscaleEnabled"`
}

func (p *WorkerPool) UnmarshalJSON(data []byte) error {
	type pool WorkerPool
	var wire struct {
		pool
		LegacyName   string `json:"name"`
		LegacyFlavor string `json:"machineType"`
		LegacySize   *int   `json:"sizePerZone"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*p = WorkerPool(wire.pool)
	if p.Name == "" {
		p.Name = wire.LegacyName
	}
	if p.Flavor == "" {
		p.Flavor = wire.LegacyFlavor
	}
	if wire.LegacySize != nil {
		p.SizePerZone = *wire.LegacySize
	}
	if len(p.Zones) == 1 {
		p.Zone = p.Zones[0].ID
	}
	return nil
}

// WorkerPoolResizeRequest represents a request to resize a worker pool
// Reference: https://cloud.ibm.com/apidocs/kubernetes#patch-workerpool
type WorkerPoolResizeRequest struct {
	SizePerZone     int               `json:"sizePerZone"`
	ReasonForResize string            `json:"reasonForResize,omitempty"`
	State           string            `json:"state,omitempty"`
	Labels          map[string]string `json:"labels"`
}

// WorkerPoolZone represents a zone configuration for a worker pool
type WorkerPoolZone struct {
	ID       string `json:"id"`
	SubnetID string `json:"subnetID,omitempty"`
}

// WorkerPoolCreateRequest represents a request to create a new worker pool
// Reference: https://cloud.ibm.com/apidocs/kubernetes#createworkerpool
type WorkerPoolCreateRequest struct {
	Name           string            `json:"name"`
	Flavor         string            `json:"flavor"`
	SizePerZone    int               `json:"sizePerZone"`
	Zones          []WorkerPoolZone  `json:"zones"`
	Labels         map[string]string `json:"labels,omitempty"`
	DiskEncryption bool              `json:"diskEncryption,omitempty"`
	VpcID          string            `json:"vpcID,omitempty"`
}

// ListWorkerPools retrieves all worker pools for a cluster
func (c *IKSClient) ListWorkerPools(ctx context.Context, clusterID string) ([]*WorkerPool, error) {
	if c.client == nil || c.client.iamClient == nil || c.httpClient == nil {
		return nil, fmt.Errorf("IKS client not properly initialized")
	}

	// Get IAM token for authentication
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}

	endpoint := "/vpc/getWorkerPools?" + url.Values{"cluster": {clusterID}}.Encode()

	// Parse response
	var workerPools []*WorkerPool

	// Make request using shared HTTP client
	if err := c.httpClient.GetJSON(ctx, endpoint, token, &workerPools); err != nil {
		return nil, err
	}

	return workerPools, nil
}

// ResizeWorkerPool resizes a worker pool to the specified size.
// This method is protected by a mutex to prevent concurrent resize operations
// from racing and causing incorrect pool sizes.
func (c *IKSClient) ResizeWorkerPool(ctx context.Context, clusterID, poolID string, newSize int) error {
	// Acquire lock to serialize resize operations
	// This prevents race conditions where multiple concurrent requests
	// could read the same pool size and increment to the same value
	c.resizeMu.Lock()
	defer c.resizeMu.Unlock()

	// Check context before proceeding
	select {
	case <-ctx.Done():
		return fmt.Errorf("context canceled before resize: %w", ctx.Err())
	default:
	}

	// Validate newSize
	if newSize < 0 {
		return fmt.Errorf("invalid pool size: %d (must be >= 0)", newSize)
	}

	// Get IAM token for authentication
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting IAM token: %w", err)
	}

	// Prepare request payload (matching IBM Cloud CLI format)
	request := WorkerPoolResizeRequest{
		SizePerZone: newSize,
		State:       "resizing",
		Labels:      nil,
	}

	// Construct API endpoint (v1 API uses /clusters/{id}/workerpools/{name})
	endpoint := fmt.Sprintf("/clusters/%s/workerpools/%s", clusterID, poolID)

	// Make request using v1 HTTP client (PATCH for updates)
	jsonData, err := json.Marshal(&request)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	_, err = c.httpClientV1.Patch(ctx, endpoint, token, strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("resize worker pool %s in cluster %s to size %d failed: %w", poolID, clusterID, newSize, err)
	}

	return nil
}

// GetWorkerPool retrieves a specific worker pool
func (c *IKSClient) GetWorkerPool(ctx context.Context, clusterID, poolID string) (*WorkerPool, error) {
	if c.client == nil || c.client.iamClient == nil || c.httpClient == nil {
		return nil, fmt.Errorf("IKS client not properly initialized")
	}

	// Get IAM token for authentication
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}

	// workerpool accepts either the pool name or ID.
	endpoint := "/vpc/getWorkerPool?" + url.Values{"cluster": {clusterID}, "workerpool": {poolID}}.Encode()

	// Parse response
	var workerPool WorkerPool

	// Make request using shared HTTP client
	if err := c.httpClient.GetJSON(ctx, endpoint, token, &workerPool); err != nil {
		return nil, err
	}

	return &workerPool, nil
}

// CreateWorkerPool creates a new worker pool in the specified cluster
func (c *IKSClient) CreateWorkerPool(ctx context.Context, clusterID string, request *WorkerPoolCreateRequest) (*WorkerPool, error) {
	if c.client == nil || c.client.iamClient == nil || c.httpClient == nil || request == nil {
		return nil, fmt.Errorf("IKS client or worker pool request not initialized")
	}
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}
	zones := make([]map[string]string, 0, len(request.Zones))
	for _, zone := range request.Zones {
		zones = append(zones, map[string]string{"id": zone.ID, "subnetID": zone.SubnetID})
	}
	body := map[string]interface{}{"cluster": clusterID, "name": request.Name, "flavor": request.Flavor,
		"workerCount": request.SizePerZone, "zones": zones, "labels": request.Labels,
		"diskEncryption": request.DiskEncryption, "vpcID": request.VpcID}
	var response struct {
		WorkerPoolID string `json:"workerPoolID"`
		ID           string `json:"id"`
	}
	if err := c.httpClient.PostJSON(ctx, "/vpc/createWorkerPool", token, body, &response); err != nil {
		return nil, fmt.Errorf("creating worker pool: %w", err)
	}
	if response.WorkerPoolID == "" {
		response.WorkerPoolID = response.ID
	}
	if response.WorkerPoolID == "" {
		return nil, fmt.Errorf("worker pool create response contains no ID")
	}
	return &WorkerPool{ID: response.WorkerPoolID, Name: request.Name}, nil
}

// DeleteWorkerPool deletes a worker pool from the specified cluster
func (c *IKSClient) DeleteWorkerPool(ctx context.Context, clusterID, poolID string) error {
	if c.client == nil || c.client.iamClient == nil || c.httpClient == nil {
		return fmt.Errorf("IKS client not properly initialized")
	}
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting IAM token: %w", err)
	}
	body := map[string]string{"cluster": clusterID, "workerpool": poolID}
	if err := c.httpClient.PostJSON(ctx, "/removeWorkerPool", token, body, nil); err != nil {
		return fmt.Errorf("deleting worker pool: %w", err)
	}
	return nil
}
