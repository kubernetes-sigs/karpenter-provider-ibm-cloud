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
	"sync/atomic"
	"testing"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/samber/lo"
)

func TestCreateInstanceBatcher_BatchesSameZone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var callCount int64
	c := &CreateInstanceBatcher{}

	opts := Options[CreateInstanceInput, vpcv1.Instance]{
		Name:          "create_instance",
		IdleTimeout:   5 * time.Millisecond,
		MaxTimeout:    50 * time.Millisecond,
		MaxItems:      100,
		RequestHasher: createInstanceHasher,
		BatchExecutor: func(ctx context.Context, inputs []*CreateInstanceInput) []Result[vpcv1.Instance] {
			atomic.AddInt64(&callCount, 1)
			results := make([]Result[vpcv1.Instance], len(inputs))
			for i := range inputs {
				results[i] = Result[vpcv1.Instance]{
					Output: &vpcv1.Instance{ID: lo.ToPtr("test-id")},
				}
			}
			return results
		},
	}
	c.batcher = NewBatcher(ctx, opts)

	const zone = "us-south-1"
	const n = 50

	var wg sync.WaitGroup
	wg.Add(n)

	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			prototype := &vpcv1.InstancePrototype{Name: lo.ToPtr("test")}
			_, err := c.CreateInstance(ctx, prototype, zone)
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

	// All same-zone requests should be batched together (1 batch execution)
	if got := atomic.LoadInt64(&callCount); got != 1 {
		t.Fatalf("expected 1 batch execution, got %d", got)
	}
}

func TestCreateInstanceBatcher_SeparatesDifferentZones(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var callCount int64
	c := &CreateInstanceBatcher{}

	opts := Options[CreateInstanceInput, vpcv1.Instance]{
		Name:          "create_instance",
		IdleTimeout:   5 * time.Millisecond,
		MaxTimeout:    50 * time.Millisecond,
		MaxItems:      100,
		RequestHasher: createInstanceHasher,
		BatchExecutor: func(ctx context.Context, inputs []*CreateInstanceInput) []Result[vpcv1.Instance] {
			atomic.AddInt64(&callCount, 1)
			results := make([]Result[vpcv1.Instance], len(inputs))
			for i := range inputs {
				results[i] = Result[vpcv1.Instance]{
					Output: &vpcv1.Instance{ID: lo.ToPtr("test-id")},
				}
			}
			return results
		},
	}
	c.batcher = NewBatcher(ctx, opts)

	zones := []string{"us-south-1", "us-south-2", "us-south-3"}
	const perZone = 20

	var wg sync.WaitGroup
	wg.Add(len(zones) * perZone)

	for _, zone := range zones {
		zone := zone
		for i := 0; i < perZone; i++ {
			go func() {
				defer wg.Done()
				prototype := &vpcv1.InstancePrototype{Name: lo.ToPtr("test")}
				_, err := c.CreateInstance(ctx, prototype, zone)
				if err != nil {
					t.Errorf("unexpected err for zone %q: %v", zone, err)
				}
			}()
		}
	}
	wg.Wait()

	// One batch execution per distinct zone
	if got := atomic.LoadInt64(&callCount); got != int64(len(zones)) {
		t.Fatalf("expected %d batch executions, got %d", len(zones), got)
	}
}

func TestCreateInstanceBatcher_FansOutError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testErr := errors.New("VPC client error")
	c := &CreateInstanceBatcher{}

	opts := Options[CreateInstanceInput, vpcv1.Instance]{
		Name:          "create_instance",
		IdleTimeout:   5 * time.Millisecond,
		MaxTimeout:    50 * time.Millisecond,
		MaxItems:      100,
		RequestHasher: createInstanceHasher,
		BatchExecutor: func(ctx context.Context, inputs []*CreateInstanceInput) []Result[vpcv1.Instance] {
			results := make([]Result[vpcv1.Instance], len(inputs))
			for i := range inputs {
				results[i] = Result[vpcv1.Instance]{Err: testErr}
			}
			return results
		},
	}
	c.batcher = NewBatcher(ctx, opts)

	const zone = "us-south-1"
	const n = 30

	var wg sync.WaitGroup
	wg.Add(n)

	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			prototype := &vpcv1.InstancePrototype{Name: lo.ToPtr("test")}
			_, err := c.CreateInstance(ctx, prototype, zone)
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
}

func TestCreateInstanceBatcher_NilPrototypeReturnsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := &CreateInstanceBatcher{}

	opts := Options[CreateInstanceInput, vpcv1.Instance]{
		Name:          "create_instance",
		IdleTimeout:   5 * time.Millisecond,
		MaxTimeout:    50 * time.Millisecond,
		MaxItems:      100,
		RequestHasher: createInstanceHasher,
		BatchExecutor: func(ctx context.Context, inputs []*CreateInstanceInput) []Result[vpcv1.Instance] {
			results := make([]Result[vpcv1.Instance], len(inputs))
			for i, req := range inputs {
				if req == nil || req.Prototype == nil {
					results[i] = Result[vpcv1.Instance]{Err: errors.New("nil instance prototype")}
					continue
				}
				results[i] = Result[vpcv1.Instance]{
					Output: &vpcv1.Instance{ID: lo.ToPtr("test-id")},
				}
			}
			return results
		},
	}
	c.batcher = NewBatcher(ctx, opts)

	_, err := c.CreateInstance(ctx, nil, "us-south-1")
	if err == nil {
		t.Fatal("expected error for nil prototype, got nil")
	}
}

func TestCreateInstanceHasher(t *testing.T) {
	ctx := context.Background()

	// Same zone should produce same hash
	hash1, _ := createInstanceHasher(ctx, &CreateInstanceInput{Zone: "us-south-1", Prototype: &vpcv1.InstancePrototype{Name: lo.ToPtr("a")}})
	hash2, _ := createInstanceHasher(ctx, &CreateInstanceInput{Zone: "us-south-1", Prototype: &vpcv1.InstancePrototype{Name: lo.ToPtr("b")}})
	if hash1 != hash2 {
		t.Errorf("same zone should produce same hash: %d != %d", hash1, hash2)
	}

	// Different zones should produce different hashes
	hash3, _ := createInstanceHasher(ctx, &CreateInstanceInput{Zone: "us-south-2", Prototype: &vpcv1.InstancePrototype{}})
	if hash1 == hash3 {
		t.Errorf("different zones should produce different hashes")
	}
}
