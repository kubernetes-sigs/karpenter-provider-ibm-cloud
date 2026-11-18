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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
								LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": testName + "-app"}},
								TopologyKey:   corev1.LabelHostname,
							}},
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
	require.Greater(t, len(podZones), 1, "NodeClass requests Balanced placement; preferred zone affinity alone does not guarantee spread")
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
	require.Greater(t, len(podZones), 1, "NodeClass requests Balanced placement; preferred zone affinity alone does not guarantee spread")

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

	t.Run("InvalidPlacementStrategy", func(t *testing.T) {
		invalidName := testName + "-invalid"
		t.Cleanup(func() { suite.cleanupTestResources(t, invalidName) })
		invalidNodeClass := &v1alpha1.IBMNodeClass{
			ObjectMeta: metav1.ObjectMeta{Name: invalidName + "-nodeclass", Labels: map[string]string{"test": "e2e", "test-name": invalidName, "created-by": "karpenter-e2e"}},
			Spec: v1alpha1.IBMNodeClassSpec{
				Region: suite.testRegion, VPC: suite.testVPC, Image: suite.testImage,
				SecurityGroups: []string{suite.testSecurityGroup}, ResourceGroup: suite.testResourceGroup,
				APIServerEndpoint: suite.APIServerEndpoint, BootstrapMode: stringPtr("cloud-init"),
				PlacementStrategy: &v1alpha1.PlacementStrategy{ZoneBalance: "Unsupported"},
			},
		}
		err := suite.kubeClient.Create(ctx, invalidNodeClass)
		require.True(t, apierrors.IsInvalid(err), "Invalid zoneBalance must fail admission; got %v", err)
	})

	t.Logf("Placement strategy validation test completed: %s", testName)
}

// TestE2EZoneFailover tests zone failover behavior when one zone becomes unavailable
func TestE2EZoneFailover(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("zone-failover-%d", time.Now().Unix())
	t.Cleanup(func() { suite.cleanupTestResources(t, testName) })
	ctx := context.Background()
	class := suite.createMultiZoneNodeClass(t, testName)
	suite.waitForNodeClassReady(t, class.Name)
	pool := suite.createMultiZoneNodePool(t, testName, class.Name)
	deployment := suite.createZoneSpreadDeployment(t, testName, pool.Name, 4)
	suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
	initial := suite.getPodZoneDistribution(t, testName+"-app", deployment.Namespace)
	require.Greater(t, len(initial), 1, "Balanced placement should initially use multiple zones")
	used := make([]string, 0, len(initial))
	for zone := range initial {
		used = append(used, zone)
	}
	sort.Strings(used)
	excluded := used[0]
	var readyClass v1alpha1.IBMNodeClass
	require.NoError(t, suite.kubeClient.Get(ctx, client.ObjectKeyFromObject(class), &readyClass))
	remainingSet := map[string]bool{}
	cloud, err := suite.vpcClient()
	require.NoError(t, err)
	require.NotEmpty(t, readyClass.Status.SelectedSubnets)
	for _, subnetID := range readyClass.Status.SelectedSubnets {
		subnet, subnetErr := cloud.GetSubnet(ctx, subnetID)
		require.NoError(t, subnetErr)
		require.NotNil(t, subnet.Zone)
		require.NotNil(t, subnet.Zone.Name)
		zone := *subnet.Zone.Name
		if zone != excluded {
			remainingSet[zone] = true
		}
	}
	remaining := make([]string, 0, len(remainingSet))
	for zone := range remainingSet {
		remaining = append(remaining, zone)
	}
	sort.Strings(remaining)
	require.GreaterOrEqual(t, len(remaining), 2, "Failover with retained spread assertion requires two remaining eligible zones")
	var original karpv1.NodeClaimList
	require.NoError(t, suite.kubeClient.List(ctx, &original, client.MatchingLabels{karpv1.NodePoolLabelKey: pool.Name}))
	require.NotEmpty(t, original.Items)
	require.NoError(t, suite.mutateOwnedTestObject(ctx, pool, testName, func(object client.Object) error {
		current := object.(*karpv1.NodePool)
		for i := range current.Spec.Template.Spec.Requirements {
			if current.Spec.Template.Spec.Requirements[i].Key == corev1.LabelTopologyZone {
				current.Spec.Template.Spec.Requirements[i].Values = remaining
				return nil
			}
		}
		return fmt.Errorf("test pool has no zone requirement")
	}))
	require.NoError(t, suite.mutateOwnedTestObject(ctx, deployment, testName, func(object client.Object) error {
		current := object.(*appsv1.Deployment)
		if current.Spec.Template.Spec.Affinity == nil {
			current.Spec.Template.Spec.Affinity = &corev1.Affinity{}
		}
		current.Spec.Template.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpIn, Values: remaining}},
			}}},
		}
		return nil
	}))
	scope, err := suite.newCleanupScope(ctx, testName)
	require.NoError(t, err)
	retired := 0
	for i := range original.Items {
		claim := &original.Items[i]
		if claim.Labels[corev1.LabelTopologyZone] != excluded {
			continue
		}
		require.NoError(t, suite.deleteCleanupObject(ctx, claim, scope))
		suite.waitForNodeClaimCleanedUp(t, claim.Name, testTimeout)
		retired++
	}
	require.Greater(t, retired, 0, "Failover must retire an allocated claim in the excluded zone")
	require.NoError(t, suite.scaleTestDeployment(ctx, deployment, 8))
	suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
	final := suite.getPodZoneDistribution(t, testName+"-app", deployment.Namespace)
	require.Zero(t, final[excluded], "Evacuated zone must have no ready workload pods")
	require.Greater(t, len(final), 1, "Should maintain multi-zone distribution after failover and scaling")
	for zone := range final {
		require.True(t, remainingSet[zone], "Replacement must use a remaining allowed zone")
	}
	total := 0
	for _, count := range final {
		total += count
	}
	require.Equal(t, 8, total)
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
			InstanceProfile:   s.GetAvailableInstanceType(t),
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
							Key:      corev1.LabelInstanceTypeStable,
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{s.GetAvailableInstanceType(t)},
						},
						// Allow any zone in the region (multi-zone)
						{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{s.testRegion + "-1", s.testRegion + "-2", s.testRegion + "-3"},
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
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if pod.Spec.NodeName == "" || !pod.DeletionTimestamp.IsZero() || !ready {
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
