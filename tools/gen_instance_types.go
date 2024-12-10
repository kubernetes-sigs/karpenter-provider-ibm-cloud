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

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

var regionZones = map[string][]string{
	"au-syd":   {"au-syd-1", "au-syd-2", "au-syd-3"},
	"jp-osa":   {"jp-osa-1", "jp-osa-2", "jp-osa-3"},
	"jp-tok":   {"jp-tok-1", "jp-tok-2", "jp-tok-3"},
	"eu-de":    {"eu-de-1", "eu-de-2", "eu-de-3"},
	"eu-es":    {"eu-es-1", "eu-es-2", "eu-es-3"},
	"eu-gb":    {"eu-gb-1", "eu-gb-2", "eu-gb-3"},
	"ca-tor":   {"ca-tor-1", "ca-tor-2", "ca-tor-3"},
	"us-south": {"us-south-1", "us-south-2", "us-south-3"},
	"us-east":  {"us-east-1", "us-east-2", "us-east-3"},
	"br-sao":   {"br-sao-1", "br-sao-2", "br-sao-3"},
}

// IBMInstanceType defines the structure for instance types
type IBMInstanceType struct {
	Name             string              `json:"name"`
	Architecture     string              `json:"architecture"`
	OperatingSystems []corev1.OSName     `json:"operatingSystems"`
	Resources        corev1.ResourceList `json:"resources"`
	Offerings        []IBMOffering       `json:"offerings"`
}

// IBMOffering defines the structure for IBM-specific offerings
type IBMOffering struct {
	Requirements []corev1.NodeSelectorRequirement `json:"requirements"`
	Offering     cloudprovider.Offering           `json:"offering"`
}

// globalCatalogService defines the interface for catalog operations
type globalCatalogService interface {
	ListCatalogEntries(options *globalcatalogv1.ListCatalogEntriesOptions) (*globalcatalogv1.EntrySearchResult, *core.DetailedResponse, error)
	GetPricing(options *globalcatalogv1.GetPricingOptions) (*globalcatalogv1.PricingGet, *core.DetailedResponse, error)
}

// initializeVPCClient initializes the VPC client using external configuration
func initializeVPCClient() (*vpcv1.VpcV1, error) {
	serviceClientOptions := &vpcv1.VpcV1Options{}
	vpcClient, err := vpcv1.NewVpcV1UsingExternalConfig(serviceClientOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create VPC client: %v", err)
	}
	return vpcClient, nil
}

// fetchInstanceProfiles fetches instance profiles using the VPC SDK
func fetchInstanceProfiles(vpcClient *vpcv1.VpcV1) ([]vpcv1.InstanceProfile, error) {
	listProfilesOptions := &vpcv1.ListInstanceProfilesOptions{}
	profiles, _, err := vpcClient.ListInstanceProfiles(listProfilesOptions)
	if err != nil {
		return nil, fmt.Errorf("error fetching instance profiles: %v", err)
	}
	return profiles.Profiles, nil
}

// initializeGlobalCatalogClient initializes the Global Catalog client using external configuration
func initializeGlobalCatalogClient() (*globalcatalogv1.GlobalCatalogV1, error) {
	serviceClientOptions := &globalcatalogv1.GlobalCatalogV1Options{}
	globalCatalog, err := globalcatalogv1.NewGlobalCatalogV1UsingExternalConfig(serviceClientOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create Global Catalog client: %v", err)
	}
	return globalCatalog, nil
}

// fetchPricing fetches instance profile pricing from IBM Cloud's Global Catalog API
func fetchPricing(catalog globalCatalogService, profileName string) (float64, error) {
	// List catalog entries matching the profile name
	listOptions := &globalcatalogv1.ListCatalogEntriesOptions{
		Q: core.StringPtr(fmt.Sprintf("name:%s", profileName)),
	}

	catalogEntries, _, err := catalog.ListCatalogEntries(listOptions)
	if err != nil {
		return 0, fmt.Errorf("error listing catalog entries: %v", err)
	}

	if catalogEntries == nil || len(catalogEntries.Resources) == 0 {
		log.Printf("No catalog entries found for profile: %s with options: %+v", profileName, listOptions)
		return 0, fmt.Errorf("no catalog entries found for profile: %s", profileName)
	}

	// Log catalog entries for debugging
	for _, entry := range catalogEntries.Resources {
		// Dereference the pointer values correctly
		name := ""
		if entry.Name != nil {
			name = *entry.Name
		}
		id := ""
		if entry.ID != nil {
			id = *entry.ID
		}
		crn := ""
		if entry.CatalogCRN != nil {
			crn = *entry.CatalogCRN
		}

		log.Printf("Catalog Entry - Name: %s, ID: %s, CatalogCRN: %s", name, id, crn)
	}

	// Use the first matching catalog entry
	catalogEntryID := *catalogEntries.Resources[0].ID

	// Get pricing data using the catalog entry ID
	pricingOptions := &globalcatalogv1.GetPricingOptions{
		ID: &catalogEntryID,
	}
	pricingData, _, err := catalog.GetPricing(pricingOptions)
	if err != nil {
		return 0, fmt.Errorf("error fetching pricing data: %v", err)
	}

	// Access pricing data from Metrics field
	if pricingData.Metrics != nil {
		for _, metric := range pricingData.Metrics {
			if metric.Amounts != nil {
				for _, amount := range metric.Amounts {
					if amount.Country != nil && *amount.Country == "USA" {
						if amount.Prices != nil {
							for _, priceObj := range amount.Prices {
								if priceObj.Price != nil {
									return *priceObj.Price, nil
								}
							}
						}
					}
				}
			}
		}
	}

	return 0, fmt.Errorf("pricing data not found for profile: %s", profileName)
}

func constructIBMInstanceTypes(vpcClient *vpcv1.VpcV1, globalCatalog globalCatalogService) []IBMInstanceType {
	instanceProfiles, err := fetchInstanceProfiles(vpcClient)
	if err != nil {
		log.Fatalf("failed to fetch instance profiles: %v", err)
	}

	var instanceTypes []IBMInstanceType
	for _, profile := range instanceProfiles {
		// handle vcpuCount using reflection
		vcpuCount := 0
		if profile.VcpuCount != nil {
			vcpuValue := reflect.ValueOf(profile.VcpuCount).Elem().FieldByName("Count")
			if vcpuValue.IsValid() && vcpuValue.Kind() == reflect.Ptr && !vcpuValue.IsNil() {
				vcpuCount = int(vcpuValue.Elem().Int())
			}
		}

		// architecture: skipping since profile.Architecture is undefined
		architecture := "" // or derive it if available from another source

		// handle memory using reflection
		memoryValue := 0
		if profile.Memory != nil {
			memValue := reflect.ValueOf(profile.Memory).Elem().FieldByName("Value")
			if memValue.IsValid() && memValue.Kind() == reflect.Ptr && !memValue.IsNil() {
				memoryValue = int(memValue.Elem().Int())
			}
		}

		// Fetch pricing from global catalog
		price, err := fetchPricing(globalCatalog, *profile.Name)
		if err != nil {
			log.Printf("failed to retrieve pricing for profile %s: %v", *profile.Name, err)
			price = 0 // fallback to zero or some default value
		}

		// define instance
		instance := IBMInstanceType{
			Name:             *profile.Name,
			Architecture:     architecture, // currently empty as explained above
			OperatingSystems: []corev1.OSName{corev1.Linux},
			Resources: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(fmt.Sprintf("%d", vcpuCount)),
				corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", memoryValue)),
			},
		}

		// add offerings
		for _, zones := range regionZones {
			for _, zone := range zones {
				instance.Offerings = append(instance.Offerings, IBMOffering{
					Requirements: []corev1.NodeSelectorRequirement{
						{Key: v1.CapacityTypeLabelKey, Operator: corev1.NodeSelectorOpIn, Values: []string{v1.CapacityTypeOnDemand}},
						{Key: corev1.LabelTopologyZone, Operator: corev1.NodeSelectorOpIn, Values: []string{zone}},
					},
					Offering: cloudprovider.Offering{
						Price:     price,
						Available: true,
					},
				})
			}
		}
		instanceTypes = append(instanceTypes, instance)
	}
	return instanceTypes
}

func main() {
	// Initialize the VPC client
	vpcClient, err := initializeVPCClient()
	if err != nil {
		log.Fatalf("failed to initialize VPC client: %v", err)
	}

	// Initialize the Global Catalog client using external configuration
	globalCatalog, err := initializeGlobalCatalogClient()
	if err != nil {
		log.Fatalf("failed to initialize Global Catalog client: %v", err)
	}

	// Construct instance types using the Global Catalog service
	instanceTypes := constructIBMInstanceTypes(vpcClient, globalCatalog)
	output, err := json.MarshalIndent(instanceTypes, "", "    ")
	if err != nil {
		log.Fatalf("failed to marshal generated instance types to JSON: %v", err)
	}

	// Write to the desired file
	outputFilePath := filepath.Join("cloudprovider", "instance_types.json")
	if err := os.WriteFile(outputFilePath, output, 0600); err != nil {
		log.Fatalf("failed to write output to file: %v", err)
	}
	fmt.Printf("Instance types have been generated and written to %s\n", outputFilePath)
}
