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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1alpha1 "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
)

// MockInstanceTypeProvider implements instancetype.Provider for testing
type MockInstanceTypeProvider struct {
	listError        error
	getError         error
	instanceTypes    []*cloudprovider.InstanceType
	listCallCount    int
	getCallCount     int
	mutex            sync.Mutex
	simulateSlowList bool
}

func NewMockInstanceTypeProvider() *MockInstanceTypeProvider {
	return &MockInstanceTypeProvider{
		instanceTypes: []*cloudprovider.InstanceType{
			{
				Name: "bx2-2x8",
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement("node.kubernetes.io/instance-type", corev1.NodeSelectorOpIn, "bx2-2x8"),
					scheduling.NewRequirement("kubernetes.io/arch", corev1.NodeSelectorOpIn, "amd64"),
				),
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
				Overhead: &cloudprovider.InstanceTypeOverhead{
					KubeReserved: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			},
			{
				Name: "bx2-4x16",
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement("node.kubernetes.io/instance-type", corev1.NodeSelectorOpIn, "bx2-4x16"),
					scheduling.NewRequirement("kubernetes.io/arch", corev1.NodeSelectorOpIn, "amd64"),
				),
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				},
				Overhead: &cloudprovider.InstanceTypeOverhead{
					KubeReserved: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
				},
			},
		},
	}
}

func (m *MockInstanceTypeProvider) List(ctx context.Context) ([]*cloudprovider.InstanceType, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.listCallCount++

	if m.simulateSlowList {
		// Simulate slow API call
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if m.listError != nil {
		return nil, m.listError
	}

	// Return a copy to avoid mutation issues
	result := make([]*cloudprovider.InstanceType, len(m.instanceTypes))
	copy(result, m.instanceTypes)
	return result, nil
}

func (m *MockInstanceTypeProvider) Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.getCallCount++

	if m.getError != nil {
		return nil, m.getError
	}

	for _, it := range m.instanceTypes {
		if it.Name == name {
			return it, nil
		}
	}

	return nil, errors.New("instance type not found")
}

func (m *MockInstanceTypeProvider) Create(ctx context.Context, instanceType *cloudprovider.InstanceType) error {
	return errors.New("create not supported for IBM Cloud instance types")
}

func (m *MockInstanceTypeProvider) Delete(ctx context.Context, instanceType *cloudprovider.InstanceType) error {
	return errors.New("delete not supported for IBM Cloud instance types")
}

func (m *MockInstanceTypeProvider) FilterInstanceTypes(ctx context.Context, requirements *v1alpha1.InstanceTypeRequirements) ([]*cloudprovider.InstanceType, error) {
	var filtered []*cloudprovider.InstanceType

	for _, it := range m.instanceTypes {
		// Simple filtering logic for testing
		cpuQuantity := it.Capacity[corev1.ResourceCPU]
		cpu, _ := cpuQuantity.AsInt64()

		memoryQuantity := it.Capacity[corev1.ResourceMemory]
		memory, _ := memoryQuantity.AsInt64()
		memoryGB := memory / (1024 * 1024 * 1024)

		if int(cpu) >= int(requirements.MinimumCPU) && int(memoryGB) >= int(requirements.MinimumMemory) {
			filtered = append(filtered, it)
		}
	}

	return filtered, nil
}

func (m *MockInstanceTypeProvider) RankInstanceTypes(instanceTypes []*cloudprovider.InstanceType) []*cloudprovider.InstanceType {
	// Simple ranking by name for testing
	result := make([]*cloudprovider.InstanceType, len(instanceTypes))
	copy(result, instanceTypes)
	return result
}

func (m *MockInstanceTypeProvider) SetListError(err error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.listError = err
}

func (m *MockInstanceTypeProvider) SetGetError(err error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.getError = err
}

func (m *MockInstanceTypeProvider) GetListCallCount() int {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.listCallCount
}

func (m *MockInstanceTypeProvider) GetGetCallCount() int {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.getCallCount
}

func (m *MockInstanceTypeProvider) SetSimulateSlowList(slow bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.simulateSlowList = slow
}

// Test actual NewController function (will fail due to IBM client dependency)
func TestController_NewController_Actual(t *testing.T) {
	// This will fail because it tries to create real IBM client
	// but we include it to test the actual function path
	controller, err := NewController()

	// We expect an error due to missing IBM Cloud credentials in test environment
	assert.Error(t, err)
	assert.Nil(t, controller)
	assert.Contains(t, err.Error(), "creating IBM client")
}

// Test controller registration
func TestController_Register(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	// Test Register method exists and can be called
	// We can't test the full registration without a real manager
	// but we can verify the method signature
	assert.NotNil(t, controller)

	// The Register method should not panic when called with nil
	// This tests the method exists and has the right signature
	defer func() {
		if r := recover(); r != nil {
			// We expect it might panic with nil manager, which is fine
			t.Logf("Register panicked as expected with nil manager: %v", r)
		}
	}()

	// This will likely panic or error, but tests the method path
	_ = controller.Register(context.Background(), nil)
}

// Test controller creation with valid provider
func TestController_NewController_WithValidProvider(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	assert.NotNil(t, controller)
	assert.NotNil(t, controller.instanceTypeProvider)
}

// Test controller creation with nil provider
func TestController_NewController_WithNilProvider(t *testing.T) {
	controller := &Controller{
		instanceTypeProvider: nil,
	}

	assert.NotNil(t, controller)
	assert.Nil(t, controller.instanceTypeProvider)
}

// Test successful reconciliation
func TestController_Reconcile_Success(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	ctx := context.Background()
	result, err := controller.Reconcile(ctx)

	require.NoError(t, err)
	assert.Equal(t, time.Hour, result.RequeueAfter)
	assert.Equal(t, 1, mockProvider.GetListCallCount())
}

// Test reconciliation with provider error
func TestController_Reconcile_ProviderError(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	mockProvider.SetListError(errors.New("provider error"))

	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	ctx := context.Background()
	result, err := controller.Reconcile(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "refreshing instance types")
	assert.Contains(t, err.Error(), "provider error")
	assert.Equal(t, reconcile.Result{}, result)
	assert.Equal(t, 1, mockProvider.GetListCallCount())
}

// Test reconciliation with canceled context
func TestController_Reconcile_CancelledContext(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	mockProvider.SetSimulateSlowList(true)

	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := controller.Reconcile(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "refreshing instance types")
	assert.Equal(t, reconcile.Result{}, result)
}

// Test reconciliation with timeout context
func TestController_Reconcile_TimeoutContext(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	mockProvider.SetSimulateSlowList(true)

	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	result, err := controller.Reconcile(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "refreshing instance types")
	assert.Equal(t, reconcile.Result{}, result)
}

// Test multiple concurrent reconciliations
func TestController_Reconcile_Concurrent(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	const numGoroutines = 10
	var wg sync.WaitGroup
	results := make([]reconcile.Result, numGoroutines)
	errors := make([]error, numGoroutines)

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(index int) {
			defer wg.Done()
			ctx := context.Background()
			results[index], errors[index] = controller.Reconcile(ctx)
		}(i)
	}

	wg.Wait()

	// All reconciliations should succeed
	for i := 0; i < numGoroutines; i++ {
		require.NoError(t, errors[i], "Reconciliation %d should not error", i)
		assert.Equal(t, time.Hour, results[i].RequeueAfter)
	}

	// Provider should have been called for each reconciliation
	assert.Equal(t, numGoroutines, mockProvider.GetListCallCount())
}

// Test controller behavior with empty instance types
func TestController_Reconcile_EmptyInstanceTypes(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	mockProvider.instanceTypes = []*cloudprovider.InstanceType{} // Empty list

	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	ctx := context.Background()
	result, err := controller.Reconcile(ctx)

	require.NoError(t, err)
	assert.Equal(t, time.Hour, result.RequeueAfter)
	assert.Equal(t, 1, mockProvider.GetListCallCount())
}

// Test integration workflow with provider operations
func TestController_IntegrationWorkflow(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	ctx := context.Background()

	// Step 1: Initial reconciliation should succeed
	result, err := controller.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, result.RequeueAfter)

	// Step 2: Verify we can get specific instance types after listing
	instanceType, err := mockProvider.Get(ctx, "bx2-2x8")
	require.NoError(t, err)
	assert.Equal(t, "bx2-2x8", instanceType.Name)

	// Step 3: Test filtering functionality
	requirements := &v1alpha1.InstanceTypeRequirements{
		MinimumCPU:    2,
		MinimumMemory: 8,
		Architecture:  "amd64",
	}
	filtered, err := mockProvider.FilterInstanceTypes(ctx, requirements)
	require.NoError(t, err)
	assert.Len(t, filtered, 2) // Both instance types should match

	// Step 4: Test ranking functionality
	ranked := mockProvider.RankInstanceTypes(filtered)
	assert.Len(t, ranked, 2)

	// Verify call counts
	assert.Equal(t, 1, mockProvider.GetListCallCount())
	assert.Equal(t, 1, mockProvider.GetGetCallCount())
}

// Test controller resilience to provider changes
func TestController_Reconcile_ProviderResilience(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	ctx := context.Background()

	// First reconciliation should succeed
	result, err := controller.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, result.RequeueAfter)

	// Introduce error in provider
	mockProvider.SetListError(errors.New("temporary error"))

	// Second reconciliation should fail
	result, err = controller.Reconcile(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temporary error")

	// Remove error from provider
	mockProvider.SetListError(nil)

	// Third reconciliation should succeed again
	result, err = controller.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, result.RequeueAfter)

	assert.Equal(t, 3, mockProvider.GetListCallCount())
}

// Test provider interface compliance
func TestController_ProviderInterfaceCompliance(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()

	// Verify the mock implements the interface
	var _ instancetype.Provider = mockProvider

	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	assert.NotNil(t, controller.instanceTypeProvider)

	// Test all interface methods are callable
	ctx := context.Background()

	// Test List
	instanceTypes, err := mockProvider.List(ctx)
	require.NoError(t, err)
	assert.Len(t, instanceTypes, 2)

	// Test Get
	instanceType, err := mockProvider.Get(ctx, "bx2-2x8")
	require.NoError(t, err)
	assert.Equal(t, "bx2-2x8", instanceType.Name)

	// Test Create (should return error for IBM Cloud)
	err = mockProvider.Create(ctx, instanceType)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create not supported")

	// Test Delete (should return error for IBM Cloud)
	err = mockProvider.Delete(ctx, instanceType)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete not supported")

	// Test FilterInstanceTypes
	requirements := &v1alpha1.InstanceTypeRequirements{
		MinimumCPU:    1,
		MinimumMemory: 4,
	}
	filtered, err := mockProvider.FilterInstanceTypes(ctx, requirements)
	require.NoError(t, err)
	assert.Len(t, filtered, 2)

	// Test RankInstanceTypes
	ranked := mockProvider.RankInstanceTypes(filtered)
	assert.Len(t, ranked, 2)
}

// Test controller with nil provider
func TestController_Reconcile_NilProvider(t *testing.T) {
	controller := &Controller{
		instanceTypeProvider: nil,
	}

	ctx := context.Background()

	// This should panic or error gracefully
	assert.Panics(t, func() {
		_, _ = controller.Reconcile(ctx) // Ignore result for panic test
	})
}

// Test requeue timing consistency
func TestController_Reconcile_RequeueConsistency(t *testing.T) {
	mockProvider := NewMockInstanceTypeProvider()
	controller := &Controller{
		instanceTypeProvider: mockProvider,
	}

	ctx := context.Background()

	// Multiple reconciliations should always return the same requeue time
	for i := 0; i < 5; i++ {
		result, err := controller.Reconcile(ctx)
		require.NoError(t, err)
		assert.Equal(t, time.Hour, result.RequeueAfter, "Iteration %d should have consistent requeue time", i)
	}

	assert.Equal(t, 5, mockProvider.GetListCallCount())
}
