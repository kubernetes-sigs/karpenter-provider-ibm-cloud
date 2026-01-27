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

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
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
}

// NewProviderFactory creates a new provider factory
func NewProviderFactory(ctx context.Context, client *ibm.Client, kubeClient client.Client, kubernetesClient kubernetes.Interface) *ProviderFactory {
	// Create shared providers
	pricingProvider := pricing.NewIBMPricingProvider(ctx, client)
	subnetProvider := subnet.NewProvider(client)
	instanceTypeProvider := instancetype.NewProvider(client, pricingProvider)

	return &ProviderFactory{
		client:               client,
		kubeClient:           kubeClient,
		kubernetesClient:     kubernetesClient,
		pricingProvider:      pricingProvider,
		subnetProvider:       subnetProvider,
		instanceTypeProvider: instanceTypeProvider,
	}
}

// GetInstanceProvider returns the appropriate instance provider based on the NodeClass
func (f *ProviderFactory) GetInstanceProvider(nodeClass *v1alpha1.IBMNodeClass) (commonTypes.InstanceProvider, error) {
	if nodeClass == nil {
		return nil, fmt.Errorf("nodeClass cannot be nil")
	}

	mode := f.determineProviderMode(nodeClass)

	switch mode {
	case commonTypes.IKSMode:
		return iksProvider.NewIKSWorkerPoolProvider(f.client, f.kubeClient)
	case commonTypes.VPCMode:
		if f.kubernetesClient != nil {
			return vpcProvider.NewVPCInstanceProvider(f.client, f.kubeClient, vpcProvider.WithKubernetesClient(f.kubernetesClient))
		}
		// Standard constructor without kubernetes client
		return vpcProvider.NewVPCInstanceProvider(f.client, f.kubeClient)
	default:
		return nil, fmt.Errorf("unknown provider mode: %s", mode)
	}
}

// GetVPCProvider returns a VPC-specific provider
func (f *ProviderFactory) GetVPCProvider(nodeClass *v1alpha1.IBMNodeClass) (commonTypes.VPCInstanceProvider, error) {
	if nodeClass == nil {
		return nil, fmt.Errorf("nodeClass cannot be nil")
	}

	mode := f.determineProviderMode(nodeClass)
	if mode != commonTypes.VPCMode {
		return nil, fmt.Errorf("VPC provider requested but NodeClass is configured for %s mode", mode)
	}

	if f.kubernetesClient != nil {
		return vpcProvider.NewVPCInstanceProvider(f.client, f.kubeClient, vpcProvider.WithKubernetesClient(f.kubernetesClient))
	}
	// Standard constructor without kubernetes client
	return vpcProvider.NewVPCInstanceProvider(f.client, f.kubeClient)
}

// GetIKSProvider returns an IKS-specific provider
func (f *ProviderFactory) GetIKSProvider(nodeClass *v1alpha1.IBMNodeClass) (commonTypes.IKSWorkerPoolProvider, error) {
	if nodeClass == nil {
		return nil, fmt.Errorf("nodeClass cannot be nil")
	}

	mode := f.determineProviderMode(nodeClass)
	if mode != commonTypes.IKSMode {
		return nil, fmt.Errorf("IKS provider requested but NodeClass is configured for %s mode", mode)
	}

	return iksProvider.NewIKSWorkerPoolProvider(f.client, f.kubeClient)
}

// determineProviderMode determines which provider mode to use based on NodeClass configuration
func (f *ProviderFactory) determineProviderMode(nodeClass *v1alpha1.IBMNodeClass) commonTypes.ProviderMode {
	// Handle nil nodeClass - default to VPC mode
	if nodeClass == nil {
		// Check environment variable for IKS mode
		if os.Getenv("IKS_CLUSTER_ID") != "" {
			return commonTypes.IKSMode
		}
		return commonTypes.VPCMode
	}

	// Check if bootstrap mode is explicitly set
	if nodeClass.Spec.BootstrapMode != nil {
		switch *nodeClass.Spec.BootstrapMode {
		case "iks-api":
			return commonTypes.IKSMode
		case "cloud-init":
			return commonTypes.VPCMode
		case "auto":
			// Continue with automatic detection based on other indicators
		}
	}

	// Check if IKS cluster ID is provided (implies IKS mode)
	if nodeClass.Spec.IKSClusterID != "" {
		return commonTypes.IKSMode
	}

	// Check environment variable
	if os.Getenv("IKS_CLUSTER_ID") != "" {
		return commonTypes.IKSMode
	}

	// Default to VPC mode
	return commonTypes.VPCMode
}

// GetProviderMode returns the provider mode for a given NodeClass
func (f *ProviderFactory) GetProviderMode(nodeClass *v1alpha1.IBMNodeClass) commonTypes.ProviderMode {
	return f.determineProviderMode(nodeClass)
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
