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
	"sync"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
)

type iamClientInterface interface {
	GetToken(context.Context) (string, error)
}

type globalCatalogClientInterface interface {
	GetCatalogEntryWithContext(context.Context, *globalcatalogv1.GetCatalogEntryOptions) (*globalcatalogv1.CatalogEntry, *core.DetailedResponse, error)
	ListCatalogEntriesWithContext(context.Context, *globalcatalogv1.ListCatalogEntriesOptions) (*globalcatalogv1.EntrySearchResult, *core.DetailedResponse, error)
}

type GlobalCatalogClient struct {
	mu           sync.Mutex
	iamClient    iamClientInterface
	client       globalCatalogClientInterface
	currentToken string
}

func NewGlobalCatalogClient(iamClient *IAMClient) *GlobalCatalogClient {
	return &GlobalCatalogClient{iamClient: iamClient}
}

func (c *GlobalCatalogClient) ensureClient(ctx context.Context) (globalCatalogClientInterface, error) {
	token, err := c.iamClient.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil && c.currentToken == token {
		return c.client, nil
	}
	authenticator := &core.BearerTokenAuthenticator{BearerToken: token}
	options := &globalcatalogv1.GlobalCatalogV1Options{Authenticator: authenticator}
	client, err := globalcatalogv1.NewGlobalCatalogV1(options)
	if err != nil {
		return nil, fmt.Errorf("initializing Global Catalog client: %w", err)
	}
	c.client = client
	c.currentToken = token
	return c.client, nil
}

func (c *GlobalCatalogClient) GetInstanceType(ctx context.Context, id string) (*globalcatalogv1.CatalogEntry, error) {
	cl, err := c.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	options := &globalcatalogv1.GetCatalogEntryOptions{
		ID:      &id,
		Include: core.StringPtr("*"),
	}
	entry, _, err := cl.GetCatalogEntryWithContext(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("getting catalog entry: %w", err)
	}
	return entry, nil
}

func (c *GlobalCatalogClient) ListInstanceTypes(ctx context.Context) ([]globalcatalogv1.CatalogEntry, error) {
	cl, err := c.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	var allEntries []globalcatalogv1.CatalogEntry
	offset := int64(0)
	limit := int64(100)
	for {
		q := "kind:vpc-instance-profile active:true"
		options := &globalcatalogv1.ListCatalogEntriesOptions{
			Q: &q, Include: core.StringPtr("*"), Complete: core.BoolPtr(true),
			Offset: &offset, Limit: &limit,
		}
		result, _, err := cl.ListCatalogEntriesWithContext(ctx, options)
		if err != nil {
			return nil, fmt.Errorf("listing catalog entries: %w", err)
		}
		if result == nil || len(result.Resources) == 0 {
			break
		}
		allEntries = append(allEntries, result.Resources...)
		if result.Count == nil || offset+limit >= *result.Count {
			break
		}
		offset += limit
	}
	return allEntries, nil
}

func (c *GlobalCatalogClient) GetPricing(ctx context.Context, catalogEntryID string, region string) (*globalcatalogv1.PricingGet, error) {
	cl, err := c.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	if sdkClient, ok := cl.(*globalcatalogv1.GlobalCatalogV1); ok {
		pricingOptions := &globalcatalogv1.GetPricingOptions{ID: &catalogEntryID}
		if region != "" {
			pricingOptions.DeploymentRegion = &region
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
