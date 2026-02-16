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
package cloudprovider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("NodeClassCircuitBreakerManager", func() {
	var (
		manager    *NodeClassCircuitBreakerManager
		config     *CircuitBreakerConfig
		ctx        = context.Background()
		nodeClassA = "test-nodeclass-a"
		nodeClassB = "test-nodeclass-b"
		region     = "us-east-1"
		testError  = errors.New("test provisioning error")
	)

	BeforeEach(func() {
		config = &CircuitBreakerConfig{
			FailureThreshold:       2, // Lower threshold for faster testing
			FailureWindow:          5 * time.Minute,
			RecoveryTimeout:        1 * time.Minute, // Shorter for testing
			HalfOpenMaxRequests:    1,
			RateLimitPerMinute:     100, // Allow enough for testing
			MaxConcurrentInstances: 10,  // Allow enough for testing
		}
		manager = NewNodeClassCircuitBreakerManager(config, logr.Discard())
	})

	Context("Per-NodeClass Isolation", func() {
		It("should isolate failures between different NodeClasses", func() {
			// NodeClass A fails twice - should open circuit breaker
			manager.RecordFailure(nodeClassA, region, testError)
			manager.RecordFailure(nodeClassA, region, testError)

			// NodeClass A should be blocked
			err := manager.CanProvision(ctx, nodeClassA, region, 0)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("circuit breaker OPEN"))

			// NodeClass B should still be allowed (not affected by A's failures)
			err = manager.CanProvision(ctx, nodeClassB, region, 0)
			Expect(err).ToNot(HaveOccurred())

			// Verify states
			stateA, err := manager.GetStateForNodeClass(nodeClassA, region)
			Expect(err).ToNot(HaveOccurred())
			Expect(stateA.State).To(Equal(CircuitBreakerOpen))

			stateB, err := manager.GetStateForNodeClass(nodeClassB, region)
			Expect(err).ToNot(HaveOccurred())
			Expect(stateB.State).To(Equal(CircuitBreakerClosed))
		})

		It("should isolate failures between different regions", func() {
			regionUS := "us-east-1"
			regionEU := "eu-west-1"

			// Same NodeClass, different regions - failures in US shouldn't affect EU
			manager.RecordFailure(nodeClassA, regionUS, testError)
			manager.RecordFailure(nodeClassA, regionUS, testError)

			// US region should be blocked
			err := manager.CanProvision(ctx, nodeClassA, regionUS, 0)
			Expect(err).To(HaveOccurred())

			// EU region should still be allowed
			err = manager.CanProvision(ctx, nodeClassA, regionEU, 0)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should create circuit breakers on-demand", func() {
			// Initially no circuit breakers should exist
			states, err := manager.GetState()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(states)).To(Equal(0))

			// First CanProvision call should create a circuit breaker
			err = manager.CanProvision(ctx, nodeClassA, region, 0)
			Expect(err).ToNot(HaveOccurred())

			states, err = manager.GetState()
			Expect(err).ToNot(HaveOccurred())
			Expect(len(states)).To(Equal(1))
			Expect(states).To(HaveKey("test-nodeclass-a-us-east-1"))
		})
	})

	Context("Circuit Breaker State Management", func() {
		It("should track individual NodeClass states correctly", func() {
			// Record one failure for NodeClass A
			manager.RecordFailure(nodeClassA, region, testError)

			stateA, err := manager.GetStateForNodeClass(nodeClassA, region)
			Expect(err).ToNot(HaveOccurred())
			Expect(stateA.RecentFailures).To(Equal(1))
			Expect(stateA.State).To(Equal(CircuitBreakerClosed))

			// Record second failure - should open circuit
			manager.RecordFailure(nodeClassA, region, testError)

			stateA, err = manager.GetStateForNodeClass(nodeClassA, region)
			Expect(err).ToNot(HaveOccurred())
			Expect(stateA.RecentFailures).To(Equal(2))
			Expect(stateA.State).To(Equal(CircuitBreakerOpen))
		})

		It("should handle success after failures correctly", func() {
			// Record failures to get into half-open state
			manager.RecordFailure(nodeClassA, region, testError)
			manager.RecordFailure(nodeClassA, region, testError)

			// Circuit should be open
			state, err := manager.GetStateForNodeClass(nodeClassA, region)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.State).To(Equal(CircuitBreakerOpen))

			// Wait for recovery timeout and check half-open transition
			config.RecoveryTimeout = 100 * time.Millisecond
			time.Sleep(150 * time.Millisecond)

			// Should allow one request in half-open state
			err = manager.CanProvision(ctx, nodeClassA, region, 0)
			Expect(err).ToNot(HaveOccurred())

			// Record success - should close circuit
			manager.RecordSuccess(nodeClassA, region)

			state, err = manager.GetStateForNodeClass(nodeClassA, region)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.State).To(Equal(CircuitBreakerClosed))
		})
	})

	Context("Management Operations", func() {
		It("should list open NodeClasses correctly", func() {
			// Open circuit breakers for multiple NodeClasses
			manager.RecordFailure(nodeClassA, region, testError)
			manager.RecordFailure(nodeClassA, region, testError)
			manager.RecordFailure(nodeClassB, region, testError)
			manager.RecordFailure(nodeClassB, region, testError)

			openBreakers := manager.GetOpenNodeClasses()
			Expect(len(openBreakers)).To(Equal(2))
			Expect(openBreakers).To(ContainElement("test-nodeclass-a-us-east-1"))
			Expect(openBreakers).To(ContainElement("test-nodeclass-b-us-east-1"))
		})

		It("should reset specific NodeClass circuit breakers", func() {
			// Open circuit breaker
			manager.RecordFailure(nodeClassA, region, testError)
			manager.RecordFailure(nodeClassA, region, testError)

			// Verify it's open
			state, err := manager.GetStateForNodeClass(nodeClassA, region)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.State).To(Equal(CircuitBreakerOpen))

			// Reset the circuit breaker
			err = manager.ResetNodeClass(nodeClassA, region)
			Expect(err).ToNot(HaveOccurred())

			// Should be closed now
			state, err = manager.GetStateForNodeClass(nodeClassA, region)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.State).To(Equal(CircuitBreakerClosed))
			Expect(state.RecentFailures).To(Equal(0))
		})

		It("should provide comprehensive stats", func() {
			// Create different states
			manager.RecordFailure(nodeClassA, region, testError)
			manager.RecordFailure(nodeClassA, region, testError) // Open

			// NodeClass B - one failure (closed)
			manager.RecordFailure(nodeClassB, region, testError)

			stats := manager.GetStats()
			Expect(stats["enabled"]).To(BeTrue())
			Expect(stats["total_breakers"]).To(Equal(2))
			Expect(stats["open_breakers"]).To(Equal(1))
			Expect(stats["closed_breakers"]).To(Equal(1))
		})
	})

	Context("Disabled Circuit Breaker", func() {
		BeforeEach(func() {
			// Create manager with nil config (disabled)
			manager = NewNodeClassCircuitBreakerManager(nil, logr.Discard())
		})

		It("should always allow provisioning when disabled", func() {
			// Should allow provisioning even with recorded failures
			err := manager.CanProvision(ctx, nodeClassA, region, 0)
			Expect(err).ToNot(HaveOccurred())

			manager.RecordFailure(nodeClassA, region, testError)
			manager.RecordFailure(nodeClassA, region, testError)
			manager.RecordFailure(nodeClassA, region, testError)

			// Should still allow provisioning
			err = manager.CanProvision(ctx, nodeClassA, region, 0)
			Expect(err).ToNot(HaveOccurred())

			stats := manager.GetStats()
			Expect(stats["enabled"]).To(BeFalse())
		})
	})

	Context("Cleanup", func() {
		It("should clean up unused circuit breakers", func() {
			// Set very short cleanup interval for testing
			manager.cleanupInterval = 100 * time.Millisecond

			// Create a circuit breaker and make it old
			manager.RecordFailure(nodeClassA, region, testError)

			// Get the breaker and artificially age it
			key := manager.getKey(nodeClassA, region)
			manager.mu.Lock()
			breaker := manager.breakers[key]
			// Simulate old last state change
			breaker.lastStateChange = time.Now().Add(-3 * time.Hour)
			manager.mu.Unlock()

			// Wait for cleanup interval
			time.Sleep(150 * time.Millisecond)

			// Trigger cleanup by calling CanProvision for a different NodeClass
			_ = manager.CanProvision(ctx, nodeClassB, region, 0)

			// Old breaker should be cleaned up
			states, err := manager.GetState()
			Expect(err).ToNot(HaveOccurred())
			Expect(states).ToNot(HaveKey(key))
			Expect(len(states)).To(Equal(1)) // Only nodeClassB should remain
		})
	})
})

func TestNodeClassCircuitBreakerManager(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "NodeClassCircuitBreakerManager Suite")
}
