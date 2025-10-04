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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/util/retry"
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
	logger.V(1).Info("reconciling NodeClaim for startup taint lifecycle", "nodeclaim", req.NamespacedName)

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
	if !controllerutil.ContainsFinalizer(nodeClaim, StartupTaintLifecycleFinalizer) {
		// Double-check deletion timestamp hasn't changed during reconciliation
		if !nodeClaim.DeletionTimestamp.IsZero() {
			logger.V(1).Info("NodeClaim entered deletion during reconciliation, skipping finalizer addition")
			return reconcile.Result{}, nil
		}

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

	// Finalizer already present, continue with normal processing
	logger.V(1).Info("startup taint finalizer already present, continuing with lifecycle management")

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

	// Phase 2: Remove startup taints when node is ready
	if startupTaintsApplied && !startupTaintsRemoved {
		// Check if node is Ready
		if !c.isNodeReady(node) {
			logger.V(1).Info("waiting for node to become ready before removing startup taints")
			return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
		}

		// Check if other system startup taints (Cilium, etc.) are still present
		hasOtherSystemTaints := c.hasOtherSystemStartupTaints(node)
		if hasOtherSystemTaints {
			logger.V(1).Info("waiting for system pods to remove their startup taints (Cilium, etc.)")
			return reconcile.Result{RequeueAfter: 5 * time.Second}, nil
		}

		// Node is ready and system taints are gone - remove Karpenter startup taint
		return c.removeKarpenterStartupTaint(ctx, node)
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

	// Update node if modified with retry on conflict
	if modified {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			// Get fresh copy of node
			fresh := &corev1.Node{}
			if err := c.kubeClient.Get(ctx, client.ObjectKeyFromObject(node), fresh); err != nil {
				return err
			}

			// Reapply changes to fresh copy
			for _, startupTaint := range nodeClaim.Spec.StartupTaints {
				if !c.hasTaint(fresh, startupTaint) {
					fresh.Spec.Taints = append(fresh.Spec.Taints, startupTaint)
				}
			}

			return c.kubeClient.Update(ctx, fresh)
		})

		if err != nil {
			return reconcile.Result{}, fmt.Errorf("updating node with startup taints: %w", err)
		}
		logger.Info("successfully applied startup taints to node")
	}

	// Mark startup taints as applied with retry on conflict
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &karpv1.NodeClaim{}
		if err := c.kubeClient.Get(ctx, client.ObjectKeyFromObject(nodeClaim), fresh); err != nil {
			return err
		}

		if fresh.Labels == nil {
			fresh.Labels = make(map[string]string)
		}
		fresh.Labels[StartupTaintsAppliedLabel] = "true"

		return c.kubeClient.Update(ctx, fresh)
	})

	if err != nil {
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

	// Update node if modified with retry on conflict
	if modified {
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			// Get fresh copy of node
			fresh := &corev1.Node{}
			if err := c.kubeClient.Get(ctx, client.ObjectKeyFromObject(node), fresh); err != nil {
				return err
			}

			// Reapply changes to fresh copy
			for _, regularTaint := range nodeClaim.Spec.Taints {
				if !c.hasTaint(fresh, regularTaint) {
					fresh.Spec.Taints = append(fresh.Spec.Taints, regularTaint)
				}
			}

			return c.kubeClient.Update(ctx, fresh)
		})

		if err != nil {
			return reconcile.Result{}, fmt.Errorf("updating node with regular taints: %w", err)
		}
		logger.Info("successfully applied regular taints to node")
	}

	// Mark regular taints as applied with retry on conflict
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		fresh := &karpv1.NodeClaim{}
		if err := c.kubeClient.Get(ctx, client.ObjectKeyFromObject(nodeClaim), fresh); err != nil {
			return err
		}

		if fresh.Labels == nil {
			fresh.Labels = make(map[string]string)
		}
		fresh.Labels[RegularTaintsAppliedLabel] = "true"

		return c.kubeClient.Update(ctx, fresh)
	})

	if err != nil {
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

	// Only remove finalizer if present
	if !controllerutil.ContainsFinalizer(nodeClaim, StartupTaintLifecycleFinalizer) {
		logger.V(1).Info("startup taint finalizer not present, nothing to clean up")
		return reconcile.Result{}, nil
	}

	logger.V(1).Info("NodeClaim is being deleted, removing startup taint finalizer")

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
	if !controllerutil.ContainsFinalizer(fresh, StartupTaintLifecycleFinalizer) {
		logger.V(1).Info("startup taint finalizer already removed by another reconciliation")
		return reconcile.Result{}, nil
	}

	// Remove our finalizer using update with retry
	patch := client.MergeFrom(fresh.DeepCopy())
	controllerutil.RemoveFinalizer(fresh, StartupTaintLifecycleFinalizer)

	if err := c.kubeClient.Patch(ctx, fresh, patch); err != nil {
		if apierrors.IsConflict(err) {
			logger.V(1).Info("conflict removing finalizer, will retry")
			return reconcile.Result{Requeue: true}, nil
		}
		logger.Error(err, "failed to remove startup taint finalizer, will retry")
		return reconcile.Result{}, fmt.Errorf("removing startup taint finalizer: %w", err)
	}

	logger.Info("successfully removed startup taint finalizer, cleanup complete")
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
		"karpenter.sh/unregistered", // Karpenter startup taint
	}

	return lo.Contains(systemStartupTaints, key)
}

// isNodeReady checks if the node has a Ready condition with status True
func (c *Controller) isNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// hasOtherSystemStartupTaints checks if node has system startup taints other than karpenter.sh/unregistered
func (c *Controller) hasOtherSystemStartupTaints(node *corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if isSystemStartupTaint(taint.Key) && taint.Key != "karpenter.sh/unregistered" {
			return true
		}
	}
	return false
}

// removeKarpenterStartupTaint removes the karpenter.sh/unregistered taint from the node
func (c *Controller) removeKarpenterStartupTaint(ctx context.Context, node *corev1.Node) (reconcile.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("removing karpenter startup taint from node")

	// Remove karpenter.sh/unregistered taint with retry on conflict
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		// Get fresh copy of node
		fresh := &corev1.Node{}
		if err := c.kubeClient.Get(ctx, client.ObjectKeyFromObject(node), fresh); err != nil {
			return err
		}

		// Remove the Karpenter startup taint
		fresh.Spec.Taints = lo.Reject(fresh.Spec.Taints, func(t corev1.Taint, _ int) bool {
			return t.Key == "karpenter.sh/unregistered"
		})

		return c.kubeClient.Update(ctx, fresh)
	})

	if err != nil {
		return reconcile.Result{}, fmt.Errorf("removing karpenter startup taint: %w", err)
	}

	logger.Info("successfully removed karpenter startup taint from node")
	// Requeue to verify all taints are removed and proceed to next phase
	return reconcile.Result{Requeue: true}, nil
}
