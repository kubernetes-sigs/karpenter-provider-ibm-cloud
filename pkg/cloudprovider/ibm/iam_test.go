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
	"errors"
	"testing"
	"time"
)

type mockAuthenticator struct {
	token string
	err   error
}

func (m *mockAuthenticator) RequestToken() (*TokenResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &TokenResponse{
		AccessToken: m.token,
		ExpiresIn:   3600,
	}, nil
}

func TestNewIAMClient(t *testing.T) {
	testKey := "test-api-key"
	client := NewIAMClient(testKey)

	if client == nil {
		t.Fatal("expected non-nil client")
		return
	}

	if client.apiKey != testKey {
		t.Errorf("expected API key %s, got %s", testKey, client.apiKey)
	}
	if client.token != "" {
		t.Error("expected empty token")
	}
	if !client.expiry.IsZero() {
		t.Error("expected zero expiry time")
	}
	if client.Authenticator == nil {
		t.Error("expected non-nil authenticator")
	}

	// Type assert to check if it's an iamAuthenticator
	if auth, ok := client.Authenticator.(*iamAuthenticator); !ok {
		t.Error("expected authenticator to be *iamAuthenticator")
	} else if auth.auth.ApiKey != testKey {
		t.Errorf("expected authenticator API key %s, got %s", testKey, auth.auth.ApiKey)
	}
}

func TestGetToken(t *testing.T) {
	ctx := context.Background()
	testKey := "test-api-key"
	testToken := "test-token"

	tests := []struct {
		name           string
		existingToken  string
		existingExpiry time.Time
		mockAuth       Authenticator
		wantToken      string
		wantErr        bool
	}{
		{
			name:           "valid cached token",
			existingToken:  "cached-token",
			existingExpiry: time.Now().Add(10 * time.Minute),
			wantToken:      "cached-token",
			wantErr:        false,
		},
		{
			name:           "expired token",
			existingToken:  "expired-token",
			existingExpiry: time.Now().Add(-10 * time.Minute),
			mockAuth: &mockAuthenticator{
				token: testToken,
			},
			wantToken: testToken,
			wantErr:   false,
		},
		{
			name: "successful new token",
			mockAuth: &mockAuthenticator{
				token: testToken,
			},
			wantToken: testToken,
			wantErr:   false,
		},
		{
			name: "token request error",
			mockAuth: &mockAuthenticator{
				err: errors.New("token request failed"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewIAMClient(testKey)
			client.token = tt.existingToken
			client.expiry = tt.existingExpiry

			if tt.mockAuth != nil {
				client.Authenticator = tt.mockAuth
			}

			token, err := client.GetToken(ctx)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				if token != "" {
					t.Error("expected empty token")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if token != tt.wantToken {
					t.Errorf("got token %s, want %s", token, tt.wantToken)
				}

				if tt.existingToken == "" {
					// Check that expiry is set correctly for new tokens
					expectedExpiry := time.Now().Add(55 * time.Minute)
					if client.expiry.Sub(expectedExpiry) > 2*time.Second {
						t.Errorf("expiry time not within expected range")
					}
				}
			}
		})
	}
}
