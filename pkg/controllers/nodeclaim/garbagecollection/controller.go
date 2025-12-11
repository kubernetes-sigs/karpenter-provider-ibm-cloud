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

package garbagecollection

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/operator/injection"
	nodeclaimutils "sigs.k8s.io/karpenter/pkg/utils/nodeclaim"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

type Controller struct {
	kubeClient          client.Client
	cloudProvider       cloudprovider.CloudProvider
	successfulCount     uint64 // keeps track of successful reconciles for more aggressive requeueing near the start of the controller
	terminationTimeout  time.Duration
	registrationTimeout time.Duration
}

const (
	// DefaultTerminationTimeout is the maximum time to wait for graceful termination
	DefaultTerminationTimeout = 10 * time.Minute
	// StuckResourceCheckInterval is how often to check for stuck terminating resources
	StuckResourceCheckInterval = 5 * time.Minute
	// DefaultRegistrationTimeout is the maximum time to wait for node registration before cleanup
	DefaultRegistrationTimeout = 30 * time.Minute
)

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider) *Controller {
	return &Controller{
		kubeClient:          kubeClient,
		cloudProvider:       cloudProvider,
		successfulCount:     0,
		terminationTimeout:  DefaultTerminationTimeout,
		registrationTimeout: getRegistrationTimeoutFromEnv(),
	}
}

// getRegistrationTimeoutFromEnv gets the registration timeout from environment variable
func getRegistrationTimeoutFromEnv() time.Duration {
	if envValue := os.Getenv("KARPENTER_REGISTRATION_TIMEOUT_MINUTES"); envValue != "" {
		if minutes, err := strconv.Atoi(envValue); err == nil && minutes > 0 {
			return time.Duration(minutes) * time.Minute
		}
	}
	return DefaultRegistrationTimeout
}

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	ctx = injection.WithControllerName(ctx, "nodeclaim.garbagecollection.ibm")

	// We LIST NodeClaims on the CloudProvider BEFORE we grab NodeClaims/Nodes on the cluster so that we make sure that, if
	// LISTing cloudNodeClaims takes a long time, our information is more updated by the time we get to Node and NodeClaim LIST
	// This works since our CloudProvider cloudNodeClaims are deleted based on whether the Machine exists or not, not vise-versa
	cloudNodeClaims, err := c.cloudProvider.List(ctx)
	if err != nil {
		return reconciler.Result{}, fmt.Errorf("listing cloudprovider nodeclaims, %w", err)
	}
	// Filter out any cloudprovider NodeClaim which is already terminating
	cloudNodeClaims = lo.Filter(cloudNodeClaims, func(nc *karpv1.NodeClaim, _ int) bool {
		return nc.DeletionTimestamp.IsZero()
	})
	clusterNodeClaims, err := nodeclaimutils.ListManaged(ctx, c.kubeClient, c.cloudProvider)
	if err != nil {
		return reconciler.Result{}, err
	}
	// Create normalized provider ID sets for comparison
	normalizedClusterProviderIDs := sets.New(lo.FilterMap(clusterNodeClaims, func(nc *karpv1.NodeClaim, _ int) (string, bool) {
		if nc.Status.ProviderID != "" {
			return c.normalizeProviderID(nc.Status.ProviderID), true
		}
		return "", false
	})...)

	nodeList := &corev1.NodeList{}
	if err = c.kubeClient.List(ctx, nodeList); err != nil {
		return reconciler.Result{}, err
	}
	// Check for stuck terminating NodeClaims and force cleanup if needed
	if stuckErr := c.handleStuckTerminatingNodeClaims(ctx, clusterNodeClaims); stuckErr != nil {
		log.FromContext(ctx).Error(stuckErr, "failed to handle stuck terminating nodeclaims")
	}

	// Check for NodeClaims that failed to register and clean them up after timeout
	if registrationErr := c.handleFailedRegistrationNodeClaims(ctx, clusterNodeClaims); registrationErr != nil {
		log.FromContext(ctx).Error(registrationErr, "failed to handle failed registration nodeclaims")
	}

	// Handle orphaned cloud instances (AWS-style garbage collection)
	errs := make([]error, len(cloudNodeClaims))
	workqueue.ParallelizeUntil(ctx, 100, len(cloudNodeClaims), func(i int) {
		if nc := cloudNodeClaims[i]; nc.Status.ProviderID != "" && time.Since(nc.CreationTimestamp.Time) > time.Second*30 {
			normalizedCloudID := c.normalizeProviderID(nc.Status.ProviderID)
			if !normalizedClusterProviderIDs.Has(normalizedCloudID) {
				// Check if the corresponding node is managed by Karpenter before garbage collecting.
				// Nodes without Karpenter labels (e.g., IKS-managed workers) should not be garbage collected.
				matchingNode, found := lo.Find(nodeList.Items, func(n corev1.Node) bool {
					return c.normalizeProviderID(n.Spec.ProviderID) == normalizedCloudID
				})
				if found {
					// Skip nodes that don't have Karpenter labels
					_, hasNodePoolLabel := matchingNode.Labels[karpv1.NodePoolLabelKey]
					_, hasNodeClassLabel := matchingNode.Labels["karpenter-ibm.sh/ibmnodeclass"]
					if !hasNodePoolLabel && !hasNodeClassLabel {
						log.FromContext(ctx).V(1).WithValues(
							"provider-id", nc.Status.ProviderID,
							"node", matchingNode.Name,
						).Info("skipping garbage collection for non-Karpenter-managed node")
						return
					}
				}
				log.FromContext(ctx).WithValues(
					"provider-id", nc.Status.ProviderID,
					"normalized-id", normalizedCloudID,
					"nodeclaim", nc.Name,
					"age", time.Since(nc.CreationTimestamp.Time),
				).Info("garbage collecting orphaned cloud instance (no matching cluster NodeClaim)")
				errs[i] = c.garbageCollect(ctx, cloudNodeClaims[i], nodeList)
			}
		}
	})

	// Handle orphaned Kubernetes nodes (nodes without corresponding cloud instances)
	if orphanedNodeErr := c.handleOrphanedNodes(ctx, nodeList, cloudNodeClaims); orphanedNodeErr != nil {
		log.FromContext(ctx).Error(orphanedNodeErr, "failed to handle orphaned kubernetes nodes")
	}

	// Filter out expected errors
	filteredErrs := lo.Filter(errs, func(err error, _ int) bool {
		if err == nil {
			return false
		}
		// Ignore "nodeclaim not found" errors as these are expected for orphaned instances
		if cloudprovider.IsNodeClaimNotFoundError(err) {
			return false
		}
		return true
	})

	if err = multierr.Combine(filteredErrs...); err != nil {
		return reconciler.Result{}, err
	}
	c.successfulCount++
	return reconciler.Result{RequeueAfter: lo.Ternary(c.successfulCount <= 20, time.Second*10, time.Minute*2)}, nil
}

// handleStuckTerminatingNodeClaims identifies and force-cleans stuck terminating NodeClaims
func (c *Controller) handleStuckTerminatingNodeClaims(ctx context.Context, clusterNodeClaims []*karpv1.NodeClaim) error {
	now := time.Now()
	var errs []error

	for _, nc := range clusterNodeClaims {
		// Only process NodeClaims that are terminating
		if nc.DeletionTimestamp.IsZero() {
			continue
		}

		// Check if termination has been stuck for too long
		terminationDuration := now.Sub(nc.DeletionTimestamp.Time)
		if terminationDuration < c.terminationTimeout {
			continue
		}

		// Log stuck termination
		ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues(
			"nodeclaim", nc.Name,
			"provider-id", nc.Status.ProviderID,
			"termination-duration", terminationDuration,
		))
		log.FromContext(ctx).Info("detected stuck terminating nodeclaim, attempting force cleanup")

		// Attempt to force cleanup
		if err := c.forceCleanupStuckNodeClaim(ctx, nc); err != nil {
			errs = append(errs, fmt.Errorf("failed to force cleanup stuck nodeclaim %s: %w", nc.Name, err))
			continue
		}

		log.FromContext(ctx).Info("successfully force cleaned stuck terminating nodeclaim")
	}

	return multierr.Combine(errs...)
}

// handleOrphanedNodes identifies and cleans up Kubernetes nodes without corresponding cloud instances
func (c *Controller) handleOrphanedNodes(ctx context.Context, nodeList *corev1.NodeList, cloudNodeClaims []*karpv1.NodeClaim) error {
	// Create a map of normalized cloud instance provider IDs for fast lookup
	// Map from normalized ID to original ID
	normalizedCloudProviderIDs := make(map[string]string)
	for _, nc := range cloudNodeClaims {
		if nc.Status.ProviderID != "" {
			normalized := c.normalizeProviderID(nc.Status.ProviderID)
			normalizedCloudProviderIDs[normalized] = nc.Status.ProviderID
		}
	}

	var errs []error

	for _, node := range nodeList.Items {
		// Skip nodes that don't have provider IDs
		if node.Spec.ProviderID == "" {
			continue
		}

		// Skip nodes that aren't managed by Karpenter
		if !c.isKarpenterManagedNode(&node) {
			continue
		}

		// Check if this node has a corresponding cloud instance using normalized IDs
		normalizedNodeID := c.normalizeProviderID(node.Spec.ProviderID)
		if _, exists := normalizedCloudProviderIDs[normalizedNodeID]; exists {
			continue // Node has corresponding cloud instance, skip
		}

		// This is an orphaned node - Kubernetes node exists but no cloud instance
		ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues(
			"node", node.Name,
			"provider-id", node.Spec.ProviderID,
		))

		log.FromContext(ctx).Info("found orphaned kubernetes node (no corresponding cloud instance), cleaning up")

		// Step 1: Remove finalizers to ensure proper cleanup
		if err := c.removeNodeFinalizers(ctx, &node); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove finalizers from orphaned node %s: %w", node.Name, err))
			continue
		}

		// Step 2: Delete the orphaned node with proper grace period
		if err := c.kubeClient.Delete(ctx, &node, &client.DeleteOptions{
			GracePeriodSeconds: lo.ToPtr(int64(30)), // 30 second grace period
		}); err != nil && client.IgnoreNotFound(err) != nil {
			errs = append(errs, fmt.Errorf("failed to delete orphaned node %s: %w", node.Name, err))
			continue
		}

		log.FromContext(ctx).Info("successfully cleaned up orphaned kubernetes node")
	}

	return multierr.Combine(errs...)
}

// removeNodeFinalizers removes all finalizers from a node to ensure proper cleanup
func (c *Controller) removeNodeFinalizers(ctx context.Context, node *corev1.Node) error {
	// Skip if node has no finalizers
	if len(node.Finalizers) == 0 {
		return nil
	}

	// Get fresh copy of the node to avoid conflicts
	freshNode := &corev1.Node{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: node.Name}, freshNode); err != nil {
		return client.IgnoreNotFound(err)
	}

	// Remove all finalizers
	if len(freshNode.Finalizers) > 0 {
		finalizerCount := len(freshNode.Finalizers)
		patch := client.MergeFrom(freshNode.DeepCopy())
		freshNode.Finalizers = nil
		if err := c.kubeClient.Patch(ctx, freshNode, patch); err != nil {
			return fmt.Errorf("removing finalizers from node: %w", err)
		}
		log.FromContext(ctx).Info("removed finalizers from orphaned node", "finalizer-count", finalizerCount)
	}

	return nil
}

// isKarpenterManagedNode checks if a node is managed by Karpenter
func (c *Controller) isKarpenterManagedNode(node *corev1.Node) bool {
	// Check for Karpenter-specific labels that indicate management.
	// Only nodes with these labels are considered managed by Karpenter.
	// This excludes IKS-managed worker nodes which have ibm:// providerIDs but no Karpenter labels.
	if _, hasNodePool := node.Labels[karpv1.NodePoolLabelKey]; hasNodePool {
		return true
	}
	if _, hasNodeClass := node.Labels["karpenter-ibm.sh/ibmnodeclass"]; hasNodeClass {
		return true
	}

	// Default to not managed to be safe
	return false
}

// handleFailedRegistrationNodeClaims identifies and cleans up NodeClaims that failed to register after timeout
func (c *Controller) handleFailedRegistrationNodeClaims(ctx context.Context, clusterNodeClaims []*karpv1.NodeClaim) error {
	now := time.Now()
	var errs []error

	for _, nc := range clusterNodeClaims {
		// Skip NodeClaims that are already terminating
		if !nc.DeletionTimestamp.IsZero() {
			continue
		}

		// Skip NodeClaims that have successfully registered (have a node)
		if nc.Status.NodeName != "" {
			continue
		}

		// Check if NodeClaim is stuck in Unknown state or has been pending too long
		registrationDuration := now.Sub(nc.CreationTimestamp.Time)
		if registrationDuration < c.registrationTimeout {
			continue
		}

		// Determine if this NodeClaim failed to register
		failedRegistration := false

		// Check for Unknown ready condition
		for _, condition := range nc.Status.Conditions {
			if condition.Type == "Ready" {
				if condition.Status == metav1.ConditionUnknown {
					failedRegistration = true
					break
				}
			}
		}

		// Also handle NodeClaims that have no ready condition but are old enough
		if len(nc.Status.Conditions) == 0 && registrationDuration > c.registrationTimeout {
			failedRegistration = true
		}

		if !failedRegistration {
			continue
		}

		// Log failed registration cleanup
		ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues(
			"nodeclaim", nc.Name,
			"provider-id", nc.Status.ProviderID,
			"registration-duration", registrationDuration,
		))
		log.FromContext(ctx).Info("detected failed registration nodeclaim, cleaning up after timeout")

		// Clean up the failed NodeClaim
		if err := c.cleanupFailedRegistrationNodeClaim(ctx, nc); err != nil {
			errs = append(errs, fmt.Errorf("failed to cleanup failed registration nodeclaim %s: %w", nc.Name, err))
			continue
		}

		log.FromContext(ctx).Info("successfully cleaned up failed registration nodeclaim")
	}

	return multierr.Combine(errs...)
}

// cleanupFailedRegistrationNodeClaim performs cleanup of a NodeClaim that failed to register
func (c *Controller) cleanupFailedRegistrationNodeClaim(ctx context.Context, nc *karpv1.NodeClaim) error {
	// Step 1: Delete the cloud provider instance
	if err := c.cloudProvider.Delete(ctx, nc); err != nil {
		// Log but don't fail - instance might already be gone
		log.FromContext(ctx).Error(err, "failed to delete cloud provider instance for failed registration")
	}

	// Step 2: Remove finalizers to allow NodeClaim deletion
	if len(nc.Finalizers) > 0 {
		patch := client.MergeFrom(nc.DeepCopy())
		nc.Finalizers = nil
		if err := c.kubeClient.Patch(ctx, nc, patch); err != nil {
			return fmt.Errorf("removing finalizers from failed registration nodeclaim: %w", err)
		}
	}

	return nil
}

// forceCleanupStuckNodeClaim performs force cleanup of a stuck NodeClaim
func (c *Controller) forceCleanupStuckNodeClaim(ctx context.Context, nc *karpv1.NodeClaim) error {
	// Step 1: Force delete any stuck pods on the node
	if nc.Status.NodeName != "" {
		if err := c.forceDeletePodsOnNode(ctx, nc.Status.NodeName); err != nil {
			log.FromContext(ctx).Error(err, "failed to force delete pods on node", "node", nc.Status.NodeName)
		}
	}

	// Step 2: Force delete the node
	if nc.Status.NodeName != "" {
		node := &corev1.Node{}
		if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: nc.Status.NodeName}, node); err == nil {
			// Remove finalizers first
			if err := c.removeNodeFinalizers(ctx, node); err != nil {
				log.FromContext(ctx).Error(err, "failed to remove finalizers from node", "node", nc.Status.NodeName)
			}

			if err := c.kubeClient.Delete(ctx, node, &client.DeleteOptions{
				GracePeriodSeconds: lo.ToPtr(int64(0)),
			}); err != nil {
				log.FromContext(ctx).Error(err, "failed to force delete node", "node", nc.Status.NodeName)
			}
		}
	}

	// Step 3: Force delete the cloud provider instance
	if err := c.cloudProvider.Delete(ctx, nc); err != nil {
		// Log but don't fail - instance might already be gone
		log.FromContext(ctx).Error(err, "failed to delete cloud provider instance")
	}

	// Step 4: Remove finalizers to allow NodeClaim deletion
	if len(nc.Finalizers) > 0 {
		patch := client.MergeFrom(nc.DeepCopy())
		nc.Finalizers = nil
		if err := c.kubeClient.Patch(ctx, nc, patch); err != nil {
			return fmt.Errorf("removing finalizers from stuck nodeclaim: %w", err)
		}
	}

	return nil
}

// forceDeletePodsOnNode force deletes all pods on a given node
func (c *Controller) forceDeletePodsOnNode(ctx context.Context, nodeName string) error {
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
			log.FromContext(ctx).Error(err, "failed to force delete pod", "pod", pod.Name, "namespace", pod.Namespace)
		}
	}

	return nil
}

func (c *Controller) garbageCollect(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeList *corev1.NodeList) error {
	ctx = log.IntoContext(ctx, log.FromContext(ctx).WithValues("provider-id", nodeClaim.Status.ProviderID))

	// Step 1: Delete the cloud provider instance
	if err := c.cloudProvider.Delete(ctx, nodeClaim); err != nil {
		// ignore NodeClaim not found errors
		if cloudprovider.IsNodeClaimNotFoundError(err) {
			log.FromContext(ctx).V(1).Info("cloud instance already deleted or not found, continuing with node cleanup")
		} else {
			return fmt.Errorf("failed to delete cloud provider instance: %w", err)
		}
	} else {
		log.FromContext(ctx).V(1).Info("garbage collected cloudprovider instance")
	}

	// Step 2: Find and delete the corresponding Kubernetes node
	if node, ok := lo.Find(nodeList.Items, func(n corev1.Node) bool {
		return n.Spec.ProviderID == nodeClaim.Status.ProviderID
	}); ok {
		// Remove finalizers first
		if err := c.removeNodeFinalizers(ctx, &node); err != nil {
			log.FromContext(ctx).Error(err, "failed to remove finalizers from node", "node", node.Name)
		}

		if err := c.kubeClient.Delete(ctx, &node); err != nil {
			// ignore not found errors
			if client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("failed to delete corresponding node %s: %w", node.Name, err)
			}
		}
		log.FromContext(ctx).WithValues("Node", klog.KObj(&node)).V(1).Info("garbage collected node")
	} else {
		log.FromContext(ctx).V(1).Info("no corresponding kubernetes node found for garbage collection")
	}

	return nil
}

// normalizeProviderID normalizes IBM Cloud provider IDs to handle format differences
// between node registration and cloud API responses
func (c *Controller) normalizeProviderID(providerID string) string {
	// Extract the actual instance ID part from the provider ID
	// Format: ibm:///region/[zone_prefix_]instance-id
	parts := strings.Split(providerID, "/")
	if len(parts) < 4 {
		return providerID // Return as-is if format is unexpected
	}

	region := parts[len(parts)-2]
	instanceID := parts[len(parts)-1]

	// Remove zone prefix if present (e.g., "02u7_" from "02u7_abc123")
	// This handles cases where cloud API returns zone-prefixed IDs but node registration doesn't
	if strings.Contains(instanceID, "_") {
		instanceIDParts := strings.Split(instanceID, "_")
		if len(instanceIDParts) >= 2 {
			instanceID = strings.Join(instanceIDParts[1:], "_")
		}
	}

	// Return normalized format: ibm:///region/instance-id
	return fmt.Sprintf("ibm:///%s/%s", region, instanceID)
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("nodeclaim.garbagecollection.ibm").
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}
