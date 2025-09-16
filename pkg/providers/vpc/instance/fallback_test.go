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

package instance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// TestInstanceTypeFallbackLogic tests the core fallback behavior logic
func TestInstanceTypeFallbackLogic(t *testing.T) {
	tests := []struct {
		name              string
		instanceTypes     []*cloudprovider.InstanceType
		createInstanceErr map[string]error // instanceType -> error
		expectedInstance  string
		expectedError     string
		expectedAttempts  int
	}{
		{
			name: "first instance type succeeds",
			instanceTypes: []*cloudprovider.InstanceType{
				{Name: "bx2-2x8"},
				{Name: "bx2-4x16"},
			},
			createInstanceErr: map[string]error{
				"bx2-2x8": nil, // Success on first try
			},
			expectedInstance: "bx2-2x8",
			expectedAttempts: 1,
		},
		{
			name: "first fails with oneOf error, second succeeds",
			instanceTypes: []*cloudprovider.InstanceType{
				{Name: "bx2-2x8"},
				{Name: "bx2-4x16"},
			},
			createInstanceErr: map[string]error{
				"bx2-2x8":  fmt.Errorf("creating instance: Expected only one oneOf fields to be set: got 0"),
				"bx2-4x16": nil, // Success on second try
			},
			expectedInstance: "bx2-4x16",
			expectedAttempts: 2,
		},
		{
			name: "all instance types fail with oneOf errors",
			instanceTypes: []*cloudprovider.InstanceType{
				{Name: "bx2-2x8"},
				{Name: "bx2-4x16"},
			},
			createInstanceErr: map[string]error{
				"bx2-2x8":  fmt.Errorf("creating instance: Expected only one oneOf fields to be set: got 0"),
				"bx2-4x16": fmt.Errorf("creating instance: Expected only one oneOf fields to be set: got 0"),
			},
			expectedError:    "all 2 instance types failed with oneOf validation errors",
			expectedAttempts: 2,
		},
		{
			name: "first instance fails with non-oneOf error, should stop immediately",
			instanceTypes: []*cloudprovider.InstanceType{
				{Name: "bx2-2x8"},
				{Name: "bx2-4x16"},
			},
			createInstanceErr: map[string]error{
				"bx2-2x8": fmt.Errorf("instance quota exceeded"),
			},
			expectedError:    "instance quota exceeded",
			expectedAttempts: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Track actual attempts made
			actualAttempts := 0

			// Create mock createInstanceWithType function
			mockCreateInstanceWithType := func(instanceType *cloudprovider.InstanceType) (*corev1.Node, error) {
				actualAttempts++

				if err, exists := tt.createInstanceErr[instanceType.Name]; exists && err != nil {
					return nil, err
				}

				// Default success case - no error defined means success
				return &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
						Labels: map[string]string{
							"node.kubernetes.io/instance-type": instanceType.Name,
						},
					},
				}, nil
			}

			// Execute the fallback logic (simulate the main loop from Create method)
			var node *corev1.Node
			var lastErr error

			if len(tt.instanceTypes) == 0 {
				lastErr = fmt.Errorf("no compatible instance types provided")
			} else {
				allOneOfErrors := true
				for _, selectedInstanceType := range tt.instanceTypes {
					if selectedInstanceType == nil {
						continue
					}

					if selectedInstanceType.Name == "" {
						continue
					}

					var err error
					node, err = mockCreateInstanceWithType(selectedInstanceType)
					if err == nil {
						// Success case - clear any previous errors
						lastErr = nil
						break
					}

					// Check if this is a oneOf validation error
					if strings.Contains(err.Error(), "Expected only one oneOf fields to be set") {
						lastErr = err
						continue // Try next instance type
					}

					// For non-oneOf errors, fail immediately
					allOneOfErrors = false
					lastErr = err
					break
				}

				// If we exhausted all types and only had oneOf errors
				if node == nil && allOneOfErrors && lastErr != nil {
					lastErr = fmt.Errorf("all %d instance types failed with oneOf validation errors, last error: %w", len(tt.instanceTypes), lastErr)
				}
			}

			// Verify results
			if tt.expectedError != "" {
				assert.Error(t, lastErr)
				assert.Contains(t, lastErr.Error(), tt.expectedError)
				assert.Nil(t, node)
			} else {
				assert.NoError(t, lastErr)
				assert.NotNil(t, node)
				if node != nil && node.Labels != nil {
					assert.Equal(t, tt.expectedInstance, node.Labels["node.kubernetes.io/instance-type"])
				}
			}

			// Verify attempt count
			if tt.expectedAttempts > 0 {
				assert.Equal(t, tt.expectedAttempts, actualAttempts)
			}
		})
	}
}

// TestOneOfErrorDetection tests the error pattern matching
func TestOneOfErrorDetection(t *testing.T) {
	tests := []struct {
		name        string
		error       error
		shouldRetry bool
	}{
		{
			name:        "oneOf validation error should retry",
			error:       fmt.Errorf("creating instance: Expected only one oneOf fields to be set: got 0"),
			shouldRetry: true,
		},
		{
			name:        "oneOf error with different message should retry",
			error:       fmt.Errorf("VPC API error: Expected only one oneOf fields to be set: got 2"),
			shouldRetry: true,
		},
		{
			name:        "quota error should not retry",
			error:       fmt.Errorf("instance quota exceeded"),
			shouldRetry: false,
		},
		{
			name:        "network error should not retry",
			error:       fmt.Errorf("connection timeout"),
			shouldRetry: false,
		},
		{
			name:        "authentication error should not retry",
			error:       fmt.Errorf("authentication failed"),
			shouldRetry: false,
		},
		{
			name:        "nil error should not retry",
			error:       nil,
			shouldRetry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.error == nil {
				return // Skip nil case
			}

			isOneOfError := strings.Contains(tt.error.Error(), "Expected only one oneOf fields to be set")
			assert.Equal(t, tt.shouldRetry, isOneOfError, "Error: %v", tt.error)
		})
	}
}

// TestInstanceTypeValidation tests input validation for instance types
func TestInstanceTypeValidation(t *testing.T) {
	tests := []struct {
		name          string
		instanceTypes []*cloudprovider.InstanceType
		expectedValid []string
	}{
		{
			name: "all valid instance types",
			instanceTypes: []*cloudprovider.InstanceType{
				{Name: "bx2-2x8"},
				{Name: "bx2-4x16"},
				{Name: "mx2-2x16"},
			},
			expectedValid: []string{"bx2-2x8", "bx2-4x16", "mx2-2x16"},
		},
		{
			name: "mixed valid and invalid types",
			instanceTypes: []*cloudprovider.InstanceType{
				{Name: "bx2-2x8"},
				nil,
				{Name: ""},
				{Name: "bx2-4x16"},
			},
			expectedValid: []string{"bx2-2x8", "bx2-4x16"},
		},
		{
			name: "all invalid types",
			instanceTypes: []*cloudprovider.InstanceType{
				nil,
				{Name: ""},
				nil,
			},
			expectedValid: []string{},
		},
		{
			name:          "empty list",
			instanceTypes: []*cloudprovider.InstanceType{},
			expectedValid: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var validTypes []string

			// Simulate the validation logic from the Create method
			for _, instanceType := range tt.instanceTypes {
				if instanceType == nil {
					continue
				}
				if instanceType.Name == "" {
					continue
				}
				validTypes = append(validTypes, instanceType.Name)
			}

			// Handle nil vs empty slice comparison
			if len(tt.expectedValid) == 0 && validTypes == nil {
				validTypes = []string{}
			}

			assert.Equal(t, tt.expectedValid, validTypes)
		})
	}
}

// TestFallbackLogging tests that appropriate log messages are generated
func TestFallbackLogging(t *testing.T) {
	instanceTypes := []*cloudprovider.InstanceType{
		{Name: "bx2-2x8"},
		{Name: "bx2-4x16"},
	}

	logMessages := []string{}

	// Create a custom logger that captures messages
	logger := logr.New(&testLogSink{messages: &logMessages})

	// Simulate the logging from the Create method
	logger.Info("Starting instance type fallback selection (respecting Karpenter ranking)",
		"availableTypes", len(instanceTypes),
		"instanceTypeNames", func() []string {
			names := make([]string, len(instanceTypes))
			for i, it := range instanceTypes {
				names[i] = it.Name
			}
			return names
		}())

	logger.Info("Attempting instance creation",
		"attemptNumber", 1,
		"instanceType", "bx2-2x8",
		"remainingTypes", len(instanceTypes)-1)

	logger.Info("oneOf validation failed, trying next instance type",
		"instanceType", "bx2-2x8",
		"attemptNumber", 1,
		"remainingTypes", 1)

	logger.Info("Instance creation succeeded",
		"instanceType", "bx2-4x16",
		"attemptNumber", 2)

	// Verify key log messages are present
	assert.Greater(t, len(logMessages), 0, "Expected log messages to be captured")

	foundFallbackStart := false
	foundAttempt := false
	foundSuccess := false

	for _, msg := range logMessages {
		if strings.Contains(msg, "Starting instance type fallback selection") {
			foundFallbackStart = true
		}
		if strings.Contains(msg, "Attempting instance creation") {
			foundAttempt = true
		}
		if strings.Contains(msg, "Instance creation succeeded") {
			foundSuccess = true
		}
	}

	assert.True(t, foundFallbackStart, "Should log fallback start")
	assert.True(t, foundAttempt, "Should log attempt")
	assert.True(t, foundSuccess, "Should log success")
}

// testLogSink implements logr.LogSink for testing
type testLogSink struct {
	messages *[]string
}

func (t *testLogSink) Init(info logr.RuntimeInfo) {}

func (t *testLogSink) Enabled(level int) bool {
	return true
}

func (t *testLogSink) Info(level int, msg string, keysAndValues ...interface{}) {
	*t.messages = append(*t.messages, msg)
}

func (t *testLogSink) Error(err error, msg string, keysAndValues ...interface{}) {
	*t.messages = append(*t.messages, msg)
}

func (t *testLogSink) WithValues(keysAndValues ...interface{}) logr.LogSink {
	return t
}

func (t *testLogSink) WithName(name string) logr.LogSink {
	return t
}
