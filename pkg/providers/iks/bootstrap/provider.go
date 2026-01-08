/*
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

package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/httpclient"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

// IKSBootstrapProvider provides IKS-specific bootstrap functionality
type IKSBootstrapProvider struct {
	client     *ibm.Client
	k8sClient  kubernetes.Interface
	httpClient *httpclient.IBMCloudHTTPClient
}

// setIKSHeaders sets the required headers for IKS API requests
func (p *IKSBootstrapProvider) setIKSHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "karpenter-provider-ibm-cloud/1.0")
}

// NewIKSBootstrapProvider creates a new IKS bootstrap provider
func NewIKSBootstrapProvider(client *ibm.Client, k8sClient kubernetes.Interface) *IKSBootstrapProvider {
	provider := &IKSBootstrapProvider{
		client:    client,
		k8sClient: k8sClient,
	}

	// Initialize HTTP client for IKS API calls
	baseURL := "https://containers.cloud.ibm.com/global/v1"
	provider.httpClient = httpclient.NewIBMCloudHTTPClient(baseURL, provider.setIKSHeaders)

	return provider
}

// GetUserData generates IKS-specific user data (which is minimal since IKS handles bootstrapping)
func (p *IKSBootstrapProvider) GetUserData(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName) (string, error) {
	logger := log.FromContext(ctx)
	logger.Info("Generated IKS user data")

	// For IKS mode, we don't need complex bootstrap scripts since IKS handles most of the setup
	// The worker pool resize API will add nodes that are automatically configured
	// Return minimal user data or empty string

	// If there's custom user data specified, include it
	if strings.TrimSpace(nodeClass.Spec.UserData) != "" {
		logger.Info("Included custom user data for IKS node")
		return nodeClass.Spec.UserData, nil
	}

	// Default minimal user data for IKS
	return `#!/bin/bash
# IKS node provisioned by Karpenter IBM Cloud Provider
echo "Node provisioned via IKS worker pool resize API"
# IKS handles the Kubernetes bootstrap process automatically
`, nil
}

// GetClusterConfig retrieves cluster configuration from IKS API
func (p *IKSBootstrapProvider) GetClusterConfig(ctx context.Context, clusterID string) (*commonTypes.ClusterInfo, error) {
	logger := log.FromContext(ctx)

	if p.client == nil {
		return nil, fmt.Errorf("IBM client not initialized")
	}

	iksClient, err := p.client.GetIKSClient()
	if err != nil {
		return nil, fmt.Errorf("getting IKS client: %w", err)
	}

	logger.Info("Retrieved cluster config from IKS API", "cluster_id", clusterID)

	// Get kubeconfig from IKS API
	kubeconfig, err := iksClient.GetClusterConfig(ctx, clusterID)
	if err != nil {
		return nil, fmt.Errorf("getting cluster config from IKS API: %w", err)
	}

	// Parse kubeconfig to extract cluster information
	endpoint, caData, err := commonTypes.ParseKubeconfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig from IKS API: %w", err)
	}

	return &commonTypes.ClusterInfo{
		Endpoint:     endpoint,
		CAData:       caData,
		ClusterName:  p.getClusterName(),
		IsIKSManaged: true,
		IKSClusterID: clusterID,
	}, nil
}

// getClusterName gets the cluster name for IKS mode
func (p *IKSBootstrapProvider) getClusterName() string {
	if name := os.Getenv("CLUSTER_NAME"); name != "" {
		return name
	}
	if clusterID := os.Getenv("IKS_CLUSTER_ID"); clusterID != "" {
		return fmt.Sprintf("iks-cluster-%s", clusterID)
	}
	return "karpenter-iks-cluster"
}

// GetUserDataWithInstanceID generates IKS-specific user data with a known instance ID
// For IKS, the instance ID doesn't affect the user data since IKS handles node registration
func (p *IKSBootstrapProvider) GetUserDataWithInstanceID(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, nodeClaim types.NamespacedName, instanceID string) (string, error) {
	// For IKS, instance ID doesn't matter - delegate to regular GetUserData
	return p.GetUserData(ctx, nodeClass, nodeClaim)
}
