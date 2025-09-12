/*
Copyright 2024 The Kubernetes Authors.

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

package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// IBMCloudHTTPClient provides a centralized HTTP client for IBM Cloud API operations
type IBMCloudHTTPClient struct {
	client     *http.Client
	baseURL    string
	setHeaders func(*http.Request, string)
}

// RequestConfig contains configuration for HTTP requests
type RequestConfig struct {
	Method   string
	Endpoint string
	Body     io.Reader
	Token    string
	Headers  map[string]string
}

// Response contains the HTTP response and body
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// IBMCloudError represents an IBM Cloud API error
type IBMCloudError struct {
	StatusCode  int    `json:"-"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Endpoint    string `json:"-"`
}

func (e *IBMCloudError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("IBM Cloud API error (code: %s): %s (endpoint: %s)", e.Code, e.Description, e.Endpoint)
	}
	return fmt.Sprintf("HTTP %d: %s (endpoint: %s)", e.StatusCode, e.Message, e.Endpoint)
}

// IsNotFound returns true if the error is a 404 Not Found error
func (e *IBMCloudError) IsNotFound() bool {
	return e.StatusCode == http.StatusNotFound
}

// IsForbidden returns true if the error is a 403 Forbidden error
func (e *IBMCloudError) IsForbidden() bool {
	return e.StatusCode == http.StatusForbidden
}

// IsUnauthorized returns true if the error is a 401 Unauthorized error
func (e *IBMCloudError) IsUnauthorized() bool {
	return e.StatusCode == http.StatusUnauthorized
}

// NewIBMCloudHTTPClient creates a new HTTP client for IBM Cloud APIs
func NewIBMCloudHTTPClient(baseURL string, setHeaders func(*http.Request, string)) *IBMCloudHTTPClient {
	return &IBMCloudHTTPClient{
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     30 * time.Second,
			},
		},
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		setHeaders: setHeaders,
	}
}

// NewIBMCloudHTTPClientWithClient creates a new HTTP client wrapper with custom http.Client
func NewIBMCloudHTTPClientWithClient(httpClient *http.Client, baseURL string, setHeaders func(*http.Request, string)) *IBMCloudHTTPClient {
	return &IBMCloudHTTPClient{
		client:     httpClient,
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		setHeaders: setHeaders,
	}
}

// Do executes an HTTP request and returns the response
func (c *IBMCloudHTTPClient) Do(ctx context.Context, config RequestConfig) (*Response, error) {
	url := c.baseURL + config.Endpoint

	req, err := http.NewRequestWithContext(ctx, config.Method, url, config.Body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Set IBM Cloud specific headers
	if c.setHeaders != nil {
		c.setHeaders(req, config.Token)
	}

	// Set additional headers
	for k, v := range config.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("making request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.FromContext(ctx).V(1).Info("failed to close response body", "error", closeErr)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	response := &Response{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       body,
	}

	// Check for errors and parse IBM Cloud error format
	if resp.StatusCode >= 400 {
		return response, c.parseIBMCloudError(resp.StatusCode, body, config.Endpoint)
	}

	return response, nil
}

// Get performs a GET request
func (c *IBMCloudHTTPClient) Get(ctx context.Context, endpoint, token string) (*Response, error) {
	return c.Do(ctx, RequestConfig{
		Method:   "GET",
		Endpoint: endpoint,
		Token:    token,
	})
}

// Post performs a POST request
func (c *IBMCloudHTTPClient) Post(ctx context.Context, endpoint, token string, body io.Reader) (*Response, error) {
	return c.Do(ctx, RequestConfig{
		Method:   "POST",
		Endpoint: endpoint,
		Body:     body,
		Token:    token,
	})
}

// Patch performs a PATCH request
func (c *IBMCloudHTTPClient) Patch(ctx context.Context, endpoint, token string, body io.Reader) (*Response, error) {
	return c.Do(ctx, RequestConfig{
		Method:   "PATCH",
		Endpoint: endpoint,
		Body:     body,
		Token:    token,
	})
}

// Delete performs a DELETE request
func (c *IBMCloudHTTPClient) Delete(ctx context.Context, endpoint, token string) (*Response, error) {
	return c.Do(ctx, RequestConfig{
		Method:   "DELETE",
		Endpoint: endpoint,
		Token:    token,
	})
}

// parseIBMCloudError parses IBM Cloud API error responses
func (c *IBMCloudHTTPClient) parseIBMCloudError(statusCode int, body []byte, endpoint string) error {
	cloudErr := &IBMCloudError{
		StatusCode: statusCode,
		Endpoint:   endpoint,
	}

	// Try to parse IBM Cloud error format
	var errorResp struct {
		Code        string `json:"code"`
		Description string `json:"description"`
		Message     string `json:"message"`
		Type        string `json:"type"`
	}

	if err := json.Unmarshal(body, &errorResp); err == nil {
		cloudErr.Code = errorResp.Code
		cloudErr.Description = errorResp.Description
		cloudErr.Message = errorResp.Message
		cloudErr.Type = errorResp.Type
	} else {
		// Fallback to raw response
		cloudErr.Message = string(body)
	}

	return cloudErr
}

// GetJSON performs a GET request and unmarshals JSON response
func (c *IBMCloudHTTPClient) GetJSON(ctx context.Context, endpoint, token string, result interface{}) error {
	resp, err := c.Get(ctx, endpoint, token)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(resp.Body, result); err != nil {
		return fmt.Errorf("unmarshaling JSON response: %w", err)
	}

	return nil
}

// PostJSON performs a POST request with JSON body and unmarshals JSON response
func (c *IBMCloudHTTPClient) PostJSON(ctx context.Context, endpoint, token string, requestBody, result interface{}) error {
	var body io.Reader
	if requestBody != nil {
		jsonData, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("marshaling request body: %w", err)
		}
		body = strings.NewReader(string(jsonData))
	}

	resp, err := c.Post(ctx, endpoint, token, body)
	if err != nil {
		return err
	}

	if result != nil && len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, result); err != nil {
			return fmt.Errorf("unmarshaling JSON response: %w", err)
		}
	}

	return nil
}
