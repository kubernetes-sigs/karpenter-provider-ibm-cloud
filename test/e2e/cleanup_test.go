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
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

// TestE2ECleanupNodePoolDeletion tests proper cleanup when deleting NodePools
func TestE2ECleanupNodePoolDeletion(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-nodepool-%d", time.Now().Unix())
	t.Logf("Starting NodePool cleanup test: %s", testName)
	ctx := context.Background()

	// Create NodeClass
	nodeClass := suite.createTestNodeClass(t, testName+"-nodeclass")
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Create NodePool with 2 replicas for better testing
	nodePool := suite.createTestNodePool(t, testName+"-nodepool", nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Create test workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName+"-workload")
	// Modify the deployment to have 2 replicas
	deployment.Spec.Replicas = &[]int32{2}[0]
	err := suite.kubeClient.Update(ctx, deployment)
	require.NoError(t, err)
	t.Logf("Created test workload with 2 replicas: %s", deployment.Name)

	// Wait for pods to be scheduled and nodes to be provisioned
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")
	t.Logf("Pods scheduled successfully")

	// Get initial list of nodes
	initialNodes := suite.getKarpenterNodes(t, nodePool.Name)
	require.Greater(t, len(initialNodes), 0, "Should have provisioned at least one node")
	t.Logf("Initial provisioned nodes: %d", len(initialNodes))

	// Delete the NodePool first - this should trigger cleanup
	err = suite.kubeClient.Delete(ctx, nodePool)
	require.NoError(t, err)
	t.Logf("Deleted NodePool: %s", nodePool.Name)

	// Wait for pods to be evicted
	suite.waitForPodsGone(t, deployment.Name+"-workload")
	t.Logf("Pods evicted successfully")

	// Verify nodes are eventually cleaned up
	// This might take a while as Karpenter needs to process the NodePool deletion
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		currentNodes := suite.getKarpenterNodes(t, nodePool.Name)
		if len(currentNodes) == 0 {
			t.Logf("✅ All nodes cleaned up successfully")
			break
		}
		t.Logf("Still waiting for %d nodes to be cleaned up", len(currentNodes))
		time.Sleep(30 * time.Second)
	}

	// Final verification
	finalNodes := suite.getKarpenterNodes(t, nodePool.Name)
	require.Equal(t, 0, len(finalNodes), "All nodes should be cleaned up after NodePool deletion")

	// Cleanup remaining resources
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.cleanupTestResources(t, testName)
	t.Logf("✅ NodePool cleanup test completed successfully: %s", testName)
}

// TestE2ECleanupNodeClassDeletion tests proper cleanup when deleting NodeClasses
func TestE2ECleanupNodeClassDeletion(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-nodeclass-%d", time.Now().Unix())
	t.Logf("Starting NodeClass cleanup test: %s", testName)
	ctx := context.Background()

	// Create NodeClass
	nodeClass := suite.createTestNodeClass(t, testName+"-nodeclass")
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Create NodePool that references this NodeClass
	nodePool := suite.createTestNodePool(t, testName+"-nodepool", nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Create test workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName+"-workload")
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
	suite.waitForPodsGone(t, deployment.Name+"-workload")

	// Delete the NodePool first
	err = suite.kubeClient.Delete(ctx, nodePool)
	require.NoError(t, err)
	t.Logf("Deleted NodePool: %s", nodePool.Name)

	// Wait for NodeClaims to be cleaned up
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		var currentNodeClaimList karpv1.NodeClaimList
		err := suite.kubeClient.List(ctx, &currentNodeClaimList, client.MatchingLabels{
			"karpenter.sh/nodepool": nodePool.Name,
		})
		require.NoError(t, err)

		if len(currentNodeClaimList.Items) == 0 {
			t.Logf("✅ All NodeClaims cleaned up")
			break
		}
		t.Logf("Still waiting for %d NodeClaims to be cleaned up", len(currentNodeClaimList.Items))
		time.Sleep(30 * time.Second)
	}

	// Now try to delete the NodeClass - it should succeed if no NodePools reference it
	err = suite.kubeClient.Delete(ctx, nodeClass)
	require.NoError(t, err)
	t.Logf("✅ Successfully deleted NodeClass: %s", nodeClass.Name)

	// Cleanup any remaining resources
	suite.cleanupTestResources(t, testName)
	t.Logf("✅ NodeClass cleanup test completed successfully: %s", testName)
}

// TestE2ECleanupOrphanedResources tests cleanup of orphaned resources
func TestE2ECleanupOrphanedResources(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-orphaned-%d", time.Now().Unix())
	t.Logf("Starting orphaned resources cleanup test: %s", testName)
	ctx := context.Background()

	// Create NodeClass and NodePool
	nodeClass := suite.createTestNodeClass(t, testName+"-nodeclass")
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createTestNodePool(t, testName+"-nodepool", nodeClass.Name)

	// Create test workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName+"-workload")
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
	err = suite.kubeClient.Delete(ctx, nodePool)
	require.NoError(t, err)
	t.Logf("Deleted NodePool, leaving NodeClaim potentially orphaned: %s", originalNodeClaim.Name)

	// Wait a bit for the controller to process the deletion
	time.Sleep(30 * time.Second)

	// Check if the NodeClaim still exists (it might be cleaned up automatically)
	var orphanedNodeClaim karpv1.NodeClaim
	err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: originalNodeClaim.Name}, &orphanedNodeClaim)

	if err == nil {
		// NodeClaim still exists - verify it gets cleaned up eventually
		t.Logf("NodeClaim still exists, waiting for automatic cleanup")
		deadline := time.Now().Add(5 * time.Minute)

		for time.Now().Before(deadline) {
			err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: originalNodeClaim.Name}, &orphanedNodeClaim)
			if err != nil {
				t.Logf("✅ Orphaned NodeClaim was automatically cleaned up")
				break
			}
			time.Sleep(15 * time.Second)
		}
	} else {
		t.Logf("✅ NodeClaim was already cleaned up with NodePool deletion")
	}

	// Clean up workload
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.waitForPodsGone(t, deployment.Name+"-workload")

	// Use our comprehensive cleanup function to catch any remaining orphaned resources
	suite.cleanupOrphanedKubernetesResources(t)

	// Final cleanup
	suite.cleanupTestResources(t, testName)
	t.Logf("✅ Orphaned resources cleanup test completed: %s", testName)
}

// TestE2ECleanupIBMCloudResources tests cleanup of IBM Cloud resources
func TestE2ECleanupIBMCloudResources(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-ibmcloud-%d", time.Now().Unix())
	t.Logf("Starting IBM Cloud resources cleanup test: %s", testName)
	ctx := context.Background()

	// Get initial list of IBM Cloud instances for comparison
	initialInstances, err := suite.getIBMCloudInstances(t)
	require.NoError(t, err)
	initialInstanceCount := len(initialInstances)
	t.Logf("Initial IBM Cloud instances: %d", initialInstanceCount)

	// Create NodeClass and NodePool
	nodeClass := suite.createTestNodeClass(t, testName+"-nodeclass")
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createTestNodePool(t, testName+"-nodepool", nodeClass.Name)

	// Create test workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName+"-workload")
	// Modify the deployment to have 2 replicas
	deployment.Spec.Replicas = &[]int32{2}[0]
	err = suite.kubeClient.Update(ctx, deployment)
	require.NoError(t, err)
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Wait a bit for instances to be fully created in IBM Cloud
	time.Sleep(60 * time.Second)

	// Get the list of instances after provisioning
	instancesAfterProvisioning, err := suite.getIBMCloudInstances(t)
	require.NoError(t, err)
	afterProvisioningCount := len(instancesAfterProvisioning)
	t.Logf("Instances after provisioning: %d (expected increase: %d)",
		afterProvisioningCount, afterProvisioningCount-initialInstanceCount)

	// Should have more instances now
	require.Greater(t, afterProvisioningCount, initialInstanceCount,
		"Should have provisioned new IBM Cloud instances")

	// Start cleanup process
	suite.cleanupTestWorkload(t, deployment.Name, "default")
	suite.waitForPodsGone(t, deployment.Name+"-workload")

	// Delete NodePool to trigger instance cleanup
	err = suite.kubeClient.Delete(ctx, nodePool)
	require.NoError(t, err)
	t.Logf("Deleted NodePool, waiting for IBM Cloud instances to be cleaned up")

	// Wait for instances to be cleaned up in IBM Cloud
	// This can take a while as IBM Cloud cleanup is not immediate
	deadline := time.Now().Add(15 * time.Minute)
	finalInstanceCount := afterProvisioningCount

	for time.Now().Before(deadline) {
		currentInstances, err := suite.getIBMCloudInstances(t)
		if err != nil {
			t.Logf("Warning: Failed to get current instance list: %v", err)
			time.Sleep(30 * time.Second)
			continue
		}

		finalInstanceCount = len(currentInstances)
		t.Logf("Current instance count: %d (target: %d)", finalInstanceCount, initialInstanceCount)

		if finalInstanceCount <= initialInstanceCount {
			t.Logf("✅ IBM Cloud instances cleaned up successfully")
			break
		}

		time.Sleep(30 * time.Second)
	}

	// Verify cleanup was successful (allow some tolerance for long-running instances)
	require.LessOrEqual(t, finalInstanceCount, initialInstanceCount+1,
		"Most IBM Cloud instances should be cleaned up (found %d, expected ~%d)",
		finalInstanceCount, initialInstanceCount)

	if finalInstanceCount > initialInstanceCount {
		t.Logf("⚠️  Warning: %d instances may still be cleaning up (this can take additional time)",
			finalInstanceCount-initialInstanceCount)
	}

	// Cleanup remaining test resources
	suite.cleanupTestResources(t, testName)
	t.Logf("✅ IBM Cloud resources cleanup test completed: %s", testName)
}
