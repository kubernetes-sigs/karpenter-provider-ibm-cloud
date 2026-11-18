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
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
)

type gateTestTransport struct{ calls int }

func (t *gateTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls++
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

func TestRequestGatePacesEveryAttemptAndCancelsBeforeHTTP(t *testing.T) {
	transport := &gateTestTransport{}
	client := NewIBMCloudHTTPClientWithClient(&http.Client{Transport: transport}, "https://example.invalid", nil)
	budget := rate.NewLimiter(rate.Every(time.Second), 1)
	client.SetRequestGate(budget.Wait)
	_, err := client.Get(context.Background(), "/workers", "")
	require.NoError(t, err)
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer stop()
	_, err = client.Get(ctx, "/retry", "")
	require.Error(t, err)
	require.Equal(t, 1, transport.calls)
}
