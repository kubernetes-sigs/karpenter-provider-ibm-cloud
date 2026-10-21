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
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
)

type placementSubnets []subnet.SubnetInfo

func (s placementSubnets) GetSubnet(_ context.Context, id string, regions ...string) (*subnet.SubnetInfo, error) {
	for _, info := range s {
		if info.ID == id {
			return &info, nil
		}
	}
	return nil, fmt.Errorf("missing subnet")
}
func (s placementSubnets) ListSubnets(context.Context, string, ...string) ([]subnet.SubnetInfo, error) {
	return s, nil
}
func (s placementSubnets) SelectSubnets(context.Context, string, *v1alpha1.PlacementStrategy, ...string) ([]subnet.SubnetInfo, error) {
	return s, nil
}
func (s placementSubnets) SetKubernetesClient(kubernetes.Interface) {}

func placementProfile(name string, zones ...string) *cloudprovider.InstanceType {
	profile := &cloudprovider.InstanceType{Name: name, Requirements: scheduling.NewLabelRequirements(map[string]string{corev1.LabelInstanceTypeStable: name})}
	for _, zone := range zones {
		profile.Offerings = append(profile.Offerings, &cloudprovider.Offering{Available: true, Requirements: scheduling.NewLabelRequirements(map[string]string{corev1.LabelTopologyZone: zone, karpv1.CapacityTypeLabelKey: karpv1.CapacityTypeOnDemand})})
	}
	return profile
}

func TestPlacementIntersectsClaimZonesAndAvailableOfferings(t *testing.T) {
	provider := &VPCInstanceProvider{subnetProvider: placementSubnets{{ID: "one", Zone: "us-south-1", State: "available", AvailableIPs: 20}, {ID: "two", Zone: "us-south-2", State: "available", AvailableIPs: 40}}}
	class := &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{Region: "us-south", VPC: "vpc"}, Status: v1alpha1.IBMNodeClassStatus{SelectedSubnets: []string{"one", "two"}}}
	claim := &karpv1.NodeClaim{Spec: karpv1.NodeClaimSpec{Requirements: []karpv1.NodeSelectorRequirementWithMinValues{{Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpIn, Values: []string{"us-south-1"}}}}}
	unavailable := placementProfile("unavailable", "us-south-1")
	unavailable.Offerings[0].Available = false
	profile, selected, err := provider.selectPlacement(context.Background(), claim, class, "cluster", []*cloudprovider.InstanceType{unavailable, placementProfile("wrong-zone", "us-south-2"), placementProfile("fits", "us-south-1", "us-south-2")})
	require.NoError(t, err)
	require.Equal(t, "fits", profile.Name)
	require.Equal(t, "us-south-1", selected.Zone)
	mixed := placementProfile("mixed", "us-south-1", "us-south-2")
	mixed.Offerings[1].Requirements = scheduling.NewLabelRequirements(map[string]string{corev1.LabelTopologyZone: "us-south-2", karpv1.CapacityTypeLabelKey: karpv1.CapacityTypeSpot})
	profile, _, err = provider.selectPlacement(context.Background(), claim, class, "cluster", []*cloudprovider.InstanceType{mixed})
	require.NoError(t, err)
	require.Len(t, profile.Offerings, 1)
	require.Equal(t, []string{karpv1.CapacityTypeOnDemand}, profile.Offerings[0].Requirements.Get(karpv1.CapacityTypeLabelKey).Values())
	class.Spec.Zone = "us-south-2"
	_, _, err = provider.selectPlacement(context.Background(), claim, class, "cluster", []*cloudprovider.InstanceType{placementProfile("fits", "us-south-1", "us-south-2")})
	require.Error(t, err)
}

func TestBalancedReservationsCoordinateProvidersAndSurviveRestart(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	class := &v1alpha1.IBMNodeClass{ObjectMeta: metav1.ObjectMeta{Name: "class", UID: "class-uid", Generation: 1}}
	objects := []client.Object{class}
	claims := make([]*karpv1.NodeClaim, 12)
	for i := range claims {
		claims[i] = &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("claim-%02d", i), UID: types.UID(fmt.Sprintf("uid-%02d", i))}, Spec: karpv1.NodeClaimSpec{NodeClassRef: &karpv1.NodeClassReference{Name: class.Name}}}
		objects = append(objects, claims[i])
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	eligible := map[string]bool{"us-south-1": true, "us-south-2": true, "us-south-3": true}
	zones := make(chan string, len(claims))
	errors := make(chan error, len(claims))
	var workers sync.WaitGroup
	for _, claim := range claims {
		workers.Add(1)
		go func() {
			defer workers.Done()
			provider := &VPCInstanceProvider{kubeClient: kube, apiReader: kube}
			zone, err := provider.reserveBalancedZone(ctx, claim, class, "cluster-uid", eligible)
			zones <- zone
			errors <- err
		}()
	}
	workers.Wait()
	close(zones)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	counts := map[string]int{}
	for zone := range zones {
		counts[zone]++
	}
	require.Equal(t, map[string]int{"us-south-1": 4, "us-south-2": 4, "us-south-3": 4}, counts)
	provider := &VPCInstanceProvider{kubeClient: kube, apiReader: kube}
	first, err := provider.reserveBalancedZone(ctx, claims[0], class, "cluster-uid", eligible)
	require.NoError(t, err)
	second, err := provider.reserveBalancedZone(ctx, claims[0], class, "cluster-uid", eligible)
	require.NoError(t, err)
	require.Equal(t, first, second)
	delete(eligible, first)
	_, err = provider.reserveBalancedZone(ctx, claims[0], class, "cluster-uid", eligible)
	require.Error(t, err)
}

func TestVolumeNamesAreUniqueAcrossClaimsAndDuplicateRootsFail(t *testing.T) {
	name := "data-disk"
	class := &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{BlockDeviceMappings: []v1alpha1.BlockDeviceMapping{{DeviceName: &name, VolumeSpec: &v1alpha1.VolumeSpec{}}}}}
	provider := &VPCInstanceProvider{}
	_, first, err := provider.buildVolumeAttachments(class, "claim-one", "us-south-1")
	require.NoError(t, err)
	_, second, err := provider.buildVolumeAttachments(class, "claim-two", "us-south-1")
	require.NoError(t, err)
	require.Equal(t, name, *first[0].Name)
	require.Equal(t, name, *second[0].Name)
	firstJSON, err := json.Marshal(first[0].Volume)
	require.NoError(t, err)
	secondJSON, err := json.Marshal(second[0].Volume)
	require.NoError(t, err)
	require.Contains(t, string(firstJSON), "claim-one-data-0")
	require.Contains(t, string(secondJSON), "claim-two-data-0")
	class.Spec.BlockDeviceMappings = []v1alpha1.BlockDeviceMapping{{RootVolume: true}, {RootVolume: true}}
	_, _, err = provider.buildVolumeAttachments(class, "claim", "us-south-1")
	require.Error(t, err)
}
