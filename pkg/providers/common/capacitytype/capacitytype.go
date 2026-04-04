/*
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

package capacitytype

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

func ResolveCapacityType(nodeClaim *karpv1.NodeClaim, instanceTypes []*cloudprovider.InstanceType) string {
	reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)
	allowed := reqs.Get(karpv1.CapacityTypeLabelKey)

	if allowed.Has(karpv1.CapacityTypeSpot) {
		for _, it := range instanceTypes {
			for _, o := range it.Offerings.Compatible(reqs).Available() {
				if o.Requirements.Get(karpv1.CapacityTypeLabelKey).Has(karpv1.CapacityTypeSpot) {
					return karpv1.CapacityTypeSpot
				}
			}
		}
	}

	return karpv1.CapacityTypeOnDemand
}

func GetCapacityTypeFromAvailabilityClass(ctx context.Context, class string) string {
	switch class {
	case "spot":
		return karpv1.CapacityTypeSpot
	case "standard", "":
		return karpv1.CapacityTypeOnDemand
	default:
		log.FromContext(ctx).Info("Unknown IBM availability class, defaulting to on-demand", "class", class)
		return karpv1.CapacityTypeOnDemand
	}
}
