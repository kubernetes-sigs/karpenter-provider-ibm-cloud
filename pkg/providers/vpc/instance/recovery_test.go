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

package instance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/globaltaggingv1"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	mockibm "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm/mock"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
)

type recordingTagger struct {
	names  []string
	onList func()
}

const testAccountID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testAccountResolver(context.Context) (string, error) { return testAccountID, nil }

const testInstanceCRN = "crn:v1:bluemix:public:is:us-south-1:a/" + testAccountID + "::instance:test"

func (t *recordingTagger) AttachTagWithContext(_ context.Context, options *globaltaggingv1.AttachTagOptions) (*globaltaggingv1.TagResults, *core.DetailedResponse, error) {
	t.names = append(t.names, options.TagNames...)
	return &globaltaggingv1.TagResults{Results: []globaltaggingv1.TagResultsItem{{ResourceID: options.Resources[0].ResourceID}}}, &core.DetailedResponse{StatusCode: 200}, nil
}

func (t *recordingTagger) ListTagsWithContext(_ context.Context, _ *globaltaggingv1.ListTagsOptions) (*globaltaggingv1.TagList, *core.DetailedResponse, error) {
	if t.onList != nil {
		t.onList()
	}
	page := &globaltaggingv1.TagList{TotalCount: core.Int64Ptr(int64(len(t.names)))}
	for _, name := range t.names {
		page.Items = append(page.Items, globaltaggingv1.Tag{Name: core.StringPtr(name)})
	}
	return page, nil, nil
}

func launchFixture(t *testing.T) (*VPCInstanceProvider, *karpv1.NodeClaim, *launchConfig, *mockibm.MockvpcClientInterface, *recordingTagger) {
	t.Helper()
	t.Setenv("IBM_ACCOUNT_ID", "")
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	claim := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", UID: types.UID("claim-uid")}, Spec: karpv1.NodeClaimSpec{NodeClassRef: &karpv1.NodeClassReference{Group: v1alpha1.Group, Kind: "IBMNodeClass", Name: "removed-class"}}}
	config := &launchConfig{Name: ownership.InstanceName("cluster-uid", string(claim.UID)), ClusterUID: "cluster-uid", ClaimUID: string(claim.UID), ClassUID: "class-uid", AccountID: testAccountID, Region: "us-south", ResourceGroup: "resource-group", VPC: "vpc", Profile: "bx2-2x8", Zone: "us-south-1", Subnet: "subnet", Image: "image", Submitted: true, SubmittedAt: time.Now().UTC()}
	value, err := json.Marshal(config)
	require.NoError(t, err)
	claim.Annotations = map[string]string{LaunchAnnotation: string(value), ownership.BackendAnnotation: "vpc"}
	claim.Finalizers = []string{LaunchFinalizer}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claim, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}}).Build()
	mock := mockibm.NewMockvpcClientInterface(gomock.NewController(t))
	tagger := &recordingTagger{}
	vpc := ibm.NewVPCClientWithMock(mock, tagger)
	provider := &VPCInstanceProvider{kubeClient: kube, apiReader: kube, vpcClientManager: vpcclient.NewManagerWithMockClient(vpc), instanceCache: cache.New(time.Hour), accountResolver: testAccountResolver}
	return provider, claim, config, mock, tagger
}

func matchingInstance(config *launchConfig) *vpcv1.Instance {
	return &vpcv1.Instance{ID: core.StringPtr("instance"), CRN: core.StringPtr("crn:v1:bluemix:public:is:" + config.Zone + ":a/" + config.AccountID + "::instance:instance"), Name: &config.Name, ResourceGroup: &vpcv1.ResourceGroupReference{ID: &config.ResourceGroup}, Profile: &vpcv1.InstanceProfileReference{Name: &config.Profile}, Zone: &vpcv1.ZoneReference{Name: &config.Zone}, VPC: &vpcv1.VPCReference{ID: &config.VPC}, Image: &vpcv1.ImageReference{ID: &config.Image}, PrimaryNetworkAttachment: &vpcv1.InstanceNetworkAttachmentReference{Subnet: &vpcv1.SubnetReference{ID: &config.Subnet}}}
}

func TestCreateRecoversSubmittedInstanceWithoutNodeClass(t *testing.T) {
	provider, claim, config, mock, tagger := launchFixture(t)
	t.Setenv("IBM_ACCOUNT_ID", testAccountID)
	value, err := json.Marshal(config)
	require.NoError(t, err)
	claim.Annotations[LaunchAnnotation] = string(value)
	require.NoError(t, provider.kubeClient.Update(context.Background(), claim))
	instance := matchingInstance(config)
	mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{*instance}}, nil, nil)
	mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(instance, nil, nil).Times(2)
	node, err := provider.Create(context.Background(), claim, nil)
	require.NoError(t, err)
	require.Equal(t, "ibm:///us-south/instance", node.Spec.ProviderID)
	require.Equal(t, config.Image, node.Annotations[LaunchImageAnnotation])
	require.Equal(t, config.AccountID, node.Annotations[ownership.AccountIDAnnotation])
	require.Contains(t, tagger.names, ownership.ClusterUIDTag+":cluster-uid")
}

func TestCreateDoesNotRetryUncertainSubmission(t *testing.T) {
	provider, claim, config, mock, _ := launchFixture(t)
	mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{}, nil, nil).Times(2)
	for range 2 {
		_, err := provider.Create(context.Background(), claim, nil)
		require.Error(t, err)
	}
	fresh := &karpv1.NodeClaim{}
	require.NoError(t, provider.kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	require.Contains(t, fresh.Finalizers, LaunchFinalizer)
}

func TestCreateRejectsProfileMismatchBeforeCloudOrCheckpoint(t *testing.T) {
	provider, claim, _, _, _ := launchFixture(t)
	ctx := context.Background()
	require.NoError(t, v1alpha1.AddToScheme(provider.kubeClient.Scheme()))
	class := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "class-uid", Generation: 1},
		Spec:       v1alpha1.IBMNodeClassSpec{Region: "us-south", Zone: "us-south-1", Subnet: "subnet", VPC: "vpc", ResourceGroup: strings.Repeat("a", 32), SecurityGroups: []string{"security-group"}, Image: "image", UserData: "#!/bin/sh\ntrue", InstanceProfile: "bx2-2x8"},
		Status:     v1alpha1.IBMNodeClassStatus{ResolvedImageID: "image", Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: 1}}},
	}
	require.NoError(t, provider.kubeClient.Create(ctx, class))
	claim.Spec.NodeClassRef.Name = class.Name
	claim.Annotations = nil
	claim.Finalizers = nil
	require.NoError(t, provider.kubeClient.Update(ctx, claim))
	_, err := provider.Create(ctx, claim, []*cloudprovider.InstanceType{{Name: "bx4-2x8", Overhead: &cloudprovider.InstanceTypeOverhead{}}})
	require.EqualError(t, err, "selected instance profile bx4-2x8 differs from the current NodeClass profile bx2-2x8")
	fresh := &karpv1.NodeClaim{}
	require.NoError(t, provider.kubeClient.Get(ctx, client.ObjectKeyFromObject(claim), fresh))
	require.Empty(t, fresh.Annotations[LaunchAnnotation])
	require.NotContains(t, fresh.Finalizers, LaunchFinalizer)
}

func TestPendingCleanupPreservesUncertainAndUnrelatedResources(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(fmt.Sprint(rejected), func(t *testing.T) {
			provider, claim, config, mock, _ := launchFixture(t)
			config.Rejected = rejected
			value, err := json.Marshal(config)
			require.NoError(t, err)
			claim.Annotations[LaunchAnnotation] = string(value)
			mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{{Name: core.StringPtr("claim")}}}, nil, nil)
			complete, err := provider.CleanupPending(context.Background(), claim)
			require.NoError(t, err)
			require.Equal(t, rejected, complete)
		})
	}
}

func TestPendingCleanupRefusesChangedConfiguration(t *testing.T) {
	for _, mutate := range []func(*vpcv1.Instance){
		func(instance *vpcv1.Instance) { instance.Image.ID = core.StringPtr("foreign-image") },
		func(instance *vpcv1.Instance) { instance.ResourceGroup.ID = core.StringPtr("foreign-resource-group") },
	} {
		provider, claim, config, mock, _ := launchFixture(t)
		instance := matchingInstance(config)
		mutate(instance)
		mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{*instance}}, nil, nil)
		mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(instance, nil, nil)
		complete, err := provider.CleanupPending(context.Background(), claim)
		require.Error(t, err)
		require.False(t, complete)
	}
}

func TestLaunchRecoveryQuarantinesChangedAccount(t *testing.T) {
	provider, claim, config, _, _ := launchFixture(t)
	config.AccountID = testAccountID
	value, err := json.Marshal(config)
	require.NoError(t, err)
	claim.Annotations[LaunchAnnotation] = string(value)
	require.NoError(t, provider.kubeClient.Update(context.Background(), claim))
	t.Setenv("IBM_ACCOUNT_ID", "other-account")
	require.ErrorContains(t, provider.ValidateLaunchTarget(context.Background(), claim), "configured IBM account")
	_, err = provider.Create(context.Background(), claim, nil)
	require.ErrorContains(t, err, "configured IBM account")
	complete, err := provider.CleanupPending(context.Background(), claim)
	require.ErrorContains(t, err, "configured IBM account")
	require.False(t, complete)
}

func TestLaunchTargetRequiresImmutableCheckpoint(t *testing.T) {
	provider, claim, _, _, _ := launchFixture(t)
	require.NoError(t, provider.ValidateLaunchTarget(context.Background(), claim))
	for _, mutate := range []func(*karpv1.NodeClaim){
		func(claim *karpv1.NodeClaim) { delete(claim.Annotations, LaunchAnnotation) },
		func(claim *karpv1.NodeClaim) { claim.Annotations[LaunchAnnotation] = "invalid" },
		func(claim *karpv1.NodeClaim) { claim.UID = "foreign-claim" },
		func(claim *karpv1.NodeClaim) { claim.Status.ProviderID = "ibm:///eu-de/instance" },
	} {
		changed := claim.DeepCopy()
		mutate(changed)
		require.Error(t, provider.ValidateLaunchTarget(context.Background(), changed))
	}
	require.Error(t, provider.ValidateLaunchTarget(context.Background(), nil))
}

func TestCredentialScopeChangeQuarantinesOwnedInstancesWithoutConfiguredAccount(t *testing.T) {
	provider, claim, config, _, _ := launchFixture(t)
	provider.accountResolver = func(context.Context) (string, error) { return strings.Repeat("b", 32), nil }
	require.ErrorContains(t, provider.ValidateLaunchTarget(context.Background(), claim), "immutable birth account")
	complete, err := provider.CleanupPending(context.Background(), claim)
	require.ErrorContains(t, err, "immutable birth account")
	require.False(t, complete)
	err = provider.Delete(context.Background(), config.node(claim, "instance"))
	require.ErrorContains(t, err, "immutable birth account")
}

func TestLegacyAccountProofRequiresActualCredentialMatch(t *testing.T) {
	provider, claim, _, _, _ := launchFixture(t)
	delete(claim.Annotations, LaunchAnnotation)
	claim.Status.ProviderID = "ibm:///us-south/instance"
	require.ErrorContains(t, provider.ValidateLaunchTarget(context.Background(), claim), "birth account is unproven")
	claim.Annotations[ownership.AccountIDAnnotation] = testAccountID
	require.NoError(t, provider.ValidateLaunchTarget(context.Background(), claim))
	provider.accountResolver = func(context.Context) (string, error) { return strings.Repeat("b", 32), nil }
	t.Setenv("IBM_ACCOUNT_ID", testAccountID)
	require.ErrorContains(t, provider.ValidateLaunchTarget(context.Background(), claim), "VPC credential account")
}

func TestDeleteWithoutAccountProofDoesNotReachCloud(t *testing.T) {
	provider, _, _, _, _ := launchFixture(t)
	err := provider.Delete(context.Background(), &corev1.Node{Spec: corev1.NodeSpec{ProviderID: "ibm:///us-south/instance"}})
	require.ErrorContains(t, err, "birth account is unproven")
}

func TestFreshInstanceProofRejectsForeignAccountCRN(t *testing.T) {
	provider, _, config, mock, _ := launchFixture(t)
	instance := matchingInstance(config)
	instance.CRN = core.StringPtr("crn:v1:bluemix:public:is:us-south-1:a/" + strings.Repeat("b", 32) + "::instance:instance")
	mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(instance, nil, nil)
	_, err := provider.GetFresh(context.Background(), "ibm:///us-south/instance")
	require.ErrorContains(t, err, "instance CRN")
}

func TestPendingCleanupWaitsForGracefulNodeTermination(t *testing.T) {
	for _, finalizer := range []bool{false, true} {
		t.Run(fmt.Sprint(finalizer), func(t *testing.T) {
			provider, claim, config, mock, _ := launchFixture(t)
			instance := matchingInstance(config)
			mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{*instance}}, nil, nil).Times(2)
			mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(instance, nil, nil).Times(3)
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "registered", UID: "node-uid"}, Spec: corev1.NodeSpec{ProviderID: "ibm:///us-south/instance"}}
			if finalizer {
				node.Finalizers = []string{karpv1.TerminationFinalizer}
			}
			require.NoError(t, provider.kubeClient.Create(context.Background(), node))
			complete, err := provider.CleanupPending(context.Background(), claim)
			require.NoError(t, err)
			require.False(t, complete)
			fresh := &corev1.Node{}
			require.NoError(t, provider.kubeClient.Get(context.Background(), client.ObjectKeyFromObject(node), fresh))
			require.False(t, fresh.DeletionTimestamp.IsZero())
			require.Contains(t, fresh.Finalizers, karpv1.TerminationFinalizer)
			require.Equal(t, claim.UID, fresh.OwnerReferences[0].UID)
		})
	}
}

func TestCheckpointRequiresSameLiveClaim(t *testing.T) {
	provider, claim, config, _, _ := launchFixture(t)
	claim.UID = "another-uid"
	require.Error(t, provider.checkpointLaunch(context.Background(), claim, config))
}

func TestFreshLookupBypassesCacheAndReturnsIndependentNodes(t *testing.T) {
	provider, _, config, mock, _ := launchFixture(t)
	instance := matchingInstance(config)
	providerID := "ibm:///us-south/instance"
	mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(instance, nil, nil).Times(2)
	first, err := provider.Get(context.Background(), providerID)
	require.NoError(t, err)
	first.Labels[corev1.LabelInstanceTypeStable] = "mutated"
	second, err := provider.Get(context.Background(), providerID)
	require.NoError(t, err)
	require.Equal(t, config.Profile, second.Labels[corev1.LabelInstanceTypeStable])
	_, err = provider.GetFresh(context.Background(), providerID)
	require.NoError(t, err)
	require.Equal(t, testAccountID, second.Annotations[ownership.AccountIDAnnotation])
}

type launchTransport func(*http.Request) (*http.Response, error)

func (f launchTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestCreateRecoversLostResponseAndPreservesRetainedVolumes(t *testing.T) {
	t.Setenv("IBM_ACCOUNT_ID", "")
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	claim := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "reused-name", UID: "new-claim-uid"}, Spec: karpv1.NodeClaimSpec{NodeClassRef: &karpv1.NodeClassReference{Name: "class"}}}
	class := &v1alpha1.IBMNodeClass{ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "class-uid", Generation: 1}, Spec: v1alpha1.IBMNodeClassSpec{Region: "us-south", Zone: "us-south-1", Subnet: "subnet", VPC: "vpc", ResourceGroup: strings.Repeat("a", 32), SecurityGroups: []string{"security-group"}, Image: "image", UserData: "#!/bin/sh\ntrue"}, Status: v1alpha1.IBMNodeClassStatus{ResolvedImageID: "image", Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: 1}}}}
	class.Spec.BlockDeviceMappings = []v1alpha1.BlockDeviceMapping{{RootVolume: true, VolumeSpec: &v1alpha1.VolumeSpec{
		DeleteOnTermination: core.BoolPtr(false),
		Tags:                []string{"custom:value", ownership.ClusterUIDTag + ":foreign"},
	}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(class).WithObjects(claim, class, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}}).Build()
	sdk, err := vpcv1.NewVpcV1(&vpcv1.VpcV1Options{URL: "https://test.iaas.cloud.ibm.com/v1", Authenticator: &core.NoAuthAuthenticator{}})
	require.NoError(t, err)
	created := false
	posts := 0
	config := &launchConfig{Name: ownership.InstanceName("cluster-uid", string(claim.UID)), AccountID: testAccountID, Region: "us-south", ResourceGroup: class.Spec.ResourceGroup, VPC: "vpc", Profile: "bx2-2x8", Zone: "us-south-1", Subnet: "subnet", Image: "image"}
	instance := matchingInstance(config)
	sdk.Service.SetHTTPClient(&http.Client{Transport: launchTransport(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		var result interface{}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/instances":
			instances := []vpcv1.Instance{}
			if created {
				instances = append(instances, *instance)
			}
			result = &vpcv1.InstanceCollection{Instances: instances}
		case request.Method == http.MethodGet && request.URL.Path == "/v1/instances/instance":
			result = instance
		case request.Method == http.MethodPost && request.URL.Path == "/v1/instances":
			fresh := &karpv1.NodeClaim{}
			require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(claim), fresh))
			require.Contains(t, fresh.Finalizers, LaunchFinalizer)
			checkpoint, decodeErr := decodeLaunch(fresh.Annotations[LaunchAnnotation])
			require.NoError(t, decodeErr)
			require.True(t, checkpoint.Submitted)
			require.Equal(t, testAccountID, checkpoint.AccountID)
			payload := map[string]interface{}{}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
			require.Equal(t, config.Name, payload["name"])
			boot := payload["boot_volume_attachment"].(map[string]interface{})
			require.Equal(t, false, boot["delete_volume_on_instance_delete"])
			volume := boot["volume"].(map[string]interface{})
			require.LessOrEqual(t, len(volume["name"].(string)), 63)
			require.NotContains(t, volume["name"], claim.Name)
			require.Contains(t, volume["user_tags"], ownership.ClusterUIDTag+":cluster-uid")
			require.NotContains(t, volume["user_tags"], ownership.ClusterUIDTag+":foreign")
			require.Contains(t, volume["user_tags"], ownership.RetainTag+":true")
			created = true
			posts++
			status = http.StatusInternalServerError
			result = map[string]interface{}{"errors": []map[string]string{{"code": "internal_error", "message": "response lost"}}}
		default:
			t.Fatalf("unexpected cloud operation: %s %s", request.Method, request.URL.Path)
		}
		body, marshalErr := json.Marshal(result)
		require.NoError(t, marshalErr)
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})})
	vpc := ibm.NewVPCClientWithMock(sdk, &recordingTagger{})
	provider := &VPCInstanceProvider{kubeClient: kube, apiReader: kube, vpcClientManager: vpcclient.NewManagerWithMockClient(vpc), instanceCache: cache.New(time.Hour), accountResolver: testAccountResolver}
	provider.subnetProvider = placementSubnets{{ID: "subnet", Zone: "us-south-1", State: "available", AvailableIPs: 20}}
	profile := placementProfile("bx2-2x8", "us-south-1")
	profile.Overhead = &cloudprovider.InstanceTypeOverhead{}
	node, err := provider.Create(ctx, claim, []*cloudprovider.InstanceType{profile})
	require.NoError(t, err)
	require.Equal(t, "ibm:///us-south/instance", node.Spec.ProviderID)
	_, err = provider.Create(ctx, claim, nil)
	require.NoError(t, err)
	require.Equal(t, 1, posts)
}

func TestCreateValidatesCloudTagsBeforeCheckpointAndPersistsRemoteRejection(t *testing.T) {
	for _, fixture := range []string{"invalid-volume-tag", "invalid-instance-tag", "remote-rejection"} {
		t.Run(fixture, func(t *testing.T) {
			t.Setenv("IBM_ACCOUNT_ID", "")
			ctx := context.Background()
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))
			scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
			require.NoError(t, v1alpha1.AddToScheme(scheme))
			claim := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", UID: "claim-uid"}, Spec: karpv1.NodeClaimSpec{NodeClassRef: &karpv1.NodeClassReference{Name: "class"}}}
			class := &v1alpha1.IBMNodeClass{ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "class-uid", Generation: 1}, Spec: v1alpha1.IBMNodeClassSpec{Region: "us-south", Zone: "us-south-1", Subnet: "subnet", VPC: "vpc", ResourceGroup: strings.Repeat("a", 32), SecurityGroups: []string{"security-group"}, Image: "image", UserData: "#!/bin/sh\ntrue"}, Status: v1alpha1.IBMNodeClassStatus{ResolvedImageID: "image", Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, ObservedGeneration: 1}}}}
			if fixture == "invalid-volume-tag" {
				class.Spec.BlockDeviceMappings = []v1alpha1.BlockDeviceMapping{{RootVolume: true, VolumeSpec: &v1alpha1.VolumeSpec{Tags: []string{"custom:invalid/value"}}}}
			}
			if fixture == "invalid-instance-tag" {
				class.Spec.Tags = map[string]string{"custom": "invalid/value"}
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(class).WithObjects(claim, class, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}}).Build()
			sdk, sdkErr := vpcv1.NewVpcV1(&vpcv1.VpcV1Options{URL: "https://test.iaas.cloud.ibm.com/v1", Authenticator: &core.NoAuthAuthenticator{}})
			require.NoError(t, sdkErr)
			posts := 0
			sdk.Service.SetHTTPClient(&http.Client{Transport: launchTransport(func(request *http.Request) (*http.Response, error) {
				status := http.StatusOK
				body := `{"instances":[]}`
				switch request.Method {
				case http.MethodGet:
					require.Equal(t, "/v1/instances", request.URL.Path)
				case http.MethodPost:
					posts++
					require.Equal(t, "/v1/instances", request.URL.Path)
					payload := map[string]any{}
					require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
					volume := payload["boot_volume_attachment"].(map[string]any)["volume"].(map[string]any)
					for _, tag := range volume["user_tags"].([]any) {
						require.NoError(t, ownership.ValidateTag(tag.(string)))
						require.NotContains(t, tag, "/")
					}
					status = http.StatusBadRequest
					body = `{"errors":[{"code":"validation_failed","message":"Expected only one oneOf fields to be set: got 0"}]}`
				default:
					t.Fatalf("unexpected cloud operation: %s", request.Method)
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})})
			vpc := ibm.NewVPCClientWithMock(sdk, &recordingTagger{})
			provider := &VPCInstanceProvider{kubeClient: kube, apiReader: kube, vpcClientManager: vpcclient.NewManagerWithMockClient(vpc), instanceCache: cache.New(time.Hour), accountResolver: testAccountResolver}
			provider.subnetProvider = placementSubnets{{ID: "subnet", Zone: "us-south-1", State: "available", AvailableIPs: 20}}
			profile := placementProfile("bx2-2x8", "us-south-1")
			profile.Overhead = &cloudprovider.InstanceTypeOverhead{}
			_, err := provider.Create(ctx, claim, []*cloudprovider.InstanceType{profile})
			require.Error(t, err)
			fresh := &karpv1.NodeClaim{}
			require.NoError(t, kube.Get(ctx, client.ObjectKeyFromObject(claim), fresh))
			if fixture != "remote-rejection" {
				require.Zero(t, posts)
				require.Empty(t, fresh.Annotations[LaunchAnnotation])
				require.NotContains(t, fresh.Finalizers, LaunchFinalizer)
				return
			}
			require.Equal(t, 1, posts)
			checkpoint, checkpointErr := decodeLaunch(fresh.Annotations[LaunchAnnotation])
			require.NoError(t, checkpointErr)
			require.True(t, checkpoint.Submitted)
			require.True(t, checkpoint.Rejected)
			completed, cleanupErr := provider.CleanupPending(ctx, fresh)
			require.NoError(t, cleanupErr)
			require.True(t, completed)
		})
	}
}

func getInstance(id string) gomock.Matcher {
	return gomock.Cond(func(options *vpcv1.GetInstanceOptions) bool {
		return options != nil && options.ID != nil && *options.ID == id
	})
}

func listByName(name string) gomock.Matcher {
	return gomock.Cond(func(options *vpcv1.ListInstancesOptions) bool {
		return options != nil && options.Name != nil && *options.Name == name
	})
}

func storeLaunch(t *testing.T, provider *VPCInstanceProvider, claim *karpv1.NodeClaim, config *launchConfig) {
	t.Helper()
	value, err := json.Marshal(config)
	require.NoError(t, err)
	claim.Annotations[LaunchAnnotation] = string(value)
	require.NoError(t, provider.kubeClient.Update(context.Background(), claim))
}

func TestCreateDiscardsCheckpointsThatCreatedNothing(t *testing.T) {
	for name, mutate := range map[string]func(*launchConfig){
		"unsubmitted": func(config *launchConfig) { config.Submitted = false },
		"rejected":    func(config *launchConfig) { config.Rejected = true },
		"abandoned":   func(config *launchConfig) { config.SubmittedAt = time.Now().Add(-2 * launchResolutionWindow) },
	} {
		t.Run(name, func(t *testing.T) {
			provider, claim, config, mock, _ := launchFixture(t)
			mutate(config)
			storeLaunch(t, provider, claim, config)
			if config.Submitted && !config.Rejected {
				mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{}, nil, nil)
			}
			_, err := provider.Create(context.Background(), claim, nil)
			require.ErrorContains(t, err, "retrying")
			fresh := &karpv1.NodeClaim{}
			require.NoError(t, provider.kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
			require.Empty(t, fresh.Annotations[LaunchAnnotation])
			require.Empty(t, fresh.Annotations[ownership.BackendAnnotation])
			require.NotContains(t, fresh.Finalizers, LaunchFinalizer)
		})
	}
}

func TestPendingCleanupReleasesAbandonedSubmission(t *testing.T) {
	provider, claim, config, mock, _ := launchFixture(t)
	config.SubmittedAt = time.Now().Add(-2 * launchResolutionWindow)
	storeLaunch(t, provider, claim, config)
	mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{}, nil, nil)
	complete, err := provider.CleanupPending(context.Background(), claim)
	require.NoError(t, err)
	require.True(t, complete)
}

func TestPendingCleanupDeletesUnregisteredInstance(t *testing.T) {
	provider, claim, config, mock, _ := launchFixture(t)
	instance := matchingInstance(config)
	mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{*instance}}, nil, nil).Times(2)
	gomock.InOrder(
		mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(instance, nil, nil).Times(3),
		mock.EXPECT().DeleteInstanceWithContext(gomock.Any(), gomock.Cond(func(options *vpcv1.DeleteInstanceOptions) bool { return *options.ID == "instance" })).Return(nil, nil),
		mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(nil, nil, &ibm.IBMError{StatusCode: 404}),
	)
	complete, err := provider.CleanupPending(context.Background(), claim)
	require.NoError(t, err)
	require.True(t, complete)
}

func TestMarkLaunchRejectedSurvivesConflict(t *testing.T) {
	provider, claim, config, _, _ := launchFixture(t)
	stale := claim.DeepCopy()
	claim.Labels = map[string]string{"unrelated": "write"}
	require.NoError(t, provider.kubeClient.Update(context.Background(), claim))
	require.NoError(t, provider.markLaunchRejected(context.Background(), stale, config))
	fresh := &karpv1.NodeClaim{}
	require.NoError(t, provider.kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
	stored, err := decodeLaunch(fresh.Annotations[LaunchAnnotation])
	require.NoError(t, err)
	require.True(t, stored.Rejected)
}

func TestInstanceWithoutCRNFailsAccountProof(t *testing.T) {
	require.Error(t, verifyInstanceAccount(&vpcv1.Instance{}, testAccountID))
}

func TestLaunchOwnershipVerification(t *testing.T) {
	for _, scenario := range []string{"stopped owned", "missing tags", "foreign tags", "changed claim"} {
		t.Run(scenario, func(t *testing.T) {
			provider, claim, config, mock, tagger := launchFixture(t)
			vm := matchingInstance(config)
			vm.Status = core.StringPtr("stopped")
			for key, value := range ownership.VPCTags(config.ClusterUID, config.ClaimUID, config.ClassUID) {
				tagger.names = append(tagger.names, key+":"+value)
			}
			switch scenario {
			case "missing tags":
				tagger.names = nil
			case "foreign tags":
				tagger.names = append(tagger.names, ownership.ClaimUIDTag+":another-claim")
			case "changed claim":
				tagger.onList = func() {
					fresh := claim.DeepCopy()
					require.NoError(t, provider.kubeClient.Get(context.Background(), client.ObjectKeyFromObject(claim), fresh))
					fresh.Annotations[LaunchAnnotation] = "changed"
					require.NoError(t, provider.kubeClient.Update(context.Background(), fresh))
				}
			}
			mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{*vm}}, nil, nil)
			mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(vm, nil, nil)
			actual, err := provider.VerifyLaunchInstance(context.Background(), claim)
			if scenario == "stopped owned" {
				require.NoError(t, err)
				require.Equal(t, "instance", *actual.ID)
			} else {
				require.Error(t, err)
				require.Nil(t, actual)
			}
		})
	}
}

func TestLaunchOwnershipRequiresExactZonalInstanceCRN(t *testing.T) {
	valid := "crn:v1:bluemix:public:is:us-south-1:a/" + testAccountID + "::instance:instance"
	cases := []struct {
		name, crn string
		valid     bool
	}{
		{"owned zonal instance", valid, true},
		{"regional location", strings.Replace(valid, "us-south-1", "us-south", 1), false},
		{"other zone", strings.Replace(valid, "us-south-1", "us-south-2", 1), false},
		{"other region", strings.Replace(valid, "us-south-1", "eu-de-1", 1), false},
		{"other account", strings.Replace(valid, testAccountID, strings.Repeat("b", 32), 1), false},
		{"other service", strings.Replace(valid, ":is:", ":foreign:", 1), false},
		{"other resource type", strings.Replace(valid, "::instance:", "::volume:", 1), false},
		{"other instance", strings.Replace(valid, "::instance:instance", "::instance:another", 1), false},
		{"other namespace", strings.Replace(valid, ":bluemix:", ":foreign:", 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, claim, config, mock, tagger := launchFixture(t)
			vm := matchingInstance(config)
			vm.CRN = core.StringPtr(tc.crn)
			for key, value := range ownership.VPCTags(config.ClusterUID, config.ClaimUID, config.ClassUID) {
				tagger.names = append(tagger.names, key+":"+value)
			}
			tagReads := 0
			tagger.onList = func() { tagReads++ }
			mock.EXPECT().ListInstancesWithContext(gomock.Any(), listByName(config.Name)).Return(&vpcv1.InstanceCollection{Instances: []vpcv1.Instance{*vm}}, nil, nil)
			mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(vm, nil, nil)
			actual, err := provider.VerifyLaunchInstance(context.Background(), claim)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, vm, actual)
				require.Equal(t, 1, tagReads)
			} else {
				require.ErrorContains(t, err, "CRN")
				require.Nil(t, actual)
				require.Zero(t, tagReads)
			}
		})
	}
}

func TestLaunchRetargetingAndFutureStateNeverReachCloud(t *testing.T) {
	provider, claim, config, _, _ := launchFixture(t)
	foreign := claim.DeepCopy()
	foreign.Status.ProviderID = "ibm:///us-south/foreign"
	_, err := provider.VerifyLaunchInstance(context.Background(), foreign)
	require.Error(t, err)
	config.Version = 2
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	claim.Annotations[LaunchAnnotation] = string(encoded)
	_, err = decodeLaunch(string(encoded))
	require.Error(t, err)
	require.NoError(t, provider.kubeClient.Update(context.Background(), claim))
	_, err = provider.VerifyLaunchInstance(context.Background(), claim)
	require.Error(t, err)
}

func legacyDeleteFixture(t *testing.T) (*VPCInstanceProvider, *corev1.Node, *vpcv1.Instance, *mockibm.MockvpcClientInterface) {
	t.Helper()
	provider, claim, _, mock, _ := launchFixture(t)
	delete(claim.Annotations, LaunchAnnotation)
	claim.Annotations[ownership.AccountIDAnnotation] = testAccountID
	claim.Status.ProviderID = "ibm:///us-south/instance"
	require.NoError(t, provider.kubeClient.Update(context.Background(), claim))
	vm := &vpcv1.Instance{ID: core.StringPtr("instance"), Name: core.StringPtr(claim.Name), CRN: core.StringPtr("crn:v1:bluemix:public:is:us-south-1:a/" + testAccountID + "::instance:instance")}
	node := &corev1.Node{ObjectMeta: *claim.ObjectMeta.DeepCopy(), Spec: corev1.NodeSpec{ProviderID: claim.Status.ProviderID}}
	return provider, node, vm, mock
}

func TestDeleteLegacyInstanceWithoutLaunchCheckpoint(t *testing.T) {
	provider, node, vm, mock := legacyDeleteFixture(t)
	gomock.InOrder(
		mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(vm, nil, nil),
		mock.EXPECT().DeleteInstanceWithContext(gomock.Any(), gomock.Cond(func(options *vpcv1.DeleteInstanceOptions) bool { return *options.ID == "instance" })).Return(nil, nil),
		mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(nil, nil, &ibm.IBMError{StatusCode: 404}),
	)
	err := provider.Delete(context.Background(), node)
	require.True(t, cloudprovider.IsNodeClaimNotFoundError(err), "got %v", err)
}

func TestDeleteLegacyInstanceAlreadyGone(t *testing.T) {
	provider, node, _, mock := legacyDeleteFixture(t)
	mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(nil, nil, &ibm.IBMError{StatusCode: 404})
	err := provider.Delete(context.Background(), node)
	require.True(t, cloudprovider.IsNodeClaimNotFoundError(err), "got %v", err)
}

func TestDeleteLegacyInstanceRefusesUnprovenTargets(t *testing.T) {
	for name, mutate := range map[string]func(*vpcv1.Instance){
		"other name": func(vm *vpcv1.Instance) { vm.Name = core.StringPtr("other-claim") },
		"foreign account": func(vm *vpcv1.Instance) {
			vm.CRN = core.StringPtr("crn:v1:bluemix:public:is:us-south-1:a/" + strings.Repeat("b", 32) + "::instance:instance")
		},
		"other instance": func(vm *vpcv1.Instance) { vm.ID = core.StringPtr("other") },
	} {
		t.Run(name, func(t *testing.T) {
			provider, node, vm, mock := legacyDeleteFixture(t)
			mutate(vm)
			mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(vm, nil, nil)
			err := provider.Delete(context.Background(), node)
			require.Error(t, err)
			require.False(t, cloudprovider.IsNodeClaimNotFoundError(err))
		})
	}
}

func TestDeleteLegacyInstanceTransientLookupIsNotNotFound(t *testing.T) {
	provider, node, _, mock := legacyDeleteFixture(t)
	mock.EXPECT().GetInstanceWithContext(gomock.Any(), getInstance("instance")).Return(nil, nil, &ibm.IBMError{StatusCode: 503})
	err := provider.Delete(context.Background(), node)
	require.Error(t, err)
	require.False(t, cloudprovider.IsNodeClaimNotFoundError(err))
}
