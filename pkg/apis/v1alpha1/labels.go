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

package v1alpha1

import (
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/constants"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

const (
	// Labels that can be selected on and are propagated to the node
	InstanceSizeLabelKey   = constants.Group + "/instance-size"
	InstanceFamilyLabelKey = constants.Group + "/instance-family"
	InstanceMemoryLabelKey = constants.Group + "/instance-memory"
	InstanceCPULabelKey    = constants.Group + "/instance-cpu"

	// Internal labels that are propagated to the node
	IbmLabelKey          = "ibm.x-k8s.io/node"
	IbmLabelValue        = "fake"
	NodeViewerLabelKey   = "eks-node-viewer/instance-price"
	IbmPartitionLabelKey = "ibm-partition"
)

func init() {
	v1.RestrictedLabelDomains = v1.RestrictedLabelDomains.Insert(constants.Group)
	v1.WellKnownLabels = v1.WellKnownLabels.Insert(
		InstanceSizeLabelKey,
		InstanceFamilyLabelKey,
		InstanceCPULabelKey,
		InstanceMemoryLabelKey,
	)
}
