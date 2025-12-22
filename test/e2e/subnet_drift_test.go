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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

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
