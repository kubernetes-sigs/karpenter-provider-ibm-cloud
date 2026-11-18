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

package allocation

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/instance"
)

type pendingProvider interface {
	CleanupPending(context.Context, *karpv1.NodeClaim) (bool, error)
	GetFresh(context.Context, string) (*corev1.Node, error)
	ValidateLaunchTarget(context.Context, *karpv1.NodeClaim) error
}

type Controller struct {
	kubeClient client.Client
	apiReader  client.Reader
	factory    *providers.ProviderFactory
	provider   pendingProvider
}

func NewController(kubeClient client.Client, reader client.Reader, factory *providers.ProviderFactory) *Controller {
	return &Controller{kubeClient: kubeClient, apiReader: reader, factory: factory}
}

func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	claim := &karpv1.NodeClaim{}
	if err := c.apiReader.Get(ctx, req.NamespacedName, claim); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if claim.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(claim, instance.LaunchFinalizer) {
		return reconcile.Result{}, nil
	}
	pending := c.provider
	if pending == nil {
		provider, err := c.factory.GetInstanceProviderForMode(commonTypes.VPCMode)
		if err != nil {
			return reconcile.Result{}, err
		}
		var ok bool
		pending, ok = provider.(pendingProvider)
		if !ok {
			return reconcile.Result{}, fmt.Errorf("VPC provider cannot recover pending allocations")
		}
	}
	if err := pending.ValidateLaunchTarget(ctx, claim); err != nil {
		return reconcile.Result{}, err
	}
	var err error

	complete := false
	if claim.Status.ProviderID != "" {
		_, err = pending.GetFresh(ctx, claim.Status.ProviderID)
		complete = cloudprovider.IsNodeClaimNotFoundError(err)
		if err != nil && !complete {
			return reconcile.Result{}, err
		}
	} else {
		complete, err = pending.CleanupPending(ctx, claim)
		if err != nil {
			return reconcile.Result{}, err
		}
	}
	if !complete {
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}
	stored := claim.DeepCopy()
	controllerutil.RemoveFinalizer(claim, instance.LaunchFinalizer)
	if err := c.kubeClient.Patch(ctx, claim, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (c *Controller) Register(_ context.Context, mgr manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(mgr).Named("nodeclaim.vpcallocation").For(&karpv1.NodeClaim{}).Complete(c)
}
