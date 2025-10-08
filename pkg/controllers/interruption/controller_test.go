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
package interruption

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cache"
)

// InfrastructureFailure represents infrastructure-related interruption events
const (
	InfrastructureFailure InterruptionReason = "infrastructure-failure"
)

func TestNewController(t *testing.T) {
	kubeClient := fake.NewClientBuilder().Build()
	recorder := record.NewFakeRecorder(10)
	unavailableOfferings := cache.NewUnavailableOfferings()

	controller := NewController(kubeClient, recorder, unavailableOfferings, nil)

	assert.NotNil(t, controller)
	assert.Equal(t, kubeClient, controller.kubeClient)
	assert.Equal(t, recorder, controller.recorder)
	assert.Equal(t, unavailableOfferings, controller.unavailableOfferings)
	assert.Nil(t, controller.providerFactory)
	assert.NotNil(t, controller.httpClient)
	assert.Equal(t, 10*time.Second, controller.httpClient.Timeout)
}

func TestControllerName(t *testing.T) {
	controller := &Controller{}
	assert.Equal(t, "interruption", controller.Name())
}

func TestReconcile(t *testing.T) {
	tests := []struct {
		name  string
		nodes []v1.Node
	}{
		{
			name:  "no nodes",
			nodes: []v1.Node{},
		},
		{
			name: "single node not interrupted",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node-1",
						Labels: map[string]string{
							"node.kubernetes.io/instance-type": "bx2-2x8",
							"topology.kubernetes.io/zone":      "us-south-1",
							"kubernetes.io/hostname":           "test-node-1",
						},
					},
					Spec: v1.NodeSpec{
						Unschedulable: false,
					},
				},
			},
		},
		{
			name: "multiple nodes",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node-1",
						Labels: map[string]string{
							"node.kubernetes.io/instance-type": "bx2-2x8",
							"topology.kubernetes.io/zone":      "us-south-1",
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node-2",
						Labels: map[string]string{
							"node.kubernetes.io/instance-type": "bx2-4x16",
							"topology.kubernetes.io/zone":      "us-south-2",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := v1.AddToScheme(scheme)
			require.NoError(t, err)

			kubeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(nodeSliceToObjects(tt.nodes)...).
				Build()

			recorder := record.NewFakeRecorder(10)
			unavailableOfferings := cache.NewUnavailableOfferings()

			controller := NewController(kubeClient, recorder, unavailableOfferings, nil)

			ctx := context.Background()
			result, err := controller.Reconcile(ctx)

			assert.NoError(t, err)
			assert.Equal(t, time.Minute, result.RequeueAfter)
		})
	}
}

func TestReconcileListError(t *testing.T) {
	// Test with a client that will return an error when listing nodes
	kubeClient := &mockErrorClient{}
	recorder := record.NewFakeRecorder(10)
	unavailableOfferings := cache.NewUnavailableOfferings()

	controller := NewController(kubeClient, recorder, unavailableOfferings, nil)

	ctx := context.Background()
	_, err := controller.Reconcile(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mock error")
}

func TestIsNodeInterrupted(t *testing.T) {
	tests := []struct {
		name           string
		node           *v1.Node
		expectedResult bool
		expectedReason InterruptionReason
	}{
		{
			name: "healthy node should not be interrupted",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "healthy-node",
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionTrue,
						},
					},
				},
			},
			expectedResult: false,
			expectedReason: "",
		},
		{
			name: "node with ready=false should be interrupted",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "unhealthy-node",
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:               v1.NodeReady,
							Status:             v1.ConditionFalse,
							Reason:             "KubeletNotReady",
							Message:            "Kubelet stopped posting node status",
							LastTransitionTime: metav1.Time{Time: time.Now().Add(-5 * time.Minute)},
						},
					},
				},
			},
			expectedResult: true,
			expectedReason: InstanceHealthFailed,
		},
		{
			name: "node with capacity issues should be capacity-related interruption",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "capacity-node",
					CreationTimestamp: metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:               v1.NodeReady,
							Status:             v1.ConditionFalse,
							Reason:             "CapacityUnavailable",
							Message:            "Not enough capacity available",
							LastTransitionTime: metav1.Time{Time: time.Now().Add(-5 * time.Minute)},
						},
					},
				},
			},
			expectedResult: true,
			expectedReason: CapacityUnavailable,
		},
		{
			name: "node with memory pressure should be capacity-related",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "memory-pressure-node",
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionTrue,
						},
						{
							Type:   v1.NodeMemoryPressure,
							Status: v1.ConditionTrue,
						},
					},
				},
			},
			expectedResult: true,
			expectedReason: CapacityUnavailable,
		},
		{
			name: "node with network unavailable should be network-related",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "network-node",
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionTrue,
						},
						{
							Type:   v1.NodeNetworkUnavailable,
							Status: v1.ConditionTrue,
						},
					},
				},
			},
			expectedResult: true,
			expectedReason: NetworkResourceLimit,
		},
		{
			name: "node with interruption annotation should not be processed again",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "already-interrupted-node",
					Annotations: map[string]string{
						InterruptionAnnotation: "true",
					},
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionFalse,
						},
					},
				},
			},
			expectedResult: false,
			expectedReason: "",
		},
		{
			name: "node with maintenance annotation should be host-maintenance",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "maintenance-node",
					Annotations: map[string]string{
						"ibm-cloud.kubernetes.io/maintenance": "true",
					},
				},
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionTrue,
						},
					},
				},
			},
			expectedResult: true,
			expectedReason: HostMaintenance,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &Controller{
				httpClient:      &http.Client{Timeout: 5 * time.Second},
				providerFactory: nil, // No provider factory in tests
			}

			interrupted, reason := controller.isNodeInterrupted(context.Background(), tt.node)
			assert.Equal(t, tt.expectedResult, interrupted)
			assert.Equal(t, tt.expectedReason, reason)
		})
	}
}

func TestIsCapacityRelated(t *testing.T) {
	tests := []struct {
		name     string
		node     *v1.Node
		reason   InterruptionReason
		expected bool
	}{
		{
			name:     "capacity unavailable should be capacity related",
			node:     &v1.Node{},
			reason:   CapacityUnavailable,
			expected: true,
		},
		{
			name:     "network resource limit should be capacity related",
			node:     &v1.Node{},
			reason:   NetworkResourceLimit,
			expected: true,
		},
		{
			name:     "host maintenance should not be capacity related",
			node:     &v1.Node{},
			reason:   HostMaintenance,
			expected: false,
		},
		{
			name:     "instance health failed should not be capacity related",
			node:     &v1.Node{},
			reason:   InstanceHealthFailed,
			expected: false,
		},
		{
			name: "unknown reason with memory pressure should be capacity related",
			node: &v1.Node{
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeMemoryPressure,
							Status: v1.ConditionTrue,
						},
					},
				},
			},
			reason:   "unknown",
			expected: true,
		},
		{
			name: "unknown reason without pressure should not be capacity related",
			node: &v1.Node{
				Status: v1.NodeStatus{
					Conditions: []v1.NodeCondition{
						{
							Type:   v1.NodeReady,
							Status: v1.ConditionTrue,
						},
					},
				},
			},
			reason:   "unknown",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &Controller{}
			result := controller.isCapacityRelated(tt.node, tt.reason)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMarkNodeAsInterrupted(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1.AddToScheme(scheme))

	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node).
		Build()

	controller := &Controller{
		kubeClient: fakeClient,
	}

	ctx := context.Background()
	reason := CapacityUnavailable

	err := controller.markNodeAsInterrupted(ctx, node, reason)
	require.NoError(t, err)

	// Verify the node was updated with interruption annotations
	updatedNode := &v1.Node{}
	err = fakeClient.Get(ctx, client.ObjectKey{Name: "test-node"}, updatedNode)
	require.NoError(t, err)

	assert.Equal(t, "true", updatedNode.Annotations[InterruptionAnnotation])
	assert.Equal(t, string(reason), updatedNode.Annotations[InterruptionReasonAnnotation])
	assert.NotEmpty(t, updatedNode.Annotations[InterruptionTimeAnnotation])

	// Verify the time annotation is a valid RFC3339 timestamp
	_, err = time.Parse(time.RFC3339, updatedNode.Annotations[InterruptionTimeAnnotation])
	assert.NoError(t, err)
}

func TestGetInstanceIDFromNode(t *testing.T) {
	tests := []struct {
		name     string
		node     *v1.Node
		expected string
	}{
		{
			name: "instance ID from provider ID",
			node: &v1.Node{
				Spec: v1.NodeSpec{
					ProviderID: "ibm:///eu-de-2/02c7_12345678-1234-1234-1234-123456789abc",
				},
			},
			expected: "02c7_12345678-1234-1234-1234-123456789abc",
		},
		{
			name: "instance ID from label",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"ibm-cloud.kubernetes.io/instance-id": "02c7_87654321-4321-4321-4321-cba987654321",
					},
				},
			},
			expected: "02c7_87654321-4321-4321-4321-cba987654321",
		},
		{
			name: "instance ID from annotation",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"ibm-cloud.kubernetes.io/instance-id": "02c7_11111111-2222-3333-4444-555555555555",
					},
				},
			},
			expected: "02c7_11111111-2222-3333-4444-555555555555",
		},
		{
			name:     "no instance ID available",
			node:     &v1.Node{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &Controller{}
			result := controller.getInstanceIDFromNode(tt.node)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCheckCapacitySignals(t *testing.T) {
	tests := []struct {
		name     string
		node     *v1.Node
		expected InterruptionReason
	}{
		{
			name: "IBM capacity annotation should return capacity unavailable",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"ibm-cloud.kubernetes.io/status": "capacity unavailable",
					},
				},
			},
			expected: CapacityUnavailable,
		},
		{
			name: "IBM network annotation should return network resource limit",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"ibm-cloud.kubernetes.io/error": "network resources unavailable",
					},
				},
			},
			expected: NetworkResourceLimit,
		},
		{
			name: "maintenance annotation should return host maintenance",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"ibm-cloud.kubernetes.io/maintenance": "true",
					},
				},
			},
			expected: HostMaintenance,
		},
		{
			name: "no relevant annotations should return empty",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"some.other.annotation": "value",
					},
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &Controller{}
			result := controller.checkCapacitySignals(tt.node)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRegister(t *testing.T) {
	// Test that Register doesn't panic and returns no error
	controller := &Controller{}

	// We can't easily test the full registration without a real manager,
	// but we can test that the method exists and doesn't panic
	assert.NotPanics(t, func() {
		// This will fail because we're passing nil, but it won't panic
		err := controller.Register(context.Background(), nil)
		// We expect an error here since we're passing nil manager
		assert.Error(t, err)
	})
}

// Helper functions and mocks

func nodeSliceToObjects(nodes []v1.Node) []client.Object {
	objects := make([]client.Object, len(nodes))
	for i, node := range nodes {
		nodeCopy := node
		objects[i] = &nodeCopy
	}
	return objects
}

// mockErrorClient is a mock client that returns errors for testing
type mockErrorClient struct {
	client.Client
}

func (m *mockErrorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return fmt.Errorf("mock error")
}

func (m *mockErrorClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return assert.AnError
}

func (m *mockErrorClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return assert.AnError
}

func (m *mockErrorClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return assert.AnError
}

func (m *mockErrorClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return assert.AnError
}

func (m *mockErrorClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return assert.AnError
}

func (m *mockErrorClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return assert.AnError
}

func (m *mockErrorClient) Status() client.SubResourceWriter {
	return &mockSubResourceWriter{}
}

func (m *mockErrorClient) SubResource(subResource string) client.SubResourceClient {
	return &mockSubResourceClient{}
}

// mockSubResourceWriter implements client.SubResourceWriter
type mockSubResourceWriter struct{}

func (m *mockSubResourceWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return assert.AnError
}

func (m *mockSubResourceWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return assert.AnError
}

func (m *mockSubResourceWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return assert.AnError
}

// mockSubResourceClient implements client.SubResourceClient
type mockSubResourceClient struct{}

func (m *mockSubResourceClient) Get(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceGetOption) error {
	return assert.AnError
}

func (m *mockSubResourceClient) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return assert.AnError
}

func (m *mockSubResourceClient) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return assert.AnError
}

func (m *mockSubResourceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return assert.AnError
}

func (m *mockErrorClient) Scheme() *runtime.Scheme {
	return runtime.NewScheme()
}

// Test node class resolution functionality

func TestGetNodeClassForNode(t *testing.T) {
	tests := []struct {
		name          string
		node          *v1.Node
		nodeClasses   []client.Object
		expectedError bool
		expectedMode  string
	}{
		{
			name: "node with karpenter.ibm.sh/nodeclass label",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/nodeclass": "test-nodeclass",
					},
				},
			},
			nodeClasses: []client.Object{
				&v1alpha1.IBMNodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclass",
					},
					Spec: v1alpha1.IBMNodeClassSpec{
						Region: "us-south",
					},
				},
			},
			expectedError: false,
			expectedMode:  "test-nodeclass",
		},
		{
			name: "node with fallback karpenter.sh/nodepool label",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter.sh/nodepool": "test-nodepool",
					},
				},
			},
			nodeClasses: []client.Object{
				&v1alpha1.IBMNodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodepool",
					},
					Spec: v1alpha1.IBMNodeClassSpec{
						Region: "us-south",
					},
				},
			},
			expectedError: false,
			expectedMode:  "test-nodepool",
		},
		{
			name: "node with no nodeclass labels",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"some-other-label": "value",
					},
				},
			},
			nodeClasses:   []client.Object{},
			expectedError: true,
		},
		{
			name: "nodeclass not found",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/nodeclass": "non-existent-nodeclass",
					},
				},
			},
			nodeClasses:   []client.Object{},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := v1.AddToScheme(scheme)
			require.NoError(t, err)
			err = v1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			kubeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.nodeClasses...).
				Build()

			recorder := record.NewFakeRecorder(10)
			unavailableOfferings := cache.NewUnavailableOfferings()
			controller := NewController(kubeClient, recorder, unavailableOfferings, nil)

			ctx := context.Background()
			nodeClass, err := controller.getNodeClassForNode(ctx, tt.node)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, nodeClass)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, nodeClass)
				assert.Equal(t, tt.expectedMode, nodeClass.Name)
			}
		})
	}
}

func TestInferModeFromNode(t *testing.T) {
	tests := []struct {
		name         string
		node         *v1.Node
		expectedMode string
	}{
		{
			name: "IKS mode - cluster ID label",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "iks-node",
					Labels: map[string]string{
						"ibm-cloud.kubernetes.io/iks-cluster-id": "test-cluster",
					},
				},
			},
			expectedMode: "iks",
		},
		{
			name: "IKS mode - worker pool annotation",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "iks-node",
					Annotations: map[string]string{
						"ibm-cloud.kubernetes.io/iks-worker-pool": "test-pool",
					},
				},
			},
			expectedMode: "iks",
		},
		{
			name: "IKS mode - provider ID format",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "iks-node",
				},
				Spec: v1.NodeSpec{
					ProviderID: "iks://cluster-id/worker-id",
				},
			},
			expectedMode: "iks",
		},
		{
			name: "VPC mode - default case",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vpc-node",
				},
				Spec: v1.NodeSpec{
					ProviderID: "ibm:///region/instance-id",
				},
			},
			expectedMode: "vpc",
		},
		{
			name: "VPC mode - no provider ID",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vpc-node",
				},
			},
			expectedMode: "vpc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &Controller{}
			mode := controller.inferModeFromNode(tt.node)
			assert.Equal(t, tt.expectedMode, string(mode))
		})
	}
}

func TestHandleVPCInterruption(t *testing.T) {
	tests := []struct {
		name                  string
		node                  *v1.Node
		reason                InterruptionReason
		expectCapacityMarking bool
		expectNodeCordoning   bool
		expectNodeDeletion    bool
		expectError           bool
	}{
		{
			name: "capacity-related interruption should mark unavailable and delete node",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "capacity-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "bx2-2x8",
						"topology.kubernetes.io/zone":      "us-south-1",
					},
				},
				Spec: v1.NodeSpec{
					Unschedulable: false,
				},
			},
			reason:                CapacityUnavailable,
			expectCapacityMarking: true,
			expectNodeCordoning:   true,
			expectNodeDeletion:    true,
			expectError:           false,
		},
		{
			name: "host maintenance should cordon and delete without capacity marking",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "maintenance-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "bx2-2x8",
						"topology.kubernetes.io/zone":      "us-south-1",
					},
				},
				Spec: v1.NodeSpec{
					Unschedulable: false,
				},
			},
			reason:                HostMaintenance,
			expectCapacityMarking: false,
			expectNodeCordoning:   true,
			expectNodeDeletion:    true,
			expectError:           false,
		},
		{
			name: "already cordoned node should still be deleted",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cordoned-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "bx2-2x8",
						"topology.kubernetes.io/zone":      "us-south-1",
					},
				},
				Spec: v1.NodeSpec{
					Unschedulable: true,
				},
			},
			reason:                CapacityUnavailable,
			expectCapacityMarking: true,
			expectNodeCordoning:   false, // Already cordoned
			expectNodeDeletion:    true,
			expectError:           false,
		},
		{
			name: "node without instance type label should still work",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "no-labels-node",
				},
				Spec: v1.NodeSpec{
					Unschedulable: false,
				},
			},
			reason:                CapacityUnavailable,
			expectCapacityMarking: false, // Can't mark without labels
			expectNodeCordoning:   true,
			expectNodeDeletion:    true,
			expectError:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := v1.AddToScheme(scheme)
			require.NoError(t, err)

			kubeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.node).
				Build()

			recorder := record.NewFakeRecorder(10)
			unavailableOfferings := cache.NewUnavailableOfferings()
			controller := NewController(kubeClient, recorder, unavailableOfferings, nil)

			ctx := context.Background()
			err = controller.handleVPCInterruption(ctx, tt.node, tt.reason)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Check if capacity was marked as unavailable
			if tt.expectCapacityMarking {
				instanceType := tt.node.Labels["node.kubernetes.io/instance-type"]
				zone := tt.node.Labels["topology.kubernetes.io/zone"]
				if instanceType != "" && zone != "" {
					key := instanceType + ":" + zone
					assert.True(t, unavailableOfferings.IsUnavailable(key))
				}
			}

			// Verify node state changes by fetching the updated node
			var updatedNode v1.Node
			err = kubeClient.Get(ctx, client.ObjectKey{Name: tt.node.Name}, &updatedNode)

			if tt.expectNodeDeletion {
				// Node should be deleted, so Get should return not found error
				assert.Error(t, err)
				assert.True(t, client.IgnoreNotFound(err) == nil)
			} else {
				assert.NoError(t, err)
				if tt.expectNodeCordoning {
					assert.True(t, updatedNode.Spec.Unschedulable)
				}
			}
		})
	}
}

func TestHandleIKSInterruption(t *testing.T) {
	tests := []struct {
		name                  string
		node                  *v1.Node
		reason                InterruptionReason
		expectCapacityMarking bool
		expectNodeCordoning   bool
		expectNodeDeletion    bool
		expectError           bool
	}{
		{
			name: "capacity-related interruption should only cordon in IKS mode",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "iks-capacity-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "bx2-2x8",
						"topology.kubernetes.io/zone":      "us-south-1",
					},
				},
				Spec: v1.NodeSpec{
					Unschedulable: false,
				},
			},
			reason:                CapacityUnavailable,
			expectCapacityMarking: true,
			expectNodeCordoning:   true,
			expectNodeDeletion:    false, // IKS doesn't delete for capacity issues
			expectError:           false,
		},
		{
			name: "infrastructure issue should cordon and delete in IKS mode",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "iks-infra-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "bx2-2x8",
						"topology.kubernetes.io/zone":      "us-south-1",
					},
				},
				Spec: v1.NodeSpec{
					Unschedulable: false,
				},
			},
			reason:                InfrastructureFailure,
			expectCapacityMarking: false,
			expectNodeCordoning:   true,
			expectNodeDeletion:    true,
			expectError:           false,
		},
		{
			name: "host maintenance should cordon and delete in IKS mode",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "iks-maintenance-node",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "bx2-2x8",
						"topology.kubernetes.io/zone":      "us-south-1",
					},
				},
				Spec: v1.NodeSpec{
					Unschedulable: false,
				},
			},
			reason:                HostMaintenance,
			expectCapacityMarking: false,
			expectNodeCordoning:   true,
			expectNodeDeletion:    true,
			expectError:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := v1.AddToScheme(scheme)
			require.NoError(t, err)

			kubeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.node).
				Build()

			recorder := record.NewFakeRecorder(10)
			unavailableOfferings := cache.NewUnavailableOfferings()
			controller := NewController(kubeClient, recorder, unavailableOfferings, nil)

			ctx := context.Background()
			err = controller.handleIKSInterruption(ctx, tt.node, tt.reason)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Check if capacity was marked as unavailable
			if tt.expectCapacityMarking {
				instanceType := tt.node.Labels["node.kubernetes.io/instance-type"]
				zone := tt.node.Labels["topology.kubernetes.io/zone"]
				if instanceType != "" && zone != "" {
					key := instanceType + ":" + zone
					assert.True(t, unavailableOfferings.IsUnavailable(key))
				}
			}

			// Verify node state changes
			var updatedNode v1.Node
			err = kubeClient.Get(ctx, client.ObjectKey{Name: tt.node.Name}, &updatedNode)

			if tt.expectNodeDeletion {
				// Node should be deleted
				assert.Error(t, err)
				assert.True(t, client.IgnoreNotFound(err) == nil)
			} else {
				assert.NoError(t, err)
				if tt.expectNodeCordoning {
					assert.True(t, updatedNode.Spec.Unschedulable)
				}
			}
		})
	}
}

func TestHandleInterruption(t *testing.T) {
	tests := []struct {
		name         string
		node         *v1.Node
		nodeClasses  []client.Object
		reason       InterruptionReason
		expectedMode string
		expectError  bool
	}{
		{
			name: "VPC mode handling with nodeclass",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vpc-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/nodeclass":       "vpc-nodeclass",
						"node.kubernetes.io/instance-type": "bx2-2x8",
						"topology.kubernetes.io/zone":      "us-south-1",
					},
				},
				Spec: v1.NodeSpec{
					Unschedulable: false,
				},
			},
			nodeClasses: []client.Object{
				&v1alpha1.IBMNodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "vpc-nodeclass",
					},
					Spec: v1alpha1.IBMNodeClassSpec{
						Region: "us-south",
					},
				},
			},
			reason:       CapacityUnavailable,
			expectedMode: "vpc",
			expectError:  false,
		},
		{
			name: "IKS mode handling with nodeclass",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "iks-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/nodeclass":       "iks-nodeclass",
						"node.kubernetes.io/instance-type": "bx2-2x8",
						"topology.kubernetes.io/zone":      "us-south-1",
					},
				},
				Spec: v1.NodeSpec{
					Unschedulable: false,
				},
			},
			nodeClasses: []client.Object{
				&v1alpha1.IBMNodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "iks-nodeclass",
					},
					Spec: v1alpha1.IBMNodeClassSpec{
						Region: "us-south",
					},
				},
			},
			reason:       CapacityUnavailable,
			expectedMode: "iks",
			expectError:  false,
		},
		{
			name: "fallback to VPC mode when nodeclass not found",
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "fallback-node",
					Labels: map[string]string{
						"karpenter.ibm.sh/nodeclass":       "non-existent",
						"node.kubernetes.io/instance-type": "bx2-2x8",
						"topology.kubernetes.io/zone":      "us-south-1",
					},
				},
				Spec: v1.NodeSpec{
					Unschedulable: false,
				},
			},
			nodeClasses: []client.Object{},
			reason:      CapacityUnavailable,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := v1.AddToScheme(scheme)
			require.NoError(t, err)
			err = v1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			allObjects := append(tt.nodeClasses, tt.node)
			kubeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(allObjects...).
				Build()

			recorder := record.NewFakeRecorder(10)
			unavailableOfferings := cache.NewUnavailableOfferings()
			controller := NewController(kubeClient, recorder, unavailableOfferings, nil)

			ctx := context.Background()
			err = controller.handleInterruption(ctx, tt.node, tt.reason)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify that some action was taken (node should be modified or deleted)
				var updatedNode v1.Node
				err = kubeClient.Get(ctx, client.ObjectKey{Name: tt.node.Name}, &updatedNode)

				// Either node is deleted (error) or cordoned (unschedulable = true)
				if err != nil {
					assert.True(t, client.IgnoreNotFound(err) == nil, "Expected node to be deleted or not found")
				} else {
					assert.True(t, updatedNode.Spec.Unschedulable, "Expected node to be cordoned")
				}
			}
		})
	}
}

func TestReconcileWithInterruptedNodes(t *testing.T) {
	tests := []struct {
		name        string
		nodes       []v1.Node
		nodeClasses []client.Object
		expectError bool
	}{
		{
			name: "reconcile with interrupted node",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "interrupted-node",
						Labels: map[string]string{
							"karpenter.ibm.sh/nodeclass":       "test-nodeclass",
							"node.kubernetes.io/instance-type": "bx2-2x8",
							"topology.kubernetes.io/zone":      "us-south-1",
						},
					},
					Status: v1.NodeStatus{
						Conditions: []v1.NodeCondition{
							{
								Type:   v1.NodeReady,
								Status: v1.ConditionFalse,
								Reason: "KubeletNotReady",
							},
						},
					},
				},
			},
			nodeClasses: []client.Object{
				&v1alpha1.IBMNodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclass",
					},
					Spec: v1alpha1.IBMNodeClassSpec{
						Region: "us-south",
					},
				},
			},
			expectError: false,
		},
		{
			name: "reconcile with multiple interrupted nodes",
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "interrupted-node-1",
						Labels: map[string]string{
							"karpenter.ibm.sh/nodeclass":       "test-nodeclass",
							"node.kubernetes.io/instance-type": "bx2-2x8",
							"topology.kubernetes.io/zone":      "us-south-1",
						},
					},
					Status: v1.NodeStatus{
						Conditions: []v1.NodeCondition{
							{
								Type:   v1.NodeReady,
								Status: v1.ConditionFalse,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "interrupted-node-2",
						Labels: map[string]string{
							"karpenter.ibm.sh/nodeclass":       "test-nodeclass",
							"node.kubernetes.io/instance-type": "bx2-4x16",
							"topology.kubernetes.io/zone":      "us-south-2",
						},
					},
					Status: v1.NodeStatus{
						Conditions: []v1.NodeCondition{
							{
								Type:   v1.NodeMemoryPressure,
								Status: v1.ConditionTrue,
							},
						},
					},
				},
			},
			nodeClasses: []client.Object{
				&v1alpha1.IBMNodeClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclass",
					},
					Spec: v1alpha1.IBMNodeClassSpec{
						Region: "us-south",
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			err := v1.AddToScheme(scheme)
			require.NoError(t, err)
			err = v1alpha1.AddToScheme(scheme)
			require.NoError(t, err)

			allObjects := append(tt.nodeClasses, nodeSliceToObjects(tt.nodes)...)
			kubeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(allObjects...).
				Build()

			recorder := record.NewFakeRecorder(10)
			unavailableOfferings := cache.NewUnavailableOfferings()
			controller := NewController(kubeClient, recorder, unavailableOfferings, nil)

			ctx := context.Background()
			result, err := controller.Reconcile(ctx)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, time.Minute, result.RequeueAfter)
			}
		})
	}
}
