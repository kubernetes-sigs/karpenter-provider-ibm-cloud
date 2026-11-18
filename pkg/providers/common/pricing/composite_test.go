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

package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
	"github.com/stretchr/testify/require"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/batcher"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

func testProfile(name string) globalcatalogv1.CatalogEntry {
	return globalcatalogv1.CatalogEntry{
		ID: core.StringPtr(name), Name: core.StringPtr(name),
		Metadata: &globalcatalogv1.CatalogEntryMetadata{Other: map[string]interface{}{"profile": map[string]interface{}{"resource_type": "instance", "measures": []interface{}{map[string]interface{}{
			"component": "instance", "deployments": []interface{}{map[string]interface{}{
				"type": "multi-tenant", "plan": "plan", "meters": []interface{}{
					map[string]interface{}{"unit": "VCPU", "quantity": "1"},
					map[string]interface{}{"unit": "MEMORY", "quantity": "1"},
					map[string]interface{}{"unit": "PROFILE", "quantity": name},
				},
			}},
		}}}}},
	}
}

func testCompositeQuote(compute, memory float64) *globalcatalogv1.PricingGet {
	quote := &globalcatalogv1.PricingGet{}
	for _, component := range []struct {
		unit  string
		price float64
	}{{"VCPU_HOURS", compute}, {"MEMORY_HOURS", memory}} {
		quote.Metrics = append(quote.Metrics, globalcatalogv1.Metrics{
			ChargeUnitName: core.StringPtr(component.unit), ChargeUnitQuantity: core.Int64Ptr(1), TierModel: core.StringPtr("linear_tier"),
			Amounts: []globalcatalogv1.Amount{{Country: core.StringPtr("USA"), Currency: core.StringPtr("USD"), Prices: []globalcatalogv1.Price{{QuantityTier: core.Int64Ptr(1), Price: core.Float64Ptr(component.price)}}}},
		})
	}
	return quote
}

func TestIBMProfileDeploymentQuotesExcludeOSAndRemainRegional(t *testing.T) {
	data, err := os.ReadFile("testdata/ibm-profile-pricing.json")
	require.NoError(t, err)
	var fixture struct {
		Quotes   map[string]*globalcatalogv1.PricingGet `json:"quotes"`
		Profiles map[string]struct {
			ID    *string                `json:"id"`
			Name  *string                `json:"name"`
			Other map[string]interface{} `json:"other"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	for key, expected := range map[string]float64{
		"child-pricing:a736a57f-0584-474f-8411-55dc7d9dc811:12ad47e6": .201,
		"pricing:a736a57f-0584-474f-8411-55dc7d9dc811:dd7e58a9":       .232,
	} {
		price, priceErr := compositeHourlyPrice(fixture.Quotes[key], []string{"VCPU_HOURS", "MEMORY_HOURS"})
		require.NoError(t, priceErr)
		require.InDelta(t, expected, price, 1e-12)
	}
	for name, expected := range map[string][]string{
		"name:cx2d-4x8":       {"VCPU_HOURS", "MEMORY_HOURS", "IS_STORAGE_GIGABYTE_HOURS"},
		"name:gx2-8x64x1v100": {"VCPU_HOURS", "MEMORY_HOURS", "V100_HOURS"},
	} {
		entry := fixture.Profiles[name]
		plan, planErr := profilePlan(globalcatalogv1.CatalogEntry{ID: entry.ID, Name: entry.Name, Metadata: &globalcatalogv1.CatalogEntryMetadata{Other: entry.Other}})
		require.NoError(t, planErr)
		require.Equal(t, "a736a57f-0584-474f-8411-55dc7d9dc811", plan.plan)
		require.Equal(t, expected, plan.units)
	}
	entry := fixture.Profiles["name:bx3d-128x640"]
	plan, planErr := profilePlan(globalcatalogv1.CatalogEntry{ID: entry.ID, Name: entry.Name, Metadata: &globalcatalogv1.CatalogEntryMetadata{Other: entry.Other}})
	require.NoError(t, planErr)
	require.Equal(t, []string{"INSTANCE_HOURS_MULTI_TENANT"}, plan.units)
	require.Equal(t, "part-is.instance-hours-bx3d-128x640", plan.metric)
	price, priceErr := profileHourlyPrice(fixture.Quotes["gen3-regional"], plan)
	require.NoError(t, priceErr)
	require.Equal(t, 7.253, price)
	plan.metric = "part-is.instance-hours-bx3d-64x320"
	_, priceErr = profileHourlyPrice(fixture.Quotes["gen3-regional"], plan)
	require.Error(t, priceErr)
	entry = fixture.Profiles["name:bx4-4x16"]
	plan, planErr = profilePlan(globalcatalogv1.CatalogEntry{ID: entry.ID, Name: entry.Name, Metadata: &globalcatalogv1.CatalogEntryMetadata{Other: entry.Other}})
	require.NoError(t, planErr)
	price, priceErr = profileHourlyPrice(fixture.Quotes["gen4-regional"], plan)
	require.NoError(t, priceErr)
	require.Equal(t, .2039796, price)
}

func TestCompositeQuoteRequiresEveryResourceAndUSD(t *testing.T) {
	for name, mutate := range map[string]func(*globalcatalogv1.PricingGet){
		"missing memory": func(q *globalcatalogv1.PricingGet) { q.Metrics = q.Metrics[:1] },
		"non USD":        func(q *globalcatalogv1.PricingGet) { q.Metrics[1].Amounts[0].Currency = core.StringPtr("EUR") },
		"nil price":      func(q *globalcatalogv1.PricingGet) { q.Metrics[1].Amounts[0].Prices[0].Price = nil },
		"nil amounts":    func(q *globalcatalogv1.PricingGet) { q.Metrics[1].Amounts = nil },
		"tiered compute": func(q *globalcatalogv1.PricingGet) { q.Metrics[0].TierModel = core.StringPtr("block_tier") },
		"plan quantities": func(q *globalcatalogv1.PricingGet) {
			q.Metrics[0].ChargeUnitQuantity = core.Int64Ptr(63)
		},
		"negative": func(q *globalcatalogv1.PricingGet) { q.Metrics[1].Amounts[0].Prices[0].Price = core.Float64Ptr(-1) },
		"nan": func(q *globalcatalogv1.PricingGet) {
			q.Metrics[1].Amounts[0].Prices[0].Price = core.Float64Ptr(math.NaN())
		},
		"duplicate compute": func(q *globalcatalogv1.PricingGet) {
			q.Metrics = append(q.Metrics, q.Metrics[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			quote := testCompositeQuote(.111, .09)
			mutate(quote)
			price, err := compositeHourlyPrice(quote, []string{"VCPU_HOURS", "MEMORY_HOURS"})
			require.Error(t, err)
			require.Zero(t, price)
		})
	}
	quote := testCompositeQuote(.111, .09)
	_, err := compositeHourlyPrice(quote, []string{"VCPU_HOURS", "MEMORY_HOURS", "V100_HOURS"})
	require.Error(t, err)
}

type deploymentCatalog struct {
	requests []string
}

func (c *deploymentCatalog) ListInstanceTypes(context.Context) ([]globalcatalogv1.CatalogEntry, error) {
	return []globalcatalogv1.CatalogEntry{testProfile("one")}, nil
}

func (c *deploymentCatalog) ListPricingDeployments(_ context.Context, plan, region string) ([]globalcatalogv1.CatalogEntry, error) {
	c.requests = append(c.requests, "list:"+plan+":"+region)
	entries := []globalcatalogv1.CatalogEntry{}
	for _, target := range []struct{ id, parent, location, name string }{
		{"dedicated", "dedicated-plan", region, "one-" + region},
		{"foreign-region", plan, "eu-de", "one-eu-de"},
		{"unrelated", plan, region, "other-" + region},
		{"exact", plan, region, "one-" + region},
	} {
		entries = append(entries, globalcatalogv1.CatalogEntry{ID: core.StringPtr(target.id), Name: core.StringPtr(target.name), ParentID: core.StringPtr(target.parent), Tags: []string{"is.composite"}, Metadata: &globalcatalogv1.CatalogEntryMetadata{Deployment: &globalcatalogv1.CatalogEntryMetadataDeployment{Location: core.StringPtr(target.location)}}})
	}
	return entries, nil
}

func (c *deploymentCatalog) GetPricing(_ context.Context, id, region string) (*globalcatalogv1.PricingGet, error) {
	c.requests = append(c.requests, "quote:"+id+":"+region)
	if id != "exact" || region != "us-south" {
		return nil, fmt.Errorf("unexpected pricing target")
	}
	quote := testCompositeQuote(.111, .09)
	quote.DeploymentID, quote.DeploymentLocation, quote.DeploymentRegion = core.StringPtr(id), core.StringPtr(region), core.StringPtr(region)
	return quote, nil
}

func TestRefreshSelectsMultiTenantProfileDeploymentInRequestedRegion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	catalog := &deploymentCatalog{}
	provider := newTestProvider()
	provider.region, provider.lifecycle, provider.catalog = "us-south", ctx, catalog
	provider.zoneResolver = func(context.Context, string) ([]string, error) { return []string{"us-south-1", "us-south-2"}, nil }
	provider.pricingBatcher = batcher.NewPricingBatcher(ctx, catalog, "us-south")
	price, err := provider.GetPrice(ctx, "one", "us-south-2")
	require.NoError(t, err)
	require.InDelta(t, .201, price, 1e-12)
	prices, err := provider.GetPrices(ctx, "us-south-1")
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"one": price}, prices)
	require.Equal(t, []string{"list:plan:us-south", "quote:exact:us-south"}, catalog.requests)
}

type unpublishedCatalog struct {
	*refreshCatalog
	failure error
}

func (c *unpublishedCatalog) ListInstanceTypes(ctx context.Context) ([]globalcatalogv1.CatalogEntry, error) {
	profiles, err := c.refreshCatalog.ListInstanceTypes(ctx)
	if err != nil {
		return nil, err
	}
	profile := testProfile("unpublished")
	metadata := profile.Metadata.Other["profile"].(map[string]interface{})
	measure := metadata["measures"].([]interface{})[0].(map[string]interface{})
	measure["deployments"].([]interface{})[0].(map[string]interface{})["plan"] = "unpublished-plan"
	return append(profiles, profile), nil
}

func (c *unpublishedCatalog) ListPricingDeployments(ctx context.Context, plan, region string) ([]globalcatalogv1.CatalogEntry, error) {
	if plan == "unpublished-plan" {
		return nil, c.failure
	}
	return c.refreshCatalog.ListPricingDeployments(ctx, plan, region)
}

func TestUnpublishedPlanDoesNotBlockSupportedQuotesAndKnownPlanLossRetainsSnapshot(t *testing.T) {
	catalog := &unpublishedCatalog{refreshCatalog: &refreshCatalog{price: .25}, failure: &ibm.IBMError{StatusCode: 404, Type: ibm.ErrorTypeNotFound}}
	provider := refreshFixture(t, catalog.refreshCatalog)
	provider.catalog = catalog
	provider.pricingBatcher = batcher.NewPricingBatcher(provider.lifecycle, catalog, "us-south")
	require.NoError(t, provider.Refresh(context.Background()))
	require.Len(t, provider.pricingMap, 2)
	provider.pricingMap["unpublished"] = map[string]float64{"us-south-1": .5}
	provider.lastUpdate = provider.lastUpdate.Add(-13 * time.Hour)
	before := provider.lastUpdate
	require.Error(t, provider.Refresh(context.Background()))
	require.Equal(t, before, provider.lastUpdate)
	require.Equal(t, .5, provider.pricingMap["unpublished"]["us-south-1"])
	provider.retryAfter = time.Time{}
	catalog.failure = &ibm.IBMError{StatusCode: 403, Type: ibm.ErrorTypeForbidden}
	delete(provider.pricingMap, "unpublished")
	require.Error(t, provider.Refresh(context.Background()))
	require.Equal(t, before, provider.lastUpdate)
	require.Equal(t, .25, provider.pricingMap["one"]["us-south-1"])
}

type duplicateCatalog struct {
	*refreshCatalog
	mutex    sync.Mutex
	quotes   map[string]int
	conflict bool
}

func (c *duplicateCatalog) ListPricingDeployments(ctx context.Context, plan, region string) ([]globalcatalogv1.CatalogEntry, error) {
	entries, err := c.refreshCatalog.ListPricingDeployments(ctx, plan, region)
	if err != nil {
		return nil, err
	}
	duplicate := entries[0]
	duplicate.ID = core.StringPtr("one-duplicate")
	return append(entries, duplicate, entries[0]), nil
}

func (c *duplicateCatalog) GetPricing(_ context.Context, id, _ string) (*globalcatalogv1.PricingGet, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.quotes == nil {
		c.quotes = map[string]int{}
	}
	c.quotes[id]++
	compute := .111
	if id == "one-duplicate" && c.conflict {
		compute = .112
	}
	quote := testCompositeQuote(compute, .09)
	osCharge := quote.Metrics[0]
	osCharge.ChargeUnitName = core.StringPtr("WINDOWS_VCPU_HOURS")
	osCharge.Amounts = []globalcatalogv1.Amount{{Country: core.StringPtr("USA"), Currency: core.StringPtr("USD"), Prices: []globalcatalogv1.Price{{QuantityTier: core.Int64Ptr(1), Price: core.Float64Ptr(float64(len(id)))}}}}
	quote.Metrics = append(quote.Metrics, osCharge)
	return quote, nil
}

func TestDuplicateDeploymentsRequireEqualBaseQuoteAndIgnoreOptionalOSDifferences(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			catalog := &duplicateCatalog{refreshCatalog: &refreshCatalog{}, conflict: conflict}
			provider := refreshFixture(t, catalog.refreshCatalog)
			provider.catalog = catalog
			provider.pricingBatcher = batcher.NewPricingBatcher(provider.lifecycle, catalog, "us-south")
			require.NoError(t, provider.Refresh(context.Background()))
			require.InDelta(t, .201, provider.pricingMap["two"]["us-south-1"], 1e-12)
			if conflict {
				require.NotContains(t, provider.pricingMap, "one")
			} else {
				require.InDelta(t, .201, provider.pricingMap["one"]["us-south-1"], 1e-12)
			}
			catalog.mutex.Lock()
			require.Equal(t, map[string]int{"one": 1, "one-duplicate": 1, "two": 1}, catalog.quotes)
			catalog.mutex.Unlock()
		})
	}
	catalog := &duplicateCatalog{refreshCatalog: &refreshCatalog{}, conflict: true}
	provider := refreshFixture(t, catalog.refreshCatalog)
	provider.catalog = catalog
	provider.pricingBatcher = batcher.NewPricingBatcher(provider.lifecycle, catalog, "us-south")
	provider.pricingMap = map[string]map[string]float64{"one": {"us-south-1": .18}, "two": {"us-south-1": .19}}
	before := time.Now().Add(-13 * time.Hour)
	provider.lastUpdate = before
	require.Error(t, provider.Refresh(context.Background()))
	require.Equal(t, before, provider.lastUpdate)
	require.Equal(t, .18, provider.pricingMap["one"]["us-south-1"])
	require.Equal(t, .19, provider.pricingMap["two"]["us-south-1"])
}
