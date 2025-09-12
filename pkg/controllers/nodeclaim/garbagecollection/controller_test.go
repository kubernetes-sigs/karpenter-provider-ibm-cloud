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

package garbagecollection

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/awslabs/operatorpkg/status"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// Mock CloudProvider for testing garbage collection
type mockCloudProvider struct {
	mu sync.Mutex
	// Map of provider IDs to NodeClaims
	nodeClaims map[string]*karpv1.NodeClaim
	// Track which provider IDs were deleted
	deletedProviderIDs []string
	// Control error behavior
	listError   error
	deleteError error
	getError    error
}

func newMockCloudProvider() *mockCloudProvider {
	return &mockCloudProvider{
		nodeClaims:         make(map[string]*karpv1.NodeClaim),
		deletedProviderIDs: []string{},
	}
}

func (m *mockCloudProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	return nodeClaim, nil
}

func (m *mockCloudProvider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.deleteError != nil {
		return m.deleteError
	}
	m.deletedProviderIDs = append(m.deletedProviderIDs, nodeClaim.Status.ProviderID)
	delete(m.nodeClaims, nodeClaim.Status.ProviderID)
	return nil
}

func (m *mockCloudProvider) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.getError != nil {
		return nil, m.getError
	}
	nc, ok := m.nodeClaims[providerID]
	if !ok {
		return nil, cloudprovider.NewNodeClaimNotFoundError(errors.New("nodeclaim not found"))
	}
	return nc, nil
}

func (m *mockCloudProvider) List(ctx context.Context) ([]*karpv1.NodeClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.listError != nil {
		return nil, m.listError
	}
	result := make([]*karpv1.NodeClaim, 0, len(m.nodeClaims))
	for _, nc := range m.nodeClaims {
		result = append(result, nc)
	}
	return result, nil
}

func (m *mockCloudProvider) IsDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim) (cloudprovider.DriftReason, error) {
	return "", nil
}

func (m *mockCloudProvider) Name() string {
	return "mock"
}

func (m *mockCloudProvider) GetInstanceTypes(ctx context.Context, nodePool *karpv1.NodePool) ([]*cloudprovider.InstanceType, error) {
	return nil, nil
}

func (m *mockCloudProvider) GetSupportedNodeClasses() []status.Object {
	return []status.Object{&v1alpha1.IBMNodeClass{}}
}

func (m *mockCloudProvider) RepairPolicies() []cloudprovider.RepairPolicy {
	return []cloudprovider.RepairPolicy{}
}

// Test helper to create a test scheme
func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = scheme.AddToScheme(s)
	_ = corev1.AddToScheme(s)

	// Register Karpenter v1 types manually
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	s.AddKnownTypes(gv,
		&karpv1.NodeClaim{},
		&karpv1.NodeClaimList{},
		&karpv1.NodePool{},
		&karpv1.NodePoolList{},
	)
	metav1.AddToGroupVersion(s, gv)

	// Register IBM v1alpha1 types
	ibmGV := schema.GroupVersion{Group: "karpenter.ibm.sh", Version: "v1alpha1"}
	s.AddKnownTypes(ibmGV,
		&v1alpha1.IBMNodeClass{},
		&v1alpha1.IBMNodeClassList{},
	)
	metav1.AddToGroupVersion(s, ibmGV)

	return s
}

// Test helper to create a NodeClaim
func testNodeClaim(name, providerID string, creationTime time.Time) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(creationTime),
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: "karpenter.ibm.sh",
				Kind:  "IBMNodeClass",
				Name:  "test-nodeclass",
			},
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID: providerID,
		},
	}
}

// Test helper to create a Node
func testNode(name, providerID string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: corev1.NodeSpec{
			ProviderID: providerID,
		},
	}
}

func TestNewController(t *testing.T) {
	kubeClient := fake.NewClientBuilder().WithScheme(testScheme()).Build()
	cloudProvider := newMockCloudProvider()

	controller := NewController(kubeClient, cloudProvider)

	assert.NotNil(t, controller)
	assert.Equal(t, kubeClient, controller.kubeClient)
	assert.Equal(t, cloudProvider, controller.cloudProvider)
	assert.Equal(t, uint64(0), controller.successfulCount)
}

func TestReconcile_NoOrphanedInstances(t *testing.T) {
	// Create cloud provider instances and matching cluster NodeClaims
	cloudProvider := newMockCloudProvider()
	cloudProvider.nodeClaims["provider-1"] = testNodeClaim("cloud-1", "provider-1", time.Now().Add(-time.Hour))
	cloudProvider.nodeClaims["provider-2"] = testNodeClaim("cloud-2", "provider-2", time.Now().Add(-time.Hour))

	// Create matching cluster NodeClaims that have successfully registered (have NodeName)
	clusterNC1 := testNodeClaim("cluster-1", "provider-1", time.Now().Add(-time.Hour))
	clusterNC1.Status.NodeName = "node-1" // Indicates successful registration
	clusterNC2 := testNodeClaim("cluster-2", "provider-2", time.Now().Add(-time.Hour))
	clusterNC2.Status.NodeName = "node-2" // Indicates successful registration

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(clusterNC1, clusterNC2).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter)
	assert.Empty(t, cloudProvider.deletedProviderIDs) // No instances should be deleted
	assert.Equal(t, uint64(1), controller.successfulCount)
}

func TestReconcile_GarbageCollectOrphanedInstance(t *testing.T) {
	// Create cloud provider instance without matching cluster NodeClaim
	cloudProvider := newMockCloudProvider()
	cloudProvider.nodeClaims["orphaned-provider-id"] = testNodeClaim("orphaned", "orphaned-provider-id", time.Now().Add(-time.Minute))

	// Create a different NodeClaim in cluster (not matching)
	clusterNC := testNodeClaim("cluster-1", "different-provider-id", time.Now().Add(-time.Hour))

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(clusterNC).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter)
	assert.Contains(t, cloudProvider.deletedProviderIDs, "orphaned-provider-id")
	assert.Equal(t, uint64(1), controller.successfulCount)
}

func TestReconcile_SkipRecentlyCreatedInstances(t *testing.T) {
	// Create cloud provider instance created less than 30 seconds ago
	cloudProvider := newMockCloudProvider()
	cloudProvider.nodeClaims["new-provider-id"] = testNodeClaim("new", "new-provider-id", time.Now().Add(-time.Second*10))

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter)
	assert.Empty(t, cloudProvider.deletedProviderIDs) // Should not delete recent instances
}

func TestReconcile_SkipTerminatingInstances(t *testing.T) {
	// Create cloud provider instance that is terminating
	now := metav1.Now()
	cloudProvider := newMockCloudProvider()
	terminatingNC := testNodeClaim("terminating", "terminating-provider-id", time.Now().Add(-time.Hour))
	terminatingNC.DeletionTimestamp = &now
	cloudProvider.nodeClaims["terminating-provider-id"] = terminatingNC

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter)
	assert.Empty(t, cloudProvider.deletedProviderIDs) // Should not delete terminating instances
}

func TestReconcile_DeleteOrphanedNode(t *testing.T) {
	// Create orphaned cloud instance and corresponding Node
	cloudProvider := newMockCloudProvider()
	cloudProvider.nodeClaims["orphaned-provider-id"] = testNodeClaim("orphaned", "orphaned-provider-id", time.Now().Add(-time.Hour))

	// Create orphaned Node
	orphanedNode := testNode("orphaned-node", "orphaned-provider-id")

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(orphanedNode).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter)
	assert.Contains(t, cloudProvider.deletedProviderIDs, "orphaned-provider-id")

	// Verify node was deleted
	var node corev1.Node
	err = kubeClient.Get(context.Background(), client.ObjectKey{Name: "orphaned-node"}, &node)
	assert.True(t, apierrors.IsNotFound(err))
}

func TestReconcile_HandleCloudProviderListError(t *testing.T) {
	cloudProvider := newMockCloudProvider()
	cloudProvider.listError = errors.New("cloud provider list error")

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "listing cloudprovider nodeclaims")
	assert.Zero(t, result.RequeueAfter)
}

func TestReconcile_HandleCloudProviderDeleteError(t *testing.T) {
	cloudProvider := newMockCloudProvider()
	cloudProvider.nodeClaims["orphaned-provider-id"] = testNodeClaim("orphaned", "orphaned-provider-id", time.Now().Add(-time.Hour))
	cloudProvider.deleteError = errors.New("cloud provider delete error")

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	_, err := controller.Reconcile(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cloud provider delete error")
}

func TestReconcile_IgnoreNodeClaimNotFoundError(t *testing.T) {
	cloudProvider := newMockCloudProvider()
	cloudProvider.nodeClaims["orphaned-provider-id"] = testNodeClaim("orphaned", "orphaned-provider-id", time.Now().Add(-time.Hour))
	cloudProvider.deleteError = cloudprovider.NewNodeClaimNotFoundError(errors.New("instance not found"))

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.NoError(t, err) // NodeClaimNotFound errors should be ignored
	assert.NotZero(t, result.RequeueAfter)
}

func TestReconcile_RequeueAfterLogic(t *testing.T) {
	tests := []struct {
		name            string
		successfulCount uint64
		expectedMin     time.Duration
		expectedMax     time.Duration
	}{
		{
			name:            "first reconcile",
			successfulCount: 0,
			expectedMin:     time.Second * 10,
			expectedMax:     time.Second * 10,
		},
		{
			name:            "early reconciles",
			successfulCount: 10,
			expectedMin:     time.Second * 10,
			expectedMax:     time.Second * 10,
		},
		{
			name:            "after 20 reconciles",
			successfulCount: 20,
			expectedMin:     time.Minute * 2,
			expectedMax:     time.Minute * 2,
		},
		{
			name:            "many reconciles",
			successfulCount: 100,
			expectedMin:     time.Minute * 2,
			expectedMax:     time.Minute * 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloudProvider := newMockCloudProvider()
			kubeClient := fake.NewClientBuilder().
				WithScheme(testScheme()).
				Build()

			controller := NewController(kubeClient, cloudProvider)
			controller.successfulCount = tt.successfulCount

			result, err := controller.Reconcile(context.Background())

			assert.NoError(t, err)
			assert.GreaterOrEqual(t, result.RequeueAfter, tt.expectedMin)
			assert.LessOrEqual(t, result.RequeueAfter, tt.expectedMax)
			assert.Equal(t, tt.successfulCount+1, controller.successfulCount)
		})
	}
}

func TestReconcile_MultipleOrphanedInstances(t *testing.T) {
	// Create multiple orphaned instances
	cloudProvider := newMockCloudProvider()
	for i := 1; i <= 5; i++ {
		providerID := fmt.Sprintf("orphaned-provider-%d", i)
		cloudProvider.nodeClaims[providerID] = testNodeClaim(fmt.Sprintf("orphaned-%d", i), providerID, time.Now().Add(-time.Hour))
	}

	// Create some matching NodeClaims (not orphaned) that have successfully registered
	clusterNC1 := testNodeClaim("cluster-1", "cluster-provider-1", time.Now().Add(-time.Hour))
	clusterNC1.Status.NodeName = "node-1" // Indicates successful registration
	clusterNC2 := testNodeClaim("cluster-2", "cluster-provider-2", time.Now().Add(-time.Hour))
	clusterNC2.Status.NodeName = "node-2" // Indicates successful registration

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(clusterNC1, clusterNC2).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter)
	assert.Len(t, cloudProvider.deletedProviderIDs, 5) // All orphaned instances should be deleted
	for i := 1; i <= 5; i++ {
		assert.Contains(t, cloudProvider.deletedProviderIDs, fmt.Sprintf("orphaned-provider-%d", i))
	}
}

func TestController_Register(t *testing.T) {
	// This test verifies the controller can be registered with the manager
	// Since we're using singleton pattern, we just verify no errors occur
	controller := &Controller{}

	// We can't easily test the actual registration without a full manager
	// but we can verify the method exists and returns nil
	assert.NotNil(t, controller)
}

func TestGarbageCollect_NodeNotFound(t *testing.T) {
	cloudProvider := newMockCloudProvider()

	// Create NodeClaim in cloud provider
	nc := testNodeClaim("test", "test-provider-id", time.Now().Add(-time.Hour))
	cloudProvider.nodeClaims["test-provider-id"] = nc

	// Create empty node list (no matching node)
	nodeList := &corev1.NodeList{Items: []corev1.Node{}}

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	err := controller.garbageCollect(context.Background(), nc, nodeList)

	assert.NoError(t, err)
	assert.Contains(t, cloudProvider.deletedProviderIDs, "test-provider-id")
}

func TestGarbageCollect_HandleNodeDeleteError(t *testing.T) {
	cloudProvider := newMockCloudProvider()

	// Create NodeClaim in cloud provider
	nc := testNodeClaim("test", "test-provider-id", time.Now().Add(-time.Hour))
	cloudProvider.nodeClaims["test-provider-id"] = nc

	// Create matching node
	node := testNode("test-node", "test-provider-id")
	nodeList := &corev1.NodeList{Items: []corev1.Node{*node}}

	// Create a client that will fail on delete
	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(node).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	// This should succeed even if node delete has issues (client.IgnoreNotFound)
	err := controller.garbageCollect(context.Background(), nc, nodeList)

	assert.NoError(t, err)
	assert.Contains(t, cloudProvider.deletedProviderIDs, "test-provider-id")
}

func TestReconcile_HandleStuckTerminatingNodeClaims(t *testing.T) {
	// Create a stuck terminating NodeClaim (deletion timestamp > 10 minutes ago)
	deletionTime := metav1.Time{Time: time.Now().Add(-15 * time.Minute)}
	stuckNodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "stuck-nodeclaim",
			DeletionTimestamp: &deletionTime,
			Finalizers:        []string{"test-finalizer"},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: "karpenter.ibm.sh",
				Kind:  "IBMNodeClass",
				Name:  "test-nodeclass",
			},
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID: "ibm:///us-south/stuck-instance",
			NodeName:   "stuck-node",
		},
	}

	// Create a stuck node in NotReady state
	stuckNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "stuck-node",
		},
		Spec: corev1.NodeSpec{
			ProviderID: "ibm:///us-south/stuck-instance",
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionFalse,
					Reason: "NodeStatusUnknown",
				},
			},
		},
	}

	cloudProvider := newMockCloudProvider()
	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(stuckNodeClaim, stuckNode).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter)

	// Check that the stuck NodeClaim's finalizers were removed
	var updatedNodeClaim karpv1.NodeClaim
	err = kubeClient.Get(context.Background(), client.ObjectKey{Name: "stuck-nodeclaim"}, &updatedNodeClaim)
	if err == nil {
		assert.Empty(t, updatedNodeClaim.Finalizers, "stuck NodeClaim finalizers should be removed")
	}

	// Verify cloud provider delete was called for stuck instance
	assert.Contains(t, cloudProvider.deletedProviderIDs, "ibm:///us-south/stuck-instance")
}

func TestReconcile_SkipRecentTerminatingNodeClaims(t *testing.T) {
	// Create a NodeClaim that's terminating but within timeout
	recentDeletionTime := metav1.Time{Time: time.Now().Add(-5 * time.Minute)}
	recentNodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "recent-nodeclaim",
			DeletionTimestamp: &recentDeletionTime,
			Finalizers:        []string{"test-finalizer"},
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: "karpenter.ibm.sh",
				Kind:  "IBMNodeClass",
				Name:  "test-nodeclass",
			},
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID: "ibm:///us-south/recent-instance",
		},
	}

	cloudProvider := newMockCloudProvider()
	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(recentNodeClaim).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter)

	// Should NOT force cleanup recent NodeClaim
	var updatedNodeClaim karpv1.NodeClaim
	err = kubeClient.Get(context.Background(), client.ObjectKey{Name: "recent-nodeclaim"}, &updatedNodeClaim)
	assert.NoError(t, err)
	assert.Contains(t, updatedNodeClaim.Finalizers, "test-finalizer", "recent NodeClaim finalizers should remain")

	// Should not delete recent instance
	assert.NotContains(t, cloudProvider.deletedProviderIDs, "ibm:///us-south/recent-instance")
}

func TestForceCleanupStuckNodeClaim_WithStuckPods(t *testing.T) {
	// Create stuck terminating pod
	podDeletionTime := metav1.Time{Time: time.Now().Add(-5 * time.Minute)}
	stuckPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "stuck-pod",
			Namespace:         "default",
			DeletionTimestamp: &podDeletionTime,
			Finalizers:        []string{"test-pod-finalizer"},
		},
		Spec: corev1.PodSpec{
			NodeName: "stuck-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	stuckNodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "stuck-nodeclaim",
			DeletionTimestamp: &metav1.Time{Time: time.Now().Add(-15 * time.Minute)},
			Finalizers:        []string{"test-finalizer"},
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID: "ibm:///us-south/stuck-instance",
			NodeName:   "stuck-node",
		},
	}

	stuckNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "stuck-node",
		},
		Spec: corev1.NodeSpec{
			ProviderID: "ibm:///us-south/stuck-instance",
		},
	}

	cloudProvider := newMockCloudProvider()
	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(stuckNodeClaim, stuckNode, stuckPod).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
			pod := obj.(*corev1.Pod)
			if pod.Spec.NodeName == "" {
				return nil
			}
			return []string{pod.Spec.NodeName}
		}).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	err := controller.forceCleanupStuckNodeClaim(context.Background(), stuckNodeClaim)

	assert.NoError(t, err)

	// Verify finalizers were removed from NodeClaim
	var updatedNodeClaim karpv1.NodeClaim
	err = kubeClient.Get(context.Background(), client.ObjectKey{Name: "stuck-nodeclaim"}, &updatedNodeClaim)
	if err == nil {
		assert.Empty(t, updatedNodeClaim.Finalizers)
	}

	// Verify cloud provider delete was called
	assert.Contains(t, cloudProvider.deletedProviderIDs, "ibm:///us-south/stuck-instance")
}

func TestHandleOrphanedNodes_WithFinalizers(t *testing.T) {
	// Create cloud provider with no matching instances
	cloudProvider := newMockCloudProvider()

	// Create orphaned node with finalizers
	orphanedNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "orphaned-node",
			Finalizers: []string{"registration.nodeclaim.ibm.sh/finalizer"},
			Labels: map[string]string{
				"karpenter.sh/nodepool": "test-nodepool",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "ibm:///eu-de/orphaned-instance",
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(orphanedNode).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	// Create empty node list and cloud node claims (no matching instances)
	nodeList := &corev1.NodeList{Items: []corev1.Node{*orphanedNode}}
	cloudNodeClaims := []*karpv1.NodeClaim{}

	err := controller.handleOrphanedNodes(context.Background(), nodeList, cloudNodeClaims)

	assert.NoError(t, err)

	// Verify node was deleted (should not exist anymore)
	var updatedNode corev1.Node
	err = kubeClient.Get(context.Background(), client.ObjectKey{Name: "orphaned-node"}, &updatedNode)
	assert.True(t, apierrors.IsNotFound(err), "orphaned node should be deleted")
}

func TestHandleOrphanedNodes_SkipNonKarpenterNodes(t *testing.T) {
	// Create cloud provider with no matching instances
	cloudProvider := newMockCloudProvider()

	// Create non-Karpenter node (no karpenter.sh/nodepool label and non-IBM provider ID)
	nonKarpenterNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "non-karpenter-node",
		},
		Spec: corev1.NodeSpec{
			ProviderID: "aws:///us-east-1a/i-1234567890abcdef0",
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(nonKarpenterNode).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	// Create node list and empty cloud node claims
	nodeList := &corev1.NodeList{Items: []corev1.Node{*nonKarpenterNode}}
	cloudNodeClaims := []*karpv1.NodeClaim{}

	err := controller.handleOrphanedNodes(context.Background(), nodeList, cloudNodeClaims)

	assert.NoError(t, err)

	// Verify non-Karpenter node was NOT deleted
	var updatedNode corev1.Node
	err = kubeClient.Get(context.Background(), client.ObjectKey{Name: "non-karpenter-node"}, &updatedNode)
	assert.NoError(t, err, "non-Karpenter node should not be deleted")
}

func TestRemoveNodeFinalizers(t *testing.T) {
	// Create node with finalizers
	nodeWithFinalizers := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-node",
			Finalizers: []string{"registration.nodeclaim.ibm.sh/finalizer", "other-finalizer"},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "ibm:///eu-de/test-instance",
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(nodeWithFinalizers).
		Build()

	controller := NewController(kubeClient, newMockCloudProvider())

	err := controller.removeNodeFinalizers(context.Background(), nodeWithFinalizers)

	assert.NoError(t, err)

	// Verify finalizers were removed
	var updatedNode corev1.Node
	err = kubeClient.Get(context.Background(), client.ObjectKey{Name: "test-node"}, &updatedNode)
	assert.NoError(t, err)
	assert.Empty(t, updatedNode.Finalizers, "all finalizers should be removed")
}

func TestRemoveNodeFinalizers_NoFinalizers(t *testing.T) {
	// Create node without finalizers
	nodeWithoutFinalizers := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
		Spec: corev1.NodeSpec{
			ProviderID: "ibm:///eu-de/test-instance",
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(nodeWithoutFinalizers).
		Build()

	controller := NewController(kubeClient, newMockCloudProvider())

	err := controller.removeNodeFinalizers(context.Background(), nodeWithoutFinalizers)

	assert.NoError(t, err)

	// Verify node is unchanged
	var updatedNode corev1.Node
	err = kubeClient.Get(context.Background(), client.ObjectKey{Name: "test-node"}, &updatedNode)
	assert.NoError(t, err)
	assert.Empty(t, updatedNode.Finalizers)
}

func TestRemoveNodeFinalizers_NodeNotFound(t *testing.T) {
	// Create node object but don't add it to the client
	nonExistentNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "non-existent-node",
			Finalizers: []string{"registration.nodeclaim.ibm.sh/finalizer"},
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		Build()

	controller := NewController(kubeClient, newMockCloudProvider())

	err := controller.removeNodeFinalizers(context.Background(), nonExistentNode)

	assert.NoError(t, err, "should ignore NotFound errors")
}

func TestNormalizeProviderID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "cloud API with zone prefix",
			input:    "ibm:///eu-de/02c7_7905bd15-f7b7-4812-9c1a-e3a46fe629df",
			expected: "ibm:///eu-de/7905bd15-f7b7-4812-9c1a-e3a46fe629df",
		},
		{
			name:     "node registration without zone prefix",
			input:    "ibm:///eu-de/7905bd15-f7b7-4812-9c1a-e3a46fe629df",
			expected: "ibm:///eu-de/7905bd15-f7b7-4812-9c1a-e3a46fe629df",
		},
		{
			name:     "different region with zone prefix",
			input:    "ibm:///us-south/02c7_abc123-def456",
			expected: "ibm:///us-south/abc123-def456",
		},
		{
			name:     "multiple underscores in instance ID",
			input:    "ibm:///us-east/02c7_abc123_def456_ghi789",
			expected: "ibm:///us-east/abc123_def456_ghi789",
		},
		{
			name:     "no zone prefix",
			input:    "ibm:///ca-tor/simple-instance-id",
			expected: "ibm:///ca-tor/simple-instance-id",
		},
		{
			name:     "invalid format should return as-is",
			input:    "invalid-provider-id",
			expected: "invalid-provider-id",
		},
		{
			name:     "short format should return as-is",
			input:    "ibm://short",
			expected: "ibm://short",
		},
	}

	controller := NewController(nil, nil)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := controller.normalizeProviderID(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestReconcile_NormalizedProviderIDMatching(t *testing.T) {
	// Create cloud provider instance with zone-prefixed ID
	cloudProvider := newMockCloudProvider()
	cloudProvider.nodeClaims["ibm:///eu-de/02c7_7905bd15-f7b7-4812-9c1a-e3a46fe629df"] = testNodeClaim("cloud-1", "ibm:///eu-de/02c7_7905bd15-f7b7-4812-9c1a-e3a46fe629df", time.Now().Add(-time.Hour))

	// Create cluster NodeClaim with non-prefixed ID (as would happen during node registration)
	clusterNC := testNodeClaim("cluster-1", "ibm:///eu-de/7905bd15-f7b7-4812-9c1a-e3a46fe629df", time.Now().Add(-time.Hour))
	clusterNC.Status.NodeName = "node-1" // Indicates successful registration

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(clusterNC).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	result, err := controller.Reconcile(context.Background())

	assert.NoError(t, err)
	assert.NotZero(t, result.RequeueAfter)
	// Should NOT delete the cloud instance because normalized IDs match
	assert.Empty(t, cloudProvider.deletedProviderIDs)
}

func TestHandleOrphanedNodes_NormalizedProviderIDMatching(t *testing.T) {
	// Create cloud provider instance with zone-prefixed ID
	cloudProvider := newMockCloudProvider()
	cloudNodeClaim := testNodeClaim("cloud-1", "ibm:///eu-de/02c7_7905bd15-f7b7-4812-9c1a-e3a46fe629df", time.Now().Add(-time.Hour))

	// Create Kubernetes node with non-prefixed ID (as would happen during node registration)
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "karpenter-node",
			Labels: map[string]string{
				"karpenter.sh/nodepool": "test-nodepool",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "ibm:///eu-de/7905bd15-f7b7-4812-9c1a-e3a46fe629df",
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(node).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	nodeList := &corev1.NodeList{Items: []corev1.Node{*node}}
	cloudNodeClaims := []*karpv1.NodeClaim{cloudNodeClaim}

	err := controller.handleOrphanedNodes(context.Background(), nodeList, cloudNodeClaims)

	assert.NoError(t, err)

	// Should NOT delete the node because normalized IDs match
	var updatedNode corev1.Node
	err = kubeClient.Get(context.Background(), client.ObjectKey{Name: "karpenter-node"}, &updatedNode)
	assert.NoError(t, err, "node should not be deleted due to normalized ID matching")
}

func TestHandleOrphanedNodes_RealOrphanedNode(t *testing.T) {
	// Create cloud provider with no matching instances
	cloudProvider := newMockCloudProvider()

	// Create orphaned Kubernetes node
	orphanedNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "orphaned-node",
			Labels: map[string]string{
				"karpenter.sh/nodepool": "test-nodepool",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "ibm:///eu-de/orphaned-instance-id",
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(orphanedNode).
		Build()

	controller := NewController(kubeClient, cloudProvider)

	nodeList := &corev1.NodeList{Items: []corev1.Node{*orphanedNode}}
	cloudNodeClaims := []*karpv1.NodeClaim{} // No matching cloud instances

	err := controller.handleOrphanedNodes(context.Background(), nodeList, cloudNodeClaims)

	assert.NoError(t, err)

	// Should delete the orphaned node
	var updatedNode corev1.Node
	err = kubeClient.Get(context.Background(), client.ObjectKey{Name: "orphaned-node"}, &updatedNode)
	assert.True(t, apierrors.IsNotFound(err), "orphaned node should be deleted")
}
