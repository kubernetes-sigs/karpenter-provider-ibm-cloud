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
package events

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func TestNodeClaimFailedToResolveNodeClass(t *testing.T) {
	tests := []struct {
		name      string
		nodeClaim *karpv1.NodeClaim
		expected  string
	}{
		{
			name: "valid NodeClaim",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclaim",
				},
			},
			expected: "Failed to resolve NodeClass for NodeClaim test-nodeclaim",
		},
		{
			name: "NodeClaim with empty name",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "",
				},
			},
			expected: "Failed to resolve NodeClass for NodeClaim ",
		},
		{
			name: "NodeClaim with namespace",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nodeclaim",
					Namespace: "karpenter",
				},
			},
			expected: "Failed to resolve NodeClass for NodeClaim test-nodeclaim",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := NodeClaimFailedToResolveNodeClass(tt.nodeClaim)

			assert.Equal(t, corev1.EventTypeWarning, event.Type)
			assert.Equal(t, "FailedToResolveNodeClass", event.Reason)
			assert.Equal(t, tt.expected, event.Message)
		})
	}
}

func TestNodePoolFailedToResolveNodeClass(t *testing.T) {
	tests := []struct {
		name     string
		nodePool *karpv1.NodePool
		expected string
	}{
		{
			name: "valid NodePool",
			nodePool: &karpv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodepool",
				},
			},
			expected: "Failed to resolve NodeClass for NodePool test-nodepool",
		},
		{
			name: "NodePool with empty name",
			nodePool: &karpv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: "",
				},
			},
			expected: "Failed to resolve NodeClass for NodePool ",
		},
		{
			name: "NodePool with namespace",
			nodePool: &karpv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nodepool",
					Namespace: "karpenter",
				},
			},
			expected: "Failed to resolve NodeClass for NodePool test-nodepool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := NodePoolFailedToResolveNodeClass(tt.nodePool)

			assert.Equal(t, corev1.EventTypeWarning, event.Type)
			assert.Equal(t, "FailedToResolveNodeClass", event.Reason)
			assert.Equal(t, tt.expected, event.Message)
		})
	}
}

func TestEventStructure(t *testing.T) {
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclaim",
		},
	}

	event := NodeClaimFailedToResolveNodeClass(nodeClaim)

	// Ensure all required fields are set
	assert.NotEmpty(t, event.Type)
	assert.NotEmpty(t, event.Reason)
	assert.NotEmpty(t, event.Message)
	assert.NotNil(t, event.InvolvedObject)
	assert.Equal(t, nodeClaim, event.InvolvedObject)

	// Ensure event type is valid
	assert.Contains(t, []string{corev1.EventTypeNormal, corev1.EventTypeWarning}, event.Type)
}

func TestNilObjectHandling(t *testing.T) {
	// These functions should handle nil objects gracefully without panicking
	assert.NotPanics(t, func() {
		event := NodeClaimFailedToResolveNodeClass(nil)
		assert.Equal(t, corev1.EventTypeWarning, event.Type)
		assert.Equal(t, "FailedToResolveNodeClass", event.Reason)
		assert.Contains(t, event.Message, "Failed to resolve NodeClass for NodeClaim <unknown>")
		assert.Nil(t, event.InvolvedObject)
	})

	assert.NotPanics(t, func() {
		event := NodePoolFailedToResolveNodeClass(nil)
		assert.Equal(t, corev1.EventTypeWarning, event.Type)
		assert.Equal(t, "FailedToResolveNodeClass", event.Reason)
		assert.Contains(t, event.Message, "Failed to resolve NodeClass for NodePool <unknown>")
		assert.Nil(t, event.InvolvedObject)
	})
}

func TestNodeClaimCircuitBreakerBlocked(t *testing.T) {
	tests := []struct {
		name      string
		nodeClaim *karpv1.NodeClaim
		reason    string
		expected  string
	}{
		{
			name: "NodeClaim with name",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclaim",
				},
			},
			reason:   "circuit breaker OPEN",
			expected: "Circuit breaker blocked provisioning for NodeClaim test-nodeclaim: circuit breaker OPEN",
		},
		{
			name:      "NodeClaim is nil",
			nodeClaim: nil,
			reason:    "rate limit exceeded",
			expected:  "Circuit breaker blocked provisioning for NodeClaim <unknown>: rate limit exceeded",
		},
		{
			name: "NodeClaim with empty name",
			nodeClaim: &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "",
				},
			},
			reason:   "concurrency limit exceeded",
			expected: "Circuit breaker blocked provisioning for NodeClaim : concurrency limit exceeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := NodeClaimCircuitBreakerBlocked(tt.nodeClaim, tt.reason)

			assert.Equal(t, corev1.EventTypeWarning, event.Type)
			assert.Equal(t, "CircuitBreakerBlocked", event.Reason)
			assert.Equal(t, tt.expected, event.Message)
		})
	}

	assert.NotPanics(t, func() {
		event := NodeClaimCircuitBreakerBlocked(nil, "test reason")
		assert.Equal(t, corev1.EventTypeWarning, event.Type)
		assert.Equal(t, "CircuitBreakerBlocked", event.Reason)
		assert.Contains(t, event.Message, "Circuit breaker blocked provisioning for NodeClaim <unknown>: test reason")
		assert.Nil(t, event.InvolvedObject)
	})
}
