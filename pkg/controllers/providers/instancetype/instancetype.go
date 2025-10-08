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
package instancetype

import (
	"context"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/metrics"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/operator/injection"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
)

type Controller struct {
	instanceTypeProvider instancetype.Provider
}

func NewController() (*Controller, error) {
	// Create IBM client
	client, err := ibm.NewClient()
	if err != nil {
		return nil, fmt.Errorf("creating IBM client: %w", err)
	}

	// Create pricing provider
	pricingProvider := pricing.NewIBMPricingProvider(client)

	// Create instance type provider
	provider := instancetype.NewProvider(client, pricingProvider)

	return &Controller{
		instanceTypeProvider: provider,
	}, nil
}

func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	ctx = injection.WithControllerName(ctx, "providers.instancetype")

	// Get region from the provider - we need to add a method to expose this
	region := c.getRegion()

	// Refresh instance types by listing them
	if _, err := c.instanceTypeProvider.List(ctx); err != nil {
		metrics.ApiRequests.WithLabelValues("ListInstanceTypes", "500", region).Inc()
		return reconciler.Result{}, fmt.Errorf("refreshing instance types: %w", err)
	}

	metrics.ApiRequests.WithLabelValues("ListInstanceTypes", "200", region).Inc()

	// Reconcile every hour to refresh instance type information
	return reconciler.Result{RequeueAfter: time.Hour}, nil
}

// Add this method to the controller
func (c *Controller) getRegion() string {
	if regionProvider, ok := c.instanceTypeProvider.(interface{ GetRegion() string }); ok {
		return regionProvider.GetRegion()
	}
	return "unknown"
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("providers.instancetype").
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}
