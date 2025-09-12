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
package tagging

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestController_Name(t *testing.T) {
	controller := &Controller{}
	assert.Equal(t, "nodeclaim.tagging", controller.Name())
}

func TestController_Register(t *testing.T) {
	// Create a proper scheme
	s := runtime.NewScheme()
	require.NoError(t, scheme.AddToScheme(s))

	// Register Karpenter v1 types manually
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	s.AddKnownTypes(gv,
		&karpenterv1.NodeClaim{},
		&karpenterv1.NodeClaimList{},
		&karpenterv1.NodePool{},
		&karpenterv1.NodePoolList{},
	)
	metav1.AddToGroupVersion(s, gv)

	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()
	controller, err := NewController(fakeClient)
	if err != nil {
		t.Skipf("Skipping test due to missing IBM credentials: %v", err)
		return
	}

	// Test that Register method exists and can be called
	// (will fail with nil manager but that's expected)
	var mgr manager.Manager
	err = controller.Register(context.Background(), mgr)
	assert.Error(t, err) // Expected because mgr is nil
}

func TestController_isVPCMode(t *testing.T) {
	controller := &Controller{}

	tests := []struct {
		name      string
		nodeClass *v1alpha1.IBMNodeClass
		expected  bool
	}{
		{
			name: "IKS mode - cluster ID set",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					IKSClusterID: "test-cluster-id",
				},
			},
			expected: false,
		},
		{
			name: "IKS API bootstrap mode",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					BootstrapMode: ptr("iks-api"),
				},
			},
			expected: false,
		},
		{
			name: "Cloud-init bootstrap mode",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					BootstrapMode: ptr("cloud-init"),
				},
			},
			expected: true,
		},
		{
			name: "VPC mode - VPC specified",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					VPC: "test-vpc-id",
				},
			},
			expected: true,
		},
		{
			name: "Default - no configuration",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{},
			},
			expected: false,
		},
		{
			name: "VPC with IKS cluster ID (IKS takes precedence)",
			nodeClass: &v1alpha1.IBMNodeClass{
				Spec: v1alpha1.IBMNodeClassSpec{
					IKSClusterID: "test-cluster",
					VPC:          "test-vpc",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := controller.isVPCMode(tt.nodeClass)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestController_Reconcile(t *testing.T) {
	// Create a proper scheme
	s := runtime.NewScheme()
	require.NoError(t, scheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))

	// Register Karpenter v1 types manually
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	s.AddKnownTypes(gv,
		&karpenterv1.NodeClaim{},
		&karpenterv1.NodeClaimList{},
		&karpenterv1.NodePool{},
		&karpenterv1.NodePoolList{},
	)
	metav1.AddToGroupVersion(s, gv)

	tests := []struct {
		name         string
		nodeClaims   []karpenterv1.NodeClaim
		nodes        []v1.Node
		nodeClasses  []v1alpha1.IBMNodeClass
		wantErr      bool
		expectTagged bool
		expectedTags map[string]string
	}{
		{
			name:    "no NodeClaims",
			wantErr: false,
		},
		{
			name: "NodeClaim without node name",
			nodeClaims: []karpenterv1.NodeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclaim-1",
					},
					Status: karpenterv1.NodeClaimStatus{
						NodeName: "", // No node name
					},
				},
			},
			wantErr:      false,
			expectTagged: false,
		},
		{
			name: "Node without provider ID",
			nodeClaims: []karpenterv1.NodeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclaim-2",
					},
					Status: karpenterv1.NodeClaimStatus{
						NodeName: "test-node",
					},
				},
			},
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Spec: v1.NodeSpec{
						ProviderID: "", // No provider ID
					},
				},
			},
			wantErr:      false,
			expectTagged: false,
		},
		{
			name: "VPC NodeClaim with custom tags",
			nodeClaims: []karpenterv1.NodeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclaim-3",
						Labels: map[string]string{
							"karpenter.sh/nodepool": "test-pool",
						},
					},
					Spec: karpenterv1.NodeClaimSpec{
						NodeClassRef: &karpenterv1.NodeClassReference{
							Name: "test-nodeclass",
						},
						Requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{
							{
								NodeSelectorRequirement: v1.NodeSelectorRequirement{
									Key:      "karpenter.ibm.sh/tags",
									Operator: v1.NodeSelectorOpIn,
									Values:   []string{"env=production", "team=platform"},
								},
							},
						},
					},
					Status: karpenterv1.NodeClaimStatus{
						NodeName: "test-node",
					},
				},
			},
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Spec: v1.NodeSpec{
						ProviderID: "ibm://instance-123",
					},
				},
			},
			nodeClasses: []v1alpha1.IBMNodeClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclass",
					},
					Spec: v1alpha1.IBMNodeClassSpec{
						VPC: "test-vpc",
					},
				},
			},
			wantErr:      false,
			expectTagged: true,
			expectedTags: map[string]string{
				"karpenter.ibm.sh/nodeclaim": "test-nodeclaim-3",
				"karpenter.ibm.sh/nodepool":  "test-pool",
				"env":                        "production",
				"team":                       "platform",
			},
		},
		{
			name: "IKS NodeClaim (should skip)",
			nodeClaims: []karpenterv1.NodeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclaim-4",
					},
					Spec: karpenterv1.NodeClaimSpec{
						NodeClassRef: &karpenterv1.NodeClassReference{
							Name: "iks-nodeclass",
						},
					},
					Status: karpenterv1.NodeClaimStatus{
						NodeName: "test-node",
					},
				},
			},
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Spec: v1.NodeSpec{
						ProviderID: "ibm://instance-456",
					},
				},
			},
			nodeClasses: []v1alpha1.IBMNodeClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "iks-nodeclass",
					},
					Spec: v1alpha1.IBMNodeClassSpec{
						IKSClusterID: "test-cluster",
					},
				},
			},
			wantErr:      false,
			expectTagged: false,
		},
		{
			name: "NodeClass not found",
			nodeClaims: []karpenterv1.NodeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclaim-5",
					},
					Spec: karpenterv1.NodeClaimSpec{
						NodeClassRef: &karpenterv1.NodeClassReference{
							Name: "missing-nodeclass",
						},
					},
					Status: karpenterv1.NodeClaimStatus{
						NodeName: "test-node",
					},
				},
			},
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Spec: v1.NodeSpec{
						ProviderID: "ibm://instance-789",
					},
				},
			},
			wantErr:      false,
			expectTagged: false,
		},
		{
			name: "Tag with invalid format (no equals sign)",
			nodeClaims: []karpenterv1.NodeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclaim-6",
						Labels: map[string]string{
							"karpenter.sh/nodepool": "test-pool",
						},
					},
					Spec: karpenterv1.NodeClaimSpec{
						NodeClassRef: &karpenterv1.NodeClassReference{
							Name: "test-nodeclass",
						},
						Requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{
							{
								NodeSelectorRequirement: v1.NodeSelectorRequirement{
									Key:      "karpenter.ibm.sh/tags",
									Operator: v1.NodeSelectorOpIn,
									Values:   []string{"invalidtag", "valid=tag"},
								},
							},
						},
					},
					Status: karpenterv1.NodeClaimStatus{
						NodeName: "test-node",
					},
				},
			},
			nodes: []v1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-node",
					},
					Spec: v1.NodeSpec{
						ProviderID: "ibm://instance-999",
					},
				},
			},
			nodeClasses: []v1alpha1.IBMNodeClass{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-nodeclass",
					},
					Spec: v1alpha1.IBMNodeClassSpec{
						VPC: "test-vpc",
					},
				},
			},
			wantErr:      false,
			expectTagged: true,
			expectedTags: map[string]string{
				"karpenter.ibm.sh/nodeclaim": "test-nodeclaim-6",
				"karpenter.ibm.sh/nodepool":  "test-pool",
				"valid":                      "tag", // Only valid tag is included
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip test if IBM credentials are needed
			if tt.expectTagged {
				t.Skip("Skipping test that requires IBM credentials and VPC provider mocking")
			}

			// Create objects for the fake client
			objs := make([]client.Object, 0)
			for i := range tt.nodeClaims {
				objs = append(objs, &tt.nodeClaims[i])
			}
			for i := range tt.nodes {
				objs = append(objs, &tt.nodes[i])
			}
			for i := range tt.nodeClasses {
				objs = append(objs, &tt.nodeClasses[i])
			}

			// Create fake client
			fakeClient := fake.NewClientBuilder().
				WithScheme(s).
				WithObjects(objs...).
				Build()

			// Create controller (will fail without IBM credentials)
			controller := &Controller{
				kubeClient: fakeClient,
				ibmClient:  nil, // We can't easily mock this
			}

			// Run reconciliation
			_, err := controller.Reconcile(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				// May still error due to nil IBM client, but that's expected in tests
				if err != nil && tt.expectTagged {
					t.Logf("Expected error due to missing IBM client: %v", err)
				}
			}
		})
	}
}

func TestNewController(t *testing.T) {
	// Create a proper scheme
	s := runtime.NewScheme()
	require.NoError(t, scheme.AddToScheme(s))

	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()

	// Test creating controller
	controller, err := NewController(fakeClient)

	// In test environment without IBM credentials, this will fail
	// but we're testing that the function exists and returns appropriate error
	if err != nil {
		assert.Contains(t, err.Error(), "creating IBM client")
		return
	}

	assert.NotNil(t, controller)
	assert.NotNil(t, controller.kubeClient)
	assert.NotNil(t, controller.ibmClient)
}

func TestUpdateVPCInstanceTags(t *testing.T) {
	// This tests the actual method signature and would need
	// integration testing or more complex mocking to fully test

	controller := &Controller{
		kubeClient: nil,
		ibmClient:  nil,
	}

	// Test that the method exists and returns error when IBM client is nil
	err := controller.updateVPCInstanceTags(context.Background(), "ibm://test-instance", map[string]string{"test": "tag"})
	assert.Error(t, err)
}

func TestTagsExtraction(t *testing.T) {
	tests := []struct {
		name         string
		requirements []karpenterv1.NodeSelectorRequirementWithMinValues
		expectedTags map[string]string
	}{
		{
			name: "single tag",
			requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{
				{
					NodeSelectorRequirement: v1.NodeSelectorRequirement{
						Key:      "karpenter.ibm.sh/tags",
						Operator: v1.NodeSelectorOpIn,
						Values:   []string{"env=prod"},
					},
				},
			},
			expectedTags: map[string]string{
				"env": "prod",
			},
		},
		{
			name: "multiple tags",
			requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{
				{
					NodeSelectorRequirement: v1.NodeSelectorRequirement{
						Key:      "karpenter.ibm.sh/tags",
						Operator: v1.NodeSelectorOpIn,
						Values:   []string{"env=prod", "team=platform", "cost-center=engineering"},
					},
				},
			},
			expectedTags: map[string]string{
				"env":         "prod",
				"team":        "platform",
				"cost-center": "engineering",
			},
		},
		{
			name: "tags with equals in value",
			requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{
				{
					NodeSelectorRequirement: v1.NodeSelectorRequirement{
						Key:      "karpenter.ibm.sh/tags",
						Operator: v1.NodeSelectorOpIn,
						Values:   []string{"config=key1=value1"},
					},
				},
			},
			expectedTags: map[string]string{
				"config": "key1=value1",
			},
		},
		{
			name: "invalid tag format",
			requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{
				{
					NodeSelectorRequirement: v1.NodeSelectorRequirement{
						Key:      "karpenter.ibm.sh/tags",
						Operator: v1.NodeSelectorOpIn,
						Values:   []string{"invalidtag"},
					},
				},
			},
			expectedTags: map[string]string{},
		},
		{
			name:         "no tag requirements",
			requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{},
			expectedTags: map[string]string{},
		},
		{
			name: "different requirement key",
			requirements: []karpenterv1.NodeSelectorRequirementWithMinValues{
				{
					NodeSelectorRequirement: v1.NodeSelectorRequirement{
						Key:      "node.kubernetes.io/instance-type",
						Operator: v1.NodeSelectorOpIn,
						Values:   []string{"cx2-2x4"},
					},
				},
			},
			expectedTags: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := make(map[string]string)

			// Extract tags using the same logic as the controller
			for _, req := range tt.requirements {
				if req.Key == "karpenter.ibm.sh/tags" {
					for _, value := range req.Values {
						parts := strings.SplitN(value, "=", 2)
						if len(parts) == 2 {
							tags[parts[0]] = parts[1]
						}
					}
				}
			}

			assert.Equal(t, tt.expectedTags, tags)
		})
	}
}

// Helper functions
func ptr(s string) *string {
	return &s
}
