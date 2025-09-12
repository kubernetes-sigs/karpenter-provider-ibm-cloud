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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// waitForNodeClassReady waits for a NodeClass to be in a ready state with cache-aware validation
func (s *E2ETestSuite) waitForNodeClassReady(t *testing.T, nodeClassName string) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	checkCount := 0

	// First, wait a brief moment to allow controller event processing
	initialDelay := 2 * time.Second
	t.Logf("⏳ Waiting %v for initial controller processing before validation checks...", initialDelay)
	select {
	case <-time.After(initialDelay):
	case <-ctx.Done():
		t.Fatal("Context cancelled during initial delay")
	}

	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		checkCount++
		var nodeClass v1alpha1.IBMNodeClass

		// Get NodeClass with exponential backoff for network issues
		var getErr error
		for attempt := 0; attempt < 3; attempt++ {
			getErr = s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClassName}, &nodeClass)
			if getErr == nil {
				break
			}
			if errors.IsNotFound(getErr) {
				break // Don't retry not found errors
			}
			backoff := time.Duration(100*attempt) * time.Millisecond
			t.Logf("Check #%d attempt %d: Get failed, retrying after %v: %v", checkCount, attempt+1, backoff, getErr)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}

		if getErr != nil {
			t.Logf("Check #%d: Failed to get NodeClass after retries: %v", checkCount, getErr)
			return false, getErr
		}

		// Log the full NodeClass spec on first successful check
		if checkCount == 1 {
			t.Logf("🔍 NodeClass Spec: VPC=%s, Subnet=%s, Zone=%s, Region=%s, InstanceProfile=%s",
				nodeClass.Spec.VPC, nodeClass.Spec.Subnet, nodeClass.Spec.Zone,
				nodeClass.Spec.Region, nodeClass.Spec.InstanceProfile)
			t.Logf("🔍 NodeClass ResourceGroup: %s", nodeClass.Spec.ResourceGroup)
			t.Logf("🔍 NodeClass SecurityGroups: %v", nodeClass.Spec.SecurityGroups)
			t.Logf("🔍 NodeClass APIServerEndpoint: %s", nodeClass.Spec.APIServerEndpoint)
			t.Logf("🔍 NodeClass ResourceVersion: %s", nodeClass.ResourceVersion)
		}

		// Check if controller has processed this resource (has status conditions)
		if len(nodeClass.Status.Conditions) == 0 {
			t.Logf("Check #%d: Controller hasn't processed NodeClass yet (no status conditions), waiting...", checkCount)
			return false, nil
		}

		// Log all conditions every check with enhanced formatting
		t.Logf("Check #%d: NodeClass %s conditions (ResourceVersion: %s):", checkCount, nodeClassName, nodeClass.ResourceVersion)
		for _, condition := range nodeClass.Status.Conditions {
			statusIcon := "❓"
			if condition.Status == metav1.ConditionTrue {
				statusIcon = "✅"
			} else if condition.Status == metav1.ConditionFalse {
				statusIcon = "❌"
			}
			t.Logf("  %s Type: %s, Status: %s, Reason: %s, Message: %s",
				statusIcon, condition.Type, condition.Status, condition.Reason, condition.Message)
		}

		// Check for Ready condition (current controller implementation)
		for _, condition := range nodeClass.Status.Conditions {
			if condition.Type == "Ready" {
				if condition.Status == metav1.ConditionTrue {
					t.Logf("✅ NodeClass is ready after %d checks: %s - %s", checkCount, condition.Reason, condition.Message)
					return true, nil
				} else {
					t.Logf("❌ NodeClass not ready (check #%d): %s - %s", checkCount, condition.Reason, condition.Message)
					// Log validation time for debugging cache issues
					if !nodeClass.Status.LastValidationTime.IsZero() {
						validationAge := time.Since(nodeClass.Status.LastValidationTime.Time)
						t.Logf("🕐 Last validation: %v ago", validationAge)
					}
					return false, nil
				}
			}
		}

		// No Ready condition found yet - this indicates controller hasn't finished processing
		t.Logf("⚠️  Ready condition not found on check #%d - controller still processing...", checkCount)
		return false, nil
	})
	require.NoError(t, err, "NodeClass should become ready within timeout")
}

// waitForInstanceCreation waits for a NodeClaim to have an instance created
func (s *E2ETestSuite) waitForInstanceCreation(t *testing.T, nodeClaimName string) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var nodeClaim karpv1.NodeClaim
		err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
		if err != nil {
			return false, err
		}
		// Check if ProviderID is set and instance is launched
		if nodeClaim.Status.ProviderID == "" {
			return false, nil
		}
		// Check if Launched condition is True
		for _, condition := range nodeClaim.Status.Conditions {
			if condition.Type == "Launched" && condition.Status == metav1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
	require.NoError(t, err, "NodeClaim should be launched within timeout")
}

// waitForInstanceDeletion waits for a NodeClaim to be fully deleted
func (s *E2ETestSuite) waitForInstanceDeletion(t *testing.T, nodeClaimName string) {
	// Use longer timeout for deletion as IBM Cloud instances can take time to clean up
	deletionTimeout := 15 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), deletionTimeout)
	defer cancel()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, deletionTimeout, true, func(ctx context.Context) (bool, error) {
		var nodeClaim karpv1.NodeClaim
		err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
		if errors.IsNotFound(err) {
			return true, nil // NodeClaim was deleted
		}
		if err != nil {
			return false, err
		}
		// Log the current status to help debug timeout issues
		t.Logf("NodeClaim %s still exists, status: %+v", nodeClaimName, nodeClaim.Status)
		return false, nil // Still exists
	})
	require.NoError(t, err, "NodeClaim should be deleted within timeout")
}

// waitForPodsToBeScheduled waits for all pods in a deployment to be scheduled and running
func (s *E2ETestSuite) waitForPodsToBeScheduled(t *testing.T, deploymentName, namespace string) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	checkCount := 0
	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		checkCount++
		var deployment appsv1.Deployment
		err := s.kubeClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, &deployment)
		if err != nil {
			return false, err
		}
		// Check if deployment has desired replicas ready
		if deployment.Status.ReadyReplicas == *deployment.Spec.Replicas {
			t.Logf("✅ All %d replicas are ready for deployment %s after %d checks",
				deployment.Status.ReadyReplicas, deploymentName, checkCount)
			return true, nil
		}
		t.Logf("Check #%d: Deployment %s: %d/%d replicas ready, %d available, %d unavailable",
			checkCount, deploymentName, deployment.Status.ReadyReplicas,
			*deployment.Spec.Replicas, deployment.Status.AvailableReplicas,
			deployment.Status.UnavailableReplicas)
		// Log NodeClaim status every 3 checks
		if checkCount%3 == 0 {
			s.logNodeClaimStatus(t)
			// Extract testName from deploymentName to get correct NodePool name
			// deploymentName format: "testname-workload" -> NodePool: "testname-nodepool"
			if strings.HasSuffix(deploymentName, "-workload") {
				testName := strings.TrimSuffix(deploymentName, "-workload")
				s.logNodePoolStatus(t, testName+"-nodepool")
			}
		}

		// If we're still waiting after many checks, dump some diagnostics
		if checkCount > 10 && checkCount%5 == 0 {
			s.logDeploymentDiagnostics(t, deploymentName, namespace)
		}

		return false, nil
	})
	require.NoError(t, err, "Deployment pods should be scheduled and running within timeout")
}

// waitForPodsGone waits for all pods from a deployment to be completely terminated
func (s *E2ETestSuite) waitForPodsGone(t *testing.T, deploymentName string) {
	ctx := context.Background()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		var podList corev1.PodList
		err := s.kubeClient.List(ctx, &podList, client.MatchingLabels{
			"app": deploymentName,
		})
		if err != nil {
			t.Logf("Error checking pods: %v", err)
			return false, nil
		}
		remainingPods := 0
		for _, pod := range podList.Items {
			if pod.DeletionTimestamp == nil {
				remainingPods++
			}
		}
		t.Logf("Remaining pods for deployment %s: %d", deploymentName, remainingPods)
		return remainingPods == 0, nil
	})
	require.NoError(t, err, "All pods should be terminated")
}
