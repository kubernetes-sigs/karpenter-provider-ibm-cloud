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

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// =============================================================================
// Subnet Drift Tests
// =============================================================================

// TestE2ESubnetDrift_PlacementStrategy_DetectedAndReplaced verifies subnet drift with PlacementStrategy:
//  1. Create NodeClass with PlacementStrategy (no explicit Subnet)
//  2. Autoplacement controller populates Status.SelectedSubnets with available subnets
//  3. Create workload => NodeClaim gets created using a subnet from the pool
//  4. Simulate drift by updating Status.SelectedSubnets to exclude the used subnet
//  5. Verify NodeClaim becomes Drifted and is replaced
//
// This tests the realistic scenario where subnet pool changes (e.g., admin changes subnet tags)
// without the NodeClass spec changing, so hash drift won't catch it.
func TestE2ESubnetDrift_PlacementStrategy_DetectedAndReplaced(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("subnet-drift-placement-%d", time.Now().Unix())
	t.Logf("Starting subnet drift with PlacementStrategy test: %s", testName)

	suite.WithAutoCleanup(t, testName, func() {
		ctx := context.Background()

		// Step 1: Create NodeClass with PlacementStrategy (no explicit subnet)
		nodeClass := suite.createTestNodeClassWithPlacementStrategy(t, testName)
		suite.waitForNodeClassReady(t, nodeClass.Name)
		require.Empty(t, nodeClass.Spec.Subnet, "NodeClass must NOT have explicit subnet")
		require.NotNil(t, nodeClass.Spec.PlacementStrategy, "NodeClass must have PlacementStrategy")
		t.Logf("OK: NodeClass %s ready with PlacementStrategy (ZoneBalance=%s)",
			nodeClass.Name, nodeClass.Spec.PlacementStrategy.ZoneBalance)

		// Step 2: Wait for autoplacement controller to populate Status.SelectedSubnets
		var currentNodeClass v1alpha1.IBMNodeClass
		waitErr := wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true,
			func(ctx context.Context) (bool, error) {
				err := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &currentNodeClass)
				if err != nil {
					return false, nil
				}
				if len(currentNodeClass.Status.SelectedSubnets) > 0 {
					t.Logf("OK: Autoplacement controller populated SelectedSubnets: %v",
						currentNodeClass.Status.SelectedSubnets)
					return true, nil
				}
				t.Logf("Waiting: SelectedSubnets not yet populated by autoplacement controller")
				return false, nil
			})
		require.NoError(t, waitErr, "Autoplacement controller should populate Status.SelectedSubnets")

		// Step 3: Create NodePool and workload to trigger provisioning
		nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
		t.Logf("OK: Created NodePool %s", nodePool.Name)

		deployment := suite.createTestWorkload(t, testName)
		suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
		t.Logf("OK: Workload %s/%s scheduled", deployment.Namespace, deployment.Name)

		// Step 4: Capture the initial READY NodeClaim
		var originalNC karpv1.NodeClaim
		var originalName string
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nodeClaimList karpv1.NodeClaimList
				err := suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
					"test-name": testName,
				})
				if err != nil {
					t.Logf("Error: Failed to list NodeClaims: %v", err)
					return false, nil
				}
				if len(nodeClaimList.Items) == 0 {
					t.Logf("Waiting: No NodeClaims found yet for test %s", testName)
					return false, nil
				}
				for _, nc := range nodeClaimList.Items {
					if suite.isNodeClaimReady(nc) {
						originalNC = nc
						originalName = nc.Name
						t.Logf("OK: Selected READY NodeClaim %s as original", originalName)
						return true, nil
					}
				}
				t.Logf("Waiting: NodeClaims exist but none are READY yet")
				return false, nil
			})
		require.NoError(t, waitErr, "Should find a READY NodeClaim")

		// Step 5: Verify subnet annotation was set by instance provider
		storedSubnet := originalNC.Annotations[v1alpha1.AnnotationIBMNodeClaimSubnetID]
		require.NotEmpty(t, storedSubnet, "NodeClaim must have subnet annotation")
		t.Logf("OK: NodeClaim %s used subnet %s", originalName, storedSubnet)

		// Verify stored subnet came from the SelectedSubnets pool
		found := false
		for _, s := range currentNodeClass.Status.SelectedSubnets {
			if s == storedSubnet {
				found = true
				break
			}
		}
		require.True(t, found, "Stored subnet %s should be in SelectedSubnets %v",
			storedSubnet, currentNodeClass.Status.SelectedSubnets)

		// Step 6: Simulate subnet drift - update SelectedSubnets to EXCLUDE the used subnet
		err := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &currentNodeClass)
		require.NoError(t, err, "Failed to get NodeClass for status update")

		updated := currentNodeClass.DeepCopy()
		var newSelectedSubnets []string
		for _, s := range currentNodeClass.Status.SelectedSubnets {
			if s != storedSubnet {
				newSelectedSubnets = append(newSelectedSubnets, s)
			}
		}
		// If all subnets would be removed, add a placeholder to simulate drift
		if len(newSelectedSubnets) == 0 {
			newSelectedSubnets = []string{"subnet-drift-replacement"}
		}
		updated.Status.SelectedSubnets = newSelectedSubnets
		updated.Status.LastValidationTime = metav1.Time{Time: time.Now()}

		t.Logf("Simulating drift: SelectedSubnets changing from %v to %v (excluding %s)",
			currentNodeClass.Status.SelectedSubnets, newSelectedSubnets, storedSubnet)
		err = suite.kubeClient.Status().Update(ctx, updated)
		require.NoError(t, err, "Failed to update NodeClass status")

		// Step 7: Wait for NodeClaim to be marked Drifted
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nc karpv1.NodeClaim
				if err := suite.kubeClient.Get(ctx, types.NamespacedName{Name: originalName}, &nc); err != nil {
					return false, nil
				}
				for _, cond := range nc.Status.Conditions {
					if cond.Type == "Drifted" && cond.Status == metav1.ConditionTrue {
						t.Logf("OK: NodeClaim %s marked Drifted (Reason=%s)", nc.Name, cond.Reason)
						return true, nil
					}
				}
				t.Logf("Waiting: NodeClaim %s not yet marked Drifted", originalName)
				return false, nil
			})
		require.NoError(t, waitErr, "NodeClaim should become drifted after subnet pool changes")

		// Step 8: Wait for replacement NodeClaim
		var replacementName string
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var allNCs karpv1.NodeClaimList
				if err := suite.kubeClient.List(ctx, &allNCs, client.MatchingLabels{"test-name": testName}); err != nil {
					return false, nil
				}
				for _, nc := range allNCs.Items {
					if nc.Name != originalName && nc.Labels["karpenter.sh/nodepool"] == nodePool.Name && suite.isNodeClaimReady(nc) {
						replacementName = nc.Name
						t.Logf("OK: Found replacement NodeClaim %s", replacementName)
						return true, nil
					}
				}
				t.Logf("Waiting: No ready replacement NodeClaim found yet")
				return false, nil
			})
		require.NoError(t, waitErr, "Replacement NodeClaim should become ready")

		// Step 9: Verify original NodeClaim is deleted or being deleted
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nc karpv1.NodeClaim
				getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: originalName}, &nc)
				if client.IgnoreNotFound(getErr) == nil {
					t.Logf("OK: Original NodeClaim %s deleted", originalName)
					return true, nil
				}
				if nc.DeletionTimestamp != nil {
					t.Logf("OK: Original NodeClaim %s marked for deletion", originalName)
					return true, nil
				}
				t.Logf("Waiting: Original NodeClaim %s still present", originalName)
				return false, nil
			})
		require.NoError(t, waitErr, "Original NodeClaim should be deleted after replacement")

		t.Logf("OK: Subnet drift test completed successfully")
	})
}

// =============================================================================
// Security Group Drift Tests
// =============================================================================

// TestE2ESecurityGroupDrift_DetectedAndReplaced verifies security group drift detection:
//  1. Create NodeClass with explicit security groups
//  2. Create workload => NodeClaim gets created with the security groups
//  3. Modify NodeClass spec.SecurityGroups to different values
//  4. Verify NodeClaim becomes Drifted and is replaced
func TestE2ESecurityGroupDrift_DetectedAndReplaced(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("sg-drift-%d", time.Now().Unix())
	t.Logf("Starting security group drift test: %s", testName)

	suite.WithAutoCleanup(t, testName, func() {
		ctx := context.Background()

		// Step 1: Create NodeClass with explicit security groups
		nodeClass := suite.createTestNodeClass(t, testName)
		suite.waitForNodeClassReady(t, nodeClass.Name)
		require.NotEmpty(t, nodeClass.Spec.SecurityGroups, "NodeClass must have security groups")

		// Verify Status.ResolvedSecurityGroups mirrors spec.SecurityGroups
		var currentNodeClass v1alpha1.IBMNodeClass
		err := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &currentNodeClass)
		require.NoError(t, err)
		require.NotEmpty(t, currentNodeClass.Status.ResolvedSecurityGroups)
		require.ElementsMatch(t, nodeClass.Spec.SecurityGroups, currentNodeClass.Status.ResolvedSecurityGroups)
		t.Logf("OK: NodeClass %s ready with security groups: %v", nodeClass.Name, nodeClass.Spec.SecurityGroups)

		// Step 2: Create NodePool and workload to trigger provisioning
		nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
		t.Logf("OK: Created NodePool %s", nodePool.Name)

		deployment := suite.createTestWorkload(t, testName)
		suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
		t.Logf("OK: Workload %s/%s scheduled", deployment.Namespace, deployment.Name)

		// Step 3: Capture the initial READY NodeClaim
		var originalNC karpv1.NodeClaim
		var originalName string
		waitErr := wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nodeClaimList karpv1.NodeClaimList
				err := suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
					"test-name": testName,
				})
				if err != nil {
					t.Logf("Error: Failed to list NodeClaims: %v", err)
					return false, nil
				}
				if len(nodeClaimList.Items) == 0 {
					t.Logf("Waiting: No NodeClaims found yet for test %s", testName)
					return false, nil
				}
				for _, nc := range nodeClaimList.Items {
					if suite.isNodeClaimReady(nc) {
						originalNC = nc
						originalName = nc.Name
						t.Logf("OK: Selected READY NodeClaim %s as original", originalName)
						return true, nil
					}
				}
				t.Logf("Waiting: NodeClaims exist but none are READY yet")
				return false, nil
			})
		require.NoError(t, waitErr, "Should find a READY NodeClaim")

		// Step 4: Verify security group annotation was set by instance provider
		storedSGs := originalNC.Annotations[v1alpha1.AnnotationIBMNodeClaimSecurityGroups]
		require.NotEmpty(t, storedSGs, "NodeClaim must have security groups annotation")
		storedSGList := strings.Split(storedSGs, ",")
		t.Logf("OK: NodeClaim %s used security groups: %v", originalName, storedSGList)

		// Step 5: Modify NodeClass to use different security groups (this triggers hash drift)
		// Get fresh copy of NodeClass
		err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &currentNodeClass)
		require.NoError(t, err, "Failed to get NodeClass for update")

		// Add a new security group ID to trigger drift
		// Note: This will trigger hash drift since spec changes, but security group
		// drift detection also validates the specific security group IDs
		updated := currentNodeClass.DeepCopy()
		updated.Spec.SecurityGroups = append(updated.Spec.SecurityGroups, "sg-drift-test-new")

		t.Logf("Simulating drift: SecurityGroups changing from %v to %v",
			currentNodeClass.Spec.SecurityGroups, updated.Spec.SecurityGroups)
		err = suite.kubeClient.Update(ctx, updated)
		require.NoError(t, err, "Failed to update NodeClass")

		// Step 6: Wait for NodeClaim to be marked Drifted
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nc karpv1.NodeClaim
				if err := suite.kubeClient.Get(ctx, types.NamespacedName{Name: originalName}, &nc); err != nil {
					return false, nil
				}
				for _, cond := range nc.Status.Conditions {
					if cond.Type == "Drifted" && cond.Status == metav1.ConditionTrue {
						t.Logf("OK: NodeClaim %s marked Drifted (Reason=%s)", nc.Name, cond.Reason)
						return true, nil
					}
				}
				t.Logf("Waiting: NodeClaim %s not yet marked Drifted", originalName)
				return false, nil
			})
		require.NoError(t, waitErr, "NodeClaim should become drifted after security groups change")

		// Step 7: Wait for replacement NodeClaim
		var replacementName string
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var allNCs karpv1.NodeClaimList
				if err := suite.kubeClient.List(ctx, &allNCs, client.MatchingLabels{"test-name": testName}); err != nil {
					return false, nil
				}
				for _, nc := range allNCs.Items {
					if nc.Name != originalName && nc.Labels["karpenter.sh/nodepool"] == nodePool.Name && suite.isNodeClaimReady(nc) {
						replacementName = nc.Name
						t.Logf("OK: Found replacement NodeClaim %s", replacementName)
						return true, nil
					}
				}
				t.Logf("Waiting: No ready replacement NodeClaim found yet")
				return false, nil
			})
		require.NoError(t, waitErr, "Replacement NodeClaim should become ready")

		// Step 8: Verify the replacement NodeClaim has the new security groups
		var replacementNC karpv1.NodeClaim
		err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: replacementName}, &replacementNC)
		require.NoError(t, err, "Failed to get replacement NodeClaim")

		replacementSGs := replacementNC.Annotations[v1alpha1.AnnotationIBMNodeClaimSecurityGroups]
		require.NotEmpty(t, replacementSGs, "Replacement NodeClaim must have security groups annotation")
		t.Logf("OK: Replacement NodeClaim %s has security groups: %s", replacementName, replacementSGs)

		// Step 9: Verify original NodeClaim is deleted or being deleted
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nc karpv1.NodeClaim
				getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: originalName}, &nc)
				if client.IgnoreNotFound(getErr) == nil {
					t.Logf("OK: Original NodeClaim %s deleted", originalName)
					return true, nil
				}
				if nc.DeletionTimestamp != nil {
					t.Logf("OK: Original NodeClaim %s marked for deletion", originalName)
					return true, nil
				}
				t.Logf("Waiting: Original NodeClaim %s still present", originalName)
				return false, nil
			})
		require.NoError(t, waitErr, "Original NodeClaim should be deleted after replacement")

		t.Logf("OK: Security group drift test completed successfully")
	})
}

// TestE2ESecurityGroupDrift_DefaultSecurityGroup verifies drift detection when using default VPC security group:
//  1. Create NodeClass WITHOUT explicit security groups (uses VPC default)
//  2. Create workload => NodeClaim gets created with the default security group
//  3. Verify the default security group ID is stored in annotation
//
// Note: This test validates that the default security group is properly detected and stored.
// Drift from default SG requires VPC-level changes which are not simulated in this test.
func TestE2ESecurityGroupDrift_DefaultSecurityGroup(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("sg-drift-default-%d", time.Now().Unix())
	t.Logf("Starting default security group drift test: %s", testName)

	suite.WithAutoCleanup(t, testName, func() {
		ctx := context.Background()

		// Step 1: Create NodeClass WITHOUT explicit security groups
		nodeClass := suite.createTestNodeClassWithoutSecurityGroups(t, testName)
		suite.waitForNodeClassReady(t, nodeClass.Name)
		require.Empty(t, nodeClass.Spec.SecurityGroups, "NodeClass must NOT have explicit security groups")

		// Verify Status.ResolvedSecurityGroups is populated with default SG
		var currentNodeClass v1alpha1.IBMNodeClass
		err := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &currentNodeClass)
		require.NoError(t, err)
		require.NotEmpty(t, currentNodeClass.Status.ResolvedSecurityGroups)
		t.Logf("OK: NodeClass %s ready with resolved SGs: %v", nodeClass.Name, currentNodeClass.Status.ResolvedSecurityGroups)

		// Step 2: Create NodePool and workload to trigger provisioning
		nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
		t.Logf("OK: Created NodePool %s", nodePool.Name)

		deployment := suite.createTestWorkload(t, testName)
		suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
		t.Logf("OK: Workload %s/%s scheduled", deployment.Namespace, deployment.Name)

		// Step 3: Capture the initial READY NodeClaim
		var originalNC karpv1.NodeClaim
		var originalName string
		waitErr := wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nodeClaimList karpv1.NodeClaimList
				err := suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
					"test-name": testName,
				})
				if err != nil {
					t.Logf("Error: Failed to list NodeClaims: %v", err)
					return false, nil
				}
				if len(nodeClaimList.Items) == 0 {
					t.Logf("Waiting: No NodeClaims found yet for test %s", testName)
					return false, nil
				}
				for _, nc := range nodeClaimList.Items {
					if suite.isNodeClaimReady(nc) {
						originalNC = nc
						originalName = nc.Name
						t.Logf("OK: Selected READY NodeClaim %s as original", originalName)
						return true, nil
					}
				}
				t.Logf("Waiting: NodeClaims exist but none are READY yet")
				return false, nil
			})
		require.NoError(t, waitErr, "Should find a READY NodeClaim")

		// Step 4: Verify default security group annotation was set
		storedSGs := originalNC.Annotations[v1alpha1.AnnotationIBMNodeClaimSecurityGroups]
		require.NotEmpty(t, storedSGs, "NodeClaim must have security groups annotation even with default SG")
		storedSGList := strings.Split(storedSGs, ",")
		require.Len(t, storedSGList, 1, "Default security group should be a single SG")
		t.Logf("OK: NodeClaim %s using default security group: %s", originalName, storedSGs)

		// Step 5: Verify node is stable (no unexpected drift)
		time.Sleep(30 * time.Second)
		var currentNC karpv1.NodeClaim
		err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: originalName}, &currentNC)
		require.NoError(t, err, "Failed to get NodeClaim after stability wait")

		// Check that the NodeClaim is NOT drifted
		for _, cond := range currentNC.Status.Conditions {
			if cond.Type == "Drifted" && cond.Status == metav1.ConditionTrue {
				t.Fatalf("FAIL: NodeClaim %s unexpectedly drifted (Reason=%s)", originalName, cond.Reason)
			}
		}
		t.Logf("OK: NodeClaim %s remains stable with default security group", originalName)

		t.Logf("OK: Default security group drift test completed successfully")
	})
}
