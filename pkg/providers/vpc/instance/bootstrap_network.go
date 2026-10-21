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

package instance

import (
	"context"
	"fmt"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func (p *VPCInstanceProvider) VerifyLaunchNetworkAddresses(ctx context.Context, claim *karpv1.NodeClaim, vm *vpcv1.Instance) ([]string, []string, error) {
	fresh, config, err := p.freshLaunchClaim(ctx, claim)
	if err != nil {
		return nil, nil, err
	}
	if vm == nil || vm.ID == nil || vm.Name == nil || *vm.Name != config.Name ||
		(fresh.Status.ProviderID != "" && fresh.Status.ProviderID != fmt.Sprintf("ibm:///%s/%s", config.Region, *vm.ID)) {
		return nil, nil, fmt.Errorf("instance differs from the launch network target")
	}
	vpc, err := p.clientForRegion(ctx, config.Region)
	if err != nil {
		return nil, nil, err
	}
	private, public, err := vpc.GetInstanceNetworkAddresses(ctx, vm)
	if err != nil {
		return nil, nil, err
	}
	if _, _, err := p.freshLaunchClaim(ctx, fresh); err != nil {
		return nil, nil, err
	}
	return private, public, nil
}
