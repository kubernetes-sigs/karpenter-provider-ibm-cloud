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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

// Controller handles instance interruption events from IBM Cloud
// Supports both VPC and IKS deployment modes with mode-specific response strategies
//
// Deployment Mode Support:
//   - VPC Mode: Direct VPC instance management with immediate node deletion and replacement
//   - IKS Mode: Hybrid approach using node cordoning and IKS worker pool management
//
// Interruption Detection:
//   - Node condition monitoring (Ready, MemoryPressure, NetworkUnavailable, etc.)
//   - IBM Cloud metadata service health state checking
//   - IBM Cloud-specific annotations and maintenance signals
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
	recorder             record.EventRecorder
	unavailableOfferings *cache.UnavailableOfferings
	httpClient           *http.Client
	providerFactory      *providers.ProviderFactory
}

// InstanceMetadata represents IBM Cloud instance metadata response
type InstanceMetadata struct {
	ID             string `json:"id"`
	LifecycleState string `json:"lifecycle_state"`
	HealthState    string `json:"health_state"`
	Status         string `json:"status"`
}

// InterruptionReason represents the cause of an interruption
type InterruptionReason string

const (
	// IBM Cloud metadata service endpoints
	MetadataBaseURL     = "http://api.metadata.cloud.ibm.com"
	MetadataTokenURL    = MetadataBaseURL + "/instance_identity/v1/token?version=2022-03-29"
	MetadataInstanceURL = MetadataBaseURL + "/metadata/v1/instance?version=2022-03-29"

	// Interruption reasons
	CapacityUnavailable  InterruptionReason = "capacity-unavailable"
	HostMaintenance      InterruptionReason = "host-maintenance"
	InstanceHealthFailed InterruptionReason = "instance-health-failed"
	NetworkResourceLimit InterruptionReason = "network-resource-limit"
	StorageFailure       InterruptionReason = "storage-failure"

	// Node annotations for IBM Cloud interruption info
	InterruptionAnnotation       = "karpenter.ibm.sh/interruption-detected"
	InterruptionReasonAnnotation = "karpenter.ibm.sh/interruption-reason"
	InterruptionTimeAnnotation   = "karpenter.ibm.sh/interruption-time"
)

// NewController constructs a controller instance
func NewController(kubeClient client.Client, recorder record.EventRecorder, unavailableOfferings *cache.UnavailableOfferings, providerFactory *providers.ProviderFactory) *Controller {
	return &Controller{
		kubeClient:           kubeClient,
		recorder:             recorder,
		unavailableOfferings: unavailableOfferings,
		providerFactory:      providerFactory,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
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

	for _, node := range nodeList.Items {
		// Check if node is being interrupted
		interrupted, reason := c.isNodeInterrupted(ctx, &node)
		if !interrupted {
			continue
		}

		// Add interruption annotations to the node
		if err := c.markNodeAsInterrupted(ctx, &node, reason); err != nil {
			log.FromContext(ctx).Error(err, "failed to mark node as interrupted", "node", node.Name)
			continue
		}

		// Record interruption event
		c.recorder.Event(&node, v1.EventTypeWarning, "Interruption",
			fmt.Sprintf("Node is being interrupted by IBM Cloud: %s", reason))

		// Handle interruption based on deployment mode (VPC vs IKS)
		if err := c.handleInterruption(ctx, &node, reason); err != nil {
			log.FromContext(ctx).Error(err, "failed to handle interruption", "node", node.Name, "reason", reason)
			continue
		}
	}

	return reconciler.Result{RequeueAfter: time.Minute}, nil
}

// isNodeInterrupted checks if a node is being interrupted by IBM Cloud
func (c *Controller) isNodeInterrupted(ctx context.Context, node *v1.Node) (bool, InterruptionReason) {
	logger := log.FromContext(ctx).WithValues("node", node.Name)

	// Check if node already has interruption annotation (avoid duplicate processing)
	if _, exists := node.Annotations[InterruptionAnnotation]; exists {
		return false, ""
	}

	// 1. Check node readiness and health conditions
	if reason := c.checkNodeConditions(node); reason != "" {
		logger.V(1).Info("detected interruption from node conditions", "reason", reason)
		return true, reason
	}

	// 2. Check IBM Cloud-specific metadata (if instance ID is available)
	if instanceID := c.getInstanceIDFromNode(node); instanceID != "" {
		if reason := c.checkInstanceMetadata(ctx, instanceID); reason != "" {
			logger.V(1).Info("detected interruption from instance metadata", "reason", reason)
			return true, reason
		}
	}

	// 3. Check for capacity-related issues from node events or annotations
	if reason := c.checkCapacitySignals(node); reason != "" {
		logger.V(1).Info("detected capacity-related interruption", "reason", reason)
		return true, reason
	}

	return false, ""
}

// isCapacityRelated checks if the interruption is due to capacity constraints
func (c *Controller) isCapacityRelated(node *v1.Node, reason InterruptionReason) bool {
	switch reason {
	case CapacityUnavailable, NetworkResourceLimit:
		return true
	case HostMaintenance, InstanceHealthFailed, StorageFailure:
		return false
	default:
		// For unknown reasons, check node conditions for capacity indicators
		return c.hasCapacityPressure(node)
	}
}

// Name returns the name of the controller
func (c *Controller) Name() string {
	return "interruption"
}

// Register registers the controller with the manager
// markNodeAsInterrupted adds interruption annotations to the node
func (c *Controller) markNodeAsInterrupted(ctx context.Context, node *v1.Node, reason InterruptionReason) error {
	nodeCopy := node.DeepCopy()
	if nodeCopy.Annotations == nil {
		nodeCopy.Annotations = make(map[string]string)
	}

	nodeCopy.Annotations[InterruptionAnnotation] = "true"
	nodeCopy.Annotations[InterruptionReasonAnnotation] = string(reason)
	nodeCopy.Annotations[InterruptionTimeAnnotation] = time.Now().Format(time.RFC3339)

	return c.kubeClient.Update(ctx, nodeCopy)
}

// checkNodeConditions examines standard Kubernetes node conditions for health issues
func (c *Controller) checkNodeConditions(node *v1.Node) InterruptionReason {
	for _, condition := range node.Status.Conditions {
		switch condition.Type {
		case v1.NodeReady:
			if condition.Status != v1.ConditionTrue {
				// Check the reason for not being ready
				if strings.Contains(strings.ToLower(condition.Message), "capacity") ||
					strings.Contains(strings.ToLower(condition.Reason), "capacity") {
					return CapacityUnavailable
				}
				if strings.Contains(strings.ToLower(condition.Message), "network") {
					return NetworkResourceLimit
				}
				return InstanceHealthFailed
			}
		case v1.NodeMemoryPressure, v1.NodeDiskPressure, v1.NodePIDPressure:
			if condition.Status == v1.ConditionTrue {
				return CapacityUnavailable
			}
		case v1.NodeNetworkUnavailable:
			if condition.Status == v1.ConditionTrue {
				return NetworkResourceLimit
			}
		}
	}
	return ""
}

// getInstanceIDFromNode extracts IBM Cloud instance ID from node labels or annotations
func (c *Controller) getInstanceIDFromNode(node *v1.Node) string {
	// Try to get instance ID from provider ID (format: ibm:///zone/instance-id)
	if node.Spec.ProviderID != "" {
		parts := strings.Split(node.Spec.ProviderID, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-1]
		}
	}

	// Fallback to labels or annotations if available
	if instanceID, exists := node.Labels["ibm-cloud.kubernetes.io/instance-id"]; exists {
		return instanceID
	}
	if instanceID, exists := node.Annotations["ibm-cloud.kubernetes.io/instance-id"]; exists {
		return instanceID
	}

	return ""
}

// checkInstanceMetadata queries IBM Cloud metadata service for instance health
func (c *Controller) checkInstanceMetadata(ctx context.Context, instanceID string) InterruptionReason {
	// Note: This requires the controller to run on the actual node to access metadata service
	// In most cases, this won't be accessible from the control plane
	// This is here for completeness but may not be practically usable

	metadata, err := c.getInstanceMetadata(ctx)
	if err != nil {
		// Metadata service not accessible from this location (expected)
		return ""
	}

	// Check health state
	switch strings.ToLower(metadata.HealthState) {
	case "degraded":
		return InstanceHealthFailed
	case "faulted":
		return InstanceHealthFailed
	default:
		return ""
	}
}

// getInstanceMetadata fetches instance metadata from IBM Cloud metadata service
func (c *Controller) getInstanceMetadata(ctx context.Context) (*InstanceMetadata, error) {
	// Get authentication token
	tokenReq, err := http.NewRequestWithContext(ctx, "PUT", MetadataTokenURL, nil)
	if err != nil {
		return nil, err
	}
	tokenReq.Header.Set("Metadata-Flavor", "ibm")

	tokenResp, err := c.httpClient.Do(tokenReq)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := tokenResp.Body.Close(); closeErr != nil {
			log.FromContext(ctx).Error(closeErr, "Failed to close token response body")
		}
	}()

	if tokenResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get metadata token: %d", tokenResp.StatusCode)
	}

	var tokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	if decodeErr := json.NewDecoder(tokenResp.Body).Decode(&tokenResponse); decodeErr != nil {
		return nil, decodeErr
	}

	// Get instance metadata
	metadataReq, err := http.NewRequestWithContext(ctx, "GET", MetadataInstanceURL, nil)
	if err != nil {
		return nil, err
	}
	metadataReq.Header.Set("Authorization", "Bearer "+tokenResponse.AccessToken)

	metadataResp, err := c.httpClient.Do(metadataReq)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := metadataResp.Body.Close(); closeErr != nil {
			log.FromContext(ctx).Error(closeErr, "Failed to close metadata response body")
		}
	}()

	if metadataResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get instance metadata: %d", metadataResp.StatusCode)
	}

	var metadata InstanceMetadata
	if err := json.NewDecoder(metadataResp.Body).Decode(&metadata); err != nil {
		return nil, err
	}

	return &metadata, nil
}

// checkCapacitySignals looks for capacity-related issues in node labels/annotations
func (c *Controller) checkCapacitySignals(node *v1.Node) InterruptionReason {
	// Check for IBM Cloud-specific capacity annotations or labels
	for key, value := range node.Annotations {
		if strings.Contains(key, "ibm") && strings.Contains(strings.ToLower(value), "capacity") {
			return CapacityUnavailable
		}
		if strings.Contains(key, "ibm") && strings.Contains(strings.ToLower(value), "network") {
			return NetworkResourceLimit
		}
	}

	// Check for maintenance-related annotations
	if maintenance, exists := node.Annotations["ibm-cloud.kubernetes.io/maintenance"]; exists && maintenance == "true" {
		return HostMaintenance
	}

	return ""
}

// hasCapacityPressure checks if node has any resource pressure conditions
func (c *Controller) hasCapacityPressure(node *v1.Node) bool {
	for _, condition := range node.Status.Conditions {
		switch condition.Type {
		case v1.NodeMemoryPressure, v1.NodeDiskPressure, v1.NodePIDPressure:
			if condition.Status == v1.ConditionTrue {
				return true
			}
		}
	}
	return false
}

// handleInterruption processes an interruption event based on the deployment mode
func (c *Controller) handleInterruption(ctx context.Context, node *v1.Node, reason InterruptionReason) error {
	logger := log.FromContext(ctx).WithValues("node", node.Name, "reason", reason)

	// Get the node class to determine deployment mode
	nodeClass, err := c.getNodeClassForNode(ctx, node)
	if err != nil {
		logger.V(1).Info("could not determine node class, defaulting to VPC mode handling", "error", err)
		// Default to VPC mode handling if we can't determine the mode
		return c.handleVPCInterruption(ctx, node, reason)
	}

	// Determine provider mode
	var mode types.ProviderMode
	if c.providerFactory != nil {
		mode = c.providerFactory.GetProviderMode(nodeClass)
	} else {
		// Fallback: try to infer from node labels/annotations
		mode = c.inferModeFromNode(node)
	}

	logger.V(1).Info("handling interruption", "mode", mode)

	// Handle based on deployment mode
	switch mode {
	case types.IKSMode:
		return c.handleIKSInterruption(ctx, node, reason)
	case types.VPCMode:
		return c.handleVPCInterruption(ctx, node, reason)
	default:
		logger.Info("unknown deployment mode, defaulting to VPC handling", "mode", mode)
		return c.handleVPCInterruption(ctx, node, reason)
	}
}

// handleVPCInterruption handles interruptions for VPC mode (direct instance management)
func (c *Controller) handleVPCInterruption(ctx context.Context, node *v1.Node, reason InterruptionReason) error {
	logger := log.FromContext(ctx).WithValues("node", node.Name, "mode", "vpc")

	// Mark instance type as unavailable if capacity related
	if c.isCapacityRelated(node, reason) {
		instanceType := node.Labels["node.kubernetes.io/instance-type"]
		zone := node.Labels["topology.kubernetes.io/zone"]
		if instanceType != "" && zone != "" {
			c.unavailableOfferings.Add(instanceType+":"+zone, time.Now().Add(time.Hour))
			logger.Info("marked instance type as unavailable due to capacity issue",
				"instanceType", instanceType, "zone", zone)
		}
	}

	// Cordon the node first
	if !node.Spec.Unschedulable {
		nodeCopy := node.DeepCopy()
		nodeCopy.Spec.Unschedulable = true
		if err := c.kubeClient.Update(ctx, nodeCopy); err != nil {
			return fmt.Errorf("failed to cordon node: %w", err)
		}
		logger.Info("cordoned node for interruption")
	}

	// Delete the node to trigger immediate replacement
	if err := c.kubeClient.Delete(ctx, node); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to delete node: %w", err)
		}
	}
	logger.Info("deleted node to trigger replacement")

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
		if instanceType != "" && zone != "" {
			c.unavailableOfferings.Add(instanceType+":"+zone, time.Now().Add(time.Hour))
			logger.Info("marked instance type as unavailable due to capacity issue",
				"instanceType", instanceType, "zone", zone)
		}
	}

	// Cordon the node to prevent new pods from being scheduled
	if !node.Spec.Unschedulable {
		nodeCopy := node.DeepCopy()
		nodeCopy.Spec.Unschedulable = true
		if err := c.kubeClient.Update(ctx, nodeCopy); err != nil {
			return fmt.Errorf("failed to cordon node: %w", err)
		}
		logger.Info("cordoned node for interruption, letting IKS worker pool management handle replacement")
	}

	// For non-capacity issues (like maintenance), we might want to delete the node
	// to trigger faster replacement, but for capacity issues, cordoning is sufficient
	if !c.isCapacityRelated(node, reason) {
		// For infrastructure/maintenance issues, trigger replacement
		if err := c.kubeClient.Delete(ctx, node); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("failed to delete node: %w", err)
			}
		}
		logger.Info("deleted node to trigger replacement for non-capacity interruption")
	}

	return nil
}

// getNodeClassForNode retrieves the IBMNodeClass associated with a node
func (c *Controller) getNodeClassForNode(ctx context.Context, node *v1.Node) (*v1alpha1.IBMNodeClass, error) {
	// Try to get node class name from node labels
	nodeClassName, exists := node.Labels["karpenter.ibm.sh/nodeclass"]
	if !exists {
		// Fallback: try standard Karpenter label
		nodeClassName, exists = node.Labels["karpenter.sh/nodepool"]
		if !exists {
			return nil, fmt.Errorf("no nodeclass label found on node")
		}
	}

	nodeClass := &v1alpha1.IBMNodeClass{}
	if err := c.kubeClient.Get(ctx, client.ObjectKey{
		Name: nodeClassName,
	}, nodeClass); err != nil {
		return nil, fmt.Errorf("failed to get nodeclass %s: %w", nodeClassName, err)
	}

	return nodeClass, nil
}

// inferModeFromNode attempts to infer the deployment mode from node characteristics
func (c *Controller) inferModeFromNode(node *v1.Node) types.ProviderMode {
	// Check for IKS-specific labels or annotations
	if _, exists := node.Labels["ibm-cloud.kubernetes.io/iks-cluster-id"]; exists {
		return types.IKSMode
	}
	if _, exists := node.Annotations["ibm-cloud.kubernetes.io/iks-worker-pool"]; exists {
		return types.IKSMode
	}

	// Check provider ID format
	if strings.Contains(node.Spec.ProviderID, "iks://") {
		return types.IKSMode
	}

	// Default to VPC mode
	return types.VPCMode
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return builder.ControllerManagedBy(m).
		For(&v1.Node{}).
		Complete(singleton.AsReconciler(c))
}
