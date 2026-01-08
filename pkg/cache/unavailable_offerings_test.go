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

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewUnavailableOfferings(t *testing.T) {
	cache := NewUnavailableOfferings()

	assert.NotNil(t, cache)
	assert.NotNil(t, cache.offerings)
}

func TestUnavailableOfferings_Add(t *testing.T) {
	cache := NewUnavailableOfferings()

	tests := []struct {
		name       string
		offeringID string
		expiry     time.Time
	}{
		{
			name:       "add single offering",
			offeringID: "bx2-4x16:us-south-1:on-demand",
			expiry:     time.Now().Add(15 * time.Minute),
		},
		{
			name:       "add spot offering",
			offeringID: "cx2-8x16:us-south-2:spot",
			expiry:     time.Now().Add(10 * time.Minute),
		},
		{
			name:       "add with longer expiry",
			offeringID: "mx2-16x128:us-south-3:on-demand",
			expiry:     time.Now().Add(30 * time.Minute),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache.Add(tt.offeringID, tt.expiry)

			// Verify the offering was added
			cache.mu.RLock()
			expiry, exists := cache.offerings[tt.offeringID]
			cache.mu.RUnlock()

			assert.True(t, exists)
			assert.Equal(t, tt.expiry.Unix(), expiry.Unix())
		})
	}
}

func TestUnavailableOfferings_IsUnavailable(t *testing.T) {
	cache := NewUnavailableOfferings()

	// Add some offerings
	cache.Add("bx2-4x16:us-south-1:on-demand", time.Now().Add(15*time.Minute))
	cache.Add("cx2-8x16:us-south-2:spot", time.Now().Add(15*time.Minute))
	cache.Add("expired-offering", time.Now().Add(-1*time.Hour)) // Already expired

	tests := []struct {
		name       string
		offeringID string
		expected   bool
	}{
		{
			name:       "unavailable offering",
			offeringID: "bx2-4x16:us-south-1:on-demand",
			expected:   true,
		},
		{
			name:       "another unavailable offering",
			offeringID: "cx2-8x16:us-south-2:spot",
			expected:   true,
		},
		{
			name:       "non-existent offering",
			offeringID: "mx2-32x256:us-south-3:on-demand",
			expected:   false,
		},
		{
			name:       "expired offering",
			offeringID: "expired-offering",
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cache.IsUnavailable(tt.offeringID)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestUnavailableOfferings_Remove(t *testing.T) {
	cache := NewUnavailableOfferings()

	// Add offerings
	cache.Add("bx2-4x16:us-south-1:on-demand", time.Now().Add(15*time.Minute))
	cache.Add("cx2-8x16:us-south-2:spot", time.Now().Add(15*time.Minute))

	// Verify they're unavailable
	assert.True(t, cache.IsUnavailable("bx2-4x16:us-south-1:on-demand"))
	assert.True(t, cache.IsUnavailable("cx2-8x16:us-south-2:spot"))

	// Remove first offering
	cache.Remove("bx2-4x16:us-south-1:on-demand")
	assert.False(t, cache.IsUnavailable("bx2-4x16:us-south-1:on-demand"))
	assert.True(t, cache.IsUnavailable("cx2-8x16:us-south-2:spot"))

	// Remove non-existent offering (should not panic)
	cache.Remove("non-existent")

	// Remove second offering
	cache.Remove("cx2-8x16:us-south-2:spot")
	assert.False(t, cache.IsUnavailable("cx2-8x16:us-south-2:spot"))
}

func TestUnavailableOfferings_Cleanup(t *testing.T) {
	cache := NewUnavailableOfferings()

	// Add offerings with manual expiration control
	now := time.Now()

	cache.mu.Lock()
	// Expired entry
	cache.offerings["bx2-4x16:us-south-1:on-demand"] = now.Add(-1 * time.Hour)
	// Valid entry
	cache.offerings["cx2-8x16:us-south-2:spot"] = now.Add(1 * time.Hour)
	// Just expired entry
	cache.offerings["mx2-16x128:us-south-3:on-demand"] = now.Add(-1 * time.Second)
	cache.mu.Unlock()

	// Run cleanup
	cache.Cleanup()

	// Check results
	cache.mu.RLock()
	_, exists1 := cache.offerings["bx2-4x16:us-south-1:on-demand"]
	_, exists2 := cache.offerings["cx2-8x16:us-south-2:spot"]
	_, exists3 := cache.offerings["mx2-16x128:us-south-3:on-demand"]
	cache.mu.RUnlock()

	assert.False(t, exists1, "Expired entry should be removed")
	assert.True(t, exists2, "Valid entry should remain")
	assert.False(t, exists3, "Just expired entry should be removed")
}

func TestUnavailableOfferings_ConcurrentAccess(t *testing.T) {
	cache := NewUnavailableOfferings()

	// Number of goroutines
	numGoroutines := 10
	numOperations := 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines * 3) // 3 types of operations

	// Concurrent adds
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				offeringID := fmt.Sprintf("type-%d-%d:us-south-1:on-demand", id, j)
				cache.Add(offeringID, time.Now().Add(15*time.Minute))
			}
		}(i)
	}

	// Concurrent checks
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				offeringID := fmt.Sprintf("type-%d-%d:us-south-1:on-demand", id, j)
				_ = cache.IsUnavailable(offeringID)
			}
		}(i)
	}

	// Concurrent removes
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				offeringID := fmt.Sprintf("type-%d-%d:us-south-1:on-demand", id, j)
				cache.Remove(offeringID)
			}
		}(i)
	}

	// Wait for all operations to complete
	wg.Wait()

	// Run cleanup to ensure no deadlock
	cache.Cleanup()
}

func TestUnavailableOfferings_TTL(t *testing.T) {
	cache := NewUnavailableOfferings()

	// Add an offering with specific TTL
	ttl := 15 * time.Minute
	expiry := time.Now().Add(ttl)
	cache.Add("bx2-4x16:us-south-1:on-demand", expiry)

	// Check it's unavailable
	assert.True(t, cache.IsUnavailable("bx2-4x16:us-south-1:on-demand"))

	// Get the expiration time
	cache.mu.RLock()
	actualExpiry, exists := cache.offerings["bx2-4x16:us-south-1:on-demand"]
	cache.mu.RUnlock()

	require.True(t, exists)

	// Verify TTL is set correctly
	assert.Equal(t, expiry.Unix(), actualExpiry.Unix())
}

func TestUnavailableOfferings_ExpiryDuringIsUnavailable(t *testing.T) {
	cache := NewUnavailableOfferings()

	// Add an offering that's already expired
	cache.Add("expired-offering", time.Now().Add(-1*time.Second))

	// The IsUnavailable check should clean it up and return false
	assert.False(t, cache.IsUnavailable("expired-offering"))

	// Verify it was removed from the cache
	cache.mu.RLock()
	_, exists := cache.offerings["expired-offering"]
	cache.mu.RUnlock()

	assert.False(t, exists, "Expired offering should be removed during IsUnavailable check")
}

func TestUnavailableOfferings_MultipleExpiredEntries(t *testing.T) {
	cache := NewUnavailableOfferings()

	// Add multiple offerings with different expiry times
	now := time.Now()
	cache.Add("expired-1", now.Add(-2*time.Hour))
	cache.Add("expired-2", now.Add(-1*time.Hour))
	cache.Add("valid-1", now.Add(1*time.Hour))
	cache.Add("expired-3", now.Add(-constants.DefaultVPCClientCacheTTL))
	cache.Add("valid-2", now.Add(2*time.Hour))

	// Run cleanup
	cache.Cleanup()

	// Check that only valid entries remain
	assert.False(t, cache.IsUnavailable("expired-1"))
	assert.False(t, cache.IsUnavailable("expired-2"))
	assert.False(t, cache.IsUnavailable("expired-3"))
	assert.True(t, cache.IsUnavailable("valid-1"))
	assert.True(t, cache.IsUnavailable("valid-2"))

	// Verify cache size
	cache.mu.RLock()
	cacheSize := len(cache.offerings)
	cache.mu.RUnlock()

	assert.Equal(t, 2, cacheSize, "Only valid entries should remain")
}
