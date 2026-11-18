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

package types

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestEffectiveMaxPods(t *testing.T) {
	max := int32(150)
	perCore := int32(10)
	zero := int32(0)
	for _, test := range []struct {
		name      string
		config    *v1alpha1.KubeletConfiguration
		cpu, want int64
	}{
		{"default", nil, 2, 110},
		{"explicit", &v1alpha1.KubeletConfiguration{MaxPods: &max}, 2, 150},
		{"per core", &v1alpha1.KubeletConfiguration{PodsPerCore: &perCore}, 2, 20},
		{"both", &v1alpha1.KubeletConfiguration{MaxPods: &max, PodsPerCore: &perCore}, 32, 150},
		{"disabled", &v1alpha1.KubeletConfiguration{MaxPods: &max, PodsPerCore: &zero}, 2, 150},
	} {
		t.Run(test.name, func(t *testing.T) { require.Equal(t, test.want, EffectiveMaxPods(test.config, test.cpu)) })
	}
}
