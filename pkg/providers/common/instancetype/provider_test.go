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
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1alpha1 "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

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
			expectedScore: 0.036111111111111115, // (0.20/4 + 0.20/9) / 2; ScaledValue(8Gi, Giga) rounds up to 9
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
			expectedScore: 7.0, // 2 + 5; ScaledValue(4Gi, Giga) rounds up to 5
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

	// With no pricing, score = CPU + ScaledValue(memory, Giga).
	// small-expensive: 2 + 5 = 7, large-cheap: 8 + 18 = 26. Lower ranks first.
	assert.Len(t, ranked, 2)
	assert.Equal(t, "small-expensive", ranked[0].Name)
	assert.Equal(t, "large-cheap", ranked[1].Name)
}

func TestCalculateOverhead_Defaults(t *testing.T) {
	ctx := context.Background()
	provider := &IBMInstanceTypeProvider{}

	overhead := provider.calculateOverhead(ctx, nil)

	assert.Equal(t, resource.MustParse("100m"), overhead.KubeReserved[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("1Gi"), overhead.KubeReserved[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("100m"), overhead.SystemReserved[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("1Gi"), overhead.SystemReserved[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("500Mi"), overhead.EvictionThreshold[corev1.ResourceMemory])
}

func TestCalculateOverhead_WithKubeletConfig(t *testing.T) {
	ctx := context.Background()
	provider := &IBMInstanceTypeProvider{}

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Kubelet: &v1alpha1.KubeletConfiguration{
				KubeReserved: map[string]string{
					"cpu":    "200m",
					"memory": "2Gi",
				},
				SystemReserved: map[string]string{
					"cpu":    "300m",
					"memory": "3Gi",
				},
				EvictionHard: map[string]string{
					"memory.available": "1Gi",
				},
			},
		},
	}

	overhead := provider.calculateOverhead(ctx, nodeClass)

	assert.Equal(t, resource.MustParse("200m"), overhead.KubeReserved[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("2Gi"), overhead.KubeReserved[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("300m"), overhead.SystemReserved[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("3Gi"), overhead.SystemReserved[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("1Gi"), overhead.EvictionThreshold[corev1.ResourceMemory])
}

func TestCalculateOverhead_InvalidKubeletValues(t *testing.T) {
	ctx := context.Background()
	provider := &IBMInstanceTypeProvider{}

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Kubelet: &v1alpha1.KubeletConfiguration{
				KubeReserved: map[string]string{
					"cpu":    "notavalue",
					"memory": "garbage",
				},
				SystemReserved: map[string]string{
					"cpu":    "???",
					"memory": "invalid",
				},
				EvictionHard: map[string]string{
					"memory.available": "nope",
				},
			},
		},
	}

	overhead := provider.calculateOverhead(ctx, nodeClass)

	// All invalid values should fall back to defaults
	assert.Equal(t, resource.MustParse("100m"), overhead.KubeReserved[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("1Gi"), overhead.KubeReserved[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("100m"), overhead.SystemReserved[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("1Gi"), overhead.SystemReserved[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("500Mi"), overhead.EvictionThreshold[corev1.ResourceMemory])
}

func TestCalculateOverhead_PartialKubeletConfig(t *testing.T) {
	ctx := context.Background()
	provider := &IBMInstanceTypeProvider{}

	nodeClass := &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			Kubelet: &v1alpha1.KubeletConfiguration{
				KubeReserved: map[string]string{
					"cpu": "500m",
					// memory not set, should use default
				},
				// SystemReserved not set at all, should use defaults
				EvictionHard: map[string]string{
					"memory.available": "2Gi",
				},
			},
		},
	}

	overhead := provider.calculateOverhead(ctx, nodeClass)

	assert.Equal(t, resource.MustParse("500m"), overhead.KubeReserved[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("1Gi"), overhead.KubeReserved[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("100m"), overhead.SystemReserved[corev1.ResourceCPU])
	assert.Equal(t, resource.MustParse("1Gi"), overhead.SystemReserved[corev1.ResourceMemory])
	assert.Equal(t, resource.MustParse("2Gi"), overhead.EvictionThreshold[corev1.ResourceMemory])
}

func TestGetRegion_NilClient(t *testing.T) {
	provider := &IBMInstanceTypeProvider{
		client: nil,
	}
	assert.Equal(t, "unknown", provider.GetRegion())
}

func TestCalculateInstanceTypeScore_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		instance *ExtendedInstanceType
		check    func(t *testing.T, score float64)
	}{
		{
			name: "very large instance",
			instance: &ExtendedInstanceType{
				InstanceType: &karpcp.InstanceType{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("128"),
						corev1.ResourceMemory: resource.MustParse("512Gi"),
					},
				},
				Price: 5.0,
			},
			check: func(t *testing.T, score float64) {
				// cpuEff = 5.0/128, memEff = 5.0/550 (512Gi ~ 550 decimal GB)
				// score = (0.0390625 + 0.009090...) / 2 ~ 0.02408
				assert.InDelta(t, 0.02408, score, 0.001)
			},
		},
		{
			name: "single CPU instance",
			instance: &ExtendedInstanceType{
				InstanceType: &karpcp.InstanceType{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				Price: 0.05,
			},
			check: func(t *testing.T, score float64) {
				// cpuEff = 0.05/1, memEff = 0.05/3 (2Gi ~ 3 decimal GB)
				// score = (0.05 + 0.01666) / 2 ~ 0.03333
				assert.InDelta(t, 0.03333, score, 0.001)
			},
		},
		{
			name: "zero memory instance with price",
			instance: &ExtendedInstanceType{
				InstanceType: &karpcp.InstanceType{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("0"),
					},
				},
				Price: 0.10,
			},
			check: func(t *testing.T, score float64) {
				// memoryGB = 0, division by zero yields +Inf
				assert.True(t, math.IsInf(score, 1), "expected +Inf for zero memory with pricing")
			},
		},
		{
			name: "same price larger resources scores lower",
			instance: &ExtendedInstanceType{
				InstanceType: &karpcp.InstanceType{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("8"),
						corev1.ResourceMemory: resource.MustParse("16Gi"),
					},
				},
				Price: 0.20,
			},
			check: func(t *testing.T, score float64) {
				smallerInstance := &ExtendedInstanceType{
					InstanceType: &karpcp.InstanceType{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
						},
					},
					Price: 0.20,
				}
				smallerScore := calculateInstanceTypeScore(smallerInstance)
				assert.Less(t, score, smallerScore, "larger instance at same price should have lower (better) score")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := calculateInstanceTypeScore(tt.instance)
			tt.check(t, score)
		})
	}
}

func TestRankInstanceTypes_WithPricing(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}

	instances := []*ExtendedInstanceType{
		{
			InstanceType: &karpcp.InstanceType{
				Name: "expensive",
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
				Requirements: scheduling.NewRequirements(),
			},
			Price: 0.50,
		},
		{
			InstanceType: &karpcp.InstanceType{
				Name: "cheap",
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
				Requirements: scheduling.NewRequirements(),
			},
			Price: 0.10,
		},
		{
			InstanceType: &karpcp.InstanceType{
				Name: "mid",
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
				Requirements: scheduling.NewRequirements(),
			},
			Price: 0.20,
		},
	}

	ranked := provider.rankInstanceTypes(instances)

	assert.Len(t, ranked, 3)
	assert.Equal(t, "cheap", ranked[0].Name)
	assert.Equal(t, "mid", ranked[1].Name)
	assert.Equal(t, "expensive", ranked[2].Name)
}

func TestRankInstanceTypes_MixedPricing(t *testing.T) {
	provider := &IBMInstanceTypeProvider{}

	instances := []*ExtendedInstanceType{
		{
			InstanceType: &karpcp.InstanceType{
				Name: "no-price",
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
				Requirements: scheduling.NewRequirements(),
			},
			Price: 0.0,
		},
		{
			InstanceType: &karpcp.InstanceType{
				Name: "priced",
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
				Requirements: scheduling.NewRequirements(),
			},
			Price: 0.10,
		},
	}

	ranked := provider.rankInstanceTypes(instances)

	assert.Len(t, ranked, 2)
	// Priced instance scores ~0.018, no-price instance scores 4+9=13
	// Lower score is ranked first, so priced comes before no-price
	assert.Equal(t, "priced", ranked[0].Name)
	assert.Equal(t, "no-price", ranked[1].Name)
}
