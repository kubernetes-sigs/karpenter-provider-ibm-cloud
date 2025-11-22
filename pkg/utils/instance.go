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

package utils

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// ParseInstanceID extracts the instance ID from a provider ID string
func ParseInstanceID(providerID string) (string, error) {
	if providerID == "" {
		return "", fmt.Errorf("provider ID is empty")
	}
	return providerID, nil
}

// GetAllSingleValuedRequirementLabels returns labels for requirements that have exactly one value
func GetAllSingleValuedRequirementLabels(instanceType *cloudprovider.InstanceType) map[string]string {
	labels := map[string]string{}
	if instanceType == nil {
		return labels
	}
	for _, req := range instanceType.Requirements {
		values := req.Values()
		if len(values) == 1 && req.Operator() == corev1.NodeSelectorOpIn {
			labels[req.Key] = values[0]
		}
	}
	return labels
}
