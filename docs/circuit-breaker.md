# Circuit Breaker Protection

## Overview

The Karpenter IBM Cloud Provider includes a circuit breaker implementation to prevent scale-up storms and protect against cascading failures during node provisioning. The circuit breaker is configurable through Helm values, environment variables, and ConfigMaps.

## Problem Statement

In production environments, bootstrap failures or API issues can cause Karpenter to continuously create new instances that fail to join the cluster.

## Circuit Breaker Implementation

### Core Components

The circuit breaker is implemented in `/pkg/cloudprovider/circuitbreaker.go` and provides:

1. **State Management**: CLOSED → OPEN → HALF_OPEN transitions
2. **Rate Limiting**: Maximum instances per minute
3. **Concurrency Control**: Maximum concurrent provisioning operations
4. **Failure Detection**: Configurable failure thresholds
5. **Automatic Recovery**: Time-based recovery with testing

### Configuration

```go
type CircuitBreakerConfig struct {
    FailureThreshold       int           // 3 consecutive failures
    FailureWindow          time.Duration // Within 5 minutes
    RecoveryTimeout        time.Duration // Wait 15 minutes before retry
    HalfOpenMaxRequests    int           // Allow 2 test requests
    RateLimitPerMinute     int           // Max 2 instances/minute
    MaxConcurrentInstances int           // Max 5 concurrent provisions
}
```

### Default Configuration

```go
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
```

## Configuration Options

### Helm Values

Configure the circuit breaker in your `values.yaml`:

```yaml
circuitBreaker:
  enabled: true

  # Use a preset configuration
  preset: "balanced"  # Options: conservative, balanced, aggressive, demo, custom

  # Custom configuration (when preset: "custom")
  config:
    failureThreshold: 3
    failureWindow: "5m"
    recoveryTimeout: "15m"
    halfOpenMaxRequests: 2
    rateLimitPerMinute: 10
    maxConcurrentInstances: 5
```

### Environment Variables

Configure using environment variables:

```bash
export CIRCUIT_BREAKER_ENABLED=true
export CIRCUIT_BREAKER_FAILURE_THRESHOLD=3
export CIRCUIT_BREAKER_FAILURE_WINDOW=5m
export CIRCUIT_BREAKER_RECOVERY_TIMEOUT=15m
export CIRCUIT_BREAKER_HALF_OPEN_MAX_REQUESTS=2
export CIRCUIT_BREAKER_RATE_LIMIT_PER_MINUTE=10
export CIRCUIT_BREAKER_MAX_CONCURRENT_INSTANCES=5
```

### Available Presets

#### Conservative (Production)
```yaml
circuitBreaker:
  preset: "conservative"
```
- Failure Threshold: 2
- Rate Limit: 2/minute
- Recovery: 20 minutes
- Best for production with strict SLAs

#### Balanced (Default)
```yaml
circuitBreaker:
  preset: "balanced"
```
- Failure Threshold: 3
- Rate Limit: 5/minute
- Recovery: 15 minutes
- Good balance for most workloads

#### Aggressive
```yaml
circuitBreaker:
  preset: "aggressive"
```
- Failure Threshold: 5
- Rate Limit: 10/minute
- Recovery: 5 minutes
- Higher throughput for development

#### Demo
```yaml
circuitBreaker:
  preset: "demo"
```
- Failure Threshold: 10
- Rate Limit: 20/minute
- Recovery: 1 minute
- Optimized for demonstrations

## Circuit Breaker States

### CLOSED (Normal Operation)
- All provisioning requests allowed
- Failures are tracked but don't block requests
- Rate limiting and concurrency limits still apply

### OPEN (Failing Fast)
- All provisioning requests blocked
- Returns `CircuitBreakerError` immediately
- Automatically transitions to HALF_OPEN after `RecoveryTimeout`

### HALF_OPEN (Testing Recovery)
- Limited number of test requests allowed (`HalfOpenMaxRequests`)
- Success → transitions to CLOSED
- Failure → transitions back to OPEN

## Protection Mechanisms

### 1. Rate Limiting
```go
// Prevents more than N instances per minute
if cb.instancesThisMinute >= cb.config.RateLimitPerMinute {
    return &RateLimitError{
        Limit:       cb.config.RateLimitPerMinute,
        Current:     cb.instancesThisMinute,
        TimeToReset: time.Minute - time.Since(cb.lastMinuteReset),
    }
}
```

### 2. Concurrency Control
```go
// Prevents too many simultaneous provisioning operations
if cb.concurrentInstances >= cb.config.MaxConcurrentInstances {
    return &ConcurrencyLimitError{
        Limit:   cb.config.MaxConcurrentInstances,
        Current: cb.concurrentInstances,
    }
}
```

### 3. Failure Detection
```go
// Opens circuit after threshold failures within window
if recentFailures >= cb.config.FailureThreshold {
    cb.transitionToOpen()
}
```

## Integration with CloudProvider

The circuit breaker is integrated into the main provisioning flow:

```go
// Check circuit breaker before provisioning
if err := c.circuitBreaker.CanProvision(ctx, nodeClass.Name, nodeClass.Spec.Region, 0); err != nil {
    return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("circuit breaker blocked provisioning: %w", err))
}

// Record success/failure after provisioning
if err != nil {
    c.circuitBreaker.RecordFailure(nodeClass.Name, nodeClass.Spec.Region, err)
} else {
    c.circuitBreaker.RecordSuccess(nodeClass.Name, nodeClass.Spec.Region)
}
```

## Error Types

### CircuitBreakerError
```go
type CircuitBreakerError struct {
    State      CircuitBreakerState
    Message    string
    TimeToWait time.Duration
}
```

### RateLimitError
```go
type RateLimitError struct {
    Limit       int
    Current     int
    TimeToReset time.Duration
}
```

### ConcurrencyLimitError
```go
type ConcurrencyLimitError struct {
    Limit   int
    Current int
}
```

## Monitoring and Observability

### Circuit Breaker Status
```go
type CircuitBreakerStatus struct {
    State               CircuitBreakerState
    RecentFailures      int
    FailureThreshold    int
    InstancesThisMinute int
    RateLimit           int
    ConcurrentInstances int
    MaxConcurrent       int
    LastStateChange     time.Time
    TimeToRecovery      time.Duration
}
```

### Logging
The circuit breaker provides logging for:
- State transitions
- Provisioning attempts (allowed/blocked)
- Success/failure recording
- Rate limit and concurrency violations
