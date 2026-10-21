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
	"strings"
	"testing"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func (s *E2ETestSuite) verifyOwnedTestInstance(t *testing.T, instance *vpcv1.Instance, claim *karpv1.NodeClaim, class *v1alpha1.IBMNodeClass) {
	t.Helper()
	ctx := t.Context()
	clusterUID, err := ownership.ClusterUID(ctx, s.kubeClient)
	require.NoError(t, err)
	require.NotNil(t, instance)
	require.NotNil(t, instance.ID)
	require.Equal(t, vpcInstanceID(claim.Status.ProviderID), *instance.ID)
	require.NotNil(t, instance.Name)
	require.Equal(t, ownership.InstanceName(clusterUID, string(claim.UID)), *instance.Name)
	require.NotNil(t, instance.CRN)
	vpc, err := s.vpcClient()
	require.NoError(t, err)
	tags, err := vpc.GetInstanceUserTags(ctx, *instance.CRN)
	require.NoError(t, err, "Independently read actual instance ownership")
	for key, expected := range ownership.VPCTags(clusterUID, string(claim.UID), string(class.UID)) {
		require.Equal(t, expected, tags[key], "Actual VM must belong to the test claim, class and cluster")
	}
	var fresh karpv1.NodeClaim
	require.NoError(t, s.kubeClient.Get(ctx, client.ObjectKeyFromObject(claim), &fresh))
	require.Equal(t, claim.UID, fresh.UID)
	require.Equal(t, claim.Status.ProviderID, fresh.Status.ProviderID)
	require.True(t, fresh.DeletionTimestamp.IsZero())
}

// verifyPodsScheduledOnCorrectNodes verifies that pods are scheduled on nodes from the expected NodePool
func (s *E2ETestSuite) verifyPodsScheduledOnCorrectNodes(t *testing.T, deploymentName, namespace, expectedNodePool string) {
	ctx := context.Background()
	var deployment appsv1.Deployment
	err := s.kubeClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, &deployment)
	require.NoError(t, err, "Should be able to get deployment")
	require.NotNil(t, deployment.Spec.Selector, "Deployment should have a pod selector")
	selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
	require.NoError(t, err, "Deployment should have a valid pod selector")

	var podList corev1.PodList
	err = s.kubeClient.List(ctx, &podList, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector})
	require.NoError(t, err, "Should be able to list pods")
	require.Greater(t, len(podList.Items), 0, "Should have at least one pod")

	// Check each pod is scheduled on a node with the correct NodePool label
	for _, pod := range podList.Items {
		require.NotEmpty(t, pod.Spec.NodeName, "Pod %s should be scheduled on a node", pod.Name)

		// Get the node
		var node corev1.Node
		err := s.kubeClient.Get(ctx, types.NamespacedName{Name: pod.Spec.NodeName}, &node)
		require.NoError(t, err, "Should be able to get node %s", pod.Spec.NodeName)

		// Verify node has the expected NodePool label
		nodePoolLabel, exists := node.Labels["karpenter.sh/nodepool"]
		require.True(t, exists, "Node %s should have karpenter.sh/nodepool label", node.Name)
		require.Equal(t, expectedNodePool, nodePoolLabel, "Pod %s should be on node from NodePool %s, but is on %s", pod.Name, expectedNodePool, nodePoolLabel)

		t.Logf("Pod %s correctly scheduled on node %s (NodePool: %s)", pod.Name, pod.Spec.NodeName, nodePoolLabel)
	}
}

// verifyKarpenterNodesExist verifies that Karpenter-managed nodes exist in the cluster
func (s *E2ETestSuite) verifyKarpenterNodesExist(t *testing.T) {
	ctx := context.Background()
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err)

	karpenterNodes := 0
	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue != "" {
			karpenterNodes++
			t.Logf("Found Karpenter node: %s (nodepool: %s)", node.Name, labelValue)
		}
	}

	require.Greater(t, karpenterNodes, 0, "At least one Karpenter node should exist")
	t.Logf("Found %d Karpenter nodes", karpenterNodes)
}

// verifyInstancesInIBMCloud verifies that instances exist and are managed by Karpenter
func (s *E2ETestSuite) verifyInstancesInIBMCloud(t *testing.T) {
	t.Helper()
	instances, err := s.getIBMCloudInstances(t)
	require.NoError(t, err, "list actual IBM Cloud instances")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var nodeList corev1.NodeList
	err = s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err)
	require.NoError(t, verifyManagedNodeInstances(nodeList.Items, s.testRegion, instances))
}

func verifyManagedNodeInstances(nodes []corev1.Node, region string, instances map[string]string) error {
	managed := 0
	prefix := "ibm:///" + region + "/"
	for _, node := range nodes {
		if node.Labels[karpv1.NodePoolLabelKey] == "" {
			continue
		}
		managed++
		if !strings.HasPrefix(node.Spec.ProviderID, prefix) {
			return fmt.Errorf("managed Node %s has an unexpected provider ID %q", node.Name, node.Spec.ProviderID)
		}
		instanceID := strings.TrimPrefix(node.Spec.ProviderID, prefix)
		if instanceID == "" || strings.Contains(instanceID, "/") {
			return fmt.Errorf("managed Node %s has an invalid instance ID", node.Name)
		}
		if _, exists := instances[instanceID]; !exists {
			return fmt.Errorf("managed Node %s instance %s is absent from the test VPC inventory", node.Name, instanceID)
		}
	}
	if managed == 0 {
		return fmt.Errorf("no Karpenter-managed Nodes exist")
	}
	return nil
}

// verifyInstancesUseAllowedTypes verifies that all Karpenter nodes use instance types from the allowed list
func (s *E2ETestSuite) verifyInstancesUseAllowedTypes(t *testing.T, allowedTypes []string) {
	ctx := context.Background()
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err)

	karpenterNodes := []corev1.Node{}
	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue != "" {
			karpenterNodes = append(karpenterNodes, node)
		}
	}

	require.Greater(t, len(karpenterNodes), 0, "At least one Karpenter node should exist")

	for _, node := range karpenterNodes {
		instanceType := node.Labels["node.kubernetes.io/instance-type"]
		require.NotEmpty(t, instanceType, "Node should have instance type label")

		found := false
		for _, allowedType := range allowedTypes {
			if instanceType == allowedType {
				found = true
				break
			}
		}

		require.True(t, found, "Node %s uses instance type %s, which is not in allowed types %v", node.Name, instanceType, allowedTypes)
		t.Logf("Node %s correctly uses allowed instance type %s", node.Name, instanceType)
	}
}

// verifyNodePoolRequirementsOnNodes verifies that all nodes from a NodePool satisfy its requirements
func (s *E2ETestSuite) verifyNodePoolRequirementsOnNodes(t *testing.T, nodePool *karpv1.NodePool) {
	ctx := context.Background()
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err)

	var nodePoolNodes []corev1.Node
	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue == nodePool.Name {
			nodePoolNodes = append(nodePoolNodes, node)
		}
	}

	require.Greater(t, len(nodePoolNodes), 0, "Should have at least one node from the NodePool")

	// Verify each requirement from the NodePool is satisfied on all nodes
	for _, requirement := range nodePool.Spec.Template.Spec.Requirements {
		for _, node := range nodePoolNodes {
			nodeValue, exists := node.Labels[requirement.Key]
			require.True(t, exists, "Node %s should have label %s", node.Name, requirement.Key)

			// Check if the node value satisfies the requirement
			switch requirement.Operator {
			case corev1.NodeSelectorOpIn:
				found := false
				for _, value := range requirement.Values {
					if nodeValue == value {
						found = true
						break
					}
				}
				require.True(t, found, "Node %s label %s=%s should be in %v", node.Name, requirement.Key, nodeValue, requirement.Values)
			case corev1.NodeSelectorOpNotIn:
				for _, value := range requirement.Values {
					require.NotEqual(t, nodeValue, value, "Node %s label %s=%s should not be in %v", node.Name, requirement.Key, nodeValue, requirement.Values)
				}
			}
		}
	}
}

// verifyInstanceUsesAllowedType verifies that a specific NodeClaim uses an instance type from the allowed list
func (s *E2ETestSuite) verifyInstanceUsesAllowedType(t *testing.T, nodeClaimName string, allowedTypes []string) {
	ctx := context.Background()
	// Get the NodeClaim to find the instance type that was selected
	var nodeClaim karpv1.NodeClaim
	err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
	require.NoError(t, err)

	// Check the instance type label set by Karpenter
	instanceType, ok := nodeClaim.Labels[corev1.LabelInstanceTypeStable]
	require.True(t, ok, "NodeClaim should have instance-type label")

	// Verify it's one of the allowed types
	found := false
	for _, allowedType := range allowedTypes {
		if instanceType == allowedType {
			found = true
			break
		}
	}

	require.True(t, found, "NodeClaim %s uses instance type %s, which is not in allowed types %v", nodeClaimName, instanceType, allowedTypes)
	t.Logf("NodeClaim %s correctly uses allowed instance type %s", nodeClaimName, instanceType)
}

// isNodeClaimReady checks if a NodeClaim is in a ready state
func (s *E2ETestSuite) isNodeClaimReady(nodeClaim karpv1.NodeClaim) bool {
	return nodeClaim.Status.ProviderID != "" && nodeClaim.DeletionTimestamp == nil && nodeClaim.StatusConditions().Get("Ready").IsTrue()
}

// getKarpenterNodes returns nodes managed by the given NodePool
func (s *E2ETestSuite) getKarpenterNodes(t *testing.T, nodePoolName string) []corev1.Node {
	var nodeList corev1.NodeList
	err := s.kubeClient.List(context.Background(), &nodeList, client.MatchingLabels{
		"karpenter.sh/nodepool": nodePoolName,
	})
	require.NoError(t, err)
	return nodeList.Items
}

// captureNodeSnapshot captures the current state of nodes for stability monitoring
func (s *E2ETestSuite) captureNodeSnapshot(t *testing.T, nodePoolName string) []NodeSnapshot {
	nodes := s.getKarpenterNodes(t, nodePoolName)
	snapshots := make([]NodeSnapshot, len(nodes))

	for i, node := range nodes {
		snapshots[i] = NodeSnapshot{
			Name:         node.Name,
			ProviderID:   node.Spec.ProviderID,
			InstanceType: node.Labels["node.kubernetes.io/instance-type"],
			CreationTime: node.CreationTimestamp.Time,
			Labels:       node.Labels,
		}
	}

	t.Logf("Captured snapshot of %d nodes from NodePool %s", len(snapshots), nodePoolName)
	return snapshots
}

// monitorNodeStability monitors nodes for stability over a duration
func (s *E2ETestSuite) monitorNodeStability(t *testing.T, nodePoolName string, initialNodes []NodeSnapshot, duration time.Duration) {
	endTime := time.Now().Add(duration)
	checkInterval := 30 * time.Second

	for time.Now().Before(endTime) {
		currentNodes := s.captureNodeSnapshot(t, nodePoolName)

		// Check if any nodes disappeared
		for _, initialNode := range initialNodes {
			found := false
			for _, currentNode := range currentNodes {
				if initialNode.Name == currentNode.Name {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Node %s disappeared during stability test", initialNode.Name)
			}
		}

		// Check if new nodes appeared (could indicate drift)
		if len(currentNodes) != len(initialNodes) {
			t.Logf("Node count changed: initial=%d, current=%d", len(initialNodes), len(currentNodes))
		}

		time.Sleep(checkInterval)
	}

	t.Logf("Node stability monitoring completed for %v", duration)
}

func (s *E2ETestSuite) getIBMCloudInstances(t *testing.T) (map[string]string, error) {
	t.Helper()
	return s.getIBMCloudInstancesWithContext(t.Context())
}

func (s *E2ETestSuite) getIBMCloudInstancesWithContext(parent context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	vpcClient, err := s.vpcClient()
	if err != nil {
		return nil, fmt.Errorf("creating VPC inventory client: %w", err)
	}
	return instancesInVPC(ctx, vpcClient, s.testVPC)
}

type instanceInventory interface {
	ListInstances(context.Context) ([]vpcv1.Instance, error)
}

func instancesInVPC(ctx context.Context, inventory instanceInventory, vpcID string) (map[string]string, error) {
	if vpcID == "" {
		return nil, fmt.Errorf("test VPC ID is required for inventory")
	}
	listed, err := inventory.ListInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing IBM Cloud instances: %w", err)
	}
	instances := make(map[string]string)
	for _, instance := range listed {
		if instance.VPC == nil || instance.VPC.ID == nil || *instance.VPC.ID != vpcID {
			continue
		}
		if instance.ID == nil || *instance.ID == "" {
			return nil, fmt.Errorf("instance in test VPC has no ID")
		}
		name := ""
		if instance.Name != nil {
			name = *instance.Name
		}
		instances[*instance.ID] = name
	}
	return instances, nil
}

// verifyDeploymentNotReady verifies that a deployment does not become ready within the timeout
// This is useful for testing negative cases like taint-based scheduling prevention
func (s *E2ETestSuite) verifyDeploymentNotReady(t *testing.T, deploymentName, namespace string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Check multiple times to ensure the deployment stays unscheduled
	checkCount := 0
	maxChecks := int(timeout / (5 * time.Second))
	if maxChecks < 3 {
		maxChecks = 3
	}

	for checkCount < maxChecks {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			checkCount++
			var deployment appsv1.Deployment
			err := s.kubeClient.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      deploymentName,
			}, &deployment)
			if err != nil {
				t.Logf("Check %d: Failed to get deployment: %v", checkCount, err)
				continue
			}

			// Verify deployment has 0 ready replicas
			if deployment.Status.ReadyReplicas > 0 {
				t.Errorf("Deployment %s unexpectedly has %d ready replicas - should have 0",
					deploymentName, deployment.Status.ReadyReplicas)
				return
			}
			t.Logf("Check %d: Deployment %s correctly has 0 ready replicas", checkCount, deploymentName)
		}
	}

	t.Logf("Verified deployment %s remained unscheduled for %v", deploymentName, timeout)
}
