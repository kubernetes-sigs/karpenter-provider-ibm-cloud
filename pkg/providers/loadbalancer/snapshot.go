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
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

const SnapshotAnnotation = "loadbalancer.ibm.sh/targets"

type ResolvedTarget struct {
	Target v1alpha1.LoadBalancerTarget `json:"target"`
	PoolID string                      `json:"poolID"`
}

type Snapshot struct {
	Version              int              `json:"version"`
	MinimumWriterVersion int              `json:"minimumWriterVersion"`
	ClaimUID             string           `json:"claimUID"`
	ClassUID             string           `json:"classUID"`
	ClusterUID           string           `json:"clusterUID"`
	AccountID            string           `json:"accountID"`
	Region               string           `json:"region"`
	ProviderID           string           `json:"providerID"`
	InstanceID           string           `json:"instanceID"`
	AutoDeregister       bool             `json:"autoDeregister"`
	RegistrationTimeout  int32            `json:"registrationTimeout"`
	Targets              []ResolvedTarget `json:"targets"`
}

func DecodeSnapshot(value string) (*Snapshot, error) {
	if value == "" {
		return nil, nil
	}
	s := &Snapshot{}
	d := json.NewDecoder(strings.NewReader(value))
	d.DisallowUnknownFields()
	if err := d.Decode(s); err != nil {
		return nil, fmt.Errorf("invalid load balancer snapshot: %w", err)
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing load balancer snapshot")
	}
	if err := ownership.ValidateStateVersion(s.Version, s.MinimumWriterVersion); err != nil {
		return nil, err
	}
	if s.ClaimUID == "" || s.ClassUID == "" || s.ClusterUID == "" || s.AccountID == "" || s.Region == "" || s.ProviderID == "" || s.InstanceID == "" {
		return nil, fmt.Errorf("incomplete load balancer ownership snapshot")
	}
	for _, target := range s.Targets {
		if target.PoolID == "" || target.Target.LoadBalancerID == "" || target.Target.Port < 1 || target.Target.Port > 65535 {
			return nil, fmt.Errorf("incomplete load balancer target snapshot")
		}
	}
	return s, nil
}

func (p *LoadBalancerProvider) VerifyAccount(ctx context.Context, account string) error {
	if p.accountID == nil {
		return fmt.Errorf("load balancer credential identity is unavailable")
	}
	actual, err := p.accountID(ctx)
	if err != nil {
		return err
	}
	if actual != account {
		return fmt.Errorf("load balancer credentials target another account")
	}
	return nil
}

func (p *LoadBalancerProvider) ResolveTargets(ctx context.Context, class *v1alpha1.IBMNodeClass, account string) ([]ResolvedTarget, error) {
	var targets []ResolvedTarget
	for _, target := range class.Spec.LoadBalancerIntegration.TargetGroups {
		lb, err := p.vpcClient.GetLoadBalancer(ctx, target.LoadBalancerID)
		if err != nil {
			return nil, err
		}
		if lb == nil || lb.CRN == nil || !strings.Contains(*lb.CRN, ":a/"+account+"::load-balancer:") {
			return nil, fmt.Errorf("load balancer account cannot be established")
		}
		poolID, err := p.findPoolByName(ctx, target.LoadBalancerID, target.PoolName)
		if err != nil {
			return nil, err
		}
		if err := NewHealthCheckManager(p.vpcClient, p.logger).ValidateHealthCheck(target.HealthCheck); err != nil {
			return nil, err
		}
		targets = append(targets, ResolvedTarget{Target: target, PoolID: poolID})
	}
	return targets, nil
}

func (p *LoadBalancerProvider) RegisterTargets(ctx context.Context, s *Snapshot) error {
	if s.RegistrationTimeout > 0 {
		bounded, cancel := context.WithTimeout(ctx, time.Duration(s.RegistrationTimeout)*time.Second)
		defer cancel()
		ctx = bounded
	}
	for _, target := range s.Targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := NewHealthCheckManager(p.vpcClient, p.logger).ConfigureHealthCheck(ctx, target.Target, target.PoolID); err != nil {
			return err
		}
		member, err := p.findMember(ctx, target.Target.LoadBalancerID, target.PoolID, s.InstanceID, int64(target.Target.Port))
		if err != nil {
			return err
		}
		if member == nil {
			weight := int64(50)
			if target.Target.Weight != nil {
				weight = int64(*target.Target.Weight)
			}
			member, err = p.vpcClient.CreateLoadBalancerPoolMember(ctx, target.Target.LoadBalancerID, target.PoolID, &vpcv1.LoadBalancerPoolMemberTargetPrototypeInstanceIdentity{ID: &s.InstanceID}, int64(target.Target.Port), weight)
			if err != nil {
				return fmt.Errorf("creating load balancer member; retry will re-read membership: %w", err)
			}
		}
		if member == nil || member.ID == nil {
			return fmt.Errorf("load balancer member response has no identity")
		}
		if s.RegistrationTimeout > 0 {
			if err := p.waitForMemberHealthy(ctx, target.Target.LoadBalancerID, target.PoolID, *member.ID, time.Duration(s.RegistrationTimeout)*time.Second, p.logger); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *LoadBalancerProvider) DeregisterTargets(ctx context.Context, s *Snapshot) error {
	if !s.AutoDeregister {
		return nil
	}
	for _, target := range s.Targets {
		members, err := p.vpcClient.ListLoadBalancerPoolMembers(ctx, target.Target.LoadBalancerID, target.PoolID)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if members == nil {
			return fmt.Errorf("load balancer membership collection is missing")
		}
		for _, member := range members.Members {
			if !matchesMember(&member, s.InstanceID, int64(target.Target.Port)) {
				continue
			}
			if member.ID == nil {
				return fmt.Errorf("matching load balancer member has no ID")
			}
			if err := p.vpcClient.DeleteLoadBalancerPoolMember(ctx, target.Target.LoadBalancerID, target.PoolID, *member.ID); err != nil && !isNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func matchesMember(member *vpcv1.LoadBalancerPoolMember, instanceID string, port int64) bool {
	target, ok := member.Target.(*vpcv1.LoadBalancerPoolMemberTarget)
	return ok && target.ID != nil && *target.ID == instanceID && member.Port != nil && *member.Port == port
}
func (p *LoadBalancerProvider) findMember(ctx context.Context, lb, pool, instance string, port int64) (*vpcv1.LoadBalancerPoolMember, error) {
	members, err := p.vpcClient.ListLoadBalancerPoolMembers(ctx, lb, pool)
	if err != nil {
		return nil, err
	}
	if members == nil {
		return nil, fmt.Errorf("load balancer membership collection is missing")
	}
	var found *vpcv1.LoadBalancerPoolMember
	for i := range members.Members {
		if matchesMember(&members.Members[i], instance, port) {
			if found != nil {
				return nil, fmt.Errorf("duplicate load balancer members for instance and port")
			}
			found = &members.Members[i]
		}
	}
	return found, nil
}
func isNotFound(err error) bool { return err != nil && ibm.ParseError(err).StatusCode == 404 }
