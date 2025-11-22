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
	"fmt"
	"net/http"
	"strings"

	"github.com/IBM/go-sdk-core/v5/core"
)

// ErrorType represents the type of IBM Cloud error
type ErrorType int

const (
	// ErrorTypeUnknown represents an unknown error type
	ErrorTypeUnknown ErrorType = iota
	// ErrorTypeNotFound represents a resource not found error
	ErrorTypeNotFound
	// ErrorTypeUnauthorized represents an authentication error
	ErrorTypeUnauthorized
	// ErrorTypeForbidden represents an authorization error
	ErrorTypeForbidden
	// ErrorTypeRateLimit represents a rate limit error
	ErrorTypeRateLimit
	// ErrorTypeServerError represents a server error
	ErrorTypeServerError
	// ErrorTypeClientError represents a client error
	ErrorTypeClientError
	// ErrorTypeTimeout represents a timeout error
	ErrorTypeTimeout
	// ErrorTypeConflict represents a resource conflict error
	ErrorTypeConflict
	// ErrorTypeValidation represents a validation error
	ErrorTypeValidation
)

// IBMError represents a structured IBM Cloud error
type IBMError struct {
	// Type is the error type
	Type ErrorType
	// StatusCode is the HTTP status code
	StatusCode int
	// Code is the IBM Cloud error code
	Code string
	// Message is the error message
	Message string
	// MoreInfo provides additional information about the error
	MoreInfo string
	// Retryable indicates if the operation can be retried
	Retryable bool
	// RetryAfter indicates when to retry (for rate limit errors)
	RetryAfter int
	// wrapped is the original error
	wrapped error
}

// Error implements the error interface
func (e *IBMError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("IBM Cloud error (code: %s, status: %d): %s", e.Code, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("IBM Cloud error (status: %d): %s", e.StatusCode, e.Message)
}

// Unwrap returns the wrapped error
func (e *IBMError) Unwrap() error {
	return e.wrapped
}

// IsNotFound returns true if the error indicates a resource was not found
func (e *IBMError) IsNotFound() bool {
	return e.Type == ErrorTypeNotFound || e.StatusCode == http.StatusNotFound
}

// IsUnauthorized returns true if the error indicates an authentication failure
func (e *IBMError) IsUnauthorized() bool {
	return e.Type == ErrorTypeUnauthorized || e.StatusCode == http.StatusUnauthorized
}

// IsForbidden returns true if the error indicates an authorization failure
func (e *IBMError) IsForbidden() bool {
	return e.Type == ErrorTypeForbidden || e.StatusCode == http.StatusForbidden
}

// IsRateLimit returns true if the error indicates rate limiting
func (e *IBMError) IsRateLimit() bool {
	return e.Type == ErrorTypeRateLimit || e.StatusCode == http.StatusTooManyRequests
}

// IsServerError returns true if the error is a server error
func (e *IBMError) IsServerError() bool {
	return e.Type == ErrorTypeServerError || (e.StatusCode >= 500 && e.StatusCode < 600)
}

// IsClientError returns true if the error is a client error
func (e *IBMError) IsClientError() bool {
	return e.Type == ErrorTypeClientError || (e.StatusCode >= 400 && e.StatusCode < 500)
}

// IsTimeout returns true if the error indicates a timeout
func (e *IBMError) IsTimeout() bool {
	return e.Type == ErrorTypeTimeout || e.StatusCode == http.StatusRequestTimeout ||
		strings.Contains(strings.ToLower(e.Message), "timeout")
}

// IsConflict returns true if the error indicates a resource conflict
func (e *IBMError) IsConflict() bool {
	return e.Type == ErrorTypeConflict || e.StatusCode == http.StatusConflict
}

// IsValidation returns true if the error indicates a validation failure
func (e *IBMError) IsValidation() bool {
	return e.Type == ErrorTypeValidation || e.StatusCode == http.StatusUnprocessableEntity ||
		e.StatusCode == http.StatusBadRequest
}

// ParseError parses an error and returns an IBMError if possible
func ParseError(err error) *IBMError {
	if err == nil {
		return nil
	}

	// Check if it's already an IBMError
	if ibmErr, ok := err.(*IBMError); ok {
		return ibmErr
	}

	// Skip DetailedResponse as it's not an error type - it's a response wrapper

	// Check for standard HTTP response errors
	if httpErr, ok := err.(*core.SDKProblem); ok {
		return parseSDKProblem(httpErr)
	}

	// Parse error string for common patterns
	return parseErrorString(err)
}

// parseDetailedResponse is removed - DetailedResponse is not an error type

// parseSDKProblem parses an IBM SDK problem
func parseSDKProblem(problem *core.SDKProblem) *IBMError {
	// Extract status code and details from the problem
	statusCode := 0
	errorCode := ""
	message := ""
	moreInfo := ""

	// Try to extract HTTP status from problem context if available
	if problem != nil {
		message = problem.Error()
		// Use default status code mapping based on error patterns
		errorStr := strings.ToLower(message)
		if strings.Contains(errorStr, "404") || strings.Contains(errorStr, "not found") {
			statusCode = http.StatusNotFound
		} else if strings.Contains(errorStr, "401") || strings.Contains(errorStr, "unauthorized") {
			statusCode = http.StatusUnauthorized
		} else if strings.Contains(errorStr, "403") || strings.Contains(errorStr, "forbidden") {
			statusCode = http.StatusForbidden
		} else if strings.Contains(errorStr, "429") || strings.Contains(errorStr, "rate limit") {
			statusCode = http.StatusTooManyRequests
		} else if strings.Contains(errorStr, "500") || strings.Contains(errorStr, "internal") {
			statusCode = http.StatusInternalServerError
		} else {
			statusCode = http.StatusInternalServerError // Default to server error
		}
	}

	ibmErr := &IBMError{
		StatusCode: statusCode,
		Code:       errorCode,
		Message:    message,
		MoreInfo:   moreInfo,
		wrapped:    problem,
		Retryable:  statusCode >= 500 || statusCode == http.StatusTooManyRequests,
	}

	// Set error type based on status code
	switch statusCode {
	case http.StatusNotFound:
		ibmErr.Type = ErrorTypeNotFound
	case http.StatusUnauthorized:
		ibmErr.Type = ErrorTypeUnauthorized
	case http.StatusForbidden:
		ibmErr.Type = ErrorTypeForbidden
	case http.StatusTooManyRequests:
		ibmErr.Type = ErrorTypeRateLimit
	case http.StatusConflict:
		ibmErr.Type = ErrorTypeConflict
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		ibmErr.Type = ErrorTypeValidation
	case http.StatusRequestTimeout:
		ibmErr.Type = ErrorTypeTimeout
	default:
		if statusCode >= 500 {
			ibmErr.Type = ErrorTypeServerError
		} else if statusCode >= 400 {
			ibmErr.Type = ErrorTypeClientError
		} else {
			ibmErr.Type = ErrorTypeUnknown
		}
	}

	return ibmErr
}

// parseErrorString parses a generic error string
func parseErrorString(err error) *IBMError {
	errStr := strings.ToLower(err.Error())
	ibmErr := &IBMError{
		Message: err.Error(),
		wrapped: err,
	}

	// Try to detect error type from string patterns
	switch {
	case strings.Contains(errStr, "not found") || strings.Contains(errStr, "not_found"):
		ibmErr.Type = ErrorTypeNotFound
		ibmErr.StatusCode = http.StatusNotFound
	case strings.Contains(errStr, "unauthorized") || strings.Contains(errStr, "authentication"):
		ibmErr.Type = ErrorTypeUnauthorized
		ibmErr.StatusCode = http.StatusUnauthorized
	case strings.Contains(errStr, "forbidden") || strings.Contains(errStr, "permission"):
		ibmErr.Type = ErrorTypeForbidden
		ibmErr.StatusCode = http.StatusForbidden
	case strings.Contains(errStr, "rate limit") || strings.Contains(errStr, "too many requests"):
		ibmErr.Type = ErrorTypeRateLimit
		ibmErr.StatusCode = http.StatusTooManyRequests
		ibmErr.Retryable = true
	case strings.Contains(errStr, "timeout"):
		ibmErr.Type = ErrorTypeTimeout
		ibmErr.StatusCode = http.StatusRequestTimeout
		ibmErr.Retryable = true
	case strings.Contains(errStr, "conflict") || strings.Contains(errStr, "already exists"):
		ibmErr.Type = ErrorTypeConflict
		ibmErr.StatusCode = http.StatusConflict
	case strings.Contains(errStr, "invalid") || strings.Contains(errStr, "validation"):
		ibmErr.Type = ErrorTypeValidation
		ibmErr.StatusCode = http.StatusBadRequest
	case strings.Contains(errStr, "internal server error") || strings.Contains(errStr, "500"):
		ibmErr.Type = ErrorTypeServerError
		ibmErr.StatusCode = http.StatusInternalServerError
		ibmErr.Retryable = true
	default:
		ibmErr.Type = ErrorTypeUnknown
		ibmErr.StatusCode = 0
	}

	// Extract status code from string if present
	if strings.Contains(errStr, "404") {
		ibmErr.StatusCode = http.StatusNotFound
		ibmErr.Type = ErrorTypeNotFound
	} else if strings.Contains(errStr, "401") {
		ibmErr.StatusCode = http.StatusUnauthorized
		ibmErr.Type = ErrorTypeUnauthorized
	} else if strings.Contains(errStr, "403") {
		ibmErr.StatusCode = http.StatusForbidden
		ibmErr.Type = ErrorTypeForbidden
	} else if strings.Contains(errStr, "429") {
		ibmErr.StatusCode = http.StatusTooManyRequests
		ibmErr.Type = ErrorTypeRateLimit
		ibmErr.Retryable = true
	} else if strings.Contains(errStr, "500") || strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "503") || strings.Contains(errStr, "504") {
		ibmErr.Type = ErrorTypeServerError
		ibmErr.Retryable = true
		if strings.Contains(errStr, "502") {
			ibmErr.StatusCode = http.StatusBadGateway
		} else if strings.Contains(errStr, "503") {
			ibmErr.StatusCode = http.StatusServiceUnavailable
		} else if strings.Contains(errStr, "504") {
			ibmErr.StatusCode = http.StatusGatewayTimeout
		} else {
			ibmErr.StatusCode = http.StatusInternalServerError
		}
	}

	return ibmErr
}

// IsNotFound checks if any error indicates a resource was not found
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	ibmErr := ParseError(err)
	return ibmErr.IsNotFound()
}

// IsRetryable checks if an error indicates the operation can be retried
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	ibmErr := ParseError(err)
	return ibmErr.Retryable
}

// IsRateLimit checks if an error indicates rate limiting
func IsRateLimit(err error) bool {
	if err == nil {
		return false
	}
	ibmErr := ParseError(err)
	return ibmErr.IsRateLimit()
}

// IsTimeout checks if an error indicates a timeout
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	ibmErr := ParseError(err)
	return ibmErr.IsTimeout()
}
