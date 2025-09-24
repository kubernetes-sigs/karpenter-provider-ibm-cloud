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

// IKSBehavior controls the fake IKS API behavior for testing
type IKSBehavior struct {
	ListWorkerPoolsBehavior     MockedFunction[ListWorkerPoolsInput, []WorkerPool]
	GetWorkerPoolBehavior       MockedFunction[GetWorkerPoolInput, WorkerPool]
	PatchWorkerPoolBehavior     MockedFunction[PatchWorkerPoolInput, struct{}]
	ResizeWorkerPoolBehavior    MockedFunction[ResizeWorkerPoolInput, struct{}]
	RebalanceWorkerPoolBehavior MockedFunction[RebalanceWorkerPoolInput, struct{}]
	CreateWorkerPoolBehavior    MockedFunction[CreateWorkerPoolInput, WorkerPool]
	DeleteWorkerPoolBehavior    MockedFunction[DeleteWorkerPoolInput, struct{}]
	ListWorkersBehavior         MockedFunction[ListWorkersInput, []Worker]
	GetWorkerBehavior           MockedFunction[GetWorkerInput, Worker]
	DeleteWorkerBehavior        MockedFunction[DeleteWorkerInput, struct{}]

	WorkerPools atomic.Slice[*WorkerPool]
	Workers     atomic.Slice[*Worker]
	NextError   AtomicError
}

// Input types for IKS operations
type ListWorkerPoolsInput struct {
	ClusterID string
}

type GetWorkerPoolInput struct {
	ClusterID    string
	WorkerPoolID string
}

type PatchWorkerPoolInput struct {
	ClusterID    string
	WorkerPoolID string
	PatchRequest WorkerPoolPatchRequest
}

type ResizeWorkerPoolInput struct {
	ClusterID    string
	WorkerPoolID string
	Size         int
	Reason       string
}

type RebalanceWorkerPoolInput struct {
	ClusterID    string
	WorkerPoolID string
}

type CreateWorkerPoolInput struct {
	ClusterID string
	Request   WorkerPoolRequest
}

type DeleteWorkerPoolInput struct {
	ClusterID    string
	WorkerPoolID string
}

type ListWorkersInput struct {
	ClusterID    string
	WorkerPoolID string
}

type GetWorkerInput struct {
	ClusterID string
	WorkerID  string
}

type DeleteWorkerInput struct {
	ClusterID string
	WorkerID  string
}

// WorkerPoolRequest represents a request to create a worker pool
type WorkerPoolRequest struct {
	Name           string            `json:"name"`
	Flavor         string            `json:"flavor"`
	SizePerZone    int               `json:"sizePerZone"`
	Zones          []WorkerPoolZone  `json:"zones"`
	Labels         map[string]string `json:"labels,omitempty"`
	DiskEncryption bool              `json:"diskEncryption,omitempty"`
}

// IKSAPI implements a fake IKS API for testing
type IKSAPI struct {
	*IKSBehavior
	mu sync.RWMutex
}

// NewIKSAPI creates a new fake IKS API
func NewIKSAPI() *IKSAPI {
	return &IKSAPI{
		IKSBehavior: &IKSBehavior{},
	}
}

// ListWorkerPools lists worker pools for a cluster
func (f *IKSAPI) ListWorkerPools(ctx context.Context, clusterID string) ([]WorkerPool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.ListWorkerPoolsBehavior.CalledWithInput.Add(ListWorkerPoolsInput{ClusterID: clusterID})

	// Return mocked output if set
	if output := f.ListWorkerPoolsBehavior.Output.Get(); output != nil {
		return *output, nil
	}

	// Default behavior: filter pools by cluster ID
	var result []WorkerPool
	f.WorkerPools.Range(func(pool *WorkerPool) bool {
		if pool.ClusterID == clusterID {
			result = append(result, *pool)
		}
		return true
	})
	return result, nil
}

// GetWorkerPool gets a specific worker pool
func (f *IKSAPI) GetWorkerPool(ctx context.Context, clusterID, workerPoolID string) (*WorkerPool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.GetWorkerPoolBehavior.CalledWithInput.Add(GetWorkerPoolInput{
		ClusterID:    clusterID,
		WorkerPoolID: workerPoolID,
	})

	// Return mocked output if set
	if output := f.GetWorkerPoolBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: find the pool
	var foundPool *WorkerPool
	f.WorkerPools.Range(func(pool *WorkerPool) bool {
		if pool.ClusterID == clusterID && (pool.ID == workerPoolID || pool.Name == workerPoolID) {
			foundPool = pool
			return false // stop iteration
		}
		return true
	})

	if foundPool != nil {
		return foundPool, nil
	}
	return nil, fmt.Errorf("worker pool %s not found in cluster %s", workerPoolID, clusterID)
}

// PatchWorkerPool patches a worker pool (resize, rebalance, labels)
func (f *IKSAPI) PatchWorkerPool(ctx context.Context, clusterID, workerPoolID string, patch WorkerPoolPatchRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.NextError.Get(); err != nil {
		return err
	}

	// Track the call
	f.PatchWorkerPoolBehavior.CalledWithInput.Add(PatchWorkerPoolInput{
		ClusterID:    clusterID,
		WorkerPoolID: workerPoolID,
		PatchRequest: patch,
	})

	// Return mocked error if set
	if err := f.PatchWorkerPoolBehavior.Error.Get(); err != nil {
		return err
	}

	// Default behavior: update the pool
	var found bool
	var updatedPoolID string

	// First, find and update the pool
	f.WorkerPools.Range(func(pool *WorkerPool) bool {
		if pool.ClusterID == clusterID && (pool.ID == workerPoolID || pool.Name == workerPoolID) {
			switch patch.State {
			case "resizing":
				if patch.SizePerZone > 0 {
					pool.SizePerZone = patch.SizePerZone
				}
				pool.State = WorkerPoolStateResizing
			case "rebalancing":
				pool.State = WorkerPoolStateRebalancing
			case "labels":
				if patch.Labels != nil {
					pool.Labels = patch.Labels
				}
			}

			pool.UpdatedDate = time.Now()
			updatedPoolID = pool.ID
			found = true
			return false // stop iteration
		}
		return true
	})

	if !found {
		return fmt.Errorf("worker pool %s not found in cluster %s", workerPoolID, clusterID)
	}

	// Simulate async completion after a delay
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.WorkerPools.Range(func(p *WorkerPool) bool {
			if p.ID == updatedPoolID {
				p.State = WorkerPoolStateNormal
				return false
			}
			return true
		})
	}()

	return nil
}

// ResizeWorkerPool resizes a worker pool (v2 API)
func (f *IKSAPI) ResizeWorkerPool(ctx context.Context, clusterID, workerPoolID string, size int, reason string) error {
	f.mu.Lock()

	if err := f.NextError.Get(); err != nil {
		f.mu.Unlock()
		return err
	}

	// Track the call
	f.ResizeWorkerPoolBehavior.CalledWithInput.Add(ResizeWorkerPoolInput{
		ClusterID:    clusterID,
		WorkerPoolID: workerPoolID,
		Size:         size,
		Reason:       reason,
	})

	f.mu.Unlock() // Release lock before calling PatchWorkerPool

	// Default behavior: delegate to PatchWorkerPool
	return f.PatchWorkerPool(ctx, clusterID, workerPoolID, WorkerPoolPatchRequest{
		State:           "resizing",
		SizePerZone:     size,
		ReasonForResize: reason,
	})
}

// RebalanceWorkerPool rebalances a worker pool
func (f *IKSAPI) RebalanceWorkerPool(ctx context.Context, clusterID, workerPoolID string) error {
	f.mu.Lock()

	if err := f.NextError.Get(); err != nil {
		f.mu.Unlock()
		return err
	}

	// Track the call
	f.RebalanceWorkerPoolBehavior.CalledWithInput.Add(RebalanceWorkerPoolInput{
		ClusterID:    clusterID,
		WorkerPoolID: workerPoolID,
	})

	f.mu.Unlock() // Release lock before calling PatchWorkerPool

	return f.PatchWorkerPool(ctx, clusterID, workerPoolID, WorkerPoolPatchRequest{
		State: "rebalancing",
	})
}

// CreateWorkerPool creates a new worker pool
func (f *IKSAPI) CreateWorkerPool(ctx context.Context, clusterID string, request WorkerPoolRequest) (*WorkerPool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.CreateWorkerPoolBehavior.CalledWithInput.Add(CreateWorkerPoolInput{
		ClusterID: clusterID,
		Request:   request,
	})

	// Return mocked output if set
	if output := f.CreateWorkerPoolBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: create a new pool
	poolID := fmt.Sprintf("pool-%d", f.WorkerPools.Len()+1)
	// For single zone, use the first zone from the request
	zone := "us-south-1" // default
	if len(request.Zones) > 0 {
		zone = request.Zones[0].ID
	}

	pool := &WorkerPool{
		ID:          poolID,
		Name:        request.Name,
		ClusterID:   clusterID,
		Flavor:      request.Flavor,
		Zone:        zone,
		SizePerZone: request.SizePerZone,
		ActualSize:  request.SizePerZone, // Initially matches requested size
		State:       WorkerPoolStateNormal,
		Zones:       request.Zones,
		Labels:      request.Labels,
		CreatedDate: time.Now(),
		UpdatedDate: time.Now(),
	}

	f.WorkerPools.Add(pool)
	return pool, nil
}

// DeleteWorkerPool deletes a worker pool
func (f *IKSAPI) DeleteWorkerPool(ctx context.Context, clusterID, workerPoolID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.NextError.Get(); err != nil {
		return err
	}

	// Track the call
	f.DeleteWorkerPoolBehavior.CalledWithInput.Add(DeleteWorkerPoolInput{
		ClusterID:    clusterID,
		WorkerPoolID: workerPoolID,
	})

	// Default behavior: remove the pool by resetting and re-adding all except the target
	var poolsToKeep []*WorkerPool
	f.WorkerPools.Range(func(pool *WorkerPool) bool {
		if pool.ClusterID != clusterID || (pool.ID != workerPoolID && pool.Name != workerPoolID) {
			poolsToKeep = append(poolsToKeep, pool)
		}
		return true
	})

	f.WorkerPools.Reset()
	for _, pool := range poolsToKeep {
		f.WorkerPools.Add(pool)
	}
	return nil
}

// ListWorkers lists workers in a cluster or worker pool
func (f *IKSAPI) ListWorkers(ctx context.Context, clusterID, workerPoolID string) ([]Worker, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.ListWorkersBehavior.CalledWithInput.Add(ListWorkersInput{
		ClusterID:    clusterID,
		WorkerPoolID: workerPoolID,
	})

	// Return mocked output if set
	if output := f.ListWorkersBehavior.Output.Get(); output != nil {
		return *output, nil
	}

	// Default behavior: filter workers
	var result []Worker
	f.Workers.Range(func(worker *Worker) bool {
		// Filter by cluster ID and optionally by pool ID
		if len(worker.ID) > 0 &&
			(workerPoolID == "" || worker.PoolID == workerPoolID) {
			result = append(result, *worker)
		}
		return true
	})
	return result, nil
}

// GetWorker gets a specific worker
func (f *IKSAPI) GetWorker(ctx context.Context, clusterID, workerID string) (*Worker, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if err := f.NextError.Get(); err != nil {
		return nil, err
	}

	// Track the call
	f.GetWorkerBehavior.CalledWithInput.Add(GetWorkerInput{
		ClusterID: clusterID,
		WorkerID:  workerID,
	})

	// Return mocked output if set
	if output := f.GetWorkerBehavior.Output.Get(); output != nil {
		return output, nil
	}

	// Default behavior: find the worker
	var foundWorker *Worker
	f.Workers.Range(func(worker *Worker) bool {
		if worker.ID == workerID {
			foundWorker = worker
			return false // stop iteration
		}
		return true
	})

	if foundWorker != nil {
		return foundWorker, nil
	}
	return nil, fmt.Errorf("worker %s not found", workerID)
}

// DeleteWorker deletes a worker
func (f *IKSAPI) DeleteWorker(ctx context.Context, clusterID, workerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.NextError.Get(); err != nil {
		return err
	}

	// Track the call
	f.DeleteWorkerBehavior.CalledWithInput.Add(DeleteWorkerInput{
		ClusterID: clusterID,
		WorkerID:  workerID,
	})

	// Default behavior: remove the worker
	var workersToKeep []*Worker
	f.Workers.Range(func(worker *Worker) bool {
		if worker.ID != workerID {
			workersToKeep = append(workersToKeep, worker)
		}
		return true
	})

	f.Workers.Reset()
	for _, worker := range workersToKeep {
		f.Workers.Add(worker)
	}
	return nil
}

// Reset resets the fake API state
func (f *IKSAPI) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.WorkerPools.Reset()
	f.Workers.Reset()
	f.NextError.Store(nil)

	// Reset all behavior mocks
	f.ListWorkerPoolsBehavior.Reset()
	f.GetWorkerPoolBehavior.Reset()
	f.PatchWorkerPoolBehavior.Reset()
	f.ResizeWorkerPoolBehavior.Reset()
	f.RebalanceWorkerPoolBehavior.Reset()
	f.CreateWorkerPoolBehavior.Reset()
	f.DeleteWorkerPoolBehavior.Reset()
	f.ListWorkersBehavior.Reset()
	f.GetWorkerBehavior.Reset()
	f.DeleteWorkerBehavior.Reset()
}
