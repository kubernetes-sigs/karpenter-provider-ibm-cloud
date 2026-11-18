//go:build e2e
// +build e2e

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
package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestCleanupE2EEnvironment(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	require.NoError(t, suite.cleanupSelectedResources(context.Background(), ""))
}

// TestE2ECleanupNodePoolDeletion tests proper cleanup when deleting NodePools
func TestE2ECleanupNodePoolDeletion(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-nodepool-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	t.Logf("Starting NodePool cleanup test: %s", testName)
	ctx := context.Background()

	// Create NodeClass
	nodeClass := suite.createTestNodeClass(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Create NodePool with 2 replicas for better testing
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Create test workload with 2 replicas to trigger provisioning
	deployment := suite.createTestWorkloadWithReplicas(t, testName, 2)
	t.Logf("Created test workload with 2 replicas: %s", deployment.Name)

	// Wait for pods to be scheduled and nodes to be provisioned
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")
	t.Logf("Pods scheduled successfully")

	// Get initial list of nodes
	initialNodes := suite.getKarpenterNodes(t, nodePool.Name)
	require.Greater(t, len(initialNodes), 0, "Should have provisioned at least one node")
	t.Logf("Initial provisioned nodes: %d", len(initialNodes))
	var originalPods corev1.PodList
	require.NoError(t, suite.kubeClient.List(ctx, &originalPods,
		client.InNamespace(deployment.Namespace), client.MatchingLabels(deployment.Spec.Selector.MatchLabels)))
	require.NotEmpty(t, originalPods.Items, "Should have running workload pods before deletion")

	// Delete the NodePool first - this should trigger cleanup
	err := suite.kubeClient.Delete(ctx, nodePool, client.Preconditions{UID: &nodePool.UID})
	require.NoError(t, err)
	t.Logf("Deleted NodePool: %s", nodePool.Name)

	// Wait for pods to be evicted
	require.Eventually(t, func() bool {
		for _, original := range originalPods.Items {
			var current corev1.Pod
			err := suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(&original), &current)
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil || current.UID == original.UID {
				return false
			}
		}
		return true
	}, 10*time.Minute, pollInterval, "Original workload pods should be evicted after NodePool deletion")
	t.Logf("Pods evicted successfully")

	// Wait for nodes to be cleaned up using proper polling
	suite.waitForNodesCleanedUp(t, nodePool.Name, 10*time.Minute)

	// Cleanup remaining resources
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.cleanupTestResources(t, testName)
	t.Logf("NodePool cleanup test completed successfully: %s", testName)
}

// TestE2ECleanupNodeClassDeletion tests proper cleanup when deleting NodeClasses
func TestE2ECleanupNodeClassDeletion(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-nodeclass-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	t.Logf("Starting NodeClass cleanup test: %s", testName)
	ctx := context.Background()

	// Create NodeClass
	nodeClass := suite.createTestNodeClass(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Create NodePool that references this NodeClass
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Create test workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName)
	t.Logf("Created test workload: %s", deployment.Name)

	// Wait for pods to be scheduled and nodes to be provisioned
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")
	t.Logf("Pods scheduled successfully")

	// Get list of provisioned NodeClaims
	var nodeClaimList karpv1.NodeClaimList
	err := suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
		"karpenter.sh/nodepool": nodePool.Name,
	})
	require.NoError(t, err)
	require.Greater(t, len(nodeClaimList.Items), 0, "Should have NodeClaims provisioned")
	initialNodeClaims := len(nodeClaimList.Items)
	t.Logf("Initial NodeClaims: %d", initialNodeClaims)

	// Delete the deployment first to reduce resource pressure
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.waitForPodsGone(t, deployment.Name)

	// Delete the NodePool first
	err = suite.kubeClient.Delete(ctx, nodePool, client.Preconditions{UID: &nodePool.UID})
	require.NoError(t, err)
	t.Logf("Deleted NodePool: %s", nodePool.Name)

	// Wait for NodeClaims to be cleaned up using proper polling
	suite.waitForNodeClaimsCleanedUp(t, nodePool.Name, 10*time.Minute)

	// Now try to delete the NodeClass - it should succeed if no NodePools reference it
	err = suite.kubeClient.Delete(ctx, nodeClass, client.Preconditions{UID: &nodeClass.UID})
	require.NoError(t, err)
	t.Logf("Successfully deleted NodeClass: %s", nodeClass.Name)

	// Cleanup any remaining resources
	suite.cleanupTestResources(t, testName)
	t.Logf("NodeClass cleanup test completed successfully: %s", testName)
}

// TestE2ECleanupOrphanedResources tests cleanup of orphaned resources
func TestE2ECleanupOrphanedResources(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-orphaned-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	t.Logf("Starting orphaned resources cleanup test: %s", testName)
	ctx := context.Background()

	// Create NodeClass and NodePool
	nodeClass := suite.createTestNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)

	// Create test workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName)
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Get the provisioned NodeClaim
	var nodeClaimList karpv1.NodeClaimList
	err := suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
		"karpenter.sh/nodepool": nodePool.Name,
	})
	require.NoError(t, err)
	require.Greater(t, len(nodeClaimList.Items), 0, "Should have NodeClaims provisioned")
	originalNodeClaim := nodeClaimList.Items[0]

	// Simulate an orphaned state by manually deleting the NodePool while keeping NodeClaims
	err = suite.kubeClient.Delete(ctx, nodePool, client.Preconditions{UID: &nodePool.UID})
	require.NoError(t, err)
	t.Logf("Deleted NodePool, leaving NodeClaim potentially orphaned: %s", originalNodeClaim.Name)

	// Wait for the orphaned NodeClaim to be automatically cleaned up
	suite.waitForNodeClaimCleanedUp(t, originalNodeClaim.Name, 5*time.Minute)

	// Clean up workload
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.waitForPodsGone(t, deployment.Name)

	// Use our comprehensive cleanup function to catch any remaining orphaned resources
	suite.cleanupOrphanedKubernetesResources(t)

	// Final cleanup
	suite.cleanupTestResources(t, testName)
	t.Logf("Orphaned resources cleanup test completed: %s", testName)
}

// TestE2ECleanupIBMCloudResources tests cleanup of IBM Cloud resources
func TestE2ECleanupIBMCloudResources(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-ibmcloud-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	t.Logf("Starting IBM Cloud resources cleanup test: %s", testName)
	ctx := context.Background()

	// Get initial list of IBM Cloud instances for comparison
	initialInstances, err := suite.getIBMCloudInstances(t)
	require.NoError(t, err)
	initialInstanceCount := len(initialInstances)
	t.Logf("Initial IBM Cloud instances: %d", initialInstanceCount)

	// Create NodeClass and NodePool
	nodeClass := suite.createTestNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)

	// Create test workload with 2 replicas to trigger provisioning
	deployment := suite.createTestWorkloadWithReplicas(t, testName, 2)
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Get the list of instances after provisioning (pods scheduled means instances exist)
	instancesAfterProvisioning, err := suite.getIBMCloudInstances(t)
	require.NoError(t, err)
	afterProvisioningCount := len(instancesAfterProvisioning)
	t.Logf("Instances after provisioning: %d (expected increase: %d)",
		afterProvisioningCount, afterProvisioningCount-initialInstanceCount)

	var provisioned []string
	for id := range instancesAfterProvisioning {
		if _, existed := initialInstances[id]; !existed {
			provisioned = append(provisioned, id)
		}
	}
	require.NotEmpty(t, provisioned, "Should have provisioned new IBM Cloud instances")

	// Start cleanup process
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.waitForPodsGone(t, deployment.Name)

	// Delete NodePool to trigger instance cleanup
	err = suite.kubeClient.Delete(ctx, nodePool, client.Preconditions{UID: &nodePool.UID})
	require.NoError(t, err)
	t.Logf("Deleted NodePool, waiting for IBM Cloud instances to be cleaned up")

	suite.waitForInstancesGone(t, provisioned, 15*time.Minute)

	// Cleanup remaining test resources
	suite.cleanupTestResources(t, testName)
	t.Logf("IBM Cloud resources cleanup test completed: %s", testName)
}

func cleanupTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, policyv1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	group := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	scheme.AddKnownTypes(group, &karpv1.NodePool{}, &karpv1.NodePoolList{}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	metav1.AddToGroupVersion(scheme, group)
	return scheme
}

func cleanupClass(name, testName string) *v1alpha1.IBMNodeClass {
	return &v1alpha1.IBMNodeClass{ObjectMeta: metav1.ObjectMeta{Name: name, UID: "class-uid", Labels: map[string]string{"test": "e2e", "test-name": testName}}}
}

func cleanupPool(name, testName string, class *v1alpha1.IBMNodeClass) *karpv1.NodePool {
	return &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "pool-uid", Labels: map[string]string{"test": "e2e", "test-name": testName}},
		Spec: karpv1.NodePoolSpec{Template: karpv1.NodeClaimTemplate{Spec: karpv1.NodeClaimTemplateSpec{
			NodeClassRef: &karpv1.NodeClassReference{Group: v1alpha1.Group, Kind: "IBMNodeClass", Name: class.Name},
		}}},
	}
}

func cleanupClaim(name string, pool *karpv1.NodePool) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "claim-uid", Labels: map[string]string{karpv1.NodePoolLabelKey: pool.Name},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodePool", Name: pool.Name, UID: pool.UID}}},
		Spec: karpv1.NodeClaimSpec{NodeClassRef: pool.Spec.Template.Spec.NodeClassRef},
	}
}

func TestCleanupOwnedResourcesPreservesForeignResources(t *testing.T) {
	ctx := context.Background()
	class := cleanupClass("owned-class", "owned")
	class.Labels = map[string]string{"created-by": "karpenter-e2e"}
	pool := cleanupPool("owned-pool", "owned", class)
	claim := cleanupClaim("generated-claim", pool)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "owned-workload", Namespace: "default", UID: "deployment-uid", Labels: map[string]string{"purpose": "karpenter-test"}}}
	legacy := &v1alpha1.IBMNodeClass{ObjectMeta: metav1.ObjectMeta{Name: "e2e-test-1700000000-nodeclass", UID: "legacy-uid"}}
	foreign := []client.Object{
		&v1alpha1.IBMNodeClass{ObjectMeta: metav1.ObjectMeta{Name: "production-nodeclass", UID: "foreign-class", Finalizers: []string{"karpenter-ibm.sh/termination"}}},
		&karpv1.NodePool{ObjectMeta: metav1.ObjectMeta{Name: "production-nodepool", UID: "foreign-pool", Finalizers: []string{"example.com/finalizer"}}},
		&karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "production-claim", UID: "foreign-claim", Finalizers: []string{karpv1.TerminationFinalizer}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "production-workload", Namespace: "default", UID: "foreign-deployment"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "prefix-e2e-test-1700000000-workload", Namespace: "default", UID: "foreign-prefix"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "controller", Namespace: "karpenter", UID: "controller-uid", Labels: map[string]string{"test": "e2e"}}},
	}
	objects := append([]client.Object{class, pool, claim, deployment, legacy}, foreign...)
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(objects...).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.NoError(t, suite.cleanupSelectedResources(ctx, ""))
	require.NoError(t, suite.waitForStaleResourcesGone(ctx, t))
	for _, object := range foreign {
		current := object.DeepCopyObject().(client.Object)
		require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(object), current))
		require.Equal(t, object.GetUID(), current.GetUID())
		require.Equal(t, object.GetFinalizers(), current.GetFinalizers())
		require.True(t, current.GetDeletionTimestamp().IsZero())
	}
	for _, object := range []client.Object{class, pool, claim, deployment, legacy} {
		current := object.DeepCopyObject().(client.Object)
		require.True(t, apierrors.IsNotFound(kube.Get(ctx, client.ObjectKeyFromObject(object), current)))
	}
}

func TestCleanupCurrentTestPreservesOtherTests(t *testing.T) {
	ctx := context.Background()
	class := cleanupClass("current-class", "current")
	pool := cleanupPool("current-pool", "current", class)
	claim := cleanupClaim("generated-current", pool)
	claim.OwnerReferences[0].APIVersion = "karpenter.sh/v1beta1"
	otherClass := cleanupClass("other-class", "other")
	otherPool := cleanupPool("other-pool", "other", otherClass)
	otherPool.UID = "other-pool-uid"
	otherClaim := cleanupClaim("generated-other", otherPool)
	otherClaim.UID = "other-claim-uid"
	currentWorkload := createResourceIntensiveWorkload("test-deployment-current", "current", nil, map[string]string{"test": "current"})
	currentWorkload.UID = "workload-uid"
	intolerantWorkload := createResourceIntensiveWorkload("test-deployment-intolerant", "currentintolerant", nil, map[string]string{"test": "current"})
	intolerantWorkload.UID = "intolerant-workload-uid"
	require.Equal(t, "current", intolerantWorkload.Spec.Template.Spec.NodeSelector["test"])
	require.Equal(t, "currentintolerant", intolerantWorkload.Spec.Template.Labels["test"])
	otherWorkload := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "current-other-workload", Namespace: "default", UID: "other-workload-uid", Labels: map[string]string{"test": "e2e", "test-name": "current-other"}}}
	currentDaemonSet := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "taint-remover-current", Namespace: "default", UID: "daemonset-uid", Labels: taintTestLabels("current")}}
	otherDaemonSet := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "taint-remover-other", Namespace: "default", UID: "other-daemonset-uid", Labels: taintTestLabels("other")}}
	var deletionOrder []string
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(class, pool, claim, otherClass, otherPool, otherClaim, currentWorkload, intolerantWorkload, otherWorkload, currentDaemonSet, otherDaemonSet).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.DeleteOption) error {
			deleteOptions := (&client.DeleteOptions{}).ApplyOptions(options)
			require.NotNil(t, deleteOptions.Preconditions)
			require.Equal(t, object.GetUID(), *deleteOptions.Preconditions.UID)
			require.Equal(t, object.GetResourceVersion(), *deleteOptions.Preconditions.ResourceVersion)
			require.Nil(t, deleteOptions.GracePeriodSeconds)
			deletionOrder = append(deletionOrder, object.GetName())
			return c.Delete(ctx, object, options...)
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.NoError(t, suite.cleanupSelectedResources(ctx, "current"))
	require.Equal(t, []string{currentDaemonSet.Name, currentWorkload.Name, intolerantWorkload.Name, claim.Name, pool.Name, class.Name}, deletionOrder)
	for _, object := range []client.Object{otherClass, otherPool, otherClaim, otherWorkload, otherDaemonSet} {
		require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(object), object.DeepCopyObject().(client.Object)))
	}
}

func TestCleanupQuiescesSelectedPoolsBeforeDeletingClaims(t *testing.T) {
	class := cleanupClass("current-class", "current")
	pool := cleanupPool("current-pool", "current", class)
	pool.Spec.Limits = karpv1.Limits{
		corev1.ResourceCPU:    resource.MustParse("8"),
		corev1.ResourceMemory: resource.MustParse("32Gi"),
	}
	claim := cleanupClaim("generated-current", pool)
	foreignPool := cleanupPool("other-pool", "other", cleanupClass("other-class", "other"))
	foreignPool.UID = "other-pool-uid"
	foreignPool.Finalizers = []string{"example.com/protect"}
	foreignPool.Spec.Limits = karpv1.Limits{corev1.ResourceCPU: resource.MustParse("16")}
	claimDeletes := 0
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(class, pool, claim, foreignPool).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.DeleteOption) error {
			if _, ok := object.(*karpv1.NodeClaim); ok {
				claimDeletes++
				current := &karpv1.NodePool{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pool), current))
				cpu := current.Spec.Limits[corev1.ResourceCPU]
				require.True(t, cpu.IsZero(), "Selected pool must stop provisioning before claim deletion")
				require.Equal(t, pool.Spec.Limits[corev1.ResourceMemory], current.Spec.Limits[corev1.ResourceMemory])
				require.Equal(t, pool.Spec.Template, current.Spec.Template)
				require.True(t, current.DeletionTimestamp.IsZero(), "Existing dependent deletion order must be preserved")
				foreign := &karpv1.NodePool{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(foreignPool), foreign))
				require.Equal(t, foreignPool.Spec, foreign.Spec)
				require.Equal(t, foreignPool.Finalizers, foreign.Finalizers)
			}
			return c.Delete(ctx, object, options...)
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.NoError(t, suite.cleanupSelectedResources(t.Context(), "current"))
	require.Equal(t, 1, claimDeletes)
	foreign := &karpv1.NodePool{}
	require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(foreignPool), foreign))
	require.Equal(t, foreignPool.Spec, foreign.Spec)
	require.True(t, foreign.DeletionTimestamp.IsZero())
}

func TestCleanupQuiesceRetriesConflictWithoutLosingConcurrentChanges(t *testing.T) {
	pool := cleanupPool("pool", "current", cleanupClass("class", "current"))
	pool.Finalizers = []string{"example.com/protect"}
	patches := 0
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(pool).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
			patches++
			if patches == 1 {
				current := &karpv1.NodePool{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(object), current))
				current.Spec.Limits = karpv1.Limits{corev1.ResourceMemory: resource.MustParse("64Gi")}
				current.Annotations = map[string]string{"concurrent": "preserved"}
				require.NoError(t, c.Update(ctx, current))
			}
			return c.Patch(ctx, object, patch, options...)
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	scope := cleanupScope{testName: "current", poolUIDs: map[string]types.UID{pool.Name: pool.UID}}
	require.NoError(t, suite.quiesceCleanupPools(t.Context(), scope))
	require.Equal(t, 2, patches, "Optimistic lock must retry after a concurrent update")
	current := &karpv1.NodePool{}
	require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(pool), current))
	cpu := current.Spec.Limits[corev1.ResourceCPU]
	require.True(t, cpu.IsZero())
	require.Equal(t, resource.MustParse("64Gi"), current.Spec.Limits[corev1.ResourceMemory])
	require.Equal(t, "preserved", current.Annotations["concurrent"])
	require.Equal(t, pool.Finalizers, current.Finalizers)
	require.True(t, current.DeletionTimestamp.IsZero())
}

func TestCleanupQuiesceRechecksIdentityAndOwnershipDuringRetry(t *testing.T) {
	for _, change := range []string{"identity", "ownership"} {
		t.Run(change, func(t *testing.T) {
			pool := cleanupPool("pool", "current", cleanupClass("class", "current"))
			pool.Spec.Limits = karpv1.Limits{corev1.ResourceCPU: resource.MustParse("8")}
			patches := 0
			kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(pool).WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, object client.Object, _ client.Patch, _ ...client.PatchOption) error {
					patches++
					current := &karpv1.NodePool{}
					require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(object), current))
					if change == "identity" {
						require.NoError(t, c.Delete(ctx, current))
						current.UID = "replacement-pool-uid"
						current.ResourceVersion = ""
						require.NoError(t, c.Create(ctx, current))
					} else {
						current.Labels["test-name"] = "other"
						require.NoError(t, c.Update(ctx, current))
					}
					return apierrors.NewConflict(schema.GroupResource{Group: "karpenter.sh", Resource: "nodepools"}, object.GetName(), fmt.Errorf("pool changed during cleanup"))
				},
			}).Build()
			suite := &E2ETestSuite{kubeClient: kube}
			scope := cleanupScope{testName: "current", poolUIDs: map[string]types.UID{pool.Name: pool.UID}}
			require.ErrorContains(t, suite.quiesceCleanupPools(t.Context(), scope), "cleanup "+change+" changed")
			require.Equal(t, 1, patches)
			current := &karpv1.NodePool{}
			require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(pool), current))
			require.Equal(t, pool.Spec.Limits, current.Spec.Limits)
			require.True(t, current.DeletionTimestamp.IsZero())
		})
	}
}

func TestCleanupTimeoutPreservesFinalizersAndOwners(t *testing.T) {
	class := cleanupClass("class", "current")
	pool := cleanupPool("pool", "current", class)
	claim := cleanupClaim("claim", pool)
	claim.Finalizers = []string{karpv1.TerminationFinalizer, "loadbalancer.nodeclaim.ibm.sh/finalizer", "karpenter-ibm.sh/vpc-launch"}
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(class, pool, claim).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, suite.cleanupSelectedResources(ctx, "current"), context.DeadlineExceeded)
	currentClaim := &karpv1.NodeClaim{}
	require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(claim), currentClaim))
	require.False(t, currentClaim.DeletionTimestamp.IsZero())
	require.Equal(t, claim.Finalizers, currentClaim.Finalizers)
	for _, object := range []client.Object{pool, class} {
		current := object.DeepCopyObject().(client.Object)
		require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(object), current))
		require.True(t, current.GetDeletionTimestamp().IsZero())
	}
}

func TestCleanupPreservesClassWithForeignDependents(t *testing.T) {
	class := cleanupClass("class", "owned")
	foreignPool := cleanupPool("production-pool", "", class)
	foreignPool.Labels = map[string]string{"team": "production"}
	foreignClaim := cleanupClaim("production-claim", foreignPool)
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(class, foreignPool, foreignClaim).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, suite.cleanupSelectedResources(ctx, ""), context.DeadlineExceeded)
	for _, object := range []client.Object{class, foreignPool, foreignClaim} {
		current := object.DeepCopyObject().(client.Object)
		require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(object), current))
		require.True(t, current.GetDeletionTimestamp().IsZero())
	}
}

func TestCleanupRejectsReplacedResourceIdentity(t *testing.T) {
	original := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "default", UID: "original-uid", Labels: map[string]string{"test": "e2e"}}}
	replacement := original.DeepCopy()
	replacement.UID = "replacement-uid"
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(replacement).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.Error(t, suite.deleteCleanupObject(context.Background(), original, cleanupScope{}))
	current := &appsv1.Deployment{}
	require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(original), current))
	require.Equal(t, replacement.UID, current.UID)
	require.True(t, current.DeletionTimestamp.IsZero())
}

func TestCleanupDeleteRetriesStatusConflictWithFreshPreconditions(t *testing.T) {
	pool := cleanupPool("pool", "current", cleanupClass("class", "current"))
	claim := cleanupClaim("claim", pool)
	claim.Finalizers = []string{karpv1.TerminationFinalizer, "karpenter-ibm.sh/vpc-launch"}
	var versions []string
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithStatusSubresource(&karpv1.NodeClaim{}).WithObjects(claim).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.DeleteOption) error {
			deleteOptions := (&client.DeleteOptions{}).ApplyOptions(options)
			require.NotNil(t, deleteOptions.Preconditions)
			require.Equal(t, claim.UID, *deleteOptions.Preconditions.UID)
			require.Equal(t, object.GetResourceVersion(), *deleteOptions.Preconditions.ResourceVersion)
			versions = append(versions, *deleteOptions.Preconditions.ResourceVersion)
			if len(versions) == 1 {
				current := &karpv1.NodeClaim{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(object), current))
				current.StatusConditions().SetTrue("Launched")
				require.NoError(t, c.Status().Update(ctx, current))
				return apierrors.NewConflict(schema.GroupResource{Group: "karpenter.sh", Resource: "nodeclaims"}, object.GetName(), fmt.Errorf("resource version changed after status update"))
			}
			return c.Delete(ctx, object, options...)
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	scope := cleanupScope{testName: "current", poolUIDs: map[string]types.UID{pool.Name: pool.UID}}
	require.NoError(t, suite.deleteCleanupObject(t.Context(), claim, scope))
	require.Len(t, versions, 2)
	require.NotEqual(t, versions[0], versions[1])
	current := &karpv1.NodeClaim{}
	require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(claim), current))
	require.False(t, current.DeletionTimestamp.IsZero())
	require.Equal(t, claim.Finalizers, current.Finalizers)
	require.True(t, current.StatusConditions().Get("Launched").IsTrue())
}

func TestCleanupDeleteProtectsReplacementUIDDuringRetry(t *testing.T) {
	original := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "default", UID: "original-uid", Labels: map[string]string{"test": "e2e"}}}
	replacement := original.DeepCopy()
	replacement.UID = "replacement-uid"
	replacement.Finalizers = []string{"example.com/protect"}
	deletes := 0
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(original).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, object client.Object, _ ...client.DeleteOption) error {
			deletes++
			require.NoError(t, c.Delete(ctx, object))
			require.NoError(t, c.Create(ctx, replacement))
			return apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, object.GetName(), fmt.Errorf("object was replaced"))
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.Error(t, suite.deleteCleanupObject(t.Context(), original, cleanupScope{}))
	require.Equal(t, 1, deletes)
	current := &appsv1.Deployment{}
	require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(original), current))
	require.Equal(t, replacement.UID, current.UID)
	require.True(t, current.DeletionTimestamp.IsZero())
	require.Equal(t, replacement.Finalizers, current.Finalizers)
}

func TestCleanupDeleteRechecksOwnershipDuringRetry(t *testing.T) {
	pool := cleanupPool("pool", "current", cleanupClass("class", "current"))
	claim := cleanupClaim("claim", pool)
	claim.Finalizers = []string{karpv1.TerminationFinalizer}
	deletes := 0
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(claim).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, object client.Object, _ ...client.DeleteOption) error {
			deletes++
			current := &karpv1.NodeClaim{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(object), current))
			current.OwnerReferences[0].UID = "replacement-pool-uid"
			require.NoError(t, c.Update(ctx, current))
			return apierrors.NewConflict(schema.GroupResource{Group: "karpenter.sh", Resource: "nodeclaims"}, object.GetName(), fmt.Errorf("owner changed"))
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	scope := cleanupScope{testName: "current", poolUIDs: map[string]types.UID{pool.Name: pool.UID}}
	require.Error(t, suite.deleteCleanupObject(t.Context(), claim, scope))
	require.Equal(t, 1, deletes)
	current := &karpv1.NodeClaim{}
	require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(claim), current))
	require.Equal(t, claim.UID, current.UID)
	require.True(t, current.DeletionTimestamp.IsZero())
	require.Equal(t, claim.Finalizers, current.Finalizers)
}

func TestCleanupDeleteWaitsForExistingTerminationAfterConflict(t *testing.T) {
	claim := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "claim-uid", Labels: map[string]string{"test": "e2e"}, Finalizers: []string{karpv1.TerminationFinalizer}}}
	deletes := 0
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(claim).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.DeleteOption) error {
			deletes++
			require.NoError(t, c.Delete(ctx, object, options...))
			return apierrors.NewConflict(schema.GroupResource{Group: "karpenter.sh", Resource: "nodeclaims"}, object.GetName(), fmt.Errorf("another deleter already started termination"))
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.NoError(t, suite.deleteCleanupObject(t.Context(), claim, cleanupScope{}))
	require.Equal(t, 1, deletes)
	current := &karpv1.NodeClaim{}
	require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(claim), current))
	require.False(t, current.DeletionTimestamp.IsZero())
	require.Equal(t, claim.Finalizers, current.Finalizers)
}

func TestCleanupListFailurePreservesResources(t *testing.T) {
	class := cleanupClass("class", "current")
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(class).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return fmt.Errorf("inventory unavailable")
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.Error(t, suite.cleanupSelectedResources(context.Background(), "current"))
	current := &v1alpha1.IBMNodeClass{}
	require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(class), current))
	require.True(t, current.DeletionTimestamp.IsZero())
}
