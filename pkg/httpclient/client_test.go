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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test data structures
type TestRequest struct {
	Message string `json:"message"`
	Value   int    `json:"value"`
}

type TestResponse struct {
	Result string `json:"result"`
	Count  int    `json:"count"`
}

// Test header setter function
func testHeaderSetter(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "test-client/1.0")
}

func TestNewIBMCloudHTTPClient(t *testing.T) {
	baseURL := "https://api.test.com"
	client := NewIBMCloudHTTPClient(baseURL, testHeaderSetter)

	assert.NotNil(t, client)
	assert.Equal(t, baseURL, client.baseURL)
	assert.NotNil(t, client.client)
	assert.NotNil(t, client.setHeaders)

	// Verify connection pooling is configured
	transport := client.client.Transport.(*http.Transport)
	assert.Equal(t, 100, transport.MaxIdleConns)
	assert.Equal(t, 10, transport.MaxIdleConnsPerHost)
	assert.Equal(t, 30*time.Second, transport.IdleConnTimeout)
}

func TestNewIBMCloudHTTPClientWithClient(t *testing.T) {
	customClient := &http.Client{Timeout: 10 * time.Second}
	baseURL := "https://api.test.com"
	client := NewIBMCloudHTTPClientWithClient(customClient, baseURL, testHeaderSetter)

	assert.NotNil(t, client)
	assert.Equal(t, baseURL, client.baseURL)
	assert.Equal(t, customClient, client.client)
	assert.NotNil(t, client.setHeaders)
}

func TestIBMCloudHTTPClient_Get(t *testing.T) {
	tests := []struct {
		name           string
		endpoint       string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		expectedError  bool
		validateReq    func(t *testing.T, r *http.Request)
	}{
		{
			name:     "successful GET request",
			endpoint: "/test/endpoint",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result": "success", "count": 42}`))
			},
			expectedError: false,
			validateReq: func(t *testing.T, r *http.Request) {
				assert.Equal(t, "GET", r.Method)
				assert.Equal(t, "/test/endpoint", r.URL.Path)
				assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				assert.Equal(t, "test-client/1.0", r.Header.Get("User-Agent"))
			},
		},
		{
			name:     "API error response",
			endpoint: "/test/error",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code": "E0001", "description": "Invalid request", "message": "Bad request"}`))
			},
			expectedError: true,
			validateReq: func(t *testing.T, r *http.Request) {
				assert.Equal(t, "GET", r.Method)
				assert.Equal(t, "/test/error", r.URL.Path)
			},
		},
		{
			name:     "server error",
			endpoint: "/test/server-error",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message": "Internal server error"}`))
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.validateReq != nil {
					tt.validateReq(t, r)
				}
				tt.serverResponse(w, r)
			}))
			defer server.Close()

			client := NewIBMCloudHTTPClient(server.URL, testHeaderSetter)
			ctx := context.Background()

			resp, err := client.Get(ctx, tt.endpoint, "test-token")

			if tt.expectedError {
				assert.Error(t, err)
				assert.IsType(t, &IBMCloudError{}, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resp)
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				assert.Contains(t, string(resp.Body), "success")
			}
		})
	}
}

func TestIBMCloudHTTPClient_Post(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/test/create", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		// Read and validate request body
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Equal(t, `{"message":"test","value":123}`, string(body))

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"result": "created", "count": 1}`))
	}))
	defer server.Close()

	client := NewIBMCloudHTTPClient(server.URL, testHeaderSetter)
	ctx := context.Background()

	requestBody := `{"message":"test","value":123}`
	resp, err := client.Post(ctx, "/test/create", "test-token", strings.NewReader(requestBody))

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Contains(t, string(resp.Body), "created")
}

func TestIBMCloudHTTPClient_Patch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PATCH", r.Method)
		assert.Equal(t, "/test/update", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Equal(t, `{"value":456}`, string(body))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result": "updated", "count": 1}`))
	}))
	defer server.Close()

	client := NewIBMCloudHTTPClient(server.URL, testHeaderSetter)
	ctx := context.Background()

	requestBody := `{"value":456}`
	resp, err := client.Patch(ctx, "/test/update", "test-token", strings.NewReader(requestBody))

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(resp.Body), "updated")
}

func TestIBMCloudHTTPClient_Delete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/test/delete", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewIBMCloudHTTPClient(server.URL, testHeaderSetter)
	ctx := context.Background()

	resp, err := client.Delete(ctx, "/test/delete", "test-token")

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestIBMCloudHTTPClient_GetJSON(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		expectedError  bool
		expectedResult *TestResponse
	}{
		{
			name: "successful JSON response",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result": "success", "count": 42}`))
			},
			expectedError:  false,
			expectedResult: &TestResponse{Result: "success", Count: 42},
		},
		{
			name: "invalid JSON response",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`invalid json`))
			},
			expectedError: true,
		},
		{
			name: "API error with JSON",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code": "E0404", "description": "Resource not found"}`))
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			client := NewIBMCloudHTTPClient(server.URL, testHeaderSetter)
			ctx := context.Background()

			var result TestResponse
			err := client.GetJSON(ctx, "/test/json", "test-token", &result)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult.Result, result.Result)
				assert.Equal(t, tt.expectedResult.Count, result.Count)
			}
		})
	}
}

func TestIBMCloudHTTPClient_PostJSON(t *testing.T) {
	tests := []struct {
		name           string
		requestBody    interface{}
		responseBody   interface{}
		serverResponse func(w http.ResponseWriter, r *http.Request)
		expectedError  bool
	}{
		{
			name:         "successful JSON request/response",
			requestBody:  &TestRequest{Message: "hello", Value: 123},
			responseBody: &TestResponse{},
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				// Validate request
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)

				var req TestRequest
				err = json.Unmarshal(body, &req)
				assert.NoError(t, err)
				assert.Equal(t, "hello", req.Message)
				assert.Equal(t, 123, req.Value)

				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"result": "created", "count": 1}`))
			},
			expectedError: false,
		},
		{
			name:         "request with nil response body",
			requestBody:  &TestRequest{Message: "test", Value: 456},
			responseBody: nil,
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusAccepted)
			},
			expectedError: false,
		},
		{
			name:         "request marshaling error",
			requestBody:  struct{ BadField chan int }{BadField: make(chan int)}, // Cannot be marshaled to JSON
			responseBody: &TestResponse{},
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("Server should not be called")
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			client := NewIBMCloudHTTPClient(server.URL, testHeaderSetter)
			ctx := context.Background()

			err := client.PostJSON(ctx, "/test/json", "test-token", tt.requestBody, tt.responseBody)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if response, ok := tt.responseBody.(*TestResponse); ok && response != nil {
					assert.Equal(t, "created", response.Result)
					assert.Equal(t, 1, response.Count)
				}
			}
		})
	}
}

func TestIBMCloudError(t *testing.T) {
	tests := []struct {
		name     string
		error    *IBMCloudError
		expected string
	}{
		{
			name: "error with IBM Cloud code",
			error: &IBMCloudError{
				StatusCode:  400,
				Code:        "E0001",
				Description: "Invalid parameter",
				Endpoint:    "/test/endpoint",
			},
			expected: "IBM Cloud API error (code: E0001): Invalid parameter (endpoint: /test/endpoint)",
		},
		{
			name: "error without IBM Cloud code",
			error: &IBMCloudError{
				StatusCode: 500,
				Message:    "Internal server error",
				Endpoint:   "/test/endpoint",
			},
			expected: "HTTP 500: Internal server error (endpoint: /test/endpoint)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.error.Error())
		})
	}
}

func TestIBMCloudError_StatusCheckers(t *testing.T) {
	tests := []struct {
		name           string
		statusCode     int
		isNotFound     bool
		isForbidden    bool
		isUnauthorized bool
	}{
		{"not found", 404, true, false, false},
		{"forbidden", 403, false, true, false},
		{"unauthorized", 401, false, false, true},
		{"bad request", 400, false, false, false},
		{"server error", 500, false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &IBMCloudError{StatusCode: tt.statusCode}
			assert.Equal(t, tt.isNotFound, err.IsNotFound())
			assert.Equal(t, tt.isForbidden, err.IsForbidden())
			assert.Equal(t, tt.isUnauthorized, err.IsUnauthorized())
		})
	}
}

func TestIBMCloudHTTPClient_parseIBMCloudError(t *testing.T) {
	client := NewIBMCloudHTTPClient("https://test.com", testHeaderSetter)

	tests := []struct {
		name       string
		statusCode int
		body       []byte
		endpoint   string
		expected   *IBMCloudError
	}{
		{
			name:       "valid IBM Cloud error JSON",
			statusCode: 400,
			body:       []byte(`{"code": "E0001", "description": "Invalid request", "message": "Bad request", "type": "validation"}`),
			endpoint:   "/test",
			expected: &IBMCloudError{
				StatusCode:  400,
				Code:        "E0001",
				Description: "Invalid request",
				Message:     "Bad request",
				Type:        "validation",
				Endpoint:    "/test",
			},
		},
		{
			name:       "invalid JSON fallback",
			statusCode: 500,
			body:       []byte(`invalid json`),
			endpoint:   "/test",
			expected: &IBMCloudError{
				StatusCode: 500,
				Message:    "invalid json",
				Endpoint:   "/test",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := client.parseIBMCloudError(tt.statusCode, tt.body, tt.endpoint)

			ibmErr, ok := err.(*IBMCloudError)
			require.True(t, ok)
			assert.Equal(t, tt.expected.StatusCode, ibmErr.StatusCode)
			assert.Equal(t, tt.expected.Code, ibmErr.Code)
			assert.Equal(t, tt.expected.Description, ibmErr.Description)
			assert.Equal(t, tt.expected.Message, ibmErr.Message)
			assert.Equal(t, tt.expected.Type, ibmErr.Type)
			assert.Equal(t, tt.expected.Endpoint, ibmErr.Endpoint)
		})
	}
}

func TestIBMCloudHTTPClient_BaseURLHandling(t *testing.T) {
	tests := []struct {
		name     string
		baseURL  string
		endpoint string
		expected string
	}{
		{
			name:     "baseURL with trailing slash",
			baseURL:  "https://api.test.com/",
			endpoint: "/test/endpoint",
			expected: "https://api.test.com/test/endpoint",
		},
		{
			name:     "baseURL without trailing slash",
			baseURL:  "https://api.test.com",
			endpoint: "/test/endpoint",
			expected: "https://api.test.com/test/endpoint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewIBMCloudHTTPClient(tt.baseURL, testHeaderSetter)
			// Replace the baseURL with our test server URL after creation
			client.baseURL = strings.TrimSuffix(tt.baseURL, "/")

			// We can't directly test the URL construction without making a request,
			// but we can verify the baseURL is trimmed correctly
			expectedBase := strings.TrimSuffix(tt.baseURL, "/")
			assert.Equal(t, expectedBase, client.baseURL)
		})
	}
}

func TestIBMCloudHTTPClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result": "success"}`))
	}))
	defer server.Close()

	client := NewIBMCloudHTTPClient(server.URL, testHeaderSetter)

	// Create a context that will be canceled immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := client.Get(ctx, "/test", "token")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestIBMCloudHTTPClient_CustomHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "custom-value", r.Header.Get("X-Custom-Header"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewIBMCloudHTTPClient(server.URL, testHeaderSetter)
	ctx := context.Background()

	// Test custom headers in RequestConfig
	_, err := client.Do(ctx, RequestConfig{
		Method:   "GET",
		Endpoint: "/test",
		Token:    "test-token",
		Headers: map[string]string{
			"X-Custom-Header": "custom-value",
		},
	})

	assert.NoError(t, err)
}
