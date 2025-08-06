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
package pricing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
)

// MockPricingProvider implements pricing.Provider for testing
type MockPricingProvider struct {
	refreshError error
	priceData    map[string]map[string]float64 // instanceType -> zone -> price
	refreshCount int
	mutex        sync.Mutex
}

func NewMockPricingProvider() *MockPricingProvider {
	return &MockPricingProvider{
		priceData: map[string]map[string]float64{
			"bx2-2x8": {
				"us-south-1": 0.096,
				"us-south-2": 0.096,
				"eu-de-1":    0.098,
			},
			"bx2-4x16": {
				"us-south-1": 0.192,
				"us-south-2": 0.192,
				"eu-de-1":    0.194,
			},
		},
	}
}

func (m *MockPricingProvider) GetPrice(ctx context.Context, instanceType string, zone string) (float64, error) {
	if zoneMap, exists := m.priceData[instanceType]; exists {
		if price, exists := zoneMap[zone]; exists {
			return price, nil
		}
	}
	return 0, errors.New("price not found")
}

func (m *MockPricingProvider) GetPrices(ctx context.Context, zone string) (map[string]float64, error) {
	prices := make(map[string]float64)
	for instanceType, zoneMap := range m.priceData {
		if price, exists := zoneMap[zone]; exists {
			prices[instanceType] = price
		}
	}
	if len(prices) == 0 {
		return nil, errors.New("no prices found for zone")
	}
	return prices, nil
}

func (m *MockPricingProvider) Refresh(ctx context.Context) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.refreshCount++
	return m.refreshError
}

func (m *MockPricingProvider) SetRefreshError(err error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.refreshError = err
}

func (m *MockPricingProvider) GetRefreshCount() int {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.refreshCount
}

func TestNewController(t *testing.T) {
	tests := []struct {
		name            string
		pricingProvider pricing.Provider
		expectError     bool
		expectNoOp      bool
	}{
		{
			name:            "with valid pricing provider",
			pricingProvider: NewMockPricingProvider(),
			expectError:     false,
			expectNoOp:      false,
		},
		{
			name:            "with nil pricing provider - should use NoOp",
			pricingProvider: nil,
			expectError:     false,
			expectNoOp:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller, err := NewController(tt.pricingProvider)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, controller)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, controller)
				assert.NotNil(t, controller.pricingProvider)

				if tt.expectNoOp {
					_, ok := controller.pricingProvider.(*NoOpPricingProvider)
					assert.True(t, ok, "Expected NoOpPricingProvider when nil provider is passed")
				}
			}
		})
	}
}

func TestControllerReconcile(t *testing.T) {
	tests := []struct {
		name            string
		refreshError    error
		expectedError   bool
		expectedRequeue bool
	}{
		{
			name:            "successful reconcile",
			refreshError:    nil,
			expectedError:   false,
			expectedRequeue: true,
		},
		{
			name:            "refresh error",
			refreshError:    errors.New("refresh failed"),
			expectedError:   true,
			expectedRequeue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockProvider := NewMockPricingProvider()
			mockProvider.SetRefreshError(tt.refreshError)

			controller, err := NewController(mockProvider)
			require.NoError(t, err)

			ctx := context.Background()
			result, err := controller.Reconcile(ctx)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "refreshing pricing information")
			} else {
				assert.NoError(t, err)
			}

			if tt.expectedRequeue {
				assert.Equal(t, 12*time.Hour, result.RequeueAfter)
			} else {
				assert.Zero(t, result.RequeueAfter)
			}

			// Verify refresh was called
			assert.Equal(t, 1, mockProvider.GetRefreshCount())
		})
	}
}

func TestControllerReconcileCallsRefresh(t *testing.T) {
	mockProvider := NewMockPricingProvider()
	controller, err := NewController(mockProvider)
	require.NoError(t, err)

	ctx := context.Background()

	// First reconcile
	result, err := controller.Reconcile(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 12*time.Hour, result.RequeueAfter)
	assert.Equal(t, 1, mockProvider.GetRefreshCount())

	// Second reconcile
	result, err = controller.Reconcile(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 12*time.Hour, result.RequeueAfter)
	assert.Equal(t, 2, mockProvider.GetRefreshCount())
}

func TestNoOpPricingProvider(t *testing.T) {
	provider := &NoOpPricingProvider{}
	ctx := context.Background()

	t.Run("GetPrice returns default", func(t *testing.T) {
		price, err := provider.GetPrice(ctx, "any-instance", "any-zone")
		assert.NoError(t, err)
		assert.Equal(t, 0.10, price)
	})

	t.Run("GetPrices returns empty map", func(t *testing.T) {
		prices, err := provider.GetPrices(ctx, "any-zone")
		assert.NoError(t, err)
		assert.Empty(t, prices)
	})

	t.Run("Refresh does nothing", func(t *testing.T) {
		err := provider.Refresh(ctx)
		assert.NoError(t, err)
	})
}

func TestControllerRegister(t *testing.T) {
	// This test verifies that the controller can be created successfully
	// The actual registration with manager is tested via integration tests

	controller, err := NewController(NewMockPricingProvider())
	require.NoError(t, err)
	assert.NotNil(t, controller)

	// Verify the controller has the required methods
	assert.NotNil(t, controller.Reconcile)
	assert.NotNil(t, controller.Register)
}

func TestControllerIntegration(t *testing.T) {
	// Integration test that verifies the full controller lifecycle
	mockProvider := NewMockPricingProvider()
	controller, err := NewController(mockProvider)
	require.NoError(t, err)

	ctx := context.Background()

	// Test multiple reconcile cycles
	for i := 1; i <= 3; i++ {
		result, reconcileErr := controller.Reconcile(ctx)
		assert.NoError(t, reconcileErr)
		assert.Equal(t, 12*time.Hour, result.RequeueAfter)
		assert.Equal(t, i, mockProvider.GetRefreshCount())
	}

	// Test error handling in middle of reconcile cycles
	mockProvider.SetRefreshError(errors.New("temporary error"))
	result, err := controller.Reconcile(ctx)
	assert.Error(t, err)
	assert.Zero(t, result.RequeueAfter)
	assert.Equal(t, 4, mockProvider.GetRefreshCount())

	// Test recovery after error
	mockProvider.SetRefreshError(nil)
	result, err = controller.Reconcile(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 12*time.Hour, result.RequeueAfter)
	assert.Equal(t, 5, mockProvider.GetRefreshCount())
}

func TestControllerWithNilProvider(t *testing.T) {
	// Test that controller works correctly with nil provider (should use NoOp)
	controller, err := NewController(nil)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := controller.Reconcile(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 12*time.Hour, result.RequeueAfter)

	// Verify it's using NoOpPricingProvider
	_, ok := controller.pricingProvider.(*NoOpPricingProvider)
	assert.True(t, ok)
}

func TestControllerConcurrency(t *testing.T) {
	// Test that controller handles concurrent reconcile calls safely
	mockProvider := NewMockPricingProvider()
	controller, err := NewController(mockProvider)
	require.NoError(t, err)

	ctx := context.Background()

	// Run multiple reconciles concurrently
	done := make(chan bool, 3)
	for i := 0; i < 3; i++ {
		go func() {
			result, err := controller.Reconcile(ctx)
			assert.NoError(t, err)
			assert.Equal(t, 12*time.Hour, result.RequeueAfter)
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 3; i++ {
		<-done
	}

	// Verify all reconciles were called
	assert.Equal(t, 3, mockProvider.GetRefreshCount())
}
