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

package fake

import (
	"context"
	"sync/atomic"

	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
)

// PricingAPI implements a fake Global Catalog Pricing API for testing
type PricingAPI struct {
	CallCount atomic.Int64

	// Configurable responses per catalog entry ID
	PricingByID map[string]*globalcatalogv1.PricingGet
	ErrorByID   map[string]error
}

// NewPricingAPI creates a new fake Pricing API
func NewPricingAPI() *PricingAPI {
	return &PricingAPI{
		PricingByID: make(map[string]*globalcatalogv1.PricingGet),
		ErrorByID:   make(map[string]error),
	}
}

// GetPricing implements the pricingClient interface for testing
func (f *PricingAPI) GetPricing(ctx context.Context, catalogEntryID string) (*globalcatalogv1.PricingGet, error) {
	f.CallCount.Add(1)

	if err, ok := f.ErrorByID[catalogEntryID]; ok {
		return nil, err
	}

	if pricing, ok := f.PricingByID[catalogEntryID]; ok {
		return pricing, nil
	}

	return &globalcatalogv1.PricingGet{}, nil
}

// GetCallCount returns the number of times GetPricing was called
func (f *PricingAPI) GetCallCount() int64 {
	return f.CallCount.Load()
}

// NewPricingGet creates a PricingGet response with the specified USD hourly price
func NewPricingGet(price float64) *globalcatalogv1.PricingGet {
	country := "USA"
	return &globalcatalogv1.PricingGet{
		Metrics: []globalcatalogv1.Metrics{
			{
				Amounts: []globalcatalogv1.Amount{
					{
						Country: &country,
						Prices: []globalcatalogv1.Price{
							{Price: &price},
						},
					},
				},
			},
		},
	}
}
