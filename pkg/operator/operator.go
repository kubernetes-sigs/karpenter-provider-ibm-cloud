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
package operator

import (
	"context"
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/operator"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/operator/options"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers"
)

// Operator wraps the core Karpenter operator with IBM-specific providers
type Operator struct {
	*operator.Operator
	UnavailableOfferings *cache.UnavailableOfferings
	ProviderFactory      *providers.ProviderFactory
	KubernetesClient     kubernetes.Interface
}

func NewOperator(ctx context.Context, coreOperator *operator.Operator) (context.Context, *Operator) {
	// Validate IBM credentials early
	if err := validateIBMCredentials(ctx); err != nil {
		log.FromContext(ctx).Error(err, "Failed to validate IBM Cloud credentials")
		os.Exit(1)
	}

	// Create IBM client
	ibmClient, err := ibm.NewClient()
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to create IBM client")
		os.Exit(1)
	}

	// Create kubernetes client from the manager's config
	kubernetesClient, err := kubernetes.NewForConfig(coreOperator.GetConfig())
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to create kubernetes client")
		os.Exit(1)
	}

	// Create provider factory with all providers
	providerFactory := providers.NewProviderFactory(ibmClient, coreOperator.GetClient(), kubernetesClient)
	unavailableOfferings := cache.NewUnavailableOfferings()

	// Create options with environment variables (including circuit breaker config)
	opts := options.NewOptions()
	ctx = opts.ToContext(ctx)

	return ctx, &Operator{
		Operator:             coreOperator,
		UnavailableOfferings: unavailableOfferings,
		ProviderFactory:      providerFactory,
		KubernetesClient:     kubernetesClient,
	}
}

func validateIBMCredentials(ctx context.Context) error {
	requiredEnvVars := []string{"IBMCLOUD_REGION", "IBMCLOUD_API_KEY", "VPC_API_KEY"}
	for _, envVar := range requiredEnvVars {
		if os.Getenv(envVar) == "" {
			return fmt.Errorf("%s environment variable is not set", envVar)
		}
		log.FromContext(ctx).Info("Found required environment variable", "name", envVar)
	}

	// Validate by creating client
	_, err := ibm.NewClient()
	if err != nil {
		return fmt.Errorf("failed to create IBM client: %w", err)
	}

	log.FromContext(ctx).Info("Successfully authenticated with IBM Cloud")
	return nil
}
