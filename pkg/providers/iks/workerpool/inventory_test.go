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
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

type bulkWorkers struct {
	ibm.IKSClientInterface
	workers   []*ibm.IKSWorkerDetails
	listCalls int
}

func (*bulkWorkers) GetAccountID() string { return testAccount }
func (*bulkWorkers) GetRegion() string    { return "us-south" }
func (w *bulkWorkers) ListWorkers(context.Context, string) ([]*ibm.IKSWorkerDetails, error) {
	w.listCalls++
	return w.workers, nil
}

type inventoryReader struct {
	client.Reader
	lists int
}

func (r *inventoryReader) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	r.lists++
	return r.Reader.List(ctx, list, options...)
}

func TestBulkInventoryReadsKubernetesOnceAndWorkersOnce(t *testing.T) {
	ctx := context.Background()
	provider, kube, _, claim := allocationFixture(t)
	_, err := provider.Create(ctx, claim, nil)
	require.NoError(t, err)
	fresh := &karpv1.NodeClaim{}
	require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(claim), fresh))
	base, err := DecodeAllocation(fresh.Annotations)
	require.NoError(t, err)
	require.NoError(t, kube.Delete(ctx, fresh))
	cloud := &bulkWorkers{}
	for i := range 500 {
		allocation := *base
		allocation.ClaimName = fmt.Sprintf("claim-%d", i)
		allocation.ClaimUID = fmt.Sprintf("uid-%d", i)
		allocation.WorkerID = fmt.Sprintf("worker-%d", i)
		allocation.PoolID = fmt.Sprintf("pool-%d", i)
		encoded, marshalErr := json.Marshal(allocation)
		require.NoError(t, marshalErr)
		id := fmt.Sprintf("ibm://%s///cluster/%s", testAccount, allocation.WorkerID)
		current := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: allocation.ClaimName, UID: types.UID(allocation.ClaimUID), Annotations: map[string]string{AllocationAnnotation: string(encoded)}}, Status: karpv1.NodeClaimStatus{ProviderID: id}}
		require.NoError(t, kube.Create(ctx, current))
		require.NoError(t, kube.Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: allocation.WorkerID, Labels: map[string]string{"example.com/retained": "value"}, Annotations: map[string]string{"example.com/retained": "value"}}, Spec: corev1.NodeSpec{ProviderID: id}}))
		cloud.workers = append(cloud.workers, &ibm.IKSWorkerDetails{ID: allocation.WorkerID, PoolID: allocation.PoolID, Location: "us-south-1", Flavor: "bx2-4x16"})
	}
	reader := &inventoryReader{Reader: kube}
	provider.apiReader = reader
	provider.iksClient = cloud
	nodes, err := provider.List(ctx)
	require.NoError(t, err)
	require.Len(t, nodes, 500)
	for _, node := range nodes {
		require.Equal(t, "value", node.Labels["example.com/retained"])
		require.Equal(t, "value", node.Annotations["example.com/retained"])
		require.Equal(t, "bx2-4x16", node.Labels[corev1.LabelInstanceTypeStable])
	}
	require.Equal(t, 2, reader.lists)
	require.Equal(t, 1, cloud.listCalls)
}

func TestBulkInventoryRejectsDuplicateRegistrationAndChangedOwnership(t *testing.T) {
	provider, kube, _, claim := allocationFixture(t)
	ctx := context.Background()
	node, err := provider.Create(ctx, claim, nil)
	require.NoError(t, err)
	fresh := &karpv1.NodeClaim{}
	require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(claim), fresh))
	_, err = indexWorkerInventory([]*karpv1.NodeClaim{fresh}, []*corev1.Node{node, node.DeepCopy()})
	require.Error(t, err)
	fresh.UID = "replacement-uid"
	_, err = indexWorkerInventory([]*karpv1.NodeClaim{fresh}, nil)
	require.Error(t, err)
}
