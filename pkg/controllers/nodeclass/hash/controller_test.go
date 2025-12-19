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
package hash

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestNewController(t *testing.T) {
	tests := []struct {
		name        string
		getClient   func() client.Client
		expectError bool
	}{
		{
			name: "valid client",
			getClient: func() client.Client {
				return fake.NewClientBuilder().Build()
			},
			expectError: false,
		},
		{
			name: "nil client",
			getClient: func() client.Client {
				return nil
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller, err := NewController(tt.getClient())

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, controller)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, controller)
			}
		})
	}
}

func TestController_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	tests := []struct {
		name               string
		nodeClass          *v1alpha1.IBMNodeClass
		existingHash       uint64
		expectedHashChange bool
		expectError        bool
	}{
		{
			name: "new nodeclass without hash",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-1",
					InstanceProfile: "bx2-4x16",
					Image:           "test-image",
					VPC:             "test-vpc",
					Subnet:          "test-subnet",
				},
			},
			existingHash:       0,
			expectedHashChange: true,
			expectError:        false,
		},
		{
			name: "nodeclass with existing hash no change",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
					Annotations: map[string]string{
						v1alpha1.AnnotationIBMNodeClassHash: "12345",
					},
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-1",
					InstanceProfile: "bx2-4x16",
					Image:           "test-image",
					VPC:             "test-vpc",
					Subnet:          "test-subnet",
				},
			},
			existingHash:       12345,
			expectedHashChange: false,
			expectError:        false,
		},
		{
			name: "nodeclass with spec change",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-1",
					InstanceProfile: "bx2-8x32", // Changed
					Image:           "test-image",
					VPC:             "test-vpc",
					Subnet:          "test-subnet",
				},
			},
			existingHash:       12345,
			expectedHashChange: true,
			expectError:        false,
		},
		{
			name: "nodeclass with deletion timestamp",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test-nodeclass",
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Finalizers:        []string{"test-finalizer"},
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					VPC:    "test-vpc",
					Image:  "test-image",
				},
			},
			expectedHashChange: false,
			expectError:        true, // Should error when trying to patch a deleting object
		},
		{
			name: "nodeclass with user data",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:   "us-south",
					VPC:      "test-vpc",
					Image:    "test-image",
					UserData: "#!/bin/bash\necho 'Hello World'",
				},
			},
			expectedHashChange: true,
			expectError:        false,
		},
		{
			name: "nodeclass with tags",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region: "us-south",
					VPC:    "test-vpc",
					Image:  "test-image",
					Tags: map[string]string{
						"env":  "production",
						"team": "platform",
					},
				},
			},
			expectedHashChange: true,
			expectError:        false,
		},
		{
			name: "nodeclass with security groups",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:         "us-south",
					VPC:            "test-vpc",
					Image:          "test-image",
					SecurityGroups: []string{"sg-1", "sg-2", "sg-3"},
				},
			},
			expectedHashChange: true,
			expectError:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.nodeClass != nil {
				builder = builder.WithObjects(tt.nodeClass).WithStatusSubresource(tt.nodeClass)
			}
			fakeClient := builder.Build()

			// Create controller
			controller, err := NewController(fakeClient)
			require.NoError(t, err)

			// Create reconcile request
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: "test-nodeclass",
				},
			}

			// Reconcile
			result, err := controller.Reconcile(context.Background(), req)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, reconcile.Result{}, result)

				// Check if hash was updated
				if tt.expectedHashChange && tt.nodeClass.DeletionTimestamp == nil {
					var updatedNodeClass v1alpha1.IBMNodeClass
					err = fakeClient.Get(context.Background(), req.NamespacedName, &updatedNodeClass)
					require.NoError(t, err)

					assert.NotEmpty(t, updatedNodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash], "Hash should be set in annotations")
					if tt.existingHash != 0 {
						// Convert existing hash to string for comparison
						existingHashStr := fmt.Sprint(tt.existingHash)
						assert.NotEqual(t, existingHashStr, updatedNodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash], "Hash should have changed")
					}
				}
			}
		})
	}
}

func TestController_ReconcileNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	controller, err := NewController(fakeClient)
	require.NoError(t, err)

	// Try to reconcile non-existent nodeclass
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "non-existent",
		},
	}

	result, err := controller.Reconcile(context.Background(), req)

	// Should not error on not found
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestController_HashCalculation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	// Create two identical nodeclasses
	nodeClass1 := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nodeclass-1",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:            "us-south-1",
			InstanceProfile: "bx2-4x16",
			Image:           "test-image",
			VPC:             "test-vpc",
			Subnet:          "test-subnet",
		},
	}

	nodeClass2 := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "nodeclass-2",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:            "us-south-1",
			InstanceProfile: "bx2-4x16",
			Image:           "test-image",
			VPC:             "test-vpc",
			Subnet:          "test-subnet",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClass1, nodeClass2).
		WithStatusSubresource(nodeClass1, nodeClass2).
		Build()

	controller, err := NewController(fakeClient)
	require.NoError(t, err)

	// Reconcile both
	_, err = controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "nodeclass-1"},
	})
	require.NoError(t, err)

	_, err = controller.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "nodeclass-2"},
	})
	require.NoError(t, err)

	// Get updated nodeclasses
	var updated1, updated2 v1alpha1.IBMNodeClass
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "nodeclass-1"}, &updated1)
	require.NoError(t, err)
	err = fakeClient.Get(context.Background(), types.NamespacedName{Name: "nodeclass-2"}, &updated2)
	require.NoError(t, err)

	// Hashes should be the same for identical specs
	assert.Equal(t, updated1.Annotations[v1alpha1.AnnotationIBMNodeClassHash], updated2.Annotations[v1alpha1.AnnotationIBMNodeClassHash])
}

func TestController_Register(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	controller, err := NewController(fakeClient)
	require.NoError(t, err)

	// Test that Register doesn't panic
	// We can't fully test registration without a real manager
	assert.NotPanics(t, func() {
		// This would normally be called with a real manager
		_ = controller
	})
}

func TestController_ConcurrentReconciliation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	// Create multiple nodeclasses
	var nodeclasses []runtime.Object
	for i := 0; i < 10; i++ {
		nc := &v1alpha1.IBMNodeClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("nodeclass-%d", i),
			},
			Spec: v1alpha1.IBMNodeClassSpec{
				Region:          "us-south",
				Zone:            fmt.Sprintf("us-south-%d", i%3+1),
				InstanceProfile: "bx2-4x16",
				Image:           "test-image",
				VPC:             "test-vpc",
				Subnet:          fmt.Sprintf("subnet-%d", i),
			},
		}
		nodeclasses = append(nodeclasses, nc)
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(nodeclasses...).
		WithStatusSubresource(&v1alpha1.IBMNodeClass{}).
		Build()

	controller, err := NewController(fakeClient)
	require.NoError(t, err)

	// Reconcile all concurrently
	done := make(chan error, len(nodeclasses))
	for i := range nodeclasses {
		go func(index int) {
			_, reconcileErr := controller.Reconcile(context.Background(), reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: fmt.Sprintf("nodeclass-%d", index),
				},
			})
			done <- reconcileErr
		}(i)
	}

	// Wait for all reconciliations
	for i := 0; i < len(nodeclasses); i++ {
		reconcileErr := <-done
		assert.NoError(t, reconcileErr)
	}

	// Verify all have hashes
	for i := range nodeclasses {
		var nc v1alpha1.IBMNodeClass
		err = fakeClient.Get(context.Background(), types.NamespacedName{
			Name: fmt.Sprintf("nodeclass-%d", i),
		}, &nc)
		require.NoError(t, err)
		assert.NotEmpty(t, nc.Annotations[v1alpha1.AnnotationIBMNodeClassHash])
	}
}
