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

package vpcclient

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

// Manager provides centralized VPC client management with caching and proper error handling
type Manager struct {
	client       *ibm.Client
	cachedClient *ibm.VPCClient
	mu           sync.RWMutex
	cacheTime    time.Time
	cacheTTL     time.Duration
}

// NewManager creates a new VPC client manager with the specified cache TTL
func NewManager(client *ibm.Client, cacheTTL time.Duration) *Manager {
	if cacheTTL == 0 {
		cacheTTL = 30 * time.Minute // Default cache TTL
	}
	return &Manager{
		client:   client,
		cacheTTL: cacheTTL,
	}
}

// GetVPCClient returns a cached VPC client or creates a new one if necessary
func (m *Manager) GetVPCClient(ctx context.Context) (*ibm.VPCClient, error) {
	logger := log.FromContext(ctx)

	// Fast path: check if we have a valid cached client
	m.mu.RLock()
	if m.cachedClient != nil && time.Since(m.cacheTime) < m.cacheTTL {
		defer m.mu.RUnlock()
		logger.V(4).Info("Using cached VPC client", "cache_age", time.Since(m.cacheTime))
		return m.cachedClient, nil
	}
	m.mu.RUnlock()

	// Slow path: need to create or refresh the client
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check pattern to avoid duplicate creation
	if m.cachedClient != nil && time.Since(m.cacheTime) < m.cacheTTL {
		return m.cachedClient, nil
	}

	logger.V(3).Info("Creating new VPC client", "reason", m.getCacheInvalidationReason())

	if m.client == nil {
		return nil, fmt.Errorf("getting VPC client: IBM client is nil")
	}

	client, err := m.client.GetVPCClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting VPC client: %w", err)
	}

	m.cachedClient = client
	m.cacheTime = time.Now()

	logger.V(3).Info("VPC client created and cached", "ttl", m.cacheTTL)
	return client, nil
}

// InvalidateCache forces the next GetVPCClient call to create a new client
func (m *Manager) InvalidateCache() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cachedClient = nil
	m.cacheTime = time.Time{}
}

// getCacheInvalidationReason returns why the cache was invalidated
func (m *Manager) getCacheInvalidationReason() string {
	if m.cachedClient == nil {
		return "no_cached_client"
	}
	if time.Since(m.cacheTime) >= m.cacheTTL {
		return "cache_expired"
	}
	return "unknown"
}

// WithVPCClient executes a function with a VPC client, handling errors consistently
func (m *Manager) WithVPCClient(ctx context.Context, fn func(*ibm.VPCClient) error) error {
	client, err := m.GetVPCClient(ctx)
	if err != nil {
		return err
	}
	return fn(client)
}

// HandleVPCError provides consistent error handling for VPC operations
func HandleVPCError(err error, logger logr.Logger, operation string, extraFields ...interface{}) error {
	if err == nil {
		return nil
	}

	ibmErr := ibm.ParseError(err)

	// Build log fields
	fields := []interface{}{
		"operation", operation,
		"status_code", ibmErr.StatusCode,
		"error_code", ibmErr.Code,
		"retryable", ibmErr.Retryable,
	}
	fields = append(fields, extraFields...)

	// Log with appropriate level based on error type
	if ibmErr.StatusCode >= 500 || ibmErr.Retryable {
		logger.Error(err, "VPC operation failed (retryable)", fields...)
	} else if ibmErr.StatusCode == 404 {
		logger.V(2).Info("VPC resource not found", fields...)
	} else {
		logger.Error(err, "VPC operation failed", fields...)
	}

	return fmt.Errorf("%s: %w", operation, err)
}
