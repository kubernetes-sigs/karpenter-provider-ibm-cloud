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
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/batcher"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/logging"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/metrics"
)

type pricingCatalog interface {
	ListInstanceTypes(context.Context) ([]globalcatalogv1.CatalogEntry, error)
	ListPricingDeployments(context.Context, string, string) ([]globalcatalogv1.CatalogEntry, error)
	GetPricing(context.Context, string, string) (*globalcatalogv1.PricingGet, error)
}

type IBMPricingProvider struct {
	catalog        pricingCatalog
	zoneResolver   func(context.Context, string) ([]string, error)
	client         *ibm.Client
	region         string
	pricingBatcher *batcher.PricingBatcher
	pricingMap     map[string]map[string]float64
	lastUpdate     time.Time
	retryAfter     time.Time
	refreshError   error
	mutex          sync.RWMutex
	ttl            time.Duration
	priceCache     *cache.Cache
	logger         *logging.Logger
	lifecycle      context.Context
	refreshFlight  singleflight.Group
	regionalMu     sync.Mutex
	regional       map[string]*IBMPricingProvider
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
		lifecycle:      ctx,
		regional:       map[string]*IBMPricingProvider{},
		region:         region,
		pricingBatcher: pricingBatcher,
		pricingMap:     make(map[string]map[string]float64),
		ttl:            12 * time.Hour,
		priceCache:     cache.NewNamed("prices", 12*time.Hour),
		logger:         logging.PricingLogger(),
	}
}

func (p *IBMPricingProvider) GetPrice(ctx context.Context, instanceType string, zone string) (float64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if regional := p.forZone(zone); regional != p {
		return regional.GetPrice(ctx, instanceType, zone)
	}
	cacheKey := fmt.Sprintf("price:%s:%s", instanceType, zone)
	p.mutex.RLock()
	if time.Since(p.lastUpdate) > p.ttl {
		p.mutex.RUnlock()
		if err := p.Refresh(ctx); err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
		}
		p.mutex.RLock()
	}
	if cached, exists := p.priceCache.Get(cacheKey); exists {
		p.mutex.RUnlock()
		return cached.(float64), nil
	}
	if zoneMap, exists := p.pricingMap[instanceType]; exists {
		if price, exists := zoneMap[zone]; exists {
			p.priceCache.Set(cacheKey, price)
			p.mutex.RUnlock()
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if regional := p.forZone(zone); regional != p {
		return regional.GetPrices(ctx, zone)
	}
	p.mutex.RLock()
	if time.Since(p.lastUpdate) > p.ttl {
		p.mutex.RUnlock()
		if err := p.Refresh(ctx); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
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

func (p *IBMPricingProvider) forZone(zone string) *IBMPricingProvider {
	p.mutex.RLock()
	for _, prices := range p.pricingMap {
		if _, exists := prices[zone]; exists {
			p.mutex.RUnlock()
			return p
		}
	}
	p.mutex.RUnlock()
	index := strings.LastIndex(zone, "-")
	if index <= 0 || zone[:index] == p.region {
		return p
	}
	region := zone[:index]
	p.regionalMu.Lock()
	defer p.regionalMu.Unlock()
	if p.regional == nil {
		p.regional = map[string]*IBMPricingProvider{}
	}
	if provider := p.regional[region]; provider != nil {
		return provider
	}
	lifecycle := p.lifecycle
	if lifecycle == nil {
		lifecycle = context.Background()
	}
	provider := NewIBMPricingProvider(lifecycle, p.client, region)
	p.regional[region] = provider
	return provider
}
func (p *IBMPricingProvider) Refresh(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.regionalMu.Lock()
	providers := make([]*IBMPricingProvider, 0, len(p.regional)+1)
	providers = append(providers, p)
	for _, provider := range p.regional {
		providers = append(providers, provider)
	}
	p.regionalMu.Unlock()
	var refreshes errgroup.Group
	refreshes.SetLimit(4)
	for _, provider := range providers {
		refreshes.Go(func() error { return provider.refreshRegion(ctx) })
	}
	return refreshes.Wait()
}

func (p *IBMPricingProvider) refreshRegion(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mutex.RLock()
	fresh := time.Since(p.lastUpdate) <= p.ttl
	retryAfter, previousError := p.retryAfter, p.refreshError
	p.mutex.RUnlock()
	if fresh {
		return nil
	}
	if time.Now().Before(retryAfter) {
		return previousError
	}
	result := p.refreshFlight.DoChan("refresh", func() (interface{}, error) {
		p.mutex.RLock()
		fresh := time.Since(p.lastUpdate) <= p.ttl
		retryAfter, previousError := p.retryAfter, p.refreshError
		p.mutex.RUnlock()
		if fresh {
			return nil, nil
		}
		if time.Now().Before(retryAfter) {
			return nil, previousError
		}
		if p.client == nil && p.catalog == nil {
			return nil, fmt.Errorf("IBM client not available for pricing API calls")
		}
		lifecycle := p.lifecycle
		if lifecycle == nil {
			lifecycle = context.Background()
		}
		refreshCtx, cancel := context.WithTimeout(lifecycle, 2*time.Minute)
		defer cancel()
		prices, err := p.fetchPricingData(refreshCtx)
		if err != nil {
			refreshError := fmt.Errorf("failed to fetch pricing data from IBM Cloud API: %w", err)
			p.mutex.Lock()
			p.retryAfter, p.refreshError = time.Now().Add(time.Minute), refreshError
			p.mutex.Unlock()
			p.logger.Warn("Failed to refresh pricing data", "error", refreshError)
			return nil, refreshError
		}
		p.mutex.Lock()
		p.pricingMap = prices
		p.lastUpdate = time.Now()
		p.retryAfter, p.refreshError = time.Time{}, nil
		p.priceCache.Clear()
		p.mutex.Unlock()
		return nil, nil
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-result:
		return r.Err
	}
}

func (p *IBMPricingProvider) fetchPricingData(ctx context.Context) (map[string]map[string]float64, error) {
	catalogClient := p.catalog
	if catalogClient == nil {
		if p.client == nil {
			return nil, fmt.Errorf("IBM client not initialized")
		}
		var err error
		catalogClient, err = p.client.GetGlobalCatalogClient()
		if err != nil {
			return nil, fmt.Errorf("getting catalog client: %w", err)
		}
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
	profiles := make([]*profilePricePlan, 0, len(instanceTypes))
	plans := map[string]bool{}
	for _, entry := range instanceTypes {
		profile, planErr := profilePlan(entry)
		if planErr != nil {
			return nil, planErr
		}
		if profile != nil {
			profiles = append(profiles, profile)
			plans[profile.plan] = true
		}
	}
	deployments := map[string]map[string][]globalcatalogv1.CatalogEntry{}
	var listings errgroup.Group
	listings.SetLimit(4)
	var mutex sync.Mutex
	for plan := range plans {
		listings.Go(func() error {
			entries, listErr := catalogClient.ListPricingDeployments(ctx, plan, p.region)
			if listErr != nil {
				var missing *ibm.IBMError
				if !errors.As(listErr, &missing) || !missing.IsNotFound() {
					return listErr
				}
				p.mutex.RLock()
				for _, profile := range profiles {
					if profile.plan == plan && len(p.pricingMap[profile.profile]) > 0 {
						p.mutex.RUnlock()
						return listErr
					}
				}
				p.mutex.RUnlock()
			}
			byName := map[string][]globalcatalogv1.CatalogEntry{}
			seen := map[string]bool{}
			for _, entry := range entries {
				if !compositeDeployment(entry, plan, p.region) {
					continue
				}
				if seen[*entry.ID] {
					continue
				}
				seen[*entry.ID] = true
				byName[*entry.Name] = append(byName[*entry.Name], entry)
			}
			mutex.Lock()
			deployments[plan] = byName
			mutex.Unlock()
			return nil
		})
	}
	if err = listings.Wait(); err != nil {
		return nil, err
	}
	var calls errgroup.Group
	calls.SetLimit(16)
	var failures []error
	for _, profile := range profiles {
		name := profile.profile + "-" + p.region
		_, compositeExists := deployments[profile.plan][name]
		_, rcExists := deployments[profile.plan][name+"-rc"]
		if compositeExists && rcExists {
			p.mutex.RLock()
			known := len(p.pricingMap[profile.profile]) > 0
			p.mutex.RUnlock()
			if known {
				return nil, fmt.Errorf("ambiguous pricing deployment for %s in %s", profile.profile, p.region)
			}
			continue
		}
		if profile.metric != "" {
			name += "-rc"
		}
		candidates, exists := deployments[profile.plan][name]
		if !exists {
			p.mutex.RLock()
			known := len(p.pricingMap[profile.profile]) > 0
			p.mutex.RUnlock()
			if known {
				return nil, fmt.Errorf("previously priced deployment missing for %s in %s", profile.profile, p.region)
			}
			continue
		}
		calls.Go(func() error {
			var price float64
			var quoteErr error
			for _, deployment := range candidates {
				quote, fetchErr := p.fetchPricingQuote(ctx, *deployment.ID)
				if fetchErr != nil {
					quoteErr = fetchErr
					break
				}
				candidatePrice, priceErr := profileHourlyPrice(quote, profile)
				if priceErr != nil {
					quoteErr = priceErr
					break
				}
				if price != 0 && price != candidatePrice {
					p.mutex.RLock()
					known := len(p.pricingMap[profile.profile]) > 0
					p.mutex.RUnlock()
					if !known {
						return nil
					}
					quoteErr = fmt.Errorf("conflicting deployment prices")
					break
				}
				price = candidatePrice
			}
			mutex.Lock()
			defer mutex.Unlock()
			if quoteErr != nil {
				failures = append(failures, fmt.Errorf("pricing %s in %s: %w", profile.profile, p.region, quoteErr))
				return nil
			}
			byZone := map[string]float64{}
			for _, zone := range zones {
				byZone[zone] = price
			}
			pricingMap[profile.profile] = byZone
			return nil
		})
	}
	_ = calls.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	if len(pricingMap) == 0 {
		return nil, fmt.Errorf("no pricing entries returned")
	}

	return pricingMap, nil
}

func (p *IBMPricingProvider) getZonesForRegion(ctx context.Context, region string) ([]string, error) {
	if p.zoneResolver != nil {
		return p.zoneResolver(ctx, region)
	}
	vpcClient, err := p.client.GetVPCClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting VPC client: %w", err)
	}
	vpcClient, err = vpcClient.ForRegion(region)
	if err != nil {
		return nil, err
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

func (p *IBMPricingProvider) fetchInstancePricing(ctx context.Context, catalogClient pricingCatalog, entry globalcatalogv1.CatalogEntry) (float64, error) {
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
	return p.fetchCompositePricing(ctx, catalogEntryID, []string{"VCPU_HOURS", "MEMORY_HOURS"})
}

func (p *IBMPricingProvider) fetchCompositePricing(ctx context.Context, catalogEntryID string, units []string) (float64, error) {
	quote, err := p.fetchPricingQuote(ctx, catalogEntryID)
	if err != nil {
		return 0, err
	}
	return compositeHourlyPrice(quote, units)
}

func (p *IBMPricingProvider) fetchPricingQuote(ctx context.Context, catalogEntryID string) (*globalcatalogv1.PricingGet, error) {
	if p.pricingBatcher == nil {
		return nil, fmt.Errorf("pricing batcher not initialized")
	}
	pricingData, err := p.pricingBatcher.GetPricing(ctx, catalogEntryID)
	if err != nil {
		return nil, fmt.Errorf("calling GetPricing API: %w", err)
	}
	if pricingData != nil && (pricingData.DeploymentID != nil && *pricingData.DeploymentID != catalogEntryID || pricingData.DeploymentLocation != nil && *pricingData.DeploymentLocation != p.region || pricingData.DeploymentRegion != nil && *pricingData.DeploymentRegion != p.region) {
		return nil, fmt.Errorf("pricing response belongs to a different deployment or region")
	}
	return pricingData, nil
}
