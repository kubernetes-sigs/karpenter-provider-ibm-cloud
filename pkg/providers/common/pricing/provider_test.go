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
	"testing"
)

// MockProvider implements the Provider interface for testing
type MockProvider struct {
	prices       map[string]map[string]float64 // zone -> instanceType -> price
	refreshError error
}

func NewMockProvider() *MockProvider {
	return &MockProvider{
		prices: make(map[string]map[string]float64),
	}
}

func (m *MockProvider) GetPrice(ctx context.Context, instanceType string, zone string) (float64, error) {
	if zonePrices, exists := m.prices[zone]; exists {
		if price, exists := zonePrices[instanceType]; exists {
			return price, nil
		}
	}
	return 0, nil
}

func (m *MockProvider) GetPrices(ctx context.Context, zone string) (map[string]float64, error) {
	if zonePrices, exists := m.prices[zone]; exists {
		result := make(map[string]float64)
		for instanceType, price := range zonePrices {
			result[instanceType] = price
		}
		return result, nil
	}
	return make(map[string]float64), nil
}

func (m *MockProvider) Refresh(ctx context.Context) error {
	return m.refreshError
}

func (m *MockProvider) SetPrice(zone, instanceType string, price float64) {
	if _, exists := m.prices[zone]; !exists {
		m.prices[zone] = make(map[string]float64)
	}
	m.prices[zone][instanceType] = price
}

func (m *MockProvider) SetRefreshError(err error) {
	m.refreshError = err
}

func TestProviderInterface(t *testing.T) {
	ctx := context.Background()
	provider := NewMockProvider()

	// Test interface methods exist and can be called
	_, err := provider.GetPrice(ctx, "bx2-2x8", "us-south-1")
	if err != nil {
		t.Errorf("GetPrice should not return error for mock provider: %v", err)
	}

	_, err = provider.GetPrices(ctx, "us-south-1")
	if err != nil {
		t.Errorf("GetPrices should not return error for mock provider: %v", err)
	}

	err = provider.Refresh(ctx)
	if err != nil {
		t.Errorf("Refresh should not return error for mock provider: %v", err)
	}
}

func TestMockProviderGetPrice(t *testing.T) {
	ctx := context.Background()
	provider := NewMockProvider()

	// Test empty provider
	price, err := provider.GetPrice(ctx, "bx2-2x8", "us-south-1")
	if err != nil {
		t.Errorf("GetPrice should not error on empty provider: %v", err)
	}
	if price != 0 {
		t.Errorf("GetPrice should return 0 for non-existent price, got %f", price)
	}

	// Test with set price
	expectedPrice := 0.096
	provider.SetPrice("us-south-1", "bx2-2x8", expectedPrice)

	price, err = provider.GetPrice(ctx, "bx2-2x8", "us-south-1")
	if err != nil {
		t.Errorf("GetPrice should not error: %v", err)
	}
	if price != expectedPrice {
		t.Errorf("GetPrice returned %f, want %f", price, expectedPrice)
	}

	// Test non-existent zone
	price, err = provider.GetPrice(ctx, "bx2-2x8", "non-existent-zone")
	if err != nil {
		t.Errorf("GetPrice should not error for non-existent zone: %v", err)
	}
	if price != 0 {
		t.Errorf("GetPrice should return 0 for non-existent zone, got %f", price)
	}
}

func TestMockProviderGetPrices(t *testing.T) {
	ctx := context.Background()
	provider := NewMockProvider()

	// Test empty provider
	prices, err := provider.GetPrices(ctx, "us-south-1")
	if err != nil {
		t.Errorf("GetPrices should not error on empty provider: %v", err)
	}
	if len(prices) != 0 {
		t.Errorf("GetPrices should return empty map for non-existent zone, got %d items", len(prices))
	}

	// Test with set prices
	provider.SetPrice("us-south-1", "bx2-2x8", 0.096)
	provider.SetPrice("us-south-1", "bx2-4x16", 0.192)
	provider.SetPrice("us-east-1", "bx2-2x8", 0.100)

	prices, err = provider.GetPrices(ctx, "us-south-1")
	if err != nil {
		t.Errorf("GetPrices should not error: %v", err)
	}
	if len(prices) != 2 {
		t.Errorf("GetPrices should return 2 prices for us-south-1, got %d", len(prices))
	}
	if prices["bx2-2x8"] != 0.096 {
		t.Errorf("GetPrices should return 0.096 for bx2-2x8, got %f", prices["bx2-2x8"])
	}
	if prices["bx2-4x16"] != 0.192 {
		t.Errorf("GetPrices should return 0.192 for bx2-4x16, got %f", prices["bx2-4x16"])
	}
}

func TestPriceStruct(t *testing.T) {
	price := Price{
		InstanceType: "bx2-2x8",
		Zone:         "us-south-1",
		HourlyPrice:  0.096,
		Currency:     "USD",
	}

	if price.InstanceType != "bx2-2x8" {
		t.Errorf("Price.InstanceType = %v, want bx2-2x8", price.InstanceType)
	}
	if price.Zone != "us-south-1" {
		t.Errorf("Price.Zone = %v, want us-south-1", price.Zone)
	}
	if price.HourlyPrice != 0.096 {
		t.Errorf("Price.HourlyPrice = %v, want 0.096", price.HourlyPrice)
	}
	if price.Currency != "USD" {
		t.Errorf("Price.Currency = %v, want USD", price.Currency)
	}
}

func TestPriceStructZeroValues(t *testing.T) {
	price := Price{}

	if price.InstanceType != "" {
		t.Errorf("Price.InstanceType zero value = %v, want empty string", price.InstanceType)
	}
	if price.Zone != "" {
		t.Errorf("Price.Zone zero value = %v, want empty string", price.Zone)
	}
	if price.HourlyPrice != 0 {
		t.Errorf("Price.HourlyPrice zero value = %v, want 0", price.HourlyPrice)
	}
	if price.Currency != "" {
		t.Errorf("Price.Currency zero value = %v, want empty string", price.Currency)
	}
}
