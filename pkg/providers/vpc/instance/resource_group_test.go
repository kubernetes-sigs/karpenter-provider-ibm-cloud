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
	"testing"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

func TestResourceGroupConfiguration(t *testing.T) {
	tests := []struct {
		name                   string
		nodeClassResourceGroup string
		expectedResourceGroup  *string
	}{
		{
			name:                   "resource group specified",
			nodeClassResourceGroup: "test-resource-group-id",
			expectedResourceGroup:  &[]string{"test-resource-group-id"}[0],
		},
		{
			name:                   "resource group empty",
			nodeClassResourceGroup: "",
			expectedResourceGroup:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeClass := &v1alpha1.IBMNodeClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclass",
				},
				Spec: v1alpha1.IBMNodeClassSpec{
					Region:          "us-south",
					Zone:            "us-south-1",
					InstanceProfile: "bx2-4x16",
					Image:           "test-image-id",
					VPC:             "test-vpc-id",
					Subnet:          "test-subnet-id",
					ResourceGroup:   tt.nodeClassResourceGroup,
				},
			}

			nodeClaim := &karpv1.NodeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-nodeclaim",
					Labels: map[string]string{
						"node.kubernetes.io/instance-type": "bx2-4x16",
					},
				},
			}

			primaryNetworkInterface := &vpcv1.NetworkInterfacePrototype{
				Subnet: &vpcv1.SubnetIdentity{
					ID: &nodeClass.Spec.Subnet,
				},
			}

			bootVolumeAttachment := &vpcv1.VolumeAttachmentPrototypeInstanceByImageContext{
				Volume: &vpcv1.VolumePrototypeInstanceByImageContext{
					Profile: &vpcv1.VolumeProfileIdentity{
						Name: &[]string{"general-purpose"}[0],
					},
					Capacity: &[]int64{100}[0],
				},
				DeleteVolumeOnInstanceDelete: &[]bool{true}[0],
			}

			instancePrototype := &vpcv1.InstancePrototypeInstanceByImage{
				Name: &nodeClaim.Name,
				Zone: &vpcv1.ZoneIdentity{
					Name: &nodeClass.Spec.Zone,
				},
				Profile: &vpcv1.InstanceProfileIdentity{
					Name: &nodeClass.Spec.InstanceProfile,
				},
				VPC: &vpcv1.VPCIdentity{
					ID: &nodeClass.Spec.VPC,
				},
				Image: &vpcv1.ImageIdentity{
					ID: &nodeClass.Spec.Image,
				},
				PrimaryNetworkInterface: primaryNetworkInterface,
				BootVolumeAttachment:    bootVolumeAttachment,
			}

			if nodeClass.Spec.ResourceGroup != "" {
				instancePrototype.ResourceGroup = &vpcv1.ResourceGroupIdentity{
					ID: &nodeClass.Spec.ResourceGroup,
				}
			}

			if tt.expectedResourceGroup != nil {
				assert.NotNil(t, instancePrototype.ResourceGroup)
				if resourceGroupIdentity, ok := instancePrototype.ResourceGroup.(*vpcv1.ResourceGroupIdentity); ok {
					assert.NotNil(t, resourceGroupIdentity.ID)
					assert.Equal(t, *tt.expectedResourceGroup, *resourceGroupIdentity.ID)
				} else {
					t.Errorf("Expected ResourceGroup to be of type *vpcv1.ResourceGroupIdentity")
				}
			} else {
				assert.Nil(t, instancePrototype.ResourceGroup)
			}
		})
	}
}
