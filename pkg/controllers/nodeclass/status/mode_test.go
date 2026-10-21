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

package status

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestIKSReadinessDoesNotResolveVPCImage(t *testing.T) {
	t.Setenv("BOOTSTRAP_MODE", "auto")
	t.Setenv("IKS_CLUSTER_ID", "")
	iks := "iks-api"
	for _, class := range []*v1alpha1.IBMNodeClass{
		{Spec: v1alpha1.IBMNodeClassSpec{Region: "us-south", VPC: "vpc", IKSClusterID: "cluster"}},
		{Spec: v1alpha1.IBMNodeClassSpec{Region: "us-south", VPC: "vpc", BootstrapMode: &iks, Image: "retired-image"}},
	} {
		class.Status.ResolvedImageID = "stale-image"
		controller := &Controller{}
		require.NoError(t, controller.validateRequiredFields(class))
		require.NoError(t, controller.validateProviderImage(context.Background(), class))
		require.Empty(t, class.Status.ResolvedImageID)
	}
	cloudInit := "cloud-init"
	class := &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{Region: "us-south", VPC: "vpc", IKSClusterID: "cluster", BootstrapMode: &cloudInit}}
	require.ErrorContains(t, (&Controller{}).validateRequiredFields(class), "image or imageSelector")
}
