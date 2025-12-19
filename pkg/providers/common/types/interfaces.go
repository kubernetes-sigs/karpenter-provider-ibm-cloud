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

//go:generate go run go.uber.org/mock/mockgen@latest -source=./interfaces.go -destination=./mock/interfaces_generated.go -package=mock

package types

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// InstanceProvider defines the interface for managing compute instances
// This interface is implemented by both VPC and IKS providers
type InstanceProvider interface {
	// Create provisions a new compute instance based on the NodeClaim
	// For VPC providers: instanceTypes contains compatible types from Karpenter
	// For IKS providers: instanceTypes is unused (can be nil)
	Create(ctx context.Context, nodeClaim *v1.NodeClaim, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error)

	// Delete removes an existing compute instance
	Delete(ctx context.Context, node *corev1.Node) error

	// Get retrieves information about an existing compute instance
	Get(ctx context.Context, providerID string) (*corev1.Node, error)

	// List returns all compute instances managed by this provider
	List(ctx context.Context) ([]*corev1.Node, error)
}

// VPCInstanceProvider extends InstanceProvider with VPC-specific operations
type VPCInstanceProvider interface {
	InstanceProvider

	// UpdateTags updates tags on a VPC instance
	UpdateTags(ctx context.Context, providerID string, tags map[string]string) error
}

// IKSWorkerPoolProvider extends InstanceProvider with IKS-specific operations
type IKSWorkerPoolProvider interface {
	InstanceProvider

	// ResizePool resizes a worker pool by the specified amount
	ResizePool(ctx context.Context, clusterID, poolID string, newSize int) error

	// GetPool retrieves information about a worker pool
	GetPool(ctx context.Context, clusterID, poolID string) (*WorkerPool, error)

	// ListPools returns all worker pools for a cluster
	ListPools(ctx context.Context, clusterID string) ([]*WorkerPool, error)

	// CreatePool creates a new worker pool with the specified configuration
	CreatePool(ctx context.Context, clusterID string, request *CreatePoolRequest) (*WorkerPool, error)

	// DeletePool deletes a worker pool from the cluster
	DeletePool(ctx context.Context, clusterID, poolID string) error
}

// WorkerPool represents an IKS worker pool
type WorkerPool struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Flavor      string            `json:"flavor"`
	Zone        string            `json:"zone"`
	SizePerZone int               `json:"sizePerZone"`
	ActualSize  int               `json:"actualSize"`
	State       string            `json:"state"`
	Labels      map[string]string `json:"labels"`
}

// CreatePoolRequest contains parameters for creating a new worker pool
type CreatePoolRequest struct {
	// Name is the name for the new worker pool
	Name string
	// Flavor is the instance type (e.g., "bx2-4x16")
	Flavor string
	// Zone is the availability zone for the pool
	Zone string
	// SubnetID is the subnet to use for worker nodes
	SubnetID string
	// SizePerZone is the initial number of nodes
	SizePerZone int
	// Labels are key-value pairs to apply to the pool
	Labels map[string]string
	// DiskEncryption enables disk encryption for worker nodes
	DiskEncryption bool
	// VpcID is the VPC for the worker pool
	VpcID string
}

// ProviderMode represents the provisioning mode
type ProviderMode string

const (
	// VPCMode provisions instances directly via VPC API
	VPCMode ProviderMode = "vpc"

	// IKSMode provisions instances via IKS worker pool resize API
	IKSMode ProviderMode = "iks"
)
