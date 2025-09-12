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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// pollUntil polls a condition until it's true or timeout
func pollUntil(t *testing.T, condition func() bool, timeout time.Duration, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond) // Small polling interval
	}
	t.Fatal(message)
}

func TestNewCache(t *testing.T) {
	ttl := 5 * time.Minute
	cache := New(ttl)

	assert.NotNil(t, cache)
	assert.Equal(t, ttl, cache.ttl)
	assert.NotNil(t, cache.items)
	assert.NotNil(t, cache.stopChan)

	// Cleanup
	cache.Stop()
}

func TestCacheSetAndGet(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	// Test setting and getting a value
	key := "test-key"
	value := "test-value"

	cache.Set(key, value)

	retrieved, exists := cache.Get(key)
	assert.True(t, exists)
	assert.Equal(t, value, retrieved)
}

func TestCacheGetNonexistent(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	retrieved, exists := cache.Get("nonexistent")
	assert.False(t, exists)
	assert.Nil(t, retrieved)
}

func TestCacheSetWithTTL(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	key := "test-key"
	value := "test-value"
	ttl := 100 * time.Millisecond

	cache.SetWithTTL(key, value, ttl)

	// Should exist immediately
	retrieved, exists := cache.Get(key)
	assert.True(t, exists)
	assert.Equal(t, value, retrieved)

	// Wait for expiration using polling
	pollUntil(t, func() bool {
		_, expired := cache.Get(key)
		return !expired
	}, 500*time.Millisecond, "cache entry should have expired")

	// Should be expired
	retrieved, exists = cache.Get(key)
	assert.False(t, exists)
	assert.Nil(t, retrieved)
}

func TestCacheHas(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	key := "test-key"
	value := "test-value"

	// Should not exist initially
	assert.False(t, cache.Has(key))

	// Set the value
	cache.Set(key, value)

	// Should exist now
	assert.True(t, cache.Has(key))
}

func TestCacheDelete(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	key := "test-key"
	value := "test-value"

	cache.Set(key, value)
	assert.True(t, cache.Has(key))

	cache.Delete(key)
	assert.False(t, cache.Has(key))
}

func TestCacheSize(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	assert.Equal(t, 0, cache.Size())

	cache.Set("key1", "value1")
	assert.Equal(t, 1, cache.Size())

	cache.Set("key2", "value2")
	assert.Equal(t, 2, cache.Size())

	cache.Delete("key1")
	assert.Equal(t, 1, cache.Size())
}

func TestCacheClear(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	cache.Set("key1", "value1")
	cache.Set("key2", "value2")
	assert.Equal(t, 2, cache.Size())

	cache.Clear()
	assert.Equal(t, 0, cache.Size())
	assert.False(t, cache.Has("key1"))
	assert.False(t, cache.Has("key2"))
}

func TestCacheGetOrSet(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	key := "test-key"
	expectedValue := "computed-value"
	callCount := 0

	fetchFunc := func() (interface{}, error) {
		callCount++
		return expectedValue, nil
	}

	// First call should fetch the value
	value, err := cache.GetOrSet(key, fetchFunc)
	assert.NoError(t, err)
	assert.Equal(t, expectedValue, value)
	assert.Equal(t, 1, callCount)

	// Second call should return cached value
	value, err = cache.GetOrSet(key, fetchFunc)
	assert.NoError(t, err)
	assert.Equal(t, expectedValue, value)
	assert.Equal(t, 1, callCount) // Should not have called fetchFunc again
}

func TestCacheGetOrSetWithError(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	key := "test-key"
	expectedError := errors.New("fetch failed")

	fetchFunc := func() (interface{}, error) {
		return nil, expectedError
	}

	value, err := cache.GetOrSet(key, fetchFunc)
	assert.Error(t, err)
	assert.Equal(t, expectedError, err)
	assert.Nil(t, value)

	// Should not have cached the error
	assert.False(t, cache.Has(key))
}

func TestCacheGetOrSetWithTTL(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	key := "test-key"
	expectedValue := "computed-value"
	ttl := 100 * time.Millisecond
	callCount := 0

	fetchFunc := func() (interface{}, error) {
		callCount++
		return expectedValue, nil
	}

	// First call should fetch the value
	value, err := cache.GetOrSetWithTTL(key, ttl, fetchFunc)
	assert.NoError(t, err)
	assert.Equal(t, expectedValue, value)
	assert.Equal(t, 1, callCount)

	// Wait for expiration using polling
	pollUntil(t, func() bool {
		// Try to get the value - if it doesn't exist, it means it expired
		_, expired := cache.Get(key)
		return !expired
	}, 500*time.Millisecond, "cache entry should have expired")

	// Should fetch again after expiration
	value, err = cache.GetOrSetWithTTL(key, ttl, fetchFunc)
	assert.NoError(t, err)
	assert.Equal(t, expectedValue, value)
	assert.Equal(t, 2, callCount)
}

func TestCacheExpiration(t *testing.T) {
	cache := New(50 * time.Millisecond) // Very short TTL
	defer cache.Stop()

	key := "test-key"
	value := "test-value"

	cache.Set(key, value)
	assert.True(t, cache.Has(key))

	// Wait for expiration using polling
	pollUntil(t, func() bool {
		_, expired := cache.Get(key)
		return !expired
	}, 500*time.Millisecond, "cache entry should have expired")

	// Get should remove expired item and return false
	retrieved, exists := cache.Get(key)
	assert.False(t, exists)
	assert.Nil(t, retrieved)
}

func TestCacheCleanup(t *testing.T) {
	cache := New(50 * time.Millisecond) // Very short TTL for fast cleanup
	defer cache.Stop()

	key := "test-key"
	value := "test-value"

	cache.Set(key, value)
	assert.Equal(t, 1, cache.Size())

	// Wait for cleanup to run using polling (cleanup runs every TTL/2)
	pollUntil(t, func() bool {
		return cache.Size() == 0
	}, 500*time.Millisecond, "cache should be cleaned up")

	// Size should be 0 after cleanup removes expired items
	assert.Equal(t, 0, cache.Size())
}

func TestCacheStop(t *testing.T) {
	cache := New(5 * time.Minute)

	// Stop should not panic
	assert.NotPanics(t, func() {
		cache.Stop()
	})

	// Stopping again should not panic (tests the fix for double-close issue)
	assert.NotPanics(t, func() {
		cache.Stop()
	})
}

func TestCacheConcurrency(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	// Test concurrent reads and writes
	done := make(chan bool, 2)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.Set("key", i)
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.Get("key")
		}
		done <- true
	}()

	// Wait for both goroutines to complete
	<-done
	<-done

	// Should not panic or race
	assert.True(t, true)
}

func TestCacheEntry(t *testing.T) {
	now := time.Now()
	entry := &Entry{
		Value:      "test-value",
		Expiration: now.Add(5 * time.Minute),
	}

	assert.Equal(t, "test-value", entry.Value)
	assert.Equal(t, now.Add(5*time.Minute), entry.Expiration)
}

func TestCacheWithDifferentTypes(t *testing.T) {
	cache := New(5 * time.Minute)
	defer cache.Stop()

	// Test with different value types
	cache.Set("string", "value")
	cache.Set("int", 42)
	cache.Set("slice", []string{"a", "b", "c"})
	cache.Set("map", map[string]int{"key": 123})

	// Retrieve and verify types
	strVal, exists := cache.Get("string")
	assert.True(t, exists)
	assert.Equal(t, "value", strVal.(string))

	intVal, exists := cache.Get("int")
	assert.True(t, exists)
	assert.Equal(t, 42, intVal.(int))

	sliceVal, exists := cache.Get("slice")
	assert.True(t, exists)
	assert.Equal(t, []string{"a", "b", "c"}, sliceVal.([]string))

	mapVal, exists := cache.Get("map")
	assert.True(t, exists)
	assert.Equal(t, map[string]int{"key": 123}, mapVal.(map[string]int))
}
