package instancetype

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
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
