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

package ibm_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	ibmMock "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestInstanceAddressesRequireFreshExactAssignments(t *testing.T) {
	for _, scenario := range []string{"verified", "changed subnet", "changed address", "changed NIC", "NIC unavailable", "NIC API error", "missing floating inventory", "floating API error", "invalid floating address", "duplicate NIC", "missing primary"} {
		t.Run(scenario, func(t *testing.T) {
			sdk := ibmMock.NewMockvpcClientInterface(gomock.NewController(t))
			vm := &vpcv1.Instance{ID: core.StringPtr("vm-id"), PrimaryNetworkInterface: &vpcv1.NetworkInterfaceInstanceContextReference{ID: core.StringPtr("nic-id"), Subnet: &vpcv1.SubnetReference{ID: core.StringPtr("subnet-id")}, PrimaryIP: &vpcv1.ReservedIPReference{Address: core.StringPtr("10.240.0.9")}}, NetworkInterfaces: []vpcv1.NetworkInterfaceInstanceContextReference{{ID: core.StringPtr("nic-id")}}}
			nic := &vpcv1.NetworkInterface{ID: core.StringPtr("nic-id"), Status: core.StringPtr("available"), Subnet: &vpcv1.SubnetReference{ID: core.StringPtr("subnet-id")}, PrimaryIP: &vpcv1.ReservedIPReference{Address: core.StringPtr("10.240.0.9")}}
			floating := &vpcv1.FloatingIPUnpaginatedCollection{FloatingIps: []vpcv1.FloatingIP{{Address: core.StringPtr("150.240.0.9")}}}
			var nicErr, floatingErr error
			switch scenario {
			case "changed subnet":
				nic.Subnet.ID = core.StringPtr("other")
			case "changed address":
				nic.PrimaryIP.Address = core.StringPtr("10.240.0.10")
			case "changed NIC":
				nic.ID = core.StringPtr("other")
			case "NIC unavailable":
				nic.Status = core.StringPtr("deleting")
			case "NIC API error":
				nicErr = fmt.Errorf("unavailable")
			case "missing floating inventory":
				floating = nil
			case "floating API error":
				floatingErr = fmt.Errorf("unavailable")
			case "invalid floating address":
				floating.FloatingIps[0].Address = core.StringPtr("invalid")
			case "duplicate NIC":
				vm.NetworkInterfaces = append(vm.NetworkInterfaces, vm.NetworkInterfaces[0])
			case "missing primary":
				vm.NetworkInterfaces = nil
			}
			if scenario != "missing primary" {
				sdk.EXPECT().GetInstanceNetworkInterfaceWithContext(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, o *vpcv1.GetInstanceNetworkInterfaceOptions) (*vpcv1.NetworkInterface, *core.DetailedResponse, error) {
					require.Equal(t, "vm-id", *o.InstanceID)
					require.Equal(t, "nic-id", *o.ID)
					return nic, nil, nicErr
				})
				if scenario == "verified" || scenario == "duplicate NIC" || scenario == "missing floating inventory" || scenario == "floating API error" || scenario == "invalid floating address" {
					sdk.EXPECT().ListInstanceNetworkInterfaceFloatingIpsWithContext(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, o *vpcv1.ListInstanceNetworkInterfaceFloatingIpsOptions) (*vpcv1.FloatingIPUnpaginatedCollection, *core.DetailedResponse, error) {
						require.Equal(t, "vm-id", *o.InstanceID)
						require.Equal(t, "nic-id", *o.NetworkInterfaceID)
						return floating, nil, floatingErr
					})
				}
			}
			private, public, err := ibm.NewVPCClientWithMock(sdk).GetInstanceNetworkAddresses(context.Background(), vm)
			if scenario == "verified" {
				require.NoError(t, err)
				require.Equal(t, []string{"10.240.0.9"}, private)
				require.Equal(t, []string{"150.240.0.9"}, public)
			} else {
				require.Error(t, err)
				require.Nil(t, private)
				require.Nil(t, public)
			}
		})
	}
}
