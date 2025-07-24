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

// PlacementStrategy defines how nodes should be placed across zones and subnets to optimize
// for different objectives such as high availability, cost efficiency, or balanced distribution.
type PlacementStrategy struct {
	// ZoneBalance determines the strategy for distributing nodes across availability zones.
	// This affects both initial placement and replacement decisions when nodes are scaled or replaced.
	// Valid values are:
	// - "Balanced" (default) - Nodes are evenly distributed across all available zones to maximize
	//   fault tolerance and prevent concentration in any single zone
	// - "AvailabilityFirst" - Prioritizes zones with the highest availability scores and most
	//   available capacity, which may result in uneven distribution but better resilience
	// - "CostOptimized" - Balances cost considerations with availability, preferring zones and
	//   instance types that offer the best price-performance ratio while maintaining redundancy
	// +optional
	// +kubebuilder:validation:Enum=Balanced;AvailabilityFirst;CostOptimized
	// +kubebuilder:default=Balanced
	ZoneBalance string `json:"zoneBalance,omitempty"`

	// SubnetSelection defines criteria for automatic subnet selection when multiple subnets are available.
	// This is only used when the Subnet field is not explicitly specified in the IBMNodeClassSpec.
	// When both ZoneBalance and SubnetSelection are configured, subnets are first filtered by the
	// selection criteria, then distributed according to the zone balancing strategy.
	// If SubnetSelection is nil, all available subnets in the VPC will be considered for placement.
	// +optional
	SubnetSelection *SubnetSelectionCriteria `json:"subnetSelection,omitempty"`
}

// SubnetSelectionCriteria defines how subnets should be automatically selected
type SubnetSelectionCriteria struct {
	// MinimumAvailableIPs is the minimum number of available IPs a subnet must have to be considered
	// for node placement. This helps ensure that subnets with sufficient capacity are chosen,
	// preventing placement failures due to IP exhaustion.
	// Example: Setting this to 10 ensures only subnets with at least 10 available IPs are used.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinimumAvailableIPs int32 `json:"minimumAvailableIPs,omitempty"`

	// RequiredTags specifies key-value pairs that subnets must have to be considered for selection.
	// All specified tags must be present on a subnet for it to be eligible.
	// This is useful for filtering subnets based on environment, team, or other organizational criteria.
	// Example: {"Environment": "production", "Team": "platform"} will only select subnets
	// that have both the Environment=production and Team=platform tags.
	// +optional
	RequiredTags map[string]string `json:"requiredTags,omitempty"`
}

// LoadBalancerTarget defines a target group configuration for load balancer integration
type LoadBalancerTarget struct {
	// LoadBalancerID is the ID of the IBM Cloud Load Balancer
	// Must be a valid IBM Cloud Load Balancer ID
	// Example: "r010-12345678-1234-5678-9abc-def012345678"
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^r[0-9]{3}-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$"
	LoadBalancerID string `json:"loadBalancerID"`

	// PoolName is the name of the load balancer pool to add targets to
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([a-z0-9-]*[a-z0-9])?$"
	PoolName string `json:"poolName"`

	// Port is the port number on the target instances
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// Weight specifies the weight for load balancing traffic to targets
	// Higher weights receive proportionally more traffic
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +kubebuilder:default=50
	Weight *int32 `json:"weight,omitempty"`

	// HealthCheck defines health check configuration for the target
	// +optional
	HealthCheck *LoadBalancerHealthCheck `json:"healthCheck,omitempty"`
}

// LoadBalancerHealthCheck defines health check configuration
type LoadBalancerHealthCheck struct {
	// Protocol is the protocol to use for health checks
	// +optional
	// +kubebuilder:validation:Enum=http;https;tcp
	// +kubebuilder:default=tcp
	Protocol string `json:"protocol,omitempty"`

	// Path is the URL path for HTTP/HTTPS health checks
	// Only used when Protocol is "http" or "https"
	// +optional
	// +kubebuilder:validation:Pattern="^/.*$"
	Path string `json:"path,omitempty"`

	// Interval is the time in seconds between health checks
	// +optional
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:validation:Maximum=300
	// +kubebuilder:default=30
	Interval *int32 `json:"interval,omitempty"`

	// Timeout is the timeout in seconds for each health check
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=60
	// +kubebuilder:default=5
	Timeout *int32 `json:"timeout,omitempty"`

	// RetryCount is the number of consecutive successful checks required
	// before marking a target as healthy
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +kubebuilder:default=2
	RetryCount *int32 `json:"retryCount,omitempty"`
}

// LoadBalancerIntegration defines load balancer integration configuration
type LoadBalancerIntegration struct {
	// Enabled controls whether load balancer integration is active
	// When enabled, nodes will be automatically registered with specified load balancers
	// +optional
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// TargetGroups defines the load balancer pools to register nodes with
	// +optional
	// +kubebuilder:validation:MaxItems=10
	TargetGroups []LoadBalancerTarget `json:"targetGroups,omitempty"`

	// AutoDeregister controls whether nodes are automatically removed from
	// load balancers when they are terminated or become unhealthy
	// +optional
	// +kubebuilder:default=true
	AutoDeregister *bool `json:"autoDeregister,omitempty"`

	// RegistrationTimeout is the maximum time to wait for target registration
	// +optional
	// +kubebuilder:validation:Minimum=30
	// +kubebuilder:validation:Maximum=600
	// +kubebuilder:default=300
	RegistrationTimeout *int32 `json:"registrationTimeout,omitempty"`
}

// InstanceTypeRequirements defines criteria for automatic instance type selection.
// This is used when InstanceProfile is not specified, allowing Karpenter to automatically
// choose the most suitable instance types based on workload requirements and cost constraints.
// Only instance types that meet ALL specified criteria will be considered for node provisioning.
type InstanceTypeRequirements struct {
	// Architecture specifies the required CPU architecture for instance types.
	// This must match the architecture of your container images and workloads.
	// Valid values: "amd64", "arm64", "s390x"
	// Example: "amd64" for x86-64 based workloads, "arm64" for ARM-based workloads,
	// "s390x" for IBM Z mainframe workloads
	// +optional
	// +kubebuilder:validation:Enum=amd64;arm64;s390x
	Architecture string `json:"architecture,omitempty"`

	// MinimumCPU specifies the minimum number of vCPUs required for instance types.
	// Instance types with fewer CPUs than this value will be excluded from consideration.
	// This helps ensure adequate compute capacity for CPU-intensive workloads.
	// Example: Setting this to 4 will only consider instance types with 4 or more vCPUs
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinimumCPU int32 `json:"minimumCPU,omitempty"`

	// MinimumMemory specifies the minimum amount of memory in GiB required for instance types.
	// Instance types with less memory than this value will be excluded from consideration.
	// This helps ensure adequate memory capacity for memory-intensive workloads.
	// Example: Setting this to 16 will only consider instance types with 16 GiB or more memory
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinimumMemory int32 `json:"minimumMemory,omitempty"`

	// MaximumHourlyPrice specifies the maximum hourly price in USD for instance types.
	// Instance types exceeding this price will be excluded from consideration.
	// This helps control costs by preventing the selection of expensive instance types.
	// Must be specified as a decimal string (e.g., "0.50" for 50 cents per hour).
	// Example: "1.00" limits selection to instance types costing $1.00/hour or less
	// +optional
	// +kubebuilder:validation:Pattern=^\\d+\\.?\\d*$
	MaximumHourlyPrice string `json:"maximumHourlyPrice,omitempty"`
}

// IBMNodeClassSpec defines the desired state of IBMNodeClass.
// This specification validates several IBM Cloud resource identifiers and configuration constraints:
//
// VPC ID Format: Must follow pattern "r###-########-####-####-####-############"
// Example: "r010-12345678-1234-5678-9abc-def012345678"
//
// Subnet ID Format: Must follow pattern "####-########-####-####-####-############" 
// Example: "0717-197e06f4-b500-426c-bc0f-900b215f996c"
//
// Configuration Rules:
// - Either instanceProfile OR instanceRequirements must be specified (mutually exclusive)
// - When bootstrapMode is "iks-api", iksClusterID must be provided
// - When zone is specified, it must belong to the specified region
//
// +kubebuilder:validation:XValidation:rule="has(self.instanceProfile) || has(self.instanceRequirements)", message="either instanceProfile or instanceRequirements must be specified"
// +kubebuilder:validation:XValidation:rule="!(has(self.instanceProfile) && has(self.instanceRequirements))", message="instanceProfile and instanceRequirements are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="self.bootstrapMode != 'iks-api' || has(self.iksClusterID)", message="iksClusterID is required when bootstrapMode is 'iks-api'"
// +kubebuilder:validation:XValidation:rule="self.region.startsWith(self.zone.split('-')[0] + '-' + self.zone.split('-')[1]) || self.zone == ''", message="zone must be within the specified region"
// +kubebuilder:validation:XValidation:rule="self.vpc.matches('^r[0-9]{3}-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$')", message="vpc must be a valid IBM Cloud VPC ID format"
// +kubebuilder:validation:XValidation:rule="self.subnet == '' || self.subnet.matches('^[0-9a-z]{4}-[a-z0-9]{8}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{4}-[a-z0-9]{12}$')", message="subnet must be a valid IBM Cloud subnet ID format"
// +kubebuilder:validation:XValidation:rule="self.image.matches('^[a-z0-9-]+$')", message="image must contain only lowercase letters, numbers, and hyphens"
type IBMNodeClassSpec struct {
	// Region is the IBM Cloud region where nodes will be created.
	// Must follow IBM Cloud region naming convention: two-letter country code followed by region name.
	// Examples: "us-south", "eu-de", "jp-tok", "au-syd"
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z]{2}-[a-z]+$"
	Region string `json:"region"`

	// Zone is the availability zone where nodes will be created.
	// If not specified, zones will be automatically selected based on placement strategy.
	// Must follow IBM Cloud zone naming convention: region name followed by zone number.
	// Examples: "us-south-1", "eu-de-2", "jp-tok-3"
	// +optional
	// +kubebuilder:validation:Pattern="^[a-z]{2}-[a-z]+-[0-9]+$"
	Zone string `json:"zone,omitempty"`

	// InstanceProfile is the name of the instance profile to use for nodes.
	// If not specified, instance types will be automatically selected based on requirements.
	// Either InstanceProfile or InstanceRequirements must be specified.
	// Must follow IBM Cloud instance profile naming convention: family-cpuxmemory.
	// Examples: "bx2-4x16" (4 vCPUs, 16GB RAM), "mx2-8x64" (8 vCPUs, 64GB RAM), "gx2-16x128x2v100" (GPU instance)
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]+-[0-9]+x[0-9]+$"
	InstanceProfile string `json:"instanceProfile,omitempty"`

	// InstanceRequirements defines requirements for automatic instance type selection
	// Only used when InstanceProfile is not specified
	// Either InstanceProfile or InstanceRequirements must be specified
	// +optional
	InstanceRequirements *InstanceTypeRequirements `json:"instanceRequirements,omitempty"`

	// Image is the ID or name of the boot image to use for nodes.
	// Must contain only lowercase letters, numbers, and hyphens.
	// Can be either an image ID or a standard image name.
	// Examples: "ubuntu-24-04-amd64", "rhel-8-amd64", "centos-8-amd64"
	// +required
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// VPC is the ID of the IBM Cloud VPC where nodes will be created.
	// Must be a valid IBM Cloud VPC ID following the format "r###-########-####-####-####-############".
	// Example: "r010-12345678-1234-5678-9abc-def012345678"
	// +required
	// +kubebuilder:validation:MinLength=1
	VPC string `json:"vpc"`

	// Subnet is the ID of the subnet where nodes will be created.
	// If not specified, subnets will be automatically selected based on placement strategy.
	// Must be a valid IBM Cloud subnet ID following the format "####-########-####-####-####-############".
	// Example: "0717-197e06f4-b500-426c-bc0f-900b215f996c"
	// +optional
	Subnet string `json:"subnet,omitempty"`

	// PlacementStrategy defines how nodes should be placed across zones and subnets when explicit
	// Zone or Subnet is not specified. This allows for intelligent distribution of nodes across
	// availability zones to optimize for cost, availability, or balanced distribution.
	// When Zone is specified, placement strategy is ignored for zone selection but may still
	// affect subnet selection within that zone. When Subnet is specified, placement strategy
	// is completely ignored as the exact placement is already determined.
	// If PlacementStrategy is nil, a default balanced distribution will be used.
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

	// APIServerEndpoint is the Kubernetes API server endpoint for node registration.
	// If specified, this endpoint will be used instead of automatic discovery.
	// This is useful when the control plane is not accessible via standard discovery methods.
	// Must be a valid HTTPS URL with hostname/IP and port.
	// Examples: "https://10.243.65.4:6443", "https://k8s-api.example.com:6443"
	// +optional
	// +kubebuilder:validation:Pattern="^https://[a-zA-Z0-9.-]+:[0-9]+$"
	APIServerEndpoint string `json:"apiServerEndpoint,omitempty"`

	// IKSClusterID is the IKS cluster ID for API-based bootstrapping.
	// Required when BootstrapMode is "iks-api".
	// Must be a valid IKS cluster identifier containing only lowercase letters and numbers.
	// Examples: "bng6n48d0t6vj7b33kag", "c4f7x6qw0bx25g5b4vhg"
	// +optional
	// +kubebuilder:validation:Pattern="^[a-z0-9]+$"
	IKSClusterID string `json:"iksClusterID,omitempty"`

	// IKSWorkerPoolID is the worker pool ID to add nodes to
	// Used with IKS API bootstrapping mode
	// +optional
	IKSWorkerPoolID string `json:"iksWorkerPoolID,omitempty"`

	// LoadBalancerIntegration defines load balancer integration settings
	// When configured, nodes will be automatically registered with IBM Cloud Load Balancers
	// +optional
	LoadBalancerIntegration *LoadBalancerIntegration `json:"loadBalancerIntegration,omitempty"`
}

// IBMNodeClassStatus defines the observed state of IBMNodeClass
type IBMNodeClassStatus struct {
	// LastValidationTime is the last time the nodeclass was validated against IBM Cloud APIs.
	// This timestamp is updated whenever the controller performs validation of the nodeclass
	// configuration, including checking VPC/subnet existence, security group validity, and
	// instance type availability. Used to determine if revalidation is needed.
	// +optional
	LastValidationTime metav1.Time `json:"lastValidationTime,omitempty"`

	// ValidationError contains the error message from the most recent validation attempt.
	// This field is populated when validation fails due to invalid configuration, missing
	// resources, or API errors. When validation succeeds, this field is cleared (empty).
	// Use this field to diagnose configuration issues that prevent node provisioning.
	// +optional
	ValidationError string `json:"validationError,omitempty"`

	// SelectedInstanceTypes contains the list of IBM Cloud instance types that meet the
	// specified InstanceRequirements criteria. This field is populated and maintained by
	// the controller when InstanceRequirements is used instead of a specific InstanceProfile.
	// The list is updated when:
	// - InstanceRequirements are modified
	// - IBM Cloud instance type availability changes
	// - Pricing information is updated
	// When InstanceProfile is used, this field remains empty.
	// +optional
	SelectedInstanceTypes []string `json:"selectedInstanceTypes,omitempty"`

	// SelectedSubnets contains the list of subnet IDs that have been selected for node placement
	// based on the PlacementStrategy and SubnetSelection criteria. This field is populated when:
	// - Subnet is not explicitly specified in the spec
	// - PlacementStrategy.SubnetSelection criteria are defined
	// The list represents subnets that meet all selection criteria and are available for use.
	// When an explicit Subnet is specified in the spec, this field remains empty.
	// The controller updates this list when subnet availability or tags change.
	// +optional
	SelectedSubnets []string `json:"selectedSubnets,omitempty"`

	// Conditions contains signals for health and readiness of the IBMNodeClass.
	// Standard conditions include:
	// - "Ready": Indicates whether the nodeclass is valid and ready for node provisioning
	// - "Validated": Indicates whether the configuration has been successfully validated
	// Conditions are updated by the controller based on validation results and resource availability.
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
