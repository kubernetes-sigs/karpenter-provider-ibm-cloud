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
package v1alpha1

import (
	"github.com/awslabs/operatorpkg/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IBMNodeClass is the Schema for the IBMNodeClass API
// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type IBMNodeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of IBMNodeClass
	Spec IBMNodeClassSpec `json:"spec,omitempty"`

	// Status defines the observed state of IBMNodeClass
	Status IBMNodeClassStatus `json:"status,omitempty"`
}

// PlacementStrategy defines how nodes should be placed across zones and subnets
type PlacementStrategy struct {
	// ZoneBalance determines how nodes are distributed across zones
	// Valid values are:
	// - "Balanced" (default) - Nodes are evenly distributed across zones
	// - "AvailabilityFirst" - Prioritize zone availability over even distribution
	// - "CostOptimized" - Consider both cost and availability when selecting zones
	// +optional
	// +kubebuilder:validation:Enum=Balanced;AvailabilityFirst;CostOptimized
	// +kubebuilder:default=Balanced
	ZoneBalance string `json:"zoneBalance,omitempty"`

	// SubnetSelection defines criteria for automatic subnet selection
	// +optional
	SubnetSelection *SubnetSelectionCriteria `json:"subnetSelection,omitempty"`
}

// SubnetSelectionCriteria defines how subnets should be automatically selected
type SubnetSelectionCriteria struct {
	// MinimumAvailableIPs is the minimum number of available IPs a subnet must have
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinimumAvailableIPs int32 `json:"minimumAvailableIPs,omitempty"`

	// Tags that subnets must have to be considered for selection
	// +optional
	RequiredTags map[string]string `json:"requiredTags,omitempty"`
}

// InstanceTypeRequirements defines criteria for automatic instance type selection
type InstanceTypeRequirements struct {
	// Architecture specifies the CPU architecture
	// Valid values: "amd64", "arm64"
	// +optional
	// +kubebuilder:validation:Enum=amd64;arm64
	Architecture string `json:"architecture,omitempty"`

	// MinimumCPU specifies the minimum number of CPUs required
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinimumCPU int32 `json:"minimumCPU,omitempty"`

	// MinimumMemory specifies the minimum amount of memory in GiB
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinimumMemory int32 `json:"minimumMemory,omitempty"`

	// MaximumHourlyPrice specifies the maximum hourly price in USD as a decimal string (e.g. "0.50")
	// +optional
	// +kubebuilder:validation:Pattern=^\\d+\\.?\\d*$
	MaximumHourlyPrice string `json:"maximumHourlyPrice,omitempty"`
}

// IBMNodeClassSpec defines the desired state of IBMNodeClass
// +kubebuilder:validation:XValidation:rule="has(self.instanceProfile) || has(self.instanceRequirements)", message="either instanceProfile or instanceRequirements must be specified"
// +kubebuilder:validation:XValidation:rule="!(has(self.instanceProfile) && has(self.instanceRequirements))", message="instanceProfile and instanceRequirements are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="self.bootstrapMode != 'iks-api' || has(self.iksClusterID)", message="iksClusterID is required when bootstrapMode is 'iks-api'"
// +kubebuilder:validation:XValidation:rule="self.region.startsWith(self.zone.split('-')[0] + '-' + self.zone.split('-')[1]) || self.zone == ''", message="zone must be within the specified region"
// +kubebuilder:validation:XValidation:rule="self.vpc.matches('^r[0-9]{3}-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$')", message="vpc must be a valid IBM Cloud VPC ID format"
// +kubebuilder:validation:XValidation:rule="self.subnet == '' || self.subnet.matches('^[0-9a-z]{4}-[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}$')", message="subnet must be a valid IBM Cloud subnet ID format"
// +kubebuilder:validation:XValidation:rule="self.image.matches('^[a-z0-9-]+$')", message="image must contain only lowercase letters, numbers, and hyphens"
type IBMNodeClassSpec struct {
	// Region is the IBM Cloud region where nodes will be created
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z]{2}-[a-z]+$"
	Region string `json:"region"`

	// Zone is the availability zone where nodes will be created
	// If not specified, zones will be automatically selected based on placement strategy
	// +optional
	// +kubebuilder:validation:Pattern="^[a-z]{2}-[a-z]+-[0-9]+$"
	Zone string `json:"zone,omitempty"`

	// InstanceProfile is the name of the instance profile to use
	// If not specified, instance types will be automatically selected based on requirements
	// Either InstanceProfile or InstanceRequirements must be specified
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]+-[0-9]+x[0-9]+$"
	InstanceProfile string `json:"instanceProfile,omitempty"`

	// InstanceRequirements defines requirements for automatic instance type selection
	// Only used when InstanceProfile is not specified
	// Either InstanceProfile or InstanceRequirements must be specified
	// +optional
	InstanceRequirements *InstanceTypeRequirements `json:"instanceRequirements,omitempty"`

	// Image is the ID of the image to use for nodes
	// +required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// VPC is the ID of the VPC where nodes will be created
	// +required
	// +kubebuilder:validation:MinLength=1
	VPC string `json:"vpc"`

	// Subnet is the ID of the subnet where nodes will be created
	// If not specified, subnets will be automatically selected based on placement strategy
	// +optional
	Subnet string `json:"subnet,omitempty"`

	// PlacementStrategy defines how nodes should be placed across zones and subnets
	// Only used when Zone or Subnet is not specified
	// +optional
	PlacementStrategy *PlacementStrategy `json:"placementStrategy,omitempty"`

	// SecurityGroups is a list of security group IDs to attach to nodes
	// At least one security group must be specified for VPC instance creation
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:Items:Pattern="^r[0-9]{3}-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$"
	SecurityGroups []string `json:"securityGroups"`

	// UserData contains user data script to run on instance initialization
	// +optional
	UserData string `json:"userData,omitempty"`

	// SSHKeys is a list of SSH key names to add to the instance
	// +optional
	// +kubebuilder:validation:Items:MinLength=1
	// +kubebuilder:validation:Items:Pattern="^[a-z0-9-]+$"
	SSHKeys []string `json:"sshKeys,omitempty"`

	// ResourceGroup is the ID of the resource group for the instance
	// +optional
	ResourceGroup string `json:"resourceGroup,omitempty"`

	// PlacementTarget is the ID of the placement target (dedicated host, placement group)
	// +optional
	PlacementTarget string `json:"placementTarget,omitempty"`

	// Tags to apply to the instances
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// BootstrapMode determines how nodes should be bootstrapped to join the cluster
	// Valid values are:
	// - "cloud-init" - Use cloud-init scripts to bootstrap nodes (default)
	// - "iks-api" - Use IKS Worker Pool API to add nodes to cluster
	// - "auto" - Automatically select the best method based on cluster type
	// +optional
	// +kubebuilder:validation:Enum=cloud-init;iks-api;auto
	// +kubebuilder:default=auto
	BootstrapMode *string `json:"bootstrapMode,omitempty"`

	// APIServerEndpoint is the Kubernetes API server endpoint for node registration
	// If specified, this endpoint will be used instead of automatic discovery
	// This is useful when the control plane is not accessible via standard discovery methods
	// Example: "https://10.243.65.4:6443"
	// +optional
	// +kubebuilder:validation:Pattern="^https://[a-zA-Z0-9.-]+:[0-9]+$"
	APIServerEndpoint string `json:"apiServerEndpoint,omitempty"`

	// IKSClusterID is the IKS cluster ID for API-based bootstrapping
	// Required when BootstrapMode is "iks-api"
	// +optional
	// +kubebuilder:validation:Pattern="^[a-z0-9]+$"
	IKSClusterID string `json:"iksClusterID,omitempty"`

	// IKSWorkerPoolID is the worker pool ID to add nodes to
	// Used with IKS API bootstrapping mode
	// +optional
	IKSWorkerPoolID string `json:"iksWorkerPoolID,omitempty"`
}

// IBMNodeClassStatus defines the observed state of IBMNodeClass
type IBMNodeClassStatus struct {
	// LastValidationTime is the last time the nodeclass was validated
	// +optional
	LastValidationTime metav1.Time `json:"lastValidationTime,omitempty"`

	// ValidationError contains the error message from the last validation
	// +optional
	ValidationError string `json:"validationError,omitempty"`

	// SelectedInstanceTypes contains the list of instance types that meet the requirements
	// Only populated when using automatic instance type selection
	// +optional
	SelectedInstanceTypes []string `json:"selectedInstanceTypes,omitempty"`

	// SelectedSubnets contains the list of subnets selected for node placement
	// Only populated when using automatic subnet selection
	// +optional
	SelectedSubnets []string `json:"selectedSubnets,omitempty"`

	// Conditions contains signals for health and readiness
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// StatusConditions returns the condition set for the status.Object interface
func (in *IBMNodeClass) StatusConditions() status.ConditionSet {
	return status.NewReadyConditions().For(in)
}

// GetConditions returns the conditions as status.Conditions for the status.Object interface
func (in *IBMNodeClass) GetConditions() []status.Condition {
	conditions := make([]status.Condition, 0, len(in.Status.Conditions))
	for _, c := range in.Status.Conditions {
		conditions = append(conditions, status.Condition{
			Type:               c.Type,
			Status:             c.Status, // Use c.Status directly as it's already a string-like value
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
			ObservedGeneration: c.ObservedGeneration,
		})
	}
	return conditions
}

// SetConditions sets the conditions from status.Conditions for the status.Object interface
func (in *IBMNodeClass) SetConditions(conditions []status.Condition) {
	metav1Conditions := make([]metav1.Condition, 0, len(conditions))
	for _, c := range conditions {
		if c.LastTransitionTime.IsZero() {
			continue
		}
		metav1Conditions = append(metav1Conditions, metav1.Condition{
			Type:               c.Type,
			Status:             metav1.ConditionStatus(c.Status),
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
			ObservedGeneration: c.ObservedGeneration,
		})
	}
	in.Status.Conditions = metav1Conditions
}

// IBMNodeClassList contains a list of IBMNodeClass
// +kubebuilder:object:root=true
// +kubebuilder:object:generate=true
type IBMNodeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IBMNodeClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IBMNodeClass{}, &IBMNodeClassList{})
}
