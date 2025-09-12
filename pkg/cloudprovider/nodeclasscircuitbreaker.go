package cloudprovider

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// NodeClassCircuitBreakerManager manages per-NodeClass circuit breakers
type NodeClassCircuitBreakerManager struct {
	config          *CircuitBreakerConfig
	logger          logr.Logger
	mu              sync.RWMutex
	breakers        map[string]*CircuitBreaker // key: nodeClass-region
	lastCleanup     time.Time
	cleanupInterval time.Duration
}

// NewNodeClassCircuitBreakerManager creates a new per-NodeClass circuit breaker manager
func NewNodeClassCircuitBreakerManager(config *CircuitBreakerConfig, logger logr.Logger) *NodeClassCircuitBreakerManager {
	manager := &NodeClassCircuitBreakerManager{
		config:          config,
		logger:          logger.WithName("nodeclass-circuit-breaker-manager"),
		breakers:        make(map[string]*CircuitBreaker),
		lastCleanup:     time.Now(),
		cleanupInterval: 30 * time.Minute, // Clean up unused breakers every 30 minutes
	}

	return manager
}

// CanProvision checks if provisioning is allowed for the given NodeClass and region
func (m *NodeClassCircuitBreakerManager) CanProvision(ctx context.Context, nodeClass, region string, _ float64) error {
	// If config is nil, circuit breaker is disabled globally
	if m.config == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Cleanup old unused circuit breakers periodically
	m.cleanupUnusedBreakersIfNeeded()

	// Get or create circuit breaker for this NodeClass-Region combination
	key := m.getKey(nodeClass, region)
	breaker, exists := m.breakers[key]
	if !exists {
		// Create new circuit breaker for this NodeClass-Region
		breakerLogger := m.logger.WithValues("nodeClass", nodeClass, "region", region)
		breaker = NewCircuitBreaker(m.config, breakerLogger)
		m.breakers[key] = breaker

		m.logger.Info("Created new circuit breaker",
			"nodeClass", nodeClass,
			"region", region,
			"key", key)
	}

	// Check if provisioning is allowed for this specific NodeClass-Region
	return breaker.CanProvision(ctx, nodeClass, region, 0)
}

// RecordSuccess records a successful provisioning operation for the NodeClass-Region
func (m *NodeClassCircuitBreakerManager) RecordSuccess(nodeClass, region string) {
	if m.config == nil {
		return
	}

	m.mu.RLock()
	key := m.getKey(nodeClass, region)
	breaker, exists := m.breakers[key]
	m.mu.RUnlock()

	if exists {
		breaker.RecordSuccess(nodeClass, region)
	} else {
		// This shouldn't happen in normal flow, but log it for debugging
		m.logger.Info("Recording success for non-existent circuit breaker",
			"nodeClass", nodeClass,
			"region", region)
	}
}

// RecordFailure records a failed provisioning operation for the NodeClass-Region
func (m *NodeClassCircuitBreakerManager) RecordFailure(nodeClass, region string, err error) {
	if m.config == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Get or create circuit breaker for this NodeClass-Region combination
	key := m.getKey(nodeClass, region)
	breaker, exists := m.breakers[key]
	if !exists {
		// Create new circuit breaker for this NodeClass-Region
		breakerLogger := m.logger.WithValues("nodeClass", nodeClass, "region", region)
		breaker = NewCircuitBreaker(m.config, breakerLogger)
		m.breakers[key] = breaker

		m.logger.Info("Created new circuit breaker for failure recording",
			"nodeClass", nodeClass,
			"region", region,
			"key", key)
	}

	breaker.RecordFailure(nodeClass, region, err)
}

// GetState returns the current state of all circuit breakers
func (m *NodeClassCircuitBreakerManager) GetState() (map[string]*CircuitBreakerStatus, error) {
	if m.config == nil {
		return make(map[string]*CircuitBreakerStatus), nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make(map[string]*CircuitBreakerStatus)
	for key, breaker := range m.breakers {
		status, err := breaker.GetState()
		if err != nil {
			m.logger.Error(err, "Failed to get circuit breaker state", "key", key)
			continue
		}
		states[key] = status
	}

	return states, nil
}

// GetStateForNodeClass returns the circuit breaker state for a specific NodeClass-Region
func (m *NodeClassCircuitBreakerManager) GetStateForNodeClass(nodeClass, region string) (*CircuitBreakerStatus, error) {
	if m.config == nil {
		return &CircuitBreakerStatus{
			State:            CircuitBreakerClosed,
			RecentFailures:   0,
			FailureThreshold: 0,
		}, nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	key := m.getKey(nodeClass, region)
	breaker, exists := m.breakers[key]
	if !exists {
		// No circuit breaker exists yet - return closed state
		return &CircuitBreakerStatus{
			State:            CircuitBreakerClosed,
			RecentFailures:   0,
			FailureThreshold: m.config.FailureThreshold,
		}, nil
	}

	return breaker.GetState()
}

// GetOpenNodeClasses returns a list of NodeClass-Region combinations that have open circuit breakers
func (m *NodeClassCircuitBreakerManager) GetOpenNodeClasses() []string {
	if m.config == nil {
		return []string{}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	var openBreakers []string
	for key, breaker := range m.breakers {
		if status, err := breaker.GetState(); err == nil && status.State == CircuitBreakerOpen {
			openBreakers = append(openBreakers, key)
		}
	}

	return openBreakers
}

// ResetNodeClass manually resets the circuit breaker for a specific NodeClass-Region
func (m *NodeClassCircuitBreakerManager) ResetNodeClass(nodeClass, region string) error {
	if m.config == nil {
		return fmt.Errorf("circuit breaker is disabled")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	key := m.getKey(nodeClass, region)
	_, exists := m.breakers[key]
	if !exists {
		return fmt.Errorf("no circuit breaker found for NodeClass %s in region %s", nodeClass, region)
	}

	// Reset by creating a new circuit breaker
	breakerLogger := m.logger.WithValues("nodeClass", nodeClass, "region", region)
	m.breakers[key] = NewCircuitBreaker(m.config, breakerLogger)

	m.logger.Info("Circuit breaker reset",
		"nodeClass", nodeClass,
		"region", region,
		"key", key)

	return nil
}

// Helper methods

// getKey generates a unique key for NodeClass-Region combination
func (m *NodeClassCircuitBreakerManager) getKey(nodeClass, region string) string {
	return fmt.Sprintf("%s-%s", nodeClass, region)
}

// cleanupUnusedBreakersIfNeeded removes circuit breakers that haven't been used recently
func (m *NodeClassCircuitBreakerManager) cleanupUnusedBreakersIfNeeded() {
	now := time.Now()
	if now.Sub(m.lastCleanup) < m.cleanupInterval {
		return
	}

	m.lastCleanup = now

	// Find circuit breakers that haven't been used for a long time
	cutoff := now.Add(-2 * time.Hour) // Remove breakers unused for 2 hours
	var toDelete []string

	for key, breaker := range m.breakers {
		status, err := breaker.GetState()
		if err != nil {
			continue
		}

		// Remove if:
		// 1. Circuit is closed AND last state change was more than 2 hours ago
		// 2. OR recent failures is 0 AND last state change was more than 2 hours ago
		if (status.State == CircuitBreakerClosed || status.RecentFailures == 0) &&
			status.LastStateChange.Before(cutoff) {
			toDelete = append(toDelete, key)
		}
	}

	// Delete unused circuit breakers
	for _, key := range toDelete {
		delete(m.breakers, key)
		m.logger.Info("Cleaned up unused circuit breaker", "key", key)
	}

	if len(toDelete) > 0 {
		m.logger.Info("Circuit breaker cleanup completed",
			"removed", len(toDelete),
			"remaining", len(m.breakers))
	}
}

// GetStats returns statistics about the circuit breaker manager
func (m *NodeClassCircuitBreakerManager) GetStats() map[string]interface{} {
	if m.config == nil {
		return map[string]interface{}{
			"enabled": false,
		}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := map[string]interface{}{
		"enabled":            true,
		"total_breakers":     len(m.breakers),
		"open_breakers":      0,
		"closed_breakers":    0,
		"half_open_breakers": 0,
	}

	for _, breaker := range m.breakers {
		if status, err := breaker.GetState(); err == nil {
			switch status.State {
			case CircuitBreakerOpen:
				stats["open_breakers"] = stats["open_breakers"].(int) + 1
			case CircuitBreakerClosed:
				stats["closed_breakers"] = stats["closed_breakers"].(int) + 1
			case CircuitBreakerHalfOpen:
				stats["half_open_breakers"] = stats["half_open_breakers"].(int) + 1
			}
		}
	}

	return stats
}
