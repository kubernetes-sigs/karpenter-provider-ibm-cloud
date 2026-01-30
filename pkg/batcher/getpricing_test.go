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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/fake"
)

// newPricingBatcherWithOptions builds the batcher with tight timeouts for testing
func newPricingBatcherWithOptions(ctx context.Context, client pricingClient) *PricingBatcher {
	p := &PricingBatcher{client: client}

	opts := Options[string, globalcatalogv1.PricingGet]{
		Name:        "get_global_catalog_pricing",
		IdleTimeout: 5 * time.Millisecond,
		MaxTimeout:  50 * time.Millisecond,
		MaxItems:    200,

		RequestHasher: pricingHasher,
		BatchExecutor: p.execPricingBatch(),
	}

	p.batcher = NewBatcher(ctx, opts)
	return p
}

func TestPricingBatcher_BatchesSameID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakePricing := fake.NewPricingAPI()
	pb := newPricingBatcherWithOptions(ctx, fakePricing)

	const id = "catalog-entry-1"
	const n = 50

	var wg sync.WaitGroup
	wg.Add(n)

	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := pb.GetPricing(ctx, id)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	}

	if got := fakePricing.GetCallCount(); got != 1 {
		t.Fatalf("expected 1 upstream call, got %d", got)
	}
}

func TestPricingBatcher_SeparatesDifferentIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakePricing := fake.NewPricingAPI()
	pb := newPricingBatcherWithOptions(ctx, fakePricing)

	ids := []string{"a", "b", "c"}
	const perID = 20

	var wg sync.WaitGroup
	wg.Add(len(ids) * perID)

	for _, id := range ids {
		id := id
		for i := 0; i < perID; i++ {
			go func() {
				defer wg.Done()
				_, err := pb.GetPricing(ctx, id)
				if err != nil {
					t.Errorf("unexpected err for id %q: %v", id, err)
				}
			}()
		}
	}
	wg.Wait()

	// One upstream call per distinct hash bucket (per ID)
	if got := fakePricing.GetCallCount(); got != int64(len(ids)) {
		t.Fatalf("expected %d upstream calls, got %d", len(ids), got)
	}
}

func TestPricingBatcher_FansOutUpstreamError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakePricing := fake.NewPricingAPI()
	pb := newPricingBatcherWithOptions(ctx, fakePricing)

	const id = "err-id"
	fakePricing.ErrorByID[id] = errors.New("boom")

	const n = 30
	var wg sync.WaitGroup
	wg.Add(n)

	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			out, err := pb.GetPricing(ctx, id)
			if out != nil {
				t.Errorf("expected nil out, got %#v", out)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err == nil {
			t.Fatalf("expected error, got nil")
		}
	}

	if got := fakePricing.GetCallCount(); got != 1 {
		t.Fatalf("expected 1 upstream call, got %d", got)
	}
}

func TestPricingBatcher_HandlesMultipleIDsInSameBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fakePricing := fake.NewPricingAPI()
	fakePricing.PricingByID["id-a"] = fake.NewPricingGet(0.10)
	fakePricing.PricingByID["id-b"] = fake.NewPricingGet(0.20)

	p := &PricingBatcher{client: fakePricing}

	// Use a CONSTANT hasher to force everything into the same batch
	opts := Options[string, globalcatalogv1.PricingGet]{
		Name:        "test_pricing",
		IdleTimeout: 5 * time.Millisecond,
		MaxTimeout:  50 * time.Millisecond,
		MaxItems:    200,
		RequestHasher: func(_ context.Context, _ *string) (uint64, error) {
			return 0, nil
		},
		BatchExecutor: p.execPricingBatch(),
	}
	p.batcher = NewBatcher(ctx, opts)

	var wg sync.WaitGroup
	wg.Add(2)

	var resultA, resultB *globalcatalogv1.PricingGet
	go func() {
		defer wg.Done()
		resultA, _ = p.GetPricing(ctx, "id-a")
	}()
	go func() {
		defer wg.Done()
		resultB, _ = p.GetPricing(ctx, "id-b")
	}()
	wg.Wait()

	// Both were in SAME batch, but should still get correct results
	priceA := *resultA.Metrics[0].Amounts[0].Prices[0].Price
	priceB := *resultB.Metrics[0].Amounts[0].Prices[0].Price

	if priceA != 0.10 {
		t.Errorf("expected id-a price 0.10, got %v", priceA)
	}
	if priceB != 0.20 {
		t.Errorf("expected id-b price 0.20, got %v", priceB)
	}

	// Should have made 2 API calls (one per unique ID)
	if got := fakePricing.GetCallCount(); got != 2 {
		t.Errorf("expected 2 API calls, got %d", got)
	}
}
