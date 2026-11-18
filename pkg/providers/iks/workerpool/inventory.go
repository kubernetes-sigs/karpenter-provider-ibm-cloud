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

package workerpool

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

type workerInventory struct {
	allocations map[string]*Allocation
	nodes       map[string]*corev1.Node
}

func indexWorkerInventory(claims []*karpv1.NodeClaim, nodes []*corev1.Node) (*workerInventory, error) {
	index := &workerInventory{allocations: map[string]*Allocation{}, nodes: map[string]*corev1.Node{}}
	for _, claim := range claims {
		allocation, err := DecodeAllocation(claim.Annotations)
		if err != nil {
			return nil, fmt.Errorf("claim %s inventory: %w", claim.Name, err)
		}
		if allocation == nil || allocation.WorkerID == "" {
			continue
		}
		if allocation.ClaimUID != string(claim.UID) {
			return nil, fmt.Errorf("claim %s allocation UID differs", claim.Name)
		}
		id := fmt.Sprintf("ibm://%s///%s/%s", allocation.AccountID, allocation.ClusterID, allocation.WorkerID)
		if index.allocations[id] != nil {
			return nil, fmt.Errorf("multiple claims have allocated provider ID %s", id)
		}
		index.allocations[id] = allocation
	}
	for _, node := range nodes {
		if _, _, _, err := ParseProviderID(node.Spec.ProviderID); err != nil {
			continue
		}
		if index.nodes[node.Spec.ProviderID] != nil {
			return nil, fmt.Errorf("multiple Nodes have the allocated provider ID")
		}
		index.nodes[node.Spec.ProviderID] = node
	}
	return index, nil
}

func (p *IKSWorkerPoolProvider) GetFresh(ctx context.Context, providerID string) (*corev1.Node, error) {
	return p.getWorker(ctx, providerID)
}

func (p *IKSWorkerPoolProvider) ListFresh(ctx context.Context, claims []*karpv1.NodeClaim, nodes []*corev1.Node) ([]*corev1.Node, error) {
	index, err := indexWorkerInventory(claims, nodes)
	if err != nil {
		return nil, err
	}
	iks, err := p.getIKSClient()
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for id := range index.allocations {
		ids[id] = true
	}
	for id := range index.nodes {
		if _, _, _, err := ParseProviderID(id); err == nil {
			ids[id] = true
		}
	}
	workers := map[string]map[string]*ibm.IKSWorkerDetails{}
	for id := range ids {
		account, cluster, _, err := ParseProviderID(id)
		if err != nil {
			return nil, err
		}
		region := ""
		if allocation := index.allocations[id]; allocation != nil {
			region = allocation.Region
		}
		if targetErr := p.validateTarget(iks, account, region); targetErr != nil {
			return nil, targetErr
		}
		if workers[cluster] != nil {
			continue
		}
		listed, err := iks.ListWorkers(ctx, cluster)
		if err != nil {
			return nil, fmt.Errorf("listing workers in cluster %s: %w", cluster, err)
		}
		workers[cluster] = map[string]*ibm.IKSWorkerDetails{}
		for _, worker := range listed {
			if worker == nil || worker.ID == "" {
				return nil, fmt.Errorf("worker inventory has missing identity")
			}
			if workers[cluster][worker.ID] != nil {
				return nil, fmt.Errorf("worker inventory has duplicate identity")
			}
			workers[cluster][worker.ID] = worker
		}
	}
	result := make([]*corev1.Node, 0, len(ids))
	for id := range ids {
		account, cluster, workerID, _ := ParseProviderID(id)
		worker := workers[cluster][workerID]
		allocation := index.allocations[id]
		if worker == nil || worker.Lifecycle.ActualState == "deleted" {
			if allocation == nil || allocation.Region == "" {
				return nil, fmt.Errorf("worker absence cannot be confirmed without immutable region identity")
			}
			continue
		}
		if allocation != nil && (allocation.PoolID != worker.PoolID || !allocationHasZone(allocation, worker.Location)) {
			return nil, fmt.Errorf("worker inventory differs from immutable allocation")
		}
		node := workerNode(account, cluster, ibm.ExtractRegionFromZone(worker.Location), worker)
		if allocation != nil {
			node = nodeForAllocation(allocation, worker)
		}
		if registered := index.nodes[id]; registered != nil {
			saved := registered.DeepCopy()
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

func (p *IKSWorkerPoolProvider) listIndexedAllocations(ctx context.Context) ([]*corev1.Node, error) {
	current, err := p.readInventory(ctx)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("kubernetes client not set")
	}
	claims := make([]*karpv1.NodeClaim, 0, len(current.claims))
	for i := range current.claims {
		claims = append(claims, &current.claims[i])
	}
	nodes := make([]*corev1.Node, 0, len(current.nodes))
	for i := range current.nodes {
		nodes = append(nodes, &current.nodes[i])
	}
	return p.ListFresh(ctx, claims, nodes)
}

func allocationHasZone(allocation *Allocation, zone string) bool {
	for _, saved := range allocation.Request.Zones {
		if saved.ID == zone {
			return true
		}
	}
	return false
}
