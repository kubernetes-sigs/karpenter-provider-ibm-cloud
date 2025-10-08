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
package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclass/hash"
	nodeclassstatus "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclass/status"
)

func TestHashControllerIntegration(t *testing.T) {
	ctx := context.Background()

	// Create scheme and add our types
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	// Create test NodeClass
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:            "us-south-1",
			InstanceProfile: "bx2-4x16",
			Image:           "r006-test-image-id",
			VPC:             "r006-test-vpc-id",
			Subnet:          "r006-test-subnet-id",
		},
	}

	// Create fake client
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClass).
		WithStatusSubresource(nodeClass).
		Build()

	// Create hash controller
	hashController, err := hash.NewController(fakeClient)
	require.NoError(t, err)

	// Reconcile the NodeClass
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: nodeClass.Name,
		},
	}

	result, err := hashController.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	// Verify hash was calculated
	var updatedNodeClass v1alpha1.IBMNodeClass
	err = fakeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &updatedNodeClass)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedNodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash])

	// Store original hash
	originalHash := updatedNodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash]

	// Update the spec and reconcile again
	updatedNodeClass.Spec.InstanceProfile = "bx2-8x32"
	err = fakeClient.Update(ctx, &updatedNodeClass)
	require.NoError(t, err)

	_, err = hashController.Reconcile(ctx, req)
	assert.NoError(t, err)

	// Verify hash changed
	err = fakeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &updatedNodeClass)
	require.NoError(t, err)
	assert.NotEqual(t, originalHash, updatedNodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash])
}

func TestStatusControllerIntegration(t *testing.T) {
	ctx := context.Background()

	// Create scheme and add our types
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	tests := []struct {
		name               string
		nodeClass          *v1alpha1.IBMNodeClass
		expectedConditions int
		expectedReady      bool
	}{
		{
			name: "valid NodeClass",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "valid-nodeclass"},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-1",
					InstanceProfile: "bx2-4x16",
					Image:           "r006-test-image-id",
					VPC:             "r006-test-vpc-id",
					Subnet:          "r006-test-subnet-id",
				},
			},
			expectedConditions: 1,
			expectedReady:      true,
		},
		{
			name: "invalid NodeClass - missing VPC",
			nodeClass: &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-nodeclass"},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-1",
					InstanceProfile: "bx2-4x16",
					Image:           "r006-test-image-id",
					// VPC missing
				},
			},
			expectedConditions: 1,
			expectedReady:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.nodeClass).
				WithStatusSubresource(tt.nodeClass).
				Build()

			// Create status controller for testing (bypasses IBM client requirement)
			statusController := nodeclassstatus.NewTestController(fakeClient)

			// Reconcile the NodeClass
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: tt.nodeClass.Name,
				},
			}

			result, err := statusController.Reconcile(ctx, req)
			assert.NoError(t, err)
			assert.Equal(t, reconcile.Result{}, result)

			// Verify status was updated
			var updatedNodeClass v1alpha1.IBMNodeClass
			err = fakeClient.Get(ctx, types.NamespacedName{Name: tt.nodeClass.Name}, &updatedNodeClass)
			require.NoError(t, err)

			assert.Len(t, updatedNodeClass.Status.Conditions, tt.expectedConditions)

			if len(updatedNodeClass.Status.Conditions) > 0 {
				readyCondition := updatedNodeClass.Status.Conditions[0]
				if tt.expectedReady {
					assert.Equal(t, metav1.ConditionTrue, readyCondition.Status)
				} else {
					assert.Equal(t, metav1.ConditionFalse, readyCondition.Status)
				}
			}
		})
	}
}

func TestControllerErrorHandling(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	// Test with non-existent NodeClass
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	hashController, err := hash.NewController(fakeClient)
	require.NoError(t, err)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "non-existent-nodeclass",
		},
	}

	// Should handle not found gracefully
	result, err := hashController.Reconcile(ctx, req)
	assert.NoError(t, err) // Should ignore not found
	assert.Equal(t, reconcile.Result{}, result)
}

func TestControllerConcurrency(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	// Create multiple NodeClasses
	nodeClasses := []*v1alpha1.IBMNodeClass{}
	for i := 0; i < 10; i++ {
		nc := &v1alpha1.IBMNodeClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "nodeclass-" + string(rune('a'+i)),
			},
			Spec: v1alpha1.IBMNodeClassSpec{
				Region:          "us-south",
				Zone:            "us-south-1",
				InstanceProfile: "bx2-4x16",
				Image:           "r006-test-image-id",
				VPC:             "r006-test-vpc-id",
			},
		}
		nodeClasses = append(nodeClasses, nc)
	}

	// Convert to objects for fake client
	objects := make([]runtime.Object, len(nodeClasses))
	for i, nc := range nodeClasses {
		objects[i] = nc
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objects...).
		WithStatusSubresource(&v1alpha1.IBMNodeClass{}).
		Build()

	hashController, err := hash.NewController(fakeClient)
	require.NoError(t, err)

	// Process all NodeClasses concurrently
	done := make(chan bool, len(nodeClasses))
	errors := make(chan error, len(nodeClasses))

	for _, nc := range nodeClasses {
		go func(nodeClassName string) {
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeClassName,
				},
			}

			_, reconcileErr := hashController.Reconcile(ctx, req)
			if reconcileErr != nil {
				errors <- reconcileErr
			} else {
				done <- true
			}
		}(nc.Name)
	}

	// Wait for all to complete
	completed := 0
	timeout := time.After(5 * time.Second)

	for completed < len(nodeClasses) {
		select {
		case <-done:
			completed++
		case recvErr := <-errors:
			t.Errorf("Unexpected error during concurrent reconciliation: %v", recvErr)
			completed++
		case <-timeout:
			t.Fatal("Timeout waiting for concurrent reconciliation to complete")
		}
	}

	// Verify all NodeClasses were processed
	for _, nc := range nodeClasses {
		var updatedNodeClass v1alpha1.IBMNodeClass
		err = fakeClient.Get(ctx, types.NamespacedName{Name: nc.Name}, &updatedNodeClass)
		require.NoError(t, err)
		assert.NotEmpty(t, updatedNodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash], "Hash should be set for %s", nc.Name)
	}
}

// Test controller lifecycle and cleanup
func TestControllerLifecycle(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	// Create NodeClass with deletion timestamp
	now := metav1.Now()
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "deleting-nodeclass",
			DeletionTimestamp: &now,
			Finalizers:        []string{"test-finalizer"},
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region: "us-south",
			VPC:    "r006-test-vpc-id",
			Image:  "r006-test-image-id",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClass).
		WithStatusSubresource(nodeClass).
		Build()

	hashController, err := hash.NewController(fakeClient)
	require.NoError(t, err)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: nodeClass.Name,
		},
	}

	// Reconcile should return error when NodeClass is being deleted
	result, err := hashController.Reconcile(ctx, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot reconcile IBMNodeClass being deleted")
	assert.Equal(t, reconcile.Result{}, result)
}

// Integration test for multiple controllers working together
func TestMultiControllerIntegration(t *testing.T) {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "integration-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:            "us-south-1",
			InstanceProfile: "bx2-4x16",
			Image:           "r006-test-image-id",
			VPC:             "r006-test-vpc-id",
			Subnet:          "r006-test-subnet-id",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeClass).
		WithStatusSubresource(nodeClass).
		Build()

	// Create both controllers
	hashController, err := hash.NewController(fakeClient)
	require.NoError(t, err)

	statusController := nodeclassstatus.NewTestController(fakeClient)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: nodeClass.Name,
		},
	}

	// Reconcile with hash controller first
	_, err = hashController.Reconcile(ctx, req)
	require.NoError(t, err)

	// Then reconcile with status controller
	_, err = statusController.Reconcile(ctx, req)
	require.NoError(t, err)

	// Verify both controllers worked
	var updatedNodeClass v1alpha1.IBMNodeClass
	err = fakeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &updatedNodeClass)
	require.NoError(t, err)

	// Hash should be set
	assert.NotEmpty(t, updatedNodeClass.Annotations[v1alpha1.AnnotationIBMNodeClassHash])

	// Status condition should be set
	assert.NotEmpty(t, updatedNodeClass.Status.Conditions)
	if len(updatedNodeClass.Status.Conditions) > 0 {
		assert.Equal(t, "Ready", updatedNodeClass.Status.Conditions[0].Type)
		assert.Equal(t, metav1.ConditionTrue, updatedNodeClass.Status.Conditions[0].Status)
	}
}
