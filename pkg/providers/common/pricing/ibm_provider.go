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
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/batcher"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/logging"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/metrics"
)

type IBMPricingProvider struct {
	client         *ibm.Client
	region         string
	pricingBatcher *batcher.PricingBatcher
	pricingMap     map[string]map[string]float64
	lastUpdate     time.Time
	mutex          sync.RWMutex
	ttl            time.Duration
	priceCache     *cache.Cache
	logger         *logging.Logger
}

func NewIBMPricingProvider(ctx context.Context, client *ibm.Client, region string) *IBMPricingProvider {
	var pricingBatcher *batcher.PricingBatcher
	if client != nil {
		if catalogClient, err := client.GetGlobalCatalogClient(); err == nil {
			pricingBatcher = batcher.NewPricingBatcher(ctx, catalogClient, region)
		}
	}
	return &IBMPricingProvider{
		client:         client,
		region:         region,
		pricingBatcher: pricingBatcher,
		pricingMap:     make(map[string]map[string]float64),
		ttl:            12 * time.Hour,
		priceCache:     cache.New(12 * time.Hour),
		logger:         logging.PricingLogger(),
	}
}

func (p *IBMPricingProvider) GetPrice(ctx context.Context, instanceType string, zone string) (float64, error) {
	cacheKey := fmt.Sprintf("price:%s:%s", instanceType, zone)
	if cached, exists := p.priceCache.Get(cacheKey); exists {
		return cached.(float64), nil
	}
	p.mutex.RLock()
	if time.Since(p.lastUpdate) > p.ttl {
		p.mutex.RUnlock()
		if err := p.Refresh(ctx); err != nil {
			p.logger.Warn("Failed to refresh pricing data", "error", err)
		}
		p.mutex.RLock()
	}
	if zoneMap, exists := p.pricingMap[instanceType]; exists {
		if price, exists := zoneMap[zone]; exists {
			p.mutex.RUnlock()
			p.priceCache.Set(cacheKey, price)
			region := zone
			if idx := strings.LastIndex(zone, "-"); idx > 0 {
				region = zone[:idx]
			}
			metrics.CostPerHour.WithLabelValues(instanceType, region).Set(price)
			return price, nil
		}
	}
	p.mutex.RUnlock()
	return 0, fmt.Errorf("no pricing data available for instance type %s in zone %s", instanceType, zone)
}

func (p *IBMPricingProvider) GetPrices(ctx context.Context, zone string) (map[string]float64, error) {
	p.mutex.RLock()
	if time.Since(p.lastUpdate) > p.ttl {
		p.mutex.RUnlock()
		if err := p.Refresh(ctx); err != nil {
			p.logger.Warn("Failed to refresh pricing data", "error", err)
		}
		p.mutex.RLock()
	}
	prices := make(map[string]float64)
	for instanceType, zoneMap := range p.pricingMap {
		if price, exists := zoneMap[zone]; exists {
			prices[instanceType] = price
		}
	}
	p.mutex.RUnlock()
	if len(prices) == 0 {
		return nil, fmt.Errorf("no pricing data available for zone %s", zone)
	}
	return prices, nil
}

func (p *IBMPricingProvider) Refresh(ctx context.Context) error {
	p.mutex.RLock()
	fresh := time.Since(p.lastUpdate) <= p.ttl
	p.mutex.RUnlock()
	if fresh {
		return nil
	}
	if p.client == nil {
		return fmt.Errorf("IBM client not available for pricing API calls")
	}
	newPricingMap, err := p.fetchPricingData(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch pricing data from IBM Cloud API: %w", err)
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if time.Since(p.lastUpdate) <= p.ttl {
		return nil
	}
	p.pricingMap = newPricingMap
	p.lastUpdate = time.Now()
	return nil
}

func (p *IBMPricingProvider) fetchPricingData(ctx context.Context) (map[string]map[string]float64, error) {
	if p.client == nil {
		return nil, fmt.Errorf("IBM client not initialized")
	}
	catalogClient, err := p.client.GetGlobalCatalogClient()
	if err != nil {
		return nil, fmt.Errorf("getting catalog client: %w", err)
	}
	instanceTypes, err := catalogClient.ListInstanceTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing instance types: %w", err)
	}
	pricingMap := make(map[string]map[string]float64)
	zones, err := p.getZonesForRegion(ctx, p.region)
	if err != nil {
		return nil, fmt.Errorf("getting zones for region %s: %w", p.region, err)
	}
	for _, entry := range instanceTypes {
		if entry.Name == nil {
			continue
		}
		instanceTypeName := *entry.Name
		price, err := p.fetchInstancePricing(ctx, catalogClient, entry)
		if err != nil {
			p.logger.Warn("Skipping instance type due to pricing error",
				"instanceType", instanceTypeName, "error", err)
			continue
		}
		pricingMap[instanceTypeName] = make(map[string]float64)
		for _, zone := range zones {
			pricingMap[instanceTypeName][zone] = price
		}
	}
	return pricingMap, nil
}

func (p *IBMPricingProvider) getZonesForRegion(ctx context.Context, region string) ([]string, error) {
	vpcClient, err := p.client.GetVPCClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting VPC client: %w", err)
	}
	sdkClient := vpcClient.GetSDKClient()
	if sdkClient == nil {
		return nil, fmt.Errorf("VPC SDK client not available")
	}
	zonesResult, _, err := sdkClient.ListRegionZonesWithContext(ctx, &vpcv1.ListRegionZonesOptions{
		RegionName: &region,
	})
	if err != nil {
		return nil, fmt.Errorf("listing zones for region %s: %w", region, err)
	}
	if zonesResult == nil || zonesResult.Zones == nil {
		return nil, fmt.Errorf("no zones found for region %s", region)
	}
	var zones []string
	for _, zone := range zonesResult.Zones {
		if zone.Name != nil {
			zones = append(zones, *zone.Name)
		}
	}
	if len(zones) == 0 {
		return nil, fmt.Errorf("no zones found for region %s", region)
	}
	return zones, nil
}

func (p *IBMPricingProvider) fetchInstancePricing(ctx context.Context, catalogClient *ibm.GlobalCatalogClient, entry globalcatalogv1.CatalogEntry) (float64, error) {
	if entry.ID == nil {
		return 0, fmt.Errorf("catalog entry ID is nil")
	}
	catalogEntryID := *entry.ID
	price, err := p.fetchPricingFromAPI(ctx, catalogEntryID)
	if err != nil {
		return 0, fmt.Errorf("fetching pricing for %s: %w", *entry.Name, err)
	}
	return price, nil
}

func (p *IBMPricingProvider) fetchPricingFromAPI(ctx context.Context, catalogEntryID string) (float64, error) {
	if p.pricingBatcher == nil {
		return 0, fmt.Errorf("pricing batcher not initialized")
	}
	pricingData, err := p.pricingBatcher.GetPricing(ctx, catalogEntryID)
	if err != nil {
		return 0, fmt.Errorf("calling GetPricing API: %w", err)
	}
	if pricingData.Metrics != nil {
		var fallbackPrice *float64
		for _, metric := range pricingData.Metrics {
			if metric.Amounts == nil {
				continue
			}
			for _, amount := range metric.Amounts {
				if amount.Prices == nil {
					continue
				}
				for _, priceObj := range amount.Prices {
					if priceObj.Price == nil {
						continue
					}
					if amount.Currency != nil && *amount.Currency == "USD" {
						return *priceObj.Price, nil
					}
					if fallbackPrice == nil {
						fallbackPrice = priceObj.Price
					}
				}
			}
		}
		if fallbackPrice != nil {
			return *fallbackPrice, nil
		}
	}
	return 0, fmt.Errorf("no pricing data found in API response")
}
