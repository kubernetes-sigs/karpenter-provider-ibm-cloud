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

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

// IKSWorkerRequest represents a request to add a worker to an IKS cluster
type IKSWorkerRequest struct {
	ClusterID     string            `json:"cluster_id"`
	WorkerPoolID  string            `json:"worker_pool_id,omitempty"`
	VPCInstanceID string            `json:"vpc_instance_id"`
	Zone          string            `json:"zone"`
	Labels        map[string]string `json:"labels,omitempty"`
	Taints        []IKSTaint        `json:"taints,omitempty"`
}

// IKSTaint represents a Kubernetes taint for IKS workers
type IKSTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

// IKSWorkerResponse represents the response from IKS worker creation
type IKSWorkerResponse struct {
	ID       string            `json:"id"`
	State    string            `json:"state"`
	Message  string            `json:"message"`
	Labels   map[string]string `json:"labels"`
	Location string            `json:"location"`
}

// AddWorkerToIKSCluster adds a VPC instance to an IKS cluster as a worker node
func (p *IKSBootstrapProvider) AddWorkerToIKSCluster(ctx context.Context, options types.IKSWorkerPoolOptions) (*IKSWorkerResponse, error) {
	logger := log.FromContext(ctx)

	// Get IAM token
	token, err := p.client.GetIAMClient().GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting IAM token: %w", err)
	}

	// Prepare request payload
	request := IKSWorkerRequest{
		ClusterID:     options.ClusterID,
		WorkerPoolID:  options.WorkerPoolID,
		VPCInstanceID: options.VPCInstanceID,
		Zone:          options.Zone,
		Labels: map[string]string{
			"karpenter.sh/managed":         "true",
			"karpenter-ibm.sh/zone":        options.Zone,
			"karpenter-ibm.sh/provisioner": "karpenter-ibm",
		},
	}

	// Construct API endpoint
	endpoint := fmt.Sprintf("/clusters/%s/workers", options.ClusterID)

	// Make request using shared HTTP client
	var response IKSWorkerResponse
	if err := p.httpClient.PostJSON(ctx, endpoint, token, &request, &response); err != nil {
		return nil, fmt.Errorf("IKS API error: %w", err)
	}

	logger.Info("Successfully added worker to IKS cluster", "worker_id", response.ID, "state", response.State)
	return &response, nil
}
