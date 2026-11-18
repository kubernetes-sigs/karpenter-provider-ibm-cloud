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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/httpclient"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

const testAccount = "0123456789abcdef0123456789abcdef"

type allocationCatalog struct {
	instancetype.Provider
	price float64
}

func (catalog allocationCatalog) Get(context.Context, string, *v1alpha1.IBMNodeClass) (*cloudprovider.InstanceType, error) {
	return &cloudprovider.InstanceType{Name: "bx2-4x16", Requirements: scheduling.NewLabelRequirements(map[string]string{corev1.LabelInstanceTypeStable: "bx2-4x16", corev1.LabelArchStable: "amd64", corev1.LabelOSStable: "linux"}),
		Capacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}, Overhead: &cloudprovider.InstanceTypeOverhead{},
		Offerings: cloudprovider.Offerings{&cloudprovider.Offering{Available: true, Price: catalog.price, Requirements: scheduling.NewLabelRequirements(map[string]string{corev1.LabelTopologyZone: "us-south-1", v1.CapacityTypeLabelKey: v1.CapacityTypeOnDemand})}}}, nil
}

func (catalog allocationCatalog) FilterInstanceTypes(ctx context.Context, requirements *v1alpha1.InstanceTypeRequirements, class *v1alpha1.IBMNodeClass) ([]*cloudprovider.InstanceType, error) {
	instanceType, err := catalog.Get(ctx, "bx2-4x16", class)
	if err != nil {
		return nil, err
	}
	if requirements.MinimumCPU > 4 || requirements.MinimumMemory > 16 || (requirements.Architecture != "" && requirements.Architecture != "amd64") {
		return nil, nil
	}
	return []*cloudprovider.InstanceType{instanceType}, nil
}

type allocationCloud struct {
	ibm.IKSClientInterface
	mu          sync.Mutex
	pool        *ibm.WorkerPool
	worker      *ibm.IKSWorkerDetails
	createCalls int
	deleteCalls []string
	createError error
	getError    error
	keepWorker  bool
	account     string
}

func (c *allocationCloud) GetAccountID() string {
	if c.account != "" {
		return c.account
	}
	return testAccount
}
func (c *allocationCloud) GetRegion() string { return "us-south" }
func (c *allocationCloud) RemoveWorker(context.Context, string, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.worker = nil
	return nil
}

func (c *allocationCloud) GetWorkerPool(_ context.Context, clusterID, pool string) (*ibm.WorkerPool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getError != nil {
		return nil, c.getError
	}
	// IKS resolves the pool argument as either a name or an ID within the cluster.
	if c.pool == nil || clusterID != "cluster" || (pool != c.pool.ID && pool != c.pool.Name) {
		return nil, &httpclient.IBMCloudError{StatusCode: 404}
	}
	copy := *c.pool
	return &copy, nil
}
func (c *allocationCloud) CreateWorkerPool(_ context.Context, _ string, request *ibm.WorkerPoolCreateRequest) (*ibm.WorkerPool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createCalls++
	c.pool = &ibm.WorkerPool{ID: "real-pool", Name: request.Name, Flavor: request.Flavor, SizePerZone: 1, Zones: request.Zones, Labels: request.Labels}
	c.worker = &ibm.IKSWorkerDetails{ID: "real-worker", PoolID: c.pool.ID, Flavor: request.Flavor, Location: request.Zones[0].ID, NetworkInterfaces: []ibm.IKSNetworkInterface{{Primary: true, SubnetID: request.Zones[0].SubnetID}}}
	if c.createError != nil {
		return nil, c.createError
	}
	return c.pool, nil
}
func (c *allocationCloud) ListWorkers(context.Context, string) ([]*ibm.IKSWorkerDetails, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getError != nil {
		return nil, c.getError
	}
	if c.worker == nil {
		return nil, nil
	}
	copy := *c.worker
	return []*ibm.IKSWorkerDetails{&copy}, nil
}
func (c *allocationCloud) GetWorkerDetails(context.Context, string, string) (*ibm.IKSWorkerDetails, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getError != nil {
		return nil, c.getError
	}
	if c.worker == nil {
		return nil, &httpclient.IBMCloudError{StatusCode: 404}
	}
	copy := *c.worker
	return &copy, nil
}
func (c *allocationCloud) DeleteWorkerPool(_ context.Context, _ string, poolID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleteCalls = append(c.deleteCalls, poolID)
	c.pool = nil
	if !c.keepWorker {
		c.worker = nil
	}
	return nil
}

func allocationFixture(t *testing.T) (*IKSWorkerPoolProvider, client.Client, *allocationCloud, *v1.NodeClaim) {
	t.Helper()
	t.Setenv("IBM_ACCOUNT_ID", testAccount)
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &v1.NodeClaim{}, &v1.NodeClaimList{})
	claim := &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", UID: "claim-uid", Labels: map[string]string{v1.NodePoolLabelKey: "pool"}},
		Spec: v1.NodeClaimSpec{NodeClassRef: &v1.NodeClassReference{Group: v1alpha1.Group, Kind: "IBMNodeClass", Name: "class"}}}
	nodeClass := &v1alpha1.IBMNodeClass{ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "class-uid", Generation: 1},
		Spec:   v1alpha1.IBMNodeClassSpec{IKSClusterID: "cluster", Region: "us-south", Zone: "us-south-1", VPC: "vpc", Subnet: "subnet", InstanceProfile: "bx2-4x16", IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{Enabled: true}},
		Status: v1alpha1.IBMNodeClassStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: 1}}}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(claim, nodeClass).
		WithObjects(claim, nodeClass, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}}).Build()
	cloud := &allocationCloud{}
	provider, err := NewIKSWorkerPoolProvider(&ibm.Client{}, kubeClient, WithIKSClient(cloud), WithAPIReader(kubeClient), WithInstanceTypeProvider(allocationCatalog{}))
	require.NoError(t, err)
	return provider.(*IKSWorkerPoolProvider), kubeClient, cloud, claim
}

func registerAllocatedNode(t *testing.T, kubeClient client.Client) *corev1.Node {
	t.Helper()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "registered-worker", Labels: map[string]string{ownership.ClaimUIDLabel: "claim-uid", ownership.ClusterUIDLabel: "cluster-uid"}},
		Spec: corev1.NodeSpec{ProviderID: "ibm://" + testAccount + "///cluster/real-worker"},
		Status: corev1.NodeStatus{Capacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")},
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3800m"), corev1.ResourceMemory: resource.MustParse("15Gi")}}}
	require.NoError(t, kubeClient.Create(context.Background(), node))
	return node
}

func TestAllocationLaunchesBeforeRegistrationAndAdoptsRegisteredResources(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	result, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	require.Equal(t, resource.MustParse("4"), result.Status.Capacity[corev1.ResourceCPU])
	require.False(t, result.Status.Allocatable.Cpu().IsZero())
	fresh := &v1.NodeClaim{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	require.Contains(t, fresh.Finalizers, AllocationFinalizer)
	allocation, err := DecodeAllocation(fresh.Annotations)
	require.NoError(t, err)
	require.Equal(t, "real-worker", allocation.WorkerID)
	reservation := &corev1.ConfigMap{}
	require.NoError(t, kubeClient.Get(context.Background(), ReservationKey("cluster", allocation.PoolName, "karpenter"), reservation))
	require.Equal(t, "active", reservation.Data[ReservationPhaseKey])
	registered := registerAllocatedNode(t, kubeClient)
	result, err = provider.Create(context.Background(), fresh, nil)
	require.NoError(t, err)
	require.Equal(t, registered.Spec.ProviderID, result.Spec.ProviderID)
	require.Equal(t, registered.Name, result.Name)
	require.Equal(t, registered.Status.Capacity, result.Status.Capacity)
	require.Equal(t, registered.Status.Allocatable, result.Status.Allocatable)
	require.Equal(t, 1, cloud.createCalls)
}

func TestAllocationRecoversLostCreateResponseAcrossProviderRestart(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	cloud.createError = fmt.Errorf("response lost")
	_, err := provider.Create(context.Background(), claim, nil)
	require.Error(t, err)
	registerAllocatedNode(t, kubeClient)
	restarted, err := NewIKSWorkerPoolProvider(&ibm.Client{}, kubeClient, WithIKSClient(cloud))
	require.NoError(t, err)
	result, err := restarted.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	require.Contains(t, result.Spec.ProviderID, "/real-worker")
	require.Equal(t, 1, cloud.createCalls)
}

func TestAllocationResumesAfterNodeClassDeleted(t *testing.T) {
	provider, kubeClient, _, claim := allocationFixture(t)
	_, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	require.NoError(t, kubeClient.Delete(context.Background(), &v1alpha1.IBMNodeClass{ObjectMeta: metav1.ObjectMeta{Name: "class"}}))
	registerAllocatedNode(t, kubeClient)
	_, err = provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
}

func TestAllocationRejectsStaticPoolWithoutMutation(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	nodeClass := &v1alpha1.IBMNodeClass{}
	require.NoError(t, kubeClient.Get(context.Background(), types.NamespacedName{Name: "class"}, nodeClass))
	nodeClass.Spec.IKSDynamicPools.Enabled = false
	require.NoError(t, kubeClient.Update(context.Background(), nodeClass))
	_, err := provider.Create(context.Background(), claim, nil)
	require.ErrorContains(t, err, "iksDynamicPools.enabled")
	require.Zero(t, cloud.createCalls)
}

func TestAllocationRejectsForeignPoolOwnership(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	_, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	cloud.pool.Labels[ownership.ClaimUIDLabel] = "another-claim"
	fresh := &v1.NodeClaim{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	require.Error(t, provider.Cleanup(context.Background(), fresh))
	require.Empty(t, cloud.deleteCalls)
}

func TestExactPoolDeletionWaitsForWorkerAbsence(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	registered := registerAllocatedNode(t, kubeClient)
	node, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	node.UID = claim.UID
	require.NoError(t, kubeClient.Delete(context.Background(), registered))
	cloud.keepWorker = true
	require.NoError(t, provider.Delete(context.Background(), node))
	require.Equal(t, []string{"real-pool"}, cloud.deleteCalls)
	err = provider.Delete(context.Background(), node)
	require.Error(t, err)
	require.False(t, cloudprovider.IsNodeClaimNotFoundError(err))
	cloud.worker = nil
	require.True(t, cloudprovider.IsNodeClaimNotFoundError(provider.Delete(context.Background(), node)))
}

func TestPendingCleanupDelegatesNodeDrainBeforeCloudDeletion(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	_, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	node := registerAllocatedNode(t, kubeClient)
	node.Finalizers = []string{v1.TerminationFinalizer}
	node.Labels[v1.NodePoolLabelKey] = "pool"
	node.Labels[v1.NodeClassLabelKey(v1alpha1.GroupVersion.WithKind("IBMNodeClass").GroupKind())] = "class"
	require.NoError(t, kubeClient.Update(context.Background(), node))
	fresh := &v1.NodeClaim{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	require.NoError(t, provider.Cleanup(context.Background(), fresh))
	require.Empty(t, cloud.deleteCalls)
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(node), node))
	require.False(t, node.DeletionTimestamp.IsZero())
	require.Contains(t, node.Finalizers, v1.TerminationFinalizer)
	node.Finalizers = nil
	require.NoError(t, kubeClient.Update(context.Background(), node))
	require.NoError(t, provider.Cleanup(context.Background(), fresh))
	require.Equal(t, []string{"real-pool"}, cloud.deleteCalls)
}

func TestWorkerLookupPreservesUncertaintyAndUsesRegisteredResources(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	_, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	registered := registerAllocatedNode(t, kubeClient)
	node, err := provider.Get(context.Background(), registered.Spec.ProviderID)
	require.NoError(t, err)
	require.Equal(t, registered.Status.Allocatable, node.Status.Allocatable)
	cloud.getError = &httpclient.IBMCloudError{StatusCode: 429}
	_, err = provider.Get(context.Background(), registered.Spec.ProviderID)
	require.Error(t, err)
	require.False(t, cloudprovider.IsNodeClaimNotFoundError(err))
	cloud.getError = nil
	cloud.worker = nil
	_, err = provider.Get(context.Background(), registered.Spec.ProviderID)
	require.True(t, cloudprovider.IsNodeClaimNotFoundError(err))
}

func TestAllocationConcurrentRetriesCreateOnePool(t *testing.T) {
	provider, _, cloud, claim := allocationFixture(t)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); _, _ = provider.Create(context.Background(), claim.DeepCopy(), nil) }()
	}
	group.Wait()
	require.Equal(t, 1, cloud.createCalls)
}

type checkpointFailureClient struct{ client.Client }

func (c *checkpointFailureClient) Patch(context.Context, client.Object, client.Patch, ...client.PatchOption) error {
	return fmt.Errorf("checkpoint unavailable")
}

func TestAllocationDoesNotMutateCloudWhenCheckpointFails(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	provider.kubeClient = &checkpointFailureClient{Client: kubeClient}
	_, err := provider.Create(context.Background(), claim, nil)
	require.Error(t, err)
	require.Zero(t, cloud.createCalls)
}

func TestAllocationRejectsRequirementsBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name, key string
		operator  corev1.NodeSelectorOperator
		values    []string
	}{
		{name: "wrong flavor", key: corev1.LabelInstanceTypeStable, operator: corev1.NodeSelectorOpIn, values: []string{"mx2-8x64"}},
		{name: "wrong architecture", key: corev1.LabelArchStable, operator: corev1.NodeSelectorOpIn, values: []string{"arm64"}},
		{name: "excluded zone", key: corev1.LabelTopologyZone, operator: corev1.NodeSelectorOpNotIn, values: []string{"us-south-1"}},
		{name: "spot capacity", key: v1.CapacityTypeLabelKey, operator: corev1.NodeSelectorOpIn, values: []string{v1.CapacityTypeSpot}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, kubeClient, cloud, claim := allocationFixture(t)
			claim.Spec.Requirements = []v1.NodeSelectorRequirementWithMinValues{{Key: test.key, Operator: test.operator, Values: test.values}}
			require.NoError(t, kubeClient.Update(context.Background(), claim))
			_, err := provider.Create(context.Background(), claim, nil)
			require.Error(t, err)
			require.Zero(t, cloud.createCalls)
			fresh := &v1.NodeClaim{}
			require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
			require.NotContains(t, fresh.Finalizers, AllocationFinalizer)
		})
	}
}

func TestAllocationRejectsForgedFlavorLabelsAndClassConstraints(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*v1alpha1.IBMNodeClass)
	}{
		{name: "forged architecture", edit: func(class *v1alpha1.IBMNodeClass) {
			class.Spec.IKSDynamicPools.Labels = map[string]string{corev1.LabelArchStable: "arm64"}
		}},
		{name: "minimum CPU", edit: func(class *v1alpha1.IBMNodeClass) {
			class.Spec.InstanceRequirements = &v1alpha1.InstanceTypeRequirements{MinimumCPU: 8}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, kubeClient, cloud, claim := allocationFixture(t)
			class := &v1alpha1.IBMNodeClass{}
			require.NoError(t, kubeClient.Get(context.Background(), types.NamespacedName{Name: "class"}, class))
			test.edit(class)
			require.NoError(t, kubeClient.Update(context.Background(), class))
			_, err := provider.Create(context.Background(), claim, nil)
			require.Error(t, err)
			require.Zero(t, cloud.createCalls)
		})
	}
}

func TestAllocationValidatesRegisteredAllocatableResources(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	claim.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3900m")}
	require.NoError(t, kubeClient.Update(context.Background(), claim))
	registerAllocatedNode(t, kubeClient)
	_, err := provider.Create(context.Background(), claim, nil)
	require.ErrorContains(t, err, "registered IKS worker resources")
	require.Equal(t, 1, cloud.createCalls)
}

func TestAllocationDoesNotRepeatAmbiguousCreateOrReleaseReservation(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	cloud.createError = fmt.Errorf("response lost")
	_, err := provider.Create(context.Background(), claim, nil)
	require.Error(t, err)
	invisiblePool := cloud.pool
	cloud.pool = nil
	cloud.createError = nil
	for range 3 {
		_, err = provider.Create(context.Background(), claim, nil)
		require.Error(t, err)
		require.Equal(t, 1, cloud.createCalls)
	}
	fresh := &v1.NodeClaim{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	for range 3 {
		err = provider.Cleanup(context.Background(), fresh)
		require.ErrorContains(t, err, "uncertain")
		require.False(t, cloudprovider.IsNodeClaimNotFoundError(err))
	}
	allocation, err := DecodeAllocation(fresh.Annotations)
	require.NoError(t, err)
	reservation := &corev1.ConfigMap{}
	require.NoError(t, kubeClient.Get(context.Background(), ReservationKey("cluster", allocation.PoolName, "karpenter"), reservation))
	cloud.pool = invisiblePool
	require.NoError(t, provider.Cleanup(context.Background(), fresh))
	require.Equal(t, []string{"real-pool"}, cloud.deleteCalls)
}

func TestAllocationQuarantinesAccountDriftAndWrongSubnet(t *testing.T) {
	t.Run("account drift", func(t *testing.T) {
		provider, kubeClient, cloud, claim := allocationFixture(t)
		_, err := provider.Create(context.Background(), claim, nil)
		require.NoError(t, err)
		fresh := &v1.NodeClaim{}
		require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
		cloud.account = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		t.Setenv("IBM_ACCOUNT_ID", "ffffffffffffffffffffffffffffffff")
		require.ErrorContains(t, provider.Cleanup(context.Background(), fresh), "immutable allocation")
		require.Empty(t, cloud.deleteCalls)
		_, err = provider.Get(context.Background(), "ibm://"+testAccount+"///cluster/real-worker")
		require.Error(t, err)
		require.False(t, cloudprovider.IsNodeClaimNotFoundError(err))
	})
	t.Run("wrong subnet", func(t *testing.T) {
		provider, kubeClient, cloud, claim := allocationFixture(t)
		_, err := provider.Create(context.Background(), claim, nil)
		require.NoError(t, err)
		registerAllocatedNode(t, kubeClient)
		cloud.worker.NetworkInterfaces[0].SubnetID = "different-subnet"
		_, err = provider.Create(context.Background(), claim, nil)
		require.ErrorContains(t, err, "worker subnet differs")
		require.Equal(t, 1, cloud.createCalls)
	})
}

func TestPendingCleanupEstablishesDrainOwnership(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	_, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	node := registerAllocatedNode(t, kubeClient)
	fresh := &v1.NodeClaim{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	require.NoError(t, provider.Cleanup(context.Background(), fresh))
	require.Empty(t, cloud.deleteCalls)
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(node), node))
	require.False(t, node.DeletionTimestamp.IsZero())
	require.Contains(t, node.Finalizers, v1.TerminationFinalizer)
	require.Equal(t, claim.UID, node.OwnerReferences[0].UID)
}

func TestBoundAllocationCleanupOnlyConfirmsCoreDeletion(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	_, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	fresh := &v1.NodeClaim{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	fresh.Status.ProviderID = "ibm://" + testAccount + "///cluster/real-worker"
	require.NoError(t, provider.Cleanup(context.Background(), fresh))
	require.Empty(t, cloud.deleteCalls)
	cloud.pool = nil
	require.NoError(t, provider.ConfirmGone(context.Background(), fresh))
	require.Empty(t, cloud.deleteCalls)
	cloud.worker = nil
	require.True(t, cloudprovider.IsNodeClaimNotFoundError(provider.ConfirmGone(context.Background(), fresh)))
}

func TestCoreDeletionRejectsProviderIDMismatch(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	registerAllocatedNode(t, kubeClient)
	node, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	node.UID = claim.UID
	node.Spec.ProviderID = "ibm://" + testAccount + "///cluster/foreign-worker"
	require.ErrorContains(t, provider.Delete(context.Background(), node), "identity differs")
	require.Empty(t, cloud.deleteCalls)
}

func TestAllocationRequiresKnownOfferingPriceForBudgetConstraint(t *testing.T) {
	for _, test := range []struct {
		name    string
		price   float64
		creates int
		unknown bool
	}{
		{name: "unknown price cannot bypass budget", price: 0, unknown: true},
		{name: "known price above budget", price: 0.20},
		{name: "known price within budget", price: 0.08, creates: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, kubeClient, cloud, claim := allocationFixture(t)
			provider.instanceTypeProvider = allocationCatalog{price: test.price}
			class := &v1alpha1.IBMNodeClass{}
			require.NoError(t, kubeClient.Get(context.Background(), types.NamespacedName{Name: "class"}, class))
			class.Spec.InstanceRequirements = &v1alpha1.InstanceTypeRequirements{MaximumHourlyPrice: "0.10"}
			require.NoError(t, kubeClient.Update(context.Background(), class))
			_, err := provider.Create(context.Background(), claim, nil)
			require.Equal(t, test.creates == 0, err != nil, "%v", err)
			if test.unknown {
				require.ErrorContains(t, err, "offering price is unavailable")
			}
			require.Equal(t, test.creates, cloud.createCalls)
			fresh := &v1.NodeClaim{}
			require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
			if test.creates == 0 {
				require.NotContains(t, fresh.Finalizers, AllocationFinalizer)
				require.Empty(t, fresh.Annotations[AllocationAnnotation])
			}
		})
	}
}

func TestIsNotFoundUsesHTTPStatus(t *testing.T) {
	require.True(t, IsNotFound(&httpclient.IBMCloudError{StatusCode: 404}))
	require.False(t, IsNotFound(&httpclient.IBMCloudError{StatusCode: 503, Message: "backend worker not found"}))
	require.False(t, IsNotFound(fmt.Errorf("get pool: %w", &httpclient.IBMCloudError{StatusCode: 500, Description: "not found"})))
}

func TestCoreDeletionRejectsForeignClaimUID(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	registerAllocatedNode(t, kubeClient)
	node, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	node.UID = "replacement-claim"
	require.ErrorContains(t, provider.Delete(context.Background(), node), "owner identity mismatch")
	require.Empty(t, cloud.deleteCalls)
}

func TestRejectedPoolCreateIsRetriedAndReleasable(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	cloud.createError = &httpclient.IBMCloudError{StatusCode: 400, Message: "invalid flavor"}
	_, err := provider.Create(context.Background(), claim, nil)
	require.Error(t, err)
	cloud.pool, cloud.worker = nil, nil
	_, err = provider.Create(context.Background(), claim, nil)
	require.Error(t, err)
	require.Equal(t, 2, cloud.createCalls)
	cloud.pool, cloud.worker = nil, nil
	fresh := &v1.NodeClaim{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	err = provider.Cleanup(context.Background(), fresh)
	require.True(t, cloudprovider.IsNodeClaimNotFoundError(err), "%v", err)
}

func TestUncertainPoolCreateExpiresAfterWindow(t *testing.T) {
	provider, kubeClient, cloud, claim := allocationFixture(t)
	cloud.createError = fmt.Errorf("response lost")
	_, err := provider.Create(context.Background(), claim, nil)
	require.Error(t, err)
	cloud.pool, cloud.worker, cloud.createError = nil, nil, nil
	fresh := &v1.NodeClaim{}
	require.NoError(t, kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	allocation, err := DecodeAllocation(fresh.Annotations)
	require.NoError(t, err)
	reservation := &corev1.ConfigMap{}
	require.NoError(t, kubeClient.Get(context.Background(), ReservationKey(allocation.ClusterID, allocation.PoolName, allocation.Namespace), reservation))
	reservation.Data[ReservationCreationStartedKey] = time.Now().Add(-2 * poolCreationWindow).UTC().Format(time.RFC3339)
	require.NoError(t, kubeClient.Update(context.Background(), reservation))
	_, err = provider.Create(context.Background(), fresh, nil)
	require.NoError(t, err)
	require.Equal(t, 2, cloud.createCalls)
}

func TestMalformedForeignAllocationDoesNotBlockLookup(t *testing.T) {
	provider, kubeClient, _, claim := allocationFixture(t)
	registerAllocatedNode(t, kubeClient)
	node, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	foreign := &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "foreign", Annotations: map[string]string{AllocationAnnotation: "{not json"}}}
	require.NoError(t, kubeClient.Create(context.Background(), foreign))
	found, err := provider.Get(context.Background(), node.Spec.ProviderID)
	require.NoError(t, err)
	require.Equal(t, node.Spec.ProviderID, found.Spec.ProviderID)
}
