package cloudprovider

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pollUntil polls a condition until it's true or timeout
func pollUntil(t *testing.T, condition func() bool, timeout time.Duration, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(message)
}

func TestNewCircuitBreaker(t *testing.T) {
	tests := []struct {
		name   string
		config *CircuitBreakerConfig
	}{
		{
			name:   "with default config",
			config: nil,
		},
		{
			name: "with custom config",
			config: &CircuitBreakerConfig{
				FailureThreshold:       5,
				FailureWindow:          10 * time.Minute,
				RecoveryTimeout:        30 * time.Minute,
				HalfOpenMaxRequests:    3,
				RateLimitPerMinute:     1,
				MaxConcurrentInstances: 3,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := NewCircuitBreaker(tt.config, logr.Discard())

			assert.NotNil(t, cb)
			assert.Equal(t, CircuitBreakerClosed, cb.state)
			assert.Empty(t, cb.failures)

			if tt.config == nil {
				// Should use default config
				assert.Equal(t, 3, cb.config.FailureThreshold)
				assert.Equal(t, 5*time.Minute, cb.config.FailureWindow)
				assert.Equal(t, 15*time.Minute, cb.config.RecoveryTimeout)
				assert.Equal(t, 2, cb.config.HalfOpenMaxRequests)
				assert.Equal(t, 2, cb.config.RateLimitPerMinute)
				assert.Equal(t, 5, cb.config.MaxConcurrentInstances)
			} else {
				// Should use provided config
				assert.Equal(t, tt.config.FailureThreshold, cb.config.FailureThreshold)
				assert.Equal(t, tt.config.FailureWindow, cb.config.FailureWindow)
				assert.Equal(t, tt.config.RecoveryTimeout, cb.config.RecoveryTimeout)
				assert.Equal(t, tt.config.HalfOpenMaxRequests, cb.config.HalfOpenMaxRequests)
				assert.Equal(t, tt.config.RateLimitPerMinute, cb.config.RateLimitPerMinute)
				assert.Equal(t, tt.config.MaxConcurrentInstances, cb.config.MaxConcurrentInstances)
			}
		})
	}
}

// TestDisabledCircuitBreaker tests that a disabled circuit breaker allows all operations
func TestDisabledCircuitBreaker(t *testing.T) {
	logger := logr.Discard()
	// Create a disabled circuit breaker by passing nil config
	cb := NewCircuitBreaker(nil, logger)

	ctx := context.Background()

	// Test that CanProvision always allows when disabled
	for i := 0; i < 10; i++ {
		err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
		assert.NoError(t, err, "Disabled circuit breaker should always allow provisioning")
	}

	// Test that RecordFailure doesn't affect state when disabled
	for i := 0; i < 10; i++ {
		cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error %d", i))
	}

	// Should still allow provisioning after many failures
	err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err, "Disabled circuit breaker should allow provisioning even after failures")

	// Test that RecordSuccess doesn't affect state when disabled
	cb.RecordSuccess("test-nodeclass", "us-south")

	// Get state should show it's disabled
	status, err := cb.GetState()
	assert.NoError(t, err)
	assert.Equal(t, CircuitBreakerClosed, status.State, "Disabled circuit breaker should always be in CLOSED state")
}

func TestCircuitBreaker_CanProvision_RateLimit(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:       3,
		FailureWindow:          5 * time.Minute,
		RecoveryTimeout:        15 * time.Minute,
		HalfOpenMaxRequests:    2,
		RateLimitPerMinute:     2, // Only 2 per minute
		MaxConcurrentInstances: 5,
	}

	cb := NewCircuitBreaker(config, logr.Discard())
	ctx := context.Background()

	// First 2 requests should succeed
	err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)

	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)

	// Third request should be rate limited
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.Error(t, err)
	assert.IsType(t, &RateLimitError{}, err)

	rateLimitErr := err.(*RateLimitError)
	assert.Equal(t, 2, rateLimitErr.Limit)
	assert.Equal(t, 2, rateLimitErr.Current)
	assert.True(t, rateLimitErr.TimeToReset > 0)
}

func TestCircuitBreaker_CanProvision_ConcurrencyLimit(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:       3,
		FailureWindow:          5 * time.Minute,
		RecoveryTimeout:        15 * time.Minute,
		HalfOpenMaxRequests:    2,
		RateLimitPerMinute:     10, // High rate limit
		MaxConcurrentInstances: 2,  // Only 2 concurrent
	}

	cb := NewCircuitBreaker(config, logr.Discard())
	ctx := context.Background()

	// First 2 requests should succeed
	err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)

	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)

	// Third request should hit concurrency limit
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.Error(t, err)
	assert.IsType(t, &ConcurrencyLimitError{}, err)

	concurrencyErr := err.(*ConcurrencyLimitError)
	assert.Equal(t, 2, concurrencyErr.Limit)
	assert.Equal(t, 2, concurrencyErr.Current)
}

func TestCircuitBreaker_FailureThreshold(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:       2, // Low threshold for testing
		FailureWindow:          5 * time.Minute,
		RecoveryTimeout:        15 * time.Minute,
		HalfOpenMaxRequests:    2,
		RateLimitPerMinute:     10,
		MaxConcurrentInstances: 10,
	}

	cb := NewCircuitBreaker(config, logr.Discard())
	ctx := context.Background()

	// Circuit should start closed
	assert.Equal(t, CircuitBreakerClosed, cb.state)

	// First failure
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error 1"))
	assert.Equal(t, CircuitBreakerClosed, cb.state)

	// Second failure should open the circuit
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error 2"))
	assert.Equal(t, CircuitBreakerOpen, cb.state)

	// Should now block requests
	err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.Error(t, err)
	assert.IsType(t, &CircuitBreakerError{}, err)

	cbErr := err.(*CircuitBreakerError)
	assert.Equal(t, CircuitBreakerOpen, cbErr.State)
	assert.Contains(t, cbErr.Message, "provisioning blocked due to recent failures")
	assert.True(t, cbErr.TimeToWait > 0)
}

func TestCircuitBreaker_RecoveryFlow(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:       2,
		FailureWindow:          5 * time.Minute,
		RecoveryTimeout:        100 * time.Millisecond, // Short timeout for testing
		HalfOpenMaxRequests:    1,
		RateLimitPerMinute:     10,
		MaxConcurrentInstances: 10,
	}

	cb := NewCircuitBreaker(config, logr.Discard())
	ctx := context.Background()

	// Trigger circuit open
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error 1"))
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error 2"))
	assert.Equal(t, CircuitBreakerOpen, cb.state)

	// Wait for recovery timeout using polling
	pollUntil(t, func() bool {
		// Check if we can provision - if so, circuit breaker is in half-open
		err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
		return err == nil
	}, 500*time.Millisecond, "circuit breaker should transition to half-open")

	// Should be in half-open state now
	assert.Equal(t, CircuitBreakerHalfOpen, cb.state)

	// Second request should be blocked (exceeds HalfOpenMaxRequests)
	err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.Error(t, err)
	assert.IsType(t, &CircuitBreakerError{}, err)

	// Record success should close the circuit
	cb.RecordSuccess("test-nodeclass", "us-south")
	assert.Equal(t, CircuitBreakerClosed, cb.state)

	// Should now allow normal requests
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)
}

func TestCircuitBreaker_HalfOpenFailureReturnsToOpen(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:       2,
		FailureWindow:          5 * time.Minute,
		RecoveryTimeout:        100 * time.Millisecond,
		HalfOpenMaxRequests:    2,
		RateLimitPerMinute:     10,
		MaxConcurrentInstances: 10,
	}

	cb := NewCircuitBreaker(config, logr.Discard())

	// Trigger circuit open
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error 1"))
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error 2"))
	assert.Equal(t, CircuitBreakerOpen, cb.state)

	// Wait for recovery and transition to half-open using polling
	pollUntil(t, func() bool {
		err := cb.CanProvision(context.Background(), "test-nodeclass", "us-south", 0)
		return err == nil && cb.state == CircuitBreakerHalfOpen
	}, 500*time.Millisecond, "circuit breaker should transition to half-open")

	// Record failure in half-open state should return to open
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error 3"))
	assert.Equal(t, CircuitBreakerOpen, cb.state)
}

func TestCircuitBreaker_ResetCountersAfterTime(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:       3,
		FailureWindow:          5 * time.Minute,
		RecoveryTimeout:        15 * time.Minute,
		HalfOpenMaxRequests:    2,
		RateLimitPerMinute:     2,
		MaxConcurrentInstances: 5,
	}

	cb := NewCircuitBreaker(config, logr.Discard())
	ctx := context.Background()

	// Use up rate limit
	err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)

	// Should be rate limited
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.Error(t, err)
	assert.IsType(t, &RateLimitError{}, err)

	// Manually reset time (simulate time passage)
	cb.mu.Lock()
	cb.lastMinuteReset = time.Now().Add(-2 * time.Minute)
	cb.mu.Unlock()

	// Should now allow requests again
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)
}

func TestCircuitBreaker_RecordSuccessDecrementsConcurrency(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig(), logr.Discard())
	ctx := context.Background()

	// Provision some instances
	err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)

	// Check concurrency
	status, err := cb.GetState()
	require.NoError(t, err)
	assert.Equal(t, 2, status.ConcurrentInstances)

	// Record success should decrement
	cb.RecordSuccess("test-nodeclass", "us-south")
	status, err = cb.GetState()
	require.NoError(t, err)
	assert.Equal(t, 1, status.ConcurrentInstances)
}

func TestCircuitBreaker_RecordFailureDecrementsConcurrency(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig(), logr.Discard())
	ctx := context.Background()

	// Provision some instances
	err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)

	// Check concurrency
	status, err := cb.GetState()
	require.NoError(t, err)
	assert.Equal(t, 2, status.ConcurrentInstances)

	// Record failure should decrement
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error"))
	status, err = cb.GetState()
	require.NoError(t, err)
	assert.Equal(t, 1, status.ConcurrentInstances)
}

func TestCircuitBreaker_GetState(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:       2,
		FailureWindow:          5 * time.Minute,
		RecoveryTimeout:        15 * time.Minute,
		HalfOpenMaxRequests:    1,
		RateLimitPerMinute:     3,
		MaxConcurrentInstances: 4,
	}

	cb := NewCircuitBreaker(config, logr.Discard())
	ctx := context.Background()

	// Initial state
	status, err := cb.GetState()
	require.NoError(t, err)
	assert.Equal(t, CircuitBreakerClosed, status.State)
	assert.Equal(t, 0, status.RecentFailures)
	assert.Equal(t, 2, status.FailureThreshold)
	assert.Equal(t, 0, status.InstancesThisMinute)
	assert.Equal(t, 3, status.RateLimit)
	assert.Equal(t, 0, status.ConcurrentInstances)
	assert.Equal(t, 4, status.MaxConcurrent)
	assert.Equal(t, time.Duration(0), status.TimeToRecovery)

	// Add some activity
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error"))

	status, err = cb.GetState()
	require.NoError(t, err)
	assert.Equal(t, CircuitBreakerClosed, status.State)
	assert.Equal(t, 1, status.RecentFailures)
	assert.Equal(t, 1, status.InstancesThisMinute)
	assert.Equal(t, 0, status.ConcurrentInstances) // Decremented by RecordFailure
}

func TestCircuitBreaker_CleanOldFailures(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold:       5,
		FailureWindow:          100 * time.Millisecond, // Short window for testing
		RecoveryTimeout:        15 * time.Minute,
		HalfOpenMaxRequests:    2,
		RateLimitPerMinute:     10,
		MaxConcurrentInstances: 10,
	}

	cb := NewCircuitBreaker(config, logr.Discard())

	// Add failures
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("error 1"))
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("error 2"))

	status, err := cb.GetState()
	require.NoError(t, err)
	assert.Equal(t, 2, status.RecentFailures)

	// Wait for failures to age out using polling
	pollUntil(t, func() bool {
		st, stErr := cb.GetState()
		return stErr == nil && st.RecentFailures == 0
	}, 500*time.Millisecond, "old failures should be cleaned up")

	// Verify cleanup occurred
	status, err = cb.GetState()
	require.NoError(t, err)
	assert.Equal(t, 0, status.RecentFailures)
}

func TestCircuitBreaker_ErrorTypes(t *testing.T) {
	t.Run("CircuitBreakerError", func(t *testing.T) {
		err := &CircuitBreakerError{
			State:      CircuitBreakerOpen,
			Message:    "test message",
			TimeToWait: 5 * time.Minute,
		}

		expected := "circuit breaker OPEN: test message (retry in 5m0s)"
		assert.Equal(t, expected, err.Error())

		// Test without TimeToWait
		err.TimeToWait = 0
		expected = "circuit breaker OPEN: test message"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("RateLimitError", func(t *testing.T) {
		err := &RateLimitError{
			Limit:       2,
			Current:     3,
			TimeToReset: 30 * time.Second,
		}

		expected := "rate limit exceeded: 3/2 instances this minute (reset in 30s)"
		assert.Equal(t, expected, err.Error())
	})

	t.Run("ConcurrencyLimitError", func(t *testing.T) {
		err := &ConcurrencyLimitError{
			Limit:   5,
			Current: 6,
		}

		expected := "concurrency limit exceeded: 6/5 concurrent instances"
		assert.Equal(t, expected, err.Error())
	})
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig(), logr.Discard())
	ctx := context.Background()

	// Test concurrent access doesn't cause race conditions
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- true }()

			// Mix of operations
			switch id % 3 {
			case 0:
				_ = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
			case 1:
				cb.RecordSuccess("test-nodeclass", "us-south")
			default:
				cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("test error %d", id))
			}

			// Get state
			_, _ = cb.GetState()
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should not panic and should be able to get final state
	status, err := cb.GetState()
	require.NoError(t, err)
	assert.NotNil(t, status)
}

func TestDefaultCircuitBreakerConfig(t *testing.T) {
	config := DefaultCircuitBreakerConfig()

	assert.Equal(t, 3, config.FailureThreshold)
	assert.Equal(t, 5*time.Minute, config.FailureWindow)
	assert.Equal(t, 15*time.Minute, config.RecoveryTimeout)
	assert.Equal(t, 2, config.HalfOpenMaxRequests)
	assert.Equal(t, 2, config.RateLimitPerMinute)
	assert.Equal(t, 5, config.MaxConcurrentInstances)
}

func TestCircuitBreaker_IntegrationWithCloudProvider(t *testing.T) {
	// Test that circuit breaker integrates correctly with CloudProvider.Create()
	config := &CircuitBreakerConfig{
		FailureThreshold:       2,
		FailureWindow:          5 * time.Minute,
		RecoveryTimeout:        15 * time.Minute,
		HalfOpenMaxRequests:    1,
		RateLimitPerMinute:     1, // Very low for testing
		MaxConcurrentInstances: 1,
	}

	cb := NewCircuitBreaker(config, logr.Discard())
	ctx := context.Background()

	// First request should succeed
	err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.NoError(t, err)

	// Second request should be rate limited
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.Error(t, err)
	assert.IsType(t, &RateLimitError{}, err)
	assert.Contains(t, err.Error(), "rate limit exceeded")

	// Record some failures to trigger circuit open
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("bootstrap failure"))
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("another bootstrap failure"))

	// Should now have circuit open
	status, err := cb.GetState()
	require.NoError(t, err)
	assert.Equal(t, CircuitBreakerOpen, status.State)

	// Reset rate limit by advancing time
	cb.mu.Lock()
	cb.lastMinuteReset = time.Now().Add(-2 * time.Minute)
	cb.instancesThisMinute = 0
	cb.concurrentInstances = 0
	cb.mu.Unlock()

	// Should still be blocked by circuit breaker
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.Error(t, err)
	assert.IsType(t, &CircuitBreakerError{}, err)
	assert.Contains(t, err.Error(), "circuit breaker OPEN")
}

func TestCircuitBreaker_ScaleUpStormPrevention(t *testing.T) {
	// Test scenario: simulate the 90-instance scale-up storm and verify circuit breaker prevents it
	config := &CircuitBreakerConfig{
		FailureThreshold:       3,
		FailureWindow:          5 * time.Minute,
		RecoveryTimeout:        15 * time.Minute,
		HalfOpenMaxRequests:    2,
		RateLimitPerMinute:     2, // This would prevent 90 instances in 15 minutes
		MaxConcurrentInstances: 5,
	}

	cb := NewCircuitBreaker(config, logr.Discard())
	ctx := context.Background()

	var successfulProvisions int
	var rateLimitErrors int
	var circuitBreakerErrors int

	// Simulate rapid provisioning attempts (like the 90-instance storm)
	for i := 0; i < 10; i++ {
		err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
		if err == nil {
			successfulProvisions++
			// Simulate bootstrap failures
			cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("bootstrap failure %d", i))
		} else if _, ok := err.(*RateLimitError); ok {
			rateLimitErrors++
		} else if _, ok := err.(*CircuitBreakerError); ok {
			circuitBreakerErrors++
		}
	}

	// Should be limited to 2 successful provisions per minute
	assert.Equal(t, 2, successfulProvisions)
	assert.True(t, rateLimitErrors > 0, "Should have rate limit errors")

	// Add one more failure to trigger circuit open (need 3 total)
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("third bootstrap failure"))

	// After 3 failures, circuit should open
	status, err := cb.GetState()
	require.NoError(t, err)
	assert.Equal(t, CircuitBreakerOpen, status.State)

	// Reset rate limit for next test
	cb.mu.Lock()
	cb.lastMinuteReset = time.Now().Add(-2 * time.Minute)
	cb.instancesThisMinute = 0
	cb.concurrentInstances = 0
	cb.mu.Unlock()

	// Further attempts should be blocked by circuit breaker
	err = cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
	assert.Error(t, err)
	assert.IsType(t, &CircuitBreakerError{}, err)

	t.Logf("Protection Summary: %d successful, %d rate limited, %d circuit blocked",
		successfulProvisions, rateLimitErrors, circuitBreakerErrors)
}

// TestCircuitBreaker_EnhancedLogging tests the enhanced logging functionality
func TestCircuitBreaker_EnhancedLogging(t *testing.T) {
	tests := []struct {
		name           string
		failures       []string
		expectedPhrase string
	}{
		{
			name: "single_timeout_error",
			failures: []string{
				"request timeout occurred",
				"another timeout",
				"third timeout",
			},
			expectedPhrase: "API timeout",
		},
		{
			name: "multiple_similar_errors",
			failures: []string{
				"request timeout occurred",
				"connection timeout",
				"timeout waiting for response",
			},
			expectedPhrase: "API timeout (x3)",
		},
		{
			name: "mixed_error_types",
			failures: []string{
				"subnet not found",
				"request timeout occurred",
				"security group not found",
			},
			expectedPhrase: "Subnet not found",
		},
		{
			name: "quota_limit_errors",
			failures: []string{
				"quota exceeded for instances",
				"instance limit reached",
				"quota limit exceeded",
			},
			expectedPhrase: "Resource quota/limit exceeded",
		},
		{
			name: "network_errors",
			failures: []string{
				"network connection failed",
				"network unreachable",
				"network timeout",
			},
			expectedPhrase: "Network error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &CircuitBreakerConfig{
				FailureThreshold:    3,
				FailureWindow:       5 * time.Minute,
				RecoveryTimeout:     15 * time.Minute,
				HalfOpenMaxRequests: 2,
			}

			logger := logr.Discard()
			cb := NewCircuitBreaker(config, logger)
			ctx := context.Background()

			// Record the test failures
			for _, failureMsg := range tt.failures {
				cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("%s", failureMsg))
			}

			// Check that the circuit breaker is now open
			err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)
			assert.Error(t, err)

			cbErr, ok := err.(*CircuitBreakerError)
			if !assert.True(t, ok, "Expected CircuitBreakerError") {
				t.Logf("Got error of type: %T, value: %v", err, err)
				return
			}
			assert.Contains(t, cbErr.Message, tt.expectedPhrase, "Error message should contain expected phrase")
			assert.Contains(t, cbErr.Message, "Recent failures:", "Error message should contain failure context")
		})
	}
}

// TestCircuitBreaker_SimplifyError tests the error simplification logic
func TestCircuitBreaker_SimplifyError(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	logger := logr.Discard()
	cb := NewCircuitBreaker(config, logger)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "timeout_error",
			input:    "request timeout occurred while calling API",
			expected: "API timeout",
		},
		{
			name:     "subnet_not_found",
			input:    "subnet subnet-123 not found in region us-south",
			expected: "Subnet not found",
		},
		{
			name:     "security_group_not_found",
			input:    "security group sg-456 not found",
			expected: "Security group not found",
		},
		{
			name:     "image_not_found",
			input:    "image img-789 not found in catalog",
			expected: "Image not found",
		},
		{
			name:     "quota_exceeded",
			input:    "quota exceeded: cannot create more instances",
			expected: "Resource quota/limit exceeded",
		},
		{
			name:     "unauthorized",
			input:    "unauthorized access to resource",
			expected: "Unauthorized/authentication error",
		},
		{
			name:     "insufficient_capacity",
			input:    "insufficient capacity in zone us-south-1",
			expected: "Insufficient capacity",
		},
		{
			name:     "invalid_configuration",
			input:    "invalid instance configuration provided",
			expected: "Invalid configuration",
		},
		{
			name:     "network_error",
			input:    "network connection failed",
			expected: "Network error",
		},
		{
			name:     "long_error_truncated",
			input:    "this is a very long error message that should be truncated because it exceeds the length limit",
			expected: "this is a very long error message that should b...",
		},
		{
			name:     "short_specific_error",
			input:    "connection refused",
			expected: "connection refused",
		},
		{
			name:     "colon_separated_error",
			input:    "API Error: invalid parameter value",
			expected: "Invalid configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cb.simplifyError(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestCircuitBreaker_GetRecentFailuresSummary tests the failure summary generation
func TestCircuitBreaker_GetRecentFailuresSummary(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold: 3,
		FailureWindow:    5 * time.Minute,
		RecoveryTimeout:  15 * time.Minute,
	}

	logger := logr.Discard()
	cb := NewCircuitBreaker(config, logger)

	t.Run("empty_failures", func(t *testing.T) {
		summary := cb.getRecentFailuresSummary()
		assert.Empty(t, summary, "Summary should be empty when no failures")
	})

	t.Run("single_failure", func(t *testing.T) {
		cb.failures = []FailureRecord{
			{
				Timestamp: time.Now(),
				Error:     "request timeout",
				NodeClass: "test",
				Region:    "us-south",
			},
		}

		summary := cb.getRecentFailuresSummary()
		assert.Contains(t, summary, "API timeout")
		assert.Contains(t, summary, "[")
		assert.Contains(t, summary, "]")
	})

	t.Run("multiple_same_failures", func(t *testing.T) {
		now := time.Now()
		cb.failures = []FailureRecord{
			{Timestamp: now.Add(-1 * time.Minute), Error: "request timeout", NodeClass: "test", Region: "us-south"},
			{Timestamp: now.Add(-2 * time.Minute), Error: "connection timeout", NodeClass: "test", Region: "us-south"},
			{Timestamp: now.Add(-3 * time.Minute), Error: "timeout occurred", NodeClass: "test", Region: "us-south"},
		}

		summary := cb.getRecentFailuresSummary()
		assert.Contains(t, summary, "API timeout")
		assert.Contains(t, summary, "x3")
	})

	t.Run("mixed_failures", func(t *testing.T) {
		now := time.Now()
		cb.failures = []FailureRecord{
			{Timestamp: now.Add(-1 * time.Minute), Error: "request timeout", NodeClass: "test", Region: "us-south"},
			{Timestamp: now.Add(-2 * time.Minute), Error: "subnet not found", NodeClass: "test", Region: "us-south"},
			{Timestamp: now.Add(-3 * time.Minute), Error: "quota exceeded", NodeClass: "test", Region: "us-south"},
		}

		summary := cb.getRecentFailuresSummary()
		assert.Contains(t, summary, "API timeout")
		assert.Contains(t, summary, "Subnet not found")
		assert.Contains(t, summary, "Resource quota/limit exceeded")
	})

	t.Run("too_many_failures_truncated", func(t *testing.T) {
		now := time.Now()
		cb.failures = []FailureRecord{
			{Timestamp: now.Add(-1 * time.Minute), Error: "timeout 1", NodeClass: "test", Region: "us-south"},
			{Timestamp: now.Add(-2 * time.Minute), Error: "subnet not found", NodeClass: "test", Region: "us-south"},
			{Timestamp: now.Add(-3 * time.Minute), Error: "quota exceeded", NodeClass: "test", Region: "us-south"},
			{Timestamp: now.Add(-4 * time.Minute), Error: "unauthorized", NodeClass: "test", Region: "us-south"},
			{Timestamp: now.Add(-5 * time.Minute), Error: "invalid config", NodeClass: "test", Region: "us-south"},
		}

		summary := cb.getRecentFailuresSummary()
		// Should be limited to 3 errors plus "..."
		assert.Contains(t, summary, "...")
	})

	t.Run("old_failures_excluded", func(t *testing.T) {
		now := time.Now()
		cb.failures = []FailureRecord{
			{Timestamp: now.Add(-1 * time.Minute), Error: "recent timeout", NodeClass: "test", Region: "us-south"},
			{Timestamp: now.Add(-10 * time.Minute), Error: "old timeout", NodeClass: "test", Region: "us-south"}, // Outside failure window
		}

		summary := cb.getRecentFailuresSummary()
		assert.Contains(t, summary, "API timeout")
		// Should only contain 1 failure, not 2
		assert.NotContains(t, summary, "x2")
	})
}

// TestCircuitBreaker_GetTimestampFromExample tests timestamp parsing from examples
func TestCircuitBreaker_GetTimestampFromExample(t *testing.T) {
	config := DefaultCircuitBreakerConfig()
	logger := logr.Discard()
	cb := NewCircuitBreaker(config, logger)

	tests := []struct {
		name    string
		input   string
		hasTime bool
	}{
		{
			name:    "valid_timestamp",
			input:   "API timeout (13:45:30)",
			hasTime: true,
		},
		{
			name:    "invalid_format",
			input:   "API timeout",
			hasTime: false,
		},
		{
			name:    "empty_string",
			input:   "",
			hasTime: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cb.getTimestampFromExample(tt.input)
			if tt.hasTime {
				assert.False(t, result.IsZero(), "Should parse a valid time")
			} else {
				assert.True(t, result.IsZero(), "Should return zero time for invalid input")
			}
		})
	}
}

// TestCircuitBreaker_EnhancedErrorMessage tests the complete enhanced error message
func TestCircuitBreaker_EnhancedErrorMessage(t *testing.T) {
	config := &CircuitBreakerConfig{
		FailureThreshold: 2,
		FailureWindow:    5 * time.Minute,
		RecoveryTimeout:  15 * time.Minute,
	}

	logger := logr.Discard()
	cb := NewCircuitBreaker(config, logger)
	ctx := context.Background()

	// Record failures to open the circuit breaker
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("request timeout"))
	cb.RecordFailure("test-nodeclass", "us-south", fmt.Errorf("connection timeout"))

	// Try to provision - should be blocked
	err := cb.CanProvision(ctx, "test-nodeclass", "us-south", 0)

	assert.Error(t, err)
	cbErr, ok := err.(*CircuitBreakerError)
	assert.True(t, ok, "Should be CircuitBreakerError")

	// Check that the enhanced message contains failure context
	assert.Contains(t, cbErr.Message, "Circuit breaker is OPEN - provisioning blocked due to recent failures")
	assert.Contains(t, cbErr.Message, "Recent failures:")
	assert.Contains(t, cbErr.Message, "API timeout")

	// Check that retry time is included
	assert.Contains(t, cbErr.Error(), "retry in")
}
