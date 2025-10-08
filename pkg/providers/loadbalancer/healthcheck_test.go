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
	"testing"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestHealthCheck(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "HealthCheck Test Suite")
}

var _ = Describe("HealthCheckManager", func() {
	var (
		ctx        context.Context
		manager    *HealthCheckManager
		mockClient *MockLoadBalancerVPCClient
		target     v1alpha1.LoadBalancerTarget
		poolID     string
	)

	BeforeEach(func() {
		ctx = context.Background()
		mockClient = &MockLoadBalancerVPCClient{}
		manager = NewHealthCheckManager(mockClient, logr.Discard())
		poolID = "pool-123"
		target = v1alpha1.LoadBalancerTarget{
			LoadBalancerID: "lb-123",
			PoolName:       "test-pool",
			Port:           80,
		}
	})

	Describe("ConfigureHealthCheck", func() {
		Context("when health check configuration is nil", func() {
			It("should return without error", func() {
				target.HealthCheck = nil
				err := manager.ConfigureHealthCheck(ctx, target, poolID)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("when health check configuration is provided", func() {
			BeforeEach(func() {
				target.HealthCheck = &v1alpha1.LoadBalancerHealthCheck{
					Protocol:   "http",
					Path:       "/health",
					Interval:   int32Ptr(30),
					Timeout:    int32Ptr(5),
					RetryCount: int32Ptr(3),
				}
			})

			It("should configure health check successfully", func() {
				existingPool := &vpcv1.LoadBalancerPool{
					ID:       stringPtr(poolID),
					Protocol: stringPtr("tcp"),
				}

				mockClient.GetLoadBalancerPoolFunc = func(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPool, error) {
					Expect(loadBalancerID).To(Equal("lb-123"))
					Expect(poolID).To(Equal("pool-123"))
					return existingPool, nil
				}

				var capturedPatch map[string]interface{}
				mockClient.UpdateLoadBalancerPoolFunc = func(ctx context.Context, loadBalancerID, poolID string, patchMap map[string]interface{}) (*vpcv1.LoadBalancerPool, error) {
					capturedPatch = patchMap
					return existingPool, nil
				}

				err := manager.ConfigureHealthCheck(ctx, target, poolID)
				Expect(err).NotTo(HaveOccurred())

				// Verify patch contains expected health monitor configuration
				Expect(capturedPatch).To(HaveKey("health_monitor"))
				healthMonitor := capturedPatch["health_monitor"].(map[string]interface{})
				Expect(healthMonitor["delay"]).To(Equal(int64(30)))
				Expect(healthMonitor["timeout"]).To(Equal(int64(5)))
				Expect(healthMonitor["max_retries"]).To(Equal(int64(3)))
				Expect(healthMonitor["type"]).To(Equal("http"))
				Expect(healthMonitor["url_path"]).To(Equal("/health"))
			})

			It("should handle GetLoadBalancerPool error", func() {
				mockClient.GetLoadBalancerPoolFunc = func(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPool, error) {
					return nil, fmt.Errorf("pool not found")
				}

				err := manager.ConfigureHealthCheck(ctx, target, poolID)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("getting load balancer pool"))
			})

			It("should handle UpdateLoadBalancerPool error", func() {
				existingPool := &vpcv1.LoadBalancerPool{
					ID:       stringPtr(poolID),
					Protocol: stringPtr("tcp"),
				}

				mockClient.GetLoadBalancerPoolFunc = func(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPool, error) {
					return existingPool, nil
				}

				mockClient.UpdateLoadBalancerPoolFunc = func(ctx context.Context, loadBalancerID, poolID string, patchMap map[string]interface{}) (*vpcv1.LoadBalancerPool, error) {
					return nil, fmt.Errorf("update failed")
				}

				err := manager.ConfigureHealthCheck(ctx, target, poolID)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("updating load balancer pool health check"))
			})
		})
	})

	Describe("buildHealthCheckPatch", func() {
		It("should build patch with default values", func() {
			healthCheck := &v1alpha1.LoadBalancerHealthCheck{}
			currentPool := &vpcv1.LoadBalancerPool{
				Protocol: stringPtr("tcp"),
			}

			needsUpdate, patch := manager.buildHealthCheckPatch(healthCheck, currentPool)
			Expect(needsUpdate).To(BeTrue())

			healthMonitor := patch["health_monitor"].(map[string]interface{})
			Expect(healthMonitor["delay"]).To(Equal(int64(30)))
			Expect(healthMonitor["timeout"]).To(Equal(int64(5)))
			Expect(healthMonitor["max_retries"]).To(Equal(int64(2)))
			Expect(healthMonitor["type"]).To(Equal("tcp"))
		})

		It("should build patch with custom values", func() {
			healthCheck := &v1alpha1.LoadBalancerHealthCheck{
				Protocol:   "https",
				Path:       "/status",
				Interval:   int32Ptr(60),
				Timeout:    int32Ptr(10),
				RetryCount: int32Ptr(5),
			}
			currentPool := &vpcv1.LoadBalancerPool{
				Protocol: stringPtr("http"),
			}

			needsUpdate, patch := manager.buildHealthCheckPatch(healthCheck, currentPool)
			Expect(needsUpdate).To(BeTrue())

			Expect(patch["protocol"]).To(Equal("https"))
			healthMonitor := patch["health_monitor"].(map[string]interface{})
			Expect(healthMonitor["delay"]).To(Equal(int64(60)))
			Expect(healthMonitor["timeout"]).To(Equal(int64(10)))
			Expect(healthMonitor["max_retries"]).To(Equal(int64(5)))
			Expect(healthMonitor["type"]).To(Equal("https"))
			Expect(healthMonitor["url_path"]).To(Equal("/status"))
		})

		It("should include URL path for HTTP/HTTPS protocols", func() {
			healthCheck := &v1alpha1.LoadBalancerHealthCheck{
				Protocol: "http",
				Path:     "/api/health",
			}
			currentPool := &vpcv1.LoadBalancerPool{}

			_, patch := manager.buildHealthCheckPatch(healthCheck, currentPool)

			healthMonitor := patch["health_monitor"].(map[string]interface{})
			Expect(healthMonitor["url_path"]).To(Equal("/api/health"))
		})

		It("should not include URL path for TCP protocol", func() {
			healthCheck := &v1alpha1.LoadBalancerHealthCheck{
				Protocol: "tcp",
				Path:     "/health", // Should be ignored for TCP
			}
			currentPool := &vpcv1.LoadBalancerPool{}

			_, patch := manager.buildHealthCheckPatch(healthCheck, currentPool)

			healthMonitor := patch["health_monitor"].(map[string]interface{})
			Expect(healthMonitor).NotTo(HaveKey("url_path"))
		})
	})

	Describe("ValidateHealthCheck", func() {
		Context("when health check is nil", func() {
			It("should return no error", func() {
				err := manager.ValidateHealthCheck(nil)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("when validating protocol", func() {
			It("should accept valid protocols", func() {
				validProtocols := []string{"", "tcp", "http", "https"}
				for _, protocol := range validProtocols {
					healthCheck := &v1alpha1.LoadBalancerHealthCheck{
						Protocol: protocol,
					}
					if protocol == "http" || protocol == "https" {
						healthCheck.Path = "/health"
					}
					err := manager.ValidateHealthCheck(healthCheck)
					Expect(err).NotTo(HaveOccurred(), "Protocol %s should be valid", protocol)
				}
			})

			It("should reject invalid protocols", func() {
				healthCheck := &v1alpha1.LoadBalancerHealthCheck{
					Protocol: "invalid",
				}
				err := manager.ValidateHealthCheck(healthCheck)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid health check protocol"))
			})
		})

		Context("when validating path", func() {
			It("should require path for HTTP/HTTPS protocols", func() {
				protocols := []string{"http", "https"}
				for _, protocol := range protocols {
					healthCheck := &v1alpha1.LoadBalancerHealthCheck{
						Protocol: protocol,
						Path:     "",
					}
					err := manager.ValidateHealthCheck(healthCheck)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("path is required for HTTP/HTTPS health checks"))
				}
			})

			It("should accept valid paths", func() {
				healthCheck := &v1alpha1.LoadBalancerHealthCheck{
					Protocol: "http",
					Path:     "/health",
				}
				err := manager.ValidateHealthCheck(healthCheck)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should reject invalid paths", func() {
				healthCheck := &v1alpha1.LoadBalancerHealthCheck{
					Protocol: "http",
					Path:     "invalid-path", // Should start with /
				}
				err := manager.ValidateHealthCheck(healthCheck)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid health check path"))
			})
		})

		Context("when validating ranges", func() {
			It("should validate interval range", func() {
				// Valid interval
				healthCheck := &v1alpha1.LoadBalancerHealthCheck{
					Interval: int32Ptr(30),
				}
				err := manager.ValidateHealthCheck(healthCheck)
				Expect(err).NotTo(HaveOccurred())

				// Invalid intervals
				invalidIntervals := []int32{4, 301}
				for _, interval := range invalidIntervals {
					healthCheck.Interval = &interval
					err := manager.ValidateHealthCheck(healthCheck)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("health check interval must be between 5 and 300 seconds"))
				}
			})

			It("should validate timeout range", func() {
				// Valid timeout
				healthCheck := &v1alpha1.LoadBalancerHealthCheck{
					Timeout: int32Ptr(10),
				}
				err := manager.ValidateHealthCheck(healthCheck)
				Expect(err).NotTo(HaveOccurred())

				// Invalid timeouts
				invalidTimeouts := []int32{0, 61}
				for _, timeout := range invalidTimeouts {
					healthCheck.Timeout = &timeout
					err := manager.ValidateHealthCheck(healthCheck)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("health check timeout must be between 1 and 60 seconds"))
				}
			})

			It("should validate retry count range", func() {
				// Valid retry count
				healthCheck := &v1alpha1.LoadBalancerHealthCheck{
					RetryCount: int32Ptr(3),
				}
				err := manager.ValidateHealthCheck(healthCheck)
				Expect(err).NotTo(HaveOccurred())

				// Invalid retry counts
				invalidRetryCounts := []int32{0, 11}
				for _, retryCount := range invalidRetryCounts {
					healthCheck.RetryCount = &retryCount
					err := manager.ValidateHealthCheck(healthCheck)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("health check retry count must be between 1 and 10"))
				}
			})

			It("should validate timeout is less than interval", func() {
				healthCheck := &v1alpha1.LoadBalancerHealthCheck{
					Interval: int32Ptr(10),
					Timeout:  int32Ptr(10), // Same as interval
				}
				err := manager.ValidateHealthCheck(healthCheck)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("health check timeout (10) must be less than interval (10)"))

				healthCheck.Timeout = int32Ptr(15) // Greater than interval
				err = manager.ValidateHealthCheck(healthCheck)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("health check timeout (15) must be less than interval (10)"))
			})
		})
	})

	Describe("isValidPath", func() {
		It("should validate path formats", func() {
			validPaths := []string{"/", "/health", "/api/v1/status"}
			for _, path := range validPaths {
				Expect(isValidPath(path)).To(BeTrue(), "Path %s should be valid", path)
			}

			invalidPaths := []string{"", "health", "api/health"}
			for _, path := range invalidPaths {
				Expect(isValidPath(path)).To(BeFalse(), "Path %s should be invalid", path)
			}
		})
	})
})

// MockLoadBalancerVPCClient for testing
type MockLoadBalancerVPCClient struct {
	GetLoadBalancerPoolFunc    func(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPool, error)
	UpdateLoadBalancerPoolFunc func(ctx context.Context, loadBalancerID, poolID string, patchMap map[string]interface{}) (*vpcv1.LoadBalancerPool, error)
}

func (m *MockLoadBalancerVPCClient) GetLoadBalancerPool(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPool, error) {
	if m.GetLoadBalancerPoolFunc != nil {
		return m.GetLoadBalancerPoolFunc(ctx, loadBalancerID, poolID)
	}
	return nil, nil
}

func (m *MockLoadBalancerVPCClient) UpdateLoadBalancerPool(ctx context.Context, loadBalancerID, poolID string, patchMap map[string]interface{}) (*vpcv1.LoadBalancerPool, error) {
	if m.UpdateLoadBalancerPoolFunc != nil {
		return m.UpdateLoadBalancerPoolFunc(ctx, loadBalancerID, poolID, patchMap)
	}
	return nil, nil
}

// Implement other required methods with no-op implementations
func (m *MockLoadBalancerVPCClient) GetLoadBalancer(ctx context.Context, loadBalancerID string) (*vpcv1.LoadBalancer, error) {
	return nil, nil
}

func (m *MockLoadBalancerVPCClient) ListLoadBalancerPools(ctx context.Context, loadBalancerID string) (*vpcv1.LoadBalancerPoolCollection, error) {
	return nil, nil
}

func (m *MockLoadBalancerVPCClient) CreateLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID string, target vpcv1.LoadBalancerPoolMemberTargetPrototypeIntf, port int64, weight int64) (*vpcv1.LoadBalancerPoolMember, error) {
	return nil, nil
}

func (m *MockLoadBalancerVPCClient) DeleteLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID, memberID string) error {
	return nil
}

func (m *MockLoadBalancerVPCClient) ListLoadBalancerPoolMembers(ctx context.Context, loadBalancerID, poolID string) (*vpcv1.LoadBalancerPoolMemberCollection, error) {
	return nil, nil
}

func (m *MockLoadBalancerVPCClient) GetLoadBalancerPoolMember(ctx context.Context, loadBalancerID, poolID, memberID string) (*vpcv1.LoadBalancerPoolMember, error) {
	return nil, nil
}

// Helper functions
func stringPtr(s string) *string {
	return &s
}

func int32Ptr(i int32) *int32 {
	return &i
}
