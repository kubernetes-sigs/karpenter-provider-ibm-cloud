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

package instance

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func (p *VPCInstanceProvider) ListFresh(ctx context.Context, claims []*karpv1.NodeClaim, nodes []*corev1.Node) ([]*corev1.Node, error) {
	byID := map[string]bool{}
	registered := map[string]*corev1.Node{}
	for _, node := range nodes {
		if node.Spec.ProviderID != "" {
			if registered[node.Spec.ProviderID] != nil {
				return nil, fmt.Errorf("multiple Nodes have the allocated provider ID")
			}
			registered[node.Spec.ProviderID] = node
			byID[node.Spec.ProviderID] = true
		}
	}
	for _, claim := range claims {
		if claim.Status.ProviderID != "" {
			byID[claim.Status.ProviderID] = true
		}
	}
	result := make([]*corev1.Node, 0, len(byID))
	for id := range byID {
		p.scheduleInventoryMetrics(providerRegion(id))
		node, err := p.GetFresh(ctx, id)
		if cloudprovider.IsNodeClaimNotFoundError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if current := registered[id]; current != nil {
			saved := current.DeepCopy()
			for key, value := range node.Labels {
				if saved.Labels == nil {
					saved.Labels = map[string]string{}
				}
				saved.Labels[key] = value
			}
			for key, value := range node.Annotations {
				if saved.Annotations == nil {
					saved.Annotations = map[string]string{}
				}
				saved.Annotations[key] = value
			}
			node = saved
		}
		result = append(result, node)
	}
	return result, nil
}
