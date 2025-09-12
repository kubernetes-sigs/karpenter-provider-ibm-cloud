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
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	commonTypes "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

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
	iksClient := p.client.GetIKSClient()
	if iksClient == nil {
		return nil, fmt.Errorf("IKS client not available")
	}

	// Extract requested instance type using same logic as VPC mode
	requestedInstanceType := nodeClass.Spec.InstanceProfile
	if requestedInstanceType == "" {
		requestedInstanceType = nodeClaim.Labels["node.kubernetes.io/instance-type"]
	}

	logger.Info("Creating IKS worker", "cluster_id", clusterID, "requested_instance_type", requestedInstanceType)

	// Find or select appropriate worker pool
	poolID, selectedInstanceType, err := p.findOrSelectWorkerPool(ctx, iksClient, clusterID, nodeClass, requestedInstanceType)
	if err != nil {
		return nil, fmt.Errorf("finding worker pool: %w", err)
	}

	// Get current worker pool state
	workerPool, err := iksClient.GetWorkerPool(ctx, clusterID, poolID)
	if err != nil {
		return nil, fmt.Errorf("getting worker pool %s: %w", poolID, err)
	}

	// Calculate new size (add one node)
	newSize := workerPool.SizePerZone + 1

	logger.Info("Resizing worker pool", "pool_id", poolID, "instance_type", selectedInstanceType, "current_size", workerPool.SizePerZone, "new_size", newSize)

	// Resize the worker pool
	if err := iksClient.ResizeWorkerPool(ctx, clusterID, poolID, newSize); err != nil {
		return nil, fmt.Errorf("resizing worker pool %s: %w", poolID, err)
	}

	// Create a placeholder node representation
	// The actual node will be created by IKS and joined to the cluster
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
			Labels: map[string]string{
				"karpenter.sh/managed":             "true",
				"karpenter.ibm.sh/cluster-id":      clusterID,
				"karpenter.ibm.sh/worker-pool-id":  poolID,
				"karpenter.ibm.sh/zone":            nodeClass.Spec.Zone,
				"karpenter.ibm.sh/region":          nodeClass.Spec.Region,
				"karpenter.ibm.sh/instance-type":   selectedInstanceType,
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
	clusterID := node.Labels["karpenter.ibm.sh/cluster-id"]
	poolID := node.Labels["karpenter.ibm.sh/worker-pool-id"]

	if clusterID == "" || poolID == "" {
		return fmt.Errorf("cluster ID or pool ID not found in node labels")
	}

	// Check if client is initialized
	if p.client == nil {
		return fmt.Errorf("IBM client is not initialized")
	}

	// Get IKS client
	iksClient := p.client.GetIKSClient()
	if iksClient == nil {
		return fmt.Errorf("IKS client not available")
	}

	// Get current worker pool state
	workerPool, err := iksClient.GetWorkerPool(ctx, clusterID, poolID)
	if err != nil {
		return fmt.Errorf("getting worker pool %s: %w", poolID, err)
	}

	// Calculate new size (remove one node)
	newSize := workerPool.SizePerZone - 1
	if newSize < 0 {
		newSize = 0
	}

	logger.Info("Resizing worker pool down", "pool_id", poolID, "current_size", workerPool.SizePerZone, "new_size", newSize)

	// Resize the worker pool
	if err := iksClient.ResizeWorkerPool(ctx, clusterID, poolID, newSize); err != nil {
		return fmt.Errorf("resizing worker pool %s: %w", poolID, err)
	}

	logger.Info("IKS worker pool resized down successfully", "pool_id", poolID, "new_size", newSize)
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

	iksClient := p.client.GetIKSClient()
	if iksClient == nil {
		return fmt.Errorf("IKS client not available")
	}

	return iksClient.ResizeWorkerPool(ctx, clusterID, poolID, newSize)
}

// GetPool retrieves information about a worker pool
func (p *IKSWorkerPoolProvider) GetPool(ctx context.Context, clusterID, poolID string) (*commonTypes.WorkerPool, error) {
	if p.client == nil {
		return nil, fmt.Errorf("IBM client is not initialized")
	}

	iksClient := p.client.GetIKSClient()
	if iksClient == nil {
		return nil, fmt.Errorf("IKS client not available")
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

	iksClient := p.client.GetIKSClient()
	if iksClient == nil {
		return nil, fmt.Errorf("IKS client not available")
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

// findOrSelectWorkerPool finds an appropriate worker pool for the given instance type
func (p *IKSWorkerPoolProvider) findOrSelectWorkerPool(ctx context.Context, iksClient ibm.IKSClientInterface, clusterID string, nodeClass *v1alpha1.IBMNodeClass, requestedInstanceType string) (string, string, error) {
	logger := log.FromContext(ctx)

	// If a specific worker pool is configured, use it and return its instance type
	if nodeClass.Spec.IKSWorkerPoolID != "" {
		logger.Info("Using configured worker pool", "pool_id", nodeClass.Spec.IKSWorkerPoolID)
		// Get the pool details to determine its instance type
		pool, err := iksClient.GetWorkerPool(ctx, clusterID, nodeClass.Spec.IKSWorkerPoolID)
		if err != nil {
			return "", "", fmt.Errorf("getting configured worker pool %s: %w", nodeClass.Spec.IKSWorkerPoolID, err)
		}
		return pool.ID, pool.Flavor, nil
	}

	// List all worker pools for the cluster
	workerPools, err := iksClient.ListWorkerPools(ctx, clusterID)
	if err != nil {
		return "", "", fmt.Errorf("listing worker pools: %w", err)
	}

	if len(workerPools) == 0 {
		return "", "", fmt.Errorf("no worker pools found for cluster %s", clusterID)
	}

	// Strategy 1: Find exact match (same instance type and zone)
	if requestedInstanceType != "" {
		for _, pool := range workerPools {
			if pool.Flavor == requestedInstanceType && pool.Zone == nodeClass.Spec.Zone {
				logger.Info("Found exact matching worker pool", "pool_id", pool.ID, "flavor", pool.Flavor, "zone", pool.Zone)
				return pool.ID, pool.Flavor, nil
			}
		}
	}

	// Strategy 2: Find pool in same zone (any instance type)
	for _, pool := range workerPools {
		if pool.Zone == nodeClass.Spec.Zone {
			if requestedInstanceType != "" && pool.Flavor != requestedInstanceType {
				logger.Info("Using worker pool in same zone with different instance type",
					"pool_id", pool.ID, "pool_flavor", pool.Flavor, "zone", pool.Zone, "requested_flavor", requestedInstanceType)
			} else {
				logger.Info("Using worker pool in same zone", "pool_id", pool.ID, "flavor", pool.Flavor, "zone", pool.Zone)
			}
			return pool.ID, pool.Flavor, nil
		}
	}

	// Strategy 3: Find pool with matching instance type (any zone)
	if requestedInstanceType != "" {
		for _, pool := range workerPools {
			if pool.Flavor == requestedInstanceType {
				logger.Info("Using worker pool with matching instance type in different zone",
					"pool_id", pool.ID, "flavor", pool.Flavor, "pool_zone", pool.Zone, "requested_zone", nodeClass.Spec.Zone)
				return pool.ID, pool.Flavor, nil
			}
		}
	}

	// Strategy 4: Use first available pool as last resort
	selectedPool := workerPools[0]
	logger.Info("Using first available worker pool as fallback",
		"pool_id", selectedPool.ID, "flavor", selectedPool.Flavor, "zone", selectedPool.Zone,
		"requested_flavor", requestedInstanceType, "requested_zone", nodeClass.Spec.Zone)
	return selectedPool.ID, selectedPool.Flavor, nil
}
