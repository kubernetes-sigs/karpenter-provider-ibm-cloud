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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestE2EFullWorkflow tests the complete end-to-end workflow
func TestE2EFullWorkflow(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("e2e-test-%d", time.Now().Unix())
	t.Logf("Starting E2E test: %s", testName)

	// Step 1: Create NodeClass
	nodeClass := suite.createTestNodeClass(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Step 2: Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Step 3: Create NodePool
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Step 4: Deploy test workload to trigger Karpenter provisioning
	deployment := suite.createTestWorkload(t, testName)
	t.Logf("Created test workload: %s", deployment.Name)

	// Step 5: Wait for pods to be scheduled and nodes to be provisioned
	suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
	t.Logf("Pods scheduled successfully")

	// Step 6: Verify that new nodes were created by Karpenter
	suite.verifyKarpenterNodesExist(t)
	t.Logf("Verified Karpenter nodes exist")

	// Step 7: Verify instances exist in IBM Cloud
	suite.verifyInstancesInIBMCloud(t)
	t.Logf("Verified instances exist in IBM Cloud")

	// Step 8: Cleanup test resources
	suite.cleanupTestWorkload(t, deployment.Name, deployment.Namespace)
	t.Logf("Deleted test workload: %s", deployment.Name)

	// Step 9: Clean up remaining resources ONLY after all verification is complete
	t.Logf("Cleaning up remaining test resources")
	suite.cleanupTestResources(t, testName)
	t.Logf("E2E test completed successfully: %s", testName)
}

// TestE2ENodePoolInstanceTypeSelection tests the customer's configuration:
// - NodeClass WITHOUT instanceProfile field (should be optional)
// - NodePool with multiple instance type requirements
func TestE2ENodePoolInstanceTypeSelection(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("nodepool-instance-selection-%d", time.Now().Unix())
	t.Logf("Starting NodePool instance type selection test: %s", testName)

	// Use automatic cleanup wrapper - CRITICAL for preventing stale resources
	suite.WithAutoCleanup(t, testName, func() {
		// Create NodeClass without instanceProfile (let NodePool control selection)
		nodeClass := suite.createTestNodeClassWithoutInstanceProfile(t, testName)
		t.Logf("Created NodeClass without instanceProfile: %s", nodeClass.Name)

		// Wait for NodeClass to be ready
		suite.waitForNodeClassReady(t, nodeClass.Name)
		t.Logf("NodeClass is ready: %s", nodeClass.Name)

		// Create NodePool with multiple instance types matching customer's config
		nodePool := suite.createTestNodePoolWithMultipleInstanceTypes(t, testName, nodeClass.Name)
		t.Logf("Created NodePool with multiple instance types: %s", nodePool.Name)

		// Create workload with specific resource requirements to trigger provisioning
		deployment := suite.createTestWorkloadWithInstanceTypeRequirements(t, testName)
		t.Logf("Created test workload with specific resource requirements: %s", deployment.Name)

		// Wait for pods to be scheduled
		suite.waitForPodsToBeScheduled(t, deployment.Name, "default")
		t.Logf("Pods scheduled successfully")

		// Verify that instances use one of the allowed types - use same dynamic types as NodePool
		allowedTypes := suite.GetMultipleInstanceTypes(t, 4)
		t.Logf("Verifying instances use allowed types: %v", allowedTypes)
		suite.verifyInstancesUseAllowedTypes(t, allowedTypes)
		t.Logf("Verified instances use allowed instance types")

		// Verify NodePool requirements are satisfied on the provisioned nodes
		suite.verifyNodePoolRequirementsOnNodes(t, nodePool)
		t.Logf("Verified NodePool requirements are satisfied")

		t.Logf("NodePool instance type selection test completed: %s", testName)
	})
}

// TestE2EInstanceTypeSelection tests Karpenter's instance type selection logic
func TestE2EInstanceTypeSelection(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("instance-selection-%d", time.Now().Unix())
	t.Logf("Starting instance type selection test: %s", testName)

	// Create NodeClass with specific instance type to ensure predictable selection
	nodeClass := suite.createTestNodeClass(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)

	// Create a NodeClaim directly to test instance provisioning
	nodeClaim := suite.createTestNodeClaim(t, testName, nodeClass.Name)
	t.Logf("Created NodeClaim: %s", nodeClaim.Name)

	// Wait for instance to be created
	suite.waitForInstanceCreation(t, nodeClaim.Name)
	t.Logf("Instance created for NodeClaim: %s", nodeClaim.Name)

	// Verify the instance uses the expected type
	expectedType := nodeClass.Spec.InstanceProfile
	suite.verifyInstanceUsesAllowedType(t, nodeClaim.Name, []string{expectedType})
	t.Logf("Verified instance uses expected type: %s", expectedType)

	// Delete the NodeClaim to test cleanup
	suite.deleteNodeClaim(t, nodeClaim.Name)
	t.Logf("Deleted NodeClaim: %s", nodeClaim.Name)

	// Wait for instance to be cleaned up
	suite.waitForInstanceDeletion(t, nodeClaim.Name)
	t.Logf("Instance cleaned up successfully")

	// Cleanup remaining resources
	suite.cleanupTestResources(t, testName)
	t.Logf("Instance type selection test completed: %s", testName)
}

// TestE2EDriftStability tests node stability and drift detection
func TestE2EDriftStability(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("drift-stability-%d", time.Now().Unix())
	t.Logf("Starting drift stability test: %s", testName)

	// Create NodeClass and NodePool for stability testing
	nodeClass := suite.createTestNodeClass(t, testName)
	suite.waitForNodeClassReady(t, nodeClass.Name)

	nodePool := suite.createDriftStabilityNodePool(t, testName, nodeClass.Name)
	t.Logf("Created stability NodePool: %s", nodePool.Name)

	// Create workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName)
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Capture initial node state
	initialNodes := suite.captureNodeSnapshot(t, nodePool.Name)
	require.Greater(t, len(initialNodes), 0, "Should have provisioned nodes")

	// Monitor stability for a brief period
	monitorDuration := 2 * time.Minute
	t.Logf("Monitoring node stability for %v", monitorDuration)
	suite.monitorNodeStability(t, nodePool.Name, initialNodes, monitorDuration)

	// Verify nodes are still stable after monitoring period
	finalNodes := suite.captureNodeSnapshot(t, nodePool.Name)
	require.Equal(t, len(initialNodes), len(finalNodes), "Node count should remain stable")

	// Verify specific nodes haven't drifted
	for _, initialNode := range initialNodes {
		found := false
		for _, finalNode := range finalNodes {
			if initialNode.Name == finalNode.Name {
				found = true
				require.Equal(t, initialNode.ProviderID, finalNode.ProviderID, "Node ProviderID should not change")
				require.Equal(t, initialNode.InstanceType, finalNode.InstanceType, "Node instance type should not change")
				break
			}
		}
		require.True(t, found, "Node %s should still exist after stability period", initialNode.Name)
	}

	// Cleanup
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.cleanupTestResources(t, testName)
	t.Logf("Drift stability test completed successfully: %s", testName)
}
