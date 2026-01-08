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
package cloudprovider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/status"
	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/metrics"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	ibmevents "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/events"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
)

const CloudProviderName = "ibmcloud"

const (
	NodeClassNotFoundDrift           cloudprovider.DriftReason = "NodeClassNotFound"
	NodeClassHashVersionChangedDrift cloudprovider.DriftReason = "NodeClassHashVersionChanged"
	NodeClassHashChangedDrift        cloudprovider.DriftReason = "NodeClassHashChanged"
	SubnetDrift                      cloudprovider.DriftReason = "SubnetDrift"
	ImageDrift                       cloudprovider.DriftReason = "ImageDrift"
	SecurityGroupDrift               cloudprovider.DriftReason = "SecurityGroupDrift"
)

var _ cloudprovider.CloudProvider = (*CloudProvider)(nil)

type CloudProvider struct {
	kubeClient client.Client
	recorder   events.Recorder
	ibmClient  *ibm.Client

	instanceTypeProvider  instancetype.Provider
	providerFactory       *providers.ProviderFactory
	subnetProvider        subnet.Provider
	defaultProviderMode   commonTypes.ProviderMode
	circuitBreakerManager *NodeClassCircuitBreakerManager
}

func New(kubeClient client.Client,
	recorder events.Recorder,
	ibmClient *ibm.Client,
	instanceTypeProvider instancetype.Provider,
	subnetProvider subnet.Provider,
	circuitBreakerConfig *CircuitBreakerConfig) *CloudProvider {
	// Determine the default provider mode based on environment
	defaultMode := commonTypes.VPCMode
	if os.Getenv("IKS_CLUSTER_ID") != "" {
		defaultMode = commonTypes.IKSMode
	}

	// Initialize per-NodeClass circuit breaker manager
	// If circuitBreakerConfig is nil, circuit breakers will be disabled
	logger := log.FromContext(context.Background()).WithName("circuit-breaker")
	circuitBreakerManager := NewNodeClassCircuitBreakerManager(circuitBreakerConfig, logger)

	return &CloudProvider{
		kubeClient: kubeClient,
		recorder:   recorder,
		ibmClient:  ibmClient,

		instanceTypeProvider:  instanceTypeProvider,
		providerFactory:       providers.NewProviderFactory(ibmClient, kubeClient, nil),
		subnetProvider:        subnetProvider,
		defaultProviderMode:   defaultMode,
		circuitBreakerManager: circuitBreakerManager,
	}
}

func (c *CloudProvider) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	log := log.FromContext(ctx).WithValues("providerID", providerID)

	// For Get operations without NodeClass, use a minimal NodeClass with default provider mode
	nodeClass := &v1alpha1.IBMNodeClass{}
	if c.defaultProviderMode == commonTypes.IKSMode {
		// Set IKS cluster ID from environment to trigger IKS mode
		nodeClass.Spec.IKSClusterID = os.Getenv("IKS_CLUSTER_ID")
	}

	// Get the appropriate instance provider
	instanceProvider, err := c.providerFactory.GetInstanceProvider(nodeClass)
	if err != nil {
		log.Error(err, "Failed to get instance provider")
		return nil, fmt.Errorf("getting instance provider, %w", err)
	}

	// Get the instance details
	node, err := instanceProvider.Get(ctx, providerID)
	if err != nil {
		log.Error(err, "Failed to get instance")
		return nil, fmt.Errorf("getting instance, %w", err)
	}
	log.Info("Got instance details")

	// Extract instance info from node
	instanceTypeName := node.Labels["node.kubernetes.io/instance-type"]
	zone := node.Labels["topology.kubernetes.io/zone"]
	log.Info("Found instance", "type", instanceTypeName, "zone", zone)

	instanceTypes, err := c.instanceTypeProvider.List(ctx, nodeClass)
	if err != nil {
		log.Error(err, "Failed to list instance types")
		return nil, fmt.Errorf("listing instance types, %w", err)
	}

	instanceType, _ := lo.Find(instanceTypes, func(i *cloudprovider.InstanceType) bool {
		return i.Name == instanceTypeName
	})
	if instanceType != nil {
		log.Info("Resolved instance type", "type", instanceType.Name)
	}

	nc := &karpv1.NodeClaim{
		Status: karpv1.NodeClaimStatus{
			ProviderID: providerID,
		},
	}

	if instanceType != nil {
		nc.Status.Capacity = instanceType.Capacity
		nc.Status.Allocatable = instanceType.Allocatable()
	}

	log.Info("Successfully retrieved NodeClaim",
		"providerID", nc.Status.ProviderID,
		"capacity", nc.Status.Capacity,
		"allocatable", nc.Status.Allocatable)
	return nc, nil
}

func (c *CloudProvider) List(ctx context.Context) ([]*karpv1.NodeClaim, error) {
	log := log.FromContext(ctx)

	// Get all nodes from the Kubernetes API
	nodeList := &corev1.NodeList{}
	if err := c.kubeClient.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("listing nodes, %w", err)
	}

	var nodeClaims []*karpv1.NodeClaim
	var providerErrors []error

	for _, node := range nodeList.Items {
		if node.Spec.ProviderID == "" {
			continue
		}

		// Skip nodes that don't have IBM provider IDs
		if !strings.HasPrefix(node.Spec.ProviderID, "ibm://") {
			continue
		}

		// Handle IKS managed nodes (format: ibm://account-id///cluster-id/worker-id)
		// These nodes are supported - the instance provider will map IKS worker to VPC instance

		// For List operations, use default provider mode
		nodeClass := &v1alpha1.IBMNodeClass{}
		if c.defaultProviderMode == commonTypes.IKSMode {
			nodeClass.Spec.IKSClusterID = os.Getenv("IKS_CLUSTER_ID")
		}

		instanceProvider, err := c.providerFactory.GetInstanceProvider(nodeClass)
		if err != nil {
			log.Error(err, "Failed to get instance provider", "node", node.Name, "providerID", node.Spec.ProviderID)
			providerErrors = append(providerErrors, fmt.Errorf("node %s: %w", node.Name, err))
			continue
		}

		_, err = instanceProvider.Get(ctx, node.Spec.ProviderID)
		if err != nil {
			// Check if this is an IKS managed cluster that doesn't allow Karpenter management
			if strings.Contains(err.Error(), "not configured for Karpenter management") {
				log.V(1).Info("Skipping IKS managed node - cluster not configured for Karpenter", "node", node.Name)
				continue
			}
			log.Error(err, "Failed to get instance details", "node", node.Name)
			continue
		}

		nc := &karpv1.NodeClaim{
			Status: karpv1.NodeClaimStatus{
				ProviderID: node.Spec.ProviderID,
			},
		}
		nodeClaims = append(nodeClaims, nc)
	}

	// Report any provider errors that occurred during listing
	if len(providerErrors) > 0 {
		log.Info("Some nodes were excluded from list due to provider errors",
			"excludedNodeCount", len(providerErrors),
			"totalNodes", len(nodeList.Items),
			"includedNodes", len(nodeClaims))
		// Log first few errors for debugging context
		for i, err := range providerErrors {
			if i >= 3 { // Only log first 3 to avoid spam
				log.Info("Additional provider errors omitted for brevity", "remainingErrors", len(providerErrors)-3)
				break
			}
			log.V(1).Info("Provider error detail", "error", err.Error())
		}
	}

	log.Info("Listed all instances")
	return nodeClaims, nil
}

func (c *CloudProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	log := log.FromContext(ctx).WithValues(
		"nodeClaim", nodeClaim.Name,
		"requirements", nodeClaim.Spec.Requirements,
		"resources", nodeClaim.Spec.Resources)
	log.Info("Started node creation")

	nodeClass := &v1alpha1.IBMNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Failed to resolve NodeClass")
			c.recorder.Publish(ibmevents.NodeClaimFailedToResolveNodeClass(nodeClaim))
		}
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("resolving node class, %w", err))
	}
	log.Info("Resolved NodeClass", "nodeClass", nodeClass.Name)

	// Log bootstrap mode for debugging
	bootstrapMode := "cloud-init"
	if nodeClass.Spec.BootstrapMode != nil {
		bootstrapMode = *nodeClass.Spec.BootstrapMode
	}
	log.Info("NodeClass configuration",
		"apiServerEndpoint", nodeClass.Spec.APIServerEndpoint,
		"bootstrapMode", bootstrapMode,
		"securityGroups", nodeClass.Spec.SecurityGroups,
		"image", nodeClass.Spec.Image,
		"subnet", nodeClass.Spec.Subnet,
		"vpc", nodeClass.Spec.VPC,
		"zone", nodeClass.Spec.Zone)

	// Check if the Ready condition exists and its status
	readyCondition := metav1.Condition{
		Type:   "Ready",
		Status: metav1.ConditionUnknown,
	}

	for _, condition := range nodeClass.Status.Conditions {
		if condition.Type == "Ready" {
			readyCondition = condition
			break
		}
	}

	if readyCondition.Status == metav1.ConditionFalse {
		log.Error(fmt.Errorf("%s", readyCondition.Message), "NodeClass not ready")
		return nil, cloudprovider.NewNodeClassNotReadyError(fmt.Errorf("%s", readyCondition.Message))
	}
	if readyCondition.Status == metav1.ConditionUnknown {
		log.Error(fmt.Errorf("%s", readyCondition.Message), "NodeClass readiness unknown")
		return nil, fmt.Errorf("resolving NodeClass readiness, NodeClass is in Ready=Unknown, %s", readyCondition.Message)
	}

	// Determine provider mode for this NodeClass
	providerMode := c.providerFactory.GetProviderMode(nodeClass)

	var compatible []*cloudprovider.InstanceType

	// For IKS mode, skip VPC instance type filtering - IKS provider handles its own flavor selection
	if providerMode == commonTypes.IKSMode {
		log.Info("IKS mode detected, skipped VPC instance type filtering")
		// For IKS, we pass nil compatible list - the IKS provider will handle flavor selection
		compatible = nil
	} else {
		instanceTypes, err := c.instanceTypeProvider.List(ctx, nodeClass)
		if err != nil {
			log.Error(err, "Failed to resolve instance types")
			return nil, fmt.Errorf("resolving instance types, %w", err)
		}
		log.Info("Resolved instance types")

		reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)
		compatible = lo.Filter(instanceTypes, func(i *cloudprovider.InstanceType, _ int) bool {
			reqErr := reqs.Compatible(i.Requirements, scheduling.AllowUndefinedWellKnownLabels)
			offeringsCompatible := len(i.Offerings.Compatible(reqs).Available()) > 0
			resourcesFit := resources.Fits(nodeClaim.Spec.Resources.Requests, i.Allocatable())

			if reqErr != nil {
				log.Info("Instance type incompatible with requirements",
					"type", i.Name,
					"error", reqErr)
				return false
			}
			if !offeringsCompatible {
				log.Info("No compatible offerings available",
					"type", i.Name)
				return false
			}
			if !resourcesFit {
				log.Info("Resources don't fit",
					"type", i.Name,
					"requested", nodeClaim.Spec.Resources.Requests,
					"allocatable", i.Allocatable())
				return false
			}
			return true
		})

		if len(compatible) == 0 {
			log.Error(nil, "No compatible instance types found")
			return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("all requested instance types were unavailable during launch"))
		}
		log.Info("Found compatible instance types", "count", len(compatible), "types", lo.Map(compatible, func(it *cloudprovider.InstanceType, _ int) string { return it.Name }))
	}

	// Per-NodeClass circuit breaker check - validate against failure rate and concurrency safeguards
	if cbErr := c.circuitBreakerManager.CanProvision(ctx, nodeClass.Name, nodeClass.Spec.Region, 0); cbErr != nil {
		// Get circuit breaker status for enhanced logging
		if cbStatus, statusErr := c.circuitBreakerManager.GetStateForNodeClass(nodeClass.Name, nodeClass.Spec.Region); statusErr == nil {
			log.Error(cbErr, "Circuit breaker blocked provisioning",
				"nodeClass", nodeClass.Name,
				"region", nodeClass.Spec.Region,
				"circuitBreakerState", cbStatus.State,
				"recentFailures", cbStatus.RecentFailures,
				"failureThreshold", cbStatus.FailureThreshold,
				"timeToRecovery", cbStatus.TimeToRecovery)
		} else {
			log.Error(cbErr, "Circuit breaker blocked provisioning",
				"nodeClass", nodeClass.Name,
				"region", nodeClass.Spec.Region)
		}
		c.recorder.Publish(ibmevents.NodeClaimCircuitBreakerBlocked(nodeClaim, cbErr.Error()))
		return nil, fmt.Errorf("provisioning temporarily blocked by circuit breaker: %w", cbErr)
	}

	// Get the appropriate instance provider based on NodeClass configuration
	instanceProvider, err := c.providerFactory.GetInstanceProvider(nodeClass)
	if err != nil {
		log.Error(err, "Failed to get instance provider")
		c.circuitBreakerManager.RecordFailure(nodeClass.Name, nodeClass.Spec.Region, err)
		return nil, fmt.Errorf("getting instance provider, %w", err)
	}

	node, err := instanceProvider.Create(ctx, nodeClaim, compatible)
	if err != nil {
		// Log the actual error details for better troubleshooting
		log.Error(err, "Failed to create instance",
			"nodeClass", nodeClass.Name,
			"region", nodeClass.Spec.Region,
			"zone", nodeClass.Spec.Zone,
			"instanceTypes", lo.Map(compatible, func(it *cloudprovider.InstanceType, _ int) string { return it.Name }))
		c.circuitBreakerManager.RecordFailure(nodeClass.Name, nodeClass.Spec.Region, err)
		return nil, fmt.Errorf("creating instance, %w", err)
	}
	log.Info("Created instance")

	// Record successful provisioning for this specific NodeClass
	c.circuitBreakerManager.RecordSuccess(nodeClass.Name, nodeClass.Spec.Region)
	log.Info("Successfully created instance", "providerID", node.Spec.ProviderID)

	instanceType, _ := lo.Find(compatible, func(i *cloudprovider.InstanceType) bool {
		return i.Name == node.Labels["node.kubernetes.io/instance-type"]
	})
	if instanceType != nil {
		log.Info("Selected instance type", "type", instanceType.Name, "capacity", instanceType.Capacity)
	}

	nc := &karpv1.NodeClaim{
		ObjectMeta: nodeClaim.ObjectMeta,
		Spec:       nodeClaim.Spec,
		Status: karpv1.NodeClaimStatus{
			ProviderID: node.Spec.ProviderID,
		},
	}

	// Set the Launched condition to indicate the instance was successfully created
	nc.StatusConditions().SetTrue(karpv1.ConditionTypeLaunched)

	// Populate NodeClaim labels similar to AWS provider reference
	// This ensures proper kubectl column display and NodeClaim labeling
	if nc.Labels == nil {
		nc.Labels = make(map[string]string)
	}

	// Copy essential labels from the created node first
	for key, value := range node.Labels {
		switch key {
		case "node.kubernetes.io/instance-type", // TYPE column
			"karpenter.sh/capacity-type",    // CAPACITY column
			"topology.kubernetes.io/zone",   // ZONE column
			"topology.kubernetes.io/region", // Region info
			"karpenter.sh/nodepool":         // Preserve nodepool label
			nc.Labels[key] = value
		}
	}

	// Ensure all single-valued requirements from the NodePool are reflected as labels
	// This prevents drift detection issues when requirements are added to the NodePool
	for _, req := range nodeClaim.Spec.Requirements {
		// For single-valued requirements, add them as labels if not already present
		if len(req.Values) == 1 && nc.Labels[req.Key] == "" {
			nc.Labels[req.Key] = req.Values[0]
		}
		// For multi-valued requirements where we've selected a specific instance type,
		// ensure the label reflects the actual selected value
		if req.Key == "node.kubernetes.io/instance-type" && instanceType != nil {
			nc.Labels[req.Key] = instanceType.Name
		}
	}

	// Populate labels from instance type requirements (similar to AWS)
	// These take precedence over node labels when available
	if instanceType != nil {
		for key, req := range instanceType.Requirements {
			if req.Len() == 1 {
				nc.Labels[key] = req.Values()[0]
			}
		}
	}

	// Set the node name in status for the NODE column
	// This will be populated once the node registers with the cluster
	nc.Status.NodeName = node.Name

	if instanceType != nil {
		nc.Status.Capacity = instanceType.Capacity
		nc.Status.Allocatable = instanceType.Allocatable()
	}

	annotations := map[string]string{
		v1alpha1.AnnotationIBMNodeClassHash:           nodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash],
		v1alpha1.AnnotationIBMNodeClassHashVersion:    v1alpha1.IBMNodeClassHashVersion,
		v1alpha1.AnnotationIBMNodeClaimSubnetID:       node.Annotations[v1alpha1.AnnotationIBMNodeClaimSubnetID],
		v1alpha1.AnnotationIBMNodeClaimSecurityGroups: node.Annotations[v1alpha1.AnnotationIBMNodeClaimSecurityGroups],
	}

	// Store resolved image ID only if available
	if nodeClass.Status.ResolvedImageID != "" {
		annotations[v1alpha1.AnnotationIBMNodeClaimImageID] = nodeClass.Status.ResolvedImageID
	}

	nc.Annotations = lo.Assign(nc.Annotations, annotations)

	log.Info("Node creation completed successfully",
		"providerID", nc.Status.ProviderID,
		"capacity", nc.Status.Capacity,
		"allocatable", nc.Status.Allocatable)
	return nc, nil
}

func (c *CloudProvider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	log := log.FromContext(ctx).WithValues("nodeClaim", nodeClaim.Name, "providerID", nodeClaim.Status.ProviderID)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
		},
		Spec: corev1.NodeSpec{
			ProviderID: nodeClaim.Status.ProviderID,
		},
	}

	// For Delete operations, get the NodeClass to determine provider mode
	nodeClass := &v1alpha1.IBMNodeClass{}
	// Try to get the NodeClass from the nodeClaim's nodeClassRef
	if nodeClaim.Spec.NodeClassRef != nil && nodeClaim.Spec.NodeClassRef.Name != "" {
		if getErr := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); getErr != nil {
			// If we can't get NodeClass, use default provider mode
			if c.defaultProviderMode == commonTypes.IKSMode {
				nodeClass.Spec.IKSClusterID = os.Getenv("IKS_CLUSTER_ID")
			}
		}
	} else {
		// No NodeClass reference, use default provider mode
		if c.defaultProviderMode == commonTypes.IKSMode {
			nodeClass.Spec.IKSClusterID = os.Getenv("IKS_CLUSTER_ID")
		}
	}

	// Get the appropriate instance provider
	instanceProvider, err := c.providerFactory.GetInstanceProvider(nodeClass)
	if err != nil {
		log.Error(err, "Failed to get instance provider")
		return fmt.Errorf("getting instance provider, %w", err)
	}

	if err := instanceProvider.Delete(ctx, node); err != nil {
		// Return NodeClaimNotFoundError unchanged - this is expected during cleanup
		if cloudprovider.IsNodeClaimNotFoundError(err) {
			log.Info("Instance already deleted or not found")
			return err
		}
		log.Error(err, "Failed to delete node")
		return err
	}

	log.Info("Successfully deleted node")
	return nil
}

func (c *CloudProvider) GetInstanceTypes(ctx context.Context, nodePool *karpv1.NodePool) ([]*cloudprovider.InstanceType, error) {
	log := log.FromContext(ctx).WithValues("nodePool", nodePool.Name)

	nodeClass := &v1alpha1.IBMNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodePool.Spec.Template.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Failed to resolve NodeClass")
			c.recorder.Publish(ibmevents.NodePoolFailedToResolveNodeClass(nodePool))
		}
		return nil, err
	}
	log.Info("Resolved NodeClass", "name", nodeClass.Name)

	instanceTypes, err := c.instanceTypeProvider.List(ctx, nodeClass)
	if err != nil {
		log.Error(err, "Failed to list instance types")
		return nil, err
	}
	log.Info("Got instance types for NodePool")

	// Filter instance types based on NodePool requirements
	reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodePool.Spec.Template.Spec.Requirements...)
	compatible := lo.Filter(instanceTypes, func(it *cloudprovider.InstanceType, _ int) bool {
		return reqs.Compatible(it.Requirements, scheduling.AllowUndefinedWellKnownLabels) == nil
	})

	log.Info("Successfully retrieved and filtered instance types",
		"total", len(instanceTypes),
		"compatible", len(compatible))
	return compatible, nil
}

func (c *CloudProvider) IsDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim) (driftReason cloudprovider.DriftReason, err error) {
	log := log.FromContext(ctx).WithValues("nodeClaim", nodeClaim.Name, "providerID", nodeClaim.Status.ProviderID)

	nodeClassName := nodeClaim.Spec.NodeClassRef.Name
	start := time.Now()
	defer func() {
		metrics.DriftDetectionDuration.WithLabelValues(
			nodeClassName,
		).Observe(time.Since(start).Seconds())

		if driftReason != "" {
			metrics.DriftDetectionsTotal.WithLabelValues(string(driftReason), nodeClassName).Inc()
		}
	}()

	// Get the NodeClass
	nodeClass, driftReason, err := c.getNodeClassForDrift(ctx, log, nodeClaim)
	if err != nil {
		return "", err
	}
	if driftReason != "" {
		return driftReason, nil
	}

	if driftReason = c.isNodeClassHashVersionDrifted(ctx, log, nodeClaim); driftReason != "" {
		return driftReason, nil
	}

	if driftReason = c.isNodeClassHashDrifted(ctx, log, nodeClaim, nodeClass); driftReason != "" {
		return driftReason, nil
	}

	if driftReason = c.isImageDrifted(ctx, log, nodeClaim, nodeClass); driftReason != "" {
		return driftReason, nil
	}

	driftReason, err = c.isSubnetDrifted(ctx, log, nodeClaim, nodeClass)
	if err != nil {
		return "", err
	}
	if driftReason != "" {
		return driftReason, nil
	}

	driftReason, err = c.areSecurityGroupsDrifted(ctx, log, nodeClaim, nodeClass)
	if err != nil {
		return "", err
	}
	if driftReason != "" {
		return driftReason, nil
	}

	log.Info("Checked if node has drifted")
	return "", nil
}

func (c *CloudProvider) getNodeClassForDrift(ctx context.Context, log logr.Logger, nodeClaim *karpv1.NodeClaim) (*v1alpha1.IBMNodeClass, cloudprovider.DriftReason, error) {
	nodeClass := &v1alpha1.IBMNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "NodeClass not found")
			return nil, NodeClassNotFoundDrift, nil
		}
		return nil, "", fmt.Errorf("getting nodeclass, %w", err)
	}
	return nodeClass, "", nil
}

func (c *CloudProvider) isNodeClassHashVersionDrifted(ctx context.Context, log logr.Logger, nodeClaim *karpv1.NodeClaim) cloudprovider.DriftReason {
	// Get the current hash version from the node's annotations
	currentVersion := nodeClaim.Annotations[v1alpha1.AnnotationIBMNodeClassHashVersion]

	// Check if the hash version matches
	if currentVersion != v1alpha1.IBMNodeClassHashVersion {
		log.Info("NodeClass hash version mismatch", "current", currentVersion, "expected", v1alpha1.IBMNodeClassHashVersion)
		return NodeClassHashVersionChangedDrift
	}
	return ""
}

func (c *CloudProvider) isNodeClassHashDrifted(ctx context.Context, log logr.Logger, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.IBMNodeClass) cloudprovider.DriftReason {
	// Get the current hash from the node's annotations
	currentHash := nodeClaim.Annotations[v1alpha1.AnnotationIBMNodeClassHash]
	expectedHash := nodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash]

	// Check if the hash matches
	if expectedHash != currentHash {
		log.Info("NodeClass hash mismatch", "current", currentHash, "expected", expectedHash)
		return NodeClassHashChangedDrift
	}
	return ""
}

func (c *CloudProvider) isImageDrifted(ctx context.Context, log logr.Logger, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.IBMNodeClass) cloudprovider.DriftReason {
	// Get the stored image id from the node's annotations
	storedImageID := nodeClaim.Annotations[v1alpha1.AnnotationIBMNodeClaimImageID]
	currentImageID := nodeClass.Status.ResolvedImageID

	// Check if the ImageID matches
	if storedImageID != "" && currentImageID != "" && storedImageID != currentImageID {
		log.Info("Node image drift detected", "storedImageID", storedImageID, "currentImageID", currentImageID)
		return ImageDrift
	}
	return ""
}

func (c *CloudProvider) isSubnetDrifted(ctx context.Context, log logr.Logger, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.IBMNodeClass) (cloudprovider.DriftReason, error) {
	storedSubnetID := nodeClaim.Annotations[v1alpha1.AnnotationIBMNodeClaimSubnetID]
	if storedSubnetID == "" {
		return "", nil
	}

	// Case 1: Explicit subnet in spec - compare directly
	if nodeClass.Spec.Subnet != "" {
		if storedSubnetID != nodeClass.Spec.Subnet {
			log.Info("Subnet drift detected", "storedSubnetID", storedSubnetID, "specSubnet", nodeClass.Spec.Subnet)
			return SubnetDrift, nil
		}
		return "", nil
	}

	// Case 2: PlacementStrategy - check against Status.SelectedSubnets
	validSubnets := nodeClass.Status.SelectedSubnets
	if len(validSubnets) == 0 {
		log.Info("No subnets in Status.SelectedSubnets, skipping drift check", "nodeClass", nodeClass.Name)
		return "", nil
	}

	for _, curSubnet := range validSubnets {
		if curSubnet == storedSubnetID {
			return "", nil
		}
	}

	log.Info("Subnet drift detected", "storedSubnetID", storedSubnetID, "validSubnets", validSubnets)
	return SubnetDrift, nil
}

func (c *CloudProvider) areSecurityGroupsDrifted(ctx context.Context, log logr.Logger, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.IBMNodeClass) (cloudprovider.DriftReason, error) {
	storedSGs := nodeClaim.Annotations[v1alpha1.AnnotationIBMNodeClaimSecurityGroups]
	if storedSGs == "" {
		return "", nil
	}

	storedSGSet := sets.New(strings.Split(storedSGs, ",")...)

	// Use resolved security groups from status (populated by status controller)
	if len(nodeClass.Status.ResolvedSecurityGroups) == 0 {
		log.Info("No security groups in Status.ResolvedSecurityGroups, skipping drift check", "nodeClass", nodeClass.Name)
		return "", nil
	}

	currentSGSet := sets.New(nodeClass.Status.ResolvedSecurityGroups...)

	if !storedSGSet.Equal(currentSGSet) {
		log.Info("Security group drift detected", "storedSecurityGroups", storedSGSet.UnsortedList(), "currentSecurityGroups", currentSGSet.UnsortedList())
		return SecurityGroupDrift, nil
	}
	return "", nil
}

func (c *CloudProvider) Name() string {
	return CloudProviderName
}

// GetIBMClient returns the IBM client
func (c *CloudProvider) GetIBMClient() *ibm.Client {
	return c.ibmClient
}

// GetInstanceTypeProvider returns the instance type provider
func (c *CloudProvider) GetInstanceTypeProvider() instancetype.Provider {
	return c.instanceTypeProvider
}

// GetSubnetProvider returns the subnet provider
func (c *CloudProvider) GetSubnetProvider() subnet.Provider {
	return c.subnetProvider
}

// GetSupportedNodeClasses returns the list of supported node class types for the IBM cloud provider
func (c *CloudProvider) GetSupportedNodeClasses() []status.Object {
	return []status.Object{&v1alpha1.IBMNodeClass{}}
}

// RepairPolicies returns the repair policies for the IBM cloud provider
// These define conditions that Karpenter should monitor to detect unhealthy nodes
func (c *CloudProvider) RepairPolicies() []cloudprovider.RepairPolicy {
	return []cloudprovider.RepairPolicy{
		// Common node conditions that indicate unhealthy state
		{
			ConditionType:      corev1.NodeReady,
			ConditionStatus:    corev1.ConditionFalse,
			TolerationDuration: 5 * time.Minute, // Wait 5 minutes before considering node for termination
		},
		{
			ConditionType:      corev1.NodeReady,
			ConditionStatus:    corev1.ConditionUnknown,
			TolerationDuration: 5 * time.Minute, // Wait 5 minutes for unknown state
		},
		{
			ConditionType:      corev1.NodeMemoryPressure,
			ConditionStatus:    corev1.ConditionTrue,
			TolerationDuration: 10 * time.Minute, // Give more time for memory pressure recovery
		},
		{
			ConditionType:      corev1.NodeDiskPressure,
			ConditionStatus:    corev1.ConditionTrue,
			TolerationDuration: 5 * time.Minute, // Disk pressure should be addressed quickly
		},
		{
			ConditionType:      corev1.NodePIDPressure,
			ConditionStatus:    corev1.ConditionTrue,
			TolerationDuration: 5 * time.Minute, // PID pressure indicates serious issues
		},
	}
}
