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

package orphancleanup

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/singleton"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/operator/injection"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

type Controller struct {
	kubeClient      client.Client
	ibmClient       *ibm.Client
	orphanTimeout   time.Duration
	successfulCount uint64
}

const (
	// DefaultOrphanTimeout is the time to wait before cleaning up orphaned nodes
	DefaultOrphanTimeout = 10 * time.Minute
	// OrphanCheckInterval is how often to check for orphaned nodes
	OrphanCheckInterval = 5 * time.Minute
	// MinimumOrphanTimeout is the minimum time to wait before cleanup (safety)
	MinimumOrphanTimeout = 5 * time.Minute
)

func NewController(kubeClient client.Client, ibmClient *ibm.Client) *Controller {
	return &Controller{
		kubeClient:      kubeClient,
		ibmClient:       ibmClient,
		orphanTimeout:   getOrphanTimeoutFromEnv(),
		successfulCount: 0,
	}
}

// getOrphanTimeoutFromEnv gets the orphan timeout from environment variable
func getOrphanTimeoutFromEnv() time.Duration {
	if envValue := os.Getenv("KARPENTER_ORPHAN_TIMEOUT_MINUTES"); envValue != "" {
		if minutes, err := strconv.Atoi(envValue); err == nil && minutes > 0 {
			timeout := time.Duration(minutes) * time.Minute
			// Enforce minimum timeout for safety
			if timeout < MinimumOrphanTimeout {
				timeout = MinimumOrphanTimeout
			}
			return timeout
		}
	}
	return DefaultOrphanTimeout
}

// isOrphanCleanupEnabled checks if orphan cleanup is enabled via environment variable
func isOrphanCleanupEnabled() bool {
	return os.Getenv("KARPENTER_ENABLE_ORPHAN_CLEANUP") == "true"
}

func (c *Controller) Reconcile(ctx context.Context) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, "node.orphancleanup.ibm")
	logger := log.FromContext(ctx)

	// Check if orphan cleanup is enabled
	if !isOrphanCleanupEnabled() {
		logger.V(1).Info("orphan cleanup is disabled, skipping")
		return reconcile.Result{RequeueAfter: OrphanCheckInterval}, nil
	}

	// Get all nodes that have IBM providerIDs
	nodeList := &corev1.NodeList{}
	if err := c.kubeClient.List(ctx, nodeList); err != nil {
		return reconcile.Result{}, fmt.Errorf("listing nodes: %w", err)
	}

	// Filter for IBM nodes managed by Karpenter only
	ibmNodes := lo.Filter(nodeList.Items, func(node corev1.Node, _ int) bool {
		// Only process nodes with IBM provider IDs
		if !strings.HasPrefix(node.Spec.ProviderID, "ibm://") {
			return false
		}

		// Only process nodes that are managed by Karpenter
		// Check for Karpenter-specific labels
		if nodeLabels := node.GetLabels(); nodeLabels != nil {
			// Must have either a karpenter.sh/nodepool or karpenter.sh/provisioner label
			if _, hasNodePool := nodeLabels["karpenter.sh/nodepool"]; hasNodePool {
				return true
			}
			if _, hasProvisioner := nodeLabels["karpenter.sh/provisioner"]; hasProvisioner {
				return true
			}
		}

		return false
	})

	if len(ibmNodes) == 0 {
		logger.V(1).Info("no IBM nodes found, skipping orphan cleanup")
		return reconcile.Result{RequeueAfter: OrphanCheckInterval}, nil
	}

	// Extract instance IDs from provider IDs
	instanceIDs := make([]string, 0, len(ibmNodes))
	nodeByInstanceID := make(map[string]corev1.Node)

	for _, node := range ibmNodes {
		instanceID := c.extractInstanceIDFromProviderID(node.Spec.ProviderID)
		if instanceID != "" {
			instanceIDs = append(instanceIDs, instanceID)
			nodeByInstanceID[instanceID] = node
		}
	}

	if len(instanceIDs) == 0 {
		logger.V(1).Info("no IBM nodes with valid provider IDs found")
		return reconcile.Result{RequeueAfter: OrphanCheckInterval}, nil
	}

	// Check which instances still exist in IBM Cloud
	existingInstances, err := c.getExistingVPCInstances(ctx, instanceIDs)
	if err != nil {
		logger.Error(err, "failed to check existing VPC instances")
		return reconcile.Result{}, err
	}

	existingInstanceIDs := sets.New(existingInstances...)

	// Find orphaned nodes (nodes whose instances no longer exist)
	orphanedNodes := make([]corev1.Node, 0)
	for instanceID, node := range nodeByInstanceID {
		if !existingInstanceIDs.Has(instanceID) {
			orphanedNodes = append(orphanedNodes, node)
		}
	}

	if len(orphanedNodes) == 0 {
		logger.V(1).Info("no orphaned nodes found")
		c.successfulCount++
		return reconcile.Result{RequeueAfter: OrphanCheckInterval}, nil
	}

	logger.Info("found orphaned nodes", "count", len(orphanedNodes))

	// Process orphaned nodes in parallel
	errs := make([]error, len(orphanedNodes))
	workqueue.ParallelizeUntil(ctx, 10, len(orphanedNodes), func(i int) {
		errs[i] = c.processOrphanedNode(ctx, orphanedNodes[i])
	})

	if err := multierr.Combine(errs...); err != nil {
		return reconcile.Result{}, err
	}

	c.successfulCount++
	return reconcile.Result{RequeueAfter: OrphanCheckInterval}, nil
}

// extractInstanceIDFromProviderID extracts the instance ID from an IBM provider ID
// Provider ID format: ibm:///region/instance-id
func (c *Controller) extractInstanceIDFromProviderID(providerID string) string {
	if !strings.HasPrefix(providerID, "ibm://") {
		return ""
	}

	// Remove "ibm://" prefix
	remaining := strings.TrimPrefix(providerID, "ibm://")

	// Split by "/" and get the last part (instance ID)
	parts := strings.Split(remaining, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-1] // Return the last part as instance ID
	}

	return ""
}

// getExistingVPCInstances checks which VPC instances still exist in IBM Cloud
func (c *Controller) getExistingVPCInstances(ctx context.Context, instanceIDs []string) ([]string, error) {
	logger := log.FromContext(ctx)

	if c.ibmClient == nil {
		logger.V(1).Info("IBM client is not initialized, skipping instance existence check")
		return instanceIDs, nil // Return all instances as existing to avoid cleanup
	}

	// For VPC instances, we need to list all instances and check which ones exist
	existingInstances := make([]string, 0)

	for _, instanceID := range instanceIDs {
		exists, err := c.ibmClient.VPCInstanceExists(ctx, instanceID)
		if err != nil {
			logger.Error(err, "failed to check if VPC instance exists", "instance-id", instanceID)
			continue
		}

		if exists {
			existingInstances = append(existingInstances, instanceID)
		}
	}

	return existingInstances, nil
}

// processOrphanedNode handles cleanup of a single orphaned node
func (c *Controller) processOrphanedNode(ctx context.Context, node corev1.Node) error {
	logger := log.FromContext(ctx).WithValues(
		"node", node.Name,
		"provider-id", node.Spec.ProviderID,
	)

	// Safety check: verify this node is managed by Karpenter
	if !c.isNodeManagedByKarpenter(node) {
		logger.V(1).Info("node not managed by Karpenter, skipping cleanup")
		return nil
	}

	// Check if the node has been in NotReady state for long enough
	if !c.isNodeOrphanedLongEnough(node) {
		logger.V(1).Info("node not orphaned long enough, skipping cleanup")
		return nil
	}

	// Additional safety check: verify the instance really doesn't exist
	instanceID := c.extractInstanceIDFromProviderID(node.Spec.ProviderID)
	if instanceID == "" {
		logger.V(1).Info("unable to extract instance ID from provider ID, skipping cleanup")
		return nil
	}

	// Double-check that the instance doesn't exist
	exists, err := c.ibmClient.VPCInstanceExists(ctx, instanceID)
	if err != nil {
		logger.Error(err, "failed to verify instance existence, skipping cleanup for safety")
		return nil
	}

	if exists {
		logger.V(1).Info("instance still exists, node is not orphaned, skipping cleanup")
		return nil
	}

	logger.Info("cleaning up orphaned node")

	// Step 1: Cordon the node to prevent new pods from being scheduled
	if err := c.cordonNode(ctx, &node); err != nil {
		logger.Error(err, "failed to cordon orphaned node")
		// Continue with cleanup even if cordoning fails
	}

	// Step 2: Force delete any pods still running on the node
	if err := c.forceDeletePodsOnNode(ctx, node.Name); err != nil {
		logger.Error(err, "failed to force delete pods on orphaned node")
		// Continue with cleanup even if pod deletion fails
	}

	// Step 3: Delete the node object
	if err := c.kubeClient.Delete(ctx, &node, &client.DeleteOptions{
		GracePeriodSeconds: lo.ToPtr(int64(0)),
	}); err != nil {
		return fmt.Errorf("deleting orphaned node: %w", err)
	}

	logger.Info("successfully cleaned up orphaned node")
	return nil
}

// isNodeManagedByKarpenter checks if a node is managed by Karpenter
func (c *Controller) isNodeManagedByKarpenter(node corev1.Node) bool {
	if nodeLabels := node.GetLabels(); nodeLabels != nil {
		// Check for Karpenter-specific labels
		if _, hasNodePool := nodeLabels["karpenter.sh/nodepool"]; hasNodePool {
			return true
		}
		if _, hasProvisioner := nodeLabels["karpenter.sh/provisioner"]; hasProvisioner {
			return true
		}
		// Additional check for IBM-specific Karpenter labels
		if _, hasIBMNodeClass := nodeLabels["karpenter.ibm.sh/ibmnodeclass"]; hasIBMNodeClass {
			return true
		}
	}

	// Also check annotations for additional safety
	if nodeAnnotations := node.GetAnnotations(); nodeAnnotations != nil {
		// Check for Karpenter-specific annotations
		if _, hasKarpenterManaged := nodeAnnotations["karpenter.sh/managed"]; hasKarpenterManaged {
			return true
		}
		if _, hasKarpenterInitialized := nodeAnnotations["karpenter.sh/initialized"]; hasKarpenterInitialized {
			return true
		}
	}

	return false
}

// isNodeOrphanedLongEnough checks if a node has been in NotReady state long enough to be considered orphaned
func (c *Controller) isNodeOrphanedLongEnough(node corev1.Node) bool {
	now := time.Now()

	// Check if node is NotReady
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			if condition.Status == corev1.ConditionFalse || condition.Status == corev1.ConditionUnknown {
				// Node is NotReady, check how long it's been in this state
				timeSinceLastTransition := now.Sub(condition.LastTransitionTime.Time)
				return timeSinceLastTransition >= c.orphanTimeout
			}
			// Node is Ready, not orphaned
			return false
		}
	}

	// If we can't determine the ready state, check node age
	nodeAge := now.Sub(node.CreationTimestamp.Time)
	return nodeAge >= c.orphanTimeout
}

// cordonNode marks a node as unschedulable
func (c *Controller) cordonNode(ctx context.Context, node *corev1.Node) error {
	if node.Spec.Unschedulable {
		return nil // Already cordoned
	}

	patch := client.MergeFrom(node.DeepCopy())
	node.Spec.Unschedulable = true

	return c.kubeClient.Patch(ctx, node, patch)
}

// forceDeletePodsOnNode force deletes all pods on a given node
func (c *Controller) forceDeletePodsOnNode(ctx context.Context, nodeName string) error {
	logger := log.FromContext(ctx)

	podList := &corev1.PodList{}
	if err := c.kubeClient.List(ctx, podList, client.MatchingFields{"spec.nodeName": nodeName}); err != nil {
		return fmt.Errorf("listing pods on node %s: %w", nodeName, err)
	}

	for _, pod := range podList.Items {
		// Skip pods that are already terminating
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}

		// Force delete the pod
		if err := c.kubeClient.Delete(ctx, &pod, &client.DeleteOptions{
			GracePeriodSeconds: lo.ToPtr(int64(0)),
		}); err != nil {
			logger.Error(err, "failed to force delete pod", "pod", pod.Name, "namespace", pod.Namespace)
		}
	}

	return nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("node.orphancleanup.ibm").
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}
