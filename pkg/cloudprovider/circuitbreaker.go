package cloudprovider

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// CircuitBreakerState represents the state of the circuit breaker
type CircuitBreakerState string

const (
	// CircuitBreakerClosed - Normal operation, allowing requests
	CircuitBreakerClosed CircuitBreakerState = "CLOSED"
	// CircuitBreakerOpen - Failing fast, blocking requests
	CircuitBreakerOpen CircuitBreakerState = "OPEN"
	// CircuitBreakerHalfOpen - Testing if the issue is resolved
	CircuitBreakerHalfOpen CircuitBreakerState = "HALF_OPEN"
)

// CircuitBreakerConfig holds configuration for the circuit breaker
type CircuitBreakerConfig struct {
	// FailureThreshold is the number of failures before opening the circuit
	FailureThreshold int
	// FailureWindow is the time window for counting failures
	FailureWindow time.Duration
	// RecoveryTimeout is how long to wait before attempting recovery
	RecoveryTimeout time.Duration
	// HalfOpenMaxRequests is max requests allowed in half-open state
	HalfOpenMaxRequests int
	// RateLimitPerMinute is max instances created per minute
	RateLimitPerMinute int
	// MaxConcurrentInstances is max instances being created simultaneously
	MaxConcurrentInstances int
}

// DefaultCircuitBreakerConfig returns sensible defaults for IBM Cloud
func DefaultCircuitBreakerConfig() *CircuitBreakerConfig {
	return &CircuitBreakerConfig{
		FailureThreshold:       3,                // 3 consecutive failures
		FailureWindow:          5 * time.Minute,  // Within 5 minutes
		RecoveryTimeout:        15 * time.Minute, // Wait 15 minutes before retry
		HalfOpenMaxRequests:    2,                // Allow 2 test requests
		RateLimitPerMinute:     2,                // Max 2 instances/minute
		MaxConcurrentInstances: 5,                // Max 5 concurrent provisions
	}
}

// FailureRecord tracks individual failures
type FailureRecord struct {
	Timestamp time.Time
	Error     string
	NodeClass string
	Region    string
}

// CircuitBreaker implements circuit breaker pattern for instance provisioning
type CircuitBreaker struct {
	config   *CircuitBreakerConfig
	logger   logr.Logger
	mu       sync.RWMutex
	state    CircuitBreakerState
	failures []FailureRecord
	enabled  bool

	// State tracking
	lastStateChange     time.Time
	halfOpenRequests    int
	concurrentInstances int
	instancesThisMinute int
	lastMinuteReset     time.Time
}

// NewCircuitBreaker creates a new circuit breaker with the given configuration
func NewCircuitBreaker(config *CircuitBreakerConfig, logger logr.Logger) *CircuitBreaker {
	// If config is nil, create a disabled circuit breaker
	enabled := config != nil
	if config == nil {
		config = DefaultCircuitBreakerConfig()
	}

	return &CircuitBreaker{
		config:          config,
		logger:          logger,
		state:           CircuitBreakerClosed,
		failures:        make([]FailureRecord, 0),
		enabled:         enabled,
		lastStateChange: time.Now(),
		lastMinuteReset: time.Now(),
	}
}

// CanProvision checks if provisioning is allowed based on circuit breaker state
func (cb *CircuitBreaker) CanProvision(ctx context.Context, nodeClass, region string, _ float64) error {
	// If circuit breaker is disabled, always allow provisioning
	if !cb.enabled {
		return nil
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	// Reset time-based counters
	cb.resetCountersIfNeeded()

	// Check circuit breaker state
	switch cb.state {
	case CircuitBreakerOpen:
		if time.Since(cb.lastStateChange) >= cb.config.RecoveryTimeout {
			cb.transitionToHalfOpen()
		} else {
			// Include recent failure context for better troubleshooting
			recentFailures := cb.getRecentFailuresSummary()
			failureContext := ""
			if len(recentFailures) > 0 {
				failureContext = fmt.Sprintf(" Recent failures: %s", recentFailures)
			}
			return &CircuitBreakerError{
				State:      cb.state,
				Message:    fmt.Sprintf("Circuit breaker is OPEN - provisioning blocked due to recent failures.%s", failureContext),
				TimeToWait: cb.config.RecoveryTimeout - time.Since(cb.lastStateChange),
			}
		}

	case CircuitBreakerHalfOpen:
		if cb.halfOpenRequests >= cb.config.HalfOpenMaxRequests {
			return &CircuitBreakerError{
				State:   cb.state,
				Message: "Circuit breaker is HALF_OPEN - maximum test requests exceeded",
			}
		}
	}

	// Check rate limiting
	if cb.instancesThisMinute >= cb.config.RateLimitPerMinute {
		return &RateLimitError{
			Limit:       cb.config.RateLimitPerMinute,
			Current:     cb.instancesThisMinute,
			TimeToReset: time.Minute - time.Since(cb.lastMinuteReset),
		}
	}

	// Check concurrent instance limit
	if cb.concurrentInstances >= cb.config.MaxConcurrentInstances {
		return &ConcurrencyLimitError{
			Limit:   cb.config.MaxConcurrentInstances,
			Current: cb.concurrentInstances,
		}
	}

	// All checks passed - increment counters
	cb.instancesThisMinute++
	cb.concurrentInstances++

	if cb.state == CircuitBreakerHalfOpen {
		cb.halfOpenRequests++
	}

	cb.logger.Info("Provisioning was allowed",
		"nodeClass", nodeClass,
		"region", region,
		"state", cb.state,
		"instancesThisMinute", cb.instancesThisMinute,
		"concurrentInstances", cb.concurrentInstances)

	return nil
}

// RecordSuccess records a successful provisioning operation
func (cb *CircuitBreaker) RecordSuccess(nodeClass, region string) {
	// If circuit breaker is disabled, do nothing
	if !cb.enabled {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.concurrentInstances > 0 {
		cb.concurrentInstances--
	}

	if cb.state == CircuitBreakerHalfOpen {
		// Successful operation in half-open state - close the circuit
		cb.transitionToClosed()
		cb.logger.Info("Circuit breaker transitioned to CLOSED after successful operation",
			"nodeClass", nodeClass, "region", region)
	}

	cb.logger.Info("Provisioning success recorded",
		"nodeClass", nodeClass,
		"region", region,
		"state", cb.state,
		"concurrentInstances", cb.concurrentInstances)
}

// RecordFailure records a failed provisioning operation
func (cb *CircuitBreaker) RecordFailure(nodeClass, region string, err error) {
	// If circuit breaker is disabled, do nothing
	if !cb.enabled {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.concurrentInstances > 0 {
		cb.concurrentInstances--
	}

	// Add failure record
	failure := FailureRecord{
		Timestamp: time.Now(),
		Error:     err.Error(),
		NodeClass: nodeClass,
		Region:    region,
	}
	cb.failures = append(cb.failures, failure)

	// Clean old failures outside the window
	cb.cleanOldFailures()

	// Check if we should open the circuit
	recentFailures := cb.countRecentFailures()

	cb.logger.Info("Provisioning failure recorded",
		"nodeClass", nodeClass,
		"region", region,
		"error", err.Error(),
		"recentFailures", recentFailures,
		"threshold", cb.config.FailureThreshold,
		"state", cb.state)

	if recentFailures >= cb.config.FailureThreshold {
		if cb.state != CircuitBreakerOpen {
			cb.transitionToOpen()
			// Include recent failure details for better debugging
			recentErrors := make([]string, 0, len(cb.failures))
			for _, f := range cb.failures {
				if f.Timestamp.After(time.Now().Add(-cb.config.FailureWindow)) {
					recentErrors = append(recentErrors, fmt.Sprintf("%s: %s", f.Timestamp.Format("15:04:05"), f.Error))
				}
			}
			cb.logger.Error(fmt.Errorf("circuit breaker opened due to failure threshold exceeded"), "Circuit breaker OPENED",
				"failures", recentFailures,
				"threshold", cb.config.FailureThreshold,
				"nodeClass", nodeClass,
				"region", region,
				"recentErrors", recentErrors,
				"recoveryTimeout", cb.config.RecoveryTimeout)
		}
	} else if cb.state == CircuitBreakerHalfOpen {
		// Failure in half-open state - go back to open
		cb.transitionToOpen()
		cb.logger.Info("Circuit breaker returned to OPEN due to failure in HALF_OPEN state")
	}
}

// GetState returns the current circuit breaker state and statistics
func (cb *CircuitBreaker) GetState() (*CircuitBreakerStatus, error) {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return &CircuitBreakerStatus{
		State:               cb.state,
		RecentFailures:      cb.countRecentFailures(),
		FailureThreshold:    cb.config.FailureThreshold,
		InstancesThisMinute: cb.instancesThisMinute,
		RateLimit:           cb.config.RateLimitPerMinute,
		ConcurrentInstances: cb.concurrentInstances,
		MaxConcurrent:       cb.config.MaxConcurrentInstances,
		LastStateChange:     cb.lastStateChange,
		TimeToRecovery:      cb.getTimeToRecovery(),
	}, nil
}

// Helper methods

func (cb *CircuitBreaker) resetCountersIfNeeded() {
	now := time.Now()

	// Reset minute counter
	if now.Sub(cb.lastMinuteReset) >= time.Minute {
		cb.instancesThisMinute = 0
		cb.lastMinuteReset = now
	}
}

func (cb *CircuitBreaker) transitionToClosed() {
	cb.state = CircuitBreakerClosed
	cb.lastStateChange = time.Now()
	cb.halfOpenRequests = 0
	// Keep failures for historical analysis but don't clear them
}

func (cb *CircuitBreaker) transitionToOpen() {
	cb.state = CircuitBreakerOpen
	cb.lastStateChange = time.Now()
	cb.halfOpenRequests = 0
}

func (cb *CircuitBreaker) transitionToHalfOpen() {
	cb.state = CircuitBreakerHalfOpen
	cb.lastStateChange = time.Now()
	cb.halfOpenRequests = 0
}

func (cb *CircuitBreaker) cleanOldFailures() {
	cutoff := time.Now().Add(-cb.config.FailureWindow)
	validFailures := make([]FailureRecord, 0, len(cb.failures))

	for _, failure := range cb.failures {
		if failure.Timestamp.After(cutoff) {
			validFailures = append(validFailures, failure)
		}
	}

	cb.failures = validFailures
}

func (cb *CircuitBreaker) countRecentFailures() int {
	cutoff := time.Now().Add(-cb.config.FailureWindow)
	count := 0
	for _, failure := range cb.failures {
		if failure.Timestamp.After(cutoff) {
			count++
		}
	}
	return count
}

func (cb *CircuitBreaker) getTimeToRecovery() time.Duration {
	if cb.state != CircuitBreakerOpen {
		return 0
	}
	elapsed := time.Since(cb.lastStateChange)
	if elapsed >= cb.config.RecoveryTimeout {
		return 0
	}
	return cb.config.RecoveryTimeout - elapsed
}

// getRecentFailuresSummary returns a summary of recent failures for better troubleshooting
func (cb *CircuitBreaker) getRecentFailuresSummary() string {
	cutoff := time.Now().Add(-cb.config.FailureWindow)
	var recentErrors []string

	// Group similar errors and show the most recent ones with actual details
	errorGroups := make(map[string][]FailureRecord)

	for _, failure := range cb.failures {
		if failure.Timestamp.After(cutoff) {
			// Create a simplified error key for grouping
			errorKey := cb.simplifyError(failure.Error)
			errorGroups[errorKey] = append(errorGroups[errorKey], failure)
		}
	}

	// Build summary with counts and actual error details
	for errorKey, failures := range errorGroups {
		count := len(failures)
		// Get the most recent failure for this group
		mostRecent := failures[0]
		for _, f := range failures[1:] {
			if f.Timestamp.After(mostRecent.Timestamp) {
				mostRecent = f
			}
		}

		// Extract the actual error details from the most recent failure
		// Look for the part after "creating VPC instance failed:" to get the root cause
		actualError := mostRecent.Error
		if idx := strings.Index(actualError, "creating VPC instance failed:"); idx >= 0 {
			// Extract just the error details after the prefix
			details := strings.TrimSpace(actualError[idx+len("creating VPC instance failed:"):])
			// Limit length but keep the important details
			if len(details) > 150 {
				details = details[:147] + "..."
			}
			if count == 1 {
				recentErrors = append(recentErrors, fmt.Sprintf("%s: %s", errorKey, details))
			} else {
				recentErrors = append(recentErrors, fmt.Sprintf("%s (x%d): %s", errorKey, count, details))
			}
		} else {
			// For non-VPC errors, include more of the actual error message
			if count == 1 {
				recentErrors = append(recentErrors, errorKey)
			} else {
				recentErrors = append(recentErrors, fmt.Sprintf("%s (x%d)", errorKey, count))
			}
		}
	}

	// Limit to 3 most common/recent errors to keep message readable
	if len(recentErrors) > 3 {
		recentErrors = recentErrors[:3]
		recentErrors = append(recentErrors, "...")
	}

	if len(recentErrors) == 0 {
		return ""
	}

	return fmt.Sprintf("[%s]", fmt.Sprintf("%v", recentErrors))
}

// simplifyError creates a simplified version of error messages for grouping
func (cb *CircuitBreaker) simplifyError(fullError string) string {
	// Extract key error patterns for grouping
	if strings.Contains(fullError, "timeout") || strings.Contains(fullError, "Timeout") {
		return "API timeout"
	}
	if strings.Contains(fullError, "subnet") && strings.Contains(fullError, "not found") {
		return "Subnet not found"
	}
	if strings.Contains(fullError, "security group") && strings.Contains(fullError, "not found") {
		return "Security group not found"
	}
	if strings.Contains(fullError, "image") && strings.Contains(fullError, "not found") {
		return "Image not found"
	}
	if (strings.Contains(fullError, "quota") && strings.Contains(fullError, "exceeded")) ||
		(strings.Contains(fullError, "limit") && (strings.Contains(fullError, "reached") || strings.Contains(fullError, "exceeded"))) {
		return "Resource quota/limit exceeded"
	}
	if strings.Contains(fullError, "unauthorized") || strings.Contains(fullError, "Unauthorized") {
		return "Unauthorized/authentication error"
	}
	if strings.Contains(fullError, "insufficient") {
		return "Insufficient capacity"
	}
	if (strings.Contains(fullError, "invalid") || strings.Contains(fullError, "Invalid")) &&
		(strings.Contains(fullError, "parameter") || strings.Contains(fullError, "configuration") || strings.Contains(fullError, "request")) {
		return "Invalid configuration"
	}
	if strings.Contains(fullError, "network") || strings.Contains(fullError, "Network") {
		return "Network error"
	}

	// For other errors, try to extract the first meaningful part
	parts := strings.Split(fullError, ":")
	if len(parts) > 0 && len(parts[0]) < 50 {
		return strings.TrimSpace(parts[0])
	}

	// Fallback: truncate long messages
	if len(fullError) > 50 {
		return fullError[:47] + "..."
	}
	return fullError
}

// getTimestampFromExample extracts timestamp from error example string
func (cb *CircuitBreaker) getTimestampFromExample(example string) time.Time {
	// Simple heuristic - if we can't parse, return zero time
	// In practice, this is only used for comparison, so it's okay
	if idx := strings.LastIndex(example, "("); idx >= 0 {
		timeStr := strings.TrimSuffix(example[idx+1:], ")")
		if t, err := time.Parse("15:04:05", timeStr); err == nil {
			// Use today's date with the parsed time
			now := time.Now()
			return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location())
		}
	}
	return time.Time{}
}

// Error types

// CircuitBreakerError is returned when the circuit breaker blocks a request
type CircuitBreakerError struct {
	State      CircuitBreakerState
	Message    string
	TimeToWait time.Duration
}

func (e *CircuitBreakerError) Error() string {
	if e.TimeToWait > 0 {
		return fmt.Sprintf("circuit breaker %s: %s (retry in %v)", e.State, e.Message, e.TimeToWait)
	}
	return fmt.Sprintf("circuit breaker %s: %s", e.State, e.Message)
}

// RateLimitError is returned when rate limit is exceeded
type RateLimitError struct {
	Limit       int
	Current     int
	TimeToReset time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limit exceeded: %d/%d instances this minute (reset in %v)",
		e.Current, e.Limit, e.TimeToReset)
}

// ConcurrencyLimitError is returned when too many concurrent operations
type ConcurrencyLimitError struct {
	Limit   int
	Current int
}

func (e *ConcurrencyLimitError) Error() string {
	return fmt.Sprintf("concurrency limit exceeded: %d/%d concurrent instances", e.Current, e.Limit)
}

// CircuitBreakerStatus holds the current status of the circuit breaker
type CircuitBreakerStatus struct {
	State               CircuitBreakerState `json:"state"`
	RecentFailures      int                 `json:"recentFailures"`
	FailureThreshold    int                 `json:"failureThreshold"`
	InstancesThisMinute int                 `json:"instancesThisMinute"`
	RateLimit           int                 `json:"rateLimit"`
	ConcurrentInstances int                 `json:"concurrentInstances"`
	MaxConcurrent       int                 `json:"maxConcurrent"`
	LastStateChange     time.Time           `json:"lastStateChange"`
	TimeToRecovery      time.Duration       `json:"timeToRecovery"`
}
