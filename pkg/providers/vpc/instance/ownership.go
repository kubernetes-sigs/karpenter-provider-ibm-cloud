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
	"fmt"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func (p *VPCInstanceProvider) freshLaunchClaim(ctx context.Context, claim *karpv1.NodeClaim) (*karpv1.NodeClaim, *launchConfig, error) {
	if claim == nil || claim.UID == "" || p.reader() == nil {
		return nil, nil, fmt.Errorf("persisted NodeClaim identity is required")
	}
	fresh := &karpv1.NodeClaim{}
	if err := p.reader().Get(ctx, client.ObjectKeyFromObject(claim), fresh); err != nil {
		return nil, nil, err
	}
	if fresh.UID != claim.UID || fresh.Status.ProviderID != claim.Status.ProviderID || fresh.Annotations[LaunchAnnotation] != claim.Annotations[LaunchAnnotation] {
		return nil, nil, fmt.Errorf("NodeClaim launch identity changed")
	}
	config, err := decodeLaunch(fresh.Annotations[LaunchAnnotation])
	if err != nil {
		return nil, nil, err
	}
	if config.ClassUID == "" {
		return nil, nil, fmt.Errorf("launch NodeClass identity is missing")
	}
	if err := p.validateLaunchTarget(ctx, fresh, config); err != nil {
		return nil, nil, err
	}
	return fresh, config, nil
}

func (p *VPCInstanceProvider) VerifyLaunchInstance(ctx context.Context, claim *karpv1.NodeClaim) (*vpcv1.Instance, error) {
	return p.verifyLaunchInstance(ctx, claim, false)
}

func (p *VPCInstanceProvider) verifyLaunchInstance(ctx context.Context, claim *karpv1.NodeClaim, repairMissingTags bool) (*vpcv1.Instance, error) {
	fresh, config, err := p.freshLaunchClaim(ctx, claim)
	if err != nil {
		return nil, err
	}
	if fresh.Status.ProviderID == "" && (!config.Submitted || config.Rejected) {
		return nil, fmt.Errorf("launch was not submitted")
	}
	vpc, err := p.clientForRegion(ctx, config.Region)
	if err != nil {
		return nil, err
	}
	vm, err := p.findLaunch(ctx, vpc, config)
	if err != nil {
		return nil, err
	}
	if vm == nil {
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("verified launch instance is absent"))
	}
	if vm.ID == nil || vm.CRN == nil || *vm.CRN != fmt.Sprintf("crn:v1:bluemix:public:is:%s:a/%s::instance:%s", config.Zone, config.AccountID, *vm.ID) {
		return nil, fmt.Errorf("instance CRN differs from the immutable launch identity")
	}
	if fresh.Status.ProviderID != "" && fresh.Status.ProviderID != fmt.Sprintf("ibm:///%s/%s", config.Region, *vm.ID) {
		return nil, fmt.Errorf("provider ID targets another instance")
	}
	tags, err := vpc.GetInstanceUserTags(ctx, *vm.CRN)
	if err != nil {
		return nil, err
	}
	expected := ownership.VPCTags(config.ClusterUID, config.ClaimUID, config.ClassUID)
	missing := false
	for key, value := range expected {
		if actual, exists := tags[key]; exists && actual != value {
			return nil, fmt.Errorf("instance ownership tag %s belongs to another owner", key)
		} else if !exists {
			missing = true
		}
	}
	if _, _, readErr := p.freshLaunchClaim(ctx, fresh); readErr != nil {
		return nil, readErr
	}
	if missing {
		if !repairMissingTags {
			return nil, fmt.Errorf("instance ownership tags are not yet established")
		}
		if updateErr := vpc.UpdateInstanceTags(ctx, *vm.ID, expected); updateErr != nil {
			return nil, updateErr
		}
		tags, err = vpc.GetInstanceUserTags(ctx, *vm.CRN)
		if err != nil {
			return nil, err
		}
		for key, value := range expected {
			if tags[key] != value {
				return nil, fmt.Errorf("instance ownership tag repair is not yet visible")
			}
		}
	}
	if _, _, readErr := p.freshLaunchClaim(ctx, fresh); readErr != nil {
		return nil, readErr
	}
	return vm, nil
}

// verifyLegacyInstance proves ownership of a VM launched before launch checkpoints existed.
// Those releases named the VM after its NodeClaim and never persisted cloud ownership tags,
// so the proof is the recorded provider ID, the VM name and the verified birth account.
func (p *VPCInstanceProvider) verifyLegacyInstance(ctx context.Context, vpc *ibm.VPCClient, claim *karpv1.NodeClaim, instanceID, accountID string) (*vpcv1.Instance, error) {
	if claim == nil || claim.UID == "" || p.reader() == nil {
		return nil, fmt.Errorf("persisted NodeClaim identity is required")
	}
	fresh := &karpv1.NodeClaim{}
	if err := p.reader().Get(ctx, client.ObjectKeyFromObject(claim), fresh); err != nil {
		return nil, err
	}
	if fresh.UID != claim.UID || fresh.Status.ProviderID != claim.Status.ProviderID || fresh.Annotations[LaunchAnnotation] != "" {
		return nil, fmt.Errorf("NodeClaim launch identity changed")
	}
	vm, err := vpc.GetInstance(ctx, instanceID)
	if isIBMInstanceNotFoundError(err) {
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("legacy instance %s not found", instanceID))
	}
	if err != nil {
		return nil, err
	}
	if vm == nil || vm.ID == nil || *vm.ID != instanceID || vm.Name == nil || *vm.Name != fresh.Name {
		return nil, fmt.Errorf("instance identity differs from the legacy NodeClaim")
	}
	if err := verifyInstanceAccount(vm, accountID); err != nil {
		return nil, err
	}
	return vm, nil
}

type LaunchIdentity struct{ AccountID, ClassUID, ClusterUID, Region string }

func ReadLaunchIdentity(claim *karpv1.NodeClaim) (*LaunchIdentity, error) {
	config, err := decodeLaunch(claim.Annotations[LaunchAnnotation])
	if err != nil {
		return nil, err
	}
	if config.ClaimUID != string(claim.UID) || config.ClassUID == "" || config.AccountID == "" {
		return nil, fmt.Errorf("launch checkpoint owner changed")
	}
	return &LaunchIdentity{AccountID: config.AccountID, ClassUID: config.ClassUID, ClusterUID: config.ClusterUID, Region: config.Region}, nil
}
