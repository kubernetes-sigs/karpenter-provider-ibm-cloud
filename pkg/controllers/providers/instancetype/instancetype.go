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

	"github.com/awslabs/operatorpkg/singleton"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/operator/injection"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
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

func (c *Controller) Reconcile(ctx context.Context) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, "providers.instancetype")

	// Refresh instance types by listing them
	if _, err := c.instanceTypeProvider.List(ctx); err != nil {
		return reconcile.Result{}, fmt.Errorf("refreshing instance types: %w", err)
	}

	// Reconcile every hour to refresh instance type information
	return reconcile.Result{RequeueAfter: time.Hour}, nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("providers.instancetype").
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}
