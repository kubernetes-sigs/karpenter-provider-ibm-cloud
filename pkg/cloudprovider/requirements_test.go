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
package cloudprovider

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestNodeClaimRequirementLabels(t *testing.T) {
	for _, test := range []struct {
		name     string
		operator corev1.NodeSelectorOperator
		value    string
		want     string
	}{
		{name: "allowed value", operator: corev1.NodeSelectorOpIn, value: "allowed", want: "allowed"},
		{name: "excluded value", operator: corev1.NodeSelectorOpNotIn, value: "forbidden"},
		{name: "lower bound", operator: corev1.NodeSelectorOpGt, value: "4"},
		{name: "upper bound", operator: corev1.NodeSelectorOpLt, value: "4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			claim := &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{Name: "test"},
				Spec: karpv1.NodeClaimSpec{Requirements: []karpv1.NodeSelectorRequirementWithMinValues{{
					Key: "workload", Operator: test.operator, Values: []string{test.value},
				}}},
			}
			result, err := (&CloudProvider{}).nodeClaimForNode(claim, &corev1.Node{}, nil, nil)
			require.NoError(t, err)
			require.Equal(t, test.want, result.Labels["workload"])
		})
	}
}
