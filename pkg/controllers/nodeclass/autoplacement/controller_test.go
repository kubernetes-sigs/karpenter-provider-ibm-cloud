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

	v1alpha1 "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
)

// mockInstanceTypeProvider implements instancetype.Provider interface
type mockInstanceTypeProvider struct{}

func (m *mockInstanceTypeProvider) FilterInstanceTypes(ctx context.Context, requirements *v1alpha1.InstanceTypeRequirements, nodeClass *v1alpha1.IBMNodeClass) ([]*cloudprovider.InstanceType, error) {
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

func (m *mockInstanceTypeProvider) Get(ctx context.Context, name string, nodeClass *v1alpha1.IBMNodeClass) (*cloudprovider.InstanceType, error) {
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

func (m *mockInstanceTypeProvider) List(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass) ([]*cloudprovider.InstanceType, error) {
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
	// Return multiple subnets for multi-zone testing
	if strategy != nil && strategy.ZoneBalance == "Balanced" {
		return []subnet.SubnetInfo{
			{
				ID:           "subnet-zone1",
				Zone:         "us-south-1",
				AvailableIPs: 100,
				State:        "available",
			},
			{
				ID:           "subnet-zone2",
				Zone:         "us-south-2",
				AvailableIPs: 80,
				State:        "available",
			},
			{
				ID:           "subnet-zone3",
				Zone:         "us-south-3",
				AvailableIPs: 120,
				State:        "available",
			},
		}, nil
	}

	// Default single subnet
	return []subnet.SubnetInfo{
		{
			ID:           "test-subnet",
			Zone:         "test-zone",
			AvailableIPs: 100,
			State:        "available",
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

func TestReconcileSubnetSelection(t *testing.T) {
	// Create a scheme with IBMNodeClass registered
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)

	tests := []struct {
		name                    string
		nodeClass               *v1alpha1.IBMNodeClass
		expectedSubnets         []string
		expectedConditionStatus metav1.ConditionStatus
		expectedConditionReason string
	}{
		{
			name: "select subnets with balanced placement strategy",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass-balanced",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					VPC: "test-vpc",
					// No Subnet specified - should trigger selection
					PlacementStrategy: &v1alpha1.PlacementStrategy{
						ZoneBalance: "Balanced",
					},
				},
			},
			expectedSubnets:         []string{"subnet-zone1", "subnet-zone2", "subnet-zone3"},
			expectedConditionStatus: metav1.ConditionTrue,
			expectedConditionReason: "SubnetSelectionSucceeded",
		},
		{
			name: "skip subnet selection when explicit subnet specified",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass-explicit",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					VPC:    "test-vpc",
					Subnet: "explicit-subnet", // Explicit subnet specified
					PlacementStrategy: &v1alpha1.PlacementStrategy{
						ZoneBalance: "Balanced",
					},
				},
			},
			expectedSubnets:         nil, // Should be nil (uninitialized)
			expectedConditionStatus: metav1.ConditionUnknown,
			expectedConditionReason: "",
		},
		{
			name: "no selection without placement strategy",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass-no-strategy",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					VPC: "test-vpc",
					// No Subnet and no PlacementStrategy
				},
			},
			expectedSubnets:         nil,
			expectedConditionStatus: metav1.ConditionUnknown,
			expectedConditionReason: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with the nodeclass and status subresource
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.nodeClass).
				WithStatusSubresource(&v1alpha1.IBMNodeClass{}).
				Build()

			// Create the controller
			controller := &Controller{
				client:        fakeClient,
				instanceTypes: &mockInstanceTypeProvider{},
				subnets:       &mockSubnetProvider{},
				log:           zap.New(zap.UseDevMode(true)),
			}

			// Reconcile the nodeclass
			result, err := controller.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: tt.nodeClass.Name,
				},
			})

			// Verify no error occurred
			assert.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)

			// Get the updated nodeclass
			var updatedNodeClass v1alpha1.IBMNodeClass
			err = fakeClient.Get(context.Background(), types.NamespacedName{
				Name: tt.nodeClass.Name,
			}, &updatedNodeClass)
			require.NoError(t, err)

			// Verify selected subnets
			assert.Equal(t, tt.expectedSubnets, updatedNodeClass.Status.SelectedSubnets,
				"SelectedSubnets should match expected")

			// Verify condition if expected
			if tt.expectedConditionReason != "" {
				var autoPlacementCondition *metav1.Condition
				for i := range updatedNodeClass.Status.Conditions {
					if updatedNodeClass.Status.Conditions[i].Type == ConditionTypeAutoPlacement {
						cond := updatedNodeClass.Status.Conditions[i]
						autoPlacementCondition = &cond
						break
					}
				}
				require.NotNil(t, autoPlacementCondition, "AutoPlacement condition should exist")
				assert.Equal(t, tt.expectedConditionStatus, autoPlacementCondition.Status)
				assert.Equal(t, tt.expectedConditionReason, autoPlacementCondition.Reason)
				assert.Contains(t, autoPlacementCondition.Message, "subnet")
			}
		})
	}
}

func TestReconcileSubnetSelectionClearOnExplicitSubnet(t *testing.T) {
	// Test that selectedSubnets is cleared when explicit subnet is set
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)

	// Create a nodeclass with pre-populated selectedSubnets
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-nodeclass-clear",
			ResourceVersion: "1",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			VPC:    "test-vpc",
			Subnet: "explicit-subnet", // Explicit subnet specified
		},
		Status: v1alpha1.IBMNodeClassStatus{
			SelectedSubnets: []string{"old-subnet1", "old-subnet2"}, // Pre-existing selected subnets
			Conditions:      []metav1.Condition{},                   // Initialize conditions
		},
	}

	// Create fake client with status subresource
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClass).
		WithStatusSubresource(&v1alpha1.IBMNodeClass{}).
		Build()

	// Create the controller
	controller := &Controller{
		client:        fakeClient,
		instanceTypes: &mockInstanceTypeProvider{},
		subnets:       &mockSubnetProvider{},
		log:           zap.New(zap.UseDevMode(true)),
	}

	// Reconcile
	_, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: nodeClass.Name,
		},
	})
	require.NoError(t, err)

	// Get the updated nodeclass
	var updatedNodeClass v1alpha1.IBMNodeClass
	err = fakeClient.Get(context.Background(), types.NamespacedName{
		Name: nodeClass.Name,
	}, &updatedNodeClass)
	require.NoError(t, err)

	// Verify selectedSubnets was cleared
	assert.Empty(t, updatedNodeClass.Status.SelectedSubnets,
		"SelectedSubnets should be cleared when explicit subnet is specified")
}
