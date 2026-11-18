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

package httpclient

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cancelAwareTransport struct {
	entered chan struct{}
}

func (t *cancelAwareTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.entered != nil {
		close(t.entered)
	}
	<-request.Context().Done()
	return nil, request.Context().Err()
}

func TestBoundedHTTPClientCopiesConfigurationAndBoundsTimeout(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	for _, originalTimeout := range []time.Duration{0, -time.Second, time.Hour, 5 * time.Second} {
		t.Run(originalTimeout.String(), func(t *testing.T) {
			transport := &cancelAwareTransport{}
			original := &http.Client{Transport: transport, Timeout: originalTimeout, Jar: jar}
			bounded := BoundedHTTPClient(original, "eu-de")
			require.NotSame(t, original, bounded)
			require.Equal(t, originalTimeout, original.Timeout)
			require.Same(t, transport, original.Transport)
			require.Same(t, jar, bounded.Jar)
			expected := 30 * time.Second
			if originalTimeout > 0 && originalTimeout < expected {
				expected = originalTimeout
			}
			require.Equal(t, expected, bounded.Timeout)
		})
	}
	defaultTimeout, defaultTransport := http.DefaultClient.Timeout, http.DefaultClient.Transport
	bounded := BoundedHTTPClient(nil, "")
	require.NotSame(t, http.DefaultClient, bounded)
	require.Equal(t, 30*time.Second, bounded.Timeout)
	require.Equal(t, defaultTimeout, http.DefaultClient.Timeout)
	require.Equal(t, defaultTransport, http.DefaultClient.Transport)
}

func TestBoundedHTTPClientPropagatesCallerCancellation(t *testing.T) {
	transport := &cancelAwareTransport{entered: make(chan struct{})}
	bounded := BoundedHTTPClient(&http.Client{Transport: transport}, "us-south")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://us-south.iaas.cloud.ibm.com/v1/instances", nil)
	require.NoError(t, err)
	completed := make(chan error, 1)
	go func() { _, err := bounded.Do(request); completed <- err }()
	select {
	case <-transport.entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach transport")
	}
	cancel()
	select {
	case err := <-completed:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("request ignored cancellation")
	}
}

func TestBoundedHTTPClientPreservesShorterResponseDeadline(t *testing.T) {
	bounded := BoundedHTTPClient(&http.Client{Transport: &cancelAwareTransport{}, Timeout: 20 * time.Millisecond}, "us-south")
	request, err := http.NewRequest(http.MethodGet, "https://us-south.iaas.cloud.ibm.com/v1/instances", nil)
	require.NoError(t, err)
	completed := make(chan error, 1)
	go func() { _, err := bounded.Do(request); completed <- err }()
	select {
	case err := <-completed:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("shorter response deadline was lost")
	}
}
