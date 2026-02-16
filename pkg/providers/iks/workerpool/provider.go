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

package workerpool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

const (
	// KarpenterManagedLabel is the label applied to dynamically created pools
	KarpenterManagedLabel = "karpenter.sh/managed"

	// maxPoolNameLength is the maximum length for IKS worker pool names
	// IKS API accepts up to 63 characters (Kubernetes naming constraint)
	maxPoolNameLength = 63
)

// poolNamePattern validates IKS pool names: lowercase alphanumeric, can contain hyphens,
// must start with a letter, cannot end with hyphen
var poolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$|^[a-z]$`)

// IKSWorkerPoolProvider implements IKS-specific worker pool provisioning
type IKSWorkerPoolProvider struct {
	client     *ibm.Client
	kubeClient client.Client
}

// NewIKSWorkerPoolProvider creates a new IKS worker pool provider
func NewIKSWorkerPoolProvider(client *ibm.Client, kubeClient client.Client) (commonTypes.IKSWorkerPoolProvider, error) {
	if client == nil {
		return nil, fmt.Errorf("IBM client cannot be nil")
	}

	return &IKSWorkerPoolProvider{
		client:     client,
		kubeClient: kubeClient,
	}, nil
}

// Create provisions a new worker by resizing an IKS worker pool
// Note: instanceTypes parameter is ignored for IKS (compatibility with VPC interface)
func (p *IKSWorkerPoolProvider) Create(ctx context.Context, nodeClaim *v1.NodeClaim, instanceTypes []*cloudprovider.InstanceType) (*corev1.Node, error) {
	logger := log.FromContext(ctx)

	if p.kubeClient == nil {
		return nil, fmt.Errorf("kubernetes client not set")
	}

	// Get the NodeClass to extract configuration
	nodeClass := &v1alpha1.IBMNodeClass{}
	if getErr := p.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); getErr != nil {
		return nil, fmt.Errorf("getting NodeClass %s: %w", nodeClaim.Spec.NodeClassRef.Name, getErr)
	}

	// Get cluster ID from NodeClass or environment
	clusterID := nodeClass.Spec.IKSClusterID
	if clusterID == "" {
		clusterID = os.Getenv("IKS_CLUSTER_ID")
	}
	if clusterID == "" {
		return nil, fmt.Errorf("IKS cluster ID not found in nodeClass.spec.iksClusterID or IKS_CLUSTER_ID environment variable")
	}

	// Check if client is initialized
	if p.client == nil {
		return nil, fmt.Errorf("IBM client is not initialized")
	}

	// Get IKS client
	iksClient, err := p.client.GetIKSClient()
	if err != nil {
		return nil, fmt.Errorf("getting IKS client: %w", err)
	}

	// Extract requested instance type using same logic as VPC mode
	requestedInstanceType := nodeClass.Spec.InstanceProfile
	if requestedInstanceType == "" {
		requestedInstanceType = nodeClaim.Labels["node.kubernetes.io/instance-type"]
	}

	logger.Info("Initiated IKS worker creation", "cluster_id", clusterID, "requested_instance_type", requestedInstanceType)

	// Find or select appropriate worker pool
	poolID, selectedInstanceType, err := p.findOrSelectWorkerPool(ctx, iksClient, clusterID, nodeClass, requestedInstanceType)
	if err != nil {
		return nil, fmt.Errorf("finding worker pool: %w", err)
	}

	logger.Info("Incremented worker pool", "pool_id", poolID, "instance_type", selectedInstanceType)

	// Atomically increment the worker pool size
	// This prevents race conditions where concurrent requests could read the same
	// pool size and both increment to the same value
	newSize, err := iksClient.IncrementWorkerPool(ctx, clusterID, poolID)
	if err != nil {
		return nil, fmt.Errorf("incrementing worker pool %s: %w", poolID, err)
	}

	logger.Info("Worker pool incremented", "pool_id", poolID, "new_size", newSize)

	// Create a placeholder node representation
	// The actual node will be created by IKS and joined to the cluster
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
			Labels: map[string]string{
				"karpenter.sh/managed":             "true",
				"karpenter-ibm.sh/cluster-id":      clusterID,
				"karpenter-ibm.sh/worker-pool-id":  poolID,
				"karpenter-ibm.sh/zone":            nodeClass.Spec.Zone,
				"karpenter-ibm.sh/region":          nodeClass.Spec.Region,
				"karpenter-ibm.sh/instance-type":   selectedInstanceType,
				"node.kubernetes.io/instance-type": selectedInstanceType,
				"topology.kubernetes.io/zone":      nodeClass.Spec.Zone,
				"topology.kubernetes.io/region":    nodeClass.Spec.Region,
				"karpenter.sh/capacity-type":       "on-demand",
				"karpenter.sh/nodepool":            nodeClaim.Labels["karpenter.sh/nodepool"],
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: fmt.Sprintf("ibm:///%s/%s", nodeClass.Spec.Region, nodeClaim.Name),
		},
		Status: corev1.NodeStatus{
			Phase: corev1.NodePending,
			Conditions: []corev1.NodeCondition{
				{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionUnknown,
					LastHeartbeatTime:  metav1.Now(),
					LastTransitionTime: metav1.Now(),
					Reason:             "NodeCreating",
					Message:            "Node is being created via IKS worker pool resize",
				},
			},
		},
	}

	logger.Info("IKS worker pool resize initiated", "pool_id", poolID, "new_size", newSize)
	return node, nil
}

// Delete removes a worker by resizing down the IKS worker pool
func (p *IKSWorkerPoolProvider) Delete(ctx context.Context, node *corev1.Node) error {
	logger := log.FromContext(ctx)

	// Extract cluster and pool information from node labels
	clusterID := node.Labels["karpenter-ibm.sh/cluster-id"]
	poolID := node.Labels["karpenter-ibm.sh/worker-pool-id"]

	if clusterID == "" || poolID == "" {
		return fmt.Errorf("cluster ID or pool ID not found in node labels")
	}

	// Check if client is initialized
	if p.client == nil {
		return fmt.Errorf("IBM client is not initialized")
	}

	// Get IKS client
	iksClient, err := p.client.GetIKSClient()
	if err != nil {
		return fmt.Errorf("getting IKS client: %w", err)
	}

	logger.Info("Decremented worker pool", "pool_id", poolID)

	// Atomically decrement the worker pool size
	newSize, err := iksClient.DecrementWorkerPool(ctx, clusterID, poolID)
	if err != nil {
		return fmt.Errorf("decrementing worker pool %s: %w", poolID, err)
	}

	logger.Info("Worker pool decremented successfully", "pool_id", poolID, "new_size", newSize)
	return nil
}

// Get retrieves information about a worker (not applicable for IKS worker pools)
func (p *IKSWorkerPoolProvider) Get(ctx context.Context, providerID string) (*corev1.Node, error) {
	// For IKS mode, individual worker tracking is handled by IKS
	// This method would need to query IKS APIs to find the specific worker
	return nil, fmt.Errorf("get operation not implemented for IKS worker pool provider")
}

// List returns all workers (not directly applicable for IKS worker pools)
func (p *IKSWorkerPoolProvider) List(ctx context.Context) ([]*corev1.Node, error) {
	// For IKS mode, worker listing would need to enumerate all clusters and their workers
	// This is complex and may not be needed for normal Karpenter operations
	return nil, fmt.Errorf("list operation not implemented for IKS worker pool provider")
}

// ResizePool resizes a worker pool to the specified size
func (p *IKSWorkerPoolProvider) ResizePool(ctx context.Context, clusterID, poolID string, newSize int) error {
	if p.client == nil {
		return fmt.Errorf("IBM client is not initialized")
	}

	iksClient, err := p.client.GetIKSClient()
	if err != nil {
		return fmt.Errorf("getting IKS client: %w", err)
	}

	return iksClient.ResizeWorkerPool(ctx, clusterID, poolID, newSize)
}

// GetPool retrieves information about a worker pool
func (p *IKSWorkerPoolProvider) GetPool(ctx context.Context, clusterID, poolID string) (*commonTypes.WorkerPool, error) {
	if p.client == nil {
		return nil, fmt.Errorf("IBM client is not initialized")
	}

	iksClient, err := p.client.GetIKSClient()
	if err != nil {
		return nil, fmt.Errorf("getting IKS client: %w", err)
	}

	pool, err := iksClient.GetWorkerPool(ctx, clusterID, poolID)
	if err != nil {
		return nil, err
	}

	// Convert from IKS client type to common type
	return &commonTypes.WorkerPool{
		ID:          pool.ID,
		Name:        pool.Name,
		Flavor:      pool.Flavor,
		Zone:        pool.Zone,
		SizePerZone: pool.SizePerZone,
		ActualSize:  pool.ActualSize,
		State:       pool.State,
		Labels:      pool.Labels,
	}, nil
}

// ListPools returns all worker pools for a cluster
func (p *IKSWorkerPoolProvider) ListPools(ctx context.Context, clusterID string) ([]*commonTypes.WorkerPool, error) {
	if p.client == nil {
		return nil, fmt.Errorf("IBM client is not initialized")
	}

	iksClient, err := p.client.GetIKSClient()
	if err != nil {
		return nil, fmt.Errorf("getting IKS client: %w", err)
	}

	pools, err := iksClient.ListWorkerPools(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	// Convert from IKS client types to common types
	var commonPools []*commonTypes.WorkerPool
	for _, pool := range pools {
		commonPools = append(commonPools, &commonTypes.WorkerPool{
			ID:          pool.ID,
			Name:        pool.Name,
			Flavor:      pool.Flavor,
			Zone:        pool.Zone,
			SizePerZone: pool.SizePerZone,
			ActualSize:  pool.ActualSize,
			State:       pool.State,
			Labels:      pool.Labels,
		})
	}

	return commonPools, nil
}

// CreatePool creates a new worker pool with the specified configuration
func (p *IKSWorkerPoolProvider) CreatePool(ctx context.Context, clusterID string, request *commonTypes.CreatePoolRequest) (*commonTypes.WorkerPool, error) {
	logger := log.FromContext(ctx)

	if p.client == nil {
		return nil, fmt.Errorf("IBM client is not initialized")
	}

	iksClient, err := p.client.GetIKSClient()
	if err != nil {
		return nil, fmt.Errorf("getting IKS client: %w", err)
	}

	// Build zone configuration
	zones := []ibm.WorkerPoolZone{
		{
			ID:       request.Zone,
			SubnetID: request.SubnetID,
		},
	}

	// Build the create request
	createRequest := &ibm.WorkerPoolCreateRequest{
		Name:           request.Name,
		Flavor:         request.Flavor,
		SizePerZone:    request.SizePerZone,
		Zones:          zones,
		Labels:         request.Labels,
		DiskEncryption: request.DiskEncryption,
		VpcID:          request.VpcID,
	}

	logger.Info("Initiated dynamic worker pool creation",
		"name", request.Name,
		"flavor", request.Flavor,
		"zone", request.Zone,
		"size", request.SizePerZone)

	pool, err := iksClient.CreateWorkerPool(ctx, clusterID, createRequest)
	if err != nil {
		return nil, fmt.Errorf("creating worker pool: %w", err)
	}

	logger.Info("Dynamic worker pool created", "pool_id", pool.ID, "pool_name", pool.Name)

	return &commonTypes.WorkerPool{
		ID:          pool.ID,
		Name:        pool.Name,
		Flavor:      pool.Flavor,
		Zone:        pool.Zone,
		SizePerZone: pool.SizePerZone,
		ActualSize:  pool.ActualSize,
		State:       pool.State,
		Labels:      pool.Labels,
	}, nil
}

// DeletePool deletes a worker pool from the cluster
func (p *IKSWorkerPoolProvider) DeletePool(ctx context.Context, clusterID, poolID string) error {
	logger := log.FromContext(ctx)

	if p.client == nil {
		return fmt.Errorf("IBM client is not initialized")
	}

	iksClient, err := p.client.GetIKSClient()
	if err != nil {
		return fmt.Errorf("getting IKS client: %w", err)
	}

	logger.Info("Initiated worker pool deletion", "cluster_id", clusterID, "pool_id", poolID)

	if err := iksClient.DeleteWorkerPool(ctx, clusterID, poolID); err != nil {
		return fmt.Errorf("deleting worker pool: %w", err)
	}

	logger.Info("Worker pool deleted", "pool_id", poolID)
	return nil
}

// generatePoolName creates a unique name for a dynamically created pool.
// Returns a name that conforms to IKS naming constraints:
// - Max 63 characters
// - Lowercase alphanumeric and hyphens
// - Must start with a letter
// - Cannot end with a hyphen
func generatePoolName(prefix, flavor string) string {
	// Generate a short random suffix (3 bytes = 6 hex chars)
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		// Fallback if random fails - use truncated flavor only
		sanitized := sanitizePoolNameComponent(flavor)
		return truncatePoolName(prefix + "-" + sanitized)
	}
	suffix := hex.EncodeToString(b)

	// Sanitize flavor name
	sanitizedFlavor := sanitizePoolNameComponent(flavor)

	// Build name: prefix-flavor-suffix
	name := fmt.Sprintf("%s-%s-%s", prefix, sanitizedFlavor, suffix)

	return truncatePoolName(name)
}

// sanitizePoolNameComponent sanitizes a string for use in pool names.
// Replaces invalid characters with hyphens and ensures lowercase.
func sanitizePoolNameComponent(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, "_", "-")

	// Remove any characters that aren't alphanumeric or hyphens
	var result strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			result.WriteRune(c)
		}
	}

	// Collapse multiple consecutive hyphens
	sanitized := result.String()
	for strings.Contains(sanitized, "--") {
		sanitized = strings.ReplaceAll(sanitized, "--", "-")
	}

	return strings.Trim(sanitized, "-")
}

// truncatePoolName ensures the name doesn't exceed max length and ends properly.
func truncatePoolName(name string) string {
	if len(name) <= maxPoolNameLength {
		return strings.TrimRight(name, "-")
	}

	// Truncate and ensure it doesn't end with a hyphen
	truncated := name[:maxPoolNameLength]
	return strings.TrimRight(truncated, "-")
}

// validatePoolName checks if a pool name conforms to IKS naming constraints.
func validatePoolName(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("pool name cannot be empty")
	}
	if len(name) > maxPoolNameLength {
		return fmt.Errorf("pool name exceeds maximum length of %d characters", maxPoolNameLength)
	}
	if !poolNamePattern.MatchString(name) {
		return fmt.Errorf("pool name '%s' does not match IKS naming pattern (lowercase alphanumeric and hyphens, must start with letter)", name)
	}
	return nil
}

// isInstanceTypeAllowed checks if the instance type is in the allowed list
func isInstanceTypeAllowed(instanceType string, allowedTypes []string) bool {
	if len(allowedTypes) == 0 {
		return true
	}
	for _, allowed := range allowedTypes {
		if allowed == instanceType {
			return true
		}
	}
	return false
}

// findOrSelectWorkerPool finds an appropriate worker pool for the given instance type.
// If dynamic pool creation is enabled and no suitable pool exists, a new pool is created.
func (p *IKSWorkerPoolProvider) findOrSelectWorkerPool(ctx context.Context, iksClient ibm.IKSClientInterface, clusterID string, nodeClass *v1alpha1.IBMNodeClass, requestedInstanceType string) (string, string, error) {
	logger := log.FromContext(ctx)

	// If a specific worker pool is configured, use it and return its instance type
	if nodeClass.Spec.IKSWorkerPoolID != "" {
		logger.Info("Used configured worker pool", "pool_id", nodeClass.Spec.IKSWorkerPoolID)
		// Get the pool details to determine its instance type
		pool, err := iksClient.GetWorkerPool(ctx, clusterID, nodeClass.Spec.IKSWorkerPoolID)
		if err != nil {
			return "", "", fmt.Errorf("getting configured worker pool %s: %w", nodeClass.Spec.IKSWorkerPoolID, err)
		}
		// Return pool.Name for v1 API compatibility (resize uses pool name, not ID)
		return pool.Name, pool.Flavor, nil
	}

	// List all worker pools for the cluster
	workerPools, err := iksClient.ListWorkerPools(ctx, clusterID)
	if err != nil {
		return "", "", fmt.Errorf("listing worker pools: %w", err)
	}

	// Strategy 1: Find exact match (same instance type and zone)
	if requestedInstanceType != "" {
		for _, pool := range workerPools {
			if pool.Flavor == requestedInstanceType && pool.Zone == nodeClass.Spec.Zone {
				logger.Info("Found exact matching worker pool", "pool_name", pool.Name, "pool_id", pool.ID, "flavor", pool.Flavor, "zone", pool.Zone)
				return pool.Name, pool.Flavor, nil
			}
		}
	}

	// Strategy 2: If dynamic pools enabled and we have a requested instance type,
	// create a new pool with the exact instance type
	if requestedInstanceType != "" && p.isDynamicPoolsEnabled(nodeClass) {
		pool, createErr := p.createDynamicPool(ctx, iksClient, clusterID, nodeClass, requestedInstanceType)
		if createErr != nil {
			logger.Error(createErr, "Failed to create dynamic pool, falling back to existing pools")
		} else {
			return pool.Name, pool.Flavor, nil
		}
	}

	// Strategy 3: Find pool in same zone (any instance type)
	for _, pool := range workerPools {
		if pool.Zone == nodeClass.Spec.Zone {
			if requestedInstanceType != "" && pool.Flavor != requestedInstanceType {
				logger.Info("Used worker pool in same zone with different instance type",
					"pool_name", pool.Name, "pool_id", pool.ID, "pool_flavor", pool.Flavor, "zone", pool.Zone, "requested_flavor", requestedInstanceType)
			} else {
				logger.Info("Used worker pool in same zone", "pool_name", pool.Name, "pool_id", pool.ID, "flavor", pool.Flavor, "zone", pool.Zone)
			}
			return pool.Name, pool.Flavor, nil
		}
	}

	// Strategy 4: Find pool with matching instance type (any zone)
	if requestedInstanceType != "" {
		for _, pool := range workerPools {
			if pool.Flavor == requestedInstanceType {
				logger.Info("Used worker pool with matching instance type in different zone",
					"pool_name", pool.Name, "pool_id", pool.ID, "flavor", pool.Flavor, "pool_zone", pool.Zone, "requested_zone", nodeClass.Spec.Zone)
				return pool.Name, pool.Flavor, nil
			}
		}
	}

	// Strategy 5: Use first available pool as last resort (if any exist)
	if len(workerPools) > 0 {
		selectedPool := workerPools[0]
		logger.Info("Used first available worker pool as fallback",
			"pool_name", selectedPool.Name, "pool_id", selectedPool.ID, "flavor", selectedPool.Flavor, "zone", selectedPool.Zone,
			"requested_flavor", requestedInstanceType, "requested_zone", nodeClass.Spec.Zone)
		return selectedPool.Name, selectedPool.Flavor, nil
	}

	return "", "", fmt.Errorf("no worker pools found for cluster %s and dynamic pool creation is not enabled", clusterID)
}

// isDynamicPoolsEnabled checks if dynamic pool creation is enabled in the nodeClass
func (p *IKSWorkerPoolProvider) isDynamicPoolsEnabled(nodeClass *v1alpha1.IBMNodeClass) bool {
	return nodeClass.Spec.IKSDynamicPools != nil && nodeClass.Spec.IKSDynamicPools.Enabled
}

// createDynamicPool creates a new worker pool for the requested instance type
func (p *IKSWorkerPoolProvider) createDynamicPool(ctx context.Context, iksClient ibm.IKSClientInterface, clusterID string, nodeClass *v1alpha1.IBMNodeClass, instanceType string) (*ibm.WorkerPool, error) {
	logger := log.FromContext(ctx)
	config := nodeClass.Spec.IKSDynamicPools

	// Check if instance type is allowed
	if !isInstanceTypeAllowed(instanceType, config.AllowedInstanceTypes) {
		return nil, fmt.Errorf("instance type %s is not in allowed list", instanceType)
	}

	// Generate pool name
	prefix := "karp"
	if config.NamePrefix != "" {
		prefix = sanitizePoolNameComponent(config.NamePrefix)
	}
	poolName := generatePoolName(prefix, instanceType)

	// Validate the generated pool name
	if err := validatePoolName(poolName); err != nil {
		return nil, fmt.Errorf("invalid pool name generated: %w", err)
	}

	// Build labels
	labels := map[string]string{
		KarpenterManagedLabel: "true",
	}
	for k, v := range config.Labels {
		labels[k] = v
	}

	// Determine disk encryption setting
	diskEncryption := true
	if config.DiskEncryption != nil {
		diskEncryption = *config.DiskEncryption
	}

	// Build zone configuration
	zones := []ibm.WorkerPoolZone{
		{
			ID:       nodeClass.Spec.Zone,
			SubnetID: nodeClass.Spec.Subnet,
		},
	}

	request := &ibm.WorkerPoolCreateRequest{
		Name:           poolName,
		Flavor:         instanceType,
		SizePerZone:    0, // Start with 0, will be resized after creation
		Zones:          zones,
		Labels:         labels,
		DiskEncryption: diskEncryption,
		VpcID:          nodeClass.Spec.VPC,
	}

	logger.Info("Initiated dynamic worker pool creation",
		"name", poolName,
		"flavor", instanceType,
		"zone", nodeClass.Spec.Zone,
		"labels", labels)

	pool, err := iksClient.CreateWorkerPool(ctx, clusterID, request)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic worker pool: %w", err)
	}

	logger.Info("Dynamic worker pool created successfully",
		"pool_id", pool.ID,
		"pool_name", pool.Name,
		"flavor", pool.Flavor)

	return pool, nil
}
