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
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"testing"
	"time"
)

func TestLegacyRetirementNeverDeletesSharedPool(t *testing.T) {
	for _, scenario := range []string{"owned", "foreign pool", "live node", "future checkpoint"} {
		t.Run(scenario, func(t *testing.T) {
			provider, kube, cloud, claim := allocationFixture(t)
			claim.Status.ProviderID = "ibm://" + testAccount + "///cluster/legacy-worker"
			proof := &LegacyRetirement{Version: 1, MinimumWriterVersion: 1, ClaimUID: string(claim.UID), ClusterUID: "cluster-uid", ClassUID: "class-uid", AccountID: testAccount, Region: "us-south", ClusterID: "cluster", PoolID: "shared-pool", WorkerID: "legacy-worker", ProviderID: claim.Status.ProviderID, NodeUID: "legacy-node-uid", Zone: "us-south-1"}
			if scenario == "future checkpoint" {
				proof.Version = 2
			}
			encoded, err := json.Marshal(proof)
			require.NoError(t, err)
			claim.Annotations = map[string]string{LegacyRetirementAnnotation: string(encoded), ownership.BackendAnnotation: "iks"}
			claim.Finalizers = []string{v1.TerminationFinalizer}
			require.NoError(t, kube.Update(context.Background(), claim))
			claim.Status.ProviderID = proof.ProviderID
			require.NoError(t, kube.Status().Update(context.Background(), claim))
			require.NoError(t, kube.Delete(context.Background(), claim))
			cloud.pool = &ibm.WorkerPool{ID: "shared-pool", Name: "shared", SizePerZone: 10, ActualSize: 10}
			cloud.worker = &ibm.IKSWorkerDetails{ID: "legacy-worker", PoolID: "shared-pool", Location: "us-south-1"}
			if scenario == "foreign pool" {
				cloud.worker.PoolID = "foreign-pool"
			}
			if scenario == "live node" {
				require.NoError(t, kube.Create(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "legacy-node", UID: "legacy-node-uid", DeletionTimestamp: &metav1.Time{Time: time.Now()}, Finalizers: []string{v1.TerminationFinalizer}}, Spec: corev1.NodeSpec{ProviderID: claim.Status.ProviderID}}))
			}
			fresh := &v1.NodeClaim{}
			require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
			synthetic := &corev1.Node{ObjectMeta: *fresh.ObjectMeta.DeepCopy(), Spec: corev1.NodeSpec{ProviderID: fresh.Status.ProviderID}}
			err = provider.Delete(context.Background(), synthetic)
			if scenario == "owned" {
				require.NoError(t, err)
				require.Nil(t, cloud.worker)
				require.True(t, cloudprovider.IsNodeClaimNotFoundError(provider.ConfirmGone(context.Background(), fresh)))
			} else {
				require.Error(t, err)
				require.NotNil(t, cloud.worker)
			}
			require.Empty(t, cloud.deleteCalls)
			require.Equal(t, 10, cloud.pool.SizePerZone)
		})
	}
}

func TestAllocationFutureVersionAndNamePrefix(t *testing.T) {
	provider, kube, _, claim := allocationFixture(t)
	class := &v1alpha1.IBMNodeClass{}
	require.NoError(t, kube.Get(context.Background(), client.ObjectKey{Name: "class"}, class))
	class.Spec.IKSDynamicPools.NamePrefix = "tenant"
	require.NoError(t, kube.Update(context.Background(), class))
	class.Status.Conditions[0].ObservedGeneration = class.Generation
	require.NoError(t, kube.Status().Update(context.Background(), class))
	_, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	fresh := &v1.NodeClaim{}
	require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	allocation, err := DecodeAllocation(fresh.Annotations)
	require.NoError(t, err)
	require.Contains(t, allocation.PoolName, "tenant-")
	require.Equal(t, 1, allocation.Version)
	allocation.Version = 2
	data, err := json.Marshal(allocation)
	require.NoError(t, err)
	_, err = DecodeAllocation(map[string]string{AllocationAnnotation: string(data)})
	require.Error(t, err)
}
