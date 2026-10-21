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

package nodeclass

import (
	"encoding/json"
	"fmt"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
	"github.com/mitchellh/hashstructure/v2"
	"io"
	"strings"
)

type provisioningSpec struct {
	Region               string
	Zone                 string
	InstanceProfile      string
	InstanceRequirements *v1alpha1.InstanceTypeRequirements
	Image                string
	ImageSelector        *v1alpha1.ImageSelector
	VPC                  string
	Subnet               string
	PlacementStrategy    *v1alpha1.PlacementStrategy
	SecurityGroups       []string
	UserData             string
	UserDataAppend       string
	SSHKeys              []string
	ResourceGroup        string
	PlacementTarget      string
	Tags                 map[string]string
	BootstrapMode        *string
	APIServerEndpoint    string
	IKSClusterID         string
	IKSWorkerPoolID      string
	BlockDeviceMappings  []v1alpha1.BlockDeviceMapping
	Kubelet              *v1alpha1.KubeletConfiguration
	DynamicPools         *dynamicPools
}

type dynamicPools struct {
	Enabled              bool
	NamePrefix           string
	Labels               map[string]string
	DiskEncryption       *bool
	AllowedInstanceTypes []string
}

func ProvisioningHash(nodeClass *v1alpha1.IBMNodeClass) (string, error) {
	spec := nodeClass.Spec
	projection := provisioningSpec{
		Region:               spec.Region,
		Zone:                 spec.Zone,
		InstanceProfile:      spec.InstanceProfile,
		InstanceRequirements: spec.InstanceRequirements,
		Image:                spec.Image,
		ImageSelector:        spec.ImageSelector,
		VPC:                  spec.VPC,
		Subnet:               spec.Subnet,
		PlacementStrategy:    spec.PlacementStrategy,
		SecurityGroups:       spec.SecurityGroups,
		UserData:             spec.UserData,
		UserDataAppend:       spec.UserDataAppend,
		SSHKeys:              spec.SSHKeys,
		ResourceGroup:        spec.ResourceGroup,
		PlacementTarget:      spec.PlacementTarget,
		Tags:                 spec.Tags,
		BootstrapMode:        spec.BootstrapMode,
		APIServerEndpoint:    spec.APIServerEndpoint,
		IKSClusterID:         spec.IKSClusterID,
		IKSWorkerPoolID:      spec.IKSWorkerPoolID,
		BlockDeviceMappings:  spec.BlockDeviceMappings,
		Kubelet:              spec.Kubelet,
	}
	if config := spec.IKSDynamicPools; config != nil {
		projection.DynamicPools = &dynamicPools{
			Enabled:              config.Enabled,
			NamePrefix:           config.NamePrefix,
			Labels:               config.Labels,
			DiskEncryption:       config.DiskEncryption,
			AllowedInstanceTypes: config.AllowedInstanceTypes,
		}
	}
	hash, err := hashstructure.Hash(projection, hashstructure.FormatV2, nil)
	return fmt.Sprint(hash), err
}

func LegacyHash(nodeClass *v1alpha1.IBMNodeClass) (string, error) {
	hash, err := hashstructure.Hash(nodeClass.Spec, hashstructure.FormatV2, nil)
	return fmt.Sprint(hash), err
}

const HashMigrationAnnotation = "karpenter-ibm.sh/nodeclass-hash-migration"

type HashMigration struct {
	Version, MinimumWriterVersion int
	LegacyHash                    string
	ProvisioningHash              string
}

func ReadHashMigration(nodeClass *v1alpha1.IBMNodeClass) (*HashMigration, error) {
	value := nodeClass.Annotations[HashMigrationAnnotation]
	if value == "" {
		return nil, nil
	}
	migration := &HashMigration{}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(migration); err != nil {
		return nil, fmt.Errorf("reading hash migration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing hash migration checkpoint")
	}
	if err := ownership.ValidateStateVersion(migration.Version, migration.MinimumWriterVersion); err != nil {
		return nil, err
	}
	if migration.LegacyHash == "" || migration.ProvisioningHash == "" {
		return nil, fmt.Errorf("invalid hash migration checkpoint")
	}
	return migration, nil
}
