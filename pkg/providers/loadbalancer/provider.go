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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-logr/logr"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	ibmclient "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

// LoadBalancerVPCClient defines the VPC client methods needed for load balancer operations
type LoadBalancerVPCClient interface {
	GetLoadBalancer(ctx context.Context, loadBalancerID string) (*vpcv1.LoadBalancer, error)
	ListLoadBalancerPools(ctx context.Context, loadBalancerID string) (*vpcv1.LoadBalancerPoolCollection, error)
	GetLoadBalancerPool(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPool, error)
	UpdateLoadBalancerPool(ctx context.Context, loadBalancerID, poolID string, updates map[string]interface{}) (*vpcv1.LoadBalancerPool, error)
	CreateLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID string, target vpcv1.LoadBalancerPoolMemberTargetPrototypeIntf, port int64, weight int64) (*vpcv1.LoadBalancerPoolMember, error)
	DeleteLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID, memberID string) error
	GetLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID, memberID string) (*vpcv1.LoadBalancerPoolMember, error)
	ListLoadBalancerPoolMembers(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPoolMemberCollection, error)
}

// LoadBalancerProvider handles load balancer integration for IBM Cloud instances
type LoadBalancerProvider struct {
	vpcClient LoadBalancerVPCClient
	logger    logr.Logger
	accountID func(context.Context) (string, error)
}

// NewLoadBalancerProvider creates a new load balancer provider instance
func NewLoadBalancerProvider(vpcClient LoadBalancerVPCClient, logger logr.Logger) *LoadBalancerProvider {
	return &LoadBalancerProvider{
		vpcClient: vpcClient,
		logger:    logger.WithName("loadbalancer"),
	}
}

// NewLoadBalancerProviderWithIBMClient creates a new load balancer provider with IBM VPC client
func NewLoadBalancerProviderWithIBMClient(vpcClient *ibmclient.VPCClient, logger logr.Logger) *LoadBalancerProvider {
	return &LoadBalancerProvider{
		vpcClient: vpcClient,
		logger:    logger.WithName("loadbalancer"),
		accountID: vpcClient.ResolveAccountID,
	}
}

// RegisterInstance registers an instance with configured load balancer target groups
func (p *LoadBalancerProvider) RegisterInstance(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID string, instanceIP string) error {
	if nodeClass.Spec.LoadBalancerIntegration == nil || !nodeClass.Spec.LoadBalancerIntegration.Enabled {
		return nil
	}

	logger := p.logger.WithValues("instanceID", instanceID, "instanceIP", instanceIP)
	logger.Info("Started load balancer registration")

	targets := nodeClass.Spec.LoadBalancerIntegration.TargetGroups
	var errs []error
	for i, target := range targets {
		targetLogger := logger.WithValues("loadBalancerID", target.LoadBalancerID, "poolName", target.PoolName, "port", target.Port)

		if err := p.registerInstanceInTarget(ctx, target, instanceID, instanceIP, targetLogger, nodeClass.Spec.LoadBalancerIntegration.RegistrationTimeout); err != nil {
			errs = append(errs, err)
			targetLogger.Error(err, "Failed to register instance in target group")
			continue
		}

		targetLogger.Info("Successfully registered instance in target group", "targetIndex", i)
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to register instance in %d/%d targets: %w", len(errs), len(targets), errors.Join(errs...))
	}
	return nil
}

// DeregisterInstance removes an instance from all configured load balancer target groups
func (p *LoadBalancerProvider) DeregisterInstance(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID string) error {
	if nodeClass.Spec.LoadBalancerIntegration == nil || !nodeClass.Spec.LoadBalancerIntegration.Enabled {
		return nil
	}

	autoDeregister := true
	if nodeClass.Spec.LoadBalancerIntegration.AutoDeregister != nil {
		autoDeregister = *nodeClass.Spec.LoadBalancerIntegration.AutoDeregister
	}

	if !autoDeregister {
		p.logger.Info("Skipped auto-deregistration as it was disabled", "instanceID", instanceID)
		return nil
	}

	logger := p.logger.WithValues("instanceID", instanceID)
	logger.Info("Started load balancer deregistration")

	targets := nodeClass.Spec.LoadBalancerIntegration.TargetGroups
	var errs []error
	for i, target := range targets {
		targetLogger := logger.WithValues("loadBalancerID", target.LoadBalancerID, "poolName", target.PoolName)

		if err := p.deregisterInstanceFromTarget(ctx, target, instanceID, targetLogger); err != nil {
			errs = append(errs, err)
			targetLogger.Error(err, "Failed to deregister instance from target group")
			continue
		}

		targetLogger.Info("Successfully deregistered instance from target group", "targetIndex", i)
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to deregister instance from %d/%d targets: %w", len(errs), len(targets), errors.Join(errs...))
	}
	return nil
}

// registerInstanceInTarget registers an instance in a specific load balancer target group
func (p *LoadBalancerProvider) registerInstanceInTarget(ctx context.Context, target v1alpha1.LoadBalancerTarget, instanceID string, instanceIP string, logger logr.Logger, timeout *int32) error {
	// Find the pool by name
	poolID, err := p.findPoolByName(ctx, target.LoadBalancerID, target.PoolName)
	if err != nil {
		return fmt.Errorf("finding pool by name: %w", err)
	}

	member, err := p.findMember(ctx, target.LoadBalancerID, poolID, instanceID, int64(target.Port))
	if err != nil {
		return err
	}
	if healthErr := NewHealthCheckManager(p.vpcClient, p.logger).ConfigureHealthCheck(ctx, target, poolID); healthErr != nil {
		return healthErr
	}
	if member == nil {
		weight := int64(50)
		if target.Weight != nil {
			weight = int64(*target.Weight)
		}
		member, err = p.vpcClient.CreateLoadBalancerPoolMember(ctx, target.LoadBalancerID, poolID, &vpcv1.LoadBalancerPoolMemberTargetPrototypeInstanceIdentity{ID: &instanceID}, int64(target.Port), weight)
		if err != nil {
			return fmt.Errorf("creating load balancer pool member: %w", err)
		}
	}
	if member == nil || member.ID == nil {
		return fmt.Errorf("load balancer member has no identity")
	}
	if timeout != nil {
		return p.waitForMemberHealthy(ctx, target.LoadBalancerID, poolID, *member.ID, time.Duration(*timeout)*time.Second, logger)
	}

	return nil
}

// deregisterInstanceFromTarget removes an instance from a specific load balancer target group
func (p *LoadBalancerProvider) deregisterInstanceFromTarget(ctx context.Context, target v1alpha1.LoadBalancerTarget, instanceID string, logger logr.Logger) error {
	// Find the pool by name
	poolID, err := p.findPoolByName(ctx, target.LoadBalancerID, target.PoolName)
	if err != nil {
		return fmt.Errorf("finding pool by name: %w", err)
	}

	// Find the member by instance ID
	memberID, err := p.findMemberByInstanceID(ctx, target.LoadBalancerID, poolID, instanceID)
	if err != nil {
		return fmt.Errorf("finding member by instance ID: %w", err)
	}

	if memberID == "" {
		logger.Info("Skipped deregistration as instance was not found in pool")
		return nil
	}

	// Delete the pool member
	err = p.vpcClient.DeleteLoadBalancerPoolMember(ctx, target.LoadBalancerID, poolID, memberID)
	if err != nil {
		return fmt.Errorf("deleting load balancer pool member: %w", err)
	}

	logger.Info("Deleted load balancer pool member", "memberID", memberID)
	return nil
}

// findPoolByName finds a load balancer pool by name
func (p *LoadBalancerProvider) findPoolByName(ctx context.Context, loadBalancerID, poolName string) (string, error) {
	pools, err := p.vpcClient.ListLoadBalancerPools(ctx, loadBalancerID)
	if err != nil {
		return "", fmt.Errorf("listing load balancer pools: %w", err)
	}

	if pools == nil {
		return "", fmt.Errorf("load balancer pool collection is missing")
	}
	found := ""
	for _, pool := range pools.Pools {
		if pool.Name != nil && *pool.Name == poolName {
			if pool.ID == nil || found != "" {
				return "", fmt.Errorf("load balancer pool identity is missing or ambiguous")
			}
			found = *pool.ID
		}
	}
	if found != "" {
		return found, nil
	}

	return "", fmt.Errorf("pool with name %s not found in load balancer %s", poolName, loadBalancerID)
}

// findMemberByInstanceID finds a pool member by instance ID
func (p *LoadBalancerProvider) findMemberByInstanceID(ctx context.Context, loadBalancerID, poolID, instanceID string) (string, error) {
	members, err := p.vpcClient.ListLoadBalancerPoolMembers(ctx, loadBalancerID, poolID)
	if err != nil {
		return "", fmt.Errorf("listing load balancer pool members: %w", err)
	}

	for _, member := range members.Members {
		if member.Target != nil {
			// Check if target is an instance identity
			if instanceTarget, ok := member.Target.(*vpcv1.LoadBalancerPoolMemberTarget); ok {
				if instanceTarget.ID != nil && *instanceTarget.ID == instanceID {
					return *member.ID, nil
				}
			}
		}
	}

	return "", nil // Not found, but not an error
}

// waitForMemberHealthy waits for a pool member to become healthy
func (p *LoadBalancerProvider) waitForMemberHealthy(ctx context.Context, loadBalancerID, poolID, memberID string, timeout time.Duration, logger logr.Logger) error {
	logger.Info("Started waiting for pool member to become healthy", "memberID", memberID, "timeout", timeout)

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		member, err := p.vpcClient.GetLoadBalancerPoolMember(timeoutCtx, loadBalancerID, poolID, memberID)
		if err == nil && member != nil && member.Health != nil && strings.EqualFold(*member.Health, "ok") {
			return nil
		}
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("waiting for pool member %s health: %w", memberID, timeoutCtx.Err())
		case <-ticker.C:
		}
	}

}

// ValidateLoadBalancerConfiguration validates the load balancer configuration
func (p *LoadBalancerProvider) ValidateLoadBalancerConfiguration(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass) error {
	if nodeClass.Spec.LoadBalancerIntegration == nil || !nodeClass.Spec.LoadBalancerIntegration.Enabled {
		return nil
	}

	for i, target := range nodeClass.Spec.LoadBalancerIntegration.TargetGroups {
		// Validate load balancer exists
		_, err := p.vpcClient.GetLoadBalancer(ctx, target.LoadBalancerID)
		if err != nil {
			return fmt.Errorf("target group %d: load balancer %s not found: %w", i, target.LoadBalancerID, err)
		}

		// Validate pool exists
		_, err = p.findPoolByName(ctx, target.LoadBalancerID, target.PoolName)
		if err != nil {
			return fmt.Errorf("target group %d: %w", i, err)
		}
	}

	return nil
}
