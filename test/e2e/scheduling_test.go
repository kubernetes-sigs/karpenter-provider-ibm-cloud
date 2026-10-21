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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// TestE2EConsolidationWithPDB tests node consolidation behavior with Pod Disruption Budgets
func TestE2EConsolidationWithPDB(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("consolidation-pdb-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	t.Logf("Starting consolidation with PDB test: %s", testName)
	ctx := context.Background()

	// Create NodeClass and NodePool
	nodeClass := suite.createTestNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
	require.NoError(t, suite.mutateOwnedTestObject(ctx, nodePool, testName, func(object client.Object) error {
		object.(*karpv1.NodePool).Spec.Disruption.ConsolidateAfter = karpv1.MustParseNillableDuration("10s")
		return nil
	}))

	// Create deployment with multiple replicas to spread across nodes
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-deployment", testName),
			Namespace: "default",
			Labels: map[string]string{
				"app":        fmt.Sprintf("%s-app", testName),
				"test":       "e2e",
				"test-name":  testName,
				"created-by": "karpenter-e2e",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{4}[0], // More replicas to force multiple nodes
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": fmt.Sprintf("%s-app", testName),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":       fmt.Sprintf("%s-app", testName),
						"test":      "e2e",
						"test-name": testName,
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"karpenter.sh/nodepool": nodePool.Name,
					},
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "quay.io/nginx/nginx-unprivileged:1.29.1-alpine",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
					// Use anti-affinity to spread pods across nodes
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: corev1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"app": fmt.Sprintf("%s-app", testName),
											},
										},
										TopologyKey: "kubernetes.io/hostname",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err := suite.kubeClient.Create(ctx, deployment)
	require.NoError(t, err)

	// Wait for pods to be scheduled
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Get initial node count
	initialNodes := suite.getKarpenterNodes(t, nodePool.Name)
	require.Greater(t, len(initialNodes), 1, "Should have multiple nodes for consolidation test")
	t.Logf("Initial nodes: %d", len(initialNodes))

	// Create PodDisruptionBudget to limit disruptions
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pdb", testName),
			Namespace: "default",
			Labels: map[string]string{
				"test":       "e2e",
				"test-name":  testName,
				"created-by": "karpenter-e2e",
			},
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &intstr.IntOrString{
				Type:   intstr.Int,
				IntVal: 2, // Keep at least 2 pods available
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": fmt.Sprintf("%s-app", testName),
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, pdb)
	require.NoError(t, err)
	t.Logf("Created PodDisruptionBudget: %s", pdb.Name)

	// Scale down deployment to trigger potential consolidation
	require.NoError(t, suite.scaleTestDeployment(ctx, deployment, 2))
	t.Logf("Scaled deployment down to 2 replicas")

	// Wait for scaling to complete by checking deployment status
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	require.NoError(t, wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var currentPDB policyv1.PodDisruptionBudget
		if err := suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(pdb), &currentPDB); err != nil {
			return false, err
		}
		if currentPDB.Status.ObservedGeneration < currentPDB.Generation {
			return false, nil
		}
		if currentPDB.Status.CurrentHealthy < 2 {
			return false, fmt.Errorf("consolidation reduced healthy replicas below the disruption budget: %d", currentPDB.Status.CurrentHealthy)
		}
		var nodes corev1.NodeList
		if err := suite.kubeClient.List(ctx, &nodes, client.MatchingLabels{karpv1.NodePoolLabelKey: nodePool.Name}); err != nil {
			return false, err
		}
		return len(nodes.Items) < len(initialNodes), nil
	}), "An empty owned node must consolidate while both protected replicas stay healthy")

	// Verify pods are still running
	suite.verifyPodsScheduledOnCorrectNodes(t, deployment.Name, "default", nodePool.Name)

	t.Logf("Consolidation with PDB test completed: %s", testName)
}

// TestE2EPodDisruptionBudget tests PodDisruptionBudget behavior during node operations
func TestE2EPodDisruptionBudget(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("pdb-test-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	ctx := context.Background()
	class := suite.createTestNodeClass(t, testName)
	suite.waitForNodeClassReady(t, class.Name)
	pool := suite.createTestNodePool(t, testName, class.Name)
	deployment := suite.createTestWorkload(t, testName)
	suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
	var pods corev1.PodList
	require.NoError(t, suite.kubeClient.List(ctx, &pods, client.InNamespace(deployment.Namespace), client.MatchingLabels(deployment.Spec.Selector.MatchLabels)))
	require.NotEmpty(t, pods.Items)
	victim := pods.Items[0]
	zero := intstr.FromInt32(0)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: testName + "-pdb", Namespace: deployment.Namespace,
			Labels: map[string]string{"test": "e2e", "test-name": testName, "created-by": "karpenter-e2e"}},
		Spec: policyv1.PodDisruptionBudgetSpec{MaxUnavailable: &zero, Selector: deployment.Spec.Selector.DeepCopy()},
	}
	require.NoError(t, suite.kubeClient.Create(ctx, pdb))
	suite.waitForPDBReady(t, pdb.Name, pdb.Namespace, time.Minute)
	var currentPDB policyv1.PodDisruptionBudget
	require.NoError(t, suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(pdb), &currentPDB))
	require.Greater(t, currentPDB.Status.ExpectedPods, int32(0))
	require.Equal(t, currentPDB.Status.ExpectedPods, currentPDB.Status.CurrentHealthy)
	require.Zero(t, currentPDB.Status.DisruptionsAllowed)
	err := suite.coreClient.Pods(victim.Namespace).EvictV1(ctx, &policyv1.Eviction{
		ObjectMeta:    metav1.ObjectMeta{Name: victim.Name, Namespace: victim.Namespace},
		DeleteOptions: &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &victim.UID}},
	})
	require.True(t, apierrors.IsTooManyRequests(err), "PDB must reject protected eviction; got %v", err)
	var claims karpv1.NodeClaimList
	require.NoError(t, suite.kubeClient.List(ctx, &claims, client.MatchingLabels{karpv1.NodePoolLabelKey: pool.Name}))
	var retiring *karpv1.NodeClaim
	for i := range claims.Items {
		if claims.Items[i].Status.NodeName == victim.Spec.NodeName {
			retiring = &claims.Items[i]
		}
	}
	require.NotNil(t, retiring, "Victim must be on a claim owned by this test")
	scope, err := suite.newCleanupScope(ctx, testName)
	require.NoError(t, err)
	require.NoError(t, suite.deleteCleanupObject(ctx, retiring, scope))
	for until := time.Now().Add(20 * time.Second); time.Now().Before(until); {
		var protected corev1.Pod
		require.NoError(t, suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(&victim), &protected))
		require.Equal(t, victim.UID, protected.UID)
		require.True(t, protected.DeletionTimestamp.IsZero(), "Claim drain must preserve the protected Pod")
		time.Sleep(pollInterval)
	}
	require.NoError(t, suite.mutateOwnedTestObject(ctx, pdb, testName, func(object client.Object) error {
		allowed := intstr.FromInt32(1)
		object.(*policyv1.PodDisruptionBudget).Spec.MaxUnavailable = &allowed
		return nil
	}))
	suite.waitForPDBReady(t, pdb.Name, pdb.Namespace, time.Minute)
	require.NoError(t, wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var current corev1.Pod
		err := suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(&victim), &current)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return current.UID != victim.UID, nil
	}), "Protected Pod should be evicted after budget relaxation")
	suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
	suite.waitForNodeClaimCleanedUp(t, retiring.Name, testTimeout)
}

// TestE2EPodAntiAffinity tests pod anti-affinity scheduling behavior
func TestE2EPodAntiAffinity(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("anti-affinity-%d", time.Now().Unix())
	t.Logf("Starting pod anti-affinity test: %s", testName)
	ctx := context.Background()

	// Create infrastructure
	nodeClass := suite.createTestNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)

	// Create deployment with strict anti-affinity
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-deployment", testName),
			Namespace: "default",
			Labels: map[string]string{
				"app":       fmt.Sprintf("%s-app", testName),
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{3}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": fmt.Sprintf("%s-app", testName),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":       fmt.Sprintf("%s-app", testName),
						"test":      "e2e",
						"test-name": testName,
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"karpenter.sh/nodepool": nodePool.Name,
					},
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "quay.io/nginx/nginx-unprivileged:1.29.1-alpine",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
					// Strict anti-affinity - no two pods on the same node
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"app": fmt.Sprintf("%s-app", testName),
										},
									},
									TopologyKey: "kubernetes.io/hostname",
								},
							},
						},
					},
				},
			},
		},
	}

	err := suite.kubeClient.Create(ctx, deployment)
	require.NoError(t, err)
	t.Logf("Created deployment with strict anti-affinity: %s", deployment.Name)

	// Wait for pods to be scheduled
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Verify that pods are on different nodes
	var podList corev1.PodList
	err = suite.kubeClient.List(ctx, &podList, client.InNamespace("default"),
		client.MatchingLabels{"app": fmt.Sprintf("%s-app", testName)})
	require.NoError(t, err)
	require.Equal(t, 3, len(podList.Items), "Should have 3 pods")

	// Check that each pod is on a different node
	nodeNames := make(map[string]bool)
	for _, pod := range podList.Items {
		require.NotEmpty(t, pod.Spec.NodeName, "Pod should be scheduled on a node")

		if nodeNames[pod.Spec.NodeName] {
			t.Errorf("Multiple pods scheduled on the same node: %s", pod.Spec.NodeName)
		}
		nodeNames[pod.Spec.NodeName] = true
	}

	require.Equal(t, 3, len(nodeNames), "Pods should be spread across 3 different nodes due to anti-affinity")
	t.Logf("Verified pods are spread across %d different nodes", len(nodeNames))

	// List the nodes they're running on
	for nodeName := range nodeNames {
		t.Logf("Pod scheduled on node: %s", nodeName)
	}

	// Cleanup
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.cleanupTestResources(t, testName)
	t.Logf("Pod anti-affinity test completed: %s", testName)
}

// TestE2ENodeAffinity tests node affinity scheduling behavior
func TestE2ENodeAffinity(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("node-affinity-%d", time.Now().Unix())
	t.Logf("Starting node affinity test: %s", testName)
	ctx := context.Background()

	// Create infrastructure
	nodeClass := suite.createTestNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)

	// Wait for initial node to be created and get its instance type
	initialDeployment := suite.createTestWorkload(t, testName)
	suite.waitForPodsToBeScheduled(t, initialDeployment.Name, "default")

	// Get the instance type of the first node
	nodes := suite.getKarpenterNodes(t, nodePool.Name)
	require.Greater(t, len(nodes), 0, "Should have at least one node")
	firstNodeInstanceType := nodes[0].Labels["node.kubernetes.io/instance-type"]
	require.NotEmpty(t, firstNodeInstanceType, "Node should have instance type label")
	t.Logf("First node instance type: %s", firstNodeInstanceType)

	// Don't clean up initial deployment yet - we need the nodes to remain for affinity test

	// Create deployment with node affinity targeting the same instance type
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-deployment", testName),
			Namespace: "default",
			Labels: map[string]string{
				"app":       fmt.Sprintf("%s-app", testName),
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{2}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": fmt.Sprintf("%s-app", testName),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":       fmt.Sprintf("%s-app", testName),
						"test":      "e2e",
						"test-name": testName,
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"karpenter.sh/nodepool": nodePool.Name,
					},
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "quay.io/nginx/nginx-unprivileged:1.29.1-alpine",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
					// Node affinity requiring specific instance type
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "node.kubernetes.io/instance-type",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{firstNodeInstanceType},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err := suite.kubeClient.Create(ctx, deployment)
	require.NoError(t, err)
	t.Logf("Created deployment with node affinity for instance type: %s", firstNodeInstanceType)

	// Wait for pods to be scheduled
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Verify that pods are scheduled on nodes with the correct instance type
	var podList corev1.PodList
	err = suite.kubeClient.List(ctx, &podList, client.InNamespace("default"),
		client.MatchingLabels{"app": fmt.Sprintf("%s-app", testName)})
	require.NoError(t, err)

	for _, pod := range podList.Items {
		require.NotEmpty(t, pod.Spec.NodeName, "Pod should be scheduled on a node")

		// Get the node and check its instance type
		var node corev1.Node
		err := suite.kubeClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, &node)
		require.NoError(t, err)

		nodeInstanceType := node.Labels["node.kubernetes.io/instance-type"]
		require.Equal(t, firstNodeInstanceType, nodeInstanceType,
			"Pod should be scheduled on node with required instance type")

		t.Logf("Pod %s correctly scheduled on node %s with instance type %s",
			pod.Name, pod.Spec.NodeName, nodeInstanceType)
	}

	// Cleanup both deployments
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.cleanupTestWorkload(t, initialDeployment.Name, "default")
	suite.cleanupTestResources(t, testName)
	t.Logf("Node affinity test completed: %s", testName)
}
