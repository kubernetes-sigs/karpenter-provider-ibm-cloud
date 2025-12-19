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

package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sigs.k8s.io/karpenter/pkg/utils/atomic"
)

// IAMTokenResponse represents an IAM token response
type IAMTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Expiration   int64  `json:"expiration"`
	Scope        string `json:"scope"`
}

// IAMTokenRequest represents an IAM token request
type IAMTokenRequest struct {
	GrantType    string `json:"grant_type"`
	APIKey       string `json:"apikey,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// IAMBehavior controls the fake IAM API behavior for testing
type IAMBehavior struct {
	GetTokenBehavior     MockedFunction[IAMTokenRequest, IAMTokenResponse]
	RefreshTokenBehavior MockedFunction[IAMTokenRequest, IAMTokenResponse]

	Tokens    atomic.Slice[*IAMTokenResponse]
	NextError AtomicError
}

// IAMAPI implements a fake IAM API for testing
type IAMAPI struct {
	*IAMBehavior
	mu sync.RWMutex
}

// NewIAMAPI creates a new fake IAM API
func NewIAMAPI() *IAMAPI {
	return &IAMAPI{
		IAMBehavior: &IAMBehavior{},
	}
}

// GetToken gets an IAM access token using API key
func (f *IAMAPI) GetToken(ctx context.Context, apiKey string) (*IAMTokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	request := IAMTokenRequest{
		GrantType: "urn:ibm:params:oauth:grant-type:apikey",
		APIKey:    apiKey,
	}

	// Track the call
	f.GetTokenBehavior.CalledWithInput.Add(request)

	// Return mocked output if set
	if output := f.GetTokenBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: generate a mock token
	now := time.Now()
	expiresIn := 3600 // 1 hour

	token := &IAMTokenResponse{
		AccessToken:  fmt.Sprintf("mock-access-token-%d", now.Unix()),
		RefreshToken: fmt.Sprintf("mock-refresh-token-%d", now.Unix()),
		TokenType:    "Bearer",
		ExpiresIn:    expiresIn,
		Expiration:   now.Add(time.Duration(expiresIn) * time.Second).Unix(),
		Scope:        "ibm openid",
	}

	f.Tokens.Add(token)
	return token, nil
}

// RefreshToken refreshes an IAM access token
func (f *IAMAPI) RefreshToken(ctx context.Context, refreshToken string) (*IAMTokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	request := IAMTokenRequest{
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
	}

	// Track the call
	f.RefreshTokenBehavior.CalledWithInput.Add(request)

	// Return mocked output if set
	if output := f.RefreshTokenBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: generate a new mock token
	now := time.Now()
	expiresIn := 3600 // 1 hour

	token := &IAMTokenResponse{
		AccessToken:  fmt.Sprintf("mock-refreshed-token-%d", now.Unix()),
		RefreshToken: fmt.Sprintf("mock-new-refresh-token-%d", now.Unix()),
		TokenType:    "Bearer",
		ExpiresIn:    expiresIn,
		Expiration:   now.Add(time.Duration(expiresIn) * time.Second).Unix(),
		Scope:        "ibm openid",
	}

	f.Tokens.Add(token)
	return token, nil
}

// ValidateToken validates an IAM access token (mock implementation)
func (f *IAMAPI) ValidateToken(ctx context.Context, accessToken string) (bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return false, err
	}

	// For testing, consider all non-empty tokens as valid
	return accessToken != "", nil
}

// Reset resets the fake API state
func (f *IAMAPI) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.Tokens.Reset()
	f.NextError.Store(nil)
	f.GetTokenBehavior.Reset()
	f.RefreshTokenBehavior.Reset()
}
