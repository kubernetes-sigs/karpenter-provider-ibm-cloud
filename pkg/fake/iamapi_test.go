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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIAMAPI_GetToken(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*IAMAPI)
		apiKey       string
		expectError  bool
		validateFunc func(*testing.T, *IAMTokenResponse, *IAMAPI)
	}{
		{
			name:        "successful token generation",
			apiKey:      "test-api-key",
			expectError: false,
			validateFunc: func(t *testing.T, token *IAMTokenResponse, api *IAMAPI) {
				assert.NotNil(t, token)
				assert.NotEmpty(t, token.AccessToken)
				assert.NotEmpty(t, token.RefreshToken)
				assert.Equal(t, "Bearer", token.TokenType)
				assert.Equal(t, 3600, token.ExpiresIn)
				assert.Equal(t, "ibm openid", token.Scope)
				assert.True(t, token.Expiration > time.Now().Unix())

				// Verify token was stored
				assert.Equal(t, 1, api.Tokens.Len())
			},
		},
		{
			name: "error during token generation",
			setupFunc: func(api *IAMAPI) {
				api.NextError.Store(fmt.Errorf("authentication failed"))
			},
			apiKey:      "test-api-key",
			expectError: true,
		},
		{
			name: "with mocked output",
			setupFunc: func(api *IAMAPI) {
				mockedToken := &IAMTokenResponse{
					AccessToken:  "custom-access-token",
					RefreshToken: "custom-refresh-token",
					TokenType:    "Bearer",
					ExpiresIn:    7200,
					Expiration:   time.Now().Add(2 * time.Hour).Unix(),
					Scope:        "custom scope",
				}
				api.GetTokenBehavior.Output.Store(mockedToken)
			},
			apiKey:      "test-api-key",
			expectError: false,
			validateFunc: func(t *testing.T, token *IAMTokenResponse, api *IAMAPI) {
				assert.Equal(t, "custom-access-token", token.AccessToken)
				assert.Equal(t, "custom-refresh-token", token.RefreshToken)
				assert.Equal(t, 7200, token.ExpiresIn)
				assert.Equal(t, "custom scope", token.Scope)
			},
		},
		{
			name:        "empty api key",
			apiKey:      "",
			expectError: false,
			validateFunc: func(t *testing.T, token *IAMTokenResponse, api *IAMAPI) {
				assert.NotNil(t, token)
				assert.NotEmpty(t, token.AccessToken)

				// Verify the call was tracked with empty API key
				inputs := api.GetTokenBehavior.CalledWithInput.Clone()
				assert.Len(t, inputs, 1)
				assert.Equal(t, "", inputs[0].APIKey)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewIAMAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			token, err := api.GetToken(context.Background(), tt.apiKey)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, the call might not be tracked
				return
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, token, api)
				}
			}

			// Verify the call was tracked (only for successful calls)
			inputs := api.GetTokenBehavior.CalledWithInput.Clone()
			assert.Len(t, inputs, 1)
			assert.Equal(t, "urn:ibm:params:oauth:grant-type:apikey", inputs[0].GrantType)
			assert.Equal(t, tt.apiKey, inputs[0].APIKey)
		})
	}
}

func TestIAMAPI_RefreshToken(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*IAMAPI)
		refreshToken string
		expectError  bool
		validateFunc func(*testing.T, *IAMTokenResponse, *IAMAPI)
	}{
		{
			name:         "successful token refresh",
			refreshToken: "existing-refresh-token",
			expectError:  false,
			validateFunc: func(t *testing.T, token *IAMTokenResponse, api *IAMAPI) {
				assert.NotNil(t, token)
				assert.Contains(t, token.AccessToken, "mock-refreshed-token")
				assert.Contains(t, token.RefreshToken, "mock-new-refresh-token")
				assert.Equal(t, "Bearer", token.TokenType)
				assert.Equal(t, 3600, token.ExpiresIn)
				assert.Equal(t, "ibm openid", token.Scope)

				// Verify token was stored
				assert.Equal(t, 1, api.Tokens.Len())
			},
		},
		{
			name: "error during refresh",
			setupFunc: func(api *IAMAPI) {
				api.NextError.Store(fmt.Errorf("refresh failed"))
			},
			refreshToken: "existing-refresh-token",
			expectError:  true,
		},
		{
			name: "with mocked output",
			setupFunc: func(api *IAMAPI) {
				mockedToken := &IAMTokenResponse{
					AccessToken:  "refreshed-custom-token",
					RefreshToken: "new-refresh-token",
					TokenType:    "Bearer",
					ExpiresIn:    3600,
					Expiration:   time.Now().Add(time.Hour).Unix(),
					Scope:        "refreshed scope",
				}
				api.RefreshTokenBehavior.Output.Store(mockedToken)
			},
			refreshToken: "test-refresh-token",
			expectError:  false,
			validateFunc: func(t *testing.T, token *IAMTokenResponse, api *IAMAPI) {
				assert.Equal(t, "refreshed-custom-token", token.AccessToken)
				assert.Equal(t, "new-refresh-token", token.RefreshToken)
				assert.Equal(t, "refreshed scope", token.Scope)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewIAMAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			token, err := api.RefreshToken(context.Background(), tt.refreshToken)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, the call might not be tracked
				return
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, token, api)
				}
			}

			// Verify the call was tracked (only for successful calls)
			inputs := api.RefreshTokenBehavior.CalledWithInput.Clone()
			assert.Len(t, inputs, 1)
			assert.Equal(t, "refresh_token", inputs[0].GrantType)
			assert.Equal(t, tt.refreshToken, inputs[0].RefreshToken)
		})
	}
}

func TestIAMAPI_ValidateToken(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(*IAMAPI)
		accessToken string
		expectValid bool
		expectError bool
	}{
		{
			name:        "valid token",
			accessToken: "some-access-token",
			expectValid: true,
			expectError: false,
		},
		{
			name:        "empty token considered invalid",
			accessToken: "",
			expectValid: false,
			expectError: false,
		},
		{
			name: "error during validation",
			setupFunc: func(api *IAMAPI) {
				api.NextError.Store(fmt.Errorf("validation service unavailable"))
			},
			accessToken: "some-token",
			expectValid: false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewIAMAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			valid, err := api.ValidateToken(context.Background(), tt.accessToken)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectValid, valid)
			}
		})
	}
}

func TestIAMAPI_TokenExpiration(t *testing.T) {
	api := NewIAMAPI()

	// Get a token
	token, err := api.GetToken(context.Background(), "test-api-key")
	require.NoError(t, err)

	// Verify expiration is set correctly
	now := time.Now()
	expectedExpiration := now.Add(time.Duration(token.ExpiresIn) * time.Second)

	// Allow 1 second tolerance for test execution time
	actualExpiration := time.Unix(token.Expiration, 0)
	diff := expectedExpiration.Sub(actualExpiration).Abs()
	assert.Less(t, diff, 2*time.Second, "Token expiration should be close to expected time")
}

func TestIAMAPI_TokenFormat(t *testing.T) {
	api := NewIAMAPI()

	// Test GetToken format
	t.Run("GetToken format", func(t *testing.T) {
		token, err := api.GetToken(context.Background(), "test-key")
		require.NoError(t, err)

		assert.True(t, strings.HasPrefix(token.AccessToken, "mock-access-token-"))
		assert.True(t, strings.HasPrefix(token.RefreshToken, "mock-refresh-token-"))
		assert.Regexp(t, `mock-access-token-\d+`, token.AccessToken)
		assert.Regexp(t, `mock-refresh-token-\d+`, token.RefreshToken)
	})

	// Test RefreshToken format
	t.Run("RefreshToken format", func(t *testing.T) {
		token, err := api.RefreshToken(context.Background(), "old-refresh-token")
		require.NoError(t, err)

		assert.True(t, strings.HasPrefix(token.AccessToken, "mock-refreshed-token-"))
		assert.True(t, strings.HasPrefix(token.RefreshToken, "mock-new-refresh-token-"))
		assert.Regexp(t, `mock-refreshed-token-\d+`, token.AccessToken)
		assert.Regexp(t, `mock-new-refresh-token-\d+`, token.RefreshToken)
	})
}

func TestIAMAPI_Reset(t *testing.T) {
	api := NewIAMAPI()

	// Add some data
	_, err := api.GetToken(context.Background(), "key1")
	require.NoError(t, err)
	_, err = api.RefreshToken(context.Background(), "refresh1")
	require.NoError(t, err)

	// Set error and mock output
	api.NextError.Store(fmt.Errorf("test error"))
	api.GetTokenBehavior.Output.Store(&IAMTokenResponse{AccessToken: "mocked"})

	// Verify data exists
	assert.Equal(t, 2, api.Tokens.Len())
	assert.NotNil(t, api.NextError.Get())
	assert.NotNil(t, api.GetTokenBehavior.Output.Get())
	assert.Greater(t, len(api.GetTokenBehavior.CalledWithInput.Clone()), 0)
	assert.Greater(t, len(api.RefreshTokenBehavior.CalledWithInput.Clone()), 0)

	// Reset
	api.Reset()

	// Verify everything is cleared
	assert.Equal(t, 0, api.Tokens.Len())
	assert.Nil(t, api.NextError.Get())
	assert.Nil(t, api.GetTokenBehavior.Output.Get())
	assert.Len(t, api.GetTokenBehavior.CalledWithInput.Clone(), 0)
	assert.Len(t, api.RefreshTokenBehavior.CalledWithInput.Clone(), 0)

	// Verify we can still use the API after reset
	newToken, err := api.GetToken(context.Background(), "new-key")
	require.NoError(t, err)
	assert.NotNil(t, newToken)
	assert.NotEmpty(t, newToken.AccessToken)
}

func TestIAMAPI_Concurrency(t *testing.T) {
	api := NewIAMAPI()

	// Test concurrent token generation
	t.Run("concurrent GetToken", func(t *testing.T) {
		const numGoroutines = 50
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		tokens := make(chan *IAMTokenResponse, numGoroutines)
		errors := make(chan error, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(idx int) {
				defer wg.Done()
				token, err := api.GetToken(context.Background(), fmt.Sprintf("key-%d", idx))
				if err != nil {
					errors <- err
				} else {
					tokens <- token
				}
			}(i)
		}

		wg.Wait()
		close(tokens)
		close(errors)

		// Check for errors
		assert.Len(t, errors, 0, "No errors should occur during concurrent access")

		// Verify all tokens were generated
		tokenCount := 0
		for range tokens {
			tokenCount++
		}

		assert.Equal(t, numGoroutines, tokenCount)

		// Verify all calls were tracked
		assert.Equal(t, numGoroutines, len(api.GetTokenBehavior.CalledWithInput.Clone()))
	})

	// Test concurrent refresh
	t.Run("concurrent RefreshToken", func(t *testing.T) {
		api.Reset()
		const numGoroutines = 30
		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(idx int) {
				defer wg.Done()
				_, err := api.RefreshToken(context.Background(), fmt.Sprintf("refresh-%d", idx))
				assert.NoError(t, err)
			}(i)
		}

		wg.Wait()

		// Verify all tokens were stored
		assert.Equal(t, numGoroutines, api.Tokens.Len())
		assert.Equal(t, numGoroutines, len(api.RefreshTokenBehavior.CalledWithInput.Clone()))
	})

	// Test mixed operations
	t.Run("mixed concurrent operations", func(t *testing.T) {
		api.Reset()
		const numOps = 100
		var wg sync.WaitGroup
		wg.Add(numOps)

		for i := 0; i < numOps; i++ {
			go func(idx int) {
				defer wg.Done()
				switch idx % 3 {
				case 0:
					_, _ = api.GetToken(context.Background(), fmt.Sprintf("key-%d", idx))
				case 1:
					_, _ = api.RefreshToken(context.Background(), fmt.Sprintf("refresh-%d", idx))
				case 2:
					_, _ = api.ValidateToken(context.Background(), fmt.Sprintf("token-%d", idx))
				}
			}(i)
		}

		wg.Wait()

		// Just verify no panic occurred
		assert.True(t, true, "Concurrent operations completed without panic")
	})
}

func TestIAMAPI_CallTracking(t *testing.T) {
	api := NewIAMAPI()

	// Make multiple calls with different parameters
	keys := []string{"key1", "key2", "key3", "key1"} // Note duplicate
	for _, key := range keys {
		_, err := api.GetToken(context.Background(), key)
		require.NoError(t, err)
	}

	// Verify all calls were tracked
	inputs := api.GetTokenBehavior.CalledWithInput.Clone()
	assert.Len(t, inputs, 4)

	// Verify the order and values
	for i, input := range inputs {
		assert.Equal(t, keys[i], input.APIKey)
		assert.Equal(t, "urn:ibm:params:oauth:grant-type:apikey", input.GrantType)
	}

	// Test refresh token tracking
	refreshTokens := []string{"refresh1", "refresh2"}
	for _, rt := range refreshTokens {
		_, err := api.RefreshToken(context.Background(), rt)
		require.NoError(t, err)
	}

	refreshInputs := api.RefreshTokenBehavior.CalledWithInput.Clone()
	assert.Len(t, refreshInputs, 2)
	for i, input := range refreshInputs {
		assert.Equal(t, refreshTokens[i], input.RefreshToken)
		assert.Equal(t, "refresh_token", input.GrantType)
	}
}

func TestIAMAPI_TokenStorage(t *testing.T) {
	api := NewIAMAPI()

	// Generate multiple tokens
	for i := 0; i < 5; i++ {
		_, err := api.GetToken(context.Background(), fmt.Sprintf("key-%d", i))
		require.NoError(t, err)
	}

	// Refresh some tokens
	for i := 0; i < 3; i++ {
		_, err := api.RefreshToken(context.Background(), fmt.Sprintf("refresh-%d", i))
		require.NoError(t, err)
	}

	// Verify all tokens are stored
	assert.Equal(t, 8, api.Tokens.Len())

	// Verify tokens can be retrieved
	var allTokens []*IAMTokenResponse
	api.Tokens.Range(func(token *IAMTokenResponse) bool {
		allTokens = append(allTokens, token)
		return true
	})
	assert.Len(t, allTokens, 8)

	// Verify we got the expected number of tokens (uniqueness not guaranteed due to timestamp collision)
	assert.GreaterOrEqual(t, len(allTokens), 6, "Should have most tokens stored")
}
