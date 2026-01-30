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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Simple input/output types for testing
type testInput struct {
	ID    int
	Value string
}

type testOutput struct {
	Result string
}

// Test timeout to prevent CI hangs
const testTimeout = 5 * time.Second

// Helper to receive result with timeout
func receiveWithTimeout[U any](t *testing.T, ch <-chan Result[U], timeout time.Duration) Result[U] {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case result := <-ch:
		return result
	case <-timer.C:
		t.Fatal("timed out waiting for result")
		return Result[U]{}
	}
}

// Default executor used by many tests
func successExecutor(_ context.Context, inputs []*testInput) []Result[testOutput] {
	results := make([]Result[testOutput], len(inputs))
	for i := range inputs {
		results[i] = Result[testOutput]{Output: &testOutput{Result: "success"}}
	}
	return results
}

// newBatcher creates a batcher with defaults. Tests override what they need.
func newBatcher(ctx context.Context, overrides ...func(*Options[testInput, testOutput])) *Batcher[testInput, testOutput] {
	opts := Options[testInput, testOutput]{
		Name:          "test-batcher",
		IdleTimeout:   10 * time.Millisecond,
		MaxTimeout:    100 * time.Millisecond,
		RequestHasher: OneBucketHasher[testInput],
		BatchExecutor: successExecutor,
	}
	for _, o := range overrides {
		o(&opts)
	}
	return NewBatcher[testInput, testOutput](ctx, opts)
}

func TestAdd_SingleRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := newBatcher(ctx)

	done := make(chan Result[testOutput], 1)
	go func() {
		input := &testInput{ID: 1, Value: "test"}
		done <- b.Add(context.Background(), input)
	}()

	result := receiveWithTimeout(t, done, testTimeout)
	assert.NoError(t, result.Err)
	require.NotNil(t, result.Output)
	assert.Equal(t, "success", result.Output.Result)
}

func TestAdd_BatchesSameHash_Deterministic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numRequests = 3
	var executorCalls atomic.Int32
	var batchSize atomic.Int32

	b := newBatcher(ctx, func(o *Options[testInput, testOutput]) {
		o.IdleTimeout = 1 * time.Second
		o.MaxTimeout = 5 * time.Second
		o.MaxItems = numRequests
		o.BatchExecutor = func(ctx context.Context, inputs []*testInput) []Result[testOutput] {
			executorCalls.Add(1)
			batchSize.Store(int32(len(inputs)))
			return successExecutor(ctx, inputs)
		}
	})

	var ready sync.WaitGroup
	var start sync.WaitGroup
	ready.Add(numRequests)
	start.Add(1)

	results := make(chan Result[testOutput], numRequests)
	for i := 0; i < numRequests; i++ {
		go func(id int) {
			ready.Done()
			start.Wait()
			input := &testInput{ID: id, Value: "test"}
			results <- b.Add(context.Background(), input)
		}(i)
	}

	ready.Wait()
	start.Done()

	for i := 0; i < numRequests; i++ {
		result := receiveWithTimeout(t, results, testTimeout)
		assert.NoError(t, result.Err)
	}

	assert.Equal(t, int32(1), executorCalls.Load())
	assert.Equal(t, int32(numRequests), batchSize.Load())
}

func TestAdd_MultipleBuckets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	bucketSizes := make(map[uint64]int)
	var executorCalls atomic.Int32

	twoWayHasher := func(_ context.Context, input *testInput) (uint64, error) {
		return uint64(input.ID % 2), nil
	}

	const numRequests = 6

	b := newBatcher(ctx, func(o *Options[testInput, testOutput]) {
		o.IdleTimeout = 1 * time.Second
		o.MaxTimeout = 5 * time.Second
		o.MaxItems = numRequests // Total requests across all buckets
		o.RequestHasher = twoWayHasher
		o.BatchExecutor = func(ctx context.Context, inputs []*testInput) []Result[testOutput] {
			executorCalls.Add(1)
			hash, _ := twoWayHasher(ctx, inputs[0])

			mu.Lock()
			bucketSizes[hash] = len(inputs)
			mu.Unlock()

			return successExecutor(ctx, inputs)
		}
	})

	results := make(chan Result[testOutput], numRequests)

	var ready sync.WaitGroup
	var start sync.WaitGroup
	ready.Add(numRequests)
	start.Add(1)

	for i := 0; i < numRequests; i++ {
		go func(id int) {
			ready.Done()
			start.Wait()
			input := &testInput{ID: id, Value: "test"}
			results <- b.Add(context.Background(), input)
		}(i)
	}

	ready.Wait()
	start.Done()

	for i := 0; i < numRequests; i++ {
		result := receiveWithTimeout(t, results, testTimeout)
		assert.NoError(t, result.Err)
	}

	assert.Equal(t, int32(2), executorCalls.Load())

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 3, bucketSizes[0], "bucket 0 should have 3 items (IDs 0,2,4)")
	assert.Equal(t, 3, bucketSizes[1], "bucket 1 should have 3 items (IDs 1,3,5)")
}

func TestFlush_IdleTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	executorCalled := make(chan struct{}, 1)

	b := newBatcher(ctx, func(o *Options[testInput, testOutput]) {
		o.IdleTimeout = 20 * time.Millisecond
		o.MaxTimeout = 5 * time.Second
		o.MaxItems = 100
		o.BatchExecutor = func(ctx context.Context, inputs []*testInput) []Result[testOutput] {
			executorCalled <- struct{}{}
			return successExecutor(ctx, inputs)
		}
	})

	done := make(chan Result[testOutput], 1)
	go func() {
		input := &testInput{ID: 1, Value: "test"}
		done <- b.Add(context.Background(), input)
	}()

	select {
	case <-executorCalled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("executor not called within expected idle timeout window")
	}

	result := receiveWithTimeout(t, done, testTimeout)
	assert.NoError(t, result.Err)
}

func TestFlush_MaxTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	executorCalled := make(chan struct{}, 1) // Simple signal, no timestamp

	b := newBatcher(ctx, func(o *Options[testInput, testOutput]) {
		o.IdleTimeout = 5 * time.Second      // Long - won't trigger
		o.MaxTimeout = 50 * time.Millisecond // Short - will trigger
		o.MaxItems = 100
		o.BatchExecutor = func(ctx context.Context, inputs []*testInput) []Result[testOutput] {
			executorCalled <- struct{}{}
			return successExecutor(ctx, inputs)
		}
	})

	done := make(chan Result[testOutput], 1)
	go func() {
		input := &testInput{ID: 1, Value: "test"}
		done <- b.Add(context.Background(), input)
	}()

	// If executor is called within 500ms, MaxTimeout worked
	// (IdleTimeout is 5s, so if it was IdleTimeout, this would timeout)
	select {
	case <-executorCalled:
		// Success
	case <-time.After(500 * time.Millisecond):
		t.Fatal("executor not called - MaxTimeout didn't trigger")
	}

	result := receiveWithTimeout(t, done, testTimeout)
	assert.NoError(t, result.Err)
}

func TestFlush_MaxItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const maxItems = 3
	executorCalled := make(chan struct{}, 1) // Simple signal

	b := newBatcher(ctx, func(o *Options[testInput, testOutput]) {
		o.IdleTimeout = 5 * time.Second
		o.MaxTimeout = 5 * time.Second
		o.MaxItems = maxItems
		o.BatchExecutor = func(ctx context.Context, inputs []*testInput) []Result[testOutput] {
			executorCalled <- struct{}{}
			return successExecutor(ctx, inputs)
		}
	})

	// Barrier pattern - still needed for determinism
	var ready sync.WaitGroup
	var start sync.WaitGroup
	ready.Add(maxItems)
	start.Add(1)

	results := make(chan Result[testOutput], maxItems)
	for i := 0; i < maxItems; i++ {
		go func(id int) {
			ready.Done()
			start.Wait()
			input := &testInput{ID: id, Value: "test"}
			results <- b.Add(context.Background(), input)
		}(i)
	}

	ready.Wait()
	start.Done()

	// If executor called within 1s, MaxItems triggered (not 5s timeouts)
	select {
	case <-executorCalled:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("executor not called - MaxItems didn't trigger flush")
	}

	for i := 0; i < maxItems; i++ {
		result := receiveWithTimeout(t, results, testTimeout)
		assert.NoError(t, result.Err)
	}
}

func TestAdd_HasherError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedErr := errors.New("hash failed")
	b := newBatcher(ctx, func(o *Options[testInput, testOutput]) {
		o.RequestHasher = func(_ context.Context, _ *testInput) (uint64, error) {
			return 0, expectedErr
		}
		o.BatchExecutor = func(ctx context.Context, inputs []*testInput) []Result[testOutput] {
			t.Fatal("executor should not be called when hasher fails")
			return nil
		}
	})

	input := &testInput{ID: 1, Value: "test"}
	result := b.Add(context.Background(), input)

	assert.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "hashing request")
	assert.ErrorIs(t, result.Err, expectedErr)
}

func TestAdd_ExecutorReturnsFewerResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const numRequests = 3

	b := newBatcher(ctx, func(o *Options[testInput, testOutput]) {
		o.IdleTimeout = 1 * time.Second
		o.MaxTimeout = 5 * time.Second
		o.MaxItems = numRequests
		o.BatchExecutor = func(ctx context.Context, inputs []*testInput) []Result[testOutput] {
			return []Result[testOutput]{{Output: &testOutput{Result: "success"}}}
		}
	})

	var ready sync.WaitGroup
	var start sync.WaitGroup
	ready.Add(numRequests)
	start.Add(1)

	results := make(chan Result[testOutput], numRequests)
	for i := 0; i < numRequests; i++ {
		go func(idx int) {
			ready.Done()
			start.Wait()
			input := &testInput{ID: idx, Value: "test"}
			results <- b.Add(context.Background(), input)
		}(i)
	}

	ready.Wait()
	start.Done()

	var successCount, errorCount int
	for i := 0; i < numRequests; i++ {
		result := receiveWithTimeout(t, results, testTimeout)
		if result.Err != nil {
			errorCount++
		} else {
			successCount++
		}
	}

	assert.Equal(t, 1, successCount, "first request should succeed")
	assert.Equal(t, numRequests-1, errorCount, "remaining requests should get error")
}

func TestDefaultHasher(t *testing.T) {
	input1 := &testInput{ID: 1, Value: "test"}
	input2 := &testInput{ID: 1, Value: "test"}
	input3 := &testInput{ID: 2, Value: "different"}

	hash1, err1 := DefaultHasher(context.Background(), input1)
	hash2, err2 := DefaultHasher(context.Background(), input2)
	hash3, err3 := DefaultHasher(context.Background(), input3)

	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.NoError(t, err3)

	assert.Equal(t, hash1, hash2)
	assert.NotEqual(t, hash1, hash3)
}

func TestOneBucketHasher(t *testing.T) {
	input1 := &testInput{ID: 1, Value: "test"}
	input2 := &testInput{ID: 999, Value: "completely different"}

	hash1, err1 := OneBucketHasher(context.Background(), input1)
	hash2, err2 := OneBucketHasher(context.Background(), input2)

	assert.NoError(t, err1)
	assert.NoError(t, err2)
	assert.Equal(t, uint64(0), hash1)
	assert.Equal(t, uint64(0), hash2)
}

func TestContextCancellation_GracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	executorStarted := make(chan struct{})
	executorDone := make(chan struct{})

	b := newBatcher(ctx, func(o *Options[testInput, testOutput]) {
		o.BatchExecutor = func(ctx context.Context, inputs []*testInput) []Result[testOutput] {
			close(executorStarted)
			time.Sleep(10 * time.Millisecond)
			close(executorDone)
			return successExecutor(ctx, inputs)
		}
	})

	done := make(chan Result[testOutput], 1)
	go func() {
		input := &testInput{ID: 1, Value: "test"}
		done <- b.Add(context.Background(), input)
	}()

	select {
	case <-executorStarted:
	case <-time.After(testTimeout):
		t.Fatal("executor never started")
	}

	cancel()

	select {
	case <-executorDone:
	case <-time.After(testTimeout):
		t.Fatal("executor didn't complete after context cancellation")
	}

	result := receiveWithTimeout(t, done, testTimeout)
	assert.NoError(t, result.Err)
}
