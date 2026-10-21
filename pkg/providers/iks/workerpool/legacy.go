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
	"encoding/json"
	"fmt"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"slices"
	"strings"
)

const LegacyRetirementAnnotation = "karpenter-ibm.sh/iks-legacy-retirement"

type LegacyRetirement struct {
	Version              int    `json:"version"`
	MinimumWriterVersion int    `json:"minimumWriterVersion"`
	ClaimUID             string `json:"claimUID"`
	ClusterUID           string `json:"clusterUID"`
	ClassUID             string `json:"classUID"`
	AccountID            string `json:"accountID"`
	Region               string `json:"region"`
	ClusterID            string `json:"clusterID"`
	PoolID               string `json:"poolID"`
	WorkerID             string `json:"workerID"`
	ProviderID           string `json:"providerID"`
	NodeUID              string `json:"nodeUID"`
	Zone                 string `json:"zone"`
}

func decodeLegacyRetirement(value string) (*LegacyRetirement, error) {
	if value == "" {
		return nil, fmt.Errorf("legacy worker requires a verified retirement checkpoint; preserve claim UID and bind the exact worker before retirement")
	}
	proof := &LegacyRetirement{}
	d := json.NewDecoder(strings.NewReader(value))
	d.DisallowUnknownFields()
	if err := d.Decode(proof); err != nil {
		return nil, err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing legacy retirement checkpoint")
	}
	if err := ownership.ValidateStateVersion(proof.Version, proof.MinimumWriterVersion); err != nil {
		return nil, err
	}
	if proof.ClaimUID == "" || proof.ClusterUID == "" || proof.ClassUID == "" || proof.Region == "" || proof.ClusterID == "" || proof.PoolID == "" || proof.WorkerID == "" || proof.NodeUID == "" || proof.Zone == "" || !accountPattern.MatchString(proof.AccountID) || proof.ProviderID != fmt.Sprintf("ibm://%s///%s/%s", proof.AccountID, proof.ClusterID, proof.WorkerID) {
		return nil, fmt.Errorf("legacy retirement checkpoint has incomplete identity")
	}
	return proof, nil
}

func (p *IKSWorkerPoolProvider) freshLegacyClaim(ctx context.Context, claim *v1.NodeClaim) (*v1.NodeClaim, error) {
	if claim == nil || claim.UID == "" {
		return nil, fmt.Errorf("persisted legacy claim UID is required")
	}
	fresh := &v1.NodeClaim{}
	if err := p.reader().Get(ctx, client.ObjectKeyFromObject(claim), fresh); err != nil {
		return nil, err
	}
	if fresh.UID != claim.UID || fresh.Status.ProviderID != claim.Status.ProviderID || fresh.Annotations[LegacyRetirementAnnotation] != claim.Annotations[LegacyRetirementAnnotation] || fresh.Annotations[AllocationAnnotation] != "" {
		return nil, fmt.Errorf("legacy claim identity changed")
	}
	return fresh, nil
}

func (p *IKSWorkerPoolProvider) PrepareLegacyRetirement(ctx context.Context, claim *v1.NodeClaim) error {
	if claim.Annotations[AllocationAnnotation] != "" {
		return nil
	}
	if claim.Annotations[LegacyRetirementAnnotation] != "" {
		_, err := decodeLegacyRetirement(claim.Annotations[LegacyRetirementAnnotation])
		return err
	}
	fresh, err := p.freshLegacyClaim(ctx, claim)
	if err != nil {
		return err
	}
	account, cluster, workerID, err := ParseProviderID(fresh.Status.ProviderID)
	if err != nil {
		return err
	}
	cloud, err := p.getIKSClient()
	if err != nil {
		return err
	}
	worker, err := cloud.GetWorkerDetails(ctx, cluster, workerID)
	if err != nil {
		return err
	}
	if worker == nil || worker.ID != workerID || worker.PoolID == "" || worker.Location == "" || worker.Lifecycle.ActualState == "deleted" {
		return fmt.Errorf("legacy worker identity cannot be established")
	}
	region := ibm.ExtractRegionFromZone(worker.Location)
	if validationErr := p.validateTarget(cloud, account, region); validationErr != nil {
		return validationErr
	}
	if stored := fresh.Annotations[ownership.PoolIDAnnotation]; stored != "" && stored != worker.PoolID {
		return fmt.Errorf("legacy worker pool changed")
	}
	node, err := p.registeredNode(ctx, fresh.Status.ProviderID)
	if err != nil {
		return err
	}
	if node == nil || node.UID == "" || !slices.ContainsFunc(node.OwnerReferences, func(ref metav1.OwnerReference) bool { return ref.Kind == "NodeClaim" && ref.UID == fresh.UID }) {
		return fmt.Errorf("legacy worker requires its freshly verified claim-owned Node or an operator-pinned retirement checkpoint")
	}
	matchesIP := false
	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP {
			for _, nic := range worker.NetworkInterfaces {
				if nic.IPAddress == address.Address {
					matchesIP = true
				}
			}
		}
	}
	if !matchesIP {
		return fmt.Errorf("legacy worker network does not match the Node")
	}
	classUID := node.Labels[ownership.NodeClassUIDLabel]
	if classUID == "" {
		return fmt.Errorf("legacy Node lacks immutable NodeClass identity; operator must pin verified retirement checkpoint")
	}
	clusterUID, err := ownership.ClusterUID(ctx, p.reader())
	if err != nil {
		return err
	}
	proof := &LegacyRetirement{Version: 1, MinimumWriterVersion: 1, ClaimUID: string(fresh.UID), ClusterUID: clusterUID, ClassUID: classUID, AccountID: account, Region: region, ClusterID: cluster, PoolID: worker.PoolID, WorkerID: workerID, ProviderID: fresh.Status.ProviderID, NodeUID: string(node.UID), Zone: worker.Location}
	if _, readErr := p.freshLegacyClaim(ctx, fresh); readErr != nil {
		return readErr
	}
	stored := fresh.DeepCopy()
	if fresh.Annotations == nil {
		fresh.Annotations = map[string]string{}
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		return err
	}
	fresh.Annotations[LegacyRetirementAnnotation] = string(encoded)
	return p.kubeClient.Patch(ctx, fresh, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{}))
}

func (p *IKSWorkerPoolProvider) retireLegacy(ctx context.Context, claim *v1.NodeClaim, confirmOnly bool) error {
	fresh, err := p.freshLegacyClaim(ctx, claim)
	if err != nil {
		return err
	}
	proof, err := decodeLegacyRetirement(fresh.Annotations[LegacyRetirementAnnotation])
	if err != nil {
		return err
	}
	if proof.ClaimUID != string(fresh.UID) || proof.ProviderID != fresh.Status.ProviderID {
		return fmt.Errorf("legacy retirement owner changed")
	}
	clusterUID, err := ownership.ClusterUID(ctx, p.reader())
	if err != nil {
		return err
	}
	if clusterUID != proof.ClusterUID {
		return fmt.Errorf("legacy retirement belongs to another cluster")
	}
	cloud, err := p.getIKSClient()
	if err != nil {
		return err
	}
	if validationErr := p.validateTarget(cloud, proof.AccountID, proof.Region); validationErr != nil {
		return validationErr
	}
	node, err := p.registeredNode(ctx, proof.ProviderID)
	if err != nil {
		return err
	}
	if node != nil {
		return fmt.Errorf("legacy worker Node must finish graceful drain before cloud retirement")
	}
	worker, err := cloud.GetWorkerDetails(ctx, proof.ClusterID, proof.WorkerID)
	if IsNotFound(err) || (err == nil && worker != nil && worker.ID == proof.WorkerID && worker.Lifecycle.ActualState == "deleted") {
		if _, readErr := p.freshLegacyClaim(ctx, fresh); readErr != nil {
			return readErr
		}
		return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("legacy worker is absent"))
	}
	if err != nil {
		return err
	}
	if worker == nil || worker.ID != proof.WorkerID || worker.PoolID != proof.PoolID || worker.Location != proof.Zone {
		return fmt.Errorf("legacy worker differs from retirement checkpoint")
	}
	if confirmOnly {
		return nil
	}
	if fresh.DeletionTimestamp.IsZero() {
		return fmt.Errorf("legacy worker retirement requires a deleting claim")
	}
	if _, readErr := p.freshLegacyClaim(ctx, fresh); readErr != nil {
		return readErr
	}
	return cloud.RemoveWorker(ctx, proof.ClusterID, proof.WorkerID)
}
