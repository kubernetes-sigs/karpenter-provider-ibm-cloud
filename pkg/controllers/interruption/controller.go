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
package interruption

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

// Controller handles instance interruption events from IBM Cloud
// Supports both VPC and IKS deployment modes with mode-specific response strategies
//
// Deployment Mode Support:
//   - VPC Mode: Direct VPC instance management with immediate node deletion and replacement
//   - IKS Mode: Hybrid approach using node cordoning and IKS worker pool management
//
// Interruption Detection:
//   - IBM Cloud-specific annotations and maintenance signals
//
// Kubelet conditions (NotReady, pressure) are left to core node repair, which applies
// the tolerations from CloudProvider.RepairPolicies.
//
// Mode-Specific Response Strategies:
//   - VPC Mode: Immediate node deletion for all interruption types to trigger Karpenter replacement
//   - IKS Mode: Node cordoning for capacity issues (let IKS manage), deletion for infrastructure issues
//
// Supported Interruption Reasons:
//   - CapacityUnavailable: Resource exhaustion scenarios
//   - NetworkResourceLimit: Network/IP address exhaustion
//   - HostMaintenance: Scheduled infrastructure maintenance
//   - InstanceHealthFailed: Instance health degradation
//   - StorageFailure: Boot/data volume issues
type Controller struct {
	kubeClient           client.Client
	apiReader            client.Reader
	recorder             record.EventRecorder
	unavailableOfferings *cache.UnavailableOfferings
}

// InterruptionReason represents the cause of an interruption
type InterruptionReason string

const (
	// Interruption reasons
	CapacityUnavailable  InterruptionReason = "capacity-unavailable"
	HostMaintenance      InterruptionReason = "host-maintenance"
	InstanceHealthFailed InterruptionReason = "instance-health-failed"
	NetworkResourceLimit InterruptionReason = "network-resource-limit"
	StorageFailure       InterruptionReason = "storage-failure"

	// Node annotations for IBM Cloud interruption info
	InterruptionAnnotation          = "karpenter-ibm.sh/interruption-detected"
	InterruptionReasonAnnotation    = "karpenter-ibm.sh/interruption-reason"
	InterruptionTimeAnnotation      = "karpenter-ibm.sh/interruption-time"
	InterruptionCompletedAnnotation = "karpenter-ibm.sh/interruption-completed"
	InterruptionEpisodeAnnotation   = "karpenter-ibm.sh/interruption-episode"
	InterruptionCordonAnnotation    = "karpenter-ibm.sh/interruption-owned-cordon"
)

// NewController constructs a controller instance
func NewController(kubeClient client.Client, recorder record.EventRecorder, unavailableOfferings *cache.UnavailableOfferings, readers ...client.Reader) *Controller {
	reader := client.Reader(kubeClient)
	if len(readers) > 0 && readers[0] != nil {
		reader = readers[0]
	}
	return &Controller{
		kubeClient:           kubeClient,
		apiReader:            reader,
		recorder:             recorder,
		unavailableOfferings: unavailableOfferings,
	}
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	// Since we're using singleton pattern, we don't get a request object
	// Instead, we'll process all nodes in the cluster

	nodeList := &v1.NodeList{}
	if err := c.kubeClient.List(ctx, nodeList); err != nil {
		return reconciler.Result{}, err
	}

	var failures []error
	for _, node := range nodeList.Items {
		if node.Labels[karpv1.NodePoolLabelKey] == "" || !strings.HasPrefix(node.Spec.ProviderID, "ibm://") {
			continue
		}
		interrupted, reason := c.isNodeInterrupted(ctx, &node)
		if !interrupted {
			if c.checkCapacitySignals(&node) == "" {
				if err := c.clearRecoveredInterruption(ctx, &node); err != nil {
					failures = append(failures, err)
				}
			}
			continue
		}
		if err := c.handleInterruption(ctx, &node, reason); err != nil {
			failures = append(failures, fmt.Errorf("handling interruption for %s: %w", node.Name, err))
			continue
		}
		if err := c.markNodeAsInterrupted(ctx, &node, reason); err != nil {
			failures = append(failures, err)
			continue
		}
		if c.recorder != nil {
			c.recorder.Event(&node, v1.EventTypeWarning, "Interruption", fmt.Sprintf("Node interruption handled: %s", reason))
		}
	}
	if len(failures) != 0 {
		return reconciler.Result{}, errors.Join(failures...)
	}

	return reconciler.Result{RequeueAfter: time.Minute}, nil
}

// isNodeInterrupted checks if a node is being interrupted by IBM Cloud
func (c *Controller) isNodeInterrupted(ctx context.Context, node *v1.Node) (bool, InterruptionReason) {
	logger := log.FromContext(ctx).WithValues("node", node.Name)

	// Check for capacity-related issues from node events or annotations
	if reason := c.checkCapacitySignals(node); reason != "" {
		if node.Annotations[InterruptionCompletedAnnotation] == "true" && node.Annotations[InterruptionEpisodeAnnotation] == interruptionEpisode(node) {
			return false, ""
		}
		logger.V(1).Info("Detecting capacity-related interruption", "reason", reason)
		return true, reason
	}

	return false, ""
}

// isCapacityRelated checks if the interruption is due to capacity constraints
func (c *Controller) isCapacityRelated(node *v1.Node, reason InterruptionReason) bool {
	switch reason {
	case CapacityUnavailable, NetworkResourceLimit:
		return true
	default:
		return false
	}
}

// Name returns the name of the controller
func (c *Controller) Name() string {
	return "interruption"
}

// markNodeAsInterrupted adds interruption annotations to the node
func (c *Controller) markNodeAsInterrupted(ctx context.Context, node *v1.Node, reason InterruptionReason) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &v1.Node{}
		if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
			return client.IgnoreNotFound(err)
		}
		if current.UID != node.UID {
			return fmt.Errorf("node UID changed while handling interruption")
		}
		if c.checkCapacitySignals(current) != reason || interruptionEpisode(current) != interruptionEpisode(node) {
			return fmt.Errorf("interruption episode changed before completion")
		}
		stored := current.DeepCopy()
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations[InterruptionAnnotation] = "true"
		current.Annotations[InterruptionCompletedAnnotation] = "true"
		current.Annotations[InterruptionEpisodeAnnotation] = interruptionEpisode(current)
		current.Annotations[InterruptionReasonAnnotation] = string(reason)
		current.Annotations[InterruptionTimeAnnotation] = time.Now().Format(time.RFC3339)
		return c.kubeClient.Patch(ctx, current, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{}))
	})
}

func (c *Controller) checkCapacitySignals(node *v1.Node) InterruptionReason {
	for _, key := range []string{"ibm-cloud.kubernetes.io/status", "ibm-cloud.kubernetes.io/error"} {
		value := strings.ToLower(strings.TrimSpace(node.Annotations[key]))
		switch value {
		case "capacity unavailable":
			return CapacityUnavailable
		case "network resources unavailable":
			return NetworkResourceLimit
		default:
			if reason := parseInterruptionReason(value); reason != "" {
				return reason
			}
		}
	}

	if node.Annotations["ibm-cloud.kubernetes.io/maintenance"] == "true" {
		return HostMaintenance
	}

	return ""
}

func parseInterruptionReason(value string) InterruptionReason {
	reason := InterruptionReason(strings.ToLower(strings.TrimSpace(value)))
	switch reason {
	case CapacityUnavailable, NetworkResourceLimit, HostMaintenance, InstanceHealthFailed, StorageFailure:
		return reason
	default:
		return ""
	}
}

// handleInterruption processes an interruption event based on the deployment mode
func (c *Controller) handleInterruption(ctx context.Context, node *v1.Node, reason InterruptionReason) error {
	switch c.inferModeFromNode(node) {
	case types.IKSMode:
		return c.handleIKSInterruption(ctx, node, reason)
	case types.VPCMode:
		return c.handleVPCInterruption(ctx, node, reason)
	default:
		return fmt.Errorf("cannot determine node backend")
	}
}

// handleVPCInterruption handles interruptions for VPC mode (direct instance management)
func (c *Controller) handleVPCInterruption(ctx context.Context, node *v1.Node, reason InterruptionReason) error {
	logger := log.FromContext(ctx).WithValues("node", node.Name, "mode", "vpc")

	// Mark instance type as unavailable if capacity related
	if c.isCapacityRelated(node, reason) {
		instanceType := node.Labels["node.kubernetes.io/instance-type"]
		zone := node.Labels["topology.kubernetes.io/zone"]
		capacityType := node.Labels[karpv1.CapacityTypeLabelKey]
		if capacityType == "" {
			capacityType = karpv1.CapacityTypeOnDemand
		}
		if instanceType != "" && zone != "" && c.unavailableOfferings != nil {
			c.unavailableOfferings.Add(instanceType+":"+zone+":"+capacityType, time.Now().Add(time.Hour))
			logger.Info("Marked instance type as unavailable due to capacity issue",
				"instanceType", instanceType, "zone", zone, "capacityType", capacityType)
		}
	}

	current, err := c.cordon(ctx, node)
	if err != nil || current == nil {
		return err
	}
	if err := c.deleteIfStillInterrupted(ctx, current); err != nil {
		return err
	}
	logger.Info("Deleted node to trigger replacement")

	return nil
}

// handleIKSInterruption handles interruptions for IKS mode (worker pool management)
func (c *Controller) handleIKSInterruption(ctx context.Context, node *v1.Node, reason InterruptionReason) error {
	logger := log.FromContext(ctx).WithValues("node", node.Name, "mode", "iks")

	// For IKS mode, we primarily cordon the node and let IKS worker pool management handle replacement
	// Direct node deletion might interfere with IKS worker pool sizing

	// Mark instance type as unavailable if capacity related (affects future provisioning)
	if c.isCapacityRelated(node, reason) {
		instanceType := node.Labels["node.kubernetes.io/instance-type"]
		zone := node.Labels["topology.kubernetes.io/zone"]
		capacityType := node.Labels[karpv1.CapacityTypeLabelKey]
		if capacityType == "" {
			capacityType = karpv1.CapacityTypeOnDemand
		}
		if instanceType != "" && zone != "" && c.unavailableOfferings != nil {
			c.unavailableOfferings.Add(instanceType+":"+zone+":"+capacityType, time.Now().Add(time.Hour))
			logger.Info("Marked instance type as unavailable due to capacity issue",
				"instanceType", instanceType, "zone", zone, "capacityType", capacityType)
		}
	}

	current, err := c.cordon(ctx, node)
	if err != nil || current == nil {
		return err
	}

	// Capacity issues only need cordoning; IKS worker pool sizing handles replacement.
	if !c.isCapacityRelated(node, reason) {
		if err := c.deleteIfStillInterrupted(ctx, current); err != nil {
			return err
		}
		logger.Info("Deleted node to trigger replacement for non-capacity interruption")
	}

	return nil
}

// inferModeFromNode attempts to infer the deployment mode from node characteristics
func (c *Controller) inferModeFromNode(node *v1.Node) types.ProviderMode {
	if node.Annotations[ownership.BackendAnnotation] == string(types.IKSMode) || (strings.HasPrefix(node.Spec.ProviderID, "ibm://") && !strings.HasPrefix(node.Spec.ProviderID, "ibm:///")) {
		return types.IKSMode
	}
	return types.VPCMode
}

// cordon returns the cordoned node as last read or written, or nil if it no longer exists.
func (c *Controller) cordon(ctx context.Context, node *v1.Node) (*v1.Node, error) {
	var result *v1.Node
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		result = nil
		current := &v1.Node{}
		if err := c.apiReader.Get(ctx, k8stypes.NamespacedName{Name: node.Name}, current); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if current.UID != node.UID {
			return fmt.Errorf("node UID changed before cordon")
		}
		if interruptionEpisode(current) != interruptionEpisode(node) || c.checkCapacitySignals(current) == "" {
			return fmt.Errorf("interruption episode changed before cordon")
		}
		if !current.Spec.Unschedulable {
			stored := current.DeepCopy()
			current.Spec.Unschedulable = true
			if current.Annotations == nil {
				current.Annotations = map[string]string{}
			}
			current.Annotations[InterruptionCordonAnnotation] = "true"
			if err := c.kubeClient.Patch(ctx, current, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
				return err
			}
		}
		result = current
		return nil
	})
	return result, err
}

func interruptionEpisode(node *v1.Node) string {
	return strings.Join([]string{node.Annotations["ibm-cloud.kubernetes.io/status"], node.Annotations["ibm-cloud.kubernetes.io/error"], node.Annotations["ibm-cloud.kubernetes.io/maintenance"]}, "|")
}

func (c *Controller) clearRecoveredInterruption(ctx context.Context, node *v1.Node) error {
	if node.Annotations[InterruptionAnnotation] == "" && node.Annotations[InterruptionCordonAnnotation] == "" {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &v1.Node{}
		if err := c.apiReader.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
			return client.IgnoreNotFound(err)
		}
		if current.UID != node.UID {
			return fmt.Errorf("node UID changed during interruption recovery")
		}
		if !current.DeletionTimestamp.IsZero() || c.checkCapacitySignals(current) != "" {
			return nil
		}
		stored := current.DeepCopy()
		if current.Annotations[InterruptionCordonAnnotation] == "true" {
			current.Spec.Unschedulable = false
		}
		for _, key := range []string{InterruptionAnnotation, InterruptionCompletedAnnotation, InterruptionEpisodeAnnotation, InterruptionCordonAnnotation, InterruptionReasonAnnotation, InterruptionTimeAnnotation} {
			delete(current.Annotations, key)
		}
		return c.kubeClient.Patch(ctx, current, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{}))
	})
}

// deleteIfStillInterrupted deletes the node only if the signal still holds on the exact
// version that was evaluated, so a cleared signal or concurrent update aborts the delete.
func (c *Controller) deleteIfStillInterrupted(ctx context.Context, node *v1.Node) error {
	if interrupted, _ := c.isNodeInterrupted(ctx, node); !interrupted {
		return fmt.Errorf("interruption signal cleared before delete")
	}
	if err := c.kubeClient.Delete(ctx, node, client.Preconditions{UID: &node.UID, ResourceVersion: &node.ResourceVersion}); client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("failed to delete node: %w", err)
	}
	return nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return builder.ControllerManagedBy(m).
		Named("interruption").
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}
