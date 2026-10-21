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

package loadbalancer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	lb "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/loadbalancer"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"testing"
	"time"
)

type checkpointProvider struct {
	snapshot *lb.Snapshot
	err      error
}

func (p *checkpointProvider) VerifyAccount(context.Context, string) error { return nil }
func (p *checkpointProvider) ResolveTargets(context.Context, *v1alpha1.IBMNodeClass, string) ([]lb.ResolvedTarget, error) {
	return nil, errors.New("must use saved targets")
}
func (p *checkpointProvider) RegisterTargets(context.Context, *lb.Snapshot) error {
	return errors.New("must not register a deleting claim")
}
func (p *checkpointProvider) DeregisterTargets(_ context.Context, s *lb.Snapshot) error {
	p.snapshot = s
	return p.err
}

func TestDeletionUsesSavedTargetsAfterClassRemovalAndRetainsErrors(t *testing.T) {
	for _, fails := range []bool{false, true} {
		scheme := runtime.NewScheme()
		require.NoError(t, corev1.AddToScheme(scheme))
		scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
		require.NoError(t, v1alpha1.AddToScheme(scheme))
		proof := &lb.Snapshot{Version: 1, MinimumWriterVersion: 1, ClaimUID: "claim-uid", ClassUID: "class-uid", ClusterUID: "cluster-uid", AccountID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Region: "us-south", ProviderID: "ibm:///us-south/instance", InstanceID: "instance", AutoDeregister: true, Targets: []lb.ResolvedTarget{{PoolID: "original-pool", Target: v1alpha1.LoadBalancerTarget{LoadBalancerID: "original-lb", PoolName: "original-name", Port: 80}}}}
		encoded, err := json.Marshal(proof)
		require.NoError(t, err)
		launch, err := json.Marshal(map[string]interface{}{"Name": ownership.InstanceName("cluster-uid", "claim-uid"), "ClaimUID": "claim-uid", "ClassUID": "class-uid", "ClusterUID": "cluster-uid", "AccountID": proof.AccountID, "Region": "us-south"})
		require.NoError(t, err)
		claim := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", UID: "claim-uid", Finalizers: []string{LoadBalancerFinalizer, karpv1.TerminationFinalizer}, DeletionTimestamp: &metav1.Time{Time: time.Now()}, Annotations: map[string]string{lb.SnapshotAnnotation: string(encoded), "karpenter-ibm.sh/vpc-launch": string(launch)}}, Spec: karpv1.NodeClaimSpec{NodeClassRef: &karpv1.NodeClassReference{Group: v1alpha1.Group, Kind: "IBMNodeClass", Name: "removed-class"}}, Status: karpv1.NodeClaimStatus{ProviderID: proof.ProviderID}}
		kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claim, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}}).Build()
		provider := &checkpointProvider{}
		if fails {
			provider.err = errors.New("cloud unavailable")
		}
		c := NewController(kube, nil, kube)
		c.loadBalancerProvider = provider
		_, err = c.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(claim)})
		if fails {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
		require.Equal(t, "original-pool", provider.snapshot.Targets[0].PoolID)
		current := &karpv1.NodeClaim{}
		require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(claim), current))
		if fails {
			require.Contains(t, current.Finalizers, LoadBalancerFinalizer)
		} else {
			require.NotContains(t, current.Finalizers, LoadBalancerFinalizer)
		}
	}
}

func TestDeletionWithoutSnapshotReleasesOnlyUnregisteredClaims(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(map[bool]string{false: "never registered", true: "registered"}[registered], func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
			require.NoError(t, v1alpha1.AddToScheme(scheme))
			claim := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", UID: "claim-uid", Finalizers: []string{LoadBalancerFinalizer, karpv1.TerminationFinalizer}, DeletionTimestamp: &metav1.Time{Time: time.Now()}}, Spec: karpv1.NodeClaimSpec{NodeClassRef: &karpv1.NodeClassReference{Group: v1alpha1.Group, Kind: "IBMNodeClass", Name: "class"}}, Status: karpv1.NodeClaimStatus{ProviderID: "ibm:///us-south/instance"}}
			if registered {
				claim.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claim).WithStatusSubresource(claim).Build()
			c := NewController(kube, nil, kube)
			c.loadBalancerProvider = &checkpointProvider{}
			_, err := c.Reconcile(context.Background(), reconcile.Request{NamespacedName: client.ObjectKeyFromObject(claim)})
			current := &karpv1.NodeClaim{}
			require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(claim), current))
			if registered {
				require.Error(t, err)
				require.Contains(t, current.Finalizers, LoadBalancerFinalizer)
			} else {
				require.NoError(t, err)
				require.NotContains(t, current.Finalizers, LoadBalancerFinalizer)
			}
		})
	}
}
