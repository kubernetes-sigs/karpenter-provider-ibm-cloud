/*
Copyright 2024 The Kubernetes Authors.

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

import "context"

// IKSClientInterface defines the interface for IKS client operations
// This interface allows for proper mocking in tests while maintaining
// type safety and clear contracts for IKS API operations.
type IKSClientInterface interface {
	// GetClusterConfig retrieves the kubeconfig for the specified cluster
	GetClusterConfig(ctx context.Context, clusterID string) (string, error)

	// GetWorkerDetails retrieves detailed information about an IKS worker
	GetWorkerDetails(ctx context.Context, clusterID, workerID string) (*IKSWorkerDetails, error)

	// GetVPCInstanceIDFromWorker extracts the VPC instance ID from worker details
	GetVPCInstanceIDFromWorker(ctx context.Context, clusterID, workerID string) (string, error)

	// ListWorkerPools retrieves all worker pools for a cluster
	ListWorkerPools(ctx context.Context, clusterID string) ([]*WorkerPool, error)

	// GetWorkerPool retrieves a specific worker pool
	GetWorkerPool(ctx context.Context, clusterID, poolID string) (*WorkerPool, error)

	// ResizeWorkerPool resizes a worker pool by adding one node
	ResizeWorkerPool(ctx context.Context, clusterID, poolID string, newSize int) error
}
