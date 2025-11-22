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
	"sync"
	"time"
)

// Entry represents a cached entry with expiration
type Entry struct {
	Value      interface{}
	Expiration time.Time
}

// Cache is a generic TTL-based cache
type Cache struct {
	mu       sync.RWMutex
	items    map[string]*Entry
	ttl      time.Duration
	stopChan chan struct{}
	stopOnce sync.Once
}

// New creates a new cache with the specified TTL
func New(ttl time.Duration) *Cache {
	c := &Cache{
		items:    make(map[string]*Entry),
		ttl:      ttl,
		stopChan: make(chan struct{}),
	}

	// Start cleanup goroutine
	go c.cleanup()

	return c
}

// Get retrieves a value from the cache
func (c *Cache) Get(key string) (interface{}, bool) {
	// First, try with read lock for the common case (non-expired entries)
	c.mu.RLock()
	entry, exists := c.items[key]
	if !exists {
		c.mu.RUnlock()
		return nil, false
	}

	now := time.Now()
	if !now.After(entry.Expiration) {
		defer c.mu.RUnlock()
		return entry.Value, true
	}

	// Entry is expired, upgrade to write lock to clean it up
	c.mu.RUnlock()
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check the entry still exists and is still expired (race condition protection)
	entry, exists = c.items[key]
	if exists && now.After(entry.Expiration) {
		delete(c.items, key)
	}
	return nil, false
}

// Set stores a value in the cache with the default TTL
func (c *Cache) Set(key string, value interface{}) {
	c.SetWithTTL(key, value, c.ttl)
}

// SetWithTTL stores a value in the cache with a custom TTL
func (c *Cache) SetWithTTL(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = &Entry{
		Value:      value,
		Expiration: time.Now().Add(ttl),
	}
}

// Delete removes a key from the cache
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// Has checks if a key exists in the cache (and is not expired)
func (c *Cache) Has(key string) bool {
	_, exists := c.Get(key)
	return exists
}

// Size returns the number of items in the cache
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Clear removes all items from the cache
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*Entry)
}

// Stop stops the cleanup goroutine
func (c *Cache) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopChan)
	})
}

// cleanup periodically removes expired entries
func (c *Cache) cleanup() {
	ticker := time.NewTicker(c.ttl / 2) // Cleanup every half TTL period
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.removeExpired()
		case <-c.stopChan:
			return
		}
	}
}

// removeExpired removes all expired entries
func (c *Cache) removeExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.items {
		if now.After(entry.Expiration) {
			delete(c.items, key)
		}
	}
}

// GetOrSet retrieves a value from cache or sets it using the provided function
func (c *Cache) GetOrSet(key string, fetchFunc func() (interface{}, error)) (interface{}, error) {
	// First, try to get from cache
	if value, exists := c.Get(key); exists {
		return value, nil
	}

	// Not in cache, fetch the value
	value, err := fetchFunc()
	if err != nil {
		return nil, err
	}

	// Store in cache
	c.Set(key, value)
	return value, nil
}

// GetOrSetWithTTL retrieves a value from cache or sets it using the provided function with custom TTL
func (c *Cache) GetOrSetWithTTL(key string, ttl time.Duration, fetchFunc func() (interface{}, error)) (interface{}, error) {
	// First, try to get from cache
	if value, exists := c.Get(key); exists {
		return value, nil
	}

	// Not in cache, fetch the value
	value, err := fetchFunc()
	if err != nil {
		return nil, err
	}

	// Store in cache with custom TTL
	c.SetWithTTL(key, value, ttl)
	return value, nil
}
