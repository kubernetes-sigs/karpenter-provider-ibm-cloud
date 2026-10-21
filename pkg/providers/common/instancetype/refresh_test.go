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

package instancetype

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestRegionalRawCacheSharesFetchAndKeepsClassPodLimits(t *testing.T) {
	ctrl := gomock.NewController(t)
	mock, manager := newMockVPCManager(t, ctrl)
	profile := makeVPCProfile("bx2-2x8", 2, 8, "amd64")
	mock.EXPECT().ListInstanceProfilesWithContext(gomock.Any(), gomock.Any()).Return(&vpcv1.InstanceProfileCollection{Profiles: []vpcv1.InstanceProfile{profile}}, &core.DetailedResponse{StatusCode: 200}, nil).Times(2)
	provider := &IBMInstanceTypeProvider{client: &MockIBMClient{}, pricingProvider: &MockPricingProvider{}, vpcClientManager: manager, zonesCache: map[string][]string{"us-south": {"us-south-1"}, "eu-de": {"eu-de-2"}}, zonesCacheTime: map[string]time.Time{"us-south": time.Now(), "eu-de": time.Now()}}
	var workers sync.WaitGroup
	errors := make(chan error, 20)
	for range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, err := provider.List(context.Background(), &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{Region: "eu-de"}})
			errors <- err
		}()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	max := int32(10)
	limited, err := provider.Get(context.Background(), "bx2-2x8", &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{Region: "eu-de", Kubelet: &v1alpha1.KubeletConfiguration{MaxPods: &max}}})
	require.NoError(t, err)
	require.Equal(t, int64(10), limited.Capacity.Pods().Value())
	for _, offering := range limited.Offerings {
		require.Equal(t, []string{"eu-de-2"}, offering.Requirements.Get(corev1.LabelTopologyZone).Values())
	}
	defaultClass, err := provider.Get(context.Background(), "bx2-2x8", nil)
	require.NoError(t, err)
	require.Equal(t, int64(110), defaultClass.Capacity.Pods().Value())
	for _, offering := range defaultClass.Offerings {
		require.Equal(t, []string{"us-south-1"}, offering.Requirements.Get(corev1.LabelTopologyZone).Values())
	}
}

func TestRefreshFailureKeepsPreviousProfiles(t *testing.T) {
	ctrl := gomock.NewController(t)
	mock, manager := newMockVPCManager(t, ctrl)
	profile := makeVPCProfile("bx2-2x8", 2, 8, "amd64")
	gomock.InOrder(
		mock.EXPECT().ListInstanceProfilesWithContext(gomock.Any(), gomock.Any()).Return(&vpcv1.InstanceProfileCollection{Profiles: []vpcv1.InstanceProfile{profile}}, &core.DetailedResponse{StatusCode: 200}, nil),
		mock.EXPECT().ListInstanceProfilesWithContext(gomock.Any(), gomock.Any()).Return(nil, &core.DetailedResponse{StatusCode: 403}, errors.New("forbidden")),
	)
	provider := &IBMInstanceTypeProvider{client: &MockIBMClient{}, pricingProvider: &MockPricingProvider{}, vpcClientManager: manager}
	region := provider.regionForClass(nil)
	_, err := provider.rawProfiles(context.Background(), region)
	require.NoError(t, err)

	require.Error(t, provider.Refresh(context.Background()))
	profiles, err := provider.rawProfiles(context.Background(), region)
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	require.Equal(t, "bx2-2x8", *profiles[0].Name)
}

func TestRefreshFailureWithoutPreviousProfilesFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	mock, manager := newMockVPCManager(t, ctrl)
	mock.EXPECT().ListInstanceProfilesWithContext(gomock.Any(), gomock.Any()).Return(nil, &core.DetailedResponse{StatusCode: 403}, errors.New("forbidden"))
	provider := &IBMInstanceTypeProvider{client: &MockIBMClient{}, pricingProvider: &MockPricingProvider{}, vpcClientManager: manager}
	_, err := provider.rawProfiles(context.Background(), provider.regionForClass(nil))
	require.Error(t, err)
}
