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
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"k8s.io/klog/v2"
)

const (
	initialBackoff = 100 * time.Millisecond
	maxBackoff     = 30 * time.Second
	maxAttempts    = 5
	retryAfterKey  = "Retry-After"
)

// DoWithRetry handles HTTP 429 rate limiting with exponential backoff.
// It performs up to maxAttempts (1 initial + 4 retries), respecting the
// Retry-After header when present (seconds or HTTP-date per RFC 7231).
func DoWithRetry[T any](ctx context.Context, fn func() (T, *core.DetailedResponse, error)) (T, error) {
	var zero T
	backoff := initialBackoff

	for attempt := 0; attempt < maxAttempts; attempt++ {
		result, response, err := fn()

		// Success or non-rate-limit error
		if response == nil || response.StatusCode != http.StatusTooManyRequests {
			return result, err
		}

		// Use Retry-After for this sleep only; keep exponential backoff progression separate.
		// Retry-After can be delay-seconds (integer) or HTTP-date
		delay := backoff
		if response.Headers != nil {
			if ra := response.Headers.Get(retryAfterKey); ra != "" {
				if secs, parseErr := strconv.Atoi(ra); parseErr == nil && secs > 0 {
					delay = time.Duration(secs) * time.Second
				} else if t, parseErr := http.ParseTime(ra); parseErr == nil {
					if d := time.Until(t); d > 0 {
						delay = d
					}
				}
			}
		}

		klog.V(4).InfoS("rate limited (429), sleeping before retry", "delay", delay, "attempt", attempt+1, "maxAttempts", maxAttempts)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return zero, ctx.Err()
		case <-timer.C:
			backoff = min(backoff*2, maxBackoff)
		}
	}

	return zero, fmt.Errorf("rate limited after %d attempts", maxAttempts)
}
