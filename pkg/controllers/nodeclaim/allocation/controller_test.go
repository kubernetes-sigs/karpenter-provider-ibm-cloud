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

package allocation

import (
	"context"
	"fmt"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/instance"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"testing"
)

type fakePending struct {
	getError     error
	cleanupCalls int
	complete     bool
}

func (p *fakePending) CleanupPending(context.Context, *karpv1.NodeClaim) (bool, error) {
	p.cleanupCalls++
	return p.complete, nil
}
func (p *fakePending) GetFresh(context.Context, string) (*corev1.Node, error) {
	return &corev1.Node{}, p.getError
}

func (p *fakePending) ValidateLaunchTarget(context.Context, *karpv1.NodeClaim) error { return nil }

func TestAllocatedClaimWaitsForCoreDeletion(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		err     error
		removed bool
	}{
		{name: "still exists"},
		{name: "read uncertain", err: fmt.Errorf("cloud timeout")},
		{name: "confirmed absent", err: cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("404")), removed: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
			claim := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time}, Finalizers: []string{karpv1.TerminationFinalizer, instance.LaunchFinalizer}}, Status: karpv1.NodeClaimStatus{ProviderID: "ibm:///us-south/instance"}}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claim).Build()
			provider := &fakePending{getError: scenario.err}
			controller := &Controller{kubeClient: kube, apiReader: kube, provider: provider}
			_, err := controller.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(claim)})
			if scenario.err != nil && !scenario.removed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Zero(t, provider.cleanupCalls)
			current := &karpv1.NodeClaim{}
			require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(claim), current))
			require.Contains(t, current.Finalizers, karpv1.TerminationFinalizer)
			if scenario.removed {
				require.NotContains(t, current.Finalizers, instance.LaunchFinalizer)
			} else {
				require.Contains(t, current.Finalizers, instance.LaunchFinalizer)
			}
		})
	}
}
