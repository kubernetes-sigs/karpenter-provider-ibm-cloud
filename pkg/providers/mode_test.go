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

package providers

import (
	"testing"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/stretchr/testify/require"
)

func TestResolveProviderMode(t *testing.T) {
	for _, test := range []struct {
		name, global, class, cluster, environment string
		want                                      commonTypes.ProviderMode
		invalid                                   bool
	}{
		{name: "default", want: commonTypes.VPCMode},
		{name: "global IKS", global: "iks-api", want: commonTypes.IKSMode},
		{name: "global VPC beats environment", global: "cloud-init", environment: "cluster", want: commonTypes.VPCMode},
		{name: "explicit VPC beats class IKS identity", global: "iks-api", class: "cloud-init", cluster: "cluster", want: commonTypes.VPCMode},
		{name: "explicit IKS beats global VPC", global: "cloud-init", class: "iks-api", want: commonTypes.IKSMode},
		{name: "automatic class IKS identity", global: "cloud-init", class: "auto", cluster: "cluster", want: commonTypes.IKSMode},
		{name: "automatic environment", global: "auto", environment: "cluster", want: commonTypes.IKSMode},
		{name: "invalid global even with override", global: "invalid", class: "cloud-init", invalid: true},
		{name: "invalid class", class: "invalid", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BOOTSTRAP_MODE", test.global)
			t.Setenv("IKS_CLUSTER_ID", test.environment)
			nc := &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{IKSClusterID: test.cluster}}
			if test.class != "" {
				nc.Spec.BootstrapMode = &test.class
			}
			mode, err := ResolveProviderMode(nc)
			if test.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, mode)
		})
	}
}
