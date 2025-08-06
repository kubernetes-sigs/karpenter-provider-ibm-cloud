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
	"time"
	"net/http" 
)

// Client represents an IBM Cloud API client
type Client struct {
	vpcURL      string
	vpcAuthType string
	credStore   SecureCredentialManager
	region      string
	iamClient   *IAMClient
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
func (c *Client) GetVPCClient() (*VPCClient, error) {
	ctx := context.Background()
	vpcAPIKey, err := c.credStore.GetVPCAPIKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting VPC API key: %w", err)
	}
	return NewVPCClient(c.vpcURL, c.vpcAuthType, vpcAPIKey, c.region)
}

// GetGlobalCatalogClient returns a configured Global Catalog API client
func (c *Client) GetGlobalCatalogClient() (*GlobalCatalogClient, error) {
	return NewGlobalCatalogClient(c.iamClient), nil
}

// GetIKSClient returns a configured IKS API client interface
func (c *Client) GetIKSClient() IKSClientInterface {
	// Use pure API client with proper IBM Cloud Kubernetes Service REST API
	return NewIKSClient(c)
}

// GetIAMClient returns the IAM client
func (c *Client) GetIAMClient() *IAMClient {
	return c.iamClient
}

// GetRegion returns the configured region
func (c *Client) GetRegion() string {
	return c.region
}

// VPCInstanceExists checks if a VPC instance exists by its ID
func (c *Client) VPCInstanceExists(ctx context.Context, instanceID string) (bool, error) {
	vpcClient, err := c.GetVPCClient()
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

func containsNotFoundError(err error) bool {
    if err == nil {
        return false
    }

    errStr := err.Error()
    // Check for common "not found" error patterns from IBM Cloud VPC API
    return containsAnyString(errStr, []string{
        "not found",
        fmt.Sprintf("%d", http.StatusNotFound), // 404
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
