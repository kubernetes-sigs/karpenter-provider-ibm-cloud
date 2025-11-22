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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestCacheLockUpgradeLogic specifically tests the lock upgrade pattern in Get()
func TestCacheLockUpgradeLogic(t *testing.T) {
	cache := New(50 * time.Millisecond) // Short TTL to trigger expiration
	defer cache.Stop()

	key := "test-key"
	value := "test-value"

	// Set a value that will expire soon
	cache.Set(key, value)

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// This should trigger the lock upgrade path
	result, exists := cache.Get(key)
	assert.False(t, exists)
	assert.Nil(t, result)

	// Verify the expired entry was cleaned up
	cache.mu.RLock()
	_, stillExists := cache.items[key]
	cache.mu.RUnlock()
	assert.False(t, stillExists, "Expired entry should be removed by lock upgrade logic")
}

// TestUnavailableOfferingsLockUpgradeLogic tests the lock upgrade in IsUnavailable()
func TestUnavailableOfferingsLockUpgradeLogic(t *testing.T) {
	unavailable := NewUnavailableOfferings()

	offeringID := "test-offering"
	// Add an offering that will expire soon
	unavailable.Add(offeringID, time.Now().Add(50*time.Millisecond))

	// Wait for expiration
	time.Sleep(100 * time.Millisecond)

	// This should trigger the lock upgrade path
	result := unavailable.IsUnavailable(offeringID)
	assert.False(t, result)

	// Verify the expired entry was cleaned up
	unavailable.mu.RLock()
	_, stillExists := unavailable.offerings[offeringID]
	unavailable.mu.RUnlock()
	assert.False(t, stillExists, "Expired offering should be removed by lock upgrade logic")
}

// TestConcurrentReadsDuringLockUpgrade tests that multiple readers can proceed while one upgrades locks
func TestConcurrentReadsDuringLockUpgrade(t *testing.T) {
	cache := New(100 * time.Millisecond)
	defer cache.Stop()

	// Add multiple entries, some will expire, some won't
	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			// These will expire
			cache.SetWithTTL(fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i), 50*time.Millisecond)
		} else {
			// These won't expire
			cache.SetWithTTL(fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i), 10*time.Minute)
		}
	}

	// Wait for some to expire
	time.Sleep(100 * time.Millisecond)

	var wg sync.WaitGroup
	var readersFinished int64
	const numReaders = 50

	// Start many concurrent readers
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			defer atomic.AddInt64(&readersFinished, 1)

			// Each reader tries to access different keys
			for j := 0; j < 10; j++ {
				key := fmt.Sprintf("key-%d", j)
				cache.Get(key) // Some will trigger lock upgrade, some won't
			}
		}(i)
	}

	wg.Wait()

	// Verify all readers completed
	assert.Equal(t, int64(numReaders), atomic.LoadInt64(&readersFinished))

	// Verify expired entries were cleaned up
	cache.mu.RLock()
	remainingItems := len(cache.items)
	cache.mu.RUnlock()

	// Should have approximately 5 items left (the non-expired ones)
	assert.LessOrEqual(t, remainingItems, 5, "Expired items should have been cleaned up")
}

// TestDoubleCheckPatternRaceCondition tests the double-check pattern handles race conditions
func TestDoubleCheckPatternRaceCondition(t *testing.T) {
	unavailable := NewUnavailableOfferings()
	offeringID := "race-test-offering"

	// Add an offering that will expire very soon
	expiry := time.Now().Add(10 * time.Millisecond)
	unavailable.Add(offeringID, expiry)

	var wg sync.WaitGroup
	results := make([]bool, 10)

	// Start multiple goroutines that will hit the race condition window
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			time.Sleep(15 * time.Millisecond) // Wait for expiry
			results[idx] = unavailable.IsUnavailable(offeringID)
		}(i)
	}

	wg.Wait()

	// All should return false (offering expired)
	for i, result := range results {
		assert.False(t, result, "Result %d should be false for expired offering", i)
	}

	// Entry should be cleaned up exactly once
	unavailable.mu.RLock()
	_, exists := unavailable.offerings[offeringID]
	unavailable.mu.RUnlock()
	assert.False(t, exists, "Expired offering should be cleaned up")
}

// TestReadPerformanceNotDegradedByLockUpgrade verifies read performance for non-expired entries
func TestReadPerformanceNotDegradedByLockUpgrade(t *testing.T) {
	cache := New(10 * time.Minute) // Long TTL, no expiration
	defer cache.Stop()

	// Add entries that won't expire
	for i := 0; i < 100; i++ {
		cache.Set(fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i))
	}

	// Time multiple reads - these should be fast (read lock only)
	start := time.Now()
	const numReads = 1000

	for i := 0; i < numReads; i++ {
		cache.Get(fmt.Sprintf("key-%d", i%100))
	}

	duration := time.Since(start)

	// Should be very fast - less than 10ms for 1000 reads
	assert.Less(t, duration, 10*time.Millisecond, "Read performance should not be degraded")
}

// TestLockUpgradeDoesNotDeadlock verifies no deadlocks occur during lock upgrade
func TestLockUpgradeDoesNotDeadlock(t *testing.T) {
	cache := New(50 * time.Millisecond)
	defer cache.Stop()

	// Add entries with varying expiration times
	for i := 0; i < 20; i++ {
		ttl := time.Duration(i*10) * time.Millisecond
		cache.SetWithTTL(fmt.Sprintf("key-%d", i), fmt.Sprintf("value-%d", i), ttl)
	}

	var wg sync.WaitGroup
	timeout := time.After(5 * time.Second) // Test timeout
	done := make(chan struct{})

	// Start multiple goroutines doing mixed operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := fmt.Sprintf("key-%d", j%20)

				// Mix of operations that could trigger lock upgrades
				cache.Get(key)
				cache.Set(key, fmt.Sprintf("new-value-%d-%d", id, j))
				cache.Has(key)
				time.Sleep(time.Millisecond) // Small delay to allow expiration
			}
		}(i)
	}

	// Wait for completion or timeout
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Test completed successfully
	case <-timeout:
		t.Fatal("Test timed out - possible deadlock in lock upgrade logic")
	}
}

// TestExpiredEntryRemovalConsistency ensures expired entries are consistently removed
func TestExpiredEntryRemovalConsistency(t *testing.T) {
	unavailable := NewUnavailableOfferings()

	// Add offerings with staggered expiration times
	baseTime := time.Now()
	for i := 0; i < 10; i++ {
		offeringID := fmt.Sprintf("offering-%d", i)
		expiry := baseTime.Add(time.Duration(i*10) * time.Millisecond)
		unavailable.Add(offeringID, expiry)
	}

	// Wait for all to expire
	time.Sleep(150 * time.Millisecond)

	// Check each offering - should trigger cleanup and return false
	for i := 0; i < 10; i++ {
		offeringID := fmt.Sprintf("offering-%d", i)
		result := unavailable.IsUnavailable(offeringID)
		assert.False(t, result, "Offering %s should be expired", offeringID)
	}

	// Verify all expired entries were removed
	unavailable.mu.RLock()
	remainingCount := len(unavailable.offerings)
	unavailable.mu.RUnlock()

	assert.Equal(t, 0, remainingCount, "All expired offerings should be removed")
}
