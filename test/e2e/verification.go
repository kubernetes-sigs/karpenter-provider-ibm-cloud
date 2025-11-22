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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// verifyPodsScheduledOnCorrectNodes verifies that pods are scheduled on nodes from the expected NodePool
func (s *E2ETestSuite) verifyPodsScheduledOnCorrectNodes(t *testing.T, deploymentName, namespace, expectedNodePool string) {
	ctx := context.Background()
	// Get all pods from the deployment
	var podList corev1.PodList
	err := s.kubeClient.List(ctx, &podList, client.InNamespace(namespace), client.MatchingLabels{"app": deploymentName})
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

		t.Logf("✅ Pod %s correctly scheduled on node %s (NodePool: %s)", pod.Name, pod.Spec.NodeName, nodePoolLabel)
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
	// This is a simplified verification - in a real implementation,
	// you would check the IBM Cloud VPC API for actual instances
	ctx := context.Background()
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err)

	karpenterNodes := 0
	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue != "" {
			karpenterNodes++
			t.Logf("Found Karpenter node in cluster: %s", node.Name)
			// In a real test, we would extract the provider ID and verify it exists in IBM Cloud
		}
	}

	require.Greater(t, karpenterNodes, 0, "At least one Karpenter node should exist")
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
		t.Logf("✅ Node %s correctly uses allowed instance type %s", node.Name, instanceType)
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
	t.Logf("✅ NodeClaim %s correctly uses allowed instance type %s", nodeClaimName, instanceType)
}

// isNodeClaimReady checks if a NodeClaim is in a ready state
func (s *E2ETestSuite) isNodeClaimReady(nodeClaim karpv1.NodeClaim) bool {
	// Check if the NodeClaim has a ProviderID (instance was created)
	if nodeClaim.Status.ProviderID == "" {
		return false
	}

	// Check conditions for readiness
	for _, condition := range nodeClaim.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == "True" {
			return true
		}
		if condition.Type == "Launched" && condition.Status == "True" {
			return true
		}
	}

	return false
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

	t.Logf("✅ Node stability monitoring completed for %v", duration)
}

// getIBMCloudInstances retrieves current IBM Cloud instances (simplified implementation)
func (s *E2ETestSuite) getIBMCloudInstances(t *testing.T) (map[string]string, error) {
	// This is a placeholder - in a real implementation, this would call IBM Cloud VPC API
	// to get actual instance information
	instances := make(map[string]string)

	// For now, we'll get the information from Kubernetes nodes
	ctx := context.Background()
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	if err != nil {
		return nil, err
	}

	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue != "" {
			// Extract instance ID from provider ID
			instanceID := extractInstanceIDFromProviderID(node.Spec.ProviderID)
			if instanceID != "" {
				instances[instanceID] = node.Name
			}
		}
	}

	return instances, nil
}
