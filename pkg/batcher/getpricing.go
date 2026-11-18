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

package batcher

import (
	"context"
	"fmt"
	"time"

	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
	"golang.org/x/sync/errgroup"
)

type pricingClient interface {
	GetPricing(ctx context.Context, catalogEntryID string, region string) (*globalcatalogv1.PricingGet, error)
}

type PricingBatcher struct {
	batcher *Batcher[string, globalcatalogv1.PricingGet]
	client  pricingClient
	region  string
}

func NewPricingBatcher(ctx context.Context, client pricingClient, region string) *PricingBatcher {
	p := &PricingBatcher{client: client, region: region}
	opts := Options[string, globalcatalogv1.PricingGet]{
		Name:          "get_global_catalog_pricing",
		IdleTimeout:   200 * time.Millisecond,
		MaxTimeout:    2 * time.Second,
		MaxItems:      200,
		RequestHasher: pricingHasher,
		BatchExecutor: p.execPricingBatch(),
	}
	p.batcher = NewBatcher(ctx, opts)
	return p
}

func (p *PricingBatcher) GetPricing(ctx context.Context, catalogEntryID string) (*globalcatalogv1.PricingGet, error) {
	res := p.batcher.Add(ctx, &catalogEntryID)
	return res.Output, res.Err
}

func pricingHasher(_ context.Context, _ *string) (uint64, error) { return 0, nil }

func (p *PricingBatcher) execPricingBatch() BatchExecutor[string, globalcatalogv1.PricingGet] {
	return func(ctx context.Context, inputs []*string) []Result[globalcatalogv1.PricingGet] {
		results := make([]Result[globalcatalogv1.PricingGet], len(inputs))
		if len(inputs) == 0 {
			return results
		}
		groups := make(map[string][]int)
		for i, id := range inputs {
			if id == nil {
				results[i] = Result[globalcatalogv1.PricingGet]{
					Err: fmt.Errorf("nil catalog entry ID provided"),
				}
				continue
			}
			groups[*id] = append(groups[*id], i)
		}
		var calls errgroup.Group
		calls.SetLimit(8)
		for catalogEntryID, indices := range groups {
			calls.Go(func() error {
				out, err := p.client.GetPricing(ctx, catalogEntryID, p.region)
				for _, i := range indices {
					results[i] = Result[globalcatalogv1.PricingGet]{Output: out, Err: err}
				}
				return nil
			})
		}
		_ = calls.Wait()

		return results
	}
}
