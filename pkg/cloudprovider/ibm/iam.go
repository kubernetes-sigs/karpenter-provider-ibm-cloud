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
	"sync"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
)

// TokenResponse represents the response from a token request
type TokenResponse struct {
	AccessToken string
	ExpiresIn   int64
}

// Authenticator defines the interface for token management
type Authenticator interface {
	RequestToken() (*TokenResponse, error)
}

// iamAuthenticator wraps the IBM authenticator to match our interface
type iamAuthenticator struct {
	auth *core.IamAuthenticator
}

func (a *iamAuthenticator) RequestToken() (*TokenResponse, error) {
	token, err := a.auth.RequestToken()
	if err != nil {
		return nil, err
	}
	return &TokenResponse{
		AccessToken: token.AccessToken,
		ExpiresIn:   token.ExpiresIn,
	}, nil
}

// IAMClient handles interactions with the IBM Cloud IAM API
type IAMClient struct {
	mu            sync.Mutex
	apiKey        string
	Authenticator Authenticator // Exported for testing
	token         string
	expiry        time.Time
}

func NewIAMClient(apiKey string) *IAMClient {
	return &IAMClient{
		apiKey: apiKey,
		Authenticator: &iamAuthenticator{
			auth: &core.IamAuthenticator{
				ApiKey: apiKey,
				// Remove scope - IBM SDK doesn't support custom scopes this way
			},
		},
	}
}

// GetToken returns a valid IAM token, fetching a new one if necessary
func (c *IAMClient) GetToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.expiry) {
		return c.token, nil
	}

	tokenResponse, err := c.Authenticator.RequestToken()
	if err != nil {
		return "", fmt.Errorf("getting IAM token: %w", err)
	}

	c.token = tokenResponse.AccessToken
	const refreshMargin int64 = 300
	const minExpiry = 60 * time.Second
	expiresIn := time.Duration(tokenResponse.ExpiresIn-refreshMargin) * time.Second
	if expiresIn < minExpiry {
		expiresIn = minExpiry
	}
	c.expiry = time.Now().Add(expiresIn)

	return c.token, nil
}
