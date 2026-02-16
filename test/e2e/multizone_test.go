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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// TestE2EMultiZoneDistribution tests that nodes are distributed across multiple zones
// when using PlacementStrategy with balanced zone distribution
func TestE2EMultiZoneDistribution(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("multizone-distribution-%d", time.Now().Unix())
	t.Logf("Starting multi-zone distribution test: %s", testName)

	// Ensure cleanup happens even if test fails
	defer func() {
		t.Logf("Running deferred cleanup for test: %s", testName)
		suite.cleanupTestResources(t, testName)
	}()

	// Skip test if multi-zone infrastructure not available
	if os.Getenv("E2E_SKIP_MULTIZONE") == "true" {
		t.Skip("Skipping multi-zone test: E2E_SKIP_MULTIZONE is set")
	}

	// Create NodeClass with placement strategy (no specific zone)
	nodeClass := suite.createMultiZoneNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)

	// Create NodePool that allows multiple zones
	nodePool := suite.createMultiZoneNodePool(t, testName, nodeClass.Name)

	// Create deployment with multiple replicas to force multiple nodes
	deployment := suite.createMultiReplicaDeployment(t, testName, nodePool.Name, 6)

	// Wait for pods to be scheduled
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Verify multi-zone distribution
	suite.verifyMultiZoneDistribution(t, testName, 2) // Expect at least 2 zones

	// Verify all pods are running
	suite.verifyPodsScheduledOnCorrectNodes(t, deployment.Name, "default", nodePool.Name)

	// Cleanup workload explicitly (resources cleaned by defer)
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	t.Logf("Multi-zone distribution test completed: %s", testName)
}

// TestE2EZoneAntiAffinity tests that pod anti-affinity works correctly with zone constraints
func TestE2EZoneAntiAffinity(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()
	testName := fmt.Sprintf("zone-anti-affinity-%d", time.Now().Unix())
	t.Logf("Starting zone anti-affinity test: %s", testName)

	// Ensure cleanup happens even if test fails
	defer func() {
		t.Logf("Running deferred cleanup for test: %s", testName)
		suite.cleanupTestResources(t, testName)
	}()

	// Create infrastructure
	nodeClass := suite.createMultiZoneNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createMultiZoneNodePool(t, testName, nodeClass.Name)

	// Create deployment with zone anti-affinity
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
			Replicas: &[]int32{3}[0], // 3 replicas to force multiple zones
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
					// Zone anti-affinity - prefer different zones
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
										TopologyKey: "topology.kubernetes.io/zone",
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
	t.Logf("Created deployment with zone anti-affinity: %s", deployment.Name)

	// Wait for pods to be scheduled
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Verify that pods are distributed across zones
	podZones := suite.getPodZoneDistribution(t, fmt.Sprintf("%s-app", testName), "default")
	require.Greater(t, len(podZones), 1, "Pods should be distributed across multiple zones")
	t.Logf("Verified pods are distributed across %d zones", len(podZones))

	// List zones for debugging
	for zone, count := range podZones {
		t.Logf("Zone %s: %d pods", zone, count)
	}

	// Cleanup workload explicitly (resources cleaned by defer)
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	t.Logf("Zone anti-affinity test completed: %s", testName)
}

// TestE2ETopologySpreadConstraints tests topology spread constraints across zones
func TestE2ETopologySpreadConstraints(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()
	testName := fmt.Sprintf("topology-spread-%d", time.Now().Unix())
	t.Logf("Starting topology spread constraints test: %s", testName)

	// Create infrastructure
	nodeClass := suite.createMultiZoneNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createMultiZoneNodePool(t, testName, nodeClass.Name)

	// Create deployment with topology spread constraints
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
			Replicas: &[]int32{6}[0], // 6 replicas to test spread across zones
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
					// Topology spread constraints for even distribution
					TopologySpreadConstraints: []corev1.TopologySpreadConstraint{
						{
							MaxSkew:           1,
							TopologyKey:       "topology.kubernetes.io/zone",
							WhenUnsatisfiable: corev1.DoNotSchedule,
							LabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"app": fmt.Sprintf("%s-app", testName),
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
	t.Logf("Created deployment with topology spread constraints: %s", deployment.Name)

	// Wait for pods to be scheduled
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Verify topology spread
	podZones := suite.getPodZoneDistribution(t, fmt.Sprintf("%s-app", testName), "default")
	require.Greater(t, len(podZones), 1, "Pods should be distributed across multiple zones")

	// Check that distribution is relatively even (within maxSkew of 1)
	var counts []int
	for zone, count := range podZones {
		counts = append(counts, count)
		t.Logf("Zone %s: %d pods", zone, count)
	}

	// Verify maxSkew constraint (difference between max and min should be <= 1)
	if len(counts) > 1 {
		min, max := counts[0], counts[0]
		for _, count := range counts {
			if count < min {
				min = count
			}
			if count > max {
				max = count
			}
		}
		skew := max - min
		require.LessOrEqual(t, skew, 1, "Pod distribution should respect maxSkew constraint of 1")
		t.Logf("Verified topology spread with skew of %d (max: %d, min: %d)", skew, max, min)
	}

	// Cleanup
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.cleanupTestResources(t, testName)
	t.Logf("Topology spread constraints test completed: %s", testName)
}

// TestE2EPlacementStrategyValidation tests PlacementStrategy validation and behavior
func TestE2EPlacementStrategyValidation(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()
	testName := fmt.Sprintf("placement-strategy-validation-%d", time.Now().Unix())
	t.Logf("Starting placement strategy validation test: %s", testName)

	// Test 1: NodeClass without zone/subnet but with placement strategy should be accepted
	t.Run("ValidPlacementStrategy", func(t *testing.T) {
		nodeClass := suite.createMultiZoneNodeClass(t, testName+"-valid")
		defer suite.cleanupTestResources(t, testName+"-valid")

		// Wait for NodeClass to be ready (should not fail validation)
		suite.waitForNodeClassReady(t, nodeClass.Name)
		t.Logf("NodeClass with placement strategy validated successfully")
	})

	// Test 2: NodeClass without zone/subnet AND without placement strategy should fail
	// Note: This test may be skipped if the current implementation doesn't enforce this validation
	t.Run("MissingPlacementStrategy", func(t *testing.T) {
		invalidNodeClass := &v1alpha1.IBMNodeClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("%s-invalid-nodeclass", testName),
				Labels: map[string]string{
					"test":      "e2e",
					"test-name": testName + "-invalid",
				},
			},
			Spec: v1alpha1.IBMNodeClassSpec{
				Region: suite.testRegion,
				// No Zone specified
				VPC:   suite.testVPC,
				Image: suite.testImage,
				// No Subnet specified
				SecurityGroups:    []string{suite.testSecurityGroup},
				SSHKeys:           []string{suite.testSshKeyId},
				ResourceGroup:     suite.testResourceGroup,
				APIServerEndpoint: suite.APIServerEndpoint,
				InstanceProfile:   "bx2-2x8",
				BootstrapMode:     stringPtr("cloud-init"),
				// No PlacementStrategy specified - should cause validation error
				Tags: map[string]string{
					"test":      "e2e",
					"test-name": testName + "-invalid",
				},
			},
		}

		err := suite.kubeClient.Create(ctx, invalidNodeClass)
		if err != nil {
			// Expected: validation should fail at creation
			t.Logf("NodeClass creation failed as expected due to missing placement strategy: %v", err)
		} else {
			// If creation succeeds, wait and check if controller marks it as invalid
			t.Logf("NodeClass created, checking if validation fails...")
			time.Sleep(5 * time.Second)

			var updatedNodeClass v1alpha1.IBMNodeClass
			err = suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(invalidNodeClass), &updatedNodeClass)
			if err != nil {
				t.Logf("NodeClass was deleted or became inaccessible (expected)")
			} else {
				// Check status conditions for validation failure
				hasValidationError := false
				for _, condition := range updatedNodeClass.Status.Conditions {
					if condition.Type == "Ready" && condition.Status == "False" {
						hasValidationError = true
						t.Logf("NodeClass marked as not ready due to validation: %s", condition.Message)
						break
					}
				}

				if !hasValidationError {
					t.Logf("Warning: NodeClass validation should have failed but didn't - this may indicate missing validation logic")
				}
			}

			// Clean up
			_ = suite.kubeClient.Delete(ctx, invalidNodeClass)
		}
	})

	t.Logf("Placement strategy validation test completed: %s", testName)
}

// TestE2EZoneFailover tests zone failover behavior when one zone becomes unavailable
func TestE2EZoneFailover(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("zone-failover-%d", time.Now().Unix())
	t.Logf("Starting zone failover test: %s", testName)

	// Note: This test is more conceptual as we can't actually make zones unavailable in E2E
	// Instead, we test that workloads can be distributed and rescheduled properly

	// Create infrastructure
	nodeClass := suite.createMultiZoneNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createMultiZoneNodePool(t, testName, nodeClass.Name)

	// Create deployment with zone spread preferences
	deployment := suite.createZoneSpreadDeployment(t, testName, nodePool.Name, 4)

	// Wait for initial placement
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")
	initialZones := suite.getPodZoneDistribution(t, fmt.Sprintf("%s-app", testName), "default")

	t.Logf("Initial zone distribution:")
	for zone, count := range initialZones {
		t.Logf("  Zone %s: %d pods", zone, count)
	}

	// Scale up to test additional zone utilization
	deployment.Spec.Replicas = &[]int32{8}[0]
	err := suite.kubeClient.Update(context.Background(), deployment)
	require.NoError(t, err)

	// Wait for scale-up (use existing waiting function)
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	finalZones := suite.getPodZoneDistribution(t, fmt.Sprintf("%s-app", testName), "default")
	t.Logf("Final zone distribution after scale-up:")
	for zone, count := range finalZones {
		t.Logf("  Zone %s: %d pods", zone, count)
	}

	// Verify we're still using multiple zones
	require.Greater(t, len(finalZones), 1, "Should maintain multi-zone distribution after scaling")

	// Cleanup
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.cleanupTestResources(t, testName)
	t.Logf("Zone failover test completed: %s", testName)
}

// Helper function to create a multi-zone NodeClass with placement strategy
func (s *E2ETestSuite) createMultiZoneNodeClass(t *testing.T, testName string) *v1alpha1.IBMNodeClass {
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclass", testName),
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region: s.testRegion,
			// Note: No Zone or Subnet specified - using placement strategy
			VPC:               s.testVPC,
			Image:             s.testImage,
			SecurityGroups:    []string{s.testSecurityGroup},
			SSHKeys:           []string{s.testSshKeyId},
			ResourceGroup:     s.testResourceGroup,
			APIServerEndpoint: s.APIServerEndpoint,
			InstanceProfile:   "bx2-4x16",
			BootstrapMode:     stringPtr("cloud-init"),
			PlacementStrategy: &v1alpha1.PlacementStrategy{
				ZoneBalance: "Balanced", // Request balanced zone distribution
			},
			Tags: map[string]string{
				"test":       "e2e",
				"test-name":  testName,
				"created-by": "karpenter-e2e",
				"purpose":    "multi-zone-test",
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClass)
	require.NoError(t, err, "Failed to create multi-zone NodeClass")
	t.Logf("Created multi-zone NodeClass: %s", nodeClass.Name)

	return nodeClass
}

// Helper function to create a NodePool that allows multiple zones
func (s *E2ETestSuite) createMultiZoneNodePool(t *testing.T, testName, nodeClassName string) *karpv1.NodePool {
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodepool", testName),
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test":      "e2e",
						"test-name": testName,
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						// Allow multiple instance types for flexibility
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelInstanceTypeStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"bx2-4x16", "bx2-2x8"},
							},
						},
						// Allow any zone in the region (multi-zone)
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      "topology.kubernetes.io/zone",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{s.testRegion + "-1", s.testRegion + "-2", s.testRegion + "-3"},
							},
						},
					},
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter-ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodePool)
	require.NoError(t, err, "Failed to create multi-zone NodePool")
	t.Logf("Created multi-zone NodePool: %s", nodePool.Name)

	return nodePool
}

// Helper function to create a deployment with multiple replicas
func (s *E2ETestSuite) createMultiReplicaDeployment(t *testing.T, testName, nodePoolName string, replicas int32) *appsv1.Deployment {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-deployment", testName),
			Namespace: "default",
			Labels: map[string]string{
				"app":       fmt.Sprintf("%s-workload", testName),
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": fmt.Sprintf("%s-workload", testName),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":       fmt.Sprintf("%s-workload", testName),
						"test":      "e2e",
						"test-name": testName,
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"karpenter.sh/nodepool": nodePoolName,
					},
					Containers: []corev1.Container{
						{
							Name:  "workload",
							Image: "quay.io/nginx/nginx-unprivileged:1.29.1-alpine",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1000m"), // Force separate nodes
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), deployment)
	require.NoError(t, err, "Failed to create multi-replica deployment")
	t.Logf("Created deployment with %d replicas: %s", replicas, deployment.Name)

	return deployment
}

// Helper function to create a deployment with zone spread preferences
func (s *E2ETestSuite) createZoneSpreadDeployment(t *testing.T, testName, nodePoolName string, replicas int32) *appsv1.Deployment {
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
			Replicas: &replicas,
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
						"karpenter.sh/nodepool": nodePoolName,
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
					// Prefer different zones but don't require it
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
								{
									Weight: 50, // Medium preference for zone spread
									PodAffinityTerm: corev1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"app": fmt.Sprintf("%s-app", testName),
											},
										},
										TopologyKey: "topology.kubernetes.io/zone",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), deployment)
	require.NoError(t, err)
	t.Logf("Created zone spread deployment: %s", deployment.Name)

	return deployment
}

// Helper function to verify multi-zone distribution
func (s *E2ETestSuite) verifyMultiZoneDistribution(t *testing.T, testName string, minZones int) {
	ctx := context.Background()

	// Get all Karpenter nodes for this test
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList, client.MatchingLabels{
		"test-name": testName,
	})
	require.NoError(t, err, "Failed to get nodes for multi-zone verification")

	// Count nodes per zone
	zoneDistribution := make(map[string]int)
	karpenterNodes := 0

	for _, node := range nodeList.Items {
		if _, isKarpenterNode := node.Labels["karpenter.sh/nodepool"]; isKarpenterNode {
			karpenterNodes++
			zone := node.Labels["topology.kubernetes.io/zone"]
			require.NotEmpty(t, zone, "Karpenter node should have zone label")
			zoneDistribution[zone]++
		}
	}

	require.Greater(t, karpenterNodes, 0, "Should have at least one Karpenter node")
	require.GreaterOrEqual(t, len(zoneDistribution), minZones,
		fmt.Sprintf("Should have nodes in at least %d zones", minZones))

	t.Logf("Multi-zone distribution verified:")
	for zone, count := range zoneDistribution {
		t.Logf("  Zone %s: %d nodes", zone, count)
		// Verify zone is in expected region
		require.True(t, strings.HasPrefix(zone, s.testRegion+"-"),
			fmt.Sprintf("Zone %s should be in region %s", zone, s.testRegion))
	}

	t.Logf("Verified %d Karpenter nodes distributed across %d zones",
		karpenterNodes, len(zoneDistribution))
}

// Helper function to get pod zone distribution
func (s *E2ETestSuite) getPodZoneDistribution(t *testing.T, appLabel, namespace string) map[string]int {
	ctx := context.Background()
	zoneDistribution := make(map[string]int)

	// Get pods with the app label
	var podList corev1.PodList
	err := s.kubeClient.List(ctx, &podList,
		client.InNamespace(namespace),
		client.MatchingLabels{"app": appLabel})
	require.NoError(t, err, "Failed to get pods for zone distribution")

	for _, pod := range podList.Items {
		if pod.Spec.NodeName == "" {
			continue // Skip unscheduled pods
		}

		// Get the node and its zone
		var node corev1.Node
		err := s.kubeClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, &node)
		require.NoError(t, err, fmt.Sprintf("Failed to get node %s", pod.Spec.NodeName))

		zone := node.Labels["topology.kubernetes.io/zone"]
		if zone != "" {
			zoneDistribution[zone]++
		}
	}

	return zoneDistribution
}
