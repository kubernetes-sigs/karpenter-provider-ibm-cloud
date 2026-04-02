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
	"testing"
	"time"

	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/batcher"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	fakedata "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/fake"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/logging"
)

func newTestProvider() *IBMPricingProvider {
	return &IBMPricingProvider{
		pricingBatcher: nil,
		pricingMap:     make(map[string]map[string]float64),
		ttl:            12 * time.Hour,
		priceCache:     cache.New(12 * time.Hour),
		logger:         logging.PricingLogger(),
	}
}

func TestFetchPricingFromAPI_NilBatcher(t *testing.T) {
	provider := newTestProvider()
	ctx := context.Background()

	price, err := provider.fetchPricingFromAPI(ctx, "some-catalog-id")
	assert.Equal(t, 0.0, price)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pricing batcher not initialized")
}

func TestFetchPricingFromAPI_EmptyMetricsResponse(t *testing.T) {
	ctx := context.Background()
	fakePricing := fakedata.NewPricingAPI()
	// Return a PricingGet with no Metrics at all.
	fakePricing.PricingByID["empty-id"] = &globalcatalogv1.PricingGet{}

	provider := &IBMPricingProvider{
		pricingBatcher: batcher.NewPricingBatcher(ctx, fakePricing, "us-south"),
		pricingMap:     make(map[string]map[string]float64),
		ttl:            12 * time.Hour,
		priceCache:     cache.New(12 * time.Hour),
		logger:         logging.PricingLogger(),
	}

	price, err := provider.fetchPricingFromAPI(ctx, "empty-id")
	assert.Equal(t, 0.0, price)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pricing data found in API response")
}

func TestFetchPricingFromAPI_NonUSDFallback(t *testing.T) {
	ctx := context.Background()
	fakePricing := fakedata.NewPricingAPI()

	eur := "EUR"
	eurPrice := 0.088
	fakePricing.PricingByID["eur-id"] = &globalcatalogv1.PricingGet{
		Metrics: []globalcatalogv1.Metrics{
			{
				Amounts: []globalcatalogv1.Amount{
					{
						Currency: &eur,
						Prices:   []globalcatalogv1.Price{{Price: &eurPrice}},
					},
				},
			},
		},
	}

	provider := &IBMPricingProvider{
		pricingBatcher: batcher.NewPricingBatcher(ctx, fakePricing, "us-south"),
		pricingMap:     make(map[string]map[string]float64),
		ttl:            12 * time.Hour,
		priceCache:     cache.New(12 * time.Hour),
		logger:         logging.PricingLogger(),
	}

	price, err := provider.fetchPricingFromAPI(ctx, "eur-id")
	require.NoError(t, err)
	assert.Equal(t, eurPrice, price)
}

func TestFetchPricingFromAPI_NilAmounts(t *testing.T) {
	ctx := context.Background()
	fakePricing := fakedata.NewPricingAPI()

	fakePricing.PricingByID["nil-amounts-id"] = &globalcatalogv1.PricingGet{
		Metrics: []globalcatalogv1.Metrics{
			{Amounts: nil},
		},
	}

	provider := &IBMPricingProvider{
		pricingBatcher: batcher.NewPricingBatcher(ctx, fakePricing, "us-south"),
		pricingMap:     make(map[string]map[string]float64),
		ttl:            12 * time.Hour,
		priceCache:     cache.New(12 * time.Hour),
		logger:         logging.PricingLogger(),
	}

	price, err := provider.fetchPricingFromAPI(ctx, "nil-amounts-id")
	assert.Equal(t, 0.0, price)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pricing data found in API response")
}

func TestFetchPricingFromAPI_NilPricePointer(t *testing.T) {
	ctx := context.Background()
	fakePricing := fakedata.NewPricingAPI()

	usd := "USD"
	fakePricing.PricingByID["nil-price-id"] = &globalcatalogv1.PricingGet{
		Metrics: []globalcatalogv1.Metrics{
			{
				Amounts: []globalcatalogv1.Amount{
					{
						Currency: &usd,
						Prices:   []globalcatalogv1.Price{{Price: nil}},
					},
				},
			},
		},
	}

	provider := &IBMPricingProvider{
		pricingBatcher: batcher.NewPricingBatcher(ctx, fakePricing, "us-south"),
		pricingMap:     make(map[string]map[string]float64),
		ttl:            12 * time.Hour,
		priceCache:     cache.New(12 * time.Hour),
		logger:         logging.PricingLogger(),
	}

	price, err := provider.fetchPricingFromAPI(ctx, "nil-price-id")
	assert.Equal(t, 0.0, price)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pricing data found in API response")
}

func TestFetchPricingFromAPI_NilPriceFallsBackToNext(t *testing.T) {
	ctx := context.Background()
	fakePricing := fakedata.NewPricingAPI()

	eur := "EUR"
	validPrice := 0.075
	fakePricing.PricingByID["mixed-id"] = &globalcatalogv1.PricingGet{
		Metrics: []globalcatalogv1.Metrics{
			{
				Amounts: []globalcatalogv1.Amount{
					{
						Currency: &eur,
						Prices: []globalcatalogv1.Price{
							{Price: nil},
							{Price: &validPrice},
						},
					},
				},
			},
		},
	}

	provider := &IBMPricingProvider{
		pricingBatcher: batcher.NewPricingBatcher(ctx, fakePricing, "us-south"),
		pricingMap:     make(map[string]map[string]float64),
		ttl:            12 * time.Hour,
		priceCache:     cache.New(12 * time.Hour),
		logger:         logging.PricingLogger(),
	}

	price, err := provider.fetchPricingFromAPI(ctx, "mixed-id")
	require.NoError(t, err)
	assert.Equal(t, validPrice, price)
}

func TestRefresh_FreshData(t *testing.T) {
	provider := newTestProvider()
	provider.pricingMap["bx2-2x8"] = map[string]float64{"us-south-1": 0.096}
	// Set lastUpdate to 1 minute ago (well within 12h TTL), so data is still fresh
	lastUpdate := time.Now().Add(-1 * time.Minute)
	provider.lastUpdate = lastUpdate

	err := provider.Refresh(context.Background())
	assert.NoError(t, err)

	// Verify refresh was skipped: lastUpdate unchanged and pricingMap unmodified
	assert.Equal(t, lastUpdate, provider.lastUpdate)
	assert.Equal(t, 0.096, provider.pricingMap["bx2-2x8"]["us-south-1"])
}

func TestGetPrice_InstanceExistsZoneNotFound(t *testing.T) {
	provider := newTestProvider()
	provider.pricingMap = map[string]map[string]float64{
		"bx2-2x8": {
			"us-south-1": 0.096,
		},
	}
	provider.lastUpdate = time.Now()

	price, err := provider.GetPrice(context.Background(), "bx2-2x8", "eu-de-1")
	assert.Equal(t, 0.0, price)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pricing data available for instance type bx2-2x8 in zone eu-de-1")
}

func TestGetPrices_ZoneNotFoundAfterExpiredTTL(t *testing.T) {
	provider := newTestProvider()
	provider.pricingMap = map[string]map[string]float64{
		"bx2-2x8": {
			"us-south-1": 0.096,
		},
	}
	provider.lastUpdate = time.Now().Add(-24 * time.Hour) // expired

	prices, err := provider.GetPrices(context.Background(), "eu-de-1")
	assert.Nil(t, prices)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pricing data available for zone eu-de-1")
}

func TestFetchPricingFromAPI_USDTakesPrecedence(t *testing.T) {
	ctx := context.Background()
	fakePricing := fakedata.NewPricingAPI()

	eur := "EUR"
	usd := "USD"
	eurPrice := 0.088
	usdPrice := 0.096
	fakePricing.PricingByID["multi-currency-id"] = &globalcatalogv1.PricingGet{
		Metrics: []globalcatalogv1.Metrics{
			{
				Amounts: []globalcatalogv1.Amount{
					{
						Currency: &eur,
						Prices:   []globalcatalogv1.Price{{Price: &eurPrice}},
					},
					{
						Currency: &usd,
						Prices:   []globalcatalogv1.Price{{Price: &usdPrice}},
					},
				},
			},
		},
	}

	provider := &IBMPricingProvider{
		pricingBatcher: batcher.NewPricingBatcher(ctx, fakePricing, "us-south"),
		pricingMap:     make(map[string]map[string]float64),
		ttl:            12 * time.Hour,
		priceCache:     cache.New(12 * time.Hour),
		logger:         logging.PricingLogger(),
	}

	price, err := provider.fetchPricingFromAPI(ctx, "multi-currency-id")
	require.NoError(t, err)
	assert.Equal(t, usdPrice, price)
}

func TestFetchPricingFromAPI_WithFakeNewPricingGet(t *testing.T) {
	ctx := context.Background()
	fakePricing := fakedata.NewPricingAPI()
	fakePricing.PricingByID["test-id"] = fakedata.NewPricingGet(0.15)

	provider := &IBMPricingProvider{
		pricingBatcher: batcher.NewPricingBatcher(ctx, fakePricing, "us-south"),
		pricingMap:     make(map[string]map[string]float64),
		ttl:            12 * time.Hour,
		priceCache:     cache.New(12 * time.Hour),
		logger:         logging.PricingLogger(),
	}

	price, err := provider.fetchPricingFromAPI(ctx, "test-id")
	require.NoError(t, err)
	assert.Equal(t, 0.15, price)
	assert.Equal(t, int64(1), fakePricing.GetCallCount())
}
