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
	"flag"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coreoptions "sigs.k8s.io/karpenter/pkg/operator/options"
)

func TestNewOptions(t *testing.T) {
	// Clear environment variables first
	_ = os.Unsetenv("IBMCLOUD_API_KEY")
	_ = os.Unsetenv("IBMCLOUD_REGION")
	_ = os.Unsetenv("IBMCLOUD_ZONE")
	_ = os.Unsetenv("IBMCLOUD_RESOURCE_GROUP_ID")
	_ = os.Unsetenv("CIRCUIT_BREAKER_ENABLED")
	_ = os.Unsetenv("CIRCUIT_BREAKER_RATE_LIMIT_PER_MINUTE")

	opts := NewOptions()

	assert.True(t, opts.Interruption) // Default should be true
	assert.Empty(t, opts.APIKey)
	assert.Empty(t, opts.Region)
	assert.Empty(t, opts.Zone)
	assert.Empty(t, opts.ResourceGroupID)
	
	// Check circuit breaker defaults
	assert.True(t, opts.CircuitBreakerEnabled)
	assert.Equal(t, 3, opts.CircuitBreakerFailureThreshold)
	assert.Equal(t, 5*time.Minute, opts.CircuitBreakerFailureWindow)
	assert.Equal(t, 15*time.Minute, opts.CircuitBreakerRecoveryTimeout)
	assert.Equal(t, 2, opts.CircuitBreakerHalfOpenMaxRequests)
	assert.Equal(t, 10, opts.CircuitBreakerRateLimitPerMinute)
	assert.Equal(t, 5, opts.CircuitBreakerMaxConcurrentInstances)
}

func TestNewOptionsWithEnvironment(t *testing.T) {
	// Set environment variables
	_ = os.Setenv("IBMCLOUD_API_KEY", "test-api-key")
	_ = os.Setenv("IBMCLOUD_REGION", "us-south")
	_ = os.Setenv("IBMCLOUD_ZONE", "us-south-1")
	_ = os.Setenv("IBMCLOUD_RESOURCE_GROUP_ID", "test-rg-id")

	defer func() {
		_ = os.Unsetenv("IBMCLOUD_API_KEY")
		_ = os.Unsetenv("IBMCLOUD_REGION")
		_ = os.Unsetenv("IBMCLOUD_ZONE")
		_ = os.Unsetenv("IBMCLOUD_RESOURCE_GROUP_ID")
	}()

	opts := NewOptions()

	assert.True(t, opts.Interruption)
	assert.Equal(t, "test-api-key", opts.APIKey)
	assert.Equal(t, "us-south", opts.Region)
	assert.Equal(t, "us-south-1", opts.Zone)
	assert.Equal(t, "test-rg-id", opts.ResourceGroupID)
}

func TestOptionsAddFlags(t *testing.T) {
	opts := &Options{}
	flagSet := &coreoptions.FlagSet{FlagSet: flag.NewFlagSet("test", flag.ContinueOnError)}

	opts.AddFlags(flagSet)

	// Test that flags are added (we can't easily test the actual flag parsing without more setup)
	assert.NotNil(t, flagSet.FlagSet)
}

func TestOptionsToContext(t *testing.T) {
	opts := &Options{
		Interruption:    true,
		APIKey:          "test-key",
		Region:          "us-south",
		Zone:            "us-south-1",
		ResourceGroupID: "test-rg",
	}

	ctx := context.Background()
	ctxWithOpts := opts.ToContext(ctx)

	retrievedOpts := FromContext(ctxWithOpts)
	require.NotNil(t, retrievedOpts)
	assert.Equal(t, opts.Interruption, retrievedOpts.Interruption)
	assert.Equal(t, opts.APIKey, retrievedOpts.APIKey)
	assert.Equal(t, opts.Region, retrievedOpts.Region)
	assert.Equal(t, opts.Zone, retrievedOpts.Zone)
	assert.Equal(t, opts.ResourceGroupID, retrievedOpts.ResourceGroupID)
}

func TestToContext(t *testing.T) {
	opts := &Options{
		Interruption:    false,
		APIKey:          "test-key-2",
		Region:          "eu-gb",
		Zone:            "eu-gb-1",
		ResourceGroupID: "test-rg-2",
	}

	ctx := context.Background()
	ctxWithOpts := ToContext(ctx, opts)

	retrievedOpts := FromContext(ctxWithOpts)
	require.NotNil(t, retrievedOpts)
	assert.Equal(t, opts, retrievedOpts)
}

func TestWithOptions(t *testing.T) {
	opts := Options{
		Interruption:    true,
		APIKey:          "test-key-3",
		Region:          "jp-tok",
		Zone:            "jp-tok-1",
		ResourceGroupID: "test-rg-3",
	}

	ctx := context.Background()
	ctxWithOpts := WithOptions(ctx, opts)

	retrievedOpts := FromContext(ctxWithOpts)
	require.NotNil(t, retrievedOpts)
	assert.Equal(t, opts.Interruption, retrievedOpts.Interruption)
	assert.Equal(t, opts.APIKey, retrievedOpts.APIKey)
	assert.Equal(t, opts.Region, retrievedOpts.Region)
	assert.Equal(t, opts.Zone, retrievedOpts.Zone)
	assert.Equal(t, opts.ResourceGroupID, retrievedOpts.ResourceGroupID)
}

func TestFromContextEmpty(t *testing.T) {
	ctx := context.Background()
	opts := FromContext(ctx)

	// Should return zero value, not nil
	require.NotNil(t, opts)
	assert.False(t, opts.Interruption)
	assert.Empty(t, opts.APIKey)
	assert.Empty(t, opts.Region)
	assert.Empty(t, opts.Zone)
	assert.Empty(t, opts.ResourceGroupID)
}

func TestFromContextWithNilValue(t *testing.T) {
	ctx := context.WithValue(context.Background(), optionsKey{}, nil)
	opts := FromContext(ctx)

	// Should return zero value when context value is nil
	require.NotNil(t, opts)
	assert.False(t, opts.Interruption)
	assert.Empty(t, opts.APIKey)
}

func TestOptionsValidation(t *testing.T) {
	tests := []struct {
		name        string
		opts        Options
		expectError bool
	}{
		{
			name: "valid options",
			opts: Options{
				APIKey:          "test-key",
				Region:          "us-south",
				Zone:            "us-south-1",
				ResourceGroupID: "test-rg",
			},
			expectError: false,
		},
		{
			name: "missing API key",
			opts: Options{
				Region:          "us-south",
				Zone:            "us-south-1",
				ResourceGroupID: "test-rg",
			},
			expectError: true,
		},
		{
			name: "missing region",
			opts: Options{
				APIKey:          "test-key",
				Zone:            "us-south-1",
				ResourceGroupID: "test-rg",
			},
			expectError: true,
		},
		{
			name: "invalid zone for region",
			opts: Options{
				APIKey:          "test-key",
				Region:          "us-south",
				Zone:            "eu-gb-1", // Wrong zone for us-south
				ResourceGroupID: "test-rg",
			},
			expectError: true,
		},
		{
			name:        "empty options should be invalid",
			opts:        Options{},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOptionsValidateRequiredFields(t *testing.T) {
	tests := []struct {
		name        string
		opts        *Options
		expectError bool
		errorMsg    string
	}{
		{
			name: "all required fields present",
			opts: &Options{
				APIKey: "test-api-key",
				Region: "us-south",
			},
			expectError: false,
		},
		{
			name: "missing API key",
			opts: &Options{
				Region: "us-south",
			},
			expectError: true,
			errorMsg:    "missing required environment variables: [IBMCLOUD_API_KEY]",
		},
		{
			name: "missing region",
			opts: &Options{
				APIKey: "test-api-key",
			},
			expectError: true,
			errorMsg:    "missing required environment variables: [IBMCLOUD_REGION]",
		},
		{
			name: "zone and resource group ID are optional",
			opts: &Options{
				APIKey: "test-api-key",
				Region: "us-south",
				// Zone and ResourceGroupID are not set
			},
			expectError: false,
		},
		{
			name: "valid with zone but no resource group",
			opts: &Options{
				APIKey: "test-api-key",
				Region: "us-south",
				Zone:   "us-south-1",
			},
			expectError: false,
		},
		{
			name: "invalid zone for region",
			opts: &Options{
				APIKey: "test-api-key",
				Region: "us-south",
				Zone:   "eu-de-1", // Wrong zone for us-south region
			},
			expectError: true,
			errorMsg:    "zone eu-de-1 does not match region us-south",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestOptionsParse(t *testing.T) {
	// Set up valid environment for parsing - only required fields
	_ = os.Setenv("IBMCLOUD_API_KEY", "test-key")
	_ = os.Setenv("IBMCLOUD_REGION", "us-south")
	defer func() {
		_ = os.Unsetenv("IBMCLOUD_API_KEY")
		_ = os.Unsetenv("IBMCLOUD_REGION")
	}()

	opts := &Options{}
	flagSet := &coreoptions.FlagSet{FlagSet: flag.NewFlagSet("test", flag.ContinueOnError)}

	opts.AddFlags(flagSet)

	// Test parsing with no arguments (should use environment variables)
	err := opts.Parse(flagSet)
	assert.NoError(t, err)
	assert.Equal(t, "test-key", opts.APIKey)
	assert.Equal(t, "us-south", opts.Region)
}

func TestOptionsParseWithArgs(t *testing.T) {
	opts := &Options{}
	flagSet := &coreoptions.FlagSet{FlagSet: flag.NewFlagSet("test", flag.ContinueOnError)}

	opts.AddFlags(flagSet)

	// Test parsing with all required arguments
	args := []string{
		"--api-key", "test-key",
		"--region", "us-south",
		"--zone", "us-south-1",
		"--resource-group-id", "test-rg",
	}
	err := opts.Parse(flagSet, args...)
	assert.NoError(t, err)
	assert.Equal(t, "test-key", opts.APIKey)
	assert.Equal(t, "us-south", opts.Region)
	assert.Equal(t, "us-south-1", opts.Zone)
	assert.Equal(t, "test-rg", opts.ResourceGroupID)
}

func TestOptionsKey(t *testing.T) {
	// Test that optionsKey is a proper type for context keys
	key1 := optionsKey{}
	key2 := optionsKey{}

	assert.Equal(t, key1, key2)

	// Test that it can be used as a context key
	ctx := context.WithValue(context.Background(), key1, "test")
	value := ctx.Value(key2)
	assert.Equal(t, "test", value)
}

func TestOptionsFieldValidation(t *testing.T) {
	opts := &Options{
		Interruption:    true,
		APIKey:          "valid-key",
		Region:          "us-south",
		Zone:            "us-south-1",
		ResourceGroupID: "valid-rg-id",
	}

	// All fields should be accessible
	assert.True(t, opts.Interruption)
	assert.Equal(t, "valid-key", opts.APIKey)
	assert.Equal(t, "us-south", opts.Region)
	assert.Equal(t, "us-south-1", opts.Zone)
	assert.Equal(t, "valid-rg-id", opts.ResourceGroupID)
}

func TestOptionsDefaults(t *testing.T) {
	opts := &Options{}

	// Test default values
	assert.False(t, opts.Interruption) // Default should be false for zero value
	assert.Empty(t, opts.APIKey)
	assert.Empty(t, opts.Region)
	assert.Empty(t, opts.Zone)
	assert.Empty(t, opts.ResourceGroupID)
}

// Circuit Breaker specific tests

func TestCircuitBreakerConfigParsing(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		validate func(t *testing.T, opts *Options)
	}{
		{
			name: "default circuit breaker values",
			envVars: map[string]string{},
			validate: func(t *testing.T, opts *Options) {
				assert.True(t, opts.CircuitBreakerEnabled)
				assert.Equal(t, 3, opts.CircuitBreakerFailureThreshold)
				assert.Equal(t, 5*time.Minute, opts.CircuitBreakerFailureWindow)
				assert.Equal(t, 15*time.Minute, opts.CircuitBreakerRecoveryTimeout)
				assert.Equal(t, 2, opts.CircuitBreakerHalfOpenMaxRequests)
				assert.Equal(t, 10, opts.CircuitBreakerRateLimitPerMinute)
				assert.Equal(t, 5, opts.CircuitBreakerMaxConcurrentInstances)
			},
		},
		{
			name: "custom values from environment",
			envVars: map[string]string{
				"CIRCUIT_BREAKER_ENABLED":                  "false",
				"CIRCUIT_BREAKER_FAILURE_THRESHOLD":        "5",
				"CIRCUIT_BREAKER_FAILURE_WINDOW":           "10m",
				"CIRCUIT_BREAKER_RECOVERY_TIMEOUT":         "30m",
				"CIRCUIT_BREAKER_HALF_OPEN_MAX_REQUESTS":   "3",
				"CIRCUIT_BREAKER_RATE_LIMIT_PER_MINUTE":    "20",
				"CIRCUIT_BREAKER_MAX_CONCURRENT_INSTANCES": "10",
			},
			validate: func(t *testing.T, opts *Options) {
				assert.False(t, opts.CircuitBreakerEnabled)
				assert.Equal(t, 5, opts.CircuitBreakerFailureThreshold)
				assert.Equal(t, 10*time.Minute, opts.CircuitBreakerFailureWindow)
				assert.Equal(t, 30*time.Minute, opts.CircuitBreakerRecoveryTimeout)
				assert.Equal(t, 3, opts.CircuitBreakerHalfOpenMaxRequests)
				assert.Equal(t, 20, opts.CircuitBreakerRateLimitPerMinute)
				assert.Equal(t, 10, opts.CircuitBreakerMaxConcurrentInstances)
			},
		},
		{
			name: "invalid duration values use defaults",
			envVars: map[string]string{
				"CIRCUIT_BREAKER_FAILURE_WINDOW":   "invalid",
				"CIRCUIT_BREAKER_RECOVERY_TIMEOUT": "not-a-duration",
			},
			validate: func(t *testing.T, opts *Options) {
				assert.Equal(t, 5*time.Minute, opts.CircuitBreakerFailureWindow)
				assert.Equal(t, 15*time.Minute, opts.CircuitBreakerRecoveryTimeout)
			},
		},
		{
			name: "invalid integer values use defaults",
			envVars: map[string]string{
				"CIRCUIT_BREAKER_FAILURE_THRESHOLD":      "not-a-number",
				"CIRCUIT_BREAKER_RATE_LIMIT_PER_MINUTE": "-5",
			},
			validate: func(t *testing.T, opts *Options) {
				assert.Equal(t, 3, opts.CircuitBreakerFailureThreshold)
				assert.Equal(t, 10, opts.CircuitBreakerRateLimitPerMinute)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for k, v := range tt.envVars {
				_ = os.Setenv(k, v)
			}
			defer func() {
				// Clean up environment variables
				for k := range tt.envVars {
					_ = os.Unsetenv(k)
				}
			}()

			opts := &Options{}
			opts.parseCircuitBreakerConfig()
			tt.validate(t, opts)
		})
	}
}

func TestGetCircuitBreakerConfig(t *testing.T) {
	tests := []struct {
		name    string
		opts    *Options
		want    *CircuitBreakerConfig
		wantNil bool
	}{
		{
			name: "returns config when enabled",
			opts: &Options{
				CircuitBreakerEnabled:                true,
				CircuitBreakerFailureThreshold:       3,
				CircuitBreakerFailureWindow:          5 * time.Minute,
				CircuitBreakerRecoveryTimeout:        15 * time.Minute,
				CircuitBreakerHalfOpenMaxRequests:    2,
				CircuitBreakerRateLimitPerMinute:     10,
				CircuitBreakerMaxConcurrentInstances: 5,
			},
			want: &CircuitBreakerConfig{
				FailureThreshold:       3,
				FailureWindow:          5 * time.Minute,
				RecoveryTimeout:        15 * time.Minute,
				HalfOpenMaxRequests:    2,
				RateLimitPerMinute:     10,
				MaxConcurrentInstances: 5,
			},
		},
		{
			name: "returns nil when disabled",
			opts: &Options{
				CircuitBreakerEnabled: false,
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.opts.GetCircuitBreakerConfig()
			if tt.wantNil {
				assert.Nil(t, got)
			} else {
				assert.NotNil(t, got)
				assert.Equal(t, tt.want.FailureThreshold, got.FailureThreshold)
				assert.Equal(t, tt.want.FailureWindow, got.FailureWindow)
				assert.Equal(t, tt.want.RecoveryTimeout, got.RecoveryTimeout)
				assert.Equal(t, tt.want.HalfOpenMaxRequests, got.HalfOpenMaxRequests)
				assert.Equal(t, tt.want.RateLimitPerMinute, got.RateLimitPerMinute)
				assert.Equal(t, tt.want.MaxConcurrentInstances, got.MaxConcurrentInstances)
			}
		})
	}
}

func TestCircuitBreakerValidation(t *testing.T) {
	tests := []struct {
		name    string
		opts    *Options
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid circuit breaker configuration",
			opts: &Options{
				APIKey:                               "test-key",
				Region:                               "us-south",
				Zone:                                 "us-south-1",
				ResourceGroupID:                      "test-rg",
				CircuitBreakerEnabled:                true,
				CircuitBreakerFailureThreshold:       3,
				CircuitBreakerFailureWindow:          5 * time.Minute,
				CircuitBreakerRecoveryTimeout:        15 * time.Minute,
				CircuitBreakerHalfOpenMaxRequests:    2,
				CircuitBreakerRateLimitPerMinute:     10,
				CircuitBreakerMaxConcurrentInstances: 5,
			},
			wantErr: false,
		},
		{
			name: "disabled circuit breaker skips validation",
			opts: &Options{
				APIKey:                          "test-key",
				Region:                          "us-south", 
				Zone:                            "us-south-1",
				ResourceGroupID:                 "test-rg",
				CircuitBreakerEnabled:           false,
				CircuitBreakerFailureThreshold:  0, // Invalid, but should be ignored
			},
			wantErr: false,
		},
		{
			name: "invalid failure threshold",
			opts: &Options{
				APIKey:                               "test-key",
				Region:                               "us-south",
				Zone:                                 "us-south-1", 
				ResourceGroupID:                      "test-rg",
				CircuitBreakerEnabled:                true,
				CircuitBreakerFailureThreshold:       0,
				CircuitBreakerFailureWindow:          5 * time.Minute,
				CircuitBreakerRecoveryTimeout:        15 * time.Minute,
				CircuitBreakerHalfOpenMaxRequests:    2,
				CircuitBreakerRateLimitPerMinute:     10,
				CircuitBreakerMaxConcurrentInstances: 5,
			},
			wantErr: true,
			errMsg:  "circuit breaker failure threshold must be at least 1",
		},
		{
			name: "invalid rate limit",
			opts: &Options{
				APIKey:                               "test-key",
				Region:                               "us-south",
				Zone:                                 "us-south-1",
				ResourceGroupID:                      "test-rg", 
				CircuitBreakerEnabled:                true,
				CircuitBreakerFailureThreshold:       3,
				CircuitBreakerFailureWindow:          5 * time.Minute,
				CircuitBreakerRecoveryTimeout:        15 * time.Minute,
				CircuitBreakerHalfOpenMaxRequests:    2,
				CircuitBreakerRateLimitPerMinute:     0,
				CircuitBreakerMaxConcurrentInstances: 5,
			},
			wantErr: true,
			errMsg:  "circuit breaker rate limit must be at least 1 per minute",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
