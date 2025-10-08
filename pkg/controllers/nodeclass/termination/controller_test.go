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
package termination

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func getTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	_ = v1.AddToScheme(scheme)
	return scheme
}

func getTestNodeClass() *v1alpha1.IBMNodeClass {
	return &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region: "us-south",
			VPC:    "test-vpc",
			Image:  "test-image",
		},
	}
}

func getTestNodeClassWithDeletion() *v1alpha1.IBMNodeClass {
	now := metav1.Now()
	nc := getTestNodeClass()
	nc.DeletionTimestamp = &now
	nc.Finalizers = []string{"test-finalizer"} // Required for fake client
	return nc
}

func getTestNode(nodeClassName string) *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			Labels: map[string]string{
				"karpenter-ibm.sh/nodeclass": nodeClassName,
			},
		},
		Spec: v1.NodeSpec{
			ProviderID: "ibm://test-instance-id",
		},
	}
}

func TestNewController(t *testing.T) {
	tests := []struct {
		name        string
		kubeClient  client.Client
		recorder    record.EventRecorder
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid parameters",
			kubeClient:  fake.NewClientBuilder().Build(),
			recorder:    record.NewFakeRecorder(10),
			expectError: false,
		},
		{
			name:        "nil client",
			kubeClient:  nil,
			recorder:    record.NewFakeRecorder(10),
			expectError: true,
			errorMsg:    "kubeClient cannot be nil",
		},
		{
			name:        "nil recorder",
			kubeClient:  fake.NewClientBuilder().Build(),
			recorder:    nil,
			expectError: true,
			errorMsg:    "recorder cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller, err := NewController(tt.kubeClient, tt.recorder)
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, controller)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, controller)
			}
		})
	}
}

func TestController_Reconcile(t *testing.T) {
	tests := []struct {
		name         string
		nodeClass    *v1alpha1.IBMNodeClass
		nodes        []*v1.Node
		expectError  bool
		expectEvents int
	}{
		{
			name:         "nodeclass not found",
			nodeClass:    nil,
			expectError:  false,
			expectEvents: 0,
		},
		{
			name:         "nodeclass without deletion timestamp",
			nodeClass:    getTestNodeClass(),
			expectError:  false,
			expectEvents: 0,
		},
		{
			name:         "nodeclass with deletion timestamp, no nodes",
			nodeClass:    getTestNodeClassWithDeletion(),
			expectError:  false,
			expectEvents: 0,
		},
		{
			name:      "nodeclass with deletion timestamp and nodes",
			nodeClass: getTestNodeClassWithDeletion(),
			nodes: []*v1.Node{
				getTestNode("test-nodeclass"),
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "another-test-node",
						Labels: map[string]string{
							"karpenter-ibm.sh/nodeclass": "test-nodeclass",
						},
					},
				},
			},
			expectError:  false,
			expectEvents: 2, // 2 deletion events
		},
		{
			name:      "nodeclass with deletion timestamp, node with different nodeclass",
			nodeClass: getTestNodeClassWithDeletion(),
			nodes: []*v1.Node{
				getTestNode("different-nodeclass"), // Different nodeclass, should not be deleted
			},
			expectError:  false,
			expectEvents: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := getTestScheme()

			builder := fake.NewClientBuilder().WithScheme(scheme)

			if tt.nodeClass != nil {
				builder = builder.WithRuntimeObjects(tt.nodeClass)
			}

			for _, node := range tt.nodes {
				builder = builder.WithRuntimeObjects(node)
			}

			fakeClient := builder.Build()
			recorder := record.NewFakeRecorder(10)

			controller, err := NewController(fakeClient, recorder)
			require.NoError(t, err)

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "test-nodeclass",
				},
			}

			result, err := controller.Reconcile(ctx, req)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, reconcile.Result{}, result)

				// Check that events were recorded
				close(recorder.Events)
				events := []string{}
				for event := range recorder.Events {
					events = append(events, event)
				}
				assert.Len(t, events, tt.expectEvents)

				// If we expected nodes to be deleted, verify they're gone
				if tt.nodeClass != nil && tt.nodeClass.DeletionTimestamp != nil {
					for _, node := range tt.nodes {
						if node.Labels["karpenter-ibm.sh/nodeclass"] == tt.nodeClass.Name {
							var deletedNode v1.Node
							err := fakeClient.Get(ctx, types.NamespacedName{Name: node.Name}, &deletedNode)
							assert.Error(t, err, "Node should have been deleted")
						}
					}
				}
			}
		})
	}
}

func TestController_ReconcileWithEventRecording(t *testing.T) {
	ctx := context.Background()
	scheme := getTestScheme()

	nodeClass := getTestNodeClassWithDeletion()
	node := getTestNode("test-nodeclass")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(nodeClass, node).
		Build()

	recorder := record.NewFakeRecorder(10)

	controller, err := NewController(fakeClient, recorder)
	require.NoError(t, err)

	_, err = controller.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "test-nodeclass"},
	})
	assert.NoError(t, err)

	// Check that correct event was recorded
	close(recorder.Events)
	events := []string{}
	for event := range recorder.Events {
		events = append(events, event)
	}

	require.Len(t, events, 1)
	assert.Contains(t, events[0], "DeletedNode")
	assert.Contains(t, events[0], "test-node")
}

func TestController_ReconcileEdgeCases(t *testing.T) {
	ctx := context.Background()
	scheme := getTestScheme()

	t.Run("multiple reconcile calls are idempotent", func(t *testing.T) {
		nodeClass := getTestNodeClassWithDeletion()
		node := getTestNode("test-nodeclass")

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(nodeClass, node).
			Build()

		recorder := record.NewFakeRecorder(10)
		controller, err := NewController(fakeClient, recorder)
		require.NoError(t, err)

		req := reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "test-nodeclass"},
		}

		// First reconcile - should delete node
		_, err = controller.Reconcile(ctx, req)
		assert.NoError(t, err)

		// Second reconcile - should be no-op since node is already deleted
		_, err = controller.Reconcile(ctx, req)
		assert.NoError(t, err)
	})

	t.Run("nodeclass without nodeclass label nodes", func(t *testing.T) {
		nodeClass := getTestNodeClassWithDeletion()
		node := &v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-without-label",
				// No nodeclass label
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(nodeClass, node).
			Build()

		recorder := record.NewFakeRecorder(10)
		controller, err := NewController(fakeClient, recorder)
		require.NoError(t, err)

		_, err = controller.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "test-nodeclass"},
		})
		assert.NoError(t, err)

		// Node without label should still exist
		var existingNode v1.Node
		err = fakeClient.Get(ctx, types.NamespacedName{Name: "node-without-label"}, &existingNode)
		assert.NoError(t, err)
	})
}
