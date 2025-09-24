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
package pricing

import (
	"context"
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/singleton"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/metrics"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/karpenter/pkg/operator/injection"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
)

// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;update;patch;create
type Controller struct {
	pricingProvider pricing.Provider
}

// NoOpPricingProvider is a no-op implementation of the pricing provider
type NoOpPricingProvider struct{}

func (n *NoOpPricingProvider) GetPrice(ctx context.Context, instanceType string, zone string) (float64, error) {
	return 0.10, nil // Default price
}

func (n *NoOpPricingProvider) GetPrices(ctx context.Context, zone string) (map[string]float64, error) {
	return map[string]float64{}, nil
}

func (n *NoOpPricingProvider) Refresh(ctx context.Context) error {
	return nil // No-op
}

func NewController(pricingProvider pricing.Provider) (*Controller, error) {
	// Create a no-op provider if none is provided
	if pricingProvider == nil {
		pricingProvider = &NoOpPricingProvider{}
	}
	return &Controller{
		pricingProvider: pricingProvider,
	}, nil
}

func (c *Controller) Reconcile(ctx context.Context) (reconcile.Result, error) {
	ctx = injection.WithControllerName(ctx, "providers.pricing")

	// Refresh pricing information
	if err := c.pricingProvider.Refresh(ctx); err != nil {
		metrics.ApiRequests.WithLabelValues("RefreshPricing", "500", "global").Inc()
		return reconcile.Result{}, fmt.Errorf("refreshing pricing information: %w", err)
	}
	metrics.ApiRequests.WithLabelValues("RefreshPricing", "200", "global").Inc()

	// Requeue after 12 hours to refresh prices
	return reconcile.Result{RequeueAfter: 12 * time.Hour}, nil
}

func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("providers.pricing").
		WatchesRawSource(singleton.Source()).
		Complete(singleton.AsReconciler(c))
}
