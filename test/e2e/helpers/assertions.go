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

package helpers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

const (
	// DefaultPollInterval should match the pollInterval in suite.go for consistency
	DefaultPollInterval = 5 * time.Second
	DefaultTimeout      = 10 * time.Minute
)

// NodeClassReadyAssertion waits for a NodeClass to be ready
func NodeClassReadyAssertion(t *testing.T, ctx context.Context, kubeClient client.Client, nodeClassName string, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	start := time.Now()
	var nodeClass v1alpha1.IBMNodeClass

	require.Eventually(t, func() bool {
		err := kubeClient.Get(ctx, client.ObjectKey{Name: nodeClassName}, &nodeClass)
		if err != nil {
			t.Logf("Failed to get NodeClass %s: %v", nodeClassName, err)
			return false
		}

		for _, cond := range nodeClass.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
				t.Logf("NodeClass %s is ready after %s", nodeClassName, time.Since(start).Round(time.Second))
				return true
			}
		}
		return false
	}, timeout, DefaultPollInterval, "NodeClass %s did not become ready within %s", nodeClassName, timeout)
}

// NodePoolReadyAssertion waits for a NodePool to be ready
func NodePoolReadyAssertion(t *testing.T, ctx context.Context, kubeClient client.Client, nodePoolName string, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	start := time.Now()
	var nodePool karpv1.NodePool

	require.Eventually(t, func() bool {
		err := kubeClient.Get(ctx, client.ObjectKey{Name: nodePoolName}, &nodePool)
		if err != nil {
			t.Logf("Failed to get NodePool %s: %v", nodePoolName, err)
			return false
		}

		for _, cond := range nodePool.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
				t.Logf("NodePool %s is ready after %s", nodePoolName, time.Since(start).Round(time.Second))
				return true
			}
		}
		return false
	}, timeout, DefaultPollInterval, "NodePool %s did not become ready within %s", nodePoolName, timeout)
}

// DeploymentReadyAssertion waits for a Deployment to have all replicas ready
func DeploymentReadyAssertion(t *testing.T, ctx context.Context, kubeClient client.Client, deploymentName, namespace string, expectedReplicas int32, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	start := time.Now()
	var deployment appsv1.Deployment

	require.Eventually(t, func() bool {
		err := kubeClient.Get(ctx, client.ObjectKey{Name: deploymentName, Namespace: namespace}, &deployment)
		if err != nil {
			t.Logf("Failed to get Deployment %s/%s: %v", namespace, deploymentName, err)
			return false
		}

		if deployment.Status.ReadyReplicas == expectedReplicas &&
			deployment.Status.AvailableReplicas == expectedReplicas &&
			deployment.Status.UnavailableReplicas == 0 {
			t.Logf("Deployment %s/%s has %d/%d replicas ready after %s",
				namespace, deploymentName, deployment.Status.ReadyReplicas, expectedReplicas, time.Since(start).Round(time.Second))
			return true
		}

		t.Logf("Deployment %s/%s: %d/%d ready, %d available, %d unavailable",
			namespace, deploymentName,
			deployment.Status.ReadyReplicas, expectedReplicas,
			deployment.Status.AvailableReplicas, deployment.Status.UnavailableReplicas)
		return false
	}, timeout, DefaultPollInterval, "Deployment %s/%s did not have %d replicas ready within %s", namespace, deploymentName, expectedReplicas, timeout)
}

// NodesExistAssertion verifies that the expected number of nodes exist with the given label selector
func NodesExistAssertion(t *testing.T, ctx context.Context, kubeClient client.Client, labelSelector map[string]string, expectedCount int, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	start := time.Now()

	require.Eventually(t, func() bool {
		var nodeList corev1.NodeList
		err := kubeClient.List(ctx, &nodeList, client.MatchingLabels(labelSelector))
		if err != nil {
			t.Logf("Failed to list nodes: %v", err)
			return false
		}

		if len(nodeList.Items) == expectedCount {
			t.Logf("Found %d nodes with labels %v after %s", expectedCount, labelSelector, time.Since(start).Round(time.Second))
			return true
		}

		t.Logf("Found %d nodes, expected %d", len(nodeList.Items), expectedCount)
		return false
	}, timeout, DefaultPollInterval, "Expected %d nodes with labels %v, but condition not met within %s", expectedCount, labelSelector, timeout)
}

// NodeClaimsReadyAssertion waits for the expected number of NodeClaims to be ready
func NodeClaimsReadyAssertion(t *testing.T, ctx context.Context, kubeClient client.Client, labelSelector map[string]string, expectedCount int, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	start := time.Now()

	require.Eventually(t, func() bool {
		var nodeClaimList karpv1.NodeClaimList
		err := kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels(labelSelector))
		if err != nil {
			t.Logf("Failed to list nodeclaims: %v", err)
			return false
		}

		readyCount := 0
		for _, nc := range nodeClaimList.Items {
			for _, cond := range nc.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == metav1.ConditionTrue {
					readyCount++
					break
				}
			}
		}

		if readyCount == expectedCount {
			t.Logf("Found %d ready NodeClaims with labels %v after %s", expectedCount, labelSelector, time.Since(start).Round(time.Second))
			return true
		}

		t.Logf("Found %d ready NodeClaims, expected %d (total: %d)", readyCount, expectedCount, len(nodeClaimList.Items))
		return false
	}, timeout, DefaultPollInterval, "Expected %d ready NodeClaims with labels %v, but condition not met within %s", expectedCount, labelSelector, timeout)
}

// PodsScheduledAssertion verifies that pods are scheduled and running
func PodsScheduledAssertion(t *testing.T, ctx context.Context, kubeClient client.Client, labelSelector map[string]string, namespace string, expectedCount int, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	start := time.Now()

	require.Eventually(t, func() bool {
		var podList corev1.PodList
		err := kubeClient.List(ctx, &podList, client.InNamespace(namespace), client.MatchingLabels(labelSelector))
		if err != nil {
			t.Logf("Failed to list pods: %v", err)
			return false
		}

		runningCount := 0
		for _, pod := range podList.Items {
			if pod.Status.Phase == corev1.PodRunning {
				runningCount++
			}
		}

		if runningCount == expectedCount {
			t.Logf("Found %d running pods with labels %v after %s", expectedCount, labelSelector, time.Since(start).Round(time.Second))
			return true
		}

		t.Logf("Found %d running pods, expected %d (total: %d)", runningCount, expectedCount, len(podList.Items))
		return false
	}, timeout, DefaultPollInterval, "Expected %d running pods with labels %v, but condition not met within %s", expectedCount, labelSelector, timeout)
}

// ResourceCleanupAssertion verifies that resources are deleted
func ResourceCleanupAssertion(t *testing.T, ctx context.Context, kubeClient client.Client, resourceList client.ObjectList, timeout time.Duration) {
	t.Helper()
	if timeout == 0 {
		timeout = 2 * time.Minute
	}

	start := time.Now()

	require.Eventually(t, func() bool {
		err := kubeClient.List(ctx, resourceList)
		if err != nil {
			t.Logf("Failed to list resources: %v", err)
			return false
		}

		// Use reflection to get the Items field from the list
		items := resourceList.(interface{ GetItems() []client.Object }).GetItems()
		if len(items) == 0 {
			t.Logf("All resources cleaned up after %s", time.Since(start).Round(time.Second))
			return true
		}

		t.Logf("Still waiting for %d resources to be deleted", len(items))
		return false
	}, timeout, DefaultPollInterval, "Resources were not cleaned up within %s", timeout)
}
