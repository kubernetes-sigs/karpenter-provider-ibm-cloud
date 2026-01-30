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

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/awslabs/operatorpkg/reconciler"
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
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/operator/injection"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/globaltaggingv1"
	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

type GlobalTaggingAPI interface {
	ListTagsWithContext(ctx context.Context, listTagsOptions *globaltaggingv1.ListTagsOptions) (result *globaltaggingv1.TagList, response *core.DetailedResponse, err error)
}

type Controller struct {
	kubeClient      client.Client
	ibmClient       *ibm.Client
	globalTagging   GlobalTaggingAPI
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
	// Initialize Global Tagging client only if orphan cleanup is enabled
	var globalTaggingClient *globaltaggingv1.GlobalTaggingV1
	if isOrphanCleanupEnabled() {
		if apiKey := os.Getenv("IBMCLOUD_API_KEY"); apiKey != "" {
			authenticator := &core.IamAuthenticator{
				ApiKey: apiKey,
			}
			globalTaggingOptions := &globaltaggingv1.GlobalTaggingV1Options{
				Authenticator: authenticator,
			}
			if client, err := globaltaggingv1.NewGlobalTaggingV1(globalTaggingOptions); err == nil {
				globalTaggingClient = client
			}
			// Note: we don't fail if Global Tagging client creation fails,
			// orphan cleanup will be disabled if the API is not available
		}
	}

	return &Controller{
		kubeClient:      kubeClient,
		ibmClient:       ibmClient,
		globalTagging:   globalTaggingClient,
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

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	ctx = injection.WithControllerName(ctx, "node.orphancleanup.ibm")
	logger := log.FromContext(ctx)

	// Check if orphan cleanup is enabled
	if !isOrphanCleanupEnabled() {
		logger.V(1).Info("Skipping as orphan cleanup is disabled")
		return reconciler.Result{RequeueAfter: OrphanCheckInterval}, nil
	}

	// Get all nodes that have IBM providerIDs
	nodeList := &corev1.NodeList{}
	if err := c.kubeClient.List(ctx, nodeList); err != nil {
		return reconciler.Result{}, fmt.Errorf("listing nodes: %w", err)
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

	// Extract instance IDs from provider IDs of existing Karpenter nodes
	instanceIDs := make([]string, 0, len(ibmNodes))
	nodeByInstanceID := make(map[string]corev1.Node)

	for _, node := range ibmNodes {
		instanceID := c.extractInstanceIDFromProviderID(node.Spec.ProviderID)
		if instanceID != "" {
			instanceIDs = append(instanceIDs, instanceID)
			nodeByInstanceID[instanceID] = node
		}
	}

	// Always check for orphaned instances, even if there are no current Karpenter nodes
	// This handles cases where all NodeClaims were deleted but instances remain
	logger.V(1).Info("Checking for orphaned instances", "managedInstances", len(instanceIDs))

	// Get all instances from IBM Cloud to check for orphans
	allInstancesMap, err := c.getAllVPCInstances(ctx)
	if err != nil {
		logger.Error(err, "Failed to get all VPC instances")
		return reconciler.Result{}, err
	}

	if len(allInstancesMap) == 0 {
		logger.V(1).Info("Requeuing scan as no VPC instances were found")
		return reconciler.Result{RequeueAfter: OrphanCheckInterval}, nil
	}

	// Get all NodeClaims to check which instances should exist
	var nodeClaims karpv1.NodeClaimList
	if err := c.kubeClient.List(ctx, &nodeClaims); err != nil {
		logger.Error(err, "Failed to list NodeClaims")
		return reconciler.Result{}, err
	}

	// Collect instance IDs that should exist (from NodeClaims and existing nodes)
	expectedInstanceIDs := sets.New(instanceIDs...)
	for _, nodeClaim := range nodeClaims.Items {
		if nodeClaim.Status.ProviderID != "" {
			instanceID := c.extractInstanceIDFromProviderID(nodeClaim.Status.ProviderID)
			if instanceID != "" {
				expectedInstanceIDs.Insert(instanceID)
			}
		}
	}

	// Find orphaned instances (instances that exist in IBM Cloud but have no NodeClaim or Node)
	// Only proceed if we have the Global Tagging API available for reliable identification
	orphanedInstanceIDs := make([]string, 0)
	if c.globalTagging != nil {
		for instanceID, instance := range allInstancesMap {
			if !expectedInstanceIDs.Has(instanceID) {
				// Check if this instance was created by Karpenter by checking tags
				// This is the primary method for identifying Karpenter-managed instances
				if c.hasKarpenterTags(ctx, instance.CRN, instanceID, logger) {
					logger.V(1).Info("Identifying a Karpenter-managed orphaned instance", "instance-id", instanceID)
					orphanedInstanceIDs = append(orphanedInstanceIDs, instanceID)
				} else {
					logger.V(2).Info("Instance not managed by Karpenter, skipping", "instance-id", instanceID)
				}
			}
		}
	} else {
		logger.V(1).Info("Global Tagging API not available, Skipping orphaned instance cleanup")
	}

	if len(orphanedInstanceIDs) == 0 {
		logger.V(1).Info("Requeuing scan as no orphaned instances were found")
		c.successfulCount++
		return reconciler.Result{RequeueAfter: OrphanCheckInterval}, nil
	}

	logger.Info("Found orphaned instances", "count", len(orphanedInstanceIDs))

	// Handle both orphaned nodes (nodes without instances) and orphaned instances (instances without nodes/nodeclaims)
	var allErrors []error

	// First, handle orphaned nodes (original logic)
	orphanedNodes := make([]corev1.Node, 0)
	for instanceID, node := range nodeByInstanceID {
		if _, ok := allInstancesMap[instanceID]; !ok {
			orphanedNodes = append(orphanedNodes, node)
		}
	}

	if len(orphanedNodes) > 0 {
		logger.Info("Found orphaned nodes (nodes without instances)", "count", len(orphanedNodes))
		nodeErrs := make([]error, len(orphanedNodes))
		workqueue.ParallelizeUntil(ctx, 10, len(orphanedNodes), func(i int) {
			nodeErrs[i] = c.processOrphanedNode(ctx, orphanedNodes[i])
		})
		allErrors = append(allErrors, nodeErrs...)
	}

	// Second, handle orphaned instances (instances without nodes/nodeclaims)
	if len(orphanedInstanceIDs) > 0 {
		logger.Info("Initiated cleanup of orphaned instances (instances without nodes/nodeclaims)", "count", len(orphanedInstanceIDs))
		instanceErrs := make([]error, len(orphanedInstanceIDs))
		workqueue.ParallelizeUntil(ctx, 10, len(orphanedInstanceIDs), func(i int) {
			instanceErrs[i] = c.processOrphanedInstance(ctx, orphanedInstanceIDs[i])
		})
		allErrors = append(allErrors, instanceErrs...)
	}

	if err := multierr.Combine(allErrors...); err != nil {
		return reconciler.Result{}, err
	}

	c.successfulCount++
	return reconciler.Result{RequeueAfter: OrphanCheckInterval}, nil
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

// getAllVPCInstances gets all VPC instances to check for orphans
func (c *Controller) getAllVPCInstances(ctx context.Context) (map[string]vpcv1.Instance, error) {
	logger := log.FromContext(ctx)

	if c.ibmClient == nil {
		logger.V(1).Info("IBM client is not initialized, Skipping all instances check")
		return nil, nil
	}

	// Get VPC client to list all instances
	// Handle potential panic from GetVPCClient when ibmClient is not properly initialized
	var vpcClient *ibm.VPCClient
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic in GetVPCClient: %v", r)
			}
		}()
		vpcClient, err = c.ibmClient.GetVPCClient(ctx)
	}()

	if err != nil {
		logger.Error(err, "Failed to get VPC client")
		return nil, nil // Return empty list rather than error to allow graceful degradation
	}

	if vpcClient == nil {
		logger.V(1).Info("VPC client is nil, Skipping all instances check")
		return nil, nil
	}

	// Get all instances from VPC
	allInstances, err := vpcClient.ListInstances(ctx)
	if err != nil {
		logger.Error(err, "Failed to list all VPC instances")
		return nil, err
	}

	instancesMap := make(map[string]vpcv1.Instance)
	for _, instance := range allInstances {
		instancesMap[*instance.ID] = instance
	}

	logger.V(1).Info("Returning all retrieved VPC instances", "count", len(instancesMap))
	return instancesMap, nil
}

// isKarpenterManagedInstance checks if an instance was created by Karpenter
// by examining its tags for Karpenter-specific markers using the Global Tagging API
func (c *Controller) isKarpenterManagedInstance(ctx context.Context, instanceID string) bool {
	logger := log.FromContext(ctx)

	// Use the Global Tagging API if available
	if c.globalTagging != nil {
		return c.checkInstanceTagsWithGlobalTaggingAPI(ctx, instanceID, logger)
	}

	// If Global Tagging API is not available, we cannot reliably identify Karpenter instances
	logger.V(1).Info("Skipping identification as Global Tagging API is not available", "instance-id", instanceID)
	return false
}

// checkInstanceTagsWithGlobalTaggingAPI uses the IBM Global Tagging API to check if an instance has Karpenter tags
func (c *Controller) checkInstanceTagsWithGlobalTaggingAPI(ctx context.Context, instanceID string, logger logr.Logger) bool {
	// Construct the resource CRN for the VPC instance
	// Format: crn:version:cname:ctype:service-name:location:scope:service-instance:resource-type:resource

	if c.ibmClient == nil {
		logger.V(1).Info("Skipping tag check as IBM client is not available", "instance-id", instanceID)
		return false
	}

	// We need to get the instance details to construct the CRN properly
	// Handle potential panic from GetVPCClient when ibmClient is not properly initialized
	var vpcClient *ibm.VPCClient
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic in GetVPCClient: %v", r)
			}
		}()
		vpcClient, err = c.ibmClient.GetVPCClient(ctx)
	}()

	if err != nil {
		logger.V(1).Info("Failing to get VPC client for Global Tagging API", "instance-id", instanceID, "error", err.Error())
		return false
	}

	instance, err := vpcClient.GetInstance(ctx, instanceID)
	if err != nil {
		logger.V(1).Info("Failing to get instance for CRN construction", "instance-id", instanceID, "error", err.Error())
		return false
	}

	// Use the instance CRN if available, otherwise construct one
	return c.hasKarpenterTags(ctx, instance.CRN, instanceID, logger)
}

func (c *Controller) hasKarpenterTags(ctx context.Context, instanceCRN *string, instanceID string, logger logr.Logger) bool {
	// Use the instance CRN if available, otherwise construct one
	var resourceCRN string
	if instanceCRN != nil {
		resourceCRN = *instanceCRN
	} else {
		// Fallback: construct CRN (this may not work perfectly without more instance details)
		logger.V(1).Info("Skipping tag check as instance CRN is not available", "instance-id", instanceID)
		return false
	}

	// List tags for this resource
	listTagsOptions := &globaltaggingv1.ListTagsOptions{
		AttachedTo: &resourceCRN,
	}

	tagResults, _, err := c.globalTagging.ListTagsWithContext(ctx, listTagsOptions)
	if err != nil {
		logger.V(1).Info("Failing to list tags using Global Tagging API", "instance-id", instanceID, "error", err.Error())
		return false
	}

	if tagResults == nil || tagResults.Items == nil {
		logger.V(1).Info("Continuing as no tags were found for instance", "instance-id", instanceID)
		return false
	}

	// Check for Karpenter-specific tags
	for _, tag := range tagResults.Items {
		if tag.Name == nil {
			continue
		}
		tagName := *tag.Name

		// Check for Karpenter management indicators
		if tagName == "karpenter.sh/managed" ||
			strings.HasPrefix(tagName, "karpenter.sh/") ||
			strings.Contains(tagName, "karpenter") {
			logger.V(1).Info("Identifying instance as Karpenter-managed (via Global Tagging API)", "instance-id", instanceID, "tag", tagName)
			return true
		}
	}

	logger.V(2).Info("Finding no Karpenter tags via Global Tagging API", "instance-id", instanceID, "tags-count", len(tagResults.Items))
	return false
}

// processOrphanedNode handles cleanup of a single orphaned node
func (c *Controller) processOrphanedNode(ctx context.Context, node corev1.Node) error {
	logger := log.FromContext(ctx).WithValues(
		"node", node.Name,
		"provider-id", node.Spec.ProviderID,
	)

	// Safety check: verify this node is managed by Karpenter
	if !c.isNodeManagedByKarpenter(node) {
		logger.V(1).Info("Skipping cleanup as node is not managed by Karpenter")
		return nil
	}

	// Check if the node has been in NotReady state for long enough
	if !c.isNodeOrphanedLongEnough(node) {
		logger.V(1).Info("Skipping cleanup as node is not orphaned long enough")
		return nil
	}

	// Additional safety check: verify the instance really doesn't exist
	instanceID := c.extractInstanceIDFromProviderID(node.Spec.ProviderID)
	if instanceID == "" {
		logger.V(1).Info("Skipping cleanup as instance ID could not be extracted from provider ID")
		return nil
	}

	// Double-check that the instance doesn't exist
	exists, err := c.ibmClient.VPCInstanceExists(ctx, instanceID)
	if err != nil {
		logger.Error(err, "Skipping cleanup for safety as failed to verify instance existence")
		return nil
	}

	if exists {
		logger.V(1).Info("Skipping cleanup as instance still exists, node is not orphaned")
		return nil
	}

	logger.Info("Initiated orphaned node cleanup")

	// Step 1: Cordon the node to prevent new pods from being scheduled
	if err := c.cordonNode(ctx, &node); err != nil {
		logger.Error(err, "Failed to cordon orphaned node")
		// Continue with cleanup even if cordoning fails
	}

	// Step 2: Force delete any pods still running on the node
	if err := c.forceDeletePodsOnNode(ctx, node.Name); err != nil {
		logger.Error(err, "Failed to force delete pods on orphaned node")
		// Continue with cleanup even if pod deletion fails
	}

	// Step 3: Delete the node object
	if err := c.kubeClient.Delete(ctx, &node, &client.DeleteOptions{
		GracePeriodSeconds: lo.ToPtr(int64(0)),
	}); err != nil {
		return fmt.Errorf("deleting orphaned node: %w", err)
	}

	logger.Info("Successfully cleaned up orphaned node")
	return nil
}

// processOrphanedInstance handles cleanup of a single orphaned instance
func (c *Controller) processOrphanedInstance(ctx context.Context, instanceID string) error {
	logger := log.FromContext(ctx).WithValues("instance-id", instanceID)

	// Safety check: verify this instance is really Karpenter-managed
	if !c.isKarpenterManagedInstance(ctx, instanceID) {
		logger.V(1).Info("Instance not managed by Karpenter, Skipping cleanup")
		return nil
	}

	// Delete the instance from IBM Cloud
	if c.ibmClient == nil {
		logger.Error(nil, "IBM client is not initialized, cannot delete instance")
		return fmt.Errorf("IBM client not initialized")
	}

	logger.Info("Initiated orphaned instance deletion from IBM Cloud")
	// Get VPC client to delete instance
	vpcClient, err := c.ibmClient.GetVPCClient(ctx)
	if err != nil {
		logger.Error(err, "Failed to get VPC client for instance deletion")
		return fmt.Errorf("failed to get VPC client: %w", err)
	}

	if err := vpcClient.DeleteInstance(ctx, instanceID); err != nil {
		logger.Error(err, "Failed to delete orphaned instance")
		return fmt.Errorf("failed to delete instance %s: %w", instanceID, err)
	}

	logger.Info("Successfully cleaned up orphaned instance")
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
		if _, hasIBMNodeClass := nodeLabels["karpenter-ibm.sh/ibmnodeclass"]; hasIBMNodeClass {
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
			logger.Error(err, "Failed to force delete pod", "pod", pod.Name, "namespace", pod.Namespace)
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
