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

// batcher implements a generic batching lib for API calls so that load can be reduced to external APIs that excessively throttle
package batcher

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"
	"golang.org/x/sync/errgroup"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/metrics"
)

// Options allows for configuration of the Batcher
type Options[T input, U output] struct {
	Name              string
	IdleTimeout       time.Duration
	MaxTimeout        time.Duration
	MaxItems          int
	MaxRequestWorkers int
	ExecutionTimeout  time.Duration
	RequestHasher     RequestHasher[T]
	BatchExecutor     BatchExecutor[T, U]
}

// Result is a container for the output and error of an execution
type Result[U output] struct {
	Output *U
	Err    error
}

type input = any
type output = any

// request is a batched request with the calling ctx, requestor, and hash to determine the batching bucket
type request[T input, U output] struct {
	ctx       context.Context
	hash      uint64
	input     *T
	requestor chan Result[U]
}

// Batcher is used to batch API calls with identical parameters into a single call
type Batcher[T input, U output] struct {
	ctx     context.Context
	options Options[T, U]

	mu       sync.Mutex
	requests map[uint64][]*request[T, U]

	// trigger to initiate the batcher
	trigger chan struct{}

	// requestWorkers is a group of concurrent workers that execute requests
	requestWorkers errgroup.Group
}

// BatchExecutor is a function that executes a slice of inputs against the batched API.
// inputs will be mutated
// The returned Result slice is expected to match the len of the input slice and be in the
// same order, if order matters for the batched API
type BatchExecutor[T input, U output] func(ctx context.Context, input []*T) []Result[U]

// RequestHasher is a function that hashes input to bucket inputs into distinct batches
type RequestHasher[T input] func(ctx context.Context, input *T) (uint64, error)

// NewBatcher creates a batcher that can batch a particular input and output type
func NewBatcher[T input, U output](ctx context.Context, options Options[T, U]) *Batcher[T, U] {
	b := &Batcher[T, U]{
		ctx:      ctx,
		options:  options,
		requests: map[uint64][]*request[T, U]{},
		// The trigger channel is buffered since we shouldn't block the Add() method on the trigger channel
		// if another Add() has already triggered it. This works because we add the request to the request map BEFORE
		// we perform the trigger
		trigger: make(chan struct{}, 1),
	}
	b.requestWorkers.SetLimit(lo.Ternary(b.options.MaxRequestWorkers != 0, b.options.MaxRequestWorkers, 100))
	go b.run()
	return b
}

// Add will add an input to the batcher using the batcher's hashing function
func (b *Batcher[T, U]) Add(ctx context.Context, input *T) Result[U] {
	if err := ctx.Err(); err != nil {
		return Result[U]{Err: err}
	}
	if err := b.ctx.Err(); err != nil {
		return Result[U]{Err: err}
	}
	hash, err := b.options.RequestHasher(ctx, input)
	if err != nil {
		return Result[U]{Err: fmt.Errorf("hashing request: %w", err)}
	}
	request := &request[T, U]{
		ctx:   ctx,
		hash:  hash,
		input: input,
		// The requestor channel is buffered to ensure that the exec runner can always write the result out preventing
		// any single caller from blocking the others. Specifically since we register our request and then trigger, the
		// request may be processed while the triggering blocks.
		requestor: make(chan Result[U], 1),
	}
	b.mu.Lock()
	b.requests[request.hash] = append(b.requests[request.hash], request)
	b.mu.Unlock()
	select {
	case b.trigger <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return Result[U]{Err: ctx.Err()}
	case <-b.ctx.Done():
		return Result[U]{Err: b.ctx.Err()}
	case result := <-request.requestor:
		return result
	}
}

// DefaultHasher will hash the entire input
func DefaultHasher[T input](_ context.Context, input *T) (uint64, error) {
	hash, err := hashstructure.Hash(input, hashstructure.FormatV2, &hashstructure.HashOptions{SlicesAsSets: true})
	if err != nil {
		return 0, err
	}
	return hash, nil
}

// OneBucketHasher will return a constant hash and should be used when there is only one type of request
func OneBucketHasher[T input](_ context.Context, _ *T) (uint64, error) {
	return 0, nil
}

func (b *Batcher[T, U]) run() {
	for {
		var batchStart time.Time
		select {
		// context that we started with has completed so the app is shutting down
		case <-b.ctx.Done():
			_ = b.requestWorkers.Wait()
			return
		case <-b.trigger:
			// wait to start the batch of create fleet calls
			batchStart = time.Now()
		}
		b.waitForIdle()
		// Observe the length of time between the start of the batch and now
		if !batchStart.IsZero() {
			metrics.BatcherBatchWindowDuration.
				WithLabelValues(b.options.Name).
				Observe(time.Since(batchStart).Seconds())
		}

		// Copy the requests, so we can reset the requests for the next batching loop
		b.mu.Lock()
		requests := b.requests
		b.requests = map[uint64][]*request[T, U]{}
		b.mu.Unlock()

		for _, v := range requests {
			req := v // create a local closure for the requests value
			b.requestWorkers.Go(func() error {
				b.runCalls(req)
				return nil
			})
		}
	}
}

func (b *Batcher[T, U]) waitForIdle() {
	timeout := time.NewTimer(b.options.MaxTimeout)
	idle := time.NewTimer(b.options.IdleTimeout)
	defer func() {
		timeout.Stop()
		idle.Stop()
	}()
	for {
		if b.options.MaxItems > 0 {
			b.mu.Lock()
			count := 0
			for _, requests := range b.requests {
				count += len(requests)
			}
			b.mu.Unlock()
			if count >= b.options.MaxItems {
				return
			}
		}
		select {
		case <-b.ctx.Done():
			return
		case <-b.trigger:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(b.options.IdleTimeout)
		case <-timeout.C:
			return
		case <-idle.C:
			return
		}
	}
}

func (b *Batcher[T, U]) runCalls(requests []*request[T, U]) {
	// Measure the size of the request batch
	metrics.BatcherBatchSize.
		WithLabelValues(b.options.Name).
		Observe(float64(len(requests)))
	active := make([]*request[T, U], 0, len(requests))
	for _, req := range requests {
		if err := req.ctx.Err(); err != nil {
			req.requestor <- Result[U]{Err: err}
		} else {
			active = append(active, req)
		}
	}
	if len(active) == 0 {
		return
	}
	timeout := b.options.ExecutionTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	execCtx, cancel := context.WithTimeout(b.ctx, timeout)
	defer cancel()
	outputs := b.options.BatchExecutor(execCtx, lo.Map(active, func(req *request[T, U], _ int) *T { return req.input }))
	for i, req := range active {
		if i < len(outputs) {
			req.requestor <- outputs[i]
		} else {
			req.requestor <- Result[U]{Err: fmt.Errorf("batch executor returned fewer results than requests: got %d, expected %d", len(outputs), len(active))}
		}
	}
}
