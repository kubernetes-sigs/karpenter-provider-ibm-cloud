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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIKSAPI_ListWorkerPools(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*IKSAPI)
		clusterID    string
		expectError  bool
		validateFunc func(*testing.T, []WorkerPool, *IKSAPI)
	}{
		{
			name: "list pools for specific cluster",
			setupFunc: func(api *IKSAPI) {
				api.WorkerPools.Add(&WorkerPool{
					ID:         "pool-1",
					ClusterID:  "cluster-1",
					Name:       "default",
					ActualSize: 3,
				})
				api.WorkerPools.Add(&WorkerPool{
					ID:         "pool-2",
					ClusterID:  "cluster-1",
					Name:       "compute",
					ActualSize: 5,
				})
				api.WorkerPools.Add(&WorkerPool{
					ID:         "pool-3",
					ClusterID:  "cluster-2",
					Name:       "other",
					ActualSize: 2,
				})
			},
			clusterID:   "cluster-1",
			expectError: false,
			validateFunc: func(t *testing.T, pools []WorkerPool, api *IKSAPI) {
				assert.Len(t, pools, 2)
				for _, pool := range pools {
					assert.Equal(t, "cluster-1", pool.ClusterID)
				}
			},
		},
		{
			name:        "empty cluster returns empty list",
			clusterID:   "non-existent",
			expectError: false,
			validateFunc: func(t *testing.T, pools []WorkerPool, api *IKSAPI) {
				assert.Empty(t, pools)
			},
		},
		{
			name: "with mocked output",
			setupFunc: func(api *IKSAPI) {
				mockedPools := []WorkerPool{
					{ID: "mocked-1", Name: "mocked-pool-1"},
					{ID: "mocked-2", Name: "mocked-pool-2"},
				}
				api.ListWorkerPoolsBehavior.Output.Store(&mockedPools)
			},
			clusterID:   "any-cluster",
			expectError: false,
			validateFunc: func(t *testing.T, pools []WorkerPool, api *IKSAPI) {
				assert.Len(t, pools, 2)
				assert.Equal(t, "mocked-1", pools[0].ID)
				assert.Equal(t, "mocked-2", pools[1].ID)
			},
		},
		{
			name: "error during list",
			setupFunc: func(api *IKSAPI) {
				api.NextError.Store(fmt.Errorf("list failed"))
			},
			clusterID:   "cluster-1",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewIKSAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			pools, err := api.ListWorkerPools(context.Background(), tt.clusterID)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, the call might not be tracked
				return
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, pools, api)
				}
			}

			// Verify the call was tracked (only for successful calls)
			inputs := api.ListWorkerPoolsBehavior.CalledWithInput.Clone()
			assert.Len(t, inputs, 1)
			assert.Equal(t, tt.clusterID, inputs[0].ClusterID)
		})
	}
}

func TestIKSAPI_GetWorkerPool(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*IKSAPI)
		clusterID    string
		workerPoolID string
		expectError  bool
		validateFunc func(*testing.T, *WorkerPool)
	}{
		{
			name: "get existing worker pool",
			setupFunc: func(api *IKSAPI) {
				api.WorkerPools.Add(&WorkerPool{
					ID:         "pool-1",
					ClusterID:  "cluster-1",
					Name:       "default",
					ActualSize: 3,
					Flavor:     "bx2.4x16",
				})
			},
			clusterID:    "cluster-1",
			workerPoolID: "pool-1",
			expectError:  false,
			validateFunc: func(t *testing.T, pool *WorkerPool) {
				assert.Equal(t, "pool-1", pool.ID)
				assert.Equal(t, "default", pool.Name)
				assert.Equal(t, 3, pool.ActualSize)
				assert.Equal(t, "bx2.4x16", pool.Flavor)
			},
		},
		{
			name:         "get non-existent pool",
			clusterID:    "cluster-1",
			workerPoolID: "non-existent",
			expectError:  true,
		},
		{
			name: "with mocked output",
			setupFunc: func(api *IKSAPI) {
				mockedPool := &WorkerPool{
					ID:         "mocked-pool",
					Name:       "mocked",
					ActualSize: 10,
					Flavor:     "cx2.8x16",
				}
				api.GetWorkerPoolBehavior.Output.Store(mockedPool)
			},
			clusterID:    "any-cluster",
			workerPoolID: "any-pool",
			expectError:  false,
			validateFunc: func(t *testing.T, pool *WorkerPool) {
				assert.Equal(t, "mocked-pool", pool.ID)
				assert.Equal(t, 10, pool.ActualSize)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewIKSAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			pool, err := api.GetWorkerPool(context.Background(), tt.clusterID, tt.workerPoolID)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, pool)
				}
			}

			// Verify the call was tracked
			inputs := api.GetWorkerPoolBehavior.CalledWithInput.Clone()
			assert.Len(t, inputs, 1)
			assert.Equal(t, tt.clusterID, inputs[0].ClusterID)
			assert.Equal(t, tt.workerPoolID, inputs[0].WorkerPoolID)
		})
	}
}

func TestIKSAPI_ResizeWorkerPool(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*IKSAPI)
		clusterID    string
		workerPoolID string
		newSize      int
		reason       string
		expectError  bool
		validateFunc func(*testing.T, *IKSAPI)
	}{
		{
			name: "successful resize",
			setupFunc: func(api *IKSAPI) {
				api.WorkerPools.Add(&WorkerPool{
					ID:         "pool-1",
					ClusterID:  "cluster-1",
					Name:       "default",
					ActualSize: 3,
				})
			},
			clusterID:    "cluster-1",
			workerPoolID: "pool-1",
			newSize:      5,
			reason:       "scale up for load",
			expectError:  false,
			validateFunc: func(t *testing.T, api *IKSAPI) {
				// In real implementation, this would update the pool size
				// For testing, we just verify the call was made
				inputs := api.ResizeWorkerPoolBehavior.CalledWithInput.Clone()
				assert.Len(t, inputs, 1)
				assert.Equal(t, 5, inputs[0].Size)
				assert.Equal(t, "scale up for load", inputs[0].Reason)
			},
		},
		{
			name: "error during resize",
			setupFunc: func(api *IKSAPI) {
				api.NextError.Store(fmt.Errorf("resize failed"))
			},
			clusterID:    "cluster-1",
			workerPoolID: "pool-1",
			newSize:      5,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewIKSAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			err := api.ResizeWorkerPool(context.Background(), tt.clusterID, tt.workerPoolID, tt.newSize, tt.reason)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, the call might not be tracked
				return
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, api)
				}
			}
		})
	}
}

func TestIKSAPI_CreateWorkerPool(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*IKSAPI)
		clusterID    string
		request      WorkerPoolRequest
		expectError  bool
		validateFunc func(*testing.T, *WorkerPool, *IKSAPI)
	}{
		{
			name:      "successful pool creation",
			clusterID: "cluster-1",
			request: WorkerPoolRequest{
				Name:        "new-pool",
				Flavor:      "bx2.8x32",
				SizePerZone: 2,
				Zones: []WorkerPoolZone{
					{ID: "us-south-1"},
					{ID: "us-south-2"},
				},
				Labels: map[string]string{
					"env": "production",
				},
			},
			expectError: false,
			validateFunc: func(t *testing.T, pool *WorkerPool, api *IKSAPI) {
				assert.NotEmpty(t, pool.ID)
				assert.Equal(t, "new-pool", pool.Name)
				assert.Equal(t, "bx2.8x32", pool.Flavor)
				assert.Equal(t, 2, pool.ActualSize) // Initially matches SizePerZone

				// Verify pool was added to store
				assert.Equal(t, 1, api.WorkerPools.Len())
			},
		},
		{
			name: "with mocked output",
			setupFunc: func(api *IKSAPI) {
				mockedPool := &WorkerPool{
					ID:         "mocked-pool-id",
					Name:       "mocked-pool",
					ActualSize: 10,
					Flavor:     "mocked-flavor",
				}
				api.CreateWorkerPoolBehavior.Output.Store(mockedPool)
			},
			clusterID: "cluster-1",
			request: WorkerPoolRequest{
				Name: "test-pool",
			},
			expectError: false,
			validateFunc: func(t *testing.T, pool *WorkerPool, api *IKSAPI) {
				assert.Equal(t, "mocked-pool-id", pool.ID)
				assert.Equal(t, "mocked-pool", pool.Name)
			},
		},
		{
			name: "error during creation",
			setupFunc: func(api *IKSAPI) {
				api.NextError.Store(fmt.Errorf("creation failed"))
			},
			clusterID: "cluster-1",
			request: WorkerPoolRequest{
				Name: "test-pool",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewIKSAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			pool, err := api.CreateWorkerPool(context.Background(), tt.clusterID, tt.request)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, the call might not be tracked
				return
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, pool, api)
				}
			}

			// Verify the call was tracked (only for successful calls)
			inputs := api.CreateWorkerPoolBehavior.CalledWithInput.Clone()
			assert.Len(t, inputs, 1)
			assert.Equal(t, tt.clusterID, inputs[0].ClusterID)
			assert.Equal(t, tt.request.Name, inputs[0].Request.Name)
		})
	}
}

func TestIKSAPI_DeleteWorkerPool(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(*IKSAPI)
		clusterID    string
		workerPoolID string
		expectError  bool
		validateFunc func(*testing.T, *IKSAPI)
	}{
		{
			name: "successful deletion",
			setupFunc: func(api *IKSAPI) {
				api.WorkerPools.Add(&WorkerPool{
					ID:        "pool-1",
					ClusterID: "cluster-1",
					Name:      "to-delete",
				})
				api.WorkerPools.Add(&WorkerPool{
					ID:        "pool-2",
					ClusterID: "cluster-1",
					Name:      "to-keep",
				})
			},
			clusterID:    "cluster-1",
			workerPoolID: "pool-1",
			expectError:  false,
			validateFunc: func(t *testing.T, api *IKSAPI) {
				// Verify pool was removed
				assert.Equal(t, 1, api.WorkerPools.Len())

				var remaining []*WorkerPool
				api.WorkerPools.Range(func(pool *WorkerPool) bool {
					remaining = append(remaining, pool)
					return true
				})
				assert.Equal(t, "pool-2", remaining[0].ID)
			},
		},
		{
			name: "error during deletion",
			setupFunc: func(api *IKSAPI) {
				api.NextError.Store(fmt.Errorf("deletion failed"))
			},
			clusterID:    "cluster-1",
			workerPoolID: "pool-1",
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := NewIKSAPI()
			if tt.setupFunc != nil {
				tt.setupFunc(api)
			}

			err := api.DeleteWorkerPool(context.Background(), tt.clusterID, tt.workerPoolID)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, the call might not be tracked
				return
			} else {
				require.NoError(t, err)
				if tt.validateFunc != nil {
					tt.validateFunc(t, api)
				}
			}

			// Verify the call was tracked (only for successful calls)
			inputs := api.DeleteWorkerPoolBehavior.CalledWithInput.Clone()
			assert.Len(t, inputs, 1)
			assert.Equal(t, tt.clusterID, inputs[0].ClusterID)
			assert.Equal(t, tt.workerPoolID, inputs[0].WorkerPoolID)
		})
	}
}

func TestIKSAPI_Workers(t *testing.T) {
	t.Run("ListWorkers", func(t *testing.T) {
		api := NewIKSAPI()

		// Add test workers
		api.Workers.Add(&Worker{
			ID:       "worker-1",
			PoolID:   "pool-1",
			PoolName: "pool-1",
			State:    "normal",
		})
		api.Workers.Add(&Worker{
			ID:       "worker-2",
			PoolID:   "pool-1",
			PoolName: "pool-1",
			State:    "normal",
		})
		api.Workers.Add(&Worker{
			ID:       "worker-3",
			PoolID:   "pool-2",
			PoolName: "pool-2",
			State:    "normal",
		})

		// List workers for specific pool
		workers, err := api.ListWorkers(context.Background(), "cluster-1", "pool-1")
		require.NoError(t, err)
		assert.Len(t, workers, 2)

		// List workers for non-existent pool
		workers, err = api.ListWorkers(context.Background(), "cluster-1", "non-existent")
		require.NoError(t, err)
		assert.Empty(t, workers)
	})

	t.Run("GetWorker", func(t *testing.T) {
		api := NewIKSAPI()

		// Add test worker
		api.Workers.Add(&Worker{
			ID:     "worker-1",
			PoolID: "pool-1",
			State:  "normal",
		})

		// Get existing worker
		worker, err := api.GetWorker(context.Background(), "cluster-1", "worker-1")
		require.NoError(t, err)
		assert.Equal(t, "worker-1", worker.ID)
		assert.Equal(t, "normal", worker.State)

		// Get non-existent worker
		_, err = api.GetWorker(context.Background(), "cluster-1", "non-existent")
		assert.Error(t, err)
	})

	t.Run("DeleteWorker", func(t *testing.T) {
		api := NewIKSAPI()

		// Add test workers
		api.Workers.Add(&Worker{ID: "worker-1", PoolID: "pool-1"})
		api.Workers.Add(&Worker{ID: "worker-2", PoolID: "pool-1"})

		// Delete a worker
		err := api.DeleteWorker(context.Background(), "cluster-1", "worker-1")
		require.NoError(t, err)

		// Verify worker was removed
		assert.Equal(t, 1, api.Workers.Len())
		var remaining []*Worker
		api.Workers.Range(func(worker *Worker) bool {
			remaining = append(remaining, worker)
			return true
		})
		assert.Equal(t, "worker-2", remaining[0].ID)
	})
}

func TestIKSAPI_Reset(t *testing.T) {
	api := NewIKSAPI()

	// Add data to all stores
	api.WorkerPools.Add(&WorkerPool{ID: "pool-1"})
	api.Workers.Add(&Worker{ID: "worker-1"})
	api.NextError.Store(fmt.Errorf("test error"))

	// Track some calls
	api.ListWorkerPoolsBehavior.CalledWithInput.Add(ListWorkerPoolsInput{ClusterID: "test"})
	api.GetWorkerPoolBehavior.Output.Store(&WorkerPool{ID: "mocked"})

	// Reset everything
	api.Reset()

	// Verify all stores are empty
	assert.Equal(t, 0, api.WorkerPools.Len())
	assert.Equal(t, 0, api.Workers.Len())
	assert.Nil(t, api.NextError.Get())

	// Verify behaviors are reset
	assert.Len(t, api.ListWorkerPoolsBehavior.CalledWithInput.Clone(), 0)
	assert.Nil(t, api.GetWorkerPoolBehavior.Output.Get())
}

func TestIKSAPI_Concurrency(t *testing.T) {
	api := NewIKSAPI()

	t.Run("concurrent pool operations", func(t *testing.T) {
		const numOps = 50
		var wg sync.WaitGroup
		wg.Add(numOps * 3)

		// Concurrent creates
		for i := 0; i < numOps; i++ {
			go func(idx int) {
				defer wg.Done()
				request := WorkerPoolRequest{
					Name: fmt.Sprintf("pool-%d", idx),
				}
				_, err := api.CreateWorkerPool(context.Background(), "cluster-1", request)
				assert.NoError(t, err)
			}(i)
		}

		// Concurrent reads
		for i := 0; i < numOps; i++ {
			go func(idx int) {
				defer wg.Done()
				_, _ = api.ListWorkerPools(context.Background(), "cluster-1")
			}(i)
		}

		// Concurrent resizes
		for i := 0; i < numOps; i++ {
			go func(idx int) {
				defer wg.Done()
				_ = api.ResizeWorkerPool(context.Background(), "cluster-1", fmt.Sprintf("pool-%d", idx), idx, "test")
			}(i)
		}

		wg.Wait()

		// Verify operations were tracked
		assert.Equal(t, numOps, len(api.CreateWorkerPoolBehavior.CalledWithInput.Clone()))
		assert.Equal(t, numOps, len(api.ListWorkerPoolsBehavior.CalledWithInput.Clone()))
		assert.Equal(t, numOps, len(api.ResizeWorkerPoolBehavior.CalledWithInput.Clone()))
	})

	t.Run("concurrent worker operations", func(t *testing.T) {
		api.Reset()

		// Add initial workers
		for i := 0; i < 10; i++ {
			api.Workers.Add(&Worker{
				ID:     fmt.Sprintf("worker-%d", i),
				PoolID: fmt.Sprintf("pool-%d", i),
			})
		}

		const numOps = 30
		var wg sync.WaitGroup
		wg.Add(numOps * 2)

		// Concurrent reads
		for i := 0; i < numOps; i++ {
			go func(idx int) {
				defer wg.Done()
				_, _ = api.ListWorkers(context.Background(), "cluster-1", "pool-1")
			}(i)
		}

		// Concurrent deletes
		for i := 0; i < numOps; i++ {
			go func(idx int) {
				defer wg.Done()
				_ = api.DeleteWorker(context.Background(), "cluster-1", fmt.Sprintf("worker-%d", idx%10))
			}(i)
		}

		wg.Wait()

		// Verify no panic occurred
		assert.True(t, true, "Concurrent operations completed without panic")
	})
}

func TestIKSAPI_PatchWorkerPool(t *testing.T) {
	api := NewIKSAPI()

	// Setup test pool
	api.WorkerPools.Add(&WorkerPool{
		ID:        "pool-1",
		ClusterID: "cluster-1",
		Name:      "default",
		Labels: map[string]string{
			"env": "dev",
		},
	})

	// Patch the pool
	patchRequest := WorkerPoolPatchRequest{
		Labels: map[string]string{
			"env":  "prod",
			"team": "platform",
		},
	}

	err := api.PatchWorkerPool(context.Background(), "cluster-1", "pool-1", patchRequest)
	require.NoError(t, err)

	// Verify the call was tracked
	inputs := api.PatchWorkerPoolBehavior.CalledWithInput.Clone()
	assert.Len(t, inputs, 1)
	assert.Equal(t, "cluster-1", inputs[0].ClusterID)
	assert.Equal(t, "pool-1", inputs[0].WorkerPoolID)
	assert.Equal(t, patchRequest.Labels, inputs[0].PatchRequest.Labels)
}

func TestIKSAPI_RebalanceWorkerPool(t *testing.T) {
	api := NewIKSAPI()

	// Create a worker pool first
	pool := &WorkerPool{
		ID:        "pool-1",
		Name:      "test-pool",
		ClusterID: "cluster-1",
		State:     WorkerPoolStateNormal,
	}
	api.WorkerPools.Add(pool)

	// Test rebalance operation
	err := api.RebalanceWorkerPool(context.Background(), "cluster-1", "pool-1")
	require.NoError(t, err)

	// Verify the call was tracked
	inputs := api.RebalanceWorkerPoolBehavior.CalledWithInput.Clone()
	assert.Len(t, inputs, 1)
	assert.Equal(t, "cluster-1", inputs[0].ClusterID)
	assert.Equal(t, "pool-1", inputs[0].WorkerPoolID)

	// Test with error
	api.NextError.Store(fmt.Errorf("rebalance failed"))
	err = api.RebalanceWorkerPool(context.Background(), "cluster-1", "pool-2")
	assert.Error(t, err)
}
