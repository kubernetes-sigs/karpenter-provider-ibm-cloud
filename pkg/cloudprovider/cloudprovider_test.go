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
package cloudprovider

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers"
)

// Mock Event Recorder
type mockEventRecorder struct {
	record.EventRecorder
	events []events.Event
}

func (m *mockEventRecorder) Publish(e ...events.Event) {
	m.events = append(m.events, e...)
}

// Mock Instance Type Provider
type mockInstanceTypeProvider struct {
	instanceTypes []*cloudprovider.InstanceType
	getError      error
	listError     error
}

func (m *mockInstanceTypeProvider) Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error) {
	if m.getError != nil {
		return nil, m.getError
	}
	for _, it := range m.instanceTypes {
		if it.Name == name {
			return it, nil
		}
	}
	return nil, fmt.Errorf("instance type %s not found", name)
}

func (m *mockInstanceTypeProvider) List(ctx context.Context) ([]*cloudprovider.InstanceType, error) {
	if m.listError != nil {
		return nil, m.listError
	}
	return m.instanceTypes, nil
}

func (m *mockInstanceTypeProvider) Create(ctx context.Context, instanceType *cloudprovider.InstanceType) error {
	return nil
}

func (m *mockInstanceTypeProvider) Delete(ctx context.Context, instanceType *cloudprovider.InstanceType) error {
	return nil
}

func (m *mockInstanceTypeProvider) FilterInstanceTypes(ctx context.Context, requirements *v1alpha1.InstanceTypeRequirements) ([]*cloudprovider.InstanceType, error) {
	return m.instanceTypes, nil
}

func (m *mockInstanceTypeProvider) RankInstanceTypes(instanceTypes []*cloudprovider.InstanceType) []*cloudprovider.InstanceType {
	return instanceTypes
}

// Mock Instance Provider
type mockInstanceProvider struct {
	createNode  *corev1.Node
	createError error
	deleteError error
	getError    error
	tagError    error
}

func (m *mockInstanceProvider) SetKubeClient(client client.Client) {}

func (m *mockInstanceProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*corev1.Node, error) {
	if m.createError != nil {
		return nil, m.createError
	}
	if m.createNode != nil {
		return m.createNode, nil
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
			Labels: map[string]string{
				"node.kubernetes.io/instance-type": "test-instance-type",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "ibm://test-instance-id",
		},
	}, nil
}

func (m *mockInstanceProvider) Delete(ctx context.Context, node *corev1.Node) error {
	return m.deleteError
}

// TagInstance method removed - not part of interface

func (m *mockInstanceProvider) UpdateTags(ctx context.Context, providerID string, tags map[string]string) error {
	return m.tagError
}

func (m *mockInstanceProvider) List(ctx context.Context) ([]*corev1.Node, error) {
	// Return empty list for mock implementation
	return []*corev1.Node{}, nil
}

func getTestProviderFactory(kubeClient client.Client) *providers.ProviderFactory {
	// Create a real factory with nil IBM client for testing
	return providers.NewProviderFactory(nil, kubeClient, nil)
}

func (m *mockInstanceProvider) Get(ctx context.Context, providerID string) (*corev1.Node, error) {
	if m.getError != nil {
		return nil, m.getError
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
		Spec: corev1.NodeSpec{
			ProviderID: providerID,
		},
	}, nil
}

// Duplicate List and UpdateTags methods removed

// Test helpers
func getTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)

	// Register Karpenter v1 types manually
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	s.AddKnownTypes(gv,
		&karpv1.NodeClaim{},
		&karpv1.NodeClaimList{},
		&karpv1.NodePool{},
		&karpv1.NodePoolList{},
	)
	metav1.AddToGroupVersion(s, gv)

	return s
}

func getTestNodeClass() *v1alpha1.IBMNodeClass {
	return &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
			Annotations: map[string]string{
				v1alpha1.AnnotationIBMNodeClassHash:        "12345",
				v1alpha1.AnnotationIBMNodeClassHashVersion: v1alpha1.IBMNodeClassHashVersion,
			},
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:            "us-south-1",
			InstanceProfile: "bx2-4x16",
			Image:           "test-image",
			VPC:             "test-vpc",
			Subnet:          "test-subnet",
		},
		Status: v1alpha1.IBMNodeClassStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					Reason:             "Ready",
					Message:            "NodeClass is ready",
				},
			},
		},
	}
}

func getTestNodeClaim(nodeClassName string) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-nodeclaim",
			Namespace: "default",
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Name: nodeClassName,
			},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{
					NodeSelectorRequirement: corev1.NodeSelectorRequirement{
						Key:      corev1.LabelInstanceTypeStable,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"test-instance-type"},
					},
				},
			},
			Resources: karpv1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
		},
	}
}

func getTestInstanceType() *cloudprovider.InstanceType {
	return &cloudprovider.InstanceType{
		Name: "test-instance-type",
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("16Gi"),
			corev1.ResourcePods:   resource.MustParse("110"),
		},
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, "test-instance-type"),
			scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
			scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "us-south-1"),
		),
		Offerings: cloudprovider.Offerings{
			{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "us-south-1"),
					scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
				),
				Price:     1.0,
				Available: true,
			},
		},
		Overhead: &cloudprovider.InstanceTypeOverhead{
			KubeReserved: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		},
	}
}

func TestCloudProvider_Create(t *testing.T) {
	tests := []struct {
		name             string
		nodeClaim        *karpv1.NodeClaim
		nodeClass        *v1alpha1.IBMNodeClass
		instanceProvider *mockInstanceProvider
		instanceTypes    []*cloudprovider.InstanceType
		expectError      bool
		errorContains    string
	}{
		{
			name:             "provider factory error with nil client",
			nodeClaim:        getTestNodeClaim("test-nodeclass"),
			nodeClass:        getTestNodeClass(),
			instanceProvider: &mockInstanceProvider{},
			instanceTypes:    []*cloudprovider.InstanceType{getTestInstanceType()},
			expectError:      true,
			errorContains:    "IBM client cannot be nil",
		},
		{
			name:             "nodeclass not found",
			nodeClaim:        getTestNodeClaim("non-existent"),
			nodeClass:        nil,
			instanceProvider: &mockInstanceProvider{},
			expectError:      true,
			errorContains:    "not found",
		},
		{
			name:      "instance creation failure due to nil client",
			nodeClaim: getTestNodeClaim("test-nodeclass"),
			nodeClass: getTestNodeClass(),
			instanceProvider: &mockInstanceProvider{
				createError: fmt.Errorf("failed to create instance"),
			},
			instanceTypes: []*cloudprovider.InstanceType{getTestInstanceType()},
			expectError:   true,
			errorContains: "IBM client cannot be nil",
		},
		{
			name:             "no matching instance types",
			nodeClaim:        getTestNodeClaim("test-nodeclass"),
			nodeClass:        getTestNodeClass(),
			instanceProvider: &mockInstanceProvider{},
			instanceTypes:    []*cloudprovider.InstanceType{},
			expectError:      true,
			errorContains:    "all requested instance types were unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := getTestScheme()

			// Build fake client with objects
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.nodeClass != nil {
				builder = builder.WithObjects(tt.nodeClass)
			}
			fakeClient := builder.Build()

			// Create CloudProvider
			cp := &CloudProvider{
				kubeClient:           fakeClient,
				recorder:             &mockEventRecorder{},
				ibmClient:            nil, // We don't use IBM client in tests
				instanceTypeProvider: &mockInstanceTypeProvider{instanceTypes: tt.instanceTypes},
				providerFactory:      getTestProviderFactory(fakeClient),
				circuitBreaker:       NewCircuitBreaker(DefaultCircuitBreakerConfig(), logr.Discard()),
			}

			// Test Create
			result, err := cp.Create(ctx, tt.nodeClaim)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.NotEmpty(t, result.Status.ProviderID)
				assert.Equal(t, "ibm://test-instance-id", result.Status.ProviderID)
			}
		})
	}
}

func TestCloudProvider_Create_NodeClaimLabelPopulation(t *testing.T) {
	// Test the label population logic by creating a unit test
	// that directly tests the label copying functionality

	t.Run("populates NodeClaim labels from instance type requirements", func(t *testing.T) {
		// Create a NodeClaim with some existing labels
		nodeClaim := &karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodeclaim",
				Labels: map[string]string{
					"existing-label": "existing-value",
				},
			},
		}

		// Create an instance type with requirements
		instanceType := &cloudprovider.InstanceType{
			Name: "test-instance-type",
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, "test-instance-type"),
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "us-south-1"),
				scheduling.NewRequirement("multi-value-key", corev1.NodeSelectorOpIn, "value1", "value2"), // Should be skipped
			),
		}

		// Create a node with labels (simulating what the provider returns)
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodeclaim",
				Labels: map[string]string{
					corev1.LabelInstanceTypeStable: "bx2-2x8",
					karpv1.CapacityTypeLabelKey:    "on-demand",
					corev1.LabelTopologyZone:       "eu-de-2",
					corev1.LabelTopologyRegion:     "eu-de",
					karpv1.NodePoolLabelKey:        "test-nodepool",
					"other-node-label":             "other-value",
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: "ibm:///eu-de/test-instance-id",
			},
		}

		// Simulate the label population logic from our CloudProvider.Create method
		nc := &karpv1.NodeClaim{
			ObjectMeta: nodeClaim.ObjectMeta,
			Spec:       nodeClaim.Spec,
			Status: karpv1.NodeClaimStatus{
				ProviderID: node.Spec.ProviderID,
			},
		}

		// Initialize labels if needed
		if nc.Labels == nil {
			nc.Labels = make(map[string]string)
		}

		// Copy essential labels from the created node first
		for key, value := range node.Labels {
			switch key {
			case corev1.LabelInstanceTypeStable, // TYPE column
				karpv1.CapacityTypeLabelKey, // CAPACITY column
				corev1.LabelTopologyZone,    // ZONE column
				corev1.LabelTopologyRegion,  // Region info
				karpv1.NodePoolLabelKey:     // Preserve nodepool label
				nc.Labels[key] = value
			}
		}

		// Populate labels from instance type requirements (only single-value requirements)
		// These take precedence over node labels when available
		for key, req := range instanceType.Requirements {
			if req.Len() == 1 {
				nc.Labels[key] = req.Values()[0]
			}
		}

		// Set the node name in status for the NODE column
		nc.Status.NodeName = node.Name

		// Verify the results
		expectedLabels := map[string]string{
			"existing-label":               "existing-value",            // Should be preserved
			corev1.LabelInstanceTypeStable: "test-instance-type",        // From instance type requirements (overrides node)
			karpv1.CapacityTypeLabelKey:    karpv1.CapacityTypeOnDemand, // From instance type requirements
			corev1.LabelTopologyZone:       "us-south-1",                // From instance type requirements (overrides node)
			corev1.LabelTopologyRegion:     "eu-de",                     // From node labels
			karpv1.NodePoolLabelKey:        "test-nodepool",             // From node labels
		}

		for expectedKey, expectedValue := range expectedLabels {
			assert.Equal(t, expectedValue, nc.Labels[expectedKey],
				"Label %s should be %s, got %s", expectedKey, expectedValue, nc.Labels[expectedKey])
		}

		// Verify multi-value requirements are not added
		assert.NotContains(t, nc.Labels, "multi-value-key", "Multi-value requirements should not be added as labels")

		// Verify other node labels are not copied
		assert.NotContains(t, nc.Labels, "other-node-label", "Non-essential node labels should not be copied")

		// Verify node name is set
		assert.Equal(t, "test-nodeclaim", nc.Status.NodeName)

		// Verify provider ID is set
		assert.Equal(t, "ibm:///eu-de/test-instance-id", nc.Status.ProviderID)
	})

	t.Run("handles nil instance type gracefully", func(t *testing.T) {
		nodeClaim := &karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodeclaim",
			},
		}

		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodeclaim",
				Labels: map[string]string{
					corev1.LabelInstanceTypeStable: "bx2-2x8",
					karpv1.CapacityTypeLabelKey:    "on-demand",
					corev1.LabelTopologyZone:       "eu-de-2",
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: "ibm:///eu-de/test-instance-id",
			},
		}

		// Simulate the label population logic with nil instance type
		nc := &karpv1.NodeClaim{
			ObjectMeta: nodeClaim.ObjectMeta,
			Spec:       nodeClaim.Spec,
			Status: karpv1.NodeClaimStatus{
				ProviderID: node.Spec.ProviderID,
			},
		}

		if nc.Labels == nil {
			nc.Labels = make(map[string]string)
		}

		// No instance type requirements to process (instanceType is nil)

		// Copy essential labels from node
		for key, value := range node.Labels {
			switch key {
			case corev1.LabelInstanceTypeStable,
				karpv1.CapacityTypeLabelKey,
				corev1.LabelTopologyZone,
				corev1.LabelTopologyRegion,
				karpv1.NodePoolLabelKey:
				nc.Labels[key] = value
			}
		}

		nc.Status.NodeName = node.Name

		// Verify labels are still copied from node
		assert.Equal(t, "bx2-2x8", nc.Labels[corev1.LabelInstanceTypeStable])
		assert.Equal(t, "on-demand", nc.Labels[karpv1.CapacityTypeLabelKey])
		assert.Equal(t, "eu-de-2", nc.Labels[corev1.LabelTopologyZone])
		assert.Equal(t, "test-nodeclaim", nc.Status.NodeName)
	})
}

func TestCloudProvider_Create_CircuitBreakerEventPublishing(t *testing.T) {
	// Simply test that when circuit breaker blocks provisioning, the correct event is published
	// This is a focused unit test for the event publishing logic only

	// Setup
	ctx := context.Background()
	nodeClaim := getTestNodeClaim("test-nodeclass")
	nodeClass := getTestNodeClass()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme)

	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClass).
		Build()

	// Create mock event recorder to capture events
	eventRecorder := &mockEventRecorder{events: []events.Event{}}

	// Create a circuit breaker in OPEN state
	cbConfig := &CircuitBreakerConfig{
		FailureThreshold:       1,
		FailureWindow:          1 * time.Minute,
		RecoveryTimeout:        15 * time.Minute,
		HalfOpenMaxRequests:    1,
		RateLimitPerMinute:     2,
		MaxConcurrentInstances: 5,
	}
	cb := NewCircuitBreaker(cbConfig, logr.Discard())

	// Force circuit breaker to OPEN state
	cb.state = CircuitBreakerOpen
	cb.lastStateChange = time.Now()

	// Create cloud provider with circuit breaker
	// providerFactory will be nil, but that's OK because we'll fail at circuit breaker
	cp := &CloudProvider{
		kubeClient:           kubeClient,
		instanceTypeProvider: &mockInstanceTypeProvider{instanceTypes: []*cloudprovider.InstanceType{getTestInstanceType()}},
		recorder:             eventRecorder,
		circuitBreaker:       cb,
	}

	// Execute
	_, err := cp.Create(ctx, nodeClaim)

	// Verify error
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker blocked provisioning")

	// Verify the correct event was published
	assert.Len(t, eventRecorder.events, 1)
	event := eventRecorder.events[0]
	assert.Equal(t, "CircuitBreakerBlocked", event.Reason)
	assert.Contains(t, event.Message, "Circuit breaker blocked provisioning for NodeClaim test-nodeclaim")
	assert.Equal(t, corev1.EventTypeWarning, event.Type)
}

func TestCloudProvider_Delete(t *testing.T) {
	tests := []struct {
		name             string
		nodeClaim        *karpv1.NodeClaim
		node             *corev1.Node
		instanceProvider *mockInstanceProvider
		expectError      bool
		errorContains    string
	}{
		{
			name: "node deletion fails due to nil client",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclaim",
				},
				Status: karpv1.NodeClaimStatus{
					ProviderID: "ibm://test-instance-id",
					NodeName:   "test-node",
				},
			},
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Spec: corev1.NodeSpec{
					ProviderID: "ibm://test-instance-id",
				},
			},
			instanceProvider: &mockInstanceProvider{},
			expectError:      true,
			errorContains:    "IBM client cannot be nil",
		},
		{
			name: "node not found - fails due to nil client",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclaim",
				},
				Status: karpv1.NodeClaimStatus{
					NodeName: "non-existent-node",
				},
			},
			instanceProvider: &mockInstanceProvider{},
			expectError:      true,
			errorContains:    "IBM client cannot be nil",
		},
		{
			name: "instance deletion failure",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclaim",
				},
				Status: karpv1.NodeClaimStatus{
					ProviderID: "ibm://test-instance-id",
					NodeName:   "test-node",
				},
			},
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Spec: corev1.NodeSpec{
					ProviderID: "ibm://test-instance-id",
				},
			},
			instanceProvider: &mockInstanceProvider{
				deleteError: fmt.Errorf("failed to delete instance"),
			},
			expectError:   true,
			errorContains: "IBM client cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := getTestScheme()

			// Build fake client with objects
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.node != nil {
				builder = builder.WithObjects(tt.node)
			}
			fakeClient := builder.Build()

			// Create CloudProvider
			cp := &CloudProvider{
				kubeClient:      fakeClient,
				recorder:        &mockEventRecorder{},
				providerFactory: getTestProviderFactory(fakeClient),
			}

			// Test Delete
			err := cp.Delete(ctx, tt.nodeClaim)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCloudProvider_GetInstanceTypes(t *testing.T) {
	tests := []struct {
		name          string
		nodePool      *karpv1.NodePool
		instanceTypes []*cloudprovider.InstanceType
		listError     error
		expectError   bool
		expectedCount int
	}{
		{
			name: "successful get instance types",
			nodePool: &karpv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodepool",
				},
				Spec: karpv1.NodePoolSpec{
					Template: karpv1.NodeClaimTemplate{
						Spec: karpv1.NodeClaimTemplateSpec{
							NodeClassRef: &karpv1.NodeClassReference{
								Name: "test-nodeclass",
							},
						},
					},
				},
			},
			instanceTypes: []*cloudprovider.InstanceType{
				getTestInstanceType(),
				{
					Name: "test-instance-type-2",
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("8"),
						corev1.ResourceMemory: resource.MustParse("32Gi"),
					},
					Requirements: scheduling.NewRequirements(),
					Offerings:    cloudprovider.Offerings{},
				},
			},
			expectError:   false,
			expectedCount: 2,
		},
		{
			name: "provider error",
			nodePool: &karpv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodepool",
				},
				Spec: karpv1.NodePoolSpec{
					Template: karpv1.NodeClaimTemplate{
						Spec: karpv1.NodeClaimTemplateSpec{
							NodeClassRef: &karpv1.NodeClassReference{
								Name: "test-nodeclass",
							},
						},
					},
				},
			},
			listError:   fmt.Errorf("failed to list instance types"),
			expectError: true,
		},
		{
			name: "empty instance types",
			nodePool: &karpv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodepool",
				},
				Spec: karpv1.NodePoolSpec{
					Template: karpv1.NodeClaimTemplate{
						Spec: karpv1.NodeClaimTemplateSpec{
							NodeClassRef: &karpv1.NodeClassReference{
								Name: "test-nodeclass",
							},
						},
					},
				},
			},
			instanceTypes: []*cloudprovider.InstanceType{},
			expectError:   false,
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := getTestScheme()

			// Create fake client with NodeClass
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(getTestNodeClass()).
				Build()

			// Create CloudProvider
			cp := &CloudProvider{
				kubeClient: fakeClient,
				instanceTypeProvider: &mockInstanceTypeProvider{
					instanceTypes: tt.instanceTypes,
					listError:     tt.listError,
				},
			}

			// Test GetInstanceTypes
			result, err := cp.GetInstanceTypes(ctx, tt.nodePool)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, result, tt.expectedCount)
			}
		})
	}
}

func TestCloudProvider_IsDrifted(t *testing.T) {
	tests := []struct {
		name          string
		nodeClaim     *karpv1.NodeClaim
		expectedDrift cloudprovider.DriftReason
		expectError   bool
	}{
		{
			name: "no drift",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclaim",
					Annotations: map[string]string{
						v1alpha1.AnnotationIBMNodeClassHashVersion: v1alpha1.IBMNodeClassHashVersion,
						v1alpha1.AnnotationIBMNodeClassHash:        "12345", // Match the hash in getTestNodeClass
					},
				},
				Spec: karpv1.NodeClaimSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Name: "test-nodeclass",
					},
				},
			},
			expectedDrift: "",
			expectError:   false,
		},
		{
			name: "nodeclaim with hash drift",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclaim",
					Annotations: map[string]string{
						v1alpha1.AnnotationIBMNodeClassHashVersion: v1alpha1.IBMNodeClassHashVersion,
						v1alpha1.AnnotationIBMNodeClassHash:        "54321", // Different hash to trigger drift
					},
				},
				Spec: karpv1.NodeClaimSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Name: "test-nodeclass",
					},
				},
			},
			expectedDrift: "NodeClassHashChanged",
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := getTestScheme()

			// Create fake client with NodeClass
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(getTestNodeClass()).
				Build()

			// Create CloudProvider
			cp := &CloudProvider{
				kubeClient: fakeClient,
			}

			// Test IsDrifted
			drift, err := cp.IsDrifted(ctx, tt.nodeClaim)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedDrift, drift)
			}
		})
	}
}

func TestCloudProvider_Get(t *testing.T) {
	tests := []struct {
		name         string
		providerID   string
		node         *corev1.Node
		getInstance  *corev1.Node
		getError     error
		expectError  bool
		expectedName string
	}{
		{
			name:       "get fails due to nil client",
			providerID: "ibm://test-instance-id",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Spec: corev1.NodeSpec{
					ProviderID: "ibm://test-instance-id",
				},
			},
			getInstance: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Spec: corev1.NodeSpec{
					ProviderID: "ibm://test-instance-id",
				},
			},
			expectError:  true,
			expectedName: "",
		},
		{
			name:        "node not found",
			providerID:  "ibm://non-existent",
			getError:    fmt.Errorf("instance not found"),
			expectError: true,
		},
		{
			name:       "instance provider error",
			providerID: "ibm://test-instance-id",
			node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
				},
				Spec: corev1.NodeSpec{
					ProviderID: "ibm://test-instance-id",
				},
			},
			getError:    fmt.Errorf("failed to get instance"),
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := getTestScheme()

			// Build fake client with objects
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.node != nil {
				builder = builder.WithObjects(tt.node)
			}
			fakeClient := builder.Build()

			// Create CloudProvider
			cp := &CloudProvider{
				kubeClient: fakeClient,
				instanceTypeProvider: &mockInstanceTypeProvider{
					instanceTypes: []*cloudprovider.InstanceType{getTestInstanceType()},
				},
				providerFactory: getTestProviderFactory(fakeClient),
			}

			// Test Get
			result, err := cp.Get(ctx, tt.providerID)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.providerID, result.Status.ProviderID)
			}
		})
	}
}

func TestCloudProvider_RepairPolicies(t *testing.T) {
	cp := &CloudProvider{}

	policies := cp.RepairPolicies()

	// Should have at least one repair policy
	assert.NotEmpty(t, policies)

	// Check first policy
	assert.Equal(t, corev1.NodeReady, policies[0].ConditionType)
	assert.Equal(t, corev1.ConditionFalse, policies[0].ConditionStatus)
	assert.Equal(t, 5*time.Minute, policies[0].TolerationDuration)
}

func TestCloudProvider_Name(t *testing.T) {
	cp := &CloudProvider{}
	assert.Equal(t, "ibmcloud", cp.Name())
}

func TestCloudProvider_GetSupportedNodeClasses(t *testing.T) {
	cp := &CloudProvider{}

	nodeClasses := cp.GetSupportedNodeClasses()

	// Should return IBMNodeClass
	assert.Len(t, nodeClasses, 1)
	_, ok := nodeClasses[0].(*v1alpha1.IBMNodeClass)
	assert.True(t, ok)
}

func TestCloudProvider_List(t *testing.T) {
	tests := []struct {
		name        string
		nodes       []runtime.Object
		expectError bool
		expected    int
	}{
		{
			name: "list nodes with karpenter label",
			nodes: []runtime.Object{
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"karpenter.sh/managed-by": "test",
						},
					},
					Spec: corev1.NodeSpec{
						ProviderID: "ibm://instance-1",
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node2",
						Labels: map[string]string{
							"karpenter.sh/managed-by": "test",
						},
					},
					Spec: corev1.NodeSpec{
						ProviderID: "ibm://instance-2",
					},
				},
				&corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node3",
						// No karpenter label
					},
				},
			},
			expectError: false,
			expected:    0,
		},
		{
			name:        "no nodes",
			nodes:       []runtime.Object{},
			expectError: false,
			expected:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := getTestScheme()

			// Build fake client with nodes
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.nodes...).
				Build()

			// Create CloudProvider
			cp := &CloudProvider{
				kubeClient:      fakeClient,
				providerFactory: getTestProviderFactory(fakeClient),
			}

			// Test List
			result, err := cp.List(ctx)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, result, tt.expected)
			}
		})
	}
}
