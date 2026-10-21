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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
	"github.com/stretchr/testify/require"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/batcher"
)

type refreshCatalog struct {
	mutex     sync.Mutex
	listCalls int
	regions   map[string]int
	price     float64
	failure   error
	entered   chan struct{}
	release   chan struct{}
}

func (c *refreshCatalog) ListInstanceTypes(context.Context) ([]globalcatalogv1.CatalogEntry, error) {
	c.mutex.Lock()
	c.listCalls++
	c.mutex.Unlock()
	return []globalcatalogv1.CatalogEntry{testProfile("one"), testProfile("two")}, nil
}

func (c *refreshCatalog) ListPricingDeployments(_ context.Context, plan, region string) ([]globalcatalogv1.CatalogEntry, error) {
	return []globalcatalogv1.CatalogEntry{
		{ID: core.StringPtr("one"), Name: core.StringPtr("one-" + region), ParentID: core.StringPtr(plan), Tags: []string{"is.composite"}, Metadata: &globalcatalogv1.CatalogEntryMetadata{Deployment: &globalcatalogv1.CatalogEntryMetadataDeployment{Location: core.StringPtr(region)}}},
		{ID: core.StringPtr("two"), Name: core.StringPtr("two-" + region), ParentID: core.StringPtr(plan), Tags: []string{"is.composite"}, Metadata: &globalcatalogv1.CatalogEntryMetadata{Deployment: &globalcatalogv1.CatalogEntryMetadataDeployment{Location: core.StringPtr(region)}}},
	}, nil
}
func (c *refreshCatalog) GetPricing(ctx context.Context, id, region string) (*globalcatalogv1.PricingGet, error) {
	if c.entered != nil {
		select {
		case c.entered <- struct{}{}:
		default:
		}
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.regions == nil {
		c.regions = map[string]int{}
	}
	c.regions[region]++
	if id == "two" && c.failure != nil {
		return nil, c.failure
	}
	return testCompositeQuote(c.price, 0), nil
}
func refreshFixture(t *testing.T, catalog *refreshCatalog, regions ...string) *IBMPricingProvider {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	p := newTestProvider()
	p.region = "us-south"
	if len(regions) > 0 {
		p.region = regions[0]
	}
	p.lifecycle = ctx
	p.catalog = catalog
	p.zoneResolver = func(_ context.Context, region string) ([]string, error) { return []string{region + "-1"}, nil }
	p.pricingBatcher = batcher.NewPricingBatcher(ctx, catalog, p.region)
	return p
}

func TestGetPriceRefreshesExpiredSnapshotBeforeLateQuoteCacheHit(t *testing.T) {
	catalog := &refreshCatalog{price: .25}
	p := refreshFixture(t, catalog)
	p.pricingMap = map[string]map[string]float64{"one": {"us-south-1": .1}}
	p.lastUpdate = time.Now().Add(-13 * time.Hour)
	p.priceCache.Set("price:one:us-south-1", .1)
	price, err := p.GetPrice(context.Background(), "one", "us-south-1")
	require.NoError(t, err)
	require.Equal(t, .25, price)
	catalog.mutex.Lock()
	calls := catalog.listCalls
	catalog.mutex.Unlock()
	require.Equal(t, 1, calls)
}

func TestRefreshIncludesExistingRegionWhenDefaultSnapshotIsFresh(t *testing.T) {
	rootCatalog := &refreshCatalog{price: .1}
	root := refreshFixture(t, rootCatalog)
	root.pricingMap = map[string]map[string]float64{"one": {"us-south-1": .1}}
	root.lastUpdate = time.Now()
	catalog := &refreshCatalog{price: .25}
	regional := refreshFixture(t, catalog, "eu-de")
	regional.pricingMap = map[string]map[string]float64{"one": {"eu-de-1": .1}}
	previous := time.Now().Add(-13 * time.Hour)
	regional.lastUpdate = previous
	regional.priceCache.Set("price:one:eu-de-1", .1)
	root.regional = map[string]*IBMPricingProvider{"eu-de": regional}
	require.NoError(t, root.Refresh(context.Background()))
	require.True(t, regional.lastUpdate.After(previous))
	price, err := root.GetPrice(context.Background(), "one", "eu-de-1")
	require.NoError(t, err)
	require.Equal(t, .25, price)
	catalog.mutex.Lock()
	require.Equal(t, map[string]int{"eu-de": 2}, catalog.regions)
	catalog.mutex.Unlock()
	rootCatalog.mutex.Lock()
	require.Zero(t, rootCatalog.listCalls)
	rootCatalog.mutex.Unlock()
}

func TestRegionalRefreshSharesWorkAndCallerCancellation(t *testing.T) {
	root := refreshFixture(t, &refreshCatalog{price: .1})
	root.lastUpdate = time.Now()
	catalog := &refreshCatalog{price: .25, entered: make(chan struct{}, 1), release: make(chan struct{})}
	regional := refreshFixture(t, catalog, "eu-de")
	root.regional = map[string]*IBMPricingProvider{"eu-de": regional}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { first <- root.Refresh(ctx) }()
	select {
	case <-catalog.entered:
	case <-time.After(time.Second):
		t.Fatal("regional refresh did not reach pricing")
	}
	second := make(chan error, 1)
	go func() { second <- root.Refresh(context.Background()) }()
	cancel()
	select {
	case err := <-first:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("regional waiter ignored cancellation")
	}
	close(catalog.release)
	require.NoError(t, <-second)
	catalog.mutex.Lock()
	calls := catalog.listCalls
	catalog.mutex.Unlock()
	require.Equal(t, 1, calls)
}

func TestRefreshSharesWorkAndCallerCancellation(t *testing.T) {
	catalog := &refreshCatalog{price: .25, entered: make(chan struct{}, 1), release: make(chan struct{})}
	p := refreshFixture(t, catalog)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { first <- p.Refresh(ctx) }()
	select {
	case <-catalog.entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not reach pricing")
	}
	second := make(chan error, 1)
	go func() { second <- p.Refresh(context.Background()) }()
	cancel()
	select {
	case err := <-first:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("waiter ignored cancellation")
	}
	close(catalog.release)
	require.NoError(t, <-second)
	catalog.mutex.Lock()
	calls := catalog.listCalls
	catalog.mutex.Unlock()
	require.Equal(t, 1, calls)
	price, err := p.GetPrice(context.Background(), "one", "us-south-1")
	require.NoError(t, err)
	require.Equal(t, .25, price)
}

func TestFailedRefreshKeepsCompleteSnapshotAndSuccessfulRefreshClearsQuoteCache(t *testing.T) {
	catalog := &refreshCatalog{price: .25, failure: errors.New("quote unavailable")}
	p := refreshFixture(t, catalog)
	p.pricingMap = map[string]map[string]float64{"one": {"us-south-1": .1}, "two": {"us-south-1": .1}}
	previous := time.Now().Add(-24 * time.Hour)
	p.lastUpdate = previous
	p.priceCache.Set("price:one:us-south-1", .1)
	require.Error(t, p.Refresh(context.Background()))
	require.Equal(t, previous, p.lastUpdate)
	require.Equal(t, .1, p.pricingMap["one"]["us-south-1"])
	catalog.mutex.Lock()
	catalog.failure = nil
	catalog.mutex.Unlock()
	p.retryAfter = time.Time{}
	require.NoError(t, p.Refresh(context.Background()))
	price, err := p.GetPrice(context.Background(), "one", "us-south-1")
	require.NoError(t, err)
	require.Equal(t, .25, price)
}

func TestFailedRefreshCooldownBoundsRepeatedOfferingReads(t *testing.T) {
	catalog := &refreshCatalog{failure: errors.New("quote unavailable")}
	p := refreshFixture(t, catalog)
	for range 500 {
		_, err := p.GetPrice(context.Background(), "one", "us-south-1")
		require.Error(t, err)
	}
	catalog.mutex.Lock()
	calls := catalog.listCalls
	catalog.mutex.Unlock()
	require.Equal(t, 1, calls)
	require.True(t, p.lastUpdate.IsZero())
	require.Empty(t, p.pricingMap)
	require.True(t, p.retryAfter.After(time.Now()))
	catalog.mutex.Lock()
	catalog.failure, catalog.price = nil, .25
	catalog.mutex.Unlock()
	p.retryAfter = time.Now().Add(-time.Second)
	price, err := p.GetPrice(context.Background(), "one", "us-south-1")
	require.NoError(t, err)
	require.Equal(t, .25, price)
	require.True(t, p.retryAfter.IsZero())
	require.Nil(t, p.refreshError)
}
