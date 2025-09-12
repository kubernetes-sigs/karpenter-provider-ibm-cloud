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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWorkerPoolState(t *testing.T) {
	tests := []struct {
		name     string
		state    WorkerPoolState
		expected string
	}{
		{
			name:     "normal state",
			state:    WorkerPoolStateNormal,
			expected: "normal",
		},
		{
			name:     "resizing state",
			state:    WorkerPoolStateResizing,
			expected: "resizing",
		},
		{
			name:     "rebalancing state",
			state:    WorkerPoolStateRebalancing,
			expected: "rebalancing",
		},
		{
			name:     "deleting state",
			state:    WorkerPoolStateDeleting,
			expected: "deleting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.state))
		})
	}
}

func TestWorkerPool(t *testing.T) {
	t.Run("create worker pool with required fields", func(t *testing.T) {
		now := time.Now()

		wp := WorkerPool{
			ID:               "test-pool-id",
			Name:             "test-pool",
			ClusterID:        "test-cluster-id",
			Flavor:           "bx2-4x16",
			Zone:             "us-south-1",
			SizePerZone:      2,
			ActualSize:       2,
			State:            WorkerPoolStateNormal,
			AutoscaleEnabled: true,
			MinSizePerZone:   1,
			MaxSizePerZone:   10,
			Labels:           map[string]string{"test": "value"},
			CreatedDate:      now,
			UpdatedDate:      now,
		}

		assert.Equal(t, "test-pool-id", wp.ID)
		assert.Equal(t, "test-pool", wp.Name)
		assert.Equal(t, "test-cluster-id", wp.ClusterID)
		assert.Equal(t, "bx2-4x16", wp.Flavor)
		assert.Equal(t, "us-south-1", wp.Zone)
		assert.Equal(t, 2, wp.SizePerZone)
		assert.Equal(t, 2, wp.ActualSize)
		assert.Equal(t, WorkerPoolStateNormal, wp.State)
		assert.True(t, wp.AutoscaleEnabled)
		assert.Equal(t, 1, wp.MinSizePerZone)
		assert.Equal(t, 10, wp.MaxSizePerZone)
		assert.Contains(t, wp.Labels, "test")
		assert.Equal(t, "value", wp.Labels["test"])
		assert.Equal(t, now, wp.CreatedDate)
		assert.Equal(t, now, wp.UpdatedDate)
	})

	t.Run("worker pool with empty zones", func(t *testing.T) {
		wp := WorkerPool{
			ID:    "test-id",
			Zones: []WorkerPoolZone{},
		}

		assert.Empty(t, wp.Zones)
		assert.NotNil(t, wp.Zones)
	})
}
