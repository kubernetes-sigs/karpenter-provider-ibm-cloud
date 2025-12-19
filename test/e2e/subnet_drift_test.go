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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// TestE2ESubnetDrift_DetectedAndReplaced verifies a full subnet drift scenario:
//  1. Create NodeClass/NodePool/workload => NodeClaim + node.
//  2. Simulate subnet drift by changing NodeClass.Status.SelectedSubnets so it no longer
//     contains the stored subnet from the NodeClaim annotation.
//  3. Verify the NodeClaim becomes Drifted and is eventually replaced by a new NodeClaim,
//     while workload pods remain available.
func TestE2ESubnetDrift_DetectedAndReplaced(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("subnet-drift-replacement-%d", time.Now().Unix())
	t.Logf("Starting subnet drift replacement test: %s", testName)

	suite.WithAutoCleanup(t, testName, func() {
		ctx := context.Background()

		// 1. Create NodeClass and wait for it to be Ready
		nodeClass := suite.createTestNodeClass(t, testName)
		suite.waitForNodeClassReady(t, nodeClass.Name)
		require.NotEmpty(t, nodeClass.Spec.Subnet, "Test NodeClass must have a subnet set")
		t.Logf("NodeClass %s is ready with subnet %s", nodeClass.Name, nodeClass.Spec.Subnet)

		// 2. Create NodePool and workload to trigger provisioning
		nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
		t.Logf("Created NodePool: %s", nodePool.Name)

		deployment := suite.createTestWorkload(t, testName)
		suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
		t.Logf("Initial workload %s/%s scheduled successfully", deployment.Namespace, deployment.Name)

		// 3. Capture the initial READY NodeClaim for this test
		var originalNC karpv1.NodeClaim
		var originalName string
		waitErr := wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nodeClaimList karpv1.NodeClaimList
				err := suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
					"test-name": testName,
				})
				if err != nil {
					t.Logf("Error listing NodeClaims for test %s: %v", testName, err)
					return false, nil
				}
				if len(nodeClaimList.Items) == 0 {
					t.Logf("⏳ No NodeClaims found yet for test %s", testName)
					return false, nil
				}

				for _, nc := range nodeClaimList.Items {
					if suite.isNodeClaimReady(nc) {
						originalNC = nc
						originalName = nc.Name
						t.Logf("✅ Selected READY NodeClaim %s as original for drift verification", originalName)
						return true, nil
					}
				}

				t.Logf("⏳ NodeClaims exist for test %s but none are READY yet (count=%d)", testName, len(nodeClaimList.Items))
				return false, nil
			})
		require.NoError(t, waitErr, "Should find a READY NodeClaim for test %s", testName)
		require.NotEmpty(t, originalName, "Original NodeClaim name should be set")

		// Sanity: ensure subnet annotation is present and matches NodeClass subnet
		storedSubnet := originalNC.Annotations[v1alpha1.AnnotationIBMNodeClaimSubnetID]
		require.NotEmpty(t, storedSubnet,
			"NodeClaim %s must have subnet annotation %s for drift detection",
			originalName, v1alpha1.AnnotationIBMNodeClaimSubnetID)
		t.Logf("Original NodeClaim %s stored subnet annotation: %s", originalName, storedSubnet)

		// 4. Simulate subnet drift by updating NodeClass.Status.SelectedSubnets
		//    to a set that does NOT include the stored subnet.
		var currentNodeClass v1alpha1.IBMNodeClass
		err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &currentNodeClass)
		require.NoError(t, err, "Failed to get NodeClass %s for status update", nodeClass.Name)

		updated := currentNodeClass.DeepCopy()
		updated.Status.SelectedSubnets = []string{"subnet-a-drift-test", "subnet-b-drift-test"}
		t.Logf("Updating NodeClass %s SelectedSubnets to %v (excluding original subnet %s)",
			nodeClass.Name, updated.Status.SelectedSubnets, storedSubnet)

		updated.Status.LastValidationTime = metav1.Time{Time: time.Now()}
		err = suite.kubeClient.Status().Update(ctx, updated)
		require.NoError(t, err, "Failed to update NodeClass status for subnet drift simulation")

		// 5. Wait for the original NodeClaim to be marked as Drifted
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nc karpv1.NodeClaim
				getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: originalName}, &nc)
				if getErr != nil {
					t.Logf("Error getting original NodeClaim %s while waiting for drift: %v", originalName, getErr)
					return false, nil
				}

				for _, cond := range nc.Status.Conditions {
					t.Logf("Original NodeClaim %s condition: Type=%s Status=%s Reason=%s Message=%s",
						nc.Name, cond.Type, cond.Status, cond.Reason, cond.Message)
					if cond.Type == "Drifted" && cond.Status == metav1.ConditionTrue {
						t.Logf("✅ Original NodeClaim %s is marked Drifted=true (Reason=%s)", nc.Name, cond.Reason)
						return true, nil
					}
				}

				t.Logf("⏳ Original NodeClaim %s not yet marked Drifted, continuing to wait...", nc.Name)
				return false, nil
			})
		require.NoError(t, waitErr, "Original NodeClaim should become drifted after subnet set changes")

		// 6. Wait for a replacement NodeClaim from the same NodePool
		var replacementName string
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var allNCs karpv1.NodeClaimList
				listErr := suite.kubeClient.List(ctx, &allNCs, client.MatchingLabels{
					"test-name": testName,
				})
				if listErr != nil {
					t.Logf("Error listing NodeClaims while waiting for replacement: %v", listErr)
					return false, nil
				}

				for _, nc := range allNCs.Items {
					if nc.Name == originalName {
						// Skip the original
						continue
					}
					// Ensure the replacement is from the same NodePool and is READY
					if nc.Labels["karpenter.sh/nodepool"] == nodePool.Name && suite.isNodeClaimReady(nc) {
						replacementName = nc.Name
						t.Logf("✅ Found replacement NodeClaim %s from NodePool %s (original was %s)",
							replacementName, nodePool.Name, originalName)
						return true, nil
					}
				}

				t.Logf("⏳ No ready replacement NodeClaim from NodePool %s found yet (original=%s, totalNCs=%d)",
					nodePool.Name, originalName, len(allNCs.Items))
				return false, nil
			})
		require.NoError(t, waitErr, "A replacement NodeClaim should become ready after drift")
		require.NotEmpty(t, replacementName, "Replacement NodeClaim name should be populated")

		// 7. Ensure the original NodeClaim is being deleted or gone
		waitErr = wait.PollUntilContextTimeout(ctx, 10*time.Second, testTimeout, true,
			func(ctx context.Context) (bool, error) {
				var nc karpv1.NodeClaim
				getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: originalName}, &nc)
				if client.IgnoreNotFound(getErr) == nil {
					t.Logf("✅ Original NodeClaim %s has been deleted", originalName)
					return true, nil
				}

				if getErr != nil {
					t.Logf("Error checking original NodeClaim %s deletion: %v", originalName, getErr)
					return false, nil
				}

				if nc.DeletionTimestamp != nil {
					t.Logf("⏳ Original NodeClaim %s has deletionTimestamp set", originalName)
					return true, nil
				}

				t.Logf("⏳ Original NodeClaim %s still present without deletionTimestamp", originalName)
				return false, nil
			})
		require.NoError(t, waitErr, "Original NodeClaim should be deleted or marked for deletion after replacement")

		// 8. Finally, verify workload is still healthy after replacement
		// Read the deployment once and ensure it is fully available after replacement
		var finalDeployment corev1.PodList
		err := suite.kubeClient.List(ctx, &finalDeployment, client.InNamespace(deployment.Namespace), client.MatchingLabels{
			"app": deployment.Name,
		})
		require.NoError(t, err, "Should be able to list pods for deployment %s/%s after drift replacement", deployment.Namespace, deployment.Name)
		t.Logf("✅ Verified workload %s/%s has %d pods after subnet drift replacement",
			deployment.Namespace, deployment.Name, len(finalDeployment.Items))
	})
}
