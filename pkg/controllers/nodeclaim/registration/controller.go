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

package registration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

const (
	// NodeClaimRegistrationFinalizer is added to NodeClaims to ensure proper cleanup
	NodeClaimRegistrationFinalizer = "registration.nodeclaim.ibm.sh/finalizer"

	// Labels for registered nodes
	RegisteredLabel  = "karpenter.sh/registered"
	InitializedLabel = "karpenter.sh/initialized"
	NodePoolLabel    = "karpenter.sh/nodepool"
	NodeClassLabel   = "karpenter-ibm.sh/ibmnodeclass"
	ProvisionerLabel = "provisioner"
	ProvisionedTaint = "karpenter-ibm.sh/provisioned"
)

// Controller reconciles NodeClaim registration with corresponding Nodes
// +kubebuilder:rbac:groups=karpenter.sh,resources=nodeclaims,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=karpenter-ibm.sh,resources=ibmnodeclasses,verbs=get;list;watch
type Controller struct {
	kubeClient client.Client
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client) (*Controller, error) {
	return &Controller{
		kubeClient: kubeClient,
	}, nil
}

// Reconcile executes a control loop for NodeClaim registration
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("nodeclaim", req.Name)

	// Get the NodeClaim
	nodeClaim := &karpv1.NodeClaim{}
	if err := c.kubeClient.Get(ctx, req.NamespacedName, nodeClaim); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion first - critical for preventing finalizer race conditions
	if !nodeClaim.DeletionTimestamp.IsZero() {
		return c.handleDeletion(ctx, nodeClaim)
	}

	// Add finalizer only if not present AND NodeClaim is NOT being deleted
	if !controllerutil.ContainsFinalizer(nodeClaim, NodeClaimRegistrationFinalizer) {
		// Double-check deletion timestamp hasn't changed during reconciliation
		if !nodeClaim.DeletionTimestamp.IsZero() {
			logger.V(1).Info("NodeClaim entered deletion during reconciliation, skipping finalizer addition")
			return reconcile.Result{}, nil
		}

		patch := client.MergeFrom(nodeClaim.DeepCopy())
		controllerutil.AddFinalizer(nodeClaim, NodeClaimRegistrationFinalizer)
		if err := c.kubeClient.Patch(ctx, nodeClaim, patch); err != nil {
			return reconcile.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		logger.V(1).Info("Adding registration finalizer to NodeClaim")
	}

	// Skip if already fully initialized (registered and ready)
	if c.isNodeClaimFullyInitialized(nodeClaim) {
		return reconcile.Result{}, nil
	}

	// Find the corresponding node
	node, err := c.findNodeForNodeClaim(ctx, nodeClaim)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("finding node for nodeclaim: %w", err)
	}

	if node == nil {
		// Node not found yet, requeue to check again
		logger.V(1).Info("Node not found for NodeClaim, requeueing")
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Sync NodeClaim properties to Node if not already registered
	if !c.isNodeClaimRegistered(nodeClaim) {
		if err := c.syncNodeClaimToNode(ctx, nodeClaim, node); err != nil {
			return reconcile.Result{}, fmt.Errorf("syncing nodeclaim properties to node: %w", err)
		}
	}

	// Update NodeClaim status (handles both registration and initialization)
	if err := c.updateNodeClaimStatus(ctx, nodeClaim, node); err != nil {
		return reconcile.Result{}, fmt.Errorf("updating nodeclaim status: %w", err)
	}

	// If registered but not initialized, requeue to check node readiness again
	if c.isNodeClaimRegistered(nodeClaim) && !nodeClaim.StatusConditions().Get(karpv1.ConditionTypeInitialized).IsTrue() {
		logger.V(1).Info("NodeClaim registered but not initialized, requeueing to check node readiness")
		return reconcile.Result{RequeueAfter: 15 * time.Second}, nil
	}

	logger.Info("Successfully processed NodeClaim", "node", node.Name,
		"registered", c.isNodeClaimRegistered(nodeClaim),
		"initialized", nodeClaim.StatusConditions().Get(karpv1.ConditionTypeInitialized).IsTrue())
	return reconcile.Result{}, nil
}

// handleDeletion handles NodeClaim deletion cleanup
func (c *Controller) handleDeletion(ctx context.Context, nodeClaim *karpv1.NodeClaim) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("nodeclaim", nodeClaim.Name)

	if !controllerutil.ContainsFinalizer(nodeClaim, NodeClaimRegistrationFinalizer) {
		return reconcile.Result{}, nil
	}

	logger.V(1).Info("NodeClaim is being deleted, removing registration finalizer")

	// Get fresh copy to avoid conflicts
	fresh := &karpv1.NodeClaim{}
	if err := c.kubeClient.Get(ctx, client.ObjectKeyFromObject(nodeClaim), fresh); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("NodeClaim already deleted, nothing to do")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("getting fresh NodeClaim: %w", err)
	}

	// Check again if finalizer is present in fresh copy
	if !controllerutil.ContainsFinalizer(fresh, NodeClaimRegistrationFinalizer) {
		logger.V(1).Info("Registration finalizer already removed by another reconciliation")
		return reconcile.Result{}, nil
	}

	// Remove finalizer using patch with conflict handling
	patch := client.MergeFrom(fresh.DeepCopy())
	controllerutil.RemoveFinalizer(fresh, NodeClaimRegistrationFinalizer)

	if err := c.kubeClient.Patch(ctx, fresh, patch); err != nil {
		if apierrors.IsConflict(err) {
			logger.V(1).Info("Conflict removing finalizer, retrying")
			return reconcile.Result{Requeue: true}, nil
		}
		return reconcile.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	logger.Info("Successfully removed registration finalizer, cleanup complete")
	return reconcile.Result{}, nil
}

// isNodeClaimRegistered checks if a NodeClaim is already registered
func (c *Controller) isNodeClaimRegistered(nodeClaim *karpv1.NodeClaim) bool {
	return nodeClaim.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue()
}

// isNodeClaimFullyInitialized checks if a NodeClaim is both registered and initialized
func (c *Controller) isNodeClaimFullyInitialized(nodeClaim *karpv1.NodeClaim) bool {
	return nodeClaim.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue() &&
		nodeClaim.StatusConditions().Get(karpv1.ConditionTypeInitialized).IsTrue()
}

// findNodeForNodeClaim finds the Node corresponding to a NodeClaim
func (c *Controller) findNodeForNodeClaim(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*corev1.Node, error) {
	logger := log.FromContext(ctx).WithValues("nodeclaim", nodeClaim.Name)

	// If NodeClaim already has a node name, use it
	if nodeClaim.Status.NodeName != "" {
		logger.V(1).Info("looking for node by nodeclaim status node name", "nodeName", nodeClaim.Status.NodeName)
		node := &corev1.Node{}
		if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: nodeClaim.Status.NodeName}, node); err != nil {
			logger.V(1).Info("Node not found by status node name", "nodeName", nodeClaim.Status.NodeName, "error", err)
			return nil, client.IgnoreNotFound(err)
		}
		logger.V(1).Info("Found node by status node name", "nodeName", node.Name)
		return node, nil
	}

	// Find node by provider ID
	if nodeClaim.Status.ProviderID != "" {
		logger.V(1).Info("looking for node by provider ID", "providerID", nodeClaim.Status.ProviderID)
		nodeList := &corev1.NodeList{}
		if err := c.kubeClient.List(ctx, nodeList); err != nil {
			return nil, err
		}

		for _, node := range nodeList.Items {
			if node.Spec.ProviderID == nodeClaim.Status.ProviderID {
				logger.V(1).Info("Found node by provider ID", "nodeName", node.Name, "providerID", node.Spec.ProviderID)
				return &node, nil
			}
		}
		logger.V(1).Info("No node found matching provider ID", "providerID", nodeClaim.Status.ProviderID)
	}

	// Fallback: try to find node by NodeClaim name (hostname-based matching)
	// This handles cases where NodeClaim name is used as hostname
	logger.V(1).Info("Trying fallback: looking for node by NodeClaim name", "nodeClaimName", nodeClaim.Name)
	node := &corev1.Node{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: nodeClaim.Name}, node); err == nil {
		logger.Info("Found node by NodeClaim name fallback", "nodeName", node.Name)
		return node, nil
	}

	logger.V(1).Info("No node found for NodeClaim", "nodeClaimName", nodeClaim.Name, "statusNodeName", nodeClaim.Status.NodeName, "providerID", nodeClaim.Status.ProviderID)
	return nil, nil
}

// syncNodeClaimToNode syncs labels and taints from NodeClaim to Node
func (c *Controller) syncNodeClaimToNode(ctx context.Context, nodeClaim *karpv1.NodeClaim, node *corev1.Node) error {
	logger := log.FromContext(ctx).WithValues("nodeclaim", nodeClaim.Name, "node", node.Name)
	logger.Info("Synced NodeClaim properties to node")

	patch := client.MergeFrom(node.DeepCopy())
	modified := false

	// Add finalizer to node
	if !controllerutil.ContainsFinalizer(node, NodeClaimRegistrationFinalizer) {
		logger.V(1).Info("adding finalizer to node")
		controllerutil.AddFinalizer(node, NodeClaimRegistrationFinalizer)
		modified = true
	}

	// Sync required labels
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}

	// Add NodePool label
	if nodePoolName, exists := nodeClaim.Labels[NodePoolLabel]; exists {
		logger.Info("Synced nodepool label", "nodePoolName", nodePoolName, "currentNodeLabel", node.Labels[NodePoolLabel])
		if node.Labels[NodePoolLabel] != nodePoolName {
			logger.Info("Updated nodepool label on node", "from", node.Labels[NodePoolLabel], "to", nodePoolName)
			node.Labels[NodePoolLabel] = nodePoolName
			modified = true
		} else {
			logger.V(1).Info("Nodepool label already correct on node", "nodePoolName", nodePoolName)
		}
	} else {
		logger.Info("Nodepool label missing from NodeClaim", "nodeClaimLabels", nodeClaim.Labels)
	}

	// Add NodeClass label
	if nodeClaim.Spec.NodeClassRef != nil {
		if node.Labels[NodeClassLabel] != nodeClaim.Spec.NodeClassRef.Name {
			node.Labels[NodeClassLabel] = nodeClaim.Spec.NodeClassRef.Name
			modified = true
		}
	}

	// Add provisioner label from NodeClaim template
	if provisioner, exists := nodeClaim.Labels[ProvisionerLabel]; exists {
		if node.Labels[ProvisionerLabel] != provisioner {
			node.Labels[ProvisionerLabel] = provisioner
			modified = true
		}
	}

	// Add registered label
	if node.Labels[RegisteredLabel] != "true" {
		node.Labels[RegisteredLabel] = "true"
		modified = true
	}

	// Convert Requirements to labels using Karpenter core scheduling pattern
	// This handles both single-value and multi-value Requirements (using first value for multi-value)
	requirementLabels := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...).Labels()
	for k, v := range requirementLabels {
		// Skip system labels that are managed elsewhere (Karpenter core already filters restricted labels)
		if strings.HasPrefix(k, "karpenter.sh/") ||
			strings.HasPrefix(k, "karpenter-ibm.sh/") ||
			strings.HasPrefix(k, "kubernetes.io/") ||
			strings.HasPrefix(k, "node.kubernetes.io/") ||
			strings.HasPrefix(k, "topology.kubernetes.io/") ||
			strings.HasPrefix(k, "beta.kubernetes.io/") ||
			strings.HasPrefix(k, "node-restriction.kubernetes.io/") {
			continue
		}

		// Apply requirement as node label
		if node.Labels[k] != v {
			logger.V(1).Info("Syncing requirement as node label", "key", k, "value", v)
			node.Labels[k] = v
			modified = true
		}
	}

	// Sync taints from NodeClaim to Node (unless do-not-sync label is set or startup taint lifecycle controller is handling it)
	if _, skipSync := nodeClaim.Labels["karpenter.sh/do-not-sync-taints"]; !skipSync {
		// Skip if startup taint lifecycle controller is managing taints
		if _, lifecycleManaged := nodeClaim.Labels["karpenter-ibm.sh/startup-taint-lifecycle"]; !lifecycleManaged {
			if c.syncTaintsToNode(nodeClaim, node) {
				modified = true
			}
		}
	}

	// Remove unregistered taint using standard Karpenter taint
	if c.removeTaintFromNode(node, karpv1.UnregisteredTaintKey) {
		modified = true
	}

	if modified {
		logger.Info("Applied node label and taint updates", "modified", modified)
		if err := c.kubeClient.Patch(ctx, node, patch); err != nil {
			logger.Error(err, "failed to patch node with updated labels/taints")
			return fmt.Errorf("patching node: %w", err)
		}
		logger.Info("Successfully updated node labels and taints")
	} else {
		logger.V(1).Info("No changes needed for node")
	}

	return nil
}

// syncTaintsToNode syncs both regular and startup taints from NodeClaim to Node
func (c *Controller) syncTaintsToNode(nodeClaim *karpv1.NodeClaim, node *corev1.Node) bool {
	// Store original taints to check if they changed
	originalTaints := make([]corev1.Taint, len(node.Spec.Taints))
	copy(originalTaints, node.Spec.Taints)

	// Combine all taints that need to be synced
	allTaints := append(nodeClaim.Spec.Taints, nodeClaim.Spec.StartupTaints...)

	// Update existing taints and add new ones
	modified := false
	for _, ncTaint := range allTaints {
		found := false
		for i, nodeTaint := range node.Spec.Taints {
			if nodeTaint.Key == ncTaint.Key && nodeTaint.Effect == ncTaint.Effect {
				found = true
				// Update the value if it's different (this handles the value update case)
				if nodeTaint.Value != ncTaint.Value {
					node.Spec.Taints[i].Value = ncTaint.Value
					modified = true
				}
				break
			}
		}
		// Add new taint if not found
		if !found {
			node.Spec.Taints = append(node.Spec.Taints, ncTaint)
			modified = true
		}
	}

	return modified
}

// removeTaintFromNode removes a taint from a node
func (c *Controller) removeTaintFromNode(node *corev1.Node, taintKey string) bool {
	initialLen := len(node.Spec.Taints)
	node.Spec.Taints = lo.Filter(node.Spec.Taints, func(taint corev1.Taint, _ int) bool {
		return taint.Key != taintKey
	})
	return len(node.Spec.Taints) != initialLen
}

// updateNodeClaimStatus updates the NodeClaim status to mark it as registered and initialized
func (c *Controller) updateNodeClaimStatus(ctx context.Context, nodeClaim *karpv1.NodeClaim, node *corev1.Node) error {
	logger := log.FromContext(ctx).WithValues("nodeclaim", nodeClaim.Name, "node", node.Name)
	patch := client.MergeFrom(nodeClaim.DeepCopy())

	// Update node name if not set
	if nodeClaim.Status.NodeName != node.Name {
		nodeClaim.Status.NodeName = node.Name
	}

	// Set registered condition using the proper StatusConditions interface
	nodeClaim.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)

	// Check if the node is ready and set Initialized condition
	nodeReady := false
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			nodeReady = true
			break
		}
	}

	// If node is ready and NodeClaim is not yet marked as initialized
	if nodeReady && !nodeClaim.StatusConditions().Get(karpv1.ConditionTypeInitialized).IsTrue() {
		logger.Info("Node is ready, marked NodeClaim as initialized")
		nodeClaim.StatusConditions().SetTrue(karpv1.ConditionTypeInitialized)

		// Also ensure the node has the initialized label
		if err := c.setNodeInitializedLabel(ctx, node); err != nil {
			logger.Error(err, "failed to set initialized label on node")
			return fmt.Errorf("setting initialized label on node: %w", err)
		}
	}

	return c.kubeClient.Status().Patch(ctx, nodeClaim, patch)
}

// setNodeInitializedLabel sets the karpenter.sh/initialized label on the node
func (c *Controller) setNodeInitializedLabel(ctx context.Context, node *corev1.Node) error {
	logger := log.FromContext(ctx).WithValues("node", node.Name)

	// Check if label is already set
	if node.Labels[InitializedLabel] == "true" {
		logger.V(1).Info("Initialized label already set on node")
		return nil
	}

	// Get fresh copy of node to avoid conflicts
	freshNode := &corev1.Node{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: node.Name}, freshNode); err != nil {
		return fmt.Errorf("getting fresh node: %w", err)
	}

	patch := client.MergeFrom(freshNode.DeepCopy())

	// Ensure labels map exists
	if freshNode.Labels == nil {
		freshNode.Labels = make(map[string]string)
	}

	// Set the initialized label
	freshNode.Labels[InitializedLabel] = "true"

	logger.Info("Set initialized label on node")
	if err := c.kubeClient.Patch(ctx, freshNode, patch); err != nil {
		return fmt.Errorf("patching node with initialized label: %w", err)
	}

	logger.Info("Successfully set initialized label on node")
	return nil
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return builder.ControllerManagedBy(m).
		Named("nodeclaim.registration.ibm").
		For(&karpv1.NodeClaim{}).
		Owns(&corev1.Node{}).
		Complete(c)
}
