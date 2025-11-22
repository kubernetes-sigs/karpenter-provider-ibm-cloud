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

package ibm

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIBMError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *IBMError
		expected string
	}{
		{
			name: "error with code",
			err: &IBMError{
				Code:       "instance_not_found",
				StatusCode: 404,
				Message:    "Instance not found",
			},
			expected: "IBM Cloud error (code: instance_not_found, status: 404): Instance not found",
		},
		{
			name: "error without code",
			err: &IBMError{
				StatusCode: 500,
				Message:    "Internal server error",
			},
			expected: "IBM Cloud error (status: 500): Internal server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.err.Error())
		})
	}
}

func TestIBMError_TypeChecks(t *testing.T) {
	tests := []struct {
		name   string
		err    *IBMError
		checks map[string]bool
	}{
		{
			name: "not found error",
			err: &IBMError{
				Type:       ErrorTypeNotFound,
				StatusCode: http.StatusNotFound,
			},
			checks: map[string]bool{
				"IsNotFound":     true,
				"IsClientError":  true,
				"IsServerError":  false,
				"IsUnauthorized": false,
			},
		},
		{
			name: "server error",
			err: &IBMError{
				Type:       ErrorTypeServerError,
				StatusCode: http.StatusInternalServerError,
				Retryable:  true,
			},
			checks: map[string]bool{
				"IsServerError": true,
				"IsClientError": false,
				"Retryable":     true,
			},
		},
		{
			name: "rate limit error",
			err: &IBMError{
				Type:       ErrorTypeRateLimit,
				StatusCode: http.StatusTooManyRequests,
				RetryAfter: 60,
				Retryable:  true,
			},
			checks: map[string]bool{
				"IsRateLimit":   true,
				"IsClientError": true,
				"Retryable":     true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if val, ok := tt.checks["IsNotFound"]; ok {
				assert.Equal(t, val, tt.err.IsNotFound())
			}
			if val, ok := tt.checks["IsServerError"]; ok {
				assert.Equal(t, val, tt.err.IsServerError())
			}
			if val, ok := tt.checks["IsClientError"]; ok {
				assert.Equal(t, val, tt.err.IsClientError())
			}
			if val, ok := tt.checks["IsRateLimit"]; ok {
				assert.Equal(t, val, tt.err.IsRateLimit())
			}
			if val, ok := tt.checks["IsUnauthorized"]; ok {
				assert.Equal(t, val, tt.err.IsUnauthorized())
			}
			if val, ok := tt.checks["Retryable"]; ok {
				assert.Equal(t, val, tt.err.Retryable)
			}
		})
	}
}

func TestParseError_StringPatterns(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		expectedType ErrorType
		expectedCode int
		retryable    bool
	}{
		{
			name:         "not found error",
			err:          errors.New("instance not found"),
			expectedType: ErrorTypeNotFound,
			expectedCode: http.StatusNotFound,
			retryable:    false,
		},
		{
			name:         "not_found error",
			err:          errors.New("resource_not_found"),
			expectedType: ErrorTypeNotFound,
			expectedCode: http.StatusNotFound,
			retryable:    false,
		},
		{
			name:         "404 error",
			err:          errors.New("Error: 404 - Resource not found"),
			expectedType: ErrorTypeNotFound,
			expectedCode: http.StatusNotFound,
			retryable:    false,
		},
		{
			name:         "unauthorized error",
			err:          errors.New("unauthorized access"),
			expectedType: ErrorTypeUnauthorized,
			expectedCode: http.StatusUnauthorized,
			retryable:    false,
		},
		{
			name:         "timeout error",
			err:          errors.New("request timeout"),
			expectedType: ErrorTypeTimeout,
			expectedCode: http.StatusRequestTimeout,
			retryable:    true,
		},
		{
			name:         "rate limit error",
			err:          errors.New("rate limit exceeded"),
			expectedType: ErrorTypeRateLimit,
			expectedCode: http.StatusTooManyRequests,
			retryable:    true,
		},
		{
			name:         "internal server error",
			err:          errors.New("internal server error occurred"),
			expectedType: ErrorTypeServerError,
			expectedCode: http.StatusInternalServerError,
			retryable:    true,
		},
		{
			name:         "500 error",
			err:          errors.New("Error 500: Server problem"),
			expectedType: ErrorTypeServerError,
			expectedCode: http.StatusInternalServerError,
			retryable:    true,
		},
		{
			name:         "validation error",
			err:          errors.New("validation failed for field 'name'"),
			expectedType: ErrorTypeValidation,
			expectedCode: http.StatusBadRequest,
			retryable:    false,
		},
		{
			name:         "conflict error",
			err:          errors.New("resource already exists"),
			expectedType: ErrorTypeConflict,
			expectedCode: http.StatusConflict,
			retryable:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ibmErr := ParseError(tt.err)
			assert.NotNil(t, ibmErr)
			assert.Equal(t, tt.expectedType, ibmErr.Type)
			assert.Equal(t, tt.expectedCode, ibmErr.StatusCode)
			assert.Equal(t, tt.retryable, ibmErr.Retryable)
			assert.Equal(t, tt.err.Error(), ibmErr.Message)
		})
	}
}

func TestParseError_SDKProblem(t *testing.T) {
	// core.SDKProblem requires complex initialization
	// and we can't create a valid instance without internal IBM SDK knowledge
	t.Skip("Skipping SDK problem test - requires internal SDK structures")

	// This test would require mocking internal SDK structures which is not
	// feasible without access to the internal implementation details
}

func TestParseError_AlreadyIBMError(t *testing.T) {
	originalErr := &IBMError{
		Type:       ErrorTypeNotFound,
		StatusCode: http.StatusNotFound,
		Code:       "test_not_found",
		Message:    "Test not found",
	}

	parsedErr := ParseError(originalErr)
	assert.Equal(t, originalErr, parsedErr)
}

func TestParseError_Nil(t *testing.T) {
	assert.Nil(t, ParseError(nil))
}

func TestHelperFunctions(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		checkFn  func(error) bool
		expected bool
	}{
		{
			name:     "IsNotFound with not found error",
			err:      errors.New("resource not found"),
			checkFn:  IsNotFound,
			expected: true,
		},
		{
			name:     "IsNotFound with other error",
			err:      errors.New("internal error"),
			checkFn:  IsNotFound,
			expected: false,
		},
		{
			name:     "IsRetryable with server error",
			err:      errors.New("500 internal server error"),
			checkFn:  IsRetryable,
			expected: true,
		},
		{
			name:     "IsRetryable with client error",
			err:      errors.New("400 bad request"),
			checkFn:  IsRetryable,
			expected: false,
		},
		{
			name:     "IsRateLimit with rate limit error",
			err:      errors.New("429 too many requests"),
			checkFn:  IsRateLimit,
			expected: true,
		},
		{
			name:     "IsTimeout with timeout error",
			err:      errors.New("request timeout occurred"),
			checkFn:  IsTimeout,
			expected: true,
		},
		{
			name:     "nil error",
			err:      nil,
			checkFn:  IsNotFound,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.checkFn(tt.err))
		})
	}
}

func TestIBMError_Unwrap(t *testing.T) {
	originalErr := errors.New("original error")
	ibmErr := &IBMError{
		Type:       ErrorTypeUnknown,
		StatusCode: 0,
		Message:    "wrapped error",
		wrapped:    originalErr,
	}

	assert.Equal(t, originalErr, ibmErr.Unwrap())
}

func TestParseError_ComplexPatterns(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		expectedType ErrorType
		expectedCode int
	}{
		{
			name:         "502 bad gateway",
			err:          errors.New("Error: 502 Bad Gateway"),
			expectedType: ErrorTypeServerError,
			expectedCode: http.StatusBadGateway,
		},
		{
			name:         "503 service unavailable",
			err:          errors.New("503 Service Unavailable"),
			expectedType: ErrorTypeServerError,
			expectedCode: http.StatusServiceUnavailable,
		},
		{
			name:         "504 gateway timeout",
			err:          errors.New("Gateway timeout: 504"),
			expectedType: ErrorTypeServerError,
			expectedCode: http.StatusGatewayTimeout,
		},
		{
			name:         "permission denied",
			err:          errors.New("permission denied to access resource"),
			expectedType: ErrorTypeForbidden,
			expectedCode: http.StatusForbidden,
		},
		{
			name:         "authentication failed",
			err:          errors.New("authentication failed"),
			expectedType: ErrorTypeUnauthorized,
			expectedCode: http.StatusUnauthorized,
		},
		{
			name:         "already exists",
			err:          errors.New("resource already exists"),
			expectedType: ErrorTypeConflict,
			expectedCode: http.StatusConflict,
		},
		{
			name:         "invalid request",
			err:          errors.New("invalid request parameters"),
			expectedType: ErrorTypeValidation,
			expectedCode: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ibmErr := ParseError(tt.err)
			assert.NotNil(t, ibmErr)
			assert.Equal(t, tt.expectedType, ibmErr.Type)
			assert.Equal(t, tt.expectedCode, ibmErr.StatusCode)
		})
	}
}
