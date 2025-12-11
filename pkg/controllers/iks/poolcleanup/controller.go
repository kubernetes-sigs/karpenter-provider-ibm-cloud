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

package poolcleanup

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	"github.com/samber/lo"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

const (
	// KarpenterManagedLabel is the label that identifies Karpenter-managed pools
	KarpenterManagedLabel = "karpenter.sh/managed"

	// DefaultEmptyPoolTTL is the default time to wait before deleting empty pools.
	// Set to 15 minutes to account for IKS pool operation latency and avoid thrashing.
	DefaultEmptyPoolTTL = 15 * time.Minute

	// maxDeletionRetries is the maximum number of times to retry deleting a pool before giving up
	maxDeletionRetries = 5
)

// poolTrackingInfo tracks state for a pool being considered for cleanup
type poolTrackingInfo struct {
	emptyTime      time.Time
	deletionErrors int
}

// Controller manages the lifecycle of dynamically created worker pools
type Controller struct {
	kubeClient client.Client
	ibmClient  *ibm.Client

	// poolTracking tracks when pools became empty and deletion failures
	poolTracking map[string]*poolTrackingInfo
	mu           sync.Mutex
}

// NewController creates a new pool cleanup controller
func NewController(kubeClient client.Client, ibmClient *ibm.Client) *Controller {
	return &Controller{
		kubeClient:   kubeClient,
		ibmClient:    ibmClient,
		poolTracking: make(map[string]*poolTrackingInfo),
	}
}

// Reconcile checks for empty Karpenter-managed pools and deletes them
func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	logger := log.FromContext(ctx).WithName("iks.poolcleanup")

	if c.ibmClient == nil {
		return reconciler.Result{RequeueAfter: time.Minute}, nil
	}

	iksClient, err := c.ibmClient.GetIKSClient()
	if err != nil {
		logger.Error(err, "Failed to get IKS client")
		return reconciler.Result{RequeueAfter: time.Minute}, nil
	}

	// List all IBMNodeClasses to find IKS configurations
	nodeClasses := &v1alpha1.IBMNodeClassList{}
	if err := c.kubeClient.List(ctx, nodeClasses); err != nil {
		logger.Error(err, "Failed to list IBMNodeClasses")
		return reconciler.Result{RequeueAfter: time.Minute}, nil
	}

	// Process each nodeClass with IKS dynamic pools enabled
	for _, nc := range nodeClasses.Items {
		if !c.isDynamicPoolsEnabled(&nc) {
			continue
		}

		if !c.isCleanupEnabled(&nc) {
			continue
		}

		clusterID := nc.Spec.IKSClusterID
		if clusterID == "" {
			continue
		}

		if err := c.cleanupEmptyPools(ctx, iksClient, clusterID, &nc); err != nil {
			logger.Error(err, "Failed to cleanup empty pools", "cluster_id", clusterID)
		}
	}

	return reconciler.Result{RequeueAfter: time.Minute}, nil
}

// cleanupEmptyPools finds and deletes empty Karpenter-managed pools.
// Uses a single lock acquisition per pool to prevent race conditions.
func (c *Controller) cleanupEmptyPools(ctx context.Context, iksClient ibm.IKSClientInterface, clusterID string, nodeClass *v1alpha1.IBMNodeClass) error {
	logger := log.FromContext(ctx)

	pools, err := iksClient.ListWorkerPools(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("listing worker pools: %w", err)
	}

	ttl := c.getEmptyPoolTTL(nodeClass)

	for _, pool := range pools {
		// Skip pools not managed by Karpenter
		if !c.isKarpenterManaged(pool) {
			continue
		}

		poolKey := fmt.Sprintf("%s/%s", clusterID, pool.ID)

		// Process pool state change with single lock acquisition
		c.processPoolState(ctx, iksClient, clusterID, pool, poolKey, ttl, logger)
	}

	return nil
}

// processPoolState handles state transitions for a single pool while holding the lock.
// This prevents race conditions by making all decisions atomically.
func (c *Controller) processPoolState(ctx context.Context, iksClient ibm.IKSClientInterface, clusterID string, pool *ibm.WorkerPool, poolKey string, ttl time.Duration, logger interface {
	Info(msg string, keysAndValues ...interface{})
	Error(err error, msg string, keysAndValues ...interface{})
}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	tracking := c.poolTracking[poolKey]

	if pool.SizePerZone == 0 && pool.ActualSize == 0 {
		// Pool is empty
		if tracking == nil {
			// First time seeing this pool empty
			c.poolTracking[poolKey] = &poolTrackingInfo{
				emptyTime:      time.Now(),
				deletionErrors: 0,
			}
			logger.Info("Pool became empty, starting TTL countdown",
				"pool_id", pool.ID,
				"pool_name", pool.Name,
				"ttl", ttl)
			return
		}

		// Check if we've exceeded max deletion retries
		if tracking.deletionErrors >= maxDeletionRetries {
			logger.Info("Skipping pool deletion after max retries",
				"pool_id", pool.ID,
				"pool_name", pool.Name,
				"errors", tracking.deletionErrors)
			return
		}

		// Check if TTL has expired
		if time.Since(tracking.emptyTime) >= ttl {
			logger.Info("Deleting empty pool after TTL",
				"pool_id", pool.ID,
				"pool_name", pool.Name,
				"empty_duration", time.Since(tracking.emptyTime))

			// Release lock during API call to avoid blocking other operations
			c.mu.Unlock()
			err := iksClient.DeleteWorkerPool(ctx, clusterID, pool.ID)
			c.mu.Lock()

			if err != nil {
				// Increment error count
				if c.poolTracking[poolKey] != nil {
					c.poolTracking[poolKey].deletionErrors++
				}
				logger.Error(err, "Failed to delete empty pool",
					"pool_id", pool.ID,
					"retries", c.poolTracking[poolKey].deletionErrors,
					"max_retries", maxDeletionRetries)
				return
			}

			// Successfully deleted - remove from tracking
			delete(c.poolTracking, poolKey)
			logger.Info("Successfully deleted empty pool",
				"pool_id", pool.ID,
				"pool_name", pool.Name)
		}
	} else {
		// Pool is not empty - remove from tracking if present
		if tracking != nil {
			delete(c.poolTracking, poolKey)
			logger.Info("Pool is no longer empty, removed from cleanup tracking",
				"pool_id", pool.ID,
				"pool_name", pool.Name)
		}
	}
}

// isKarpenterManaged checks if a pool was created by Karpenter
func (c *Controller) isKarpenterManaged(pool *ibm.WorkerPool) bool {
	if pool.Labels == nil {
		return false
	}
	return pool.Labels[KarpenterManagedLabel] == "true"
}

// isDynamicPoolsEnabled checks if dynamic pools are enabled for the nodeClass
func (c *Controller) isDynamicPoolsEnabled(nodeClass *v1alpha1.IBMNodeClass) bool {
	return nodeClass.Spec.IKSDynamicPools != nil && nodeClass.Spec.IKSDynamicPools.Enabled
}

// isCleanupEnabled checks if cleanup is enabled for the nodeClass
func (c *Controller) isCleanupEnabled(nodeClass *v1alpha1.IBMNodeClass) bool {
	config := nodeClass.Spec.IKSDynamicPools
	if config == nil || config.CleanupPolicy == nil {
		return true // Default to enabled
	}
	return lo.FromPtr(config.CleanupPolicy.DeleteOnEmpty)
}

// getEmptyPoolTTL returns the TTL for empty pools from the nodeClass config
func (c *Controller) getEmptyPoolTTL(nodeClass *v1alpha1.IBMNodeClass) time.Duration {
	config := nodeClass.Spec.IKSDynamicPools
	if config == nil || config.CleanupPolicy == nil || config.CleanupPolicy.EmptyPoolTTL == "" {
		return DefaultEmptyPoolTTL
	}

	ttl, err := time.ParseDuration(config.CleanupPolicy.EmptyPoolTTL)
	if err != nil {
		return DefaultEmptyPoolTTL
	}
	return ttl
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("iks.poolcleanup").
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}
