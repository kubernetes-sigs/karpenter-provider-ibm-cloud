//go:build e2e

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

package e2e

import (
	"context"
	"testing"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	kptr "k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestUnregisteredTaintLifecycleRequiresObservedOwnedIdentity(t *testing.T) {
	pool := &karpv1.NodePool{ObjectMeta: metav1.ObjectMeta{Name: "fixture-pool", UID: "pool-uid"}}
	claim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim", UID: "claim-uid", Labels: map[string]string{
			"test": "fixture", karpv1.NodePoolLabelKey: pool.Name,
		}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodePool", Name: pool.Name, UID: pool.UID}}},
		Status: karpv1.NodeClaimStatus{ProviderID: "ibm:///us-south/instance"},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: claim.Name, UID: "node-uid", Labels: claim.Labels},
		Spec: corev1.NodeSpec{ProviderID: claim.Status.ProviderID, Taints: []corev1.Taint{karpv1.UnregisteredNoExecuteTaint}}}
	for _, test := range []struct {
		name   string
		change func(*corev1.Node, *karpv1.NodeClaim)
		valid  bool
	}{
		{"observed initial taint", func(*corev1.Node, *karpv1.NodeClaim) {}, true},
		{"never applied", func(n *corev1.Node, _ *karpv1.NodeClaim) { n.Spec.Taints = nil }, false},
		{"wrong effect", func(n *corev1.Node, _ *karpv1.NodeClaim) { n.Spec.Taints[0].Effect = corev1.TaintEffectNoSchedule }, false},
		{"foreign pool", func(_ *corev1.Node, c *karpv1.NodeClaim) { c.OwnerReferences[0].UID = "foreign" }, false},
		{"wrong provider", func(n *corev1.Node, _ *karpv1.NodeClaim) { n.Spec.ProviderID = "ibm:///us-south/foreign" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			observedNode, observedClaim := node.DeepCopy(), claim.DeepCopy()
			test.change(observedNode, observedClaim)
			kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(observedClaim).Build()
			suite := &E2ETestSuite{kubeClient: kube}
			events := watch.NewRaceFreeFake()
			events.Add(observedNode)
			events.Stop()
			initialNode, initialClaim, err := suite.waitForUnregisteredTestNode(t.Context(), events, pool, "fixture")
			if test.valid {
				require.NoError(t, err)
				require.Equal(t, node.UID, initialNode.UID)
				require.Equal(t, claim.UID, initialClaim.UID)
			} else {
				require.Error(t, err)
			}
		})
	}
	registered := node.DeepCopy()
	registered.Spec.Taints = nil
	registered.OwnerReferences = []metav1.OwnerReference{{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: claim.Name, UID: claim.UID}}
	registeredClaim := claim.DeepCopy()
	registeredClaim.Status.NodeName = node.Name
	registeredClaim.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)
	for _, test := range []struct {
		name   string
		change func(*corev1.Node, *karpv1.NodeClaim)
		valid  bool
	}{
		{"same identities removed", func(*corev1.Node, *karpv1.NodeClaim) {}, true},
		{"recreated Node", func(n *corev1.Node, _ *karpv1.NodeClaim) { n.UID = "replacement" }, false},
		{"recreated claim", func(_ *corev1.Node, c *karpv1.NodeClaim) { c.UID = "replacement" }, false},
		{"foreign owner", func(n *corev1.Node, _ *karpv1.NodeClaim) { n.OwnerReferences[0].UID = "foreign" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			currentNode, currentClaim := registered.DeepCopy(), registeredClaim.DeepCopy()
			test.change(currentNode, currentClaim)
			complete, err := registeredTestNodeMatches(node, claim, currentNode, currentClaim)
			if test.valid {
				require.NoError(t, err)
				require.True(t, complete)
			} else {
				require.Error(t, err)
				require.False(t, complete)
			}
		})
	}
	registered.Spec.Taints = []corev1.Taint{karpv1.UnregisteredNoExecuteTaint}
	complete, err := registeredTestNodeMatches(node, claim, registered, registeredClaim)
	require.NoError(t, err)
	require.False(t, complete, "Registration without removal must keep waiting")
}

func TestStartupTaintRemovalPreservesConcurrentTaints(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	startup := corev1.Taint{Key: "example.com/startup", Value: "pending", Effect: corev1.TaintEffectNoSchedule}
	regular := corev1.Taint{Key: "dedicated", Value: "test", Effect: corev1.TaintEffectNoSchedule}
	concurrent := corev1.Taint{Key: "another-controller", Effect: corev1.TaintEffectNoExecute}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "node-uid", Labels: map[string]string{
		"test-name": "fixture", "created-by": "karpenter-e2e", "karpenter.sh/nodepool": "fixture-pool",
	}}, Spec: corev1.NodeSpec{ProviderID: "ibm:///us-south/instance", Taints: []corev1.Taint{startup, regular}}}
	expected := node.DeepCopy()
	patches := 0
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, underlying client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
			patches++
			if patches == 1 {
				fresh := &corev1.Node{}
				require.NoError(t, underlying.Get(ctx, client.ObjectKeyFromObject(node), fresh))
				fresh.Spec.Taints = append(fresh.Spec.Taints, concurrent)
				require.NoError(t, underlying.Update(ctx, fresh))
				return apierrors.NewConflict(schema.GroupResource{Resource: "nodes"}, node.Name, nil)
			}
			return underlying.Patch(ctx, object, patch, opts...)
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.NoError(t, suite.removeTestStartupTaint(context.Background(), expected, "fixture", startup))
	var current corev1.Node
	require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(node), &current))
	require.ElementsMatch(t, []corev1.Taint{regular, concurrent}, current.Spec.Taints)
	require.Equal(t, 2, patches)
	for _, change := range []string{"owner", "uid"} {
		t.Run(change, func(t *testing.T) {
			foreign := expected.DeepCopy()
			if change == "owner" {
				foreign.Labels["test-name"] = "foreign"
			} else {
				foreign.UID = "replacement"
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreign).Build()
			suite := &E2ETestSuite{kubeClient: kube}
			require.Error(t, suite.removeTestStartupTaint(context.Background(), expected, "fixture", startup))
			var unchanged corev1.Node
			require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(foreign), &unchanged))
			require.ElementsMatch(t, expected.Spec.Taints, unchanged.Spec.Taints)
		})
	}
}

func TestNodeAbsenceDoesNotHideForbidden(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	kube := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "node", nil)
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.True(t, apierrors.IsForbidden(suite.waitForNodeAbsent(context.Background(), "node")))
	suite.kubeClient = fake.NewClientBuilder().WithScheme(scheme).Build()
	require.NoError(t, suite.waitForNodeAbsent(context.Background(), "node"))
}

func TestSelectedImageVerificationRejectsWrongIdentity(t *testing.T) {
	selector := &v1alpha1.ImageSelector{OS: "ubuntu", MajorVersion: "24", MinorVersion: "04", Architecture: "amd64", Variant: "minimal"}
	for _, test := range []struct {
		name   string
		change func(*vpcv1.Image)
	}{
		{"valid", func(*vpcv1.Image) {}},
		{"short family", func(image *vpcv1.Image) { image.OperatingSystem.Family = kptr.To("ubuntu") }},
		{"wrong minor", func(image *vpcv1.Image) { image.Name = kptr.To("ibm-ubuntu-24-10-minimal-amd64-1") }},
		{"wrong OS", func(image *vpcv1.Image) { image.OperatingSystem.Family = kptr.To("debian") }},
		{"contradictory family", func(image *vpcv1.Image) { image.OperatingSystem.Family = kptr.To("Debian Linux") }},
		{"contradictory OS name", func(image *vpcv1.Image) { image.OperatingSystem.Name = kptr.To("debian-24-04-amd64") }},
		{"wrong OS name major", func(image *vpcv1.Image) { image.OperatingSystem.Name = kptr.To("ubuntu-22-04-amd64") }},
		{"wrong OS name minor", func(image *vpcv1.Image) { image.OperatingSystem.Name = kptr.To("ubuntu-24-10-amd64") }},
		{"wrong OS name boundary", func(image *vpcv1.Image) { image.OperatingSystem.Name = kptr.To("ubuntu-24-040-amd64") }},
		{"missing OS name", func(image *vpcv1.Image) { image.OperatingSystem.Name = nil }},
		{"wrong major metadata", func(image *vpcv1.Image) { image.OperatingSystem.Version = kptr.To("22.04") }},
		{"wrong architecture", func(image *vpcv1.Image) { image.OperatingSystem.Architecture = kptr.To("arm64") }},
		{"wrong variant", func(image *vpcv1.Image) { image.Name = kptr.To("ibm-ubuntu-24-04-server-amd64-1") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			image := &vpcv1.Image{ID: kptr.To("image"), Name: kptr.To("ibm-ubuntu-24-04-6-minimal-amd64-3"),
				OperatingSystem: &vpcv1.OperatingSystem{Family: kptr.To("Ubuntu Linux"), Name: kptr.To("ubuntu-24-04-amd64"),
					Version: kptr.To("24.04 LTS Noble Numbat Minimal Install"), Architecture: kptr.To("amd64")}}
			test.change(image)
			err := selectedTestImageMatches(image, selector)
			if test.name == "valid" || test.name == "short family" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
