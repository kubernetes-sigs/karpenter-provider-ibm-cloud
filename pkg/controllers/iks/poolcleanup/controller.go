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
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	corev1 "k8s.io/api/core/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/iks/workerpool"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

const (
	KarpenterManagedLabel = ownership.ManagedLabel
	DefaultEmptyPoolTTL   = 15 * time.Minute
)

type Controller struct {
	kubeClient   client.Client
	apiReader    client.Reader
	ibmClient    *ibm.Client
	iksClient    ibm.IKSClientInterface
	poolTracking map[string]time.Time
	mu           sync.Mutex
}

func NewController(kubeClient client.Client, ibmClient *ibm.Client, readers ...client.Reader) *Controller {
	reader := client.Reader(kubeClient)
	if len(readers) > 0 && readers[0] != nil {
		reader = readers[0]
	}
	return &Controller{kubeClient: kubeClient, apiReader: reader, ibmClient: ibmClient, poolTracking: map[string]time.Time{}}
}

func (c *Controller) SetIKSClient(iksClient ibm.IKSClientInterface) { c.iksClient = iksClient }

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	if c.ibmClient == nil && c.iksClient == nil {
		return reconciler.Result{RequeueAfter: time.Minute}, nil
	}
	iksClient := c.iksClient
	if iksClient == nil {
		var err error
		iksClient, err = c.ibmClient.GetIKSClient()
		if err != nil {
			return reconciler.Result{}, err
		}
	}
	classes := &v1alpha1.IBMNodeClassList{}
	if operationErr := c.apiReader.List(ctx, classes); operationErr != nil {
		return reconciler.Result{}, operationErr
	}
	var failures []error
	for _, nodeClass := range classes.Items {
		if !c.isDynamicPoolsEnabled(&nodeClass) || !c.isCleanupEnabled(&nodeClass) || nodeClass.Spec.IKSClusterID == "" {
			continue
		}
		if operationErr := c.cleanupEmptyPools(ctx, iksClient, nodeClass.Spec.IKSClusterID, &nodeClass); operationErr != nil {
			failures = append(failures, operationErr)
		}
	}
	if operationErr := c.releaseDeletedReservations(ctx, iksClient); operationErr != nil {
		failures = append(failures, operationErr)
	}
	return reconciler.Result{RequeueAfter: time.Minute}, errors.Join(failures...)
}

func (c *Controller) namespace() string {
	if namespace := os.Getenv("POD_NAMESPACE"); namespace != "" {
		return namespace
	}
	return "karpenter"
}

func (c *Controller) cleanupEmptyPools(ctx context.Context, iksClient ibm.IKSClientInterface, clusterID string, nodeClass *v1alpha1.IBMNodeClass) error {
	accountID, region, targetErr := c.target(iksClient)
	if targetErr != nil {
		return targetErr
	}
	if region != nodeClass.Spec.Region {
		return fmt.Errorf("IKS pool cleanup client region differs from NodeClass region")
	}
	clusterUID, err := ownership.ClusterUID(ctx, c.apiReader)
	if err != nil {
		return err
	}
	pools, err := iksClient.ListWorkerPools(ctx, clusterID)
	if err != nil {
		return fmt.Errorf("listing worker pools: %w", err)
	}
	var failures []error
	for _, pool := range pools {
		if pool == nil || !c.isKarpenterManaged(pool) || pool.Labels[ownership.ProviderLabel] != "iks" || pool.Labels[ownership.ClusterUIDLabel] != clusterUID ||
			nodeClass.UID == "" || pool.Labels[ownership.NodeClassUIDLabel] != string(nodeClass.UID) || pool.Labels[ownership.ClaimUIDLabel] != "" {
			continue
		}
		key := clusterID + "/" + pool.ID
		c.mu.Lock()
		since, tracked := c.poolTracking[key]
		empty := pool.SizePerZone == 0 && pool.ActualSize == 0
		if !empty {
			delete(c.poolTracking, key)
		} else if !tracked {
			c.poolTracking[key] = time.Now()
		}
		c.mu.Unlock()
		if !empty || !tracked || time.Since(since) < c.getEmptyPoolTTL(nodeClass) {
			continue
		}
		freshClass := &v1alpha1.IBMNodeClass{}
		if operationErr := c.apiReader.Get(ctx, client.ObjectKeyFromObject(nodeClass), freshClass); operationErr != nil {
			failures = append(failures, operationErr)
			continue
		}
		if freshClass.UID != nodeClass.UID || freshClass.Spec.IKSClusterID != clusterID || !c.isCleanupEnabled(freshClass) || !c.isDynamicPoolsEnabled(freshClass) {
			continue
		}
		deleted, err := workerpool.TryDeleteEmptyPool(ctx, c.kubeClient, c.apiReader, iksClient, clusterID, clusterUID, string(nodeClass.UID), c.namespace(), pool, accountID, region)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if deleted {
			c.mu.Lock()
			delete(c.poolTracking, key)
			c.mu.Unlock()
		}
	}
	return errors.Join(failures...)
}

func (c *Controller) releaseDeletedReservations(ctx context.Context, iksClient ibm.IKSClientInterface) error {
	accountID, region, targetErr := c.target(iksClient)
	if targetErr != nil {
		return targetErr
	}
	clusterUID, err := ownership.ClusterUID(ctx, c.apiReader)
	if err != nil {
		return err
	}
	reservations := &corev1.ConfigMapList{}
	if operationErr := c.apiReader.List(ctx, reservations, client.InNamespace(c.namespace()), client.MatchingLabels{ownership.ClusterUIDLabel: clusterUID, ownership.ProviderLabel: "iks"}); operationErr != nil {
		return operationErr
	}
	for i := range reservations.Items {
		reservation := &reservations.Items[i]
		checkpoint, decodeErr := workerpool.DecodePoolCleanupCheckpoint(reservation)
		if decodeErr != nil {
			return decodeErr
		}
		if checkpoint == nil {
			continue
		}
		if checkpoint.ClusterUID != clusterUID || checkpoint.AccountID != accountID || checkpoint.Region != region {
			return fmt.Errorf("IKS pool cleanup target changed; retaining reservation")
		}
		_, err := iksClient.GetWorkerPool(ctx, checkpoint.ClusterID, checkpoint.PoolID)
		if workerpool.IsNotFound(err) {
			if operationErr := c.kubeClient.Delete(ctx, reservation, client.Preconditions{UID: &reservation.UID, ResourceVersion: &reservation.ResourceVersion}); client.IgnoreNotFound(operationErr) != nil {
				return operationErr
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) target(iksClient ibm.IKSClientInterface) (string, string, error) {
	if iksClient == nil || iksClient.GetAccountID() == "" || iksClient.GetRegion() == "" {
		return "", "", fmt.Errorf("IKS pool cleanup client has no account and region identity")
	}
	return iksClient.GetAccountID(), iksClient.GetRegion(), nil
}

func (c *Controller) isKarpenterManaged(pool *ibm.WorkerPool) bool {
	return pool != nil && pool.Labels[KarpenterManagedLabel] == "true"
}
func (c *Controller) isDynamicPoolsEnabled(nodeClass *v1alpha1.IBMNodeClass) bool {
	return nodeClass.Spec.IKSDynamicPools != nil && nodeClass.Spec.IKSDynamicPools.Enabled
}
func (c *Controller) isCleanupEnabled(nodeClass *v1alpha1.IBMNodeClass) bool {
	if nodeClass.Spec.IKSDynamicPools == nil || nodeClass.Spec.IKSDynamicPools.CleanupPolicy == nil {
		return true
	}
	policy := nodeClass.Spec.IKSDynamicPools.CleanupPolicy
	return policy.DeleteOnEmpty == nil || *policy.DeleteOnEmpty
}
func (c *Controller) getEmptyPoolTTL(nodeClass *v1alpha1.IBMNodeClass) time.Duration {
	if nodeClass.Spec.IKSDynamicPools != nil && nodeClass.Spec.IKSDynamicPools.CleanupPolicy != nil {
		if ttl, err := time.ParseDuration(nodeClass.Spec.IKSDynamicPools.CleanupPolicy.EmptyPoolTTL); err == nil && ttl >= 0 {
			return ttl
		}
	}
	return DefaultEmptyPoolTTL
}
func (c *Controller) Register(_ context.Context, mgr manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(mgr).Named("iks.poolcleanup").WatchesRawSource(singleton.Source()).Complete(singleton.AsReconciler(c))
}
