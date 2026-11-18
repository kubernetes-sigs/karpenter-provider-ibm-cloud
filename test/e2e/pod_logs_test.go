//go:build e2e
// +build e2e

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

package e2e

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

type podLogsTransport func(*http.Request) (*http.Response, error)

func (f podLogsTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type podLogsBody struct {
	io.Reader
	closed bool
}

func (b *podLogsBody) Close() error {
	b.closed = true
	return nil
}

func podLogsSuite(t *testing.T, transport podLogsTransport) *E2ETestSuite {
	t.Helper()
	coreClient, err := typedcorev1.NewForConfig(&rest.Config{
		Host:        "https://configured-api.example",
		BearerToken: "configured-token",
		Transport:   transport,
	})
	require.NoError(t, err)
	return &E2ETestSuite{coreClient: coreClient}
}

func TestPodLogsUseConfiguredAPIWithoutKubectl(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	foreignConfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(foreignConfig, []byte("apiVersion: v1\nkind: Config\ncurrent-context: foreign\ncontexts:\n- name: foreign\n  context:\n    cluster: foreign\nclusters:\n- name: foreign\n  cluster:\n    server: https://foreign-api.example\n"), 0600))
	t.Setenv("KUBECONFIG", foreignConfig)

	for _, namespace := range []string{"inspector", ""} {
		t.Run("namespace="+namespace, func(t *testing.T) {
			body := &podLogsBody{Reader: strings.NewReader("disk inspection\ncomplete\n")}
			requests := 0
			suite := podLogsSuite(t, func(request *http.Request) (*http.Response, error) {
				requests++
				expectedNamespace := namespace
				if expectedNamespace == "" {
					expectedNamespace = "default"
				}
				require.Equal(t, http.MethodGet, request.Method)
				require.Equal(t, "configured-api.example", request.URL.Host)
				require.Equal(t, "/api/v1/namespaces/"+expectedNamespace+"/pods/disk-check/log", request.URL.Path)
				require.Equal(t, "Bearer configured-token", request.Header.Get("Authorization"))
				deadline, ok := request.Context().Deadline()
				require.True(t, ok, "Background callers must have a bounded log request")
				require.InDelta(t, 30, time.Until(deadline).Seconds(), 1)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}, nil
			})

			logs, err := suite.getPodLogs(context.Background(), "disk-check", namespace)
			require.NoError(t, err)
			require.Equal(t, "disk inspection\ncomplete\n", logs)
			require.Equal(t, 1, requests)
			require.True(t, body.closed)
		})
	}
}

func TestPodLogsPreserveAPIError(t *testing.T) {
	suite := podLogsSuite(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","message":"pod logs forbidden","code":403}`)),
		}, nil
	})
	logs, err := suite.getPodLogs(t.Context(), "disk-check", "default")
	require.True(t, apierrors.IsForbidden(err), "API errors must remain identifiable through the helper")
	require.Empty(t, logs)
}

type canceledPodLogsReader struct {
	ctx context.Context
}

func (r canceledPodLogsReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func TestPodLogsCancelBlockedStreamAndCloseBody(t *testing.T) {
	var body *podLogsBody
	suite := podLogsSuite(t, func(request *http.Request) (*http.Response, error) {
		body = &podLogsBody{Reader: canceledPodLogsReader{ctx: request.Context()}}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	logs, err := suite.getPodLogs(ctx, "disk-check", "default")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, logs)
	require.NotNil(t, body)
	require.True(t, body.closed)
}

func TestPodLogsPreserveTransportCancellation(t *testing.T) {
	suite := podLogsSuite(t, func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	logs, err := suite.getPodLogs(ctx, "disk-check", "default")
	require.True(t, errors.Is(err, context.Canceled))
	require.Empty(t, logs)
}
