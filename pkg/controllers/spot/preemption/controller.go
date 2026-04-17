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

package preemption

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const SpotPreemptionCheckInterval = 1 * time.Minute

type vpcClientProvider interface {
	GetVPCClient(ctx context.Context) (*ibm.VPCClient, error)
}

type Controller struct {
	ibmClient            vpcClientProvider
	kubeClient           client.Client
	recorder             record.EventRecorder
	unavailableOfferings *cache.UnavailableOfferings
}

func NewController(kubeClient client.Client, recorder record.EventRecorder, unavailableOfferings *cache.UnavailableOfferings, ibmClient *ibm.Client) *Controller {
	return &Controller{
		ibmClient:            ibmClient,
		kubeClient:           kubeClient,
		recorder:             recorder,
		unavailableOfferings: unavailableOfferings,
	}
}

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	vpcClient, err := c.ibmClient.GetVPCClient(ctx)
	if err != nil {
		return reconciler.Result{}, err
	}
	instances, err := vpcClient.ListSpotInstances(ctx)
	if err != nil {
		return reconciler.Result{}, err
	}
	claimsByInstanceID, err := c.buildNodeClaimMap(ctx)
	if err != nil {
		return reconciler.Result{}, err
	}

	for _, instance := range instances {
		// Idempotency guard, only act on fully stopped instances
		if instance.Status == nil || *instance.Status != "stopped" {
			continue
		}
		for _, reason := range instance.StatusReasons {
			if reason.Code != nil && *reason.Code == "stopped_by_preemption" {
				if instance.ID == nil || instance.Profile == nil || instance.Profile.Name == nil || instance.Zone == nil || instance.Zone.Name == nil {
					log.FromContext(ctx).Error(fmt.Errorf("instance missing required fields"), "skipping malformed instance from VPC API",
						"instanceID", lo.FromPtr(instance.ID),
						"status", lo.FromPtr(instance.Status),
						"name", lo.FromPtr(instance.Name),
					)
					continue
				}
				nodeClaim := claimsByInstanceID[*instance.ID]
				if nodeClaim == nil {
					continue
				}
				instanceType := *instance.Profile.Name
				zone := *instance.Zone.Name
				key := instanceType + ":" + zone + ":" + karpv1.CapacityTypeSpot
				c.unavailableOfferings.Add(key, time.Now().Add(time.Hour))
				if err := vpcClient.DeleteInstance(ctx, *instance.ID); err != nil {
					log.FromContext(ctx).Error(err, "failed deleting preempted instance", "instanceID", *instance.ID, "instanceType", instanceType, "zone", zone)
				}
				if err := c.kubeClient.Delete(ctx, nodeClaim); err != nil {
					log.FromContext(ctx).Error(err, "failed deleting nodeclaim", "nodeClaim", nodeClaim.Name, "instanceID", *instance.ID)
				}
				c.recorder.Event(nodeClaim, corev1.EventTypeWarning, "SpotPreemption", fmt.Sprintf("Spot instance %s preempted in zone %s", instanceType, zone))
			}
		}
	}

	return reconciler.Result{RequeueAfter: SpotPreemptionCheckInterval}, nil
}

func (c *Controller) buildNodeClaimMap(ctx context.Context) (map[string]*karpv1.NodeClaim, error) {
	nodeClaimList := &karpv1.NodeClaimList{}
	if err := c.kubeClient.List(ctx, nodeClaimList); err != nil {
		return nil, err
	}
	claimsByInstanceID := make(map[string]*karpv1.NodeClaim, len(nodeClaimList.Items))
	for i := range nodeClaimList.Items {
		nodeClaim := &nodeClaimList.Items[i]
		if nodeClaim.Status.ProviderID == "" {
			continue
		}
		parts := strings.Split(nodeClaim.Status.ProviderID, "/")
		if len(parts) >= 4 {
			claimsByInstanceID[parts[len(parts)-1]] = nodeClaim
		}
	}
	return claimsByInstanceID, nil
}

func (c *Controller) Name() string {
	return "spot.preemption"
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return builder.ControllerManagedBy(m).
		Named("spot.preemption").
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}
