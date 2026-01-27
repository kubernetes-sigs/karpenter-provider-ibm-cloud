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
	"time"

	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
	"github.com/mitchellh/hashstructure/v2"
)

type PricingQueryInput struct {
	CatalogEntryID string
}

type pricingClient interface {
	GetPricing(ctx context.Context, catalogEntryID string) (*globalcatalogv1.PricingGet, error)
}

type PricingBatcher struct {
	batcher *Batcher[PricingQueryInput, globalcatalogv1.PricingGet]
	client  pricingClient
}

func NewPricingBatcher(ctx context.Context, client pricingClient) *PricingBatcher {
	p := &PricingBatcher{client: client}

	opts := Options[PricingQueryInput, globalcatalogv1.PricingGet]{
		Name:        "get_global_catalog_pricing",
		IdleTimeout: 200 * time.Millisecond,
		MaxTimeout:  2 * time.Second,
		MaxItems:    200,

		// Group by catalogEntryID so identical pricing requests share one upstream call
		RequestHasher: PricingHasher,

		BatchExecutor: p.execPricingBatch(),
	}

	p.batcher = NewBatcher(ctx, opts)
	return p
}

func (p *PricingBatcher) GetPricing(ctx context.Context, catalogEntryID string) (*globalcatalogv1.PricingGet, error) {
	res := p.batcher.Add(ctx, &PricingQueryInput{CatalogEntryID: catalogEntryID})
	return res.Output, res.Err
}

func PricingHasher(_ context.Context, in *PricingQueryInput) (uint64, error) {
	if in == nil {
		return 0, nil
	}

	hash, err := hashstructure.Hash(in.CatalogEntryID, hashstructure.FormatV2, nil)
	if err != nil {
		return 0, err
	}

	return hash, nil
}

func (p *PricingBatcher) execPricingBatch() BatchExecutor[PricingQueryInput, globalcatalogv1.PricingGet] {
	return func(ctx context.Context, inputs []*PricingQueryInput) []Result[globalcatalogv1.PricingGet] {
		results := make([]Result[globalcatalogv1.PricingGet], len(inputs))
		if len(inputs) == 0 {
			return results
		}

		id := inputs[0].CatalogEntryID
		out, err := p.client.GetPricing(ctx, id)

		for i := range inputs {
			results[i] = Result[globalcatalogv1.PricingGet]{Output: out, Err: err}
		}
		return results
	}
}
