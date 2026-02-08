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
	"strings"
	"sync"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/resourcemanagerv2"
)

// Client represents an IBM Cloud API client
type Client struct {
	vpcURL      string
	vpcAuthType string
	credStore   SecureCredentialManager
	region      string
	iamClient   *IAMClient

	// VPC client lazy initialization
	vpcClient   *VPCClient
	vpcClientMu sync.RWMutex

	// Global Catalog client lazy initialization
	globalCatalogClient   *GlobalCatalogClient
	globalCatalogClientMu sync.RWMutex

	// IKS client lazy initialization
	iksClient    IKSClientInterface
	iksClientErr error
	iksOnce      sync.Once
}

// NewClient creates a new IBM Cloud client using environment variables
func NewClient() (*Client, error) {
	// Create credential provider and secure store
	credProvider := &EnvironmentCredentialProvider{}
	credStore, err := NewSecureCredentialStore(credProvider, 12*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("creating secure credential store: %w", err)
	}

	// Get initial credentials to validate they exist
	ctx := context.Background()
	region, err := credStore.GetRegion(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting region: %w", err)
	}

	// Get IBM API key for IAM client
	ibmAPIKey, err := credStore.GetIBMAPIKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IBM API key: %w", err)
	}

	vpcURL := os.Getenv("VPC_URL")
	if vpcURL == "" {
		vpcURL = "https://us-south.iaas.cloud.ibm.com/v1" // default value
	}

	vpcAuthType := os.Getenv("VPC_AUTH_TYPE")
	if vpcAuthType == "" {
		vpcAuthType = "iam" // default value
	}

	client := &Client{
		vpcURL:      vpcURL,
		vpcAuthType: vpcAuthType,
		credStore:   credStore,
		region:      region,
	}

	// Initialize the IAM client
	client.iamClient = NewIAMClient(ibmAPIKey)

	return client, nil
}

// GetVPCClient returns a configured VPC API client
func (c *Client) GetVPCClient(ctx context.Context) (*VPCClient, error) {
	// Fast path (read lock): return cached client if already initialized
	c.vpcClientMu.RLock()
	client := c.vpcClient
	c.vpcClientMu.RUnlock()

	if client != nil {
		return client, nil
	}

	// Slow path (write lock): initialize once
	c.vpcClientMu.Lock()
	defer c.vpcClientMu.Unlock()

	// Double-check after acquiring write lock
	if c.vpcClient != nil {
		return c.vpcClient, nil
	}

	vpcAPIKey, err := c.credStore.GetVPCAPIKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting VPC API key: %w", err)
	}

	vpc, err := NewVPCClient(c.vpcURL, c.vpcAuthType, vpcAPIKey, c.region, "")
	if err != nil {
		return nil, err
	}

	c.vpcClient = vpc
	return vpc, nil
}

// GetGlobalCatalogClient returns a configured Global Catalog API client
func (c *Client) GetGlobalCatalogClient() (*GlobalCatalogClient, error) {
	// Fast path
	c.globalCatalogClientMu.RLock()
	client := c.globalCatalogClient
	c.globalCatalogClientMu.RUnlock()

	if client != nil {
		return client, nil
	}

	// Slow path
	c.globalCatalogClientMu.Lock()
	defer c.globalCatalogClientMu.Unlock()

	// Double-check
	if c.globalCatalogClient != nil {
		return c.globalCatalogClient, nil
	}

	c.globalCatalogClient = NewGlobalCatalogClient(c.iamClient)
	return c.globalCatalogClient, nil
}

// GetIKSClient returns a configured IKS API client interface.
// Uses lazy initialization with validation - returns error if IBM_ACCOUNT_ID
// is not properly configured.
func (c *Client) GetIKSClient() (IKSClientInterface, error) {
	c.iksOnce.Do(func() {
		c.iksClient, c.iksClientErr = NewIKSClient(c)
	})
	return c.iksClient, c.iksClientErr
}

// GetIAMClient returns the IAM client
func (c *Client) GetIAMClient() *IAMClient {
	return c.iamClient
}

// GetRegion returns the configured region
func (c *Client) GetRegion() string {
	return c.region
}

// GetResourceGroupIDByName resolves a resource group name to its ID
func (c *Client) GetResourceGroupIDByName(ctx context.Context, resourceGroupName string) (string, error) {
	// Get access token for authentication
	token, err := c.iamClient.GetToken(ctx)
	if err != nil {
		return "", fmt.Errorf("getting IAM token: %w", err)
	}

	// Create Resource Manager client
	resourceManagerClient, err := resourcemanagerv2.NewResourceManagerV2(&resourcemanagerv2.ResourceManagerV2Options{
		Authenticator: &core.BearerTokenAuthenticator{
			BearerToken: token,
		},
	})
	if err != nil {
		return "", fmt.Errorf("creating resource manager client: %w", err)
	}

	// List resource groups and find the one with matching name
	listOptions := &resourcemanagerv2.ListResourceGroupsOptions{}
	result, _, err := resourceManagerClient.ListResourceGroupsWithContext(ctx, listOptions)
	if err != nil {
		return "", fmt.Errorf("listing resource groups: %w", err)
	}

	// Find resource group by name
	for _, rg := range result.Resources {
		if rg.Name != nil && *rg.Name == resourceGroupName {
			if rg.ID != nil {
				return *rg.ID, nil
			}
		}
	}

	return "", fmt.Errorf("resource group '%s' not found", resourceGroupName)
}

// VPCInstanceExists checks if a VPC instance exists by its ID
func (c *Client) VPCInstanceExists(ctx context.Context, instanceID string) (bool, error) {
	vpcClient, err := c.GetVPCClient(ctx)
	if err != nil {
		return false, fmt.Errorf("getting VPC client: %w", err)
	}

	_, err = vpcClient.GetInstance(ctx, instanceID)
	if err != nil {
		// If the error indicates the instance doesn't exist, return false
		if containsNotFoundError(err) {
			return false, nil
		}
		// For other errors, return the error
		return false, fmt.Errorf("checking VPC instance existence: %w", err)
	}

	return true, nil
}

// containsNotFoundError checks if an error indicates a resource was not found
func containsNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	// Check for common "not found" error patterns from IBM Cloud VPC API
	return containsAnyString(errStr, []string{
		"not found",
		"404",
		"Not Found",
		"does not exist",
		"cannot be found",
	})
}

// containsAnyString checks if a string contains any of the specified substrings
func containsAnyString(s string, substrings []string) bool {
	for _, substring := range substrings {
		if len(substring) > 0 && len(s) >= len(substring) {
			for i := 0; i <= len(s)-len(substring); i++ {
				if s[i:i+len(substring)] == substring {
					return true
				}
			}
		}
	}
	return false
}

// ExtractRegionFromZone extracts the region from an IBM Cloud zone name
// Examples: "br-sao-1" -> "br-sao", "eu-de-2" -> "eu-de", "us-south-3" -> "us-south"
func ExtractRegionFromZone(zone string) string {
	if zone == "" {
		return ""
	}

	// IBM Cloud zone format is typically "{region}-{zone-number}"
	// Find the last hyphen and take everything before it
	lastHyphen := strings.LastIndex(zone, "-")
	if lastHyphen == -1 {
		// If no hyphen found, return the zone as-is (shouldn't happen in normal cases)
		return zone
	}

	return zone[:lastHyphen]
}
