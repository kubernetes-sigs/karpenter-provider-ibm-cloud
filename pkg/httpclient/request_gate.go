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
)

type gatedTransport struct {
	base http.RoundTripper
	wait func(context.Context) error
}

func (t *gatedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := t.wait(request.Context()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(request)
}

func (c *IBMCloudHTTPClient) SetRequestGate(wait func(context.Context) error) {
	copy := *c.client
	base := copy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	copy.Transport = &gatedTransport{base: base, wait: wait}
	c.client = &copy
}
