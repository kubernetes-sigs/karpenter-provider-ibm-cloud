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

package preemption

import (
	"context"
	"fmt"
	"testing"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	mock_ibm "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

func ptrString(s string) *string { return &s }

type mockVPCClientProvider struct {
	vpcClient *ibm.VPCClient
	err       error
}

func (m *mockVPCClientProvider) GetVPCClient(_ context.Context) (*ibm.VPCClient, error) {
	return m.vpcClient, m.err
}

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	s.AddKnownTypes(gv,
		&karpv1.NodeClaim{},
		&karpv1.NodeClaimList{},
	)
	metav1.AddToGroupVersion(s, gv)
	return s
}

func newTestController(t *testing.T, mockVPC *mock_ibm.MockvpcClientInterface, objs ...client.Object) *Controller {
	t.Helper()
	scheme := newTestScheme()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	vpcClient := ibm.NewVPCClientWithMock(mockVPC)

	return &Controller{
		ibmClient:            &mockVPCClientProvider{vpcClient: vpcClient},
		kubeClient:           kubeClient,
		recorder:             record.NewFakeRecorder(10),
		unavailableOfferings: cache.NewUnavailableOfferings(),
	}
}

func testInstance(id, profile, zone, status string, statusReasons []vpcv1.InstanceStatusReason) vpcv1.Instance {
	return vpcv1.Instance{
		ID:            ptrString(id),
		Profile:       &vpcv1.InstanceProfileReference{Name: ptrString(profile)},
		Zone:          &vpcv1.ZoneReference{Name: ptrString(zone)},
		Status:        ptrString(status),
		StatusReasons: statusReasons,
	}
}

func preemptionReason() []vpcv1.InstanceStatusReason {
	return []vpcv1.InstanceStatusReason{
		{Code: ptrString(vpcv1.InstanceStatusReasonCodeStoppedByPreemptionConst)},
	}
}

func testNodeClaim(name, instanceID string) *karpv1.NodeClaim {
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID: fmt.Sprintf("ibm:///us-south/%s", instanceID),
		},
	}
}

func TestReconcile_PreemptedInstance(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
	nc := testNodeClaim("test-nc", "instance-123")
	controller := newTestController(t, mockVPC, nc)

	instance := testInstance("instance-123", "bx2-4x16", "us-south-1", "stopped", preemptionReason())
	mockVPC.EXPECT().
		ListInstancesWithContext(gomock.Any(), gomock.Any()).
		Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{instance}}, &core.DetailedResponse{}, nil)
	mockVPC.EXPECT().
		DeleteInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(&core.DetailedResponse{}, nil)

	result, err := controller.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SpotPreemptionCheckInterval, result.RequeueAfter)

	// NodeClaim should be deleted
	err = controller.kubeClient.Get(context.Background(), types.NamespacedName{Name: "test-nc"}, &karpv1.NodeClaim{})
	assert.True(t, client.IgnoreNotFound(err) == nil && err != nil, "nodeclaim should be deleted")

	// Offering should be marked unavailable
	assert.True(t, controller.unavailableOfferings.IsUnavailable("bx2-4x16:us-south-1:spot"))
}

func TestReconcile_RunningInstanceSkipped(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
	nc := testNodeClaim("test-nc", "instance-123")
	controller := newTestController(t, mockVPC, nc)

	instance := testInstance("instance-123", "bx2-4x16", "us-south-1", "running", preemptionReason())
	mockVPC.EXPECT().
		ListInstancesWithContext(gomock.Any(), gomock.Any()).
		Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{instance}}, &core.DetailedResponse{}, nil)

	result, err := controller.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SpotPreemptionCheckInterval, result.RequeueAfter)

	// NodeClaim should NOT be deleted
	err = controller.kubeClient.Get(context.Background(), types.NamespacedName{Name: "test-nc"}, &karpv1.NodeClaim{})
	assert.NoError(t, err)

	// Offering should NOT be marked unavailable
	assert.False(t, controller.unavailableOfferings.IsUnavailable("bx2-4x16:us-south-1:spot"))
}

func TestReconcile_NoMatchingNodeClaim(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
	controller := newTestController(t, mockVPC) // no NodeClaims

	instance := testInstance("instance-orphan", "bx2-4x16", "us-south-1", "stopped", preemptionReason())
	mockVPC.EXPECT().
		ListInstancesWithContext(gomock.Any(), gomock.Any()).
		Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{instance}}, &core.DetailedResponse{}, nil)
	// DeleteInstance should NOT be called since there's no matching NodeClaim

	result, err := controller.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SpotPreemptionCheckInterval, result.RequeueAfter)

	assert.False(t, controller.unavailableOfferings.IsUnavailable("bx2-4x16:us-south-1:spot"))
}

func TestReconcile_MultipleInstances(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
	nc1 := testNodeClaim("nc-1", "instance-1")
	nc2 := testNodeClaim("nc-2", "instance-2")
	controller := newTestController(t, mockVPC, nc1, nc2)

	instances := []vpcv1.Instance{
		testInstance("instance-1", "bx2-4x16", "us-south-1", "stopped", preemptionReason()),
		testInstance("instance-2", "cx2-8x16", "us-south-2", "stopped", preemptionReason()),
		testInstance("instance-3", "bx2-4x16", "us-south-1", "running", nil),
	}
	mockVPC.EXPECT().
		ListInstancesWithContext(gomock.Any(), gomock.Any()).
		Return(&vpcv1.InstanceCollection{Instances: instances}, &core.DetailedResponse{}, nil)
	mockVPC.EXPECT().
		DeleteInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(&core.DetailedResponse{}, nil).
		Times(2)

	result, err := controller.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SpotPreemptionCheckInterval, result.RequeueAfter)

	// Both NodeClaims should be deleted
	err = controller.kubeClient.Get(context.Background(), types.NamespacedName{Name: "nc-1"}, &karpv1.NodeClaim{})
	assert.True(t, client.IgnoreNotFound(err) == nil && err != nil)
	err = controller.kubeClient.Get(context.Background(), types.NamespacedName{Name: "nc-2"}, &karpv1.NodeClaim{})
	assert.True(t, client.IgnoreNotFound(err) == nil && err != nil)

	// Both offerings should be marked unavailable
	assert.True(t, controller.unavailableOfferings.IsUnavailable("bx2-4x16:us-south-1:spot"))
	assert.True(t, controller.unavailableOfferings.IsUnavailable("cx2-8x16:us-south-2:spot"))
}

func TestReconcile_DeleteInstanceError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockVPC := mock_ibm.NewMockvpcClientInterface(ctrl)
	nc := testNodeClaim("test-nc", "instance-123")
	controller := newTestController(t, mockVPC, nc)

	instance := testInstance("instance-123", "bx2-4x16", "us-south-1", "stopped", preemptionReason())
	mockVPC.EXPECT().
		ListInstancesWithContext(gomock.Any(), gomock.Any()).
		Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{instance}}, &core.DetailedResponse{}, nil)
	mockVPC.EXPECT().
		DeleteInstanceWithContext(gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("delete failed"))

	result, err := controller.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SpotPreemptionCheckInterval, result.RequeueAfter)

	// Offering should still be marked unavailable despite delete failure
	assert.True(t, controller.unavailableOfferings.IsUnavailable("bx2-4x16:us-south-1:spot"))

	// NodeClaim should still be deleted (independent of VPC delete)
	err = controller.kubeClient.Get(context.Background(), types.NamespacedName{Name: "test-nc"}, &karpv1.NodeClaim{})
	assert.True(t, client.IgnoreNotFound(err) == nil && err != nil)
}
