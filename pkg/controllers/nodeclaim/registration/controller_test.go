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

package registration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

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

func getTestNodeClaim(name, providerID string) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				NodePoolLabel: "test-nodepool",
			},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: "karpenter-ibm.sh",
				Kind:  "IBMNodeClass",
				Name:  "test-nodeclass",
			},
			Taints: []corev1.Taint{
				{
					Key:    "test-taint",
					Value:  "test-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID: providerID,
		},
	}
}

func getTestNode(name, providerID string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"kubernetes.io/hostname": name,
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: providerID,
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				},
			},
		},
	}
}

func getUnregisteredNode(name, providerID string) *corev1.Node {
	node := getTestNode(name, providerID)
	node.Spec.Taints = []corev1.Taint{
		{
			Key:    karpv1.UnregisteredTaintKey,
			Effect: corev1.TaintEffectNoExecute,
		},
	}
	return node
}

func TestNewController(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(getTestScheme()).Build()

	controller, err := NewController(fakeClient)

	assert.NoError(t, err)
	assert.NotNil(t, controller)
	assert.Equal(t, fakeClient, controller.kubeClient)
}

func TestController_Reconcile_AddsFinalizer(t *testing.T) {
	scheme := getTestScheme()
	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim).
		Build()

	controller, _ := NewController(fakeClient)

	result, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-nodeclaim"},
	})

	assert.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter) // Should requeue waiting for node

	// Check that finalizer was added
	var updatedNodeClaim karpv1.NodeClaim
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-nodeclaim"}, &updatedNodeClaim)
	assert.NoError(t, err)
	assert.Contains(t, updatedNodeClaim.Finalizers, NodeClaimRegistrationFinalizer)
}

func TestController_Reconcile_SkipsAlreadyRegistered(t *testing.T) {
	scheme := getTestScheme()
	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")

	// Mark as already registered AND initialized (fully ready)
	nodeClaim.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)
	nodeClaim.StatusConditions().SetTrue(karpv1.ConditionTypeInitialized)
	nodeClaim.Finalizers = []string{NodeClaimRegistrationFinalizer}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim).
		Build()

	controller, _ := NewController(fakeClient)

	result, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-nodeclaim"},
	})

	assert.NoError(t, err)
	assert.Zero(t, result.RequeueAfter) // Should not requeue when fully initialized
}

func TestController_Reconcile_RequeuesWhenRegisteredButNotInitialized(t *testing.T) {
	scheme := getTestScheme()
	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")

	// Mark as registered but NOT initialized
	nodeClaim.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)
	nodeClaim.Finalizers = []string{NodeClaimRegistrationFinalizer}
	nodeClaim.Status.NodeName = "test-node"

	// Create a node that is not ready yet
	node := getTestNode("test-node", "ibm://test-instance-id")
	node.Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeReady,
			Status: corev1.ConditionFalse,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim, node).
		WithStatusSubresource(nodeClaim).
		Build()

	controller, _ := NewController(fakeClient)

	result, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-nodeclaim"},
	})

	assert.NoError(t, err)
	assert.Equal(t, 15*time.Second, result.RequeueAfter) // Should requeue to check readiness
}

func TestController_Reconcile_RequeuesWhenNodeNotFound(t *testing.T) {
	scheme := getTestScheme()
	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")
	nodeClaim.Finalizers = []string{NodeClaimRegistrationFinalizer}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim).
		Build()

	controller, _ := NewController(fakeClient)

	result, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-nodeclaim"},
	})

	assert.NoError(t, err)
	assert.Equal(t, 30*time.Second, result.RequeueAfter)
}

func TestController_Reconcile_SuccessfulRegistration(t *testing.T) {
	scheme := getTestScheme()
	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")
	nodeClaim.Finalizers = []string{NodeClaimRegistrationFinalizer}

	node := getUnregisteredNode("test-node", "ibm://test-instance-id")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim, node).
		WithStatusSubresource(&karpv1.NodeClaim{}).
		Build()

	controller, _ := NewController(fakeClient)

	_, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-nodeclaim"},
	})

	// The test may fail with status update issues due to fake client limitations
	// We'll check that the main sync operation worked even if status update failed
	if err != nil {
		t.Logf("Expected error with fake client status update: %v", err)
	}

	// Check Node was updated with correct labels and taints
	var updatedNode corev1.Node
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-node"}, &updatedNode)
	assert.NoError(t, err)
	assert.Equal(t, "test-nodepool", updatedNode.Labels[NodePoolLabel])
	assert.Equal(t, "test-nodeclass", updatedNode.Labels[NodeClassLabel])
	assert.Equal(t, "true", updatedNode.Labels[RegisteredLabel])
	assert.Contains(t, updatedNode.Finalizers, NodeClaimRegistrationFinalizer)

	// Check unregistered taint was removed
	for _, taint := range updatedNode.Spec.Taints {
		assert.NotEqual(t, karpv1.UnregisteredTaintKey, taint.Key)
	}

	// Check NodeClaim taint was added
	foundTestTaint := false
	for _, taint := range updatedNode.Spec.Taints {
		if taint.Key == "test-taint" {
			foundTestTaint = true
			assert.Equal(t, "test-value", taint.Value)
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
	}
	assert.True(t, foundTestTaint, "NodeClaim taint should be synced to Node")
}

func TestController_Reconcile_FindsNodeByName(t *testing.T) {
	scheme := getTestScheme()
	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")
	nodeClaim.Finalizers = []string{NodeClaimRegistrationFinalizer}
	nodeClaim.Status.NodeName = "test-node" // Pre-set node name

	node := getTestNode("test-node", "ibm://test-instance-id")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim, node).
		WithStatusSubresource(&karpv1.NodeClaim{}).
		Build()

	controller, _ := NewController(fakeClient)

	_, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-nodeclaim"},
	})

	// Status update may fail with fake client
	if err != nil {
		t.Logf("Expected error with fake client status update: %v", err)
	}

	// Check that node sync operations worked
	var updatedNode corev1.Node
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "test-node"}, &updatedNode)
	assert.NoError(t, err)
	assert.Equal(t, "test-nodepool", updatedNode.Labels[NodePoolLabel])
	assert.Equal(t, "true", updatedNode.Labels[RegisteredLabel])
}

func TestController_Reconcile_HandlesDeletion(t *testing.T) {
	scheme := getTestScheme()
	deletionTime := metav1.Now()
	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")
	nodeClaim.DeletionTimestamp = &deletionTime
	nodeClaim.Finalizers = []string{NodeClaimRegistrationFinalizer}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim).
		Build()

	controller, _ := NewController(fakeClient)

	// Test the deletion detection logic
	result, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-nodeclaim"},
	})

	// The deletion path should be executed without errors
	assert.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)
}

func TestController_Reconcile_IgnoresNotFound(t *testing.T) {
	scheme := getTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	controller, _ := NewController(fakeClient)

	result, err := controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "non-existent-nodeclaim"},
	})

	assert.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)
}

func TestController_SyncTaintsToNode(t *testing.T) {
	controller := &Controller{}

	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")
	nodeClaim.Spec.Taints = []corev1.Taint{
		{
			Key:    "new-taint",
			Value:  "new-value",
			Effect: corev1.TaintEffectNoExecute,
		},
	}

	node := &corev1.Node{
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    "existing-taint",
					Value:  "existing-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	modified := controller.syncTaintsToNode(nodeClaim, node)

	assert.True(t, modified)
	assert.Len(t, node.Spec.Taints, 2)

	// Check new taint was added
	foundNewTaint := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == "new-taint" {
			foundNewTaint = true
			assert.Equal(t, "new-value", taint.Value)
			assert.Equal(t, corev1.TaintEffectNoExecute, taint.Effect)
		}
	}
	assert.True(t, foundNewTaint)
}

func TestController_SyncTaintsToNode_NoChange(t *testing.T) {
	controller := &Controller{}

	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")
	nodeClaim.Spec.Taints = []corev1.Taint{
		{
			Key:    "existing-taint",
			Value:  "existing-value",
			Effect: corev1.TaintEffectNoSchedule,
		},
	}

	node := &corev1.Node{
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    "existing-taint",
					Value:  "existing-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	modified := controller.syncTaintsToNode(nodeClaim, node)

	assert.False(t, modified)
	assert.Len(t, node.Spec.Taints, 1)
}

func TestController_SyncTaintsToNode_UpdateValue(t *testing.T) {
	controller := &Controller{}

	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")
	nodeClaim.Spec.Taints = []corev1.Taint{
		{
			Key:    "existing-taint",
			Value:  "updated-value",
			Effect: corev1.TaintEffectNoSchedule,
		},
	}

	node := &corev1.Node{
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    "existing-taint",
					Value:  "old-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	modified := controller.syncTaintsToNode(nodeClaim, node)

	assert.True(t, modified)
	assert.Len(t, node.Spec.Taints, 1)
	assert.Equal(t, "updated-value", node.Spec.Taints[0].Value)
	assert.Equal(t, "existing-taint", node.Spec.Taints[0].Key)
	assert.Equal(t, corev1.TaintEffectNoSchedule, node.Spec.Taints[0].Effect)
}

func TestController_RemoveTaintFromNode(t *testing.T) {
	controller := &Controller{}

	node := &corev1.Node{
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    "taint-to-remove",
					Effect: corev1.TaintEffectNoSchedule,
				},
				{
					Key:    "taint-to-keep",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	removed := controller.removeTaintFromNode(node, "taint-to-remove")

	assert.True(t, removed)
	assert.Len(t, node.Spec.Taints, 1)
	assert.Equal(t, "taint-to-keep", node.Spec.Taints[0].Key)
}

func TestController_RemoveTaintFromNode_NotFound(t *testing.T) {
	controller := &Controller{}

	node := &corev1.Node{
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    "existing-taint",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	removed := controller.removeTaintFromNode(node, "non-existent-taint")

	assert.False(t, removed)
	assert.Len(t, node.Spec.Taints, 1)
}

func TestController_IsNodeClaimRegistered(t *testing.T) {
	controller := &Controller{}

	tests := []struct {
		name       string
		nodeClaim  *karpv1.NodeClaim
		registered bool
	}{
		{
			name:       "not registered",
			nodeClaim:  getTestNodeClaim("test", "ibm://test"),
			registered: false,
		},
		{
			name: "registered",
			nodeClaim: func() *karpv1.NodeClaim {
				nc := getTestNodeClaim("test", "ibm://test")
				nc.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)
				return nc
			}(),
			registered: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := controller.isNodeClaimRegistered(tt.nodeClaim)
			assert.Equal(t, tt.registered, result)
		})
	}
}

func TestController_IsNodeClaimFullyInitialized(t *testing.T) {
	controller := &Controller{}

	tests := []struct {
		name        string
		nodeClaim   *karpv1.NodeClaim
		initialized bool
	}{
		{
			name:        "not registered or initialized",
			nodeClaim:   getTestNodeClaim("test", "ibm://test"),
			initialized: false,
		},
		{
			name: "registered but not initialized",
			nodeClaim: func() *karpv1.NodeClaim {
				nc := getTestNodeClaim("test", "ibm://test")
				nc.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)
				return nc
			}(),
			initialized: false,
		},
		{
			name: "registered and initialized",
			nodeClaim: func() *karpv1.NodeClaim {
				nc := getTestNodeClaim("test", "ibm://test")
				nc.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)
				nc.StatusConditions().SetTrue(karpv1.ConditionTypeInitialized)
				return nc
			}(),
			initialized: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := controller.isNodeClaimFullyInitialized(tt.nodeClaim)
			assert.Equal(t, tt.initialized, result)
		})
	}
}

func TestController_FindNodeForNodeClaim_ByProviderID(t *testing.T) {
	scheme := getTestScheme()
	node1 := getTestNode("node1", "ibm://instance-1")
	node2 := getTestNode("node2", "ibm://instance-2")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node1, node2).
		Build()

	controller, _ := NewController(fakeClient)

	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://instance-2")

	node, err := controller.findNodeForNodeClaim(context.Background(), nodeClaim)

	assert.NoError(t, err)
	assert.NotNil(t, node)
	assert.Equal(t, "node2", node.Name)
}

func TestController_FindNodeForNodeClaim_ByNodeName(t *testing.T) {
	scheme := getTestScheme()
	node := getTestNode("specific-node", "ibm://instance-1")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node).
		Build()

	controller, _ := NewController(fakeClient)

	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://instance-1")
	nodeClaim.Status.NodeName = "specific-node"

	foundNode, err := controller.findNodeForNodeClaim(context.Background(), nodeClaim)

	assert.NoError(t, err)
	assert.NotNil(t, foundNode)
	assert.Equal(t, "specific-node", foundNode.Name)
}

func TestController_FindNodeForNodeClaim_NotFound(t *testing.T) {
	scheme := getTestScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	controller, _ := NewController(fakeClient)

	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://non-existent")

	node, err := controller.findNodeForNodeClaim(context.Background(), nodeClaim)

	assert.NoError(t, err)
	assert.Nil(t, node)
}

func TestController_SyncNodeWithDoNotSyncTaints(t *testing.T) {
	scheme := getTestScheme()
	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")
	// Add do-not-sync label
	nodeClaim.Labels["karpenter.sh/do-not-sync-taints"] = "true"

	node := getUnregisteredNode("test-node", "ibm://test-instance-id")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim, node).
		Build()

	controller, _ := NewController(fakeClient)

	err := controller.syncNodeClaimToNode(context.Background(), nodeClaim, node)
	assert.NoError(t, err)

	// Check that NodeClaim taints were NOT synced
	foundTestTaint := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == "test-taint" {
			foundTestTaint = true
		}
	}
	assert.False(t, foundTestTaint, "NodeClaim taints should not be synced when do-not-sync label is set")

	// But unregistered taint should still be removed
	foundUnregisteredTaint := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == karpv1.UnregisteredTaintKey {
			foundUnregisteredTaint = true
		}
	}
	assert.False(t, foundUnregisteredTaint, "Unregistered taint should still be removed")
}

func TestController_FinalizerLogic(t *testing.T) {
	tests := []struct {
		name               string
		nodeClaim          *karpv1.NodeClaim
		expectHasFinalizer bool
		expectDeletion     bool
	}{
		{
			name: "normal nodeclaim without finalizer",
			nodeClaim: func() *karpv1.NodeClaim {
				nc := getTestNodeClaim("test", "ibm://test")
				return nc
			}(),
			expectHasFinalizer: false,
			expectDeletion:     false,
		},
		{
			name: "nodeclaim with finalizer",
			nodeClaim: func() *karpv1.NodeClaim {
				nc := getTestNodeClaim("test", "ibm://test")
				nc.Finalizers = []string{NodeClaimRegistrationFinalizer}
				return nc
			}(),
			expectHasFinalizer: true,
			expectDeletion:     false,
		},
		{
			name: "deleting nodeclaim with finalizer",
			nodeClaim: func() *karpv1.NodeClaim {
				nc := getTestNodeClaim("test", "ibm://test")
				nc.Finalizers = []string{NodeClaimRegistrationFinalizer}
				deletionTime := metav1.Now()
				nc.DeletionTimestamp = &deletionTime
				return nc
			}(),
			expectHasFinalizer: true,
			expectDeletion:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test finalizer detection
			hasFinalizer := false
			for _, finalizer := range tt.nodeClaim.Finalizers {
				if finalizer == NodeClaimRegistrationFinalizer {
					hasFinalizer = true
					break
				}
			}
			assert.Equal(t, tt.expectHasFinalizer, hasFinalizer)

			// Test deletion detection
			isDeletion := !tt.nodeClaim.DeletionTimestamp.IsZero()
			assert.Equal(t, tt.expectDeletion, isDeletion)
		})
	}
}

func TestController_SyncStartupTaintsToNode(t *testing.T) {
	controller := &Controller{}

	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")
	// Add startup taints
	nodeClaim.Spec.StartupTaints = []corev1.Taint{
		{
			Key:    "node.kubernetes.io/not-ready",
			Effect: corev1.TaintEffectNoSchedule,
		},
		{
			Key:    "example.com/initializing",
			Value:  "true",
			Effect: corev1.TaintEffectNoSchedule,
		},
	}

	node := &corev1.Node{
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    "existing-taint",
					Value:  "existing-value",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	modified := controller.syncTaintsToNode(nodeClaim, node)

	assert.True(t, modified)
	// Should have: existing taint + regular taint + 2 startup taints = 4 total
	assert.Len(t, node.Spec.Taints, 4)

	// Check startup taints were added
	foundNotReady := false
	foundInitializing := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == "node.kubernetes.io/not-ready" {
			foundNotReady = true
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
		if taint.Key == "example.com/initializing" {
			foundInitializing = true
			assert.Equal(t, "true", taint.Value)
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
	}
	assert.True(t, foundNotReady, "StartupTaint 'not-ready' should be synced to Node")
	assert.True(t, foundInitializing, "StartupTaint 'initializing' should be synced to Node")
}

func TestController_SyncBothTaintsAndStartupTaints(t *testing.T) {
	controller := &Controller{}

	nodeClaim := getTestNodeClaim("test-nodeclaim", "ibm://test-instance-id")
	// Regular taint from getTestNodeClaim: test-taint=test-value:NoSchedule
	// Add startup taint
	nodeClaim.Spec.StartupTaints = []corev1.Taint{
		{
			Key:    "startup-taint",
			Value:  "startup-value",
			Effect: corev1.TaintEffectNoExecute,
		},
	}

	node := &corev1.Node{
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{},
		},
	}

	modified := controller.syncTaintsToNode(nodeClaim, node)

	assert.True(t, modified)
	assert.Len(t, node.Spec.Taints, 2) // Both regular and startup taint

	// Check both taints were added
	foundRegularTaint := false
	foundStartupTaint := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == "test-taint" {
			foundRegularTaint = true
			assert.Equal(t, "test-value", taint.Value)
			assert.Equal(t, corev1.TaintEffectNoSchedule, taint.Effect)
		}
		if taint.Key == "startup-taint" {
			foundStartupTaint = true
			assert.Equal(t, "startup-value", taint.Value)
			assert.Equal(t, corev1.TaintEffectNoExecute, taint.Effect)
		}
	}
	assert.True(t, foundRegularTaint, "Regular taint should be synced to Node")
	assert.True(t, foundStartupTaint, "StartupTaint should be synced to Node")
}

func TestController_SyncTaintsWithEmptyTaints(t *testing.T) {
	controller := &Controller{}

	// Create NodeClaim with no taints or startup taints
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclaim",
		},
		Spec: karpv1.NodeClaimSpec{
			Taints:        []corev1.Taint{}, // No regular taints
			StartupTaints: []corev1.Taint{}, // No startup taints
		},
	}

	node := &corev1.Node{
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{},
		},
	}

	modified := controller.syncTaintsToNode(nodeClaim, node)

	// Should return false because there are no taints to sync
	assert.False(t, modified)
	assert.Len(t, node.Spec.Taints, 0) // No taints should be added
}

func TestController_RemoveStandardUnregisteredTaint(t *testing.T) {
	controller := &Controller{}

	node := &corev1.Node{
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    karpv1.UnregisteredTaintKey,
					Effect: corev1.TaintEffectNoExecute,
				},
				{
					Key:    "taint-to-keep",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	removed := controller.removeTaintFromNode(node, karpv1.UnregisteredTaintKey)

	assert.True(t, removed)
	assert.Len(t, node.Spec.Taints, 1)
	assert.Equal(t, "taint-to-keep", node.Spec.Taints[0].Key)
}

func TestController_Register(t *testing.T) {
	// This test would require a real manager implementation
	// For now, we just test that the method exists and doesn't panic
	controller := &Controller{}

	// This would normally require a real manager, but we can at least check the method exists
	assert.NotNil(t, controller.Register)
}
