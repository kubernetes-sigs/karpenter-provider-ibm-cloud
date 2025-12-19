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
	"fmt"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-logr/logr"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// HealthCheckManager manages health check configuration for load balancer pools
type HealthCheckManager struct {
	vpcClient LoadBalancerVPCClient
	logger    logr.Logger
}

// NewHealthCheckManager creates a new health check manager
func NewHealthCheckManager(vpcClient LoadBalancerVPCClient, logger logr.Logger) *HealthCheckManager {
	return &HealthCheckManager{
		vpcClient: vpcClient,
		logger:    logger.WithName("healthcheck"),
	}
}

// ConfigureHealthCheck configures health check settings for a load balancer pool
func (hc *HealthCheckManager) ConfigureHealthCheck(ctx context.Context, target v1alpha1.LoadBalancerTarget, poolID string) error {
	if target.HealthCheck == nil {
		hc.logger.Info("No health check configuration specified, using pool defaults", "poolID", poolID)
		return nil
	}

	logger := hc.logger.WithValues("loadBalancerID", target.LoadBalancerID, "poolID", poolID, "poolName", target.PoolName)
	logger.Info("Configuring health check for pool")

	// Get current pool configuration
	pool, err := hc.vpcClient.GetLoadBalancerPool(ctx, target.LoadBalancerID, poolID)
	if err != nil {
		return fmt.Errorf("getting load balancer pool: %w", err)
	}

	// Check if health check configuration needs updating
	needsUpdate, patchMap := hc.buildHealthCheckPatch(target.HealthCheck, pool)
	if !needsUpdate {
		logger.Info("Health check configuration already matches desired state")
		return nil
	}

	// Update the pool with new health check configuration
	_, err = hc.vpcClient.UpdateLoadBalancerPool(ctx, target.LoadBalancerID, poolID, patchMap)
	if err != nil {
		return fmt.Errorf("updating load balancer pool health check: %w", err)
	}

	logger.Info("Successfully updated health check configuration")
	return nil
}

// buildHealthCheckPatch creates a patch for updating health check configuration
func (hc *HealthCheckManager) buildHealthCheckPatch(desired *v1alpha1.LoadBalancerHealthCheck, current *vpcv1.LoadBalancerPool) (bool, map[string]interface{}) {
	patch := make(map[string]interface{})

	// Set defaults for health check configuration
	protocol := "tcp"
	if desired.Protocol != "" {
		protocol = desired.Protocol
	}

	interval := int64(30)
	if desired.Interval != nil {
		interval = int64(*desired.Interval)
	}

	timeout := int64(5)
	if desired.Timeout != nil {
		timeout = int64(*desired.Timeout)
	}

	retryCount := int64(2)
	if desired.RetryCount != nil {
		retryCount = int64(*desired.RetryCount)
	}

	// Check protocol
	if current.Protocol == nil || *current.Protocol != protocol {
		patch["protocol"] = protocol
	}

	// Create health monitor configuration
	healthMonitor := map[string]interface{}{
		"delay":       interval,
		"max_retries": retryCount,
		"timeout":     timeout,
		"type":        protocol,
	}

	// Handle HTTP/HTTPS specific configuration
	if (protocol == "http" || protocol == "https") && desired.Path != "" {
		healthMonitor["url_path"] = desired.Path
	}

	// For simplicity, always update health monitor if we have a desired config
	patch["health_monitor"] = healthMonitor

	return true, patch
}

// ValidateHealthCheck validates health check configuration
func (hc *HealthCheckManager) ValidateHealthCheck(healthCheck *v1alpha1.LoadBalancerHealthCheck) error {
	if healthCheck == nil {
		return nil
	}

	// Validate protocol
	switch healthCheck.Protocol {
	case "", "tcp", "http", "https":
		// Valid protocols
	default:
		return fmt.Errorf("invalid health check protocol: %s", healthCheck.Protocol)
	}

	// Validate that path is provided for HTTP/HTTPS
	if (healthCheck.Protocol == "http" || healthCheck.Protocol == "https") && healthCheck.Path == "" {
		return fmt.Errorf("path is required for HTTP/HTTPS health checks")
	}

	// Validate path format for HTTP/HTTPS
	if healthCheck.Path != "" && !isValidPath(healthCheck.Path) {
		return fmt.Errorf("invalid health check path: %s", healthCheck.Path)
	}

	// Validate ranges
	if healthCheck.Interval != nil && (*healthCheck.Interval < 5 || *healthCheck.Interval > 300) {
		return fmt.Errorf("health check interval must be between 5 and 300 seconds")
	}

	if healthCheck.Timeout != nil && (*healthCheck.Timeout < 1 || *healthCheck.Timeout > 60) {
		return fmt.Errorf("health check timeout must be between 1 and 60 seconds")
	}

	if healthCheck.RetryCount != nil && (*healthCheck.RetryCount < 1 || *healthCheck.RetryCount > 10) {
		return fmt.Errorf("health check retry count must be between 1 and 10")
	}

	// Validate that timeout is less than interval
	if healthCheck.Interval != nil && healthCheck.Timeout != nil && *healthCheck.Timeout >= *healthCheck.Interval {
		return fmt.Errorf("health check timeout (%d) must be less than interval (%d)", *healthCheck.Timeout, *healthCheck.Interval)
	}

	return nil
}

// Helper functions

func isValidPath(path string) bool {
	if len(path) == 0 {
		return false
	}
	if path[0] != '/' {
		return false
	}
	// Additional path validation can be added here
	return true
}
