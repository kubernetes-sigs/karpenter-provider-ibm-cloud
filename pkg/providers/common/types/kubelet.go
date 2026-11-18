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

import "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"

func EffectiveMaxPods(kubelet *v1alpha1.KubeletConfiguration, cpuCount int64) int64 {
	limit := int64(110)
	if kubelet == nil {
		return limit
	}
	if kubelet.MaxPods != nil {
		limit = int64(*kubelet.MaxPods)
	}
	if kubelet.PodsPerCore != nil && *kubelet.PodsPerCore > 0 {
		perCore := cpuCount * int64(*kubelet.PodsPerCore)
		if perCore < limit {
			limit = perCore
		}
	}
	return limit
}
