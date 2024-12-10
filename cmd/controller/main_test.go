package main

import (
	"context"
	"testing"

	"github.com/awslabs/operatorpkg/status"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	ibmcloud "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/instance"
)

// Mock Event Recorder
type mockEventRecorder struct {
	record.EventRecorder
}

func (m *mockEventRecorder) Publish(e ...events.Event) {
	// No-op for testing
}

// Mock InstanceType Provider
type mockInstanceTypeProvider struct{}

func (m *mockInstanceTypeProvider) Get(ctx context.Context, name string) (*cloudprovider.InstanceType, error) {
	return &cloudprovider.InstanceType{
		Name: "test-instance-type",
		Capacity: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("64Gi"),
			corev1.ResourcePods:   resource.MustParse("110"),
		},
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, "test-instance-type"),
			scheduling.NewRequirement(v1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, v1.CapacityTypeOnDemand),
			scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "us-south-1"),
		),
		Offerings: []cloudprovider.Offering{
			{
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "us-south-1"),
					scheduling.NewRequirement(v1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, v1.CapacityTypeOnDemand),
				),
				Price:     1.0,
				Available: true,
			},
		},
		Overhead: &cloudprovider.InstanceTypeOverhead{
			KubeReserved: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			SystemReserved: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			EvictionThreshold: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("500Mi"),
			},
		},
	}, nil
}

func (m *mockInstanceTypeProvider) List(ctx context.Context) ([]*cloudprovider.InstanceType, error) {
	instanceType, _ := m.Get(ctx, "test-instance-type")
	return []*cloudprovider.InstanceType{instanceType}, nil
}

func (m *mockInstanceTypeProvider) Create(ctx context.Context, instanceType *cloudprovider.InstanceType) error {
	return nil
}

func (m *mockInstanceTypeProvider) Delete(ctx context.Context, instanceType *cloudprovider.InstanceType) error {
	return nil
}

// Mock Instance Provider
type mockInstanceProvider struct{}

func (m *mockInstanceProvider) Create(ctx context.Context, nodeClaim *v1.NodeClaim) (*corev1.Node, error) {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeClaim.Name,
			Labels: map[string]string{
				"node.kubernetes.io/instance-type": "test-instance-type",
			},
		},
		Spec: corev1.NodeSpec{
			ProviderID: "ibm://test-instance-id",
		},
	}, nil
}

func (m *mockInstanceProvider) Delete(ctx context.Context, node *corev1.Node) error {
	return nil
}

func (m *mockInstanceProvider) GetInstance(ctx context.Context, node *corev1.Node) (*instance.Instance, error) {
	return &instance.Instance{
		ID:           "test-instance-id",
		Type:         "test-instance-type",
		Zone:         "us-south-1",
		Region:       "us-south",
		CapacityType: "on-demand",
		Status:       instance.InstanceStatusRunning,
	}, nil
}

func (m *mockInstanceProvider) TagInstance(ctx context.Context, instanceID string, tags map[string]string) error {
	return nil
}

func TestReconcile(t *testing.T) {
	// Create a new scheme and register types
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("Failed to add core scheme: %v", err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("Failed to add ibm scheme: %v", err)
	}

	// Register NodeClaim types
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	s.AddKnownTypes(gv,
		&v1.NodeClaim{},
		&v1.NodeClaimList{},
		&v1.NodePool{},
		&v1.NodePoolList{},
	)
	metav1.AddToGroupVersion(s, gv)

	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-nodeclass",
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:           "us-south-1",
			InstanceProfile: "bx2-4x16",
			Image:          "test-image",
			VPC:            "test-vpc",
			Subnet:         "test-subnet",
		},
		Status: v1alpha1.IBMNodeClassStatus{
			SpecHash: 12345,
			Conditions: []status.Condition{
				{
					Type:   "Ready",
					Status: metav1.ConditionTrue,
				},
			},
		},
	}

	nodeClaim := &v1.NodeClaim{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "karpenter.sh/v1",
			Kind:       "NodeClaim",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-node",
			Namespace: "default",
		},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{
				Name: nodeClass.Name,
			},
			Requirements: []v1.NodeSelectorRequirementWithMinValues{
				{
					NodeSelectorRequirement: corev1.NodeSelectorRequirement{
						Key:      corev1.LabelInstanceTypeStable,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"test-instance-type"},
					},
				},
			},
			Resources: v1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
		},
	}

	// Create fake client with objects
	client := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(nodeClass, nodeClaim).
		WithStatusSubresource(&v1.NodeClaim{}).
		Build()

	// Create CloudProvider with mocked dependencies
	cloudProvider := ibmcloud.New(
		client,
		&mockEventRecorder{},
		&mockInstanceTypeProvider{},
		&mockInstanceProvider{},
	)

	reconciler := NewIBMCloudReconciler(client, cloudProvider)

	_, err := reconciler.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      nodeClaim.Name,
			Namespace: nodeClaim.Namespace,
		},
	})
	assert.NoError(t, err)

	// Verify nodeclaim was updated
	var updatedNodeClaim v1.NodeClaim
	err = client.Get(context.Background(), types.NamespacedName{
		Name:      nodeClaim.Name,
		Namespace: nodeClaim.Namespace,
	}, &updatedNodeClaim)
	assert.NoError(t, err)
	assert.Contains(t, updatedNodeClaim.Finalizers, "karpenter.sh/finalizer")
}
