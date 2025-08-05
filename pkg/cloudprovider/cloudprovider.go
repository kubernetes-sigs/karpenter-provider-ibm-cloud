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
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	ibmevents "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/events"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
	commonTypes "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
)

const CloudProviderName = "ibmcloud"

var _ cloudprovider.CloudProvider = (*CloudProvider)(nil)

type CloudProvider struct {
	kubeClient client.Client
	recorder   events.Recorder
	ibmClient  *ibm.Client

	instanceTypeProvider instancetype.Provider
	providerFactory      *providers.ProviderFactory
	subnetProvider       subnet.Provider
	defaultProviderMode  commonTypes.ProviderMode
	circuitBreaker       *CircuitBreaker
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

	// Initialize circuit breaker with provided config or default
	logger := log.FromContext(context.Background()).WithName("circuit-breaker")
	if circuitBreakerConfig == nil {
		circuitBreakerConfig = DefaultCircuitBreakerConfig()
	}
	circuitBreaker := NewCircuitBreaker(circuitBreakerConfig, logger)

	return &CloudProvider{
		kubeClient: kubeClient,
		recorder:   recorder,
		ibmClient:  ibmClient,

		instanceTypeProvider: instanceTypeProvider,
		providerFactory:      providers.NewProviderFactory(ibmClient, kubeClient, nil),
		subnetProvider:       subnetProvider,
		defaultProviderMode:  defaultMode,
		circuitBreaker:       circuitBreaker,
	}
}

func (c *CloudProvider) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	log := log.FromContext(ctx).WithValues("providerID", providerID)
	log.Info("Getting instance details")

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

	// Extract instance info from node
	instanceTypeName := node.Labels["node.kubernetes.io/instance-type"]
	zone := node.Labels["topology.kubernetes.io/zone"]
	log.Info("Found instance", "type", instanceTypeName, "zone", zone)

	instanceTypes, err := c.instanceTypeProvider.List(ctx)
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
	log.Info("Listing all instances")

	// Get all nodes from the Kubernetes API
	nodeList := &corev1.NodeList{}
	if err := c.kubeClient.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("listing nodes, %w", err)
	}

	var nodeClaims []*karpv1.NodeClaim
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
			continue // Skip if we can't get provider
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

	return nodeClaims, nil
}

func (c *CloudProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	log := log.FromContext(ctx).WithValues(
		"nodeClaim", nodeClaim.Name,
		"requirements", nodeClaim.Spec.Requirements,
		"resources", nodeClaim.Spec.Resources)
	log.Info("Starting node creation")

	nodeClass := &v1alpha1.IBMNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Failed to resolve NodeClass")
			c.recorder.Publish(ibmevents.NodeClaimFailedToResolveNodeClass(nodeClaim))
		}
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("resolving node class, %w", err))
	}
	log.Info("Resolved NodeClass", "nodeClass", nodeClass.Name)

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

	log.Info("Resolving instance types")
	instanceTypes, err := c.instanceTypeProvider.List(ctx)
	if err != nil {
		log.Error(err, "Failed to resolve instance types")
		return nil, fmt.Errorf("resolving instance types, %w", err)
	}

	reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)
	compatible := lo.Filter(instanceTypes, func(i *cloudprovider.InstanceType, _ int) bool {
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

	log.Info("Creating instance")

	// Circuit breaker check - validate against failure rate and concurrency safeguards
	if cbErr := c.circuitBreaker.CanProvision(ctx, nodeClass.Name, nodeClass.Spec.Region, 0); cbErr != nil {
		log.Error(cbErr, "Circuit breaker blocked provisioning",
			"nodeClass", nodeClass.Name,
			"region", nodeClass.Spec.Region)
		c.recorder.Publish(ibmevents.NodeClaimCircuitBreakerBlocked(nodeClaim, cbErr.Error()))
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("circuit breaker blocked provisioning: %w", cbErr))
	}

	// Get the appropriate instance provider based on NodeClass configuration
	instanceProvider, err := c.providerFactory.GetInstanceProvider(nodeClass)
	if err != nil {
		log.Error(err, "Failed to get instance provider")
		c.circuitBreaker.RecordFailure(nodeClass.Name, nodeClass.Spec.Region, err)
		return nil, fmt.Errorf("getting instance provider, %w", err)
	}

	node, err := instanceProvider.Create(ctx, nodeClaim)
	if err != nil {
		log.Error(err, "Failed to create instance")
		c.circuitBreaker.RecordFailure(nodeClass.Name, nodeClass.Spec.Region, err)
		return nil, fmt.Errorf("creating instance, %w", err)
	}

	// Record successful provisioning
	c.circuitBreaker.RecordSuccess(nodeClass.Name, nodeClass.Spec.Region)
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

	nc.Annotations = lo.Assign(nc.Annotations, map[string]string{
		v1alpha1.AnnotationIBMNodeClassHash:        nodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash],
		v1alpha1.AnnotationIBMNodeClassHashVersion: v1alpha1.IBMNodeClassHashVersion,
	})

	log.Info("Node creation completed successfully",
		"providerID", nc.Status.ProviderID,
		"capacity", nc.Status.Capacity,
		"allocatable", nc.Status.Allocatable)
	return nc, nil
}

func (c *CloudProvider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	log := log.FromContext(ctx).WithValues("nodeClaim", nodeClaim.Name, "providerID", nodeClaim.Status.ProviderID)
	log.Info("Deleting node")

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
	log.Info("Getting instance types for NodePool")

	nodeClass := &v1alpha1.IBMNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodePool.Spec.Template.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Failed to resolve NodeClass")
			c.recorder.Publish(ibmevents.NodePoolFailedToResolveNodeClass(nodePool))
		}
		return nil, err
	}
	log.Info("Resolved NodeClass", "name", nodeClass.Name)

	instanceTypes, err := c.instanceTypeProvider.List(ctx)
	if err != nil {
		log.Error(err, "Failed to list instance types")
		return nil, err
	}
	log.Info("Successfully retrieved instance types", "count", len(instanceTypes))
	return instanceTypes, nil
}

func (c *CloudProvider) IsDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim) (cloudprovider.DriftReason, error) {
	log := log.FromContext(ctx).WithValues("nodeClaim", nodeClaim.Name, "providerID", nodeClaim.Status.ProviderID)
	log.Info("Checking if node has drifted")

	// Get the current hash from the node's annotations
	currentHash := nodeClaim.Annotations[v1alpha1.AnnotationIBMNodeClassHash]
	currentVersion := nodeClaim.Annotations[v1alpha1.AnnotationIBMNodeClassHashVersion]

	// Get the NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "NodeClass not found")
			return "NodeClassNotFound", nil
		}
		return "", fmt.Errorf("getting nodeclass, %w", err)
	}

	// Check if the hash version matches
	if currentVersion != v1alpha1.IBMNodeClassHashVersion {
		log.Info("NodeClass hash version mismatch", "current", currentVersion, "expected", v1alpha1.IBMNodeClassHashVersion)
		return "NodeClassHashVersionChanged", nil
	}

	// Check if the hash matches
	expectedHash := nodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash]
	if expectedHash != currentHash {
		log.Info("NodeClass hash mismatch", "current", currentHash, "expected", expectedHash)
		return "NodeClassHashChanged", nil
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
