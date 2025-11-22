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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// NodeEventHandler handles Node events and triggers NodeClaim reconciliation
// when startup taints are removed
type NodeEventHandler struct{}

// Create implements handler.EventHandler
func (h *NodeEventHandler) Create(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// No action needed on node creation
}

// Update implements handler.EventHandler
func (h *NodeEventHandler) Update(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	logger := log.FromContext(ctx)

	oldNode, ok := e.ObjectOld.(*corev1.Node)
	if !ok {
		logger.Error(nil, "unexpected object type in Update", "type", e.ObjectOld.GetObjectKind())
		return
	}

	newNode, ok := e.ObjectNew.(*corev1.Node)
	if !ok {
		logger.Error(nil, "unexpected object type in Update", "type", e.ObjectNew.GetObjectKind())
		return
	}

	// Check if startup taints were removed
	if h.startupTaintsWereRemoved(oldNode, newNode) {
		logger.V(1).Info("startup taints were removed from node, triggering NodeClaim reconciliation", "node", newNode.Name)

		// Find the associated NodeClaim and trigger reconciliation
		if nodeClaimName := h.getNodeClaimForNode(newNode); nodeClaimName != "" {
			q.Add(reconcile.Request{
				NamespacedName: client.ObjectKey{Name: nodeClaimName},
			})
		}
	}
}

// Delete implements handler.EventHandler
func (h *NodeEventHandler) Delete(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// No action needed on node deletion
}

// Generic implements handler.EventHandler
func (h *NodeEventHandler) Generic(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	// No action needed for generic events
}

// startupTaintsWereRemoved checks if any system startup taints were removed between old and new node
func (h *NodeEventHandler) startupTaintsWereRemoved(oldNode, newNode *corev1.Node) bool {
	// Check if any system startup taints present in old node are missing in new node
	for _, oldTaint := range oldNode.Spec.Taints {
		if isSystemStartupTaint(oldTaint.Key) {
			// Check if this startup taint is missing in new node
			found := false
			for _, newTaint := range newNode.Spec.Taints {
				if newTaint.Key == oldTaint.Key &&
					newTaint.Effect == oldTaint.Effect &&
					newTaint.Value == oldTaint.Value {
					found = true
					break
				}
			}
			if !found {
				return true // A startup taint was removed
			}
		}
	}
	return false
}

// getNodeClaimForNode finds the NodeClaim associated with a Node
func (h *NodeEventHandler) getNodeClaimForNode(node *corev1.Node) string {
	// Look for NodeClaim name in node labels
	if nodeClaimName, exists := node.Labels["karpenter.sh/nodeclaim"]; exists {
		return nodeClaimName
	}

	// Fallback: use node name if it matches NodeClaim naming pattern
	return node.Name
}
