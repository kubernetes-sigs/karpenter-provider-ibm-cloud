package main

import (
	"encoding/json"
	"testing"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// vpcInstanceProfileLister defines the interface we need to mock
type vpcInstanceProfileLister interface {
	ListInstanceProfiles(listInstanceProfilesOptions *vpcv1.ListInstanceProfilesOptions) (*vpcv1.InstanceProfileCollection, *core.DetailedResponse, error)
}

// mockVPCClient implements the necessary interface for testing
type mockVPCClient struct{}

func (m *mockVPCClient) ListInstanceProfiles(listInstanceProfilesOptions *vpcv1.ListInstanceProfilesOptions) (*vpcv1.InstanceProfileCollection, *core.DetailedResponse, error) {
	name := "test-profile"
	vcpuCount := int64(4)
	memoryValue := int64(8192)

	return &vpcv1.InstanceProfileCollection{
		Profiles: []vpcv1.InstanceProfile{
			{
				Name: &name,
				VcpuCount: &vpcv1.InstanceProfileVcpu{
					Value: &vcpuCount,
				},
				Memory: &vpcv1.InstanceProfileMemory{
					Value: &memoryValue,
				},
			},
		},
	}, &core.DetailedResponse{}, nil
}

// Modify fetchInstanceProfiles to accept our interface instead of concrete type
func fetchInstanceProfilesTest(client vpcInstanceProfileLister) ([]vpcv1.InstanceProfile, error) {
	listProfilesOptions := &vpcv1.ListInstanceProfilesOptions{}
	profiles, _, err := client.ListInstanceProfiles(listProfilesOptions)
	if err != nil {
		return nil, err
	}
	return profiles.Profiles, nil
}

func TestRegionZones(t *testing.T) {
	// Verify region zones mapping
	expectedRegions := []string{
		"au-syd", "jp-osa", "jp-tok", "eu-de", "eu-es",
		"eu-gb", "ca-tor", "us-south", "us-east", "br-sao",
	}

	for _, region := range expectedRegions {
		zones, exists := regionZones[region]
		assert.True(t, exists, "Region %s should exist in regionZones map", region)
		assert.Len(t, zones, 3, "Region %s should have exactly 3 zones", region)
	}
}

func TestIBMInstanceTypeJSON(t *testing.T) {
	instance := IBMInstanceType{
		Name:         "test-instance",
		Architecture: "amd64",
		OperatingSystems: []corev1.OSName{
			corev1.Linux,
		},
		Resources: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("4"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
		},
		Offerings: []IBMOffering{
			{
				Requirements: []corev1.NodeSelectorRequirement{
					{
						Key:      v1.CapacityTypeLabelKey,
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{v1.CapacityTypeOnDemand},
					},
				},
				Offering: cloudprovider.Offering{
					Price:     0.5,
					Available: true,
				},
			},
		},
	}

	// Test marshaling
	data, err := json.Marshal(instance)
	assert.NoError(t, err)
	assert.NotEmpty(t, data)

	// Test unmarshaling
	var unmarshaled IBMInstanceType
	err = json.Unmarshal(data, &unmarshaled)
	assert.NoError(t, err)
	assert.Equal(t, instance.Name, unmarshaled.Name)
	assert.Equal(t, instance.Architecture, unmarshaled.Architecture)
	assert.Equal(t, instance.OperatingSystems, unmarshaled.OperatingSystems)
	assert.Equal(t, instance.Resources, unmarshaled.Resources)
	assert.Equal(t, len(instance.Offerings), len(unmarshaled.Offerings))
}

func TestFetchInstanceProfiles(t *testing.T) {
	// Create a mock implementation
	mock := &mockVPCClient{}

	profiles, err := fetchInstanceProfilesTest(mock)
	assert.NoError(t, err)
	assert.NotNil(t, profiles)
	assert.Len(t, profiles, 1)
	assert.Equal(t, "test-profile", *profiles[0].Name)
}

// mockGlobalCatalogService implements the globalCatalogService interface
type mockGlobalCatalogService struct{}

func (m *mockGlobalCatalogService) ListCatalogEntries(options *globalcatalogv1.ListCatalogEntriesOptions) (*globalcatalogv1.EntrySearchResult, *core.DetailedResponse, error) {
	id := "test-id"
	return &globalcatalogv1.EntrySearchResult{
		Resources: []globalcatalogv1.CatalogEntry{
			{
				ID: &id,
			},
		},
	}, &core.DetailedResponse{}, nil
}

func (m *mockGlobalCatalogService) GetPricing(options *globalcatalogv1.GetPricingOptions) (*globalcatalogv1.PricingGet, *core.DetailedResponse, error) {
	price := float64(0.5)
	country := "USA"
	return &globalcatalogv1.PricingGet{
		Metrics: []globalcatalogv1.Metrics{
			{
				Amounts: []globalcatalogv1.Amount{
					{
						Country: &country,
						Prices: []globalcatalogv1.Price{
							{
								Price: &price,
							},
						},
					},
				},
			},
		},
	}, &core.DetailedResponse{}, nil
}

func TestFetchPricing(t *testing.T) {
	mockCatalog := &mockGlobalCatalogService{}
	price, err := fetchPricing(mockCatalog, "test-profile")
	assert.NoError(t, err)
	assert.Equal(t, 0.5, price)
}
