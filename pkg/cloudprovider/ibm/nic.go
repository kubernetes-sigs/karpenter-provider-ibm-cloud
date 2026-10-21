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

package ibm

import (
	"context"
	"fmt"
	"net"
	"sort"

	"github.com/IBM/vpc-go-sdk/vpcv1"
)

func (c *VPCClient) GetInstanceNetworkAddresses(ctx context.Context, instance *vpcv1.Instance) ([]string, []string, error) {
	if instance == nil || instance.ID == nil || instance.PrimaryNetworkInterface == nil || instance.PrimaryNetworkInterface.ID == nil {
		return nil, nil, fmt.Errorf("instance network identity is incomplete")
	}
	private, public := map[string]bool{}, map[string]bool{}
	seen := map[string]bool{}
	for _, reference := range instance.NetworkInterfaces {
		if reference.ID == nil || *reference.ID == "" || seen[*reference.ID] {
			return nil, nil, fmt.Errorf("instance network interface identity is absent or duplicated")
		}
		seen[*reference.ID] = true
		nic, _, err := c.client.GetInstanceNetworkInterfaceWithContext(ctx, &vpcv1.GetInstanceNetworkInterfaceOptions{InstanceID: instance.ID, ID: reference.ID})
		if err != nil {
			return nil, nil, fmt.Errorf("reading instance network interface: %w", err)
		}
		if nic == nil || nic.ID == nil || *nic.ID != *reference.ID || nic.Status == nil || *nic.Status != "available" ||
			nic.PrimaryIP == nil || nic.PrimaryIP.Address == nil || net.ParseIP(*nic.PrimaryIP.Address) == nil {
			return nil, nil, fmt.Errorf("instance network interface is not available with a valid assigned IP")
		}
		if *reference.ID == *instance.PrimaryNetworkInterface.ID && (nic.Subnet == nil || nic.Subnet.ID == nil ||
			instance.PrimaryNetworkInterface.Subnet == nil || instance.PrimaryNetworkInterface.Subnet.ID == nil ||
			*nic.Subnet.ID != *instance.PrimaryNetworkInterface.Subnet.ID || instance.PrimaryNetworkInterface.PrimaryIP == nil ||
			instance.PrimaryNetworkInterface.PrimaryIP.Address == nil || *nic.PrimaryIP.Address != *instance.PrimaryNetworkInterface.PrimaryIP.Address) {
			return nil, nil, fmt.Errorf("primary network interface assignment changed")
		}
		private[net.ParseIP(*nic.PrimaryIP.Address).String()] = true
		floating, _, err := c.client.ListInstanceNetworkInterfaceFloatingIpsWithContext(ctx, &vpcv1.ListInstanceNetworkInterfaceFloatingIpsOptions{InstanceID: instance.ID, NetworkInterfaceID: reference.ID})
		if err != nil {
			return nil, nil, fmt.Errorf("reading instance floating IP assignments: %w", err)
		}
		if floating == nil {
			return nil, nil, fmt.Errorf("instance floating IP inventory is unavailable")
		}
		for _, address := range floating.FloatingIps {
			if address.Address == nil || net.ParseIP(*address.Address) == nil {
				return nil, nil, fmt.Errorf("invalid assigned floating IP")
			}
			public[net.ParseIP(*address.Address).String()] = true
		}
	}
	if !seen[*instance.PrimaryNetworkInterface.ID] {
		return nil, nil, fmt.Errorf("primary network interface is absent from instance inventory")
	}
	toSlice := func(values map[string]bool) []string {
		result := make([]string, 0, len(values))
		for value := range values {
			result = append(result, value)
		}
		sort.Strings(result)
		return result
	}
	return toSlice(private), toSlice(public), nil
}
