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

package providers

import (
	"context"
	"fmt"
	"os"
	"sync"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	ibmcache "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	iksProvider "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/iks/workerpool"
	vpcProvider "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/instance"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
)

// ProviderFactory creates the appropriate instance provider based on the NodeClass configuration
type ProviderFactory struct {
	client               *ibm.Client
	kubeClient           client.Client
	kubernetesClient     kubernetes.Interface
	pricingProvider      pricing.Provider
	subnetProvider       subnet.Provider
	instanceTypeProvider instancetype.Provider
	apiReader            client.Reader
	ctx                  context.Context
	mu                   sync.Mutex
	vpc                  commonTypes.VPCInstanceProvider
	iks                  commonTypes.IKSWorkerPoolProvider
}

// NewProviderFactory creates a new provider factory
func NewProviderFactory(ctx context.Context, client *ibm.Client, kubeClient client.Client, kubernetesClient kubernetes.Interface, unavailableOfferings *ibmcache.UnavailableOfferings, options ...FactoryOption) *ProviderFactory {
	// Create shared providers
	var region string
	if client != nil {
		region = client.GetRegion()
	}
	pricingProvider := pricing.NewIBMPricingProvider(ctx, client, region)
	subnetProvider := subnet.NewProvider(client)
	instanceTypeProvider := instancetype.NewProvider(client, pricingProvider, unavailableOfferings, ctx)

	factory := &ProviderFactory{
		ctx:                  ctx,
		client:               client,
		kubeClient:           kubeClient,
		kubernetesClient:     kubernetesClient,
		pricingProvider:      pricingProvider,
		subnetProvider:       subnetProvider,
		instanceTypeProvider: instanceTypeProvider,
		apiReader:            kubeClient,
	}
	for _, option := range options {
		option(factory)
	}
	return factory
}

type FactoryOption func(*ProviderFactory)

func WithAPIReader(reader client.Reader) FactoryOption {
	return func(f *ProviderFactory) { f.apiReader = reader }
}

// WithVPCInstanceProvider supplies the VPC instance provider instead of building one lazily.
func WithVPCInstanceProvider(provider commonTypes.VPCInstanceProvider) FactoryOption {
	return func(f *ProviderFactory) { f.vpc = provider }
}

func (f *ProviderFactory) GetInstanceProvider(nodeClass *v1alpha1.IBMNodeClass) (commonTypes.InstanceProvider, error) {
	if nodeClass == nil {
		return nil, fmt.Errorf("nodeClass cannot be nil")
	}
	mode, err := ResolveProviderMode(nodeClass)
	if err != nil {
		return nil, err
	}
	return f.GetInstanceProviderForMode(mode)
}

func (f *ProviderFactory) GetInstanceProviderForMode(mode commonTypes.ProviderMode) (commonTypes.InstanceProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch mode {
	case commonTypes.IKSMode:
		if f.iks == nil {
			provider, err := iksProvider.NewIKSWorkerPoolProvider(f.client, f.kubeClient,
				iksProvider.WithAPIReader(f.apiReader),
				iksProvider.WithInstanceTypeProvider(f.instanceTypeProvider),
			)
			if err != nil {
				return nil, err
			}
			f.iks = provider
		}
		return f.iks, nil
	case commonTypes.VPCMode:
		if f.vpc == nil {
			options := []vpcProvider.Option{vpcProvider.WithAPIReader(f.apiReader), vpcProvider.WithMetricsContext(f.ctx)}
			if f.kubernetesClient != nil {
				options = append(options, vpcProvider.WithKubernetesClient(f.kubernetesClient))
			}
			provider, err := vpcProvider.NewVPCInstanceProvider(f.client, f.kubeClient, options...)
			if err != nil {
				return nil, err
			}
			f.vpc = provider
		}
		return f.vpc, nil
	default:
		return nil, fmt.Errorf("unknown provider mode: %s", mode)
	}
}

func (f *ProviderFactory) GetVPCProvider(nodeClass *v1alpha1.IBMNodeClass) (commonTypes.VPCInstanceProvider, error) {
	if nodeClass == nil {
		return nil, fmt.Errorf("nodeClass cannot be nil")
	}
	mode, err := ResolveProviderMode(nodeClass)
	if err != nil {
		return nil, err
	}
	if mode != commonTypes.VPCMode {
		return nil, fmt.Errorf("VPC provider requested but NodeClass is configured for %s mode", mode)
	}
	provider, err := f.GetInstanceProviderForMode(mode)
	if err != nil {
		return nil, err
	}
	return provider.(commonTypes.VPCInstanceProvider), nil
}

func (f *ProviderFactory) GetIKSProvider(nodeClass *v1alpha1.IBMNodeClass) (commonTypes.IKSWorkerPoolProvider, error) {
	if nodeClass == nil {
		return nil, fmt.Errorf("nodeClass cannot be nil")
	}
	mode, err := ResolveProviderMode(nodeClass)
	if err != nil {
		return nil, err
	}
	if mode != commonTypes.IKSMode {
		return nil, fmt.Errorf("IKS provider requested but NodeClass is configured for %s mode", mode)
	}
	provider, err := f.GetInstanceProviderForMode(mode)
	if err != nil {
		return nil, err
	}
	return provider.(commonTypes.IKSWorkerPoolProvider), nil
}

// GetProviderMode returns the provider mode for a given NodeClass
func (f *ProviderFactory) GetProviderMode(nodeClass *v1alpha1.IBMNodeClass) (commonTypes.ProviderMode, error) {
	return ResolveProviderMode(nodeClass)
}

// ResolveProviderMode applies explicit class settings before the controller default.
func ResolveProviderMode(nodeClass *v1alpha1.IBMNodeClass) (commonTypes.ProviderMode, error) {
	global := os.Getenv("BOOTSTRAP_MODE")
	if global != "" && global != "auto" && global != "cloud-init" && global != "iks-api" {
		return "", fmt.Errorf("invalid BOOTSTRAP_MODE %q: expected auto, cloud-init or iks-api", global)
	}
	if nodeClass != nil {
		if nodeClass.Spec.BootstrapMode != nil {
			switch mode := *nodeClass.Spec.BootstrapMode; mode {
			case "cloud-init":
				return commonTypes.VPCMode, nil
			case "iks-api":
				return commonTypes.IKSMode, nil
			case "auto":
			default:
				return "", fmt.Errorf("invalid NodeClass bootstrapMode %q", mode)
			}
		}
		if nodeClass.Spec.IKSClusterID != "" {
			return commonTypes.IKSMode, nil
		}
	}
	switch global {
	case "cloud-init":
		return commonTypes.VPCMode, nil
	case "iks-api":
		return commonTypes.IKSMode, nil
	}
	if os.Getenv("IKS_CLUSTER_ID") != "" {
		return commonTypes.IKSMode, nil
	}
	return commonTypes.VPCMode, nil
}

// GetPricingProvider returns the shared pricing provider
func (f *ProviderFactory) GetPricingProvider() pricing.Provider {
	return f.pricingProvider
}

// GetSubnetProvider returns the shared subnet provider
func (f *ProviderFactory) GetSubnetProvider() subnet.Provider {
	return f.subnetProvider
}

// GetInstanceTypeProvider returns the shared instance type provider
func (f *ProviderFactory) GetInstanceTypeProvider() instancetype.Provider {
	return f.instanceTypeProvider
}

// GetClient returns the IBM Cloud client
func (f *ProviderFactory) GetClient() *ibm.Client {
	return f.client
}
