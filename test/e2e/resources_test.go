//go:build e2e
// +build e2e

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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestScaleDeploymentRetriesConflictAndPreservesConcurrentFields(t *testing.T) {
	replicas := int32(4)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "default", UID: "original-uid"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
	patches := 0
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithStatusSubresource(&appsv1.Deployment{}).WithObjects(deployment).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, options ...client.PatchOption) error {
			patches++
			if patches == 1 {
				current := &appsv1.Deployment{}
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(object), current))
				current.Spec.Template.Spec.NodeSelector = map[string]string{"concurrent": "preserved"}
				current.Annotations = map[string]string{"concurrent": "preserved"}
				require.NoError(t, c.Update(ctx, current))
				current.Status.ReadyReplicas = 3
				require.NoError(t, c.Status().Update(ctx, current))
			}
			return c.Patch(ctx, object, patch, options...)
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.NoError(t, suite.scaleTestDeployment(t.Context(), deployment, 8))
	require.Equal(t, 2, patches, "A resource-version conflict must retry from fresh state")
	current := &appsv1.Deployment{}
	require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(deployment), current))
	require.EqualValues(t, 8, *current.Spec.Replicas)
	require.Equal(t, "preserved", current.Spec.Template.Spec.NodeSelector["concurrent"])
	require.Equal(t, "preserved", current.Annotations["concurrent"])
	require.EqualValues(t, 3, current.Status.ReadyReplicas)
}

func TestScaleDeploymentRejectsReplacementDuringConflictRetry(t *testing.T) {
	replicas := int32(4)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: "default", UID: "original-uid"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
	patches := 0
	kube := fake.NewClientBuilder().WithScheme(cleanupTestScheme(t)).WithObjects(deployment).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, _ client.Patch, _ ...client.PatchOption) error {
			patches++
			current := &appsv1.Deployment{}
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(object), current))
			require.NoError(t, c.Delete(ctx, current))
			current.UID = "replacement-uid"
			current.ResourceVersion = ""
			require.NoError(t, c.Create(ctx, current))
			return apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, object.GetName(), fmt.Errorf("deployment was replaced"))
		},
	}).Build()
	suite := &E2ETestSuite{kubeClient: kube}
	require.ErrorContains(t, suite.scaleTestDeployment(t.Context(), deployment, 8), "deployment identity changed")
	require.Equal(t, 1, patches)
	current := &appsv1.Deployment{}
	require.NoError(t, kube.Get(t.Context(), client.ObjectKeyFromObject(deployment), current))
	require.EqualValues(t, 4, *current.Spec.Replicas)
	require.EqualValues(t, "replacement-uid", current.UID)
}
