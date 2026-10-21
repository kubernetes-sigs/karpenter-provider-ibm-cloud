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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

type placementCandidate struct {
	profile *cloudprovider.InstanceType
	subnet  subnet.SubnetInfo
}

type zoneReservations struct {
	Version              int               `json:"version"`
	MinimumWriterVersion int               `json:"minimumWriterVersion"`
	ClusterUID           string            `json:"clusterUID"`
	ClassUID             string            `json:"classUID"`
	Claims               map[string]string `json:"claims"`
}

func (p *VPCInstanceProvider) selectPlacement(ctx context.Context, claim *karpv1.NodeClaim, class *v1alpha1.IBMNodeClass, clusterUID string, profiles []*cloudprovider.InstanceType) (*cloudprovider.InstanceType, subnet.SubnetInfo, error) {
	if p.subnetProvider == nil {
		return nil, subnet.SubnetInfo{}, fmt.Errorf("subnet provider is required")
	}
	var subnets []subnet.SubnetInfo
	if class.Spec.Subnet != "" {
		info, err := p.subnetProvider.GetSubnet(ctx, class.Spec.Subnet, class.Spec.Region)
		if err != nil {
			return nil, subnet.SubnetInfo{}, err
		}
		subnets = append(subnets, *info)
	} else if len(class.Status.SelectedSubnets) > 0 && class.Spec.Zone == "" {
		for _, id := range class.Status.SelectedSubnets {
			info, err := p.subnetProvider.GetSubnet(ctx, id, class.Spec.Region)
			if err != nil {
				return nil, subnet.SubnetInfo{}, err
			}
			subnets = append(subnets, *info)
		}
	} else if class.Spec.PlacementStrategy != nil && class.Spec.Zone == "" {
		var err error
		subnets, err = p.subnetProvider.SelectSubnets(ctx, class.Spec.VPC, class.Spec.PlacementStrategy, class.Spec.Region)
		if err != nil {
			return nil, subnet.SubnetInfo{}, err
		}
	} else {
		var err error
		subnets, err = p.subnetProvider.ListSubnets(ctx, class.Spec.VPC, class.Spec.Region)
		if err != nil {
			return nil, subnet.SubnetInfo{}, err
		}
	}
	requirements := scheduling.NewNodeSelectorRequirementsWithMinValues(claim.Spec.Requirements...)
	var candidates []placementCandidate
	for _, profile := range profiles {
		if profile == nil || profile.Name == "" || (class.Spec.InstanceProfile != "" && class.Spec.InstanceProfile != profile.Name) {
			continue
		}
		if requirements.Compatible(profile.Requirements, scheduling.AllowUndefinedWellKnownLabels) != nil {
			continue
		}
		zones := map[string]bool{}
		for _, offering := range profile.Offerings.Compatible(requirements).Available() {
			for _, zone := range offering.Requirements.Get(corev1.LabelTopologyZone).Values() {
				zones[zone] = true
			}
		}
		for _, info := range subnets {
			if info.State != "available" || info.AvailableIPs <= 0 || !zones[info.Zone] {
				continue
			}
			if class.Spec.Zone != "" && class.Spec.Zone != info.Zone {
				continue
			}
			candidates = append(candidates, placementCandidate{profile: profile, subnet: info})
		}
	}
	if len(candidates) == 0 {
		return nil, subnet.SubnetInfo{}, fmt.Errorf("no subnet and available offering satisfy NodeClaim requirements")
	}
	zone := ""
	if class.Spec.PlacementStrategy != nil && class.Spec.PlacementStrategy.ZoneBalance == "Balanced" {
		eligible := map[string]bool{}
		for _, candidate := range candidates {
			eligible[candidate.subnet.Zone] = true
		}
		var err error
		zone, err = p.reserveBalancedZone(ctx, claim, class, clusterUID, eligible)
		if err != nil {
			return nil, subnet.SubnetInfo{}, err
		}
	}
	var selected *placementCandidate
	for i := range candidates {
		candidate := &candidates[i]
		if zone != "" && candidate.subnet.Zone != zone {
			continue
		}
		if selected == nil || (candidate.profile.Name == selected.profile.Name && (candidate.subnet.AvailableIPs > selected.subnet.AvailableIPs || (candidate.subnet.AvailableIPs == selected.subnet.AvailableIPs && candidate.subnet.ID < selected.subnet.ID))) {
			selected = candidate
		}
	}
	if selected == nil {
		return nil, subnet.SubnetInfo{}, fmt.Errorf("reserved zone has no compatible offering")
	}
	profile := selected.profile.DeepCopy()
	profile.Offerings = nil
	for _, offering := range selected.profile.Offerings.Compatible(requirements).Available() {
		if offering.Requirements.Get(corev1.LabelTopologyZone).Has(selected.subnet.Zone) {
			profile.Offerings = append(profile.Offerings, offering)
		}
	}
	return profile, selected.subnet, nil
}

func (p *VPCInstanceProvider) reserveBalancedZone(ctx context.Context, claim *karpv1.NodeClaim, class *v1alpha1.IBMNodeClass, clusterUID string, eligible map[string]bool) (string, error) {
	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = "karpenter"
	}
	digest := sha256.Sum256([]byte(clusterUID + "/" + string(class.UID)))
	key := types.NamespacedName{Namespace: namespace, Name: fmt.Sprintf("vpc-zones-%x", digest[:20])}
	chosen := ""
	// Bounded growth: at most ~10s per sleep and ~20s in total before the launch is retried.
	backoff := wait.Backoff{Duration: 20 * time.Millisecond, Factor: 2, Jitter: 1, Steps: 10}
	err := retry.OnError(backoff, func(err error) bool { return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) }, func() error {
		freshClaim := &karpv1.NodeClaim{}
		if err := p.reader().Get(ctx, client.ObjectKeyFromObject(claim), freshClaim); err != nil {
			return err
		}
		if freshClaim.UID != claim.UID || !freshClaim.DeletionTimestamp.IsZero() {
			return fmt.Errorf("claim changed while reserving placement")
		}
		freshClass := &v1alpha1.IBMNodeClass{}
		if err := p.reader().Get(ctx, client.ObjectKeyFromObject(class), freshClass); err != nil {
			return err
		}
		if freshClass.UID != class.UID || freshClass.Generation != class.Generation || !freshClass.DeletionTimestamp.IsZero() {
			return fmt.Errorf("class changed while reserving placement")
		}
		reservation := &corev1.ConfigMap{}
		err := p.reader().Get(ctx, key, reservation)
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return err
		}
		state := zoneReservations{Version: 1, MinimumWriterVersion: 1, ClusterUID: clusterUID, ClassUID: string(class.UID), Claims: map[string]string{}}
		if !create {
			decoder := json.NewDecoder(strings.NewReader(reservation.Data["reservations"]))
			decoder.DisallowUnknownFields()
			if decodeErr := decoder.Decode(&state); decodeErr != nil {
				return fmt.Errorf("invalid placement reservation: %w", decodeErr)
			}
			if trailingErr := decoder.Decode(&struct{}{}); trailingErr != io.EOF {
				return fmt.Errorf("invalid trailing placement reservation")
			}
			if state.Version != 1 || state.MinimumWriterVersion != 1 || state.ClusterUID != clusterUID || state.ClassUID != string(class.UID) || state.Claims == nil {
				return fmt.Errorf("unsupported or foreign placement reservation")
			}
		} else {
			reservation = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: map[string]string{ownership.ClusterUIDLabel: clusterUID, ownership.NodeClassUIDLabel: string(class.UID)}, OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "IBMNodeClass", Name: class.Name, UID: class.UID}}}}
		}
		claims := &karpv1.NodeClaimList{}
		if listErr := p.reader().List(ctx, claims); listErr != nil {
			return listErr
		}
		nodes := &corev1.NodeList{}
		if listErr := p.reader().List(ctx, nodes); listErr != nil {
			return listErr
		}
		live := map[string]bool{string(claim.UID): true}
		for _, current := range claims.Items {
			live[string(current.UID)] = true
			if current.Spec.NodeClassRef == nil || current.Spec.NodeClassRef.Name != class.Name {
				continue
			}
			if raw := current.Annotations[LaunchAnnotation]; raw != "" {
				launch, decodeErr := decodeLaunch(raw)
				if decodeErr != nil {
					return fmt.Errorf("cannot account for claim %s placement: %w", current.Name, decodeErr)
				}
				if launch.ClassUID == string(class.UID) && launch.ClusterUID == clusterUID {
					state.Claims[string(current.UID)] = launch.Zone
				}
			}
		}
		for _, node := range nodes.Items {
			uid := node.Labels[ownership.ClaimUIDLabel]
			zone := node.Labels[corev1.LabelTopologyZone]
			owned := node.Labels[ownership.NodeClassUIDLabel] == string(class.UID) && node.Labels[ownership.ClusterUIDLabel] == clusterUID
			if raw := node.Annotations[LaunchAnnotation]; raw != "" {
				launch, decodeErr := decodeLaunch(raw)
				if decodeErr != nil {
					return decodeErr
				}
				if launch.ClassUID == string(class.UID) && launch.ClusterUID == clusterUID {
					owned = true
					uid = launch.ClaimUID
					zone = launch.Zone
				}
			}
			if owned && uid != "" {
				live[uid] = true
				if zone != "" {
					state.Claims[uid] = zone
				}
			}
		}

		for uid := range state.Claims {
			if !live[uid] {
				delete(state.Claims, uid)
			}
		}
		chosen = state.Claims[string(claim.UID)]
		if chosen != "" && !eligible[chosen] {
			return fmt.Errorf("reserved zone %s no longer has an allowed offering", chosen)
		}
		if chosen == "" {
			counts := map[string]int{}
			for _, zone := range state.Claims {
				counts[zone]++
			}
			zones := make([]string, 0, len(eligible))
			for zone := range eligible {
				zones = append(zones, zone)
			}
			sort.Strings(zones)
			for _, zone := range zones {
				if chosen == "" || counts[zone] < counts[chosen] {
					chosen = zone
				}
			}
			state.Claims[string(claim.UID)] = chosen
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			return err
		}
		reservation.Data = map[string]string{"reservations": string(encoded)}
		if create {
			return p.kubeClient.Create(ctx, reservation)
		}
		return p.kubeClient.Update(ctx, reservation)
	})
	return chosen, err
}
