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

package startuptaint

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestStartupTaintLifecycle_NoStartupTaints(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Register Karpenter v1 types manually
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	scheme.AddKnownTypes(gv,
		&karpv1.NodeClaim{},
		&karpv1.NodeClaimList{},
	)

	// NodeClaim with only regular taints (no startup taints)
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclaim",
		},
		Spec: karpv1.NodeClaimSpec{
			Taints: []corev1.Taint{
				{
					Key:    "example.com/special-taint",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			// No StartupTaints defined
		},
		Status: karpv1.NodeClaimStatus{
			NodeName: "test-node",
		},
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{},
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim, node).
		Build()

	controller := NewController(kubeClient)

	// Process the lifecycle
	result, err := controller.processStartupTaintLifecycle(ctx, nodeClaim, node)
	assert.NoError(t, err)

	// Should complete immediately since no startup taints
	assert.Equal(t, time.Duration(0), result.RequeueAfter)
	assert.Equal(t, int64(0), result.RequeueAfter.Nanoseconds())

	// Check that regular taint was applied to node
	updatedNode := &corev1.Node{}
	err = kubeClient.Get(ctx, client.ObjectKey{Name: "test-node"}, updatedNode)
	assert.NoError(t, err)
	assert.Len(t, updatedNode.Spec.Taints, 1)
	assert.Equal(t, "example.com/special-taint", updatedNode.Spec.Taints[0].Key)
}

func TestStartupTaintLifecycle_WithStartupTaints(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Register Karpenter v1 types manually
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	scheme.AddKnownTypes(gv,
		&karpv1.NodeClaim{},
		&karpv1.NodeClaimList{},
	)

	// NodeClaim with both startup and regular taints
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclaim",
		},
		Spec: karpv1.NodeClaimSpec{
			Taints: []corev1.Taint{
				{
					Key:    "example.com/special-taint",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			StartupTaints: []corev1.Taint{
				{
					Key:    "node.kubernetes.io/not-ready",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
		Status: karpv1.NodeClaimStatus{
			NodeName: "test-node",
		},
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{},
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim, node).
		Build()

	controller := NewController(kubeClient)

	// Phase 1: Should apply startup taints only
	result, err := controller.processStartupTaintLifecycle(ctx, nodeClaim, node)
	assert.NoError(t, err)

	// Should requeue for next phase
	assert.True(t, result.RequeueAfter > 0)

	// Check that startup taint was applied but not regular taint
	updatedNode := &corev1.Node{}
	err = kubeClient.Get(ctx, client.ObjectKey{Name: "test-node"}, updatedNode)
	assert.NoError(t, err)
	assert.Len(t, updatedNode.Spec.Taints, 1)
	assert.Equal(t, "node.kubernetes.io/not-ready", updatedNode.Spec.Taints[0].Key)
}

func TestStartupTaintLifecycle_StartupTaintsRemoved(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	// Register Karpenter v1 types manually
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	scheme.AddKnownTypes(gv,
		&karpv1.NodeClaim{},
		&karpv1.NodeClaimList{},
	)

	// NodeClaim marked as having startup taints applied
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclaim",
			Labels: map[string]string{
				StartupTaintsAppliedLabel: "true",
			},
		},
		Spec: karpv1.NodeClaimSpec{
			Taints: []corev1.Taint{
				{
					Key:    "example.com/special-taint",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			StartupTaints: []corev1.Taint{
				{
					Key:    "node.kubernetes.io/not-ready",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
		Status: karpv1.NodeClaimStatus{
			NodeName: "test-node",
		},
	}

	// Node with startup taints removed (simulating system pods removed them)
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
		},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				// Only non-system taints remain
				{
					Key:    "some-other-taint",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClaim, node).
		Build()

	controller := NewController(kubeClient)

	// Should apply regular taints since startup taints are gone
	result, err := controller.processStartupTaintLifecycle(ctx, nodeClaim, node)
	assert.NoError(t, err)

	// Should complete the lifecycle
	assert.Equal(t, time.Duration(0), result.RequeueAfter)
	assert.Equal(t, int64(0), result.RequeueAfter.Nanoseconds())

	// Check that regular taint was applied
	updatedNode := &corev1.Node{}
	err = kubeClient.Get(ctx, client.ObjectKey{Name: "test-node"}, updatedNode)
	assert.NoError(t, err)

	// Should have the original taint plus the new regular taint
	assert.Len(t, updatedNode.Spec.Taints, 2)

	foundSpecialTaint := false
	foundOtherTaint := false
	for _, taint := range updatedNode.Spec.Taints {
		if taint.Key == "example.com/special-taint" {
			foundSpecialTaint = true
		}
		if taint.Key == "some-other-taint" {
			foundOtherTaint = true
		}
	}
	assert.True(t, foundSpecialTaint, "Regular taint should be applied")
	assert.True(t, foundOtherTaint, "Existing taint should remain")
}

func TestIsSystemStartupTaint(t *testing.T) {
	testCases := []struct {
		taintKey string
		expected bool
	}{
		{"node.cilium.io/agent-not-ready", true},
		{"node.kubernetes.io/not-ready", true},
		{"node.kubernetes.io/unreachable", true},
		{"node.kubernetes.io/disk-pressure", true},
		{"node.kubernetes.io/memory-pressure", true},
		{"node.kubernetes.io/pid-pressure", true},
		{"example.com/custom-taint", false},
		{"app-specific/taint", false},
	}

	for _, tc := range testCases {
		t.Run(tc.taintKey, func(t *testing.T) {
			result := isSystemStartupTaint(tc.taintKey)
			assert.Equal(t, tc.expected, result, "Taint key: %s", tc.taintKey)
		})
	}
}
