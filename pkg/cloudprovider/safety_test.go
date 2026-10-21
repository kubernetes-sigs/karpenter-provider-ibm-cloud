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

package cloudprovider

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcloud "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/controllers/nodeclaim/lifecycle"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

type accountTargetProvider struct {
	commonTypes.VPCInstanceProvider
	node    *corev1.Node
	err     error
	account string
}

func (p *accountTargetProvider) GetFresh(context.Context, string) (*corev1.Node, error) {
	return p.node, p.err
}

func (p *accountTargetProvider) ValidateLaunchTarget(_ context.Context, claim *karpv1.NodeClaim) error {
	if claim.Annotations[ownership.AccountIDAnnotation] != p.account {
		return fmt.Errorf("credential account differs from birth account")
	}
	return nil
}

func TestLegacyVPCAccountMigrationRequiresLiveInstanceAndSameClaim(t *testing.T) {
	for _, test := range []struct {
		name         string
		cloudMissing bool
		replaced     bool
	}{
		{name: "live instance"},
		{name: "cloud absence", cloudMissing: true},
		{name: "reused claim name", replaced: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			claim := getTestNodeClaim("class")
			claim.UID = types.UID("original-claim")
			claim.Status.ProviderID = "ibm:///us-south/instance"
			persisted := claim.DeepCopy()
			if test.replaced {
				persisted.UID = types.UID("replacement-claim")
			}
			kube := fake.NewClientBuilder().WithScheme(getTestScheme()).WithObjects(persisted).Build()
			provider := &accountTargetProvider{account: "birth-account", node: &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ownership.AccountIDAnnotation: "birth-account"}},
				Spec:       corev1.NodeSpec{ProviderID: claim.Status.ProviderID},
			}}
			if test.cloudMissing {
				provider.err = karpcloud.NewNodeClaimNotFoundError(fmt.Errorf("instance absent"))
			}
			cloud := &CloudProvider{kubeClient: kube, apiReader: kube}
			err := cloud.ensureBirthTarget(context.Background(), provider, claim)
			updated := persisted.DeepCopy()
			require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(persisted), updated))
			if test.cloudMissing || test.replaced {
				require.Error(t, err)
				require.False(t, karpcloud.IsNodeClaimNotFoundError(err))
				require.Empty(t, updated.Annotations[ownership.AccountIDAnnotation])
			} else {
				require.NoError(t, err)
				require.Equal(t, provider.account, updated.Annotations[ownership.AccountIDAnnotation])
			}
		})
	}
}

func TestVPCBirthTargetUsesCredentialIdentityWithoutAccountEnvironment(t *testing.T) {
	t.Setenv("IBM_ACCOUNT_ID", "")
	claim := getTestNodeClaim("class")
	claim.Status.ProviderID = "ibm:///us-south/instance"
	claim.Annotations = map[string]string{ownership.AccountIDAnnotation: "birth-account"}
	provider := &accountTargetProvider{account: "birth-account"}
	require.NoError(t, validateBirthTarget(context.Background(), provider, claim))
	provider.account = "other-account"
	require.Error(t, validateBirthTarget(context.Background(), provider, claim))
}

func TestStaleNodeClassGenerationRetriesWithoutDeletingClaim(t *testing.T) {
	class := getTestNodeClass()
	class.Generation = 2
	class.Status.Conditions[0].ObservedGeneration = 1
	class.Status.Conditions[0].Status = metav1.ConditionFalse
	kube := fake.NewClientBuilder().WithScheme(getTestScheme()).WithObjects(class).Build()
	provider := &CloudProvider{kubeClient: kube}
	_, err := provider.Create(context.Background(), getTestNodeClaim(class.Name))
	require.Error(t, err)
	require.False(t, karpcloud.IsNodeClassNotReadyError(err))
}

func TestBackendRoutingIgnoresCurrentEnvironment(t *testing.T) {
	t.Setenv("IKS_CLUSTER_ID", "changed-cluster")
	class, mode, err := classForProviderID("ibm:///us-south/instance")
	require.NoError(t, err)
	require.Equal(t, commonTypes.VPCMode, mode)
	require.Equal(t, "us-south", class.Spec.Region)
	class, mode, err = classForProviderID("ibm://account///birth-cluster/worker")
	require.NoError(t, err)
	require.Equal(t, commonTypes.IKSMode, mode)
	require.Equal(t, "birth-cluster", class.Spec.IKSClusterID)
	_, _, err = classForProviderID("ibm://unproven-placeholder")
	require.Error(t, err)
}

func TestRecoveredLaunchKeepsActualResourcesAndBirthIdentity(t *testing.T) {
	claim := getTestNodeClaim("class")
	claim.Annotations = map[string]string{ownership.BackendAnnotation: "iks"}
	actual := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("8Gi")}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{ownership.BackendAnnotation: "vpc"}}, Spec: corev1.NodeSpec{ProviderID: "ibm:///us-south/instance"}, Status: corev1.NodeStatus{Capacity: actual, Allocatable: actual}}
	provider := &CloudProvider{}
	recovered, err := provider.nodeClaimForNode(claim, node, nil, nil)
	require.NoError(t, err)
	populated := lifecycle.PopulateNodeClaimDetails(claim, recovered)
	require.Equal(t, actual, populated.Status.Capacity)
	require.Equal(t, actual, populated.Status.Allocatable)
	require.Equal(t, "vpc", populated.Annotations[ownership.BackendAnnotation])
	require.Equal(t, node.Spec.ProviderID, populated.Status.ProviderID)
}

type listProvider struct {
	commonTypes.VPCInstanceProvider
	present map[string]bool
}

func (p *listProvider) GetFresh(_ context.Context, id string) (*corev1.Node, error) {
	if !p.present[id] {
		return nil, karpcloud.NewNodeClaimNotFoundError(fmt.Errorf("instance %s absent", id))
	}
	return &corev1.Node{Spec: corev1.NodeSpec{ProviderID: id}}, nil
}

func TestListOmitsClaimlessAbsentNodeInsteadOfFailing(t *testing.T) {
	live, orphan := "ibm:///us-south/live", "ibm:///us-south/orphan"
	kube := fake.NewClientBuilder().WithScheme(getTestScheme()).WithObjects(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "live"}, Spec: corev1.NodeSpec{ProviderID: live}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "orphan"}, Spec: corev1.NodeSpec{ProviderID: orphan}},
	).Build()
	factory := providers.NewProviderFactory(context.Background(), nil, kube, nil, nil, providers.WithVPCInstanceProvider(&listProvider{present: map[string]bool{live: true}}))
	cloud := &CloudProvider{kubeClient: kube, apiReader: kube, providerFactory: factory}
	claims, err := cloud.List(context.Background())
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, live, claims[0].Status.ProviderID)
}

func (p *listProvider) ListFresh(ctx context.Context, _ []*karpv1.NodeClaim, nodes []*corev1.Node) ([]*corev1.Node, error) {
	var live []*corev1.Node
	for _, node := range nodes {
		found, err := p.GetFresh(ctx, node.Spec.ProviderID)
		if karpcloud.IsNodeClaimNotFoundError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		live = append(live, found)
	}
	return live, nil
}
