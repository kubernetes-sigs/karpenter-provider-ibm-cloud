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

package startuptaint

import (
	"context"
	"fmt"
	"time"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const (
	// StartupTaintLifecycleFinalizer ensures proper cleanup
	StartupTaintLifecycleFinalizer = "startuptaint.nodeclaim.ibm.sh/finalizer"

	// Labels and annotations for tracking state
	StartupTaintsAppliedLabel = "karpenter.ibm.sh/startup-taints-applied"
	RegularTaintsAppliedLabel = "karpenter.ibm.sh/regular-taints-applied"

	// Common startup taint keys to monitor
	CiliumNotReadyTaint = "node.cilium.io/agent-not-ready"
	NodeNotReadyTaint   = "node.kubernetes.io/not-ready"
)

// Controller manages the lifecycle of startup taints and regular taint application
type Controller struct {
	kubeClient client.Client
}

// NewController creates a new startup taint lifecycle controller
func NewController(kubeClient client.Client) *Controller {
	return &Controller{
		kubeClient: kubeClient,
	}
}

func (c *Controller) Register(ctx context.Context, mgr manager.Manager) error {
	_, err := builder.ControllerManagedBy(mgr).
		Named("startuptaint.lifecycle").
		For(&karpv1.NodeClaim{}).
		Build(c)
	return err
}

func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithName("startuptaint.lifecycle")
	logger.Info("reconciling NodeClaim for startup taint lifecycle", "nodeclaim", req.NamespacedName)

	// Get the NodeClaim
	nodeClaim := &karpv1.NodeClaim{}
	if err := c.kubeClient.Get(ctx, req.NamespacedName, nodeClaim); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !nodeClaim.DeletionTimestamp.IsZero() {
		return c.handleDeletion(ctx, nodeClaim)
	}

	// ONLY add finalizer if NodeClaim is NOT being deleted - prevent race conditions
	if controllerutil.ContainsFinalizer(nodeClaim, StartupTaintLifecycleFinalizer) {
		// Finalizer already present, continue with normal processing
		logger.V(1).Info("startup taint finalizer already present, continuing with lifecycle management")
	} else {
		// Add finalizer and label in a single atomic update
		patch := client.MergeFrom(nodeClaim.DeepCopy())
		controllerutil.AddFinalizer(nodeClaim, StartupTaintLifecycleFinalizer)

		// Mark this NodeClaim as being managed by startup taint lifecycle controller
		if nodeClaim.Labels == nil {
			nodeClaim.Labels = make(map[string]string)
		}
		nodeClaim.Labels["karpenter.ibm.sh/startup-taint-lifecycle"] = "true"

		if err := c.kubeClient.Patch(ctx, nodeClaim, patch); err != nil {
			return reconcile.Result{}, fmt.Errorf("adding finalizer and lifecycle label: %w", err)
		}
		logger.V(1).Info("added startup taint finalizer to nodeclaim")
		return reconcile.Result{Requeue: true}, nil
	}

	// Skip if NodeClaim doesn't have an associated Node yet
	if nodeClaim.Status.NodeName == "" {
		logger.V(1).Info("NodeClaim has no associated Node, skipping")
		return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Get the associated Node
	node := &corev1.Node{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: nodeClaim.Status.NodeName}, node); err != nil {
		logger.Error(err, "failed to get Node", "node", nodeClaim.Status.NodeName)
		return reconcile.Result{RequeueAfter: 10 * time.Second}, client.IgnoreNotFound(err)
	}

	// Process startup taint lifecycle
	return c.processStartupTaintLifecycle(ctx, nodeClaim, node)
}

func (c *Controller) processStartupTaintLifecycle(ctx context.Context, nodeClaim *karpv1.NodeClaim, node *corev1.Node) (reconcile.Result, error) {
	logger := log.FromContext(ctx)

	// Check current state
	startupTaintsApplied := nodeClaim.Labels[StartupTaintsAppliedLabel] == "true"
	regularTaintsApplied := nodeClaim.Labels[RegularTaintsAppliedLabel] == "true"
	hasStartupTaints := len(nodeClaim.Spec.StartupTaints) > 0
	hasRegularTaints := len(nodeClaim.Spec.Taints) > 0
	startupTaintsRemoved := c.areStartupTaintsRemoved(node)

	logger.Info("processing startup taint lifecycle",
		"startupTaintsApplied", startupTaintsApplied,
		"regularTaintsApplied", regularTaintsApplied,
		"hasStartupTaints", hasStartupTaints,
		"hasRegularTaints", hasRegularTaints,
		"startupTaintsRemoved", startupTaintsRemoved,
		"nodeTaintCount", len(node.Spec.Taints))

	// Handle case where there are no startup taints at all
	if !hasStartupTaints {
		// Skip startup phase, go straight to regular taints if needed
		if hasRegularTaints && !regularTaintsApplied {
			logger.Info("no startup taints defined, applying regular taints immediately")
			return c.applyRegularTaints(ctx, nodeClaim, node)
		}
		// Nothing to do if no taints defined at all
		return reconcile.Result{}, nil
	}

	// Phase 1: Apply only startup taints initially (when startup taints exist)
	if !startupTaintsApplied {
		return c.applyStartupTaints(ctx, nodeClaim, node)
	}

	// Phase 2: Wait for startup taints to be removed by system pods
	if startupTaintsApplied && !startupTaintsRemoved {
		logger.V(1).Info("waiting for system pods to remove startup taints")
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Phase 3: Apply regular taints after startup taints are removed
	if startupTaintsRemoved && hasRegularTaints && !regularTaintsApplied {
		return c.applyRegularTaints(ctx, nodeClaim, node)
	}

	// All phases complete
	logger.V(1).Info("startup taint lifecycle complete")
	return reconcile.Result{}, nil
}

func (c *Controller) applyStartupTaints(ctx context.Context, nodeClaim *karpv1.NodeClaim, node *corev1.Node) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("applying startup taints only")

	modified := false
	originalTaints := make([]corev1.Taint, len(node.Spec.Taints))
	copy(originalTaints, node.Spec.Taints)

	// Add startup taints to node
	for _, startupTaint := range nodeClaim.Spec.StartupTaints {
		if !c.hasTaint(node, startupTaint) {
			logger.Info("adding startup taint to node", "key", startupTaint.Key, "effect", startupTaint.Effect)
			node.Spec.Taints = append(node.Spec.Taints, startupTaint)
			modified = true
		}
	}

	// Update node if modified
	if modified {
		if err := c.kubeClient.Update(ctx, node); err != nil {
			return reconcile.Result{}, fmt.Errorf("updating node with startup taints: %w", err)
		}
		logger.Info("successfully applied startup taints to node")
	}

	// Mark startup taints as applied
	if nodeClaim.Labels == nil {
		nodeClaim.Labels = make(map[string]string)
	}
	nodeClaim.Labels[StartupTaintsAppliedLabel] = "true"

	if err := c.kubeClient.Update(ctx, nodeClaim); err != nil {
		return reconcile.Result{}, fmt.Errorf("updating NodeClaim labels: %w", err)
	}

	return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
}

func (c *Controller) applyRegularTaints(ctx context.Context, nodeClaim *karpv1.NodeClaim, node *corev1.Node) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("applying regular taints after startup taints removed")

	modified := false
	originalTaints := make([]corev1.Taint, len(node.Spec.Taints))
	copy(originalTaints, node.Spec.Taints)

	// Add regular taints to node
	for _, regularTaint := range nodeClaim.Spec.Taints {
		if !c.hasTaint(node, regularTaint) {
			logger.Info("adding regular taint to node", "key", regularTaint.Key, "effect", regularTaint.Effect)
			node.Spec.Taints = append(node.Spec.Taints, regularTaint)
			modified = true
		}
	}

	// Update node if modified
	if modified {
		if err := c.kubeClient.Update(ctx, node); err != nil {
			return reconcile.Result{}, fmt.Errorf("updating node with regular taints: %w", err)
		}
		logger.Info("successfully applied regular taints to node")
	}

	// Mark regular taints as applied
	if nodeClaim.Labels == nil {
		nodeClaim.Labels = make(map[string]string)
	}
	nodeClaim.Labels[RegularTaintsAppliedLabel] = "true"

	if err := c.kubeClient.Update(ctx, nodeClaim); err != nil {
		return reconcile.Result{}, fmt.Errorf("updating NodeClaim labels: %w", err)
	}

	logger.Info("startup taint lifecycle completed successfully")
	return reconcile.Result{}, nil
}

func (c *Controller) areStartupTaintsRemoved(node *corev1.Node) bool {
	// Check if critical startup taints are gone
	for _, taint := range node.Spec.Taints {
		if isSystemStartupTaint(taint.Key) {
			return false
		}
	}
	return true
}

func (c *Controller) hasTaint(node *corev1.Node, targetTaint corev1.Taint) bool {
	for _, taint := range node.Spec.Taints {
		if taint.Key == targetTaint.Key &&
			taint.Effect == targetTaint.Effect &&
			taint.Value == targetTaint.Value {
			return true
		}
	}
	return false
}

func (c *Controller) handleDeletion(ctx context.Context, nodeClaim *karpv1.NodeClaim) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("nodeclaim", nodeClaim.Name)

	// Only remove finalizer if present to avoid race conditions
	if !controllerutil.ContainsFinalizer(nodeClaim, StartupTaintLifecycleFinalizer) {
		logger.V(1).Info("startup taint finalizer not present, skipping removal")
		return reconcile.Result{}, nil
	}

	logger.V(1).Info("handling NodeClaim deletion, removing startup taint finalizer")

	// Use patch for atomic finalizer removal to prevent race conditions
	patch := client.MergeFrom(nodeClaim.DeepCopy())
	controllerutil.RemoveFinalizer(nodeClaim, StartupTaintLifecycleFinalizer)
	if err := c.kubeClient.Patch(ctx, nodeClaim, patch); err != nil {
		return reconcile.Result{}, fmt.Errorf("removing startup taint finalizer: %w", err)
	}

	logger.V(1).Info("successfully removed startup taint finalizer")
	return reconcile.Result{}, nil
}

// isSystemStartupTaint checks if a taint key is a known system startup taint
func isSystemStartupTaint(key string) bool {
	systemStartupTaints := []string{
		CiliumNotReadyTaint,
		NodeNotReadyTaint,
		"node.kubernetes.io/unreachable",
		"node.kubernetes.io/disk-pressure",
		"node.kubernetes.io/memory-pressure",
		"node.kubernetes.io/pid-pressure",
	}

	return lo.Contains(systemStartupTaints, key)
}
