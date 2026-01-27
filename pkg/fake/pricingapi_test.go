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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPricingAPI_GetPricing(t *testing.T) {
	ctx := context.Background()

	t.Run("returns configured pricing", func(t *testing.T) {
		api := NewPricingAPI()
		api.PricingByID["test-id"] = NewPricingGet(0.25)

		result, err := api.GetPricing(ctx, "test-id")

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, int64(1), api.GetCallCount())
	})

	t.Run("returns configured error", func(t *testing.T) {
		api := NewPricingAPI()
		api.ErrorByID["error-id"] = errors.New("test error")

		result, err := api.GetPricing(ctx, "error-id")

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "test error")
	})

	t.Run("returns empty pricing by default", func(t *testing.T) {
		api := NewPricingAPI()

		result, err := api.GetPricing(ctx, "unknown-id")

		require.NoError(t, err)
		require.NotNil(t, result)
	})
}

func TestNewPricingGet(t *testing.T) {
	pricing := NewPricingGet(0.15)

	require.NotNil(t, pricing)
	require.NotNil(t, pricing.Metrics)
	require.Len(t, pricing.Metrics, 1)
	require.NotNil(t, pricing.Metrics[0].Amounts)
	require.Len(t, pricing.Metrics[0].Amounts, 1)
	assert.Equal(t, "USA", *pricing.Metrics[0].Amounts[0].Country)
	require.NotNil(t, pricing.Metrics[0].Amounts[0].Prices)
	require.Len(t, pricing.Metrics[0].Amounts[0].Prices, 1)
	assert.Equal(t, 0.15, *pricing.Metrics[0].Amounts[0].Prices[0].Price)
}
