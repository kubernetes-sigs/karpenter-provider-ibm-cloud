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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// deleteNodeClaim deletes a specific NodeClaim
func (s *E2ETestSuite) deleteNodeClaim(t *testing.T, nodeClaimName string) {
	ctx := context.Background()
	var nodeClaim karpv1.NodeClaim
	err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
	require.NoError(t, err)
	err = s.kubeClient.Delete(ctx, &nodeClaim)
	require.NoError(t, err)
}

// cleanupTestWorkload deletes a test deployment
func (s *E2ETestSuite) cleanupTestWorkload(t *testing.T, deploymentName, namespace string) {
	ctx := context.Background()
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: namespace,
		},
	}
	err := s.kubeClient.Delete(ctx, deployment)
	if err != nil && !errors.IsNotFound(err) {
		t.Logf("Failed to delete deployment %s: %v", deploymentName, err)
	}
}

// waitForResourceDeletion waits for resources to be completely deleted
func (s *E2ETestSuite) waitForResourceDeletion(ctx context.Context, t *testing.T, resourceName string, listObj client.ObjectList, labelSelector map[string]string, maxWait time.Duration) bool {
	t.Logf("Waiting for %s deletion (max %v)...", resourceName, maxWait)

	start := time.Now()
	for time.Since(start) < maxWait {
		err := s.kubeClient.List(ctx, listObj, client.MatchingLabels(labelSelector))
		if err != nil {
			t.Logf("Error checking %s deletion: %v", resourceName, err)
			return false
		}

		var itemCount int
		switch list := listObj.(type) {
		case *appsv1.DeploymentList:
			itemCount = len(list.Items)
		case *karpv1.NodeClaimList:
			itemCount = len(list.Items)
		case *karpv1.NodePoolList:
			itemCount = len(list.Items)
		case *v1alpha1.IBMNodeClassList:
			itemCount = len(list.Items)
		case *policyv1.PodDisruptionBudgetList:
			itemCount = len(list.Items)
		}

		if itemCount == 0 {
			t.Logf("✅ All %s resources deleted", resourceName)
			return true
		}

		t.Logf("Still waiting for %s deletion... (%d remaining, %v elapsed)", resourceName, itemCount, time.Since(start).Round(time.Second))
		time.Sleep(10 * time.Second)
	}

	t.Logf("❌ Warning: %s deletion timeout after %v", resourceName, maxWait)
	return false
}

// cleanupTestResources performs comprehensive cleanup of test resources with proper timeouts
func (s *E2ETestSuite) cleanupTestResources(t *testing.T, testName string) {
	ctx := context.Background()
	t.Logf("Starting cleanup for test: %s", testName)

	// List of resources to clean up in specific order (pods first, then IBMNodeClass last)
	resources := []struct {
		name          string
		obj           client.Object
		listObj       client.ObjectList
		timeout       time.Duration
		deleteTimeout time.Duration
	}{
		{
			name:          "PodDisruptionBudget",
			obj:           &policyv1.PodDisruptionBudget{},
			listObj:       &policyv1.PodDisruptionBudgetList{},
			timeout:       60 * time.Second,
			deleteTimeout: 30 * time.Second,
		},
		{
			name:          "Deployment",
			obj:           &appsv1.Deployment{},
			listObj:       &appsv1.DeploymentList{},
			timeout:       120 * time.Second,
			deleteTimeout: 60 * time.Second,
		},
		{
			name:          "NodeClaim",
			obj:           &karpv1.NodeClaim{},
			listObj:       &karpv1.NodeClaimList{},
			timeout:       10 * time.Minute, // IBM Cloud takes longer
			deleteTimeout: 60 * time.Second,
		},
		{
			name:          "NodePool",
			obj:           &karpv1.NodePool{},
			listObj:       &karpv1.NodePoolList{},
			timeout:       8 * time.Minute,
			deleteTimeout: 60 * time.Second,
		},
		{
			name:          "IBMNodeClass",
			obj:           &v1alpha1.IBMNodeClass{},
			listObj:       &v1alpha1.IBMNodeClassList{},
			timeout:       5 * time.Minute,
			deleteTimeout: 60 * time.Second,
		},
	}

	// Clean up resources by test name label in specific order
	for _, resource := range resources {
		t.Logf("Cleaning up %s resources for test %s", resource.name, testName)

		// Create context with timeout for the delete operations
		deleteCtx, cancel := context.WithTimeout(ctx, resource.deleteTimeout)

		// List resources with multiple possible label selectors
		labelSelectors := []map[string]string{
			{"test-name": testName},
			{"test": "e2e"},
		}

		resourcesDeleted := false
		for _, labels := range labelSelectors {
			err := s.kubeClient.List(ctx, resource.listObj, client.MatchingLabels(labels))
			if err != nil {
				t.Logf("Failed to list %s resources with labels %v: %v", resource.name, labels, err)
				continue
			}

			// Delete items using type assertions with timeout context
			switch list := resource.listObj.(type) {
			case *appsv1.DeploymentList:
				if len(list.Items) > 0 {
					for _, item := range list.Items {
						t.Logf("Deleting %s: %s", resource.name, item.Name)
						if err := s.kubeClient.Delete(deleteCtx, &item); err != nil && !errors.IsNotFound(err) {
							t.Logf("Failed to delete %s %s: %v", resource.name, item.Name, err)
						} else {
							resourcesDeleted = true
						}
					}
				}
			case *karpv1.NodeClaimList:
				if len(list.Items) > 0 {
					for _, item := range list.Items {
						t.Logf("Deleting %s: %s", resource.name, item.Name)
						if err := s.kubeClient.Delete(deleteCtx, &item); err != nil && !errors.IsNotFound(err) {
							t.Logf("Failed to delete %s %s: %v", resource.name, item.Name, err)
						} else {
							resourcesDeleted = true
						}
					}
				}
			case *karpv1.NodePoolList:
				if len(list.Items) > 0 {
					for _, item := range list.Items {
						t.Logf("Deleting %s: %s", resource.name, item.Name)
						if err := s.kubeClient.Delete(deleteCtx, &item); err != nil && !errors.IsNotFound(err) {
							t.Logf("Failed to delete %s %s: %v", resource.name, item.Name, err)
						} else {
							resourcesDeleted = true
						}
					}
				}
			case *v1alpha1.IBMNodeClassList:
				if len(list.Items) > 0 {
					for _, item := range list.Items {
						t.Logf("Deleting %s: %s", resource.name, item.Name)
						if err := s.kubeClient.Delete(deleteCtx, &item); err != nil && !errors.IsNotFound(err) {
							t.Logf("Failed to delete %s %s: %v", resource.name, item.Name, err)
						} else {
							resourcesDeleted = true
						}
					}
				}
			case *policyv1.PodDisruptionBudgetList:
				if len(list.Items) > 0 {
					for _, item := range list.Items {
						t.Logf("Deleting %s: %s", resource.name, item.Name)
						if err := s.kubeClient.Delete(deleteCtx, &item); err != nil && !errors.IsNotFound(err) {
							t.Logf("Failed to delete %s %s: %v", resource.name, item.Name, err)
						} else {
							resourcesDeleted = true
						}
					}
				}
			}
		}

		cancel() // Cancel the delete timeout context

		// If we deleted any resources, wait for them to be fully deleted
		if resourcesDeleted {
			// Wait for deletion with appropriate timeout for IBM Cloud resources
			success := s.waitForResourceDeletion(ctx, t, resource.name, resource.listObj, map[string]string{"test-name": testName}, resource.timeout)
			if !success {
				// Also check with "test": "e2e" label
				s.waitForResourceDeletion(ctx, t, resource.name, resource.listObj, map[string]string{"test": "e2e"}, resource.timeout/2)
			}
		}
	}

	t.Logf("✅ Cleanup completed for test %s", testName)
}

// cleanupFloatingIP removes a floating IP from IBM Cloud
func (s *E2ETestSuite) cleanupFloatingIP(t *testing.T, floatingIP string) {
	t.Logf("Cleaning up floating IP: %s", floatingIP)

	// Get floating IP details to find ID
	cmd := exec.Command("ibmcloud", "is", "floating-ips", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		t.Logf("Failed to list floating IPs for cleanup: %v", err)
		return
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, floatingIP) {
		t.Logf("Floating IP %s not found for cleanup", floatingIP)
		return
	}

	// Find the floating IP ID from the output
	lines := strings.Split(outputStr, "\n")
	var floatingIPID string
	for _, line := range lines {
		if strings.Contains(line, floatingIP) && strings.Contains(line, "\"id\"") {
			// Parse JSON to extract ID - simplified parsing
			parts := strings.Split(line, "\"")
			for i, part := range parts {
				if part == "id" && i+2 < len(parts) {
					floatingIPID = parts[i+2]
					break
				}
			}
			break
		}
	}

	if floatingIPID == "" {
		t.Logf("Could not find ID for floating IP %s", floatingIP)
		return
	}

	// Delete the floating IP
	deleteCmd := exec.Command("ibmcloud", "is", "floating-ip-delete", floatingIPID, "--force")
	if err := deleteCmd.Run(); err != nil {
		t.Logf("Failed to delete floating IP %s: %v", floatingIP, err)
		return
	}

	t.Logf("✅ Successfully cleaned up floating IP: %s", floatingIP)
}

// cleanupAllStaleResources performs aggressive cleanup of ALL E2E resources (stale and current)
func (s *E2ETestSuite) cleanupAllStaleResources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	t.Logf("🧹 Starting AGGRESSIVE cleanup of ALL E2E resources...")

	// Phase 1: Emergency cleanup - delete ALL IBMNodeClasses (they cause circuit breaker issues)
	t.Logf("Phase 1: Emergency IBMNodeClass cleanup...")
	var allNodeClasses v1alpha1.IBMNodeClassList
	if err := s.kubeClient.List(ctx, &allNodeClasses); err == nil {
		for _, nodeClass := range allNodeClasses.Items {
			if err := s.kubeClient.Delete(ctx, &nodeClass); err != nil && !errors.IsNotFound(err) {
				t.Logf("⚠️ Force deleting IBMNodeClass %s: %v", nodeClass.Name, err)
				// Force delete with finalizer removal if needed
				s.forceDeleteWithFinalizers(ctx, t, &nodeClass)
			} else {
				t.Logf("🗑️ Deleted IBMNodeClass: %s", nodeClass.Name)
			}
		}
	}

	// Phase 2: Standard resource cleanup with multiple label patterns
	labelPatterns := []map[string]string{
		{"test": "e2e"},
		{"created-by": "karpenter-e2e"},
		{"purpose": "e2e-verification"},
		{"purpose": "instance-type-test"},
		{"purpose": "nodepool-instancetype-test"},
		{"purpose": "karpenter-test"},
	}

	// Also clean by name patterns (for resources that might not have proper labels)
	namePatterns := []string{
		"e2e-test-",
		"drift-stability-",
		"instance-selection-",
		"nodepool-instance-selection-",
		"validation-test-",
		"valid-nodeclass-",
		"-nodeclass",
		"-nodepool",
		"-workload",
	}

	t.Logf("Phase 2: Systematic resource cleanup...")

	// Clean up in reverse dependency order (most dependent first)
	resources := []struct {
		name    string
		listObj client.ObjectList
		objType string
	}{
		{"Deployments", &appsv1.DeploymentList{}, "Deployment"},
		{"PodDisruptionBudgets", &policyv1.PodDisruptionBudgetList{}, "PDB"},
		{"NodeClaims", &karpv1.NodeClaimList{}, "NodeClaim"},
		{"NodePools", &karpv1.NodePoolList{}, "NodePool"},
		{"IBMNodeClasses", &v1alpha1.IBMNodeClassList{}, "IBMNodeClass"}, // Last cleanup
	}

	for _, resource := range resources {
		t.Logf("Cleaning %s...", resource.name)

		// Try cleanup by labels
		for _, labels := range labelPatterns {
			s.deleteResourcesByLabels(ctx, t, resource.listObj, labels, resource.objType)
		}

		// Try cleanup by name patterns
		s.deleteResourcesByNamePattern(ctx, t, resource.listObj, namePatterns, resource.objType)
	}

	t.Logf("✅ Aggressive cleanup completed")
}

// forceDeleteWithFinalizers removes finalizers and force deletes a resource
func (s *E2ETestSuite) forceDeleteWithFinalizers(ctx context.Context, t *testing.T, obj client.Object) {
	// Remove finalizers
	obj.SetFinalizers([]string{})
	if err := s.kubeClient.Update(ctx, obj); err != nil {
		t.Logf("Failed to remove finalizers from %s: %v", obj.GetName(), err)
	}

	// Force delete with zero grace period
	if err := s.kubeClient.Delete(ctx, obj, client.GracePeriodSeconds(0)); err != nil && !errors.IsNotFound(err) {
		t.Logf("Failed to force delete %s: %v", obj.GetName(), err)
	}
}

// deleteResourcesByLabels deletes resources matching label selectors
func (s *E2ETestSuite) deleteResourcesByLabels(ctx context.Context, t *testing.T, listObj client.ObjectList, labels map[string]string, resourceType string) {
	if err := s.kubeClient.List(ctx, listObj, client.MatchingLabels(labels)); err != nil {
		return
	}

	switch list := listObj.(type) {
	case *appsv1.DeploymentList:
		for _, item := range list.Items {
			// Skip resources in karpenter namespace to avoid deleting the controller
			if item.Namespace == "karpenter" {
				t.Logf("⏭️ Skipping %s in karpenter namespace: %s", resourceType, item.Name)
				continue
			}
			s.kubeClient.Delete(ctx, &item, client.GracePeriodSeconds(0))
			t.Logf("🗑️ Deleted %s: %s", resourceType, item.Name)
		}
	case *karpv1.NodeClaimList:
		for _, item := range list.Items {
			s.forceDeleteWithFinalizers(ctx, t, &item)
			t.Logf("🗑️ Force deleted %s: %s", resourceType, item.Name)
		}
	case *karpv1.NodePoolList:
		for _, item := range list.Items {
			s.forceDeleteWithFinalizers(ctx, t, &item)
			t.Logf("🗑️ Force deleted %s: %s", resourceType, item.Name)
		}
	case *v1alpha1.IBMNodeClassList:
		for _, item := range list.Items {
			s.forceDeleteWithFinalizers(ctx, t, &item)
			t.Logf("🗑️ Force deleted %s: %s", resourceType, item.Name)
		}
	case *policyv1.PodDisruptionBudgetList:
		for _, item := range list.Items {
			s.kubeClient.Delete(ctx, &item, client.GracePeriodSeconds(0))
			t.Logf("🗑️ Deleted %s: %s", resourceType, item.Name)
		}
	}
}

// deleteResourcesByNamePattern deletes resources matching name patterns
func (s *E2ETestSuite) deleteResourcesByNamePattern(ctx context.Context, t *testing.T, listObj client.ObjectList, patterns []string, resourceType string) {
	if err := s.kubeClient.List(ctx, listObj); err != nil {
		return
	}

	switch list := listObj.(type) {
	case *appsv1.DeploymentList:
		for _, item := range list.Items {
			// Skip resources in karpenter namespace to avoid deleting the controller
			if item.Namespace == "karpenter" {
				continue
			}
			if s.matchesAnyPattern(item.Name, patterns) {
				s.kubeClient.Delete(ctx, &item, client.GracePeriodSeconds(0))
				t.Logf("🗑️ Pattern-deleted %s: %s", resourceType, item.Name)
			}
		}
	case *karpv1.NodeClaimList:
		for _, item := range list.Items {
			if s.matchesAnyPattern(item.Name, patterns) {
				s.forceDeleteWithFinalizers(ctx, t, &item)
				t.Logf("🗑️ Pattern-deleted %s: %s", resourceType, item.Name)
			}
		}
	case *karpv1.NodePoolList:
		for _, item := range list.Items {
			if s.matchesAnyPattern(item.Name, patterns) {
				s.forceDeleteWithFinalizers(ctx, t, &item)
				t.Logf("🗑️ Pattern-deleted %s: %s", resourceType, item.Name)
			}
		}
	case *v1alpha1.IBMNodeClassList:
		for _, item := range list.Items {
			if s.matchesAnyPattern(item.Name, patterns) {
				s.forceDeleteWithFinalizers(ctx, t, &item)
				t.Logf("🗑️ Pattern-deleted %s: %s", resourceType, item.Name)
			}
		}
	case *policyv1.PodDisruptionBudgetList:
		for _, item := range list.Items {
			if s.matchesAnyPattern(item.Name, patterns) {
				s.kubeClient.Delete(ctx, &item, client.GracePeriodSeconds(0))
				t.Logf("🗑️ Pattern-deleted %s: %s", resourceType, item.Name)
			}
		}
	}
}

// matchesAnyPattern checks if a name matches any of the given patterns
func (s *E2ETestSuite) matchesAnyPattern(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if strings.Contains(name, pattern) {
			return true
		}
	}
	return false
}

// cleanupAllE2EResources performs a comprehensive cleanup of all E2E test resources
func (s *E2ETestSuite) cleanupAllE2EResources(t *testing.T) {
	ctx := context.Background()
	t.Logf("Starting comprehensive E2E resource cleanup")

	// Clean up all resources with the e2e test label
	err := s.kubeClient.List(ctx, &appsv1.DeploymentList{}, client.MatchingLabels{"test": "e2e"})
	if err == nil {
		var deploymentList appsv1.DeploymentList
		s.kubeClient.List(ctx, &deploymentList, client.MatchingLabels{"test": "e2e"})
		for _, deployment := range deploymentList.Items {
			s.kubeClient.Delete(ctx, &deployment)
			t.Logf("Deleted deployment: %s", deployment.Name)
		}
	}

	// Clean up NodeClaims
	var nodeClaimList karpv1.NodeClaimList
	err = s.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{"test": "e2e"})
	if err == nil {
		for _, nodeClaim := range nodeClaimList.Items {
			s.kubeClient.Delete(ctx, &nodeClaim)
			t.Logf("Deleted NodeClaim: %s", nodeClaim.Name)
		}
	}

	// Clean up NodePools
	var nodePoolList karpv1.NodePoolList
	err = s.kubeClient.List(ctx, &nodePoolList, client.MatchingLabels{"test": "e2e"})
	if err == nil {
		for _, nodePool := range nodePoolList.Items {
			s.kubeClient.Delete(ctx, &nodePool)
			t.Logf("Deleted NodePool: %s", nodePool.Name)
		}
	}

	// Clean up IBMNodeClasses
	var nodeClassList v1alpha1.IBMNodeClassList
	err = s.kubeClient.List(ctx, &nodeClassList, client.MatchingLabels{"test": "e2e"})
	if err == nil {
		for _, nodeClass := range nodeClassList.Items {
			s.kubeClient.Delete(ctx, &nodeClass)
			t.Logf("Deleted IBMNodeClass: %s", nodeClass.Name)
		}
	}

	// Clean up PodDisruptionBudgets
	var pdbList policyv1.PodDisruptionBudgetList
	err = s.kubeClient.List(ctx, &pdbList, client.MatchingLabels{"test": "e2e"})
	if err == nil {
		for _, pdb := range pdbList.Items {
			s.kubeClient.Delete(ctx, &pdb)
			t.Logf("Deleted PodDisruptionBudget: %s", pdb.Name)
		}
	}

	t.Logf("✅ Comprehensive E2E resource cleanup completed")
}

// cleanupOrphanedKubernetesResources removes resources that may be left behind after tests
func (s *E2ETestSuite) cleanupOrphanedKubernetesResources(t *testing.T) {
	ctx := context.Background()
	t.Logf("Cleaning up orphaned Kubernetes resources")

	// Find nodes without corresponding NodeClaims
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList, client.MatchingLabels{
		"karpenter.sh/nodepool": "",
	})
	if err != nil {
		t.Logf("Failed to list Karpenter nodes: %v", err)
		return
	}

	var nodeClaimList karpv1.NodeClaimList
	err = s.kubeClient.List(ctx, &nodeClaimList)
	if err != nil {
		t.Logf("Failed to list NodeClaims: %v", err)
		return
	}

	// Create a map of NodeClaim names to check for orphaned nodes
	nodeClaimNames := make(map[string]bool)
	for _, nodeClaim := range nodeClaimList.Items {
		if nodeClaim.Status.NodeName != "" {
			nodeClaimNames[nodeClaim.Status.NodeName] = true
		}
	}

	// Check for orphaned nodes
	orphanedNodes := 0
	for _, node := range nodeList.Items {
		if _, exists := node.Labels["karpenter.sh/nodepool"]; exists {
			if !nodeClaimNames[node.Name] {
				t.Logf("Found potentially orphaned node: %s", node.Name)
				orphanedNodes++
			}
		}
	}

	if orphanedNodes > 0 {
		t.Logf("Found %d potentially orphaned nodes", orphanedNodes)
	} else {
		t.Logf("✅ No orphaned nodes found")
	}

	// Clean up failed pods that might be stuck
	var podList corev1.PodList
	err = s.kubeClient.List(ctx, &podList, client.InNamespace("default"))
	if err != nil {
		t.Logf("Failed to list pods: %v", err)
		return
	}

	failedPods := 0
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodFailed && pod.Labels["test"] == "e2e" {
			err := s.kubeClient.Delete(ctx, &pod)
			if err != nil {
				t.Logf("Failed to delete failed pod %s: %v", pod.Name, err)
			} else {
				failedPods++
			}
		}
	}

	if failedPods > 0 {
		t.Logf("Cleaned up %d failed E2E pods", failedPods)
	}

	t.Logf("✅ Orphaned resource cleanup completed")
}

// WithAutoCleanup wraps a test function with automatic cleanup
// This ensures cleanup happens even if test panics or fails
func (s *E2ETestSuite) WithAutoCleanup(t *testing.T, testName string, testFunc func()) {
	// Track if test completed successfully
	completed := false

	// Setup defer cleanup that ALWAYS runs
	defer func() {
		if r := recover(); r != nil {
			t.Logf("🚨 TEST PANICKED: %s - performing emergency cleanup: %v", testName, r)
			s.emergencyCleanup(t, testName)
			panic(r) // Re-panic after cleanup
		}

		if !completed {
			t.Logf("🧹 Test failed or interrupted: %s - performing cleanup", testName)
		} else {
			t.Logf("🧹 Test completed: %s - performing cleanup", testName)
		}

		s.cleanupTestResources(t, testName)

		// Also check for any stale resources that might have been missed
		if os.Getenv("E2E_AGGRESSIVE_CLEANUP") == "true" {
			t.Logf("🧹 Aggressive cleanup mode - checking for stale resources")
			s.cleanupAllStaleResources(t)
		}
	}()

	// Run the actual test
	testFunc()
	completed = true
}

// emergencyCleanup performs immediate cleanup when test panics
func (s *E2ETestSuite) emergencyCleanup(t *testing.T, testName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	t.Logf("🚨 Emergency cleanup for test: %s", testName)

	// Immediate force delete of test resources
	resources := []struct {
		name    string
		listObj client.ObjectList
	}{
		{"IBMNodeClasses", &v1alpha1.IBMNodeClassList{}},
		{"NodeClaims", &karpv1.NodeClaimList{}},
		{"NodePools", &karpv1.NodePoolList{}},
		{"Deployments", &appsv1.DeploymentList{}},
	}

	for _, resource := range resources {
		if err := s.kubeClient.List(ctx, resource.listObj, client.MatchingLabels{"test-name": testName}); err != nil {
			continue
		}

		switch list := resource.listObj.(type) {
		case *v1alpha1.IBMNodeClassList:
			for _, item := range list.Items {
				s.forceDeleteWithFinalizers(ctx, t, &item)
				t.Logf("🗑️ Emergency deleted IBMNodeClass: %s", item.Name)
			}
		case *karpv1.NodeClaimList:
			for _, item := range list.Items {
				s.forceDeleteWithFinalizers(ctx, t, &item)
				t.Logf("🗑️ Emergency deleted NodeClaim: %s", item.Name)
			}
		case *karpv1.NodePoolList:
			for _, item := range list.Items {
				s.forceDeleteWithFinalizers(ctx, t, &item)
				t.Logf("🗑️ Emergency deleted NodePool: %s", item.Name)
			}
		case *appsv1.DeploymentList:
			for _, item := range list.Items {
				s.kubeClient.Delete(ctx, &item, client.GracePeriodSeconds(0))
				t.Logf("🗑️ Emergency deleted Deployment: %s", item.Name)
			}
		}
	}
}
