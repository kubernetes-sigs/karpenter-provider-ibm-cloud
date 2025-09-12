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

package fake

import (
	"time"
)

// WorkerPoolState represents the state of a worker pool
type WorkerPoolState string

const (
	WorkerPoolStateNormal      WorkerPoolState = "normal"
	WorkerPoolStateResizing    WorkerPoolState = "resizing"
	WorkerPoolStateRebalancing WorkerPoolState = "rebalancing"
	WorkerPoolStateDeleting    WorkerPoolState = "deleting"
)

// WorkerPool represents an IKS worker pool for testing
type WorkerPool struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	ClusterID        string            `json:"clusterID"`
	Flavor           string            `json:"flavor"`
	Zone             string            `json:"zone"`
	SizePerZone      int               `json:"sizePerZone"`
	ActualSize       int               `json:"actualSize"`
	State            WorkerPoolState   `json:"state"`
	Zones            []WorkerPoolZone  `json:"zones"`
	AutoscaleEnabled bool              `json:"autoscaleEnabled"`
	MinSizePerZone   int               `json:"minSizePerZone"`
	MaxSizePerZone   int               `json:"maxSizePerZone"`
	Labels           map[string]string `json:"labels"`
	CreatedDate      time.Time         `json:"createdDate"`
	UpdatedDate      time.Time         `json:"updatedDate"`
}

// WorkerPoolZone represents a zone in a worker pool
type WorkerPoolZone struct {
	ID                     string `json:"id"`
	PrivateVLAN            string `json:"privateVLAN,omitempty"`
	PublicVLAN             string `json:"publicVLAN,omitempty"`
	PrivateSubnet          string `json:"privateSubnet,omitempty"`
	PublicSubnet           string `json:"publicSubnet,omitempty"`
	WorkerCount            int    `json:"workerCount"`
	PrivateServiceEndpoint bool   `json:"privateServiceEndpoint"`
	PublicServiceEndpoint  bool   `json:"publicServiceEndpoint"`
}

// Worker represents an IKS worker node
type Worker struct {
	ID                string                   `json:"id"`
	Provider          string                   `json:"provider"`
	Flavor            string                   `json:"flavor"`
	Location          string                   `json:"location"`
	PoolID            string                   `json:"poolID"`
	PoolName          string                   `json:"poolName"`
	NetworkInterfaces []WorkerNetworkInterface `json:"networkInterfaces"`
	Health            WorkerHealthStatus       `json:"health"`
	Lifecycle         WorkerLifecycleStatus    `json:"lifecycle"`
	KubernetesVersion string                   `json:"kubernetesVersion"`
	State             string                   `json:"state"`
	StateMessage      string                   `json:"stateMessage"`
	PrivateIP         string                   `json:"privateIP"`
	PublicIP          string                   `json:"publicIP"`
}

// WorkerNetworkInterface represents a worker's network interface
type WorkerNetworkInterface struct {
	SubnetID  string `json:"subnetID"`
	IPAddress string `json:"ipAddress"`
	CIDR      string `json:"cidr"`
	Primary   bool   `json:"primary"`
}

// WorkerHealthStatus represents worker health information
type WorkerHealthStatus struct {
	State   string `json:"state"` // normal, warning, critical
	Message string `json:"message"`
}

// WorkerLifecycleStatus represents worker lifecycle information
type WorkerLifecycleStatus struct {
	DesiredState string `json:"desiredState"` // deployed, deleted
	ActualState  string `json:"actualState"`  // deploying, deployed, deleting, deleted
	Message      string `json:"message"`
}

// ResizeWorkerPoolRequest represents a request to resize a worker pool
type ResizeWorkerPoolRequest struct {
	Cluster    string `json:"cluster"`
	WorkerPool string `json:"workerpool"`
	Size       int    `json:"size"`
}

// WorkerPoolPatchRequest represents a request to patch a worker pool
type WorkerPoolPatchRequest struct {
	State           string            `json:"state,omitempty"` // resizing, rebalancing, labels
	SizePerZone     int               `json:"sizePerZone,omitempty"`
	ReasonForResize string            `json:"reasonForResize,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

// VPCInstance represents a VPC instance for testing
type VPCInstance struct {
	ID                      string                `json:"id"`
	Name                    string                `json:"name"`
	Status                  string                `json:"status"`
	Profile                 VPCInstanceProfile    `json:"profile"`
	Image                   VPCImage              `json:"image"`
	Zone                    VPCZone               `json:"zone"`
	VPC                     VPCReference          `json:"vpc"`
	PrimaryNetworkInterface VPCNetworkInterface   `json:"primary_network_interface"`
	NetworkInterfaces       []VPCNetworkInterface `json:"network_interfaces"`
	BootVolumeAttachment    VPCVolumeAttachment   `json:"boot_volume_attachment"`
	VolumeAttachments       []VPCVolumeAttachment `json:"volume_attachments"`
	CreatedAt               time.Time             `json:"created_at"`
	UserData                string                `json:"user_data,omitempty"`
	Tags                    []VPCTag              `json:"tags"`
}

// VPCInstanceProfile represents an instance profile
type VPCInstanceProfile struct {
	Name string `json:"name"`
}

// VPCImage represents a VPC image
type VPCImage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	CRN  string `json:"crn"`
}

// VPCZone represents a VPC zone
type VPCZone struct {
	Name string `json:"name"`
}

// VPCReference represents a reference to a VPC resource
type VPCReference struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	CRN  string `json:"crn"`
}

// VPCNetworkInterface represents a VPC network interface
type VPCNetworkInterface struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	PrimaryIPV4Address string         `json:"primary_ipv4_address"`
	Subnet             VPCReference   `json:"subnet"`
	SecurityGroups     []VPCReference `json:"security_groups"`
}

// VPCVolumeAttachment represents a volume attachment
type VPCVolumeAttachment struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Volume VPCReference `json:"volume"`
}

// VPCTag represents a tag
type VPCTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// VPCSubnet represents a VPC subnet
type VPCSubnet struct {
	ID                        string       `json:"id"`
	Name                      string       `json:"name"`
	CIDR                      string       `json:"ipv4_cidr_block"`
	AvailableIPv4AddressCount int          `json:"available_ipv4_address_count"`
	TotalIPv4AddressCount     int          `json:"total_ipv4_address_count"`
	Zone                      VPCZone      `json:"zone"`
	VPC                       VPCReference `json:"vpc"`
	Status                    string       `json:"status"`
	CreatedAt                 time.Time    `json:"created_at"`
}
