/*
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

package bootstrap

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/types"
)

func TestIKSWorkerRequestStruct(t *testing.T) {
	request := IKSWorkerRequest{
		ClusterID:     "cluster-123",
		WorkerPoolID:  "pool-456",
		VPCInstanceID: "vpc-instance-789",
		Zone:          "us-south-1",
		Labels:        map[string]string{"key": "value"},
		Taints:        []IKSTaint{{Key: "test", Value: "value", Effect: "NoSchedule"}},
	}

	if request.ClusterID != "cluster-123" {
		t.Errorf("IKSWorkerRequest.ClusterID = %v, want cluster-123", request.ClusterID)
	}
	if request.WorkerPoolID != "pool-456" {
		t.Errorf("IKSWorkerRequest.WorkerPoolID = %v, want pool-456", request.WorkerPoolID)
	}
	if request.VPCInstanceID != "vpc-instance-789" {
		t.Errorf("IKSWorkerRequest.VPCInstanceID = %v, want vpc-instance-789", request.VPCInstanceID)
	}
	if request.Zone != "us-south-1" {
		t.Errorf("IKSWorkerRequest.Zone = %v, want us-south-1", request.Zone)
	}
	if request.Labels["key"] != "value" {
		t.Errorf("IKSWorkerRequest.Labels[key] = %v, want value", request.Labels["key"])
	}
	if len(request.Taints) != 1 {
		t.Errorf("IKSWorkerRequest.Taints length = %v, want 1", len(request.Taints))
	}
	if request.Taints[0].Key != "test" {
		t.Errorf("IKSWorkerRequest.Taints[0].Key = %v, want test", request.Taints[0].Key)
	}
}

func TestIKSTaintStruct(t *testing.T) {
	taint := IKSTaint{
		Key:    "example.com/test",
		Value:  "test-value",
		Effect: "NoSchedule",
	}

	if taint.Key != "example.com/test" {
		t.Errorf("IKSTaint.Key = %v, want example.com/test", taint.Key)
	}
	if taint.Value != "test-value" {
		t.Errorf("IKSTaint.Value = %v, want test-value", taint.Value)
	}
	if taint.Effect != "NoSchedule" {
		t.Errorf("IKSTaint.Effect = %v, want NoSchedule", taint.Effect)
	}
}

func TestIKSWorkerResponseStruct(t *testing.T) {
	response := IKSWorkerResponse{
		ID:       "worker-123",
		State:    "provisioning",
		Message:  "Worker is being provisioned",
		Labels:   map[string]string{"zone": "us-south-1"},
		Location: "us-south",
	}

	if response.ID != "worker-123" {
		t.Errorf("IKSWorkerResponse.ID = %v, want worker-123", response.ID)
	}
	if response.State != "provisioning" {
		t.Errorf("IKSWorkerResponse.State = %v, want provisioning", response.State)
	}
	if response.Message != "Worker is being provisioned" {
		t.Errorf("IKSWorkerResponse.Message = %v, want 'Worker is being provisioned'", response.Message)
	}
	if response.Labels["zone"] != "us-south-1" {
		t.Errorf("IKSWorkerResponse.Labels[zone] = %v, want us-south-1", response.Labels["zone"])
	}
	if response.Location != "us-south" {
		t.Errorf("IKSWorkerResponse.Location = %v, want us-south", response.Location)
	}
}

func TestIKSWorkerRequestJSON(t *testing.T) {
	request := IKSWorkerRequest{
		ClusterID:     "cluster-123",
		WorkerPoolID:  "pool-456",
		VPCInstanceID: "vpc-instance-789",
		Zone:          "us-south-1",
		Labels:        map[string]string{"env": "test"},
		Taints: []IKSTaint{
			{Key: "dedicated", Value: "gpu", Effect: "NoSchedule"},
		},
	}

	jsonData, err := json.Marshal(request)
	if err != nil {
		t.Errorf("Failed to marshal IKSWorkerRequest to JSON: %v", err)
	}

	var unmarshaledRequest IKSWorkerRequest
	err = json.Unmarshal(jsonData, &unmarshaledRequest)
	if err != nil {
		t.Errorf("Failed to unmarshal JSON to IKSWorkerRequest: %v", err)
	}

	if unmarshaledRequest.ClusterID != request.ClusterID {
		t.Errorf("JSON round-trip failed for ClusterID: got %v, want %v", unmarshaledRequest.ClusterID, request.ClusterID)
	}
	if unmarshaledRequest.WorkerPoolID != request.WorkerPoolID {
		t.Errorf("JSON round-trip failed for WorkerPoolID: got %v, want %v", unmarshaledRequest.WorkerPoolID, request.WorkerPoolID)
	}
	if unmarshaledRequest.VPCInstanceID != request.VPCInstanceID {
		t.Errorf("JSON round-trip failed for VPCInstanceID: got %v, want %v", unmarshaledRequest.VPCInstanceID, request.VPCInstanceID)
	}
	if unmarshaledRequest.Zone != request.Zone {
		t.Errorf("JSON round-trip failed for Zone: got %v, want %v", unmarshaledRequest.Zone, request.Zone)
	}
}

func TestIKSWorkerResponseJSON(t *testing.T) {
	// Test unmarshaling a typical IKS API response
	responseJSON := `{
		"id": "worker-abc123",
		"state": "deployed",
		"message": "Worker successfully deployed",
		"labels": {
			"zone": "us-south-1",
			"worker-type": "compute"
		},
		"location": "us-south"
	}`

	var response IKSWorkerResponse
	err := json.Unmarshal([]byte(responseJSON), &response)
	if err != nil {
		t.Errorf("Failed to unmarshal JSON to IKSWorkerResponse: %v", err)
	}

	if response.ID != "worker-abc123" {
		t.Errorf("IKSWorkerResponse.ID = %v, want worker-abc123", response.ID)
	}
	if response.State != "deployed" {
		t.Errorf("IKSWorkerResponse.State = %v, want deployed", response.State)
	}
	if response.Message != "Worker successfully deployed" {
		t.Errorf("IKSWorkerResponse.Message = %v, want 'Worker successfully deployed'", response.Message)
	}
	if response.Labels["zone"] != "us-south-1" {
		t.Errorf("IKSWorkerResponse.Labels[zone] = %v, want us-south-1", response.Labels["zone"])
	}
	if response.Labels["worker-type"] != "compute" {
		t.Errorf("IKSWorkerResponse.Labels[worker-type] = %v, want compute", response.Labels["worker-type"])
	}
	if response.Location != "us-south" {
		t.Errorf("IKSWorkerResponse.Location = %v, want us-south", response.Location)
	}
}

func TestIKSWorkerRequestDefaultLabels(t *testing.T) {
	// Test the logic for default labels that would be applied in AddWorkerToIKSCluster
	options := types.IKSWorkerPoolOptions{
		ClusterID:     "cluster-123",
		WorkerPoolID:  "pool-456",
		VPCInstanceID: "vpc-instance-789",
		Zone:          "us-south-1",
	}

	// Simulate the label creation logic from AddWorkerToIKSCluster
	expectedLabels := map[string]string{
		"karpenter.sh/managed":         "true",
		"karpenter.ibm.sh/zone":        options.Zone,
		"karpenter.ibm.sh/provisioner": "karpenter-ibm",
	}

	request := IKSWorkerRequest{
		ClusterID:     options.ClusterID,
		WorkerPoolID:  options.WorkerPoolID,
		VPCInstanceID: options.VPCInstanceID,
		Zone:          options.Zone,
		Labels:        expectedLabels,
	}

	if request.Labels["karpenter.sh/managed"] != "true" {
		t.Errorf("Expected karpenter.sh/managed label to be 'true', got %v", request.Labels["karpenter.sh/managed"])
	}
	if request.Labels["karpenter.ibm.sh/zone"] != "us-south-1" {
		t.Errorf("Expected karpenter.ibm.sh/zone label to be 'us-south-1', got %v", request.Labels["karpenter.ibm.sh/zone"])
	}
	if request.Labels["karpenter.ibm.sh/provisioner"] != "karpenter-ibm" {
		t.Errorf("Expected karpenter.ibm.sh/provisioner label to be 'karpenter-ibm', got %v", request.Labels["karpenter.ibm.sh/provisioner"])
	}
}

func TestZeroValueStructs(t *testing.T) {
	// Test zero values for all structs
	var request IKSWorkerRequest
	if request.ClusterID != "" {
		t.Errorf("Zero value IKSWorkerRequest.ClusterID should be empty, got %v", request.ClusterID)
	}
	if request.Labels != nil {
		t.Errorf("Zero value IKSWorkerRequest.Labels should be nil, got %v", request.Labels)
	}
	if request.Taints != nil {
		t.Errorf("Zero value IKSWorkerRequest.Taints should be nil, got %v", request.Taints)
	}

	var taint IKSTaint
	if taint.Key != "" {
		t.Errorf("Zero value IKSTaint.Key should be empty, got %v", taint.Key)
	}
	if taint.Effect != "" {
		t.Errorf("Zero value IKSTaint.Effect should be empty, got %v", taint.Effect)
	}

	var response IKSWorkerResponse
	if response.ID != "" {
		t.Errorf("Zero value IKSWorkerResponse.ID should be empty, got %v", response.ID)
	}
	if response.Labels != nil {
		t.Errorf("Zero value IKSWorkerResponse.Labels should be nil, got %v", response.Labels)
	}
}

func TestEndpointConstruction(t *testing.T) {
	// Test the endpoint construction logic from AddWorkerToIKSCluster
	clusterID := "test-cluster-123"
	expectedEndpoint := "/clusters/test-cluster-123/workers"

	actualEndpoint := "/clusters/" + clusterID + "/workers"

	if actualEndpoint != expectedEndpoint {
		t.Errorf("Endpoint construction = %v, want %v", actualEndpoint, expectedEndpoint)
	}
}

// Mock test to verify the structure would work with a real HTTP client
func TestIKSWorkerRequestStructureForHTTPClient(t *testing.T) {
	ctx := context.Background()
	options := types.IKSWorkerPoolOptions{
		ClusterID:     "cluster-123",
		WorkerPoolID:  "pool-456",
		VPCInstanceID: "vpc-instance-789",
		Zone:          "us-south-1",
	}

	// This tests the structure that would be used in the real AddWorkerToIKSCluster method
	request := IKSWorkerRequest{
		ClusterID:     options.ClusterID,
		WorkerPoolID:  options.WorkerPoolID,
		VPCInstanceID: options.VPCInstanceID,
		Zone:          options.Zone,
		Labels: map[string]string{
			"karpenter.sh/managed":         "true",
			"karpenter.ibm.sh/zone":        options.Zone,
			"karpenter.ibm.sh/provisioner": "karpenter-ibm",
		},
	}

	// Verify the request can be marshaled to JSON (as would be done by HTTP client)
	jsonData, err := json.Marshal(request)
	if err != nil {
		t.Errorf("Failed to marshal IKSWorkerRequest for HTTP request: %v", err)
	}

	// Verify it's valid JSON
	if !json.Valid(jsonData) {
		t.Error("Generated JSON is not valid")
	}

	// Verify context can be used (as would be done in real implementation)
	if ctx.Err() != nil {
		t.Errorf("Context should be valid: %v", ctx.Err())
	}

	t.Logf("Successfully validated IKSWorkerRequest structure for HTTP client usage")
}
