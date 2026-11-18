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

package loadbalancer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/stretchr/testify/require"
	"testing"
)

type membershipCloud struct {
	LoadBalancerVPCClient
	members   map[string][]vpcv1.LoadBalancerPoolMember
	creates   int
	ambiguous bool
	deleted   []string
}

func (c *membershipCloud) ListLoadBalancerPoolMembers(_ context.Context, _ string, pool string) (*vpcv1.LoadBalancerPoolMemberCollection, error) {
	return &vpcv1.LoadBalancerPoolMemberCollection{Members: c.members[pool]}, nil
}
func (c *membershipCloud) CreateLoadBalancerPoolMember(_ context.Context, _, pool string, target vpcv1.LoadBalancerPoolMemberTargetPrototypeIntf, port, weight int64) (*vpcv1.LoadBalancerPoolMember, error) {
	c.creates++
	instance := target.(*vpcv1.LoadBalancerPoolMemberTargetPrototypeInstanceIdentity)
	member := vpcv1.LoadBalancerPoolMember{ID: core.StringPtr(pool), Target: &vpcv1.LoadBalancerPoolMemberTarget{ID: instance.ID}, Port: &port, Weight: &weight}
	c.members[pool] = append(c.members[pool], member)
	if c.ambiguous && pool == "pool2" {
		c.ambiguous = false
		return nil, errors.New("response lost")
	}
	return &member, nil
}
func (c *membershipCloud) DeleteLoadBalancerPoolMember(_ context.Context, _, _, id string) error {
	c.deleted = append(c.deleted, id)
	return nil
}
func (c *membershipCloud) GetLoadBalancerPoolMember(_ context.Context, _, _, id string) (*vpcv1.LoadBalancerPoolMember, error) {
	return &vpcv1.LoadBalancerPoolMember{ID: &id, Health: core.StringPtr("ok")}, nil
}

func TestRetryAdoptsPartialAndAmbiguousMembershipByInstanceAndPort(t *testing.T) {
	cloud := &membershipCloud{members: map[string][]vpcv1.LoadBalancerPoolMember{}, ambiguous: true}
	provider := NewLoadBalancerProvider(cloud, logr.Discard())
	snapshot := &Snapshot{InstanceID: "instance", RegistrationTimeout: 1, AutoDeregister: true, Targets: []ResolvedTarget{{PoolID: "pool1", Target: v1alpha1.LoadBalancerTarget{LoadBalancerID: "lb", Port: 80}}, {PoolID: "pool2", Target: v1alpha1.LoadBalancerTarget{LoadBalancerID: "lb", Port: 8080}}}}
	require.Error(t, provider.RegisterTargets(context.Background(), snapshot))
	require.Equal(t, 2, cloud.creates)
	require.NoError(t, provider.RegisterTargets(context.Background(), snapshot))
	require.Equal(t, 2, cloud.creates)
	cloud.members["pool1"] = append(cloud.members["pool1"], vpcv1.LoadBalancerPoolMember{ID: core.StringPtr("unrelated-port"), Target: &vpcv1.LoadBalancerPoolMemberTarget{ID: core.StringPtr("instance")}, Port: core.Int64Ptr(443)})
	require.NoError(t, provider.DeregisterTargets(context.Background(), snapshot))
	require.ElementsMatch(t, []string{"pool1", "pool2"}, cloud.deleted)
}

func TestFutureLoadBalancerSnapshotRetained(t *testing.T) {
	encoded, err := json.Marshal(&Snapshot{Version: 2, MinimumWriterVersion: 2})
	require.NoError(t, err)
	_, err = DecodeSnapshot(string(encoded))
	require.Error(t, err)
}
