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

package ownership

import (
	"context"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"testing"
	"time"
)

func TestDeletingLegacyDrainHonorsEvictionAndVolumeDetachment(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, policyv1.AddToScheme(scheme))
	require.NoError(t, storagev1.AddToScheme(scheme))
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid", Finalizers: []string{"old.example/termination"}, DeletionTimestamp: &metav1.Time{Time: time.Now().Add(-time.Minute)}}, Spec: corev1.NodeSpec{ProviderID: "ibm:///us-south/instance"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "default", UID: "pod-uid"}, Spec: corev1.PodSpec{NodeName: node.Name}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	attachment := &storagev1.VolumeAttachment{ObjectMeta: metav1.ObjectMeta{Name: "attachment"}, Spec: storagev1.VolumeAttachmentSpec{NodeName: node.Name}}
	evictions := 0
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, pod, attachment).WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string { return []string{obj.(*corev1.Pod).Spec.NodeName} }).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, kube client.WithWatch, obj client.Object, patch client.Patch, options ...client.PatchOption) error {
			if current, ok := obj.(*corev1.Node); ok {
				before := &corev1.Node{}
				require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(current), before))
				require.Empty(t, validation.ValidateObjectMetaUpdate(&current.ObjectMeta, &before.ObjectMeta, field.NewPath("metadata")))
				require.NotContains(t, current.Finalizers, karpv1.TerminationFinalizer)
			}
			return kube.Patch(ctx, obj, patch, options...)
		},
		SubResourceCreate: func(_ context.Context, _ client.Client, sub string, _ client.Object, body client.Object, _ ...client.SubResourceCreateOption) error {
			require.Equal(t, "eviction", sub)
			eviction := body.(*policyv1.Eviction)
			require.Equal(t, pod.UID, *eviction.DeleteOptions.Preconditions.UID)
			evictions++
			return apierrors.NewTooManyRequests("PDB denies eviction", 0)
		},
	}).Build()
	done, err := DrainLegacyNode(context.Background(), kube, kube, node)
	require.NoError(t, err)
	require.False(t, done)
	require.Equal(t, 1, evictions)
	require.NoError(t, kube.Delete(context.Background(), pod))
	done, err = DrainLegacyNode(context.Background(), kube, kube, node)
	require.NoError(t, err)
	require.False(t, done)
	require.NoError(t, kube.Delete(context.Background(), attachment))
	done, err = DrainLegacyNode(context.Background(), kube, kube, node)
	require.NoError(t, err)
	require.True(t, done)
}

func TestStateVersionGate(t *testing.T) {
	for _, v := range [][2]int{{0, 0}, {1, 0}, {1, 1}} {
		require.NoError(t, ValidateStateVersion(v[0], v[1]))
	}
	for _, v := range [][2]int{{2, 1}, {1, 2}, {0, 1}, {-1, 0}, {1, -1}} {
		require.Error(t, ValidateStateVersion(v[0], v[1]))
	}
}
