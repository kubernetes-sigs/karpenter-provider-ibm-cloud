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
package interruption

import (
	"context"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"testing"
)

func TestIKSInterruptionEpisodesAndCordonOwnership(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		scheme := runtime.NewScheme()
		require.NoError(t, corev1.AddToScheme(scheme))
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid", Labels: map[string]string{karpv1.NodePoolLabelKey: "pool"}, Annotations: map[string]string{"ibm-cloud.kubernetes.io/status": "capacity unavailable"}}, Spec: corev1.NodeSpec{ProviderID: "ibm://account///cluster/worker", Unschedulable: preexisting}}
		kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
		c := NewController(kube, nil, nil, kube)
		_, err := c.Reconcile(context.Background())
		require.NoError(t, err)
		current := &corev1.Node{}
		require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(node), current))
		require.True(t, current.Spec.Unschedulable)
		interrupted, _ := c.isNodeInterrupted(context.Background(), current)
		require.False(t, interrupted)
		current.Annotations["ibm-cloud.kubernetes.io/status"] = "network resources unavailable"
		require.NoError(t, kube.Update(context.Background(), current))
		interrupted, reason := c.isNodeInterrupted(context.Background(), current)
		require.True(t, interrupted)
		require.Equal(t, NetworkResourceLimit, reason)
		_, err = c.Reconcile(context.Background())
		require.NoError(t, err)
		require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(node), current))
		delete(current.Annotations, "ibm-cloud.kubernetes.io/status")
		require.NoError(t, kube.Update(context.Background(), current))
		_, err = c.Reconcile(context.Background())
		require.NoError(t, err)
		require.NoError(t, kube.Get(context.Background(), client.ObjectKeyFromObject(node), current))
		require.Equal(t, preexisting, current.Spec.Unschedulable)
		require.NotContains(t, current.Annotations, InterruptionCompletedAnnotation)
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations["ibm-cloud.kubernetes.io/maintenance"] = "true"
		require.NoError(t, kube.Update(context.Background(), current))
		_, err = c.Reconcile(context.Background())
		require.NoError(t, err)
		require.Error(t, kube.Get(context.Background(), client.ObjectKeyFromObject(node), &corev1.Node{}))
	}
}
