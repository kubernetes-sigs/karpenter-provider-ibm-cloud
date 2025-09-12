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

package orphancleanup

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

func TestOrphanCleanupController(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	t.Run("should skip cleanup when disabled", func(t *testing.T) {
		// Ensure orphan cleanup is disabled
		_ = os.Setenv("KARPENTER_ENABLE_ORPHAN_CLEANUP", "false")
		defer func() { _ = os.Unsetenv("KARPENTER_ENABLE_ORPHAN_CLEANUP") }()

		client := clientfake.NewClientBuilder().WithScheme(scheme).Build()
		controller := NewController(client, nil)

		result, err := controller.Reconcile(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, OrphanCheckInterval, result.RequeueAfter)
	})

	t.Run("should skip cleanup when orphan cleanup is enabled but no IBM client", func(t *testing.T) {
		_ = os.Setenv("KARPENTER_ENABLE_ORPHAN_CLEANUP", "true")
		defer func() { _ = os.Unsetenv("KARPENTER_ENABLE_ORPHAN_CLEANUP") }()

		client := clientfake.NewClientBuilder().WithScheme(scheme).Build()
		controller := NewController(client, nil)

		result, err := controller.Reconcile(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, OrphanCheckInterval, result.RequeueAfter)
	})

	t.Run("should skip non-Karpenter nodes", func(t *testing.T) {
		_ = os.Setenv("KARPENTER_ENABLE_ORPHAN_CLEANUP", "true")
		defer func() { _ = os.Unsetenv("KARPENTER_ENABLE_ORPHAN_CLEANUP") }()

		// Create a node without Karpenter labels
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
				Labels: map[string]string{
					"node-role.kubernetes.io/worker": "true",
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: "ibm:///us-south/instance-123",
			},
		}

		client := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
		ibmClient := &ibm.Client{} // Mock IBM client
		controller := NewController(client, ibmClient)

		result, err := controller.Reconcile(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, OrphanCheckInterval, result.RequeueAfter)
	})

	t.Run("should process Karpenter-managed nodes", func(t *testing.T) {
		_ = os.Setenv("KARPENTER_ENABLE_ORPHAN_CLEANUP", "true")
		defer func() { _ = os.Unsetenv("KARPENTER_ENABLE_ORPHAN_CLEANUP") }()

		// Create a node with Karpenter labels
		node := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
				Labels: map[string]string{
					"karpenter.sh/nodepool": "test-nodepool",
				},
			},
			Spec: corev1.NodeSpec{
				ProviderID: "ibm:///us-south/instance-123",
			},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.Time{Time: time.Now().Add(-15 * time.Minute)},
					},
				},
			},
		}

		client := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
		// Don't use IBM client in test - the controller will skip API calls if client is nil
		controller := NewController(client, nil)

		result, err := controller.Reconcile(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, OrphanCheckInterval, result.RequeueAfter)
	})
}

func TestIsNodeManagedByKarpenter(t *testing.T) {
	controller := &Controller{}

	testCases := []struct {
		name     string
		node     corev1.Node
		expected bool
	}{
		{
			name: "node with karpenter.sh/nodepool label",
			node: corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"karpenter.sh/nodepool": "test-nodepool",
					},
				},
			},
			expected: true,
		},
		{
			name: "node with karpenter.sh/provisioner label",
			node: corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"karpenter.sh/provisioner": "test-provisioner",
					},
				},
			},
			expected: true,
		},
		{
			name: "node with karpenter.ibm.sh/ibmnodeclass label",
			node: corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"karpenter.ibm.sh/ibmnodeclass": "test-nodeclass",
					},
				},
			},
			expected: true,
		},
		{
			name: "node with karpenter.sh/managed annotation",
			node: corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"karpenter.sh/managed": "true",
					},
				},
			},
			expected: true,
		},
		{
			name: "node without Karpenter labels or annotations",
			node: corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"node-role.kubernetes.io/worker": "true",
					},
				},
			},
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := controller.isNodeManagedByKarpenter(tc.node)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestExtractInstanceIDFromProviderID(t *testing.T) {
	controller := &Controller{}

	testCases := []struct {
		name       string
		providerID string
		expected   string
	}{
		{
			name:       "valid IBM provider ID",
			providerID: "ibm:///us-south/instance-123",
			expected:   "instance-123",
		},
		{
			name:       "valid IBM provider ID with different region",
			providerID: "ibm:///eu-de/instance-456",
			expected:   "instance-456",
		},
		{
			name:       "non-IBM provider ID",
			providerID: "aws:///us-west-2/i-123456789",
			expected:   "",
		},
		{
			name:       "malformed IBM provider ID",
			providerID: "ibm://instance-123",
			expected:   "",
		},
		{
			name:       "empty provider ID",
			providerID: "",
			expected:   "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := controller.extractInstanceIDFromProviderID(tc.providerID)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestIsNodeOrphanedLongEnough(t *testing.T) {
	controller := &Controller{
		orphanTimeout: 10 * time.Minute,
	}

	testCases := []struct {
		name     string
		node     corev1.Node
		expected bool
	}{
		{
			name: "node NotReady for long enough",
			node: corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.Time{Time: time.Now().Add(-15 * time.Minute)},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "node NotReady but not long enough",
			node: corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.Time{Time: time.Now().Add(-5 * time.Minute)},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "node Ready",
			node: corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: metav1.Time{Time: time.Now().Add(-15 * time.Minute)},
						},
					},
				},
			},
			expected: false,
		},
		{
			name: "node Unknown for long enough",
			node: corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionUnknown,
							LastTransitionTime: metav1.Time{Time: time.Now().Add(-15 * time.Minute)},
						},
					},
				},
			},
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := controller.isNodeOrphanedLongEnough(tc.node)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestGetOrphanTimeoutFromEnv(t *testing.T) {
	testCases := []struct {
		name     string
		envValue string
		expected time.Duration
	}{
		{
			name:     "valid timeout",
			envValue: "20",
			expected: 20 * time.Minute,
		},
		{
			name:     "timeout below minimum",
			envValue: "2",
			expected: MinimumOrphanTimeout,
		},
		{
			name:     "invalid timeout",
			envValue: "invalid",
			expected: DefaultOrphanTimeout,
		},
		{
			name:     "empty timeout",
			envValue: "",
			expected: DefaultOrphanTimeout,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envValue != "" {
				_ = os.Setenv("KARPENTER_ORPHAN_TIMEOUT_MINUTES", tc.envValue)
				defer func() { _ = os.Unsetenv("KARPENTER_ORPHAN_TIMEOUT_MINUTES") }()
			}

			result := getOrphanTimeoutFromEnv()
			assert.Equal(t, tc.expected, result)
		})
	}
}
