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

package instancetype

import (
	"context"

	v1alpha1 "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// Provider defines the interface for managing IBM Cloud instance types
type Provider interface {
	// Get retrieves a specific instance type by name
	Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error)

	// List returns all available instance types
	List(ctx context.Context) ([]*cloudprovider.InstanceType, error)

	// Create creates a new instance type (no-op for IBM Cloud as types are predefined)
	Create(ctx context.Context, instanceType *cloudprovider.InstanceType) error

	// Delete deletes an instance type (no-op for IBM Cloud as types are predefined)
	Delete(ctx context.Context, instanceType *cloudprovider.InstanceType) error

	// FilterInstanceTypes returns instance types that meet the specified requirements
	FilterInstanceTypes(ctx context.Context, requirements *v1alpha1.InstanceTypeRequirements) ([]*cloudprovider.InstanceType, error)

	// RankInstanceTypes sorts instance types by cost efficiency and other criteria
	RankInstanceTypes(instanceTypes []*cloudprovider.InstanceType) []*cloudprovider.InstanceType
}

// InstanceTypeRequirements defines the requirements for instance type selection
type InstanceTypeRequirements struct {
	// CPU is the minimum number of CPU cores required
	CPU int
	// Memory is the minimum amount of memory required in GB
	Memory int
	// Architecture is the required CPU architecture
	Architecture string
	// GPU indicates if GPU is required
	GPU bool
}

// InstanceTypeCapabilities defines the capabilities of an instance type
type InstanceTypeCapabilities struct {
	// CPU is the number of CPU cores available
	CPU int
	// Memory is the amount of memory available in GB
	Memory int
	// Architecture is the CPU architecture
	Architecture string
	// GPU indicates if GPU is available
	GPU bool
	// NetworkBandwidth is the network bandwidth in Gbps
	NetworkBandwidth int
	// StorageType is the type of storage available
	StorageType string
}
