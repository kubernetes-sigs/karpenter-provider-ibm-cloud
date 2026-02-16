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

// IKSDynamicPoolConfig defines configuration for dynamic worker pool creation in IKS mode.
// When enabled, Karpenter will dynamically create new worker pools to match pod requirements
// when no existing pool with the required instance type is available.
type IKSDynamicPoolConfig struct {
	// Enabled controls whether dynamic pool creation is active.
	// When true, Karpenter will create new worker pools as needed to satisfy pod requirements.
	// When false (default), Karpenter will only use existing worker pools.
	// +optional
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// NamePrefix is the prefix used when naming dynamically created worker pools.
	// Pool names will follow the pattern: {NamePrefix}-{instanceType}-{uniqueID}
	// Example: With prefix "karp", a pool might be named "karp-bx2-4x16-a1b2c3"
	// +optional
	// +kubebuilder:validation:MaxLength=20
	// +kubebuilder:validation:Pattern="^[a-z][a-z0-9-]*$"
	// +kubebuilder:default="karp"
	NamePrefix string `json:"namePrefix,omitempty"`

	// Labels are key-value pairs applied to dynamically created worker pools.
	// These labels help identify and manage Karpenter-created pools.
	// The label "karpenter.sh/managed=true" is always added automatically.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// DiskEncryption enables disk encryption for worker nodes in dynamically created pools.
	// When true, the boot volume of worker nodes will be encrypted.
	// +optional
	// +kubebuilder:default=true
	DiskEncryption *bool `json:"diskEncryption,omitempty"`

	// AllowedInstanceTypes is a list of instance types (flavors) that can be used when
	// creating dynamic pools. If empty, all available instance types are allowed.
	// Example: ["bx2-4x16", "bx2-8x32", "cx2-4x8"]
	// +optional
	AllowedInstanceTypes []string `json:"allowedInstanceTypes,omitempty"`

	// CleanupPolicy defines when dynamically created worker pools should be deleted.
	// +optional
	CleanupPolicy *IKSPoolCleanupPolicy `json:"cleanupPolicy,omitempty"`
}

// IKSPoolCleanupPolicy defines the policy for cleaning up dynamically created worker pools.
type IKSPoolCleanupPolicy struct {
	// EmptyPoolTTL specifies how long an empty pool should remain before being deleted.
	// An empty pool is one with SizePerZone of 0 nodes.
	// Format: duration string (e.g., "30m", "1h", "24h")
	// If not specified, empty pools are cleaned up immediately.
	// +optional
	// +kubebuilder:validation:Pattern="^([0-9]+(s|m|h))+$"
	// +kubebuilder:default="5m"
	EmptyPoolTTL string `json:"emptyPoolTTL,omitempty"`

	// DeleteOnEmpty controls whether empty pools are automatically deleted.
	// When true (default), pools are deleted after being empty for EmptyPoolTTL.
	// When false, pools are retained even when empty.
	// +optional
	// +kubebuilder:default=true
	DeleteOnEmpty *bool `json:"deleteOnEmpty,omitempty"`
}

// LoadBalancerTarget defines a target group configuration for load balancer integration
type LoadBalancerTarget struct {
	// LoadBalancerID is the ID of the IBM Cloud Load Balancer
	// Must be a valid IBM Cloud Load Balancer ID
	// Example: "r010-12345678-1234-5678-9abc-def012345678"
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^r[0-9]+-[a-zA-Z0-9]{8}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{12}$"
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
// - instanceProfile and instanceRequirements are mutually exclusive (both are optional)
// - When bootstrapMode is "iks-api", iksClusterID must be provided
// - When zone is specified, it must belong to the specified region
//
// BlockDeviceMapping defines storage device configuration for instances
// This allows customization of boot and data volumes attached to instances
type BlockDeviceMapping struct {
	// DeviceName is the name for this volume attachment
	// If not specified, a name will be auto-generated
	// +optional
	DeviceName *string `json:"deviceName,omitempty"`

	// VolumeSpec contains the volume configuration
	// +optional
	VolumeSpec *VolumeSpec `json:"volumeSpec,omitempty"`

	// RootVolume indicates if this is the boot/root volume
	// Only one volume can be marked as root volume
	// +optional
	RootVolume bool `json:"rootVolume,omitempty"`
}

// KubeletConfiguration defines args to be used when configuring kubelet on provisioned nodes.
type KubeletConfiguration struct {
	// ClusterDNS is a list of IP addresses for the cluster DNS server.
	// +optional
	ClusterDNS []string `json:"clusterDNS,omitempty"`

	// MaxPods is an override for the maximum number of pods that can run on a worker node instance.
	// +kubebuilder:validation:Minimum:=0
	// +optional
	MaxPods *int32 `json:"maxPods,omitempty"`

	// PodsPerCore is an override for the number of pods that can run on a worker node
	// instance based on the number of cpu cores.
	// +kubebuilder:validation:Minimum:=0
	// +optional
	PodsPerCore *int32 `json:"podsPerCore,omitempty"`

	// SystemReserved contains resources reserved for OS system daemons and kernel memory.
	// +kubebuilder:validation:XValidation:message="valid keys for systemReserved are ['cpu','memory','ephemeral-storage','pid']",rule="self.all(x, x=='cpu' || x=='memory' || x=='ephemeral-storage' || x=='pid')"
	// +kubebuilder:validation:XValidation:message="systemReserved value cannot be a negative resource quantity",rule="self.all(x, !self[x].startsWith('-'))"
	// +optional
	SystemReserved map[string]string `json:"systemReserved,omitempty"`

	// KubeReserved contains resources reserved for Kubernetes system components.
	// +kubebuilder:validation:XValidation:message="valid keys for kubeReserved are ['cpu','memory','ephemeral-storage','pid']",rule="self.all(x, x=='cpu' || x=='memory' || x=='ephemeral-storage' || x=='pid')"
	// +kubebuilder:validation:XValidation:message="kubeReserved value cannot be a negative resource quantity",rule="self.all(x, !self[x].startsWith('-'))"
	// +optional
	KubeReserved map[string]string `json:"kubeReserved,omitempty"`

	// EvictionHard is the map of signal names to quantities that define hard eviction thresholds
	// +kubebuilder:validation:XValidation:message="valid keys for evictionHard are ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available']",rule="self.all(x, x in ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available'])"
	// +kubebuilder:validation:XValidation:message="evictionHard value cannot be a negative resource quantity",rule="self.all(x, !self[x].startsWith('-'))"
	// +optional
	EvictionHard map[string]string `json:"evictionHard,omitempty"`

	// EvictionSoft is the map of signal names to quantities that define soft eviction thresholds
	// +kubebuilder:validation:XValidation:message="valid keys for evictionSoft are ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available']",rule="self.all(x, x in ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available'])"
	// +kubebuilder:validation:XValidation:message="evictionSoft value cannot be a negative resource quantity",rule="self.all(x, !self[x].startsWith('-'))"
	// +optional
	EvictionSoft map[string]string `json:"evictionSoft,omitempty"`

	// EvictionSoftGracePeriod is the map of signal names to quantities that define grace periods for each eviction signal
	// +kubebuilder:validation:XValidation:message="valid keys for evictionSoftGracePeriod are ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available']",rule="self.all(x, x in ['memory.available','nodefs.available','nodefs.inodesFree','imagefs.available','imagefs.inodesFree','pid.available'])"
	// +optional
	EvictionSoftGracePeriod map[string]metav1.Duration `json:"evictionSoftGracePeriod,omitempty"`

	// EvictionMaxPodGracePeriod is the maximum allowed grace period (in seconds) to use when terminating pods in
	// response to soft eviction thresholds being met.
	// +kubebuilder:validation:Minimum:=0
	// +optional
	EvictionMaxPodGracePeriod *int32 `json:"evictionMaxPodGracePeriod,omitempty"`

	// ImageGCHighThresholdPercent is the percent of disk usage after which image
	// garbage collection is always run.
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:validation:Maximum:=100
	// +optional
	ImageGCHighThresholdPercent *int32 `json:"imageGCHighThresholdPercent,omitempty"`

	// ImageGCLowThresholdPercent is the percent of disk usage before which image
	// garbage collection is never run.
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:validation:Maximum:=100
	// +optional
	ImageGCLowThresholdPercent *int32 `json:"imageGCLowThresholdPercent,omitempty"`

	// CPUCFSQuota enables CPU CFS quota enforcement for containers that specify CPU limits.
	// +optional
	CPUCFSQuota *bool `json:"cpuCFSQuota,omitempty"`
}

// VolumeSpec defines IBM Cloud volume configuration
type VolumeSpec struct {
	// Capacity is the volume size in gigabytes
	// For boot volumes: minimum is typically the image's minimum provisioned size, maximum is 250GB
	// For data volumes: ranges vary by profile (typically 10GB to 16000GB)
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16000
	// +optional
	Capacity *int64 `json:"capacity,omitempty"`

	// Profile is the storage profile to use
	// Examples: "general-purpose", "5iops-tier", "10iops-tier", "custom"
	// If not specified, defaults to "general-purpose"
	// +optional
	Profile *string `json:"profile,omitempty"`

	// IOPS is the max I/O operations per second
	// Only applicable when Profile is "custom" or "defined_performance"
	// Must be within the range supported by the profile and capacity
	// +optional
	// +kubebuilder:validation:Minimum=100
	// +kubebuilder:validation:Maximum=64000
	IOPS *int64 `json:"iops,omitempty"`

	// Bandwidth is the max bandwidth in megabits per second
	// If not specified, it will be calculated based on profile, capacity, and IOPS
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16000
	Bandwidth *int64 `json:"bandwidth,omitempty"`

	// EncryptionKeyID is the CRN of the customer root key for volume encryption
	// If not specified, provider-managed encryption will be used
	// Example: "crn:v1:bluemix:public:kms:us-south:a/..."
	// +optional
	EncryptionKeyID *string `json:"encryptionKeyID,omitempty"`

	// DeleteOnTermination controls whether the volume is deleted when the instance is terminated
	// Defaults to true
	// +kubebuilder:default=true
	// +optional
	DeleteOnTermination *bool `json:"deleteOnTermination,omitempty"`

	// Tags are user tags to apply to the volume
	// +optional
	// +kubebuilder:validation:MaxItems=10
	Tags []string `json:"tags,omitempty"`
}

// ImageSelector defines semantic image selection criteria for automated latest image resolution.
// When used, the system will automatically select the most recent image matching the specified criteria.
// This eliminates the need to track specific image IDs and ensures nodes use up-to-date images.
type ImageSelector struct {
	// OS specifies the operating system for the image.
	// Common values include: "ubuntu", "rhel", "centos", "rocky", "fedora", "debian", "suse"
	// Examples: "ubuntu", "rhel", "centos"
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z]+$"
	OS string `json:"os"`

	// MajorVersion specifies the major version of the operating system.
	// Examples: "20" for Ubuntu 20.x, "8" for RHEL 8.x, "22" for Ubuntu 22.x
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[0-9]+$"
	MajorVersion string `json:"majorVersion"`

	// MinorVersion specifies the minor version of the operating system.
	// If not specified, the latest available minor version will be automatically selected.
	// Examples: "04" for Ubuntu x.04, omit for latest
	// +optional
	// +kubebuilder:validation:Pattern="^[0-9]+$"
	MinorVersion string `json:"minorVersion,omitempty"`

	// Architecture specifies the CPU architecture for the image.
	// Must match the architecture requirements of your workload.
	// Examples: "amd64", "arm64", "s390x"
	// +optional
	// +kubebuilder:validation:Enum=amd64;arm64;s390x
	// +kubebuilder:default="amd64"
	Architecture string `json:"architecture,omitempty"`

	// Variant specifies the image variant or flavor.
	// Common variants include: "minimal", "server", "cloud", "base"
	// If not specified, any variant will be considered, with preference for "minimal".
	// Examples: "minimal", "server"
	// +optional
	// +kubebuilder:validation:Pattern="^[a-z]+$"
	Variant string `json:"variant,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.subnet) || self.subnet == \"\" || self.subnet.matches('^[a-zA-Z0-9]{4}-[a-zA-Z0-9]{8}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{12}$')", message="subnet must be a valid IBM Cloud subnet ID format"
// +kubebuilder:validation:XValidation:rule="!has(self.image) || self.image.matches('^[a-z0-9-]+$')", message="image must contain only lowercase letters, numbers, and hyphens"
// +kubebuilder:validation:XValidation:rule="has(self.image) || has(self.imageSelector)", message="either image or imageSelector must be specified"
// +kubebuilder:validation:XValidation:rule="!(has(self.image) && has(self.imageSelector))", message="image and imageSelector are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!(has(self.instanceProfile) && has(self.instanceRequirements))", message="instanceProfile and instanceRequirements are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!has(self.bootstrapMode) || self.bootstrapMode != 'iks-api' || has(self.iksClusterID)", message="iksClusterID is required when bootstrapMode is 'iks-api'"
// +kubebuilder:validation:XValidation:rule="!has(self.zone) || self.zone == \"\" || self.region.startsWith(self.zone.split('-')[0] + '-' + self.zone.split('-')[1])", message="zone must be within the specified region"
// +kubebuilder:validation:XValidation:rule="self.vpc.matches('^r[0-9]+-[a-zA-Z0-9]{8}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{12}$')", message="vpc must be a valid IBM Cloud VPC ID format"
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
	// If not specified, instance types will be automatically selected based on NodePool requirements.
	// Must follow IBM Cloud instance profile naming convention: family-cpuxmemory.
	// Examples: "bx2-4x16" (4 vCPUs, 16GB RAM), "mx2-8x64" (8 vCPUs, 64GB RAM), "gx2-16x128x2v100" (GPU instance)
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]+-[0-9]+x[0-9]+$"
	InstanceProfile string `json:"instanceProfile,omitempty"`

	// InstanceRequirements defines requirements for automatic instance type selection
	// Only used when InstanceProfile is not specified
	// If neither InstanceProfile nor InstanceRequirements are specified, NodePool requirements will be used
	// +optional
	InstanceRequirements *InstanceTypeRequirements `json:"instanceRequirements,omitempty"`

	// Image is the ID or name of the boot image to use for nodes.
	// Must contain only lowercase letters, numbers, and hyphens.
	// Can be either an image ID or a standard image name.
	// Examples: "ubuntu-24-04-amd64", "rhel-8-amd64", "centos-8-amd64"
	// Mutually exclusive with ImageSelector. If both are specified, Image takes precedence.
	// +optional
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image,omitempty"`

	// ImageSelector provides semantic image selection based on OS, version, and architecture.
	// This allows for automatic selection of the latest available minor versions and provides
	// fallback mechanisms when specific images become unavailable.
	// Mutually exclusive with Image. If both are specified, Image takes precedence.
	// +optional
	ImageSelector *ImageSelector `json:"imageSelector,omitempty"`

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
	// +kubebuilder:validation:Items:Pattern="^r[0-9]+-[a-zA-Z0-9]{8}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{12}$"
	SecurityGroups []string `json:"securityGroups"`

	// UserData contains user data script to run on instance initialization
	// When specified, this completely overrides the default bootstrap script
	// Use UserDataAppend if you want to add custom commands without replacing the bootstrap process
	// +optional
	UserData string `json:"userData,omitempty"`

	// UserDataAppend contains additional user data script to append to the default bootstrap script
	// Unlike UserData, this field adds your custom commands after the standard bootstrap process
	// This is useful for running additional configuration without disrupting node bootstrapping
	// +optional
	UserDataAppend string `json:"userDataAppend,omitempty"`

	// SSHKeys is a list of SSH key IDs to add to the instance.
	// Must be valid IBM Cloud SSH key IDs in the format "r###-########-####-####-####-############".
	// Example: "r010-82091c89-68e4-4b3f-bd2b-4e63ca2f67da"
	// To find SSH key IDs: ibmcloud is keys --output json | jq '.[] | {name, id}'
	// +optional
	// +kubebuilder:validation:Items:MinLength=1
	// +kubebuilder:validation:Items:Pattern="^r[0-9]+-[a-zA-Z0-9]{8}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{12}$"
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
	// Must be a valid HTTP or HTTPS URL with hostname/IP and port.
	// Examples: "https://10.243.65.4:6443", "http://k8s-api.example.com:6443"
	// +optional
	// +kubebuilder:validation:Pattern="^https?://[a-zA-Z0-9.-]+:[0-9]+$"
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

	// IKSDynamicPools configures dynamic worker pool creation for IKS mode.
	// When enabled, Karpenter will create new worker pools to match pod requirements
	// when no existing pool with the required instance type is available.
	// This allows Karpenter to provision nodes with specific instance types dynamically.
	// +optional
	IKSDynamicPools *IKSDynamicPoolConfig `json:"iksDynamicPools,omitempty"`

	// LoadBalancerIntegration defines load balancer integration settings
	// When configured, nodes will be automatically registered with IBM Cloud Load Balancers
	// +optional
	LoadBalancerIntegration *LoadBalancerIntegration `json:"loadBalancerIntegration,omitempty"`

	// BlockDeviceMappings defines custom block device configurations for instances
	// If not specified, a default 100GB general-purpose boot volume will be used
	// When specified, at least one mapping must have RootVolume set to true
	// +optional
	// +kubebuilder:validation:MaxItems=10
	BlockDeviceMappings []BlockDeviceMapping `json:"blockDeviceMappings,omitempty"`

	// Kubelet defines args to be used when configuring kubelet on provisioned nodes.
	// They are a subset of the upstream types, recognizing not all options may be supported.
	// Wherever possible, the types and names should reflect the upstream kubelet types.
	// +optional
	// +kubebuilder:validation:XValidation:message="imageGCHighThresholdPercent must be greater than imageGCLowThresholdPercent",rule="has(self.imageGCHighThresholdPercent) && has(self.imageGCLowThresholdPercent) ? self.imageGCHighThresholdPercent > self.imageGCLowThresholdPercent : true"
	// +kubebuilder:validation:XValidation:message="evictionSoft key does not have a matching evictionSoftGracePeriod",rule="has(self.evictionSoft) ? self.evictionSoft.all(e, e in self.evictionSoftGracePeriod) : true"
	// +kubebuilder:validation:XValidation:message="evictionSoftGracePeriod key does not have a matching evictionSoft",rule="has(self.evictionSoftGracePeriod) ? self.evictionSoftGracePeriod.all(e, e in self.evictionSoft) : true"
	Kubelet *KubeletConfiguration `json:"kubelet,omitempty"`
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

	// ResolvedSecurityGroups contains the security group IDs that will be used for nodes.
	// When Spec.SecurityGroups is specified, this mirrors those values.
	// When Spec.SecurityGroups is empty, this contains the VPC's default security group.
	// This field is populated by the status controller and used for drift detection.
	// +optional
	ResolvedSecurityGroups []string `json:"resolvedSecurityGroups,omitempty"`

	// ResolvedImageID contains the IBM Cloud VPC image ID that has been resolved from either
	// the Image field or the ImageSelector criteria. This field is populated by the status
	// controller during validation and is used by the instance provider during provisioning.
	// This eliminates duplicate VPC API calls and ensures consistency between validation and provisioning.
	// The field is updated when:
	// - ImageSelector criteria are specified and successfully resolved to an image
	// - Image field is specified and successfully validated
	// When this field is populated, the instance provider will use it directly instead of
	// performing image resolution again.
	// +optional
	ResolvedImageID string `json:"resolvedImageID,omitempty"`

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
