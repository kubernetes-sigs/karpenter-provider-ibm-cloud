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
package autoplacement

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	v1alpha1 "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
)

// mockInstanceTypeProvider implements instancetype.Provider interface
type mockInstanceTypeProvider struct{}

func (m *mockInstanceTypeProvider) FilterInstanceTypes(ctx context.Context, requirements *v1alpha1.InstanceTypeRequirements) ([]*cloudprovider.InstanceType, error) {
	instanceTypes := []*cloudprovider.InstanceType{
		{
			Name: "test-instance-type",
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(4*1024*1024*1024, resource.BinarySI),
			},
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, "test-instance-type"),
				scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
			),
		},
	}
	return instanceTypes, nil
}

func (m *mockInstanceTypeProvider) Create(ctx context.Context, instanceType *cloudprovider.InstanceType) error {
	return nil
}

func (m *mockInstanceTypeProvider) Delete(ctx context.Context, instanceType *cloudprovider.InstanceType) error {
	return nil
}

func (m *mockInstanceTypeProvider) Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error) {
	return &cloudprovider.InstanceType{
		Name: name,
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(4*1024*1024*1024, resource.BinarySI),
		},
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, name),
			scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
		),
	}, nil
}

func (m *mockInstanceTypeProvider) List(ctx context.Context) ([]*cloudprovider.InstanceType, error) {
	return []*cloudprovider.InstanceType{
		{
			Name: "test-instance-type",
			Capacity: corev1.ResourceList{
				corev1.ResourceCPU:    *resource.NewQuantity(2, resource.DecimalSI),
				corev1.ResourceMemory: *resource.NewQuantity(4*1024*1024*1024, resource.BinarySI),
			},
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, "test-instance-type"),
				scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
			),
		},
	}, nil
}

func (m *mockInstanceTypeProvider) RankInstanceTypes(instanceTypes []*cloudprovider.InstanceType) []*cloudprovider.InstanceType {
	return instanceTypes
}

// mockSubnetProvider implements subnet.Provider interface
type mockSubnetProvider struct{}

func (m *mockSubnetProvider) ListSubnets(ctx context.Context, vpcID string) ([]subnet.SubnetInfo, error) {
	return []subnet.SubnetInfo{
		{
			ID:           "test-subnet",
			Zone:         "test-zone",
			AvailableIPs: 100,
		},
	}, nil
}

func (m *mockSubnetProvider) GetSubnet(ctx context.Context, subnetID string) (*subnet.SubnetInfo, error) {
	return &subnet.SubnetInfo{
		ID:           "test-subnet",
		Zone:         "test-zone",
		AvailableIPs: 100,
	}, nil
}

func (m *mockSubnetProvider) SelectSubnets(ctx context.Context, vpcID string, strategy *v1alpha1.PlacementStrategy) ([]subnet.SubnetInfo, error) {
	return []subnet.SubnetInfo{
		{
			ID:           "test-subnet",
			Zone:         "test-zone",
			AvailableIPs: 100,
		},
	}, nil
}

func (m *mockSubnetProvider) SetKubernetesClient(kubeClient kubernetes.Interface) {
	// Mock implementation - do nothing
}

func TestUpdateCondition(t *testing.T) {
	// Create a scheme with IBMNodeClass registered
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)

	// Create a test nodeclass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
		Status: v1alpha1.IBMNodeClassStatus{
			Conditions: []metav1.Condition{},
		},
	}

	// Create a fake client with the scheme and initial objects
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClass).
		WithStatusSubresource(nodeClass).
		Build()

	// Create a test controller with mock providers
	controller := &Controller{
		client:        client,
		log:           zap.New(),
		instanceTypes: &mockInstanceTypeProvider{},
		subnets:       &mockSubnetProvider{},
	}

	// Test adding a new condition
	controller.updateCondition(nodeClass, ConditionTypeAutoPlacement, metav1.ConditionTrue, "TestReason", "Test message")

	// Verify the condition was added correctly
	assert.Len(t, nodeClass.Status.Conditions, 1)
	condition := nodeClass.Status.Conditions[0]
	assert.Equal(t, ConditionTypeAutoPlacement, condition.Type)
	assert.Equal(t, metav1.ConditionTrue, condition.Status)
	assert.Equal(t, "TestReason", condition.Reason)
	assert.Equal(t, "Test message", condition.Message)
	assert.NotNil(t, condition.LastTransitionTime)

	// Test updating an existing condition
	oldTime := condition.LastTransitionTime
	time.Sleep(time.Millisecond) // Ensure time difference
	controller.updateCondition(nodeClass, ConditionTypeAutoPlacement, metav1.ConditionFalse, "NewReason", "New message")

	// Verify the condition was updated correctly
	assert.Len(t, nodeClass.Status.Conditions, 1)
	condition = nodeClass.Status.Conditions[0]
	assert.Equal(t, ConditionTypeAutoPlacement, condition.Type)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, "NewReason", condition.Reason)
	assert.Equal(t, "New message", condition.Message)
	assert.NotEqual(t, oldTime, condition.LastTransitionTime)
}

func TestReconcile(t *testing.T) {
	// Create a scheme with IBMNodeClass registered
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)

	// Create a test nodeclass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region: "us-south",
			VPC:    "test-vpc",
			Image:  "test-image",
			InstanceRequirements: &v1alpha1.InstanceTypeRequirements{
				MinimumCPU:    1,
				MinimumMemory: 2,
			},
		},
		Status: v1alpha1.IBMNodeClassStatus{
			Conditions:            []metav1.Condition{},
			SelectedInstanceTypes: []string{},
			SelectedSubnets:       []string{},
		},
	}

	// Create a fake client with the scheme and initial objects
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClass).
		WithStatusSubresource(nodeClass).
		Build()

	// Create a test controller with mock providers
	controller := &Controller{
		client:        client,
		log:           zap.New(),
		instanceTypes: &mockInstanceTypeProvider{},
		subnets:       &mockSubnetProvider{},
	}

	// Test reconciliation
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "test-nodeclass",
		},
	}

	// Verify initial state
	initialNodeClass := &v1alpha1.IBMNodeClass{}
	err := client.Get(context.Background(), types.NamespacedName{Name: "test-nodeclass"}, initialNodeClass)
	require.NoError(t, err)
	require.Empty(t, initialNodeClass.Status.SelectedInstanceTypes, "Initial SelectedInstanceTypes should be empty")

	// Perform reconciliation
	result, err := controller.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	// Get the updated nodeclass
	updatedNodeClass := &v1alpha1.IBMNodeClass{}
	err = client.Get(context.Background(), types.NamespacedName{Name: "test-nodeclass"}, updatedNodeClass)
	require.NoError(t, err)

	// Verify instance type selection
	t.Logf("Instance Profile: %s", updatedNodeClass.Spec.InstanceProfile)
	t.Logf("Selected Instance Types: %v", updatedNodeClass.Status.SelectedInstanceTypes)
	// When using instanceRequirements, the instanceProfile should remain empty (they are mutually exclusive)
	assert.Empty(t, updatedNodeClass.Spec.InstanceProfile, "Instance profile should remain empty when using instanceRequirements")
	assert.NotEmpty(t, updatedNodeClass.Status.SelectedInstanceTypes, "SelectedInstanceTypes should not be empty")
	assert.Contains(t, updatedNodeClass.Status.SelectedInstanceTypes, "test-instance-type", "SelectedInstanceTypes should contain test-instance-type")

	// Verify conditions were set correctly
	assert.NotEmpty(t, updatedNodeClass.Status.Conditions)
	var autoPlacementCondition *metav1.Condition
	for i := range updatedNodeClass.Status.Conditions {
		if updatedNodeClass.Status.Conditions[i].Type == ConditionTypeAutoPlacement {
			cond := updatedNodeClass.Status.Conditions[i]
			autoPlacementCondition = &cond
			break
		}
	}
	require.NotNil(t, autoPlacementCondition)
	assert.Equal(t, metav1.ConditionTrue, autoPlacementCondition.Status)
	assert.Equal(t, "InstanceTypeSelectionSucceeded", autoPlacementCondition.Reason)
}
