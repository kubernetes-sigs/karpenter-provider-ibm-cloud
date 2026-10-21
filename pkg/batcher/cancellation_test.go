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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCanceledWaiterDoesNotCancelSharedBatch(t *testing.T) {
	lifecycle, stop := context.WithCancel(context.Background())
	defer stop()
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	b := NewBatcher(lifecycle, Options[int, int]{Name: "cancel-test", IdleTimeout: 20 * time.Millisecond, MaxTimeout: time.Second, RequestHasher: OneBucketHasher[int], BatchExecutor: func(ctx context.Context, inputs []*int) []Result[int] {
		started <- ctx
		<-release
		results := make([]Result[int], len(inputs))
		for i, input := range inputs {
			results[i] = Result[int]{Output: input}
		}
		return results
	}})
	first, second := make(chan Result[int], 1), make(chan Result[int], 1)
	one, two := 1, 2
	go func() { first <- b.Add(caller, &one) }()
	go func() { second <- b.Add(context.Background(), &two) }()
	select {
	case ctx := <-started:
		cancel()
		select {
		case result := <-first:
			require.ErrorIs(t, result.Err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("canceled waiter did not return")
		}
		require.NoError(t, ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("batch did not start")
	}
	close(release)
	select {
	case result := <-second:
		require.NoError(t, result.Err)
		require.Equal(t, 2, *result.Output)
	case <-time.After(time.Second):
		t.Fatal("surviving waiter did not receive result")
	}
}

func TestAddAfterShutdownReturnsWithoutTrigger(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := NewBatcher(ctx, Options[int, int]{Name: "stopped", RequestHasher: OneBucketHasher[int]})
	value := 1
	require.ErrorIs(t, b.Add(context.Background(), &value).Err, context.Canceled)
}
