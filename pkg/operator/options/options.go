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
package options

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
	"sigs.k8s.io/karpenter/pkg/utils/env"
)

func init() {
	coreoptions.Injectables = append(coreoptions.Injectables, &Options{})
}

// Key is the context key for Options
type optionsKey struct{}

// Options contains operator configuration
type Options struct {
	// Interruption enables the interruption controller
	Interruption bool

	// APIKey is the IBM Cloud API key used for authentication
	APIKey string
	// Region is the IBM Cloud region to operate in
	Region string
	// Zone is the availability zone within the region
	Zone string
	// ResourceGroupID is the ID of the resource group to use
	ResourceGroupID string

	// CircuitBreaker configuration
	CircuitBreakerEnabled            bool
	CircuitBreakerFailureThreshold   int
	CircuitBreakerFailureWindow      time.Duration
	CircuitBreakerRecoveryTimeout    time.Duration
	CircuitBreakerHalfOpenMaxRequests int
	CircuitBreakerRateLimitPerMinute int
	CircuitBreakerMaxConcurrentInstances int
}

// AddFlags adds command-line flags for the options
func (o *Options) AddFlags(fs *coreoptions.FlagSet) {
	fs.BoolVarWithEnv(&o.Interruption, "interruption", "INTERRUPTION", true, "Enable interruption controller")
	fs.StringVar(&o.APIKey, "api-key", env.WithDefaultString("IBMCLOUD_API_KEY", ""), "IBM Cloud API key")
	fs.StringVar(&o.Region, "region", env.WithDefaultString("IBMCLOUD_REGION", ""), "IBM Cloud region")
	fs.StringVar(&o.Zone, "zone", env.WithDefaultString("IBMCLOUD_ZONE", ""), "IBM Cloud availability zone")
	fs.StringVar(&o.ResourceGroupID, "resource-group-id", env.WithDefaultString("IBMCLOUD_RESOURCE_GROUP_ID", ""), "IBM Cloud resource group ID")
}

// Parse parses command-line flags
func (o *Options) Parse(fs *coreoptions.FlagSet, args ...string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		return fmt.Errorf("parsing flags, %w", err)
	}
	if err := o.Validate(); err != nil {
		return fmt.Errorf("validating options, %w", err)
	}
	return nil
}

// ToContext adds the options to the context
func (o *Options) ToContext(ctx context.Context) context.Context {
	return ToContext(ctx, o)
}

// FromContext retrieves Options from the context
func FromContext(ctx context.Context) *Options {
	if v := ctx.Value(optionsKey{}); v != nil {
		return v.(*Options)
	}
	// Return zero value instead of nil to prevent panics
	return &Options{}
}

// ToContext returns a new context with the given Options
func ToContext(ctx context.Context, options *Options) context.Context {
	return context.WithValue(ctx, optionsKey{}, options)
}

// WithOptions returns a new context with the given Options (for backwards compatibility)
func WithOptions(ctx context.Context, options Options) context.Context {
	return ToContext(ctx, &options)
}

// NewOptions creates a new Options instance with values from environment variables
func NewOptions() Options {
	options := Options{
		Interruption:    true, // Enable interruption controller by default
		APIKey:          os.Getenv("IBMCLOUD_API_KEY"),
		Region:          os.Getenv("IBMCLOUD_REGION"),
		Zone:            os.Getenv("IBMCLOUD_ZONE"),
		ResourceGroupID: os.Getenv("IBMCLOUD_RESOURCE_GROUP_ID"),
	}

	// Parse circuit breaker configuration from environment variables
	options.parseCircuitBreakerConfig()
	
	return options
}

// parseCircuitBreakerConfig reads circuit breaker configuration from environment variables
func (o *Options) parseCircuitBreakerConfig() {
	// Parse enabled state
	if enabled := os.Getenv("CIRCUIT_BREAKER_ENABLED"); enabled != "" {
		o.CircuitBreakerEnabled, _ = strconv.ParseBool(enabled)
	} else {
		o.CircuitBreakerEnabled = true // Default to enabled
	}

	// Parse failure threshold
	if threshold := os.Getenv("CIRCUIT_BREAKER_FAILURE_THRESHOLD"); threshold != "" {
		if val, err := strconv.Atoi(threshold); err == nil && val > 0 {
			o.CircuitBreakerFailureThreshold = val
		}
	}
	if o.CircuitBreakerFailureThreshold == 0 {
		o.CircuitBreakerFailureThreshold = 3 // Default
	}

	// Parse failure window
	if window := os.Getenv("CIRCUIT_BREAKER_FAILURE_WINDOW"); window != "" {
		if duration, err := time.ParseDuration(window); err == nil {
			o.CircuitBreakerFailureWindow = duration
		}
	}
	if o.CircuitBreakerFailureWindow == 0 {
		o.CircuitBreakerFailureWindow = 5 * time.Minute // Default
	}

	// Parse recovery timeout
	if timeout := os.Getenv("CIRCUIT_BREAKER_RECOVERY_TIMEOUT"); timeout != "" {
		if duration, err := time.ParseDuration(timeout); err == nil {
			o.CircuitBreakerRecoveryTimeout = duration
		}
	}
	if o.CircuitBreakerRecoveryTimeout == 0 {
		o.CircuitBreakerRecoveryTimeout = 15 * time.Minute // Default
	}

	// Parse half-open max requests
	if requests := os.Getenv("CIRCUIT_BREAKER_HALF_OPEN_MAX_REQUESTS"); requests != "" {
		if val, err := strconv.Atoi(requests); err == nil && val > 0 {
			o.CircuitBreakerHalfOpenMaxRequests = val
		}
	}
	if o.CircuitBreakerHalfOpenMaxRequests == 0 {
		o.CircuitBreakerHalfOpenMaxRequests = 2 // Default
	}

	// Parse rate limit per minute
	if rateLimit := os.Getenv("CIRCUIT_BREAKER_RATE_LIMIT_PER_MINUTE"); rateLimit != "" {
		if val, err := strconv.Atoi(rateLimit); err == nil && val > 0 {
			o.CircuitBreakerRateLimitPerMinute = val
		}
	}
	if o.CircuitBreakerRateLimitPerMinute == 0 {
		o.CircuitBreakerRateLimitPerMinute = 10 // Default (increased for demo)
	}

	// Parse max concurrent instances
	if maxConcurrent := os.Getenv("CIRCUIT_BREAKER_MAX_CONCURRENT_INSTANCES"); maxConcurrent != "" {
		if val, err := strconv.Atoi(maxConcurrent); err == nil && val > 0 {
			o.CircuitBreakerMaxConcurrentInstances = val
		}
	}
	if o.CircuitBreakerMaxConcurrentInstances == 0 {
		o.CircuitBreakerMaxConcurrentInstances = 5 // Default
	}
}

// GetCircuitBreakerConfig converts Options to a CircuitBreakerConfig
func (o *Options) GetCircuitBreakerConfig() *CircuitBreakerConfig {
	if !o.CircuitBreakerEnabled {
		return nil
	}
	
	return &CircuitBreakerConfig{
		FailureThreshold:       o.CircuitBreakerFailureThreshold,
		FailureWindow:          o.CircuitBreakerFailureWindow,
		RecoveryTimeout:        o.CircuitBreakerRecoveryTimeout,
		HalfOpenMaxRequests:    o.CircuitBreakerHalfOpenMaxRequests,
		RateLimitPerMinute:     o.CircuitBreakerRateLimitPerMinute,
		MaxConcurrentInstances: o.CircuitBreakerMaxConcurrentInstances,
	}
}

// CircuitBreakerConfig holds configuration for the circuit breaker
type CircuitBreakerConfig struct {
	FailureThreshold       int
	FailureWindow          time.Duration
	RecoveryTimeout        time.Duration
	HalfOpenMaxRequests    int
	RateLimitPerMinute     int
	MaxConcurrentInstances int
}

// Validate validates the options
func (o *Options) Validate() error {
	// First validate required IBM Cloud fields
	var missingFields []string

	if o.APIKey == "" {
		missingFields = append(missingFields, "IBMCLOUD_API_KEY")
	}
	if o.Region == "" {
		missingFields = append(missingFields, "IBMCLOUD_REGION")
	}

	if len(missingFields) > 0 {
		return fmt.Errorf("missing required environment variables: %v", missingFields)
	}

	// Validate region/zone pair only if zone is provided
	if o.Zone != "" {
		if err := validateRegionZonePair(o.Region, o.Zone); err != nil {
			return err
		}
	}

	// Validate circuit breaker configuration if enabled
	if o.CircuitBreakerEnabled {
		if o.CircuitBreakerFailureThreshold < 1 {
			return fmt.Errorf("circuit breaker failure threshold must be at least 1, got %d", o.CircuitBreakerFailureThreshold)
		}
		if o.CircuitBreakerFailureWindow <= 0 {
			return fmt.Errorf("circuit breaker failure window must be positive, got %v", o.CircuitBreakerFailureWindow)
		}
		if o.CircuitBreakerRecoveryTimeout <= 0 {
			return fmt.Errorf("circuit breaker recovery timeout must be positive, got %v", o.CircuitBreakerRecoveryTimeout)
		}
		if o.CircuitBreakerHalfOpenMaxRequests < 1 {
			return fmt.Errorf("circuit breaker half-open max requests must be at least 1, got %d", o.CircuitBreakerHalfOpenMaxRequests)
		}
		if o.CircuitBreakerRateLimitPerMinute < 1 {
			return fmt.Errorf("circuit breaker rate limit must be at least 1 per minute, got %d", o.CircuitBreakerRateLimitPerMinute)
		}
		if o.CircuitBreakerMaxConcurrentInstances < 1 {
			return fmt.Errorf("circuit breaker max concurrent instances must be at least 1, got %d", o.CircuitBreakerMaxConcurrentInstances)
		}
		
		// Warn if values seem unreasonably high
		logger := log.Log.WithName("options-validation")
		if o.CircuitBreakerRateLimitPerMinute > 100 {
			logger.V(1).Info("Circuit breaker rate limit seems very high for production use",
				"rateLimit", o.CircuitBreakerRateLimitPerMinute,
				"recommendation", "Consider using a lower value")
		}
		if o.CircuitBreakerMaxConcurrentInstances > 50 {
			logger.V(1).Info("Circuit breaker max concurrent instances seems very high",
				"maxConcurrent", o.CircuitBreakerMaxConcurrentInstances,
				"recommendation", "Consider using a lower value")
		}
	}
	
	return nil
}

// validateRegionZonePair ensures the zone follows the correct format for the given region
func validateRegionZonePair(region, zone string) error {
	// Validate zone format: should be region-N where N is a digit
	expectedPrefix := region + "-"
	if !strings.HasPrefix(zone, expectedPrefix) {
		return fmt.Errorf("zone %s does not match region %s: expected format %sN", zone, region, expectedPrefix)
	}
	
	// Extract and validate the zone number
	zoneSuffix := strings.TrimPrefix(zone, expectedPrefix)
	if len(zoneSuffix) != 1 {
		return fmt.Errorf("invalid zone format %s: expected single digit after %s", zone, expectedPrefix)
	}
	
	// Check if it's a digit
	if zoneSuffix[0] < '1' || zoneSuffix[0] > '9' {
		return fmt.Errorf("invalid zone number in %s: must be between 1-9", zone)
	}
	
	return nil
}
