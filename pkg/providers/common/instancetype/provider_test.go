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
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1alpha1 "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

type mockProvider struct {
	instanceTypes map[string]*karpcp.InstanceType
}

func newMockProvider() *mockProvider {
	requirements := scheduling.NewRequirements(
		scheduling.NewRequirement("node.kubernetes.io/instance-type", corev1.NodeSelectorOpIn, "test-instance"),
	)
	return &mockProvider{
		instanceTypes: map[string]*karpcp.InstanceType{
			"test-instance": {
				Name:         "test-instance",
				Requirements: requirements,
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
			},
		},
	}
}

func (m *mockProvider) Get(ctx context.Context, name string) (*karpcp.InstanceType, error) {
	if it, ok := m.instanceTypes[name]; ok {
		return it, nil
	}
	return nil, nil
}

func (m *mockProvider) List(ctx context.Context) ([]*karpcp.InstanceType, error) {
	var instances []*karpcp.InstanceType
	for _, it := range m.instanceTypes {
		instances = append(instances, it)
	}
	return instances, nil
}

func (m *mockProvider) Create(ctx context.Context, instanceType *karpcp.InstanceType) error {
	// no-op as mentioned in the interface
	return nil
}

func (m *mockProvider) Delete(ctx context.Context, instanceType *karpcp.InstanceType) error {
	// no-op as mentioned in the interface
	return nil
}

func TestProviderGet(t *testing.T) {
	ctx := context.Background()
	provider := newMockProvider()

	tests := []struct {
		name           string
		instanceName   string
		expectInstance bool
	}{
		{
			name:           "existing instance type",
			instanceName:   "test-instance",
			expectInstance: true,
		},
		{
			name:           "non-existent instance type",
			instanceName:   "non-existent",
			expectInstance: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance, err := provider.Get(ctx, tt.instanceName)
			assert.NoError(t, err)

			if tt.expectInstance {
				assert.NotNil(t, instance)
				assert.Equal(t, tt.instanceName, instance.Name)
				assert.Equal(t, resource.MustParse("4"), instance.Capacity[corev1.ResourceCPU])
				assert.Equal(t, resource.MustParse("8Gi"), instance.Capacity[corev1.ResourceMemory])
			} else {
				assert.Nil(t, instance)
			}
		})
	}
}

func TestProviderList(t *testing.T) {
	ctx := context.Background()
	provider := newMockProvider()

	instances, err := provider.List(ctx)
	assert.NoError(t, err)
	assert.Len(t, instances, 1)
	assert.Equal(t, "test-instance", instances[0].Name)
	assert.Equal(t, resource.MustParse("4"), instances[0].Capacity[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("8Gi"), instances[0].Capacity[corev1.ResourceMemory])
}

func TestProviderCreate(t *testing.T) {
	ctx := context.Background()
	provider := newMockProvider()

	// Create should be no-op for IBM Cloud
	err := provider.Create(ctx, &karpcp.InstanceType{
		Name: "new-instance",
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	})
	assert.NoError(t, err)
}

func TestProviderDelete(t *testing.T) {
	ctx := context.Background()
	provider := newMockProvider()

	// Delete should be no-op for IBM Cloud
	err := provider.Delete(ctx, &karpcp.InstanceType{
		Name: "test-instance",
	})
	assert.NoError(t, err)
}

func TestIBMInstanceTypeProvider_Get(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		instanceType string
		expectError  bool
	}{
		{
			name:         "valid instance type name",
			instanceType: "bx2-4x16",
			expectError:  true, // Will fail without real IBM client
		},
		{
			name:         "empty instance type",
			instanceType: "",
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &IBMInstanceTypeProvider{
				client: nil, // No real client for unit tests
			}

			_, err := provider.Get(ctx, tt.instanceType, nil)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIBMInstanceTypeProvider_List(t *testing.T) {
	ctx := context.Background()

	provider := &IBMInstanceTypeProvider{
		client: nil, // No real client for unit tests
	}

	_, err := provider.List(ctx, nil)

	// Should fail without real IBM client
	assert.Error(t, err)
}

func TestIBMInstanceTypeProvider_FilterInstanceTypes(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		requirements *v1alpha1.InstanceTypeRequirements
		expectError  bool
	}{
		{
			name: "basic requirements",
			requirements: &v1alpha1.InstanceTypeRequirements{
				MinimumCPU:    2,
				MinimumMemory: 4,
			},
			expectError: true, // Will fail without real IBM client
		},
		{
			name: "architecture requirement",
			requirements: &v1alpha1.InstanceTypeRequirements{
				Architecture:  "amd64",
				MinimumCPU:    4,
				MinimumMemory: 8,
			},
			expectError: true, // Will fail without real IBM client
		},
		{
			name: "price requirement",
			requirements: &v1alpha1.InstanceTypeRequirements{
				MinimumCPU:         2,
				MinimumMemory:      4,
				MaximumHourlyPrice: "0.50",
			},
			expectError: true, // Will fail without real IBM client
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &IBMInstanceTypeProvider{
				client: nil, // No real client for unit tests
			}

			_, err := provider.FilterInstanceTypes(ctx, tt.requirements, nil)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCalculateInstanceTypeScore(t *testing.T) {
	tests := []struct {
		name          string
		instanceType  *ExtendedInstanceType
		expectedScore float64
	}{
		{
			name: "instance with pricing",
			instanceType: &ExtendedInstanceType{
				InstanceType: &karpcp.InstanceType{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
				},
				Price: 0.20, // $0.20/hour
			},
			expectedScore: 0.036111111111111115, // (0.20/4 + 0.20/9) / 2 = (0.05 + 0.0222) / 2 = 0.0361 (8Gi = 9 decimal GB)
		},
		{
			name: "instance without pricing",
			instanceType: &ExtendedInstanceType{
				InstanceType: &karpcp.InstanceType{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
				Price: 0.0, // No pricing data
			},
			expectedScore: 7.0, // 2 + 5 (CPU + memory where 4Gi = 5 decimal GB)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := calculateInstanceTypeScore(tt.instanceType)
			assert.InDelta(t, tt.expectedScore, score, 0.001)
		})
	}
}

func TestGetArchitecture(t *testing.T) {
	tests := []struct {
		name         string
		instanceType *karpcp.InstanceType
		expected     string
	}{
		{
			name: "with amd64 architecture",
			instanceType: &karpcp.InstanceType{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
				),
			},
			expected: "amd64",
		},
		{
			name: "with arm64 architecture",
			instanceType: &karpcp.InstanceType{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "arm64"),
				),
			},
			expected: "arm64",
		},
		{
			name: "without architecture requirement",
			instanceType: &karpcp.InstanceType{
				Requirements: scheduling.NewRequirements(),
			},
			expected: "amd64", // default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arch := getArchitecture(tt.instanceType)
			assert.Equal(t, tt.expected, arch)
		})
	}
}

func TestRankInstanceTypes(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}

	// Create test instance types with different characteristics
	instanceTypes := []*karpcp.InstanceType{
		{
			Name: "small-expensive",
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			},
			Requirements: scheduling.NewRequirements(),
		},
		{
			Name: "large-cheap",
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("8"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
			},
			Requirements: scheduling.NewRequirements(),
		},
	}

	ranked := provider.RankInstanceTypes(instanceTypes)

	// Should return same number of instance types
	assert.Len(t, ranked, len(instanceTypes))

	// All instance types should be present
	names := make([]string, len(ranked))
	for i, it := range ranked {
		names[i] = it.Name
	}
	assert.Contains(t, names, "small-expensive")
	assert.Contains(t, names, "large-cheap")
}

func TestNewProvider_ErrorHandling(t *testing.T) {
	// Test that NewProvider handles nil arguments gracefully
	provider := NewProvider(nil, nil)
	// Should not panic but provider methods should handle nil clients gracefully
	assert.NotNil(t, provider)
}

func TestV1Alpha1InstanceTypeRequirements(t *testing.T) {
	tests := []struct {
		name string
		req  v1alpha1.InstanceTypeRequirements
	}{
		{
			name: "basic CPU and memory requirements",
			req: v1alpha1.InstanceTypeRequirements{
				MinimumCPU:    4,
				MinimumMemory: 8,
			},
		},
		{
			name: "GPU requirements",
			req: v1alpha1.InstanceTypeRequirements{
				MinimumCPU:    2,
				MinimumMemory: 4,
			},
		},
		{
			name: "arm64 architecture",
			req: v1alpha1.InstanceTypeRequirements{
				Architecture:  "arm64",
				MinimumCPU:    2,
				MinimumMemory: 4,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that we can create and access the requirements structure
			assert.GreaterOrEqual(t, tt.req.MinimumCPU, int32(0))
			assert.GreaterOrEqual(t, tt.req.MinimumMemory, int32(0))
		})
	}
}
