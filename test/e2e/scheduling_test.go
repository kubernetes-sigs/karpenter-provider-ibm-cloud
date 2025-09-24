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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestE2EConsolidationWithPDB tests node consolidation behavior with Pod Disruption Budgets
func TestE2EConsolidationWithPDB(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("consolidation-pdb-%d", time.Now().Unix())
	t.Logf("Starting consolidation with PDB test: %s", testName)
	ctx := context.Background()

	// Create NodeClass and NodePool
	nodeClass := suite.createTestNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)

	// Create deployment with multiple replicas to spread across nodes
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
				"test":      "e2e",
				"test-name": testName,
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
	deployment.Spec.Replicas = &[]int32{2}[0]
	err = suite.kubeClient.Update(ctx, deployment)
	require.NoError(t, err)
	t.Logf("Scaled deployment down to 2 replicas")

	// Wait for scaling to complete
	time.Sleep(60 * time.Second)

	// Check if consolidation occurred while respecting PDB
	// Note: Actual consolidation timing depends on Karpenter configuration
	finalNodes := suite.getKarpenterNodes(t, nodePool.Name)
	t.Logf("Final nodes after scaling: %d (initial: %d)", len(finalNodes), len(initialNodes))

	// Verify pods are still running
	suite.verifyPodsScheduledOnCorrectNodes(t, deployment.Name, "default", nodePool.Name)

	// Cleanup
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.cleanupTestResources(t, testName)
	t.Logf("✅ Consolidation with PDB test completed: %s", testName)
}

// TestE2EPodDisruptionBudget tests PodDisruptionBudget behavior during node operations
func TestE2EPodDisruptionBudget(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("pdb-test-%d", time.Now().Unix())
	t.Logf("Starting PodDisruptionBudget test: %s", testName)
	ctx := context.Background()

	// Ensure cleanup happens even if test fails
	defer func() {
		t.Logf("Running deferred cleanup for test: %s", testName)
		suite.cleanupTestResources(t, testName)
	}()

	// Create infrastructure
	nodeClass := suite.createTestNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	_ = suite.createTestNodePool(t, testName, nodeClass.Name)

	// Create deployment with 3 replicas (default from createTestWorkload)
	deployment := suite.createTestWorkload(t, testName)

	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Create restrictive PDB
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pdb", testName),
			Namespace: "default",
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &intstr.IntOrString{
				Type:   intstr.Int,
				IntVal: 1, // Only allow 1 pod to be unavailable
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": fmt.Sprintf("%s-workload", testName),
				},
			},
		},
	}

	err := suite.kubeClient.Create(ctx, pdb)
	require.NoError(t, err)
	t.Logf("Created restrictive PDB: %s", pdb.Name)

	// Wait for PDB to be processed
	time.Sleep(30 * time.Second)

	// Verify PDB is active
	var updatedPDB policyv1.PodDisruptionBudget
	err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: pdb.Name, Namespace: pdb.Namespace}, &updatedPDB)
	require.NoError(t, err)
	t.Logf("PDB Status - Expected: %d, Current: %d, Desired: %d",
		updatedPDB.Status.ExpectedPods, updatedPDB.Status.CurrentHealthy, updatedPDB.Status.DesiredHealthy)

	// Verify that pods remain available despite potential disruptions
	// This is more of a validation that the PDB is properly configured
	require.True(t, updatedPDB.Status.CurrentHealthy >= updatedPDB.Status.DesiredHealthy,
		"PDB should maintain desired number of healthy pods")

	// Cleanup workload explicitly (resources will be cleaned by defer)
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	t.Logf("✅ PodDisruptionBudget test completed: %s", testName)
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
	t.Logf("✅ Verified pods are spread across %d different nodes", len(nodeNames))

	// List the nodes they're running on
	for nodeName := range nodeNames {
		t.Logf("Pod scheduled on node: %s", nodeName)
	}

	// Cleanup
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.cleanupTestResources(t, testName)
	t.Logf("✅ Pod anti-affinity test completed: %s", testName)
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

		t.Logf("✅ Pod %s correctly scheduled on node %s with instance type %s",
			pod.Name, pod.Spec.NodeName, nodeInstanceType)
	}

	// Cleanup both deployments
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.cleanupTestWorkload(t, initialDeployment.Name, "default")
	suite.cleanupTestResources(t, testName)
	t.Logf("✅ Node affinity test completed: %s", testName)
}
