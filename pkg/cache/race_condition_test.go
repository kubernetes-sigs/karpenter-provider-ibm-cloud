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
package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestCacheRaceCondition tests that concurrent access to the cache doesn't cause data races
func TestCacheRaceCondition(t *testing.T) {
	cache := New(100 * time.Millisecond) // Short TTL for testing
	defer cache.Stop()

	const numGoroutines = 100
	const numOperations = 100

	var wg sync.WaitGroup

	// Start multiple goroutines that read and write to the cache concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				key := "test-key"

				// Set a value
				cache.Set(key, id)

				// Get the value (this might trigger expiration cleanup)
				cache.Get(key)

				// Delete the value
				cache.Delete(key)
			}
		}(i)
	}

	wg.Wait()
}

// TestUnavailableOfferingsRaceCondition tests concurrent access to UnavailableOfferings
func TestUnavailableOfferingsRaceCondition(t *testing.T) {
	unavailable := NewUnavailableOfferings()

	const numGoroutines = 100
	const numOperations = 100

	var wg sync.WaitGroup

	// Start multiple goroutines that add/check/remove offerings concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				offeringID := "offering-test"

				// Add offering with short expiry
				unavailable.Add(offeringID, time.Now().Add(10*time.Millisecond))

				// Check if unavailable (this might trigger expiration cleanup)
				unavailable.IsUnavailable(offeringID)

				// Remove offering
				unavailable.Remove(offeringID)
			}
		}(i)
	}

	wg.Wait()
}

// TestCacheStopRaceCondition tests that calling Stop() multiple times doesn't panic
func TestCacheStopRaceCondition(t *testing.T) {
	cache := New(time.Minute)

	const numGoroutines = 50

	var wg sync.WaitGroup

	// Start multiple goroutines that all try to stop the cache
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.Stop() // This should not panic even when called concurrently
		}()
	}

	wg.Wait()
}

// TestCacheExpirationConcurrency tests that expiration cleanup works correctly under concurrent load
func TestCacheExpirationConcurrency(t *testing.T) {
	cache := New(50 * time.Millisecond) // Short TTL
	defer cache.Stop()

	const numGoroutines = 20
	const numKeys = 100

	var wg sync.WaitGroup

	// Writer goroutines - continuously add entries
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numKeys; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				cache.Set(key, fmt.Sprintf("value-%d-%d", id, j))
				time.Sleep(time.Millisecond) // Small delay to allow expiration
			}
		}(i)
	}

	// Reader goroutines - continuously read entries (triggering expiration cleanup)
	for i := 0; i < numGoroutines/2; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numKeys; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				cache.Get(key) // This might trigger expiration cleanup
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	wg.Wait()
}
