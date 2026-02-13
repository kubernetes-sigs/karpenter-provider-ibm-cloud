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
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoWithRateLimitRetry_SuccessFirstTry(t *testing.T) {
	ctx := context.Background()
	calls := 0
	result, err := DoWithRateLimitRetry(ctx, func() (string, *core.DetailedResponse, error) {
		calls++
		return "ok", &core.DetailedResponse{StatusCode: 200}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 1, calls)
}

func TestDoWithRateLimitRetry_Non429ErrorReturnsImmediately(t *testing.T) {
	ctx := context.Background()
	calls := 0
	apiErr := assert.AnError
	result, err := DoWithRateLimitRetry(ctx, func() (string, *core.DetailedResponse, error) {
		calls++
		return "", &core.DetailedResponse{StatusCode: 500}, apiErr
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, apiErr)
	assert.Equal(t, "", result)
	assert.Equal(t, 1, calls)
}

func TestDoWithRateLimitRetry_NilResponseReturnsImmediately(t *testing.T) {
	ctx := context.Background()
	calls := 0
	result, err := DoWithRateLimitRetry(ctx, func() (string, *core.DetailedResponse, error) {
		calls++
		return "ok", nil, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 1, calls)
}

func TestDoWithRateLimitRetry_429ThenSuccess(t *testing.T) {
	ctx := context.Background()
	calls := 0
	result, err := DoWithRateLimitRetry(ctx, func() (int, *core.DetailedResponse, error) {
		calls++
		if calls == 1 {
			return 0, &core.DetailedResponse{StatusCode: 429}, nil
		}
		return 42, &core.DetailedResponse{StatusCode: 200}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 42, result)
	assert.Equal(t, 2, calls)
}

func TestDoWithRateLimitRetry_ExhaustedRetries(t *testing.T) {
	ctx := context.Background()
	calls := 0
	result, err := DoWithRateLimitRetry(ctx, func() (string, *core.DetailedResponse, error) {
		calls++
		return "", &core.DetailedResponse{StatusCode: 429}, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limited after 5 attempts")
	assert.Equal(t, "", result)
	assert.Equal(t, 5, calls)
}

func TestDoWithRateLimitRetry_ContextCanceledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan struct{})
	var result string
	var err error
	go func() {
		result, err = DoWithRateLimitRetry(ctx, func() (string, *core.DetailedResponse, error) {
			calls++
			return "", &core.DetailedResponse{StatusCode: 429}, nil
		})
		close(done)
	}()
	cancel()
	<-done
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, "", result)
	assert.Equal(t, 1, calls)
}

func TestDoWithRateLimitRetry_RespectsRetryAfterHeaderSeconds(t *testing.T) {
	ctx := context.Background()
	calls := 0
	headers := http.Header{}
	headers.Set("Retry-After", "1")
	result, err := DoWithRateLimitRetry(ctx, func() (string, *core.DetailedResponse, error) {
		calls++
		if calls == 1 {
			return "", &core.DetailedResponse{StatusCode: 429, Headers: headers}, nil
		}
		return "ok", &core.DetailedResponse{StatusCode: 200}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 2, calls)
}

func TestDoWithRateLimitRetry_RespectsRetryAfterHeaderHTTPDate(t *testing.T) {
	ctx := context.Background()
	calls := 0
	headers := http.Header{}
	// HTTP-date format (RFC 1123): 1 second from now so delay is short
	headers.Set("Retry-After", time.Now().Add(1*time.Second).UTC().Format(time.RFC1123))
	result, err := DoWithRateLimitRetry(ctx, func() (string, *core.DetailedResponse, error) {
		calls++
		if calls == 1 {
			return "", &core.DetailedResponse{StatusCode: 429, Headers: headers}, nil
		}
		return "ok", &core.DetailedResponse{StatusCode: 200}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 2, calls)
}

func TestDoWithRateLimitRetry_InvalidRetryAfterFallsBackToBackoff(t *testing.T) {
	ctx := context.Background()
	calls := 0
	headers := http.Header{}
	// Invalid Retry-After value (not a number, not a valid HTTP-date) -> should use exponential backoff
	headers.Set("Retry-After", "not-a-number-or-date")
	result, err := DoWithRateLimitRetry(ctx, func() (string, *core.DetailedResponse, error) {
		calls++
		if calls == 1 {
			return "", &core.DetailedResponse{StatusCode: 429, Headers: headers}, nil
		}
		return "ok", &core.DetailedResponse{StatusCode: 200}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 2, calls)
}

func TestDoWithRateLimitRetry_LargeRetryAfterCappedAtMaxBackoff(t *testing.T) {
	ctx := context.Background()
	calls := 0
	headers := http.Header{}
	headers.Set("Retry-After", "600")

	start := time.Now()
	result, err := DoWithRateLimitRetry(ctx, func() (string, *core.DetailedResponse, error) {
		calls++
		if calls == 1 {
			return "", &core.DetailedResponse{StatusCode: 429, Headers: headers}, nil
		}
		return "ok", &core.DetailedResponse{StatusCode: 200}, nil
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 2, calls)
	assert.Less(t, elapsed, maxBackoff+15*time.Second,
		"delay should be capped at maxBackoff, not full Retry-After")
}
