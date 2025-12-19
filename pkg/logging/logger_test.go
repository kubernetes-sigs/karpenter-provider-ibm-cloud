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

package logging

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewLogger(t *testing.T) {
	logger := NewLogger("test-component")
	assert.NotNil(t, logger)
	assert.Equal(t, "test-component", logger.GetComponent())
}

func TestFromContext(t *testing.T) {
	ctx := context.Background()
	logger := FromContext(ctx, "test-component")
	assert.NotNil(t, logger)
	assert.Equal(t, "test-component", logger.GetComponent())
}

func TestWithName(t *testing.T) {
	logger := NewLogger("base")
	childLogger := logger.WithName("child")
	assert.Equal(t, "base.child", childLogger.GetComponent())
}

func TestWithValues(t *testing.T) {
	logger := NewLogger("test")
	loggerWithValues := logger.WithValues("key", "value")
	assert.NotNil(t, loggerWithValues)
	assert.Equal(t, "test", loggerWithValues.GetComponent())
}

func TestComponentLoggers(t *testing.T) {
	instanceLogger := InstanceLogger()
	assert.NotNil(t, instanceLogger)
	assert.Contains(t, instanceLogger.GetComponent(), "instance")

	pricingLogger := PricingLogger()
	assert.NotNil(t, pricingLogger)
	assert.Contains(t, pricingLogger.GetComponent(), "pricing")

	subnetLogger := SubnetLogger()
	assert.NotNil(t, subnetLogger)
	assert.Contains(t, subnetLogger.GetComponent(), "subnet")
}

func TestLoggingMethods(t *testing.T) {
	// Save original LOG_LEVEL
	originalLogLevel := os.Getenv("LOG_LEVEL")
	defer func() {
		_ = os.Setenv("LOG_LEVEL", originalLogLevel)
	}()

	// Test with debug level (should log everything)
	_ = os.Setenv("LOG_LEVEL", "debug")
	logger := NewLogger("test")

	// These should not panic and should execute the logging branch
	logger.Info("test info message", "key", "value")
	logger.Debug("test debug message", "key", "value") // This tests the shouldLog("debug") == true branch
	logger.Warn("test warning message", "key", "value")
	logger.Error(nil, "test error message", "key", "value")

	// Test with error level (should not log debug)
	_ = os.Setenv("LOG_LEVEL", "error")
	errorLogger := NewLogger("test-error")
	errorLogger.Debug("test debug message", "key", "value") // This tests the shouldLog("debug") == false branch

	// Test Warn with different log levels
	_ = os.Setenv("LOG_LEVEL", "warn")
	warnLogger := NewLogger("test-warn")
	warnLogger.Warn("test warning message", "key", "value") // shouldLog("warn") == true

	_ = os.Setenv("LOG_LEVEL", "error")
	errorLogger2 := NewLogger("test-error2")
	errorLogger2.Warn("test warning message", "key", "value") // shouldLog("warn") == false
}

func TestLogLevelFiltering(t *testing.T) {
	// Save original LOG_LEVEL
	originalLogLevel := os.Getenv("LOG_LEVEL")
	defer func() {
		_ = os.Setenv("LOG_LEVEL", originalLogLevel)
	}()

	tests := []struct {
		logLevel       string
		shouldLogDebug bool
		shouldLogInfo  bool
		shouldLogWarn  bool
		shouldLogError bool
	}{
		{"debug", true, true, true, true},
		{"info", false, true, true, true},
		{"warn", false, false, true, true},
		{"error", false, false, false, true},
		{"", false, true, true, true},        // default to info
		{"invalid", false, true, true, true}, // fallback to info
	}

	for _, tt := range tests {
		t.Run("log_level_"+tt.logLevel, func(t *testing.T) {
			_ = os.Setenv("LOG_LEVEL", tt.logLevel)
			logger := NewLogger("test")

			assert.Equal(t, tt.shouldLogDebug, logger.shouldLog("debug"))
			assert.Equal(t, tt.shouldLogInfo, logger.shouldLog("info"))
			assert.Equal(t, tt.shouldLogWarn, logger.shouldLog("warn"))
			assert.Equal(t, tt.shouldLogError, logger.shouldLog("error"))
		})
	}
}

func TestGetLogLevel(t *testing.T) {
	// Save original LOG_LEVEL
	originalLogLevel := os.Getenv("LOG_LEVEL")
	defer func() {
		_ = os.Setenv("LOG_LEVEL", originalLogLevel)
	}()

	tests := []struct {
		envValue string
		expected string
	}{
		{"debug", "debug"},
		{"DEBUG", "debug"}, // should be lowercase
		{"info", "info"},
		{"INFO", "info"},
		{"warn", "warn"},
		{"WARN", "warn"},
		{"error", "error"},
		{"ERROR", "error"},
		{"", "info"}, // default
	}

	for _, tt := range tests {
		t.Run("env_"+tt.envValue, func(t *testing.T) {
			_ = os.Setenv("LOG_LEVEL", tt.envValue)
			assert.Equal(t, tt.expected, getLogLevel())
		})
	}
}

func TestShouldLogMethodBoundaries(t *testing.T) {
	// Save original LOG_LEVEL
	originalLogLevel := os.Getenv("LOG_LEVEL")
	defer func() {
		_ = os.Setenv("LOG_LEVEL", originalLogLevel)
	}()

	_ = os.Setenv("LOG_LEVEL", "warn")
	logger := NewLogger("test")

	// Test boundary conditions
	assert.False(t, logger.shouldLog("debug"))
	assert.False(t, logger.shouldLog("info"))
	assert.True(t, logger.shouldLog("warn"))
	assert.True(t, logger.shouldLog("error"))

	// Test invalid message levels (should fallback to info)
	assert.False(t, logger.shouldLog("invalid"))
	assert.False(t, logger.shouldLog(""))
}

func TestLoggerPreservesLogLevelInChaining(t *testing.T) {
	// Save original LOG_LEVEL
	originalLogLevel := os.Getenv("LOG_LEVEL")
	defer func() {
		_ = os.Setenv("LOG_LEVEL", originalLogLevel)
	}()

	_ = os.Setenv("LOG_LEVEL", "error")
	baseLogger := NewLogger("base")

	// Verify base logger has correct log level
	assert.Equal(t, "error", baseLogger.logLevel)

	// Test WithName preserves log level
	namedLogger := baseLogger.WithName("child")
	assert.Equal(t, "error", namedLogger.logLevel)
	assert.Equal(t, "base.child", namedLogger.GetComponent())

	// Test WithValues preserves log level
	valuedLogger := baseLogger.WithValues("key", "value")
	assert.Equal(t, "error", valuedLogger.logLevel)
	assert.Equal(t, "base", valuedLogger.GetComponent())
}
