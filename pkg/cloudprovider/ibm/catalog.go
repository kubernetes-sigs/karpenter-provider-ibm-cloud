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

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
)

// iamClientInterface defines the interface for the IAM client
type iamClientInterface interface {
	GetToken(context.Context) (string, error)
}

// globalCatalogClientInterface defines the interface for the Global Catalog client
type globalCatalogClientInterface interface {
	GetCatalogEntryWithContext(context.Context, *globalcatalogv1.GetCatalogEntryOptions) (*globalcatalogv1.CatalogEntry, *core.DetailedResponse, error)
	ListCatalogEntriesWithContext(context.Context, *globalcatalogv1.ListCatalogEntriesOptions) (*globalcatalogv1.EntrySearchResult, *core.DetailedResponse, error)
}

// GlobalCatalogClient handles interactions with the IBM Cloud Global Catalog API
type GlobalCatalogClient struct {
	iamClient iamClientInterface
	client    globalCatalogClientInterface
}

func NewGlobalCatalogClient(iamClient *IAMClient) *GlobalCatalogClient {
	return &GlobalCatalogClient{
		iamClient: iamClient,
	}
}

func (c *GlobalCatalogClient) ensureClient(ctx context.Context) error {
	if c.client != nil {
		return nil
	}

	// Get a fresh token
	token, err := c.iamClient.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting IAM token: %w", err)
	}

	// Initialize the Global Catalog client with the token
	authenticator := &core.BearerTokenAuthenticator{
		BearerToken: token,
	}

	options := &globalcatalogv1.GlobalCatalogV1Options{
		Authenticator: authenticator,
	}

	client, err := globalcatalogv1.NewGlobalCatalogV1(options)
	if err != nil {
		return fmt.Errorf("initializing Global Catalog client: %w", err)
	}

	c.client = client
	return nil
}

func (c *GlobalCatalogClient) GetInstanceType(ctx context.Context, id string) (*globalcatalogv1.CatalogEntry, error) {
	if err := c.ensureClient(ctx); err != nil {
		return nil, err
	}

	options := &globalcatalogv1.GetCatalogEntryOptions{
		ID:      &id,
		Include: core.StringPtr("*"), // Include all fields
	}

	entry, _, err := c.client.GetCatalogEntryWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("getting catalog entry: %w", err)
	}

	return entry, nil
}

func (c *GlobalCatalogClient) ListInstanceTypes(ctx context.Context) ([]globalcatalogv1.CatalogEntry, error) {
	if err := c.ensureClient(ctx); err != nil {
		return nil, err
	}

	var allEntries []globalcatalogv1.CatalogEntry
	offset := int64(0)
	limit := int64(100)

	for {
		// Query for VPC instance profiles
		q := "kind:vpc-instance-profile active:true"

		options := &globalcatalogv1.ListCatalogEntriesOptions{
			Q:        &q,
			Include:  core.StringPtr("*"), // Include all fields
			Complete: core.BoolPtr(true),  // Get complete information
			Offset:   &offset,
			Limit:    &limit,
		}

		result, _, err := c.client.ListCatalogEntriesWithContext(ctx, options)
		if err != nil {
			return nil, fmt.Errorf("listing catalog entries: %w", err)
		}

		if result == nil || len(result.Resources) == 0 {
			break
		}

		allEntries = append(allEntries, result.Resources...)

		// If Count is nil or we've got all entries, break
		if result.Count == nil || offset+limit >= *result.Count {
			break
		}

		offset += limit
	}

	return allEntries, nil
}

func (c *GlobalCatalogClient) GetPricing(ctx context.Context, catalogEntryID string) (*globalcatalogv1.PricingGet, error) {
	if err := c.ensureClient(ctx); err != nil {
		return nil, err
	}

	// Cast the client interface to access GetPricing method
	if sdkClient, ok := c.client.(*globalcatalogv1.GlobalCatalogV1); ok {
		pricingOptions := &globalcatalogv1.GetPricingOptions{
			ID: &catalogEntryID,
		}

		pricingData, err := DoWithRateLimitRetry(ctx, func() (*globalcatalogv1.PricingGet, *core.DetailedResponse, error) {
			return sdkClient.GetPricing(pricingOptions)
		})
		if err != nil {
			return nil, fmt.Errorf("calling GetPricing API: %w", err)
		}

		return pricingData, nil
	}

	return nil, fmt.Errorf("invalid client type for GetPricing")
}
