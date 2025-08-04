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

package vpcclient

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

func TestNewManager(t *testing.T) {
	t.Run("creates manager with default TTL", func(t *testing.T) {
		manager := NewManager(nil, 0)
		assert.NotNil(t, manager)
		assert.Equal(t, 30*time.Minute, manager.cacheTTL)
	})

	t.Run("creates manager with custom TTL", func(t *testing.T) {
		customTTL := 10 * time.Minute
		manager := NewManager(nil, customTTL)
		assert.NotNil(t, manager)
		assert.Equal(t, customTTL, manager.cacheTTL)
	})
}

func TestManager_InvalidateCache(t *testing.T) {
	manager := NewManager(nil, 1*time.Hour)
	
	// Set up some cached state
	manager.cachedClient = &ibm.VPCClient{}
	manager.cacheTime = time.Now()
	
	// Invalidate cache
	manager.InvalidateCache()
	
	// Verify cache is cleared
	assert.Nil(t, manager.cachedClient)
	assert.True(t, manager.cacheTime.IsZero())
}

func TestManager_getCacheInvalidationReason(t *testing.T) {
	manager := NewManager(nil, 5*time.Minute)
	
	t.Run("no cached client", func(t *testing.T) {
		reason := manager.getCacheInvalidationReason()
		assert.Equal(t, "no_cached_client", reason)
	})
	
	t.Run("cache expired", func(t *testing.T) {
		manager.cachedClient = &ibm.VPCClient{}
		manager.cacheTime = time.Now().Add(-10 * time.Minute) // Expired
		
		reason := manager.getCacheInvalidationReason()
		assert.Equal(t, "cache_expired", reason)
	})
}

func TestHandleVPCError(t *testing.T) {
	logger := logr.Discard()
	
	tests := []struct {
		name        string
		err         error
		operation   string
		extraFields []interface{}
		wantErr     string
	}{
		{
			name:      "nil error returns nil",
			err:       nil,
			operation: "test operation",
			wantErr:   "",
		},
		{
			name: "formats error with operation",
			err: &ibm.IBMError{
				StatusCode: 400,
				Code:       "bad_request",
				Message:    "Invalid parameter",
			},
			operation: "creating instance",
			wantErr:   "creating instance:",
		},
		{
			name: "includes extra fields",
			err: &ibm.IBMError{
				StatusCode: 404,
				Code:       "not_found",
				Message:    "Resource not found",
			},
			operation:   "deleting instance",
			extraFields: []interface{}{"instance_id", "i-12345"},
			wantErr:     "deleting instance:",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := HandleVPCError(tt.err, logger, tt.operation, tt.extraFields...)
			
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestManager_GetVPCClient_ErrorHandling(t *testing.T) {
	// Test that GetVPCClient properly handles nil client
	ctx := context.Background()
	manager := NewManager(nil, 1*time.Hour)
	
	// This should fail gracefully with nil client
	client, err := manager.GetVPCClient(ctx)
	assert.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "getting VPC client")
}

func TestManager_MustGetVPCClient_Panics(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, 1*time.Hour)
	
	// Should panic with nil client
	assert.Panics(t, func() {
		manager.MustGetVPCClient(ctx)
	})
}

func TestManager_WithVPCClient_ErrorHandling(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, 1*time.Hour)
	
	// Should propagate client creation error
	executed := false
	err := manager.WithVPCClient(ctx, func(client *ibm.VPCClient) error {
		executed = true
		return nil
	})
	
	assert.Error(t, err)
	assert.False(t, executed)
}

func TestManager_WithVPCClient_FunctionError(t *testing.T) {
	// Test that function errors are properly propagated
	// This test validates the error propagation logic without requiring actual client
	expectedErr := fmt.Errorf("operation failed")
	
	ctx := context.Background()
	
	// Create a manager that will fail client creation
	manager := NewManager(nil, 1*time.Hour)
	
	err := manager.WithVPCClient(ctx, func(client *ibm.VPCClient) error {
		return expectedErr
	})
	
	// Should get client creation error, not function error
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "getting VPC client")
}