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

package pricing

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
)

type profilePricePlan struct {
	profile string
	plan    string
	units   []string
	metric  string
}

func profilePlan(entry globalcatalogv1.CatalogEntry) (*profilePricePlan, error) {
	if entry.ID == nil || entry.Name == nil || entry.Metadata == nil || entry.Metadata.Other["profile"] == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(entry.Metadata.Other["profile"])
	if err != nil {
		return nil, err
	}
	var profile struct {
		ResourceType string `json:"resource_type"`
		Measures     []struct {
			Component   string `json:"component"`
			Deployments []struct {
				Type   string `json:"type"`
				Plan   string `json:"plan"`
				Meters []struct {
					Unit     string `json:"unit"`
					Quantity string `json:"quantity"`
				} `json:"meters"`
			} `json:"deployments"`
		} `json:"measures"`
	}
	if err = json.Unmarshal(encoded, &profile); err != nil {
		return nil, fmt.Errorf("decoding profile %s pricing measures: %w", *entry.Name, err)
	}
	if profile.ResourceType != "instance" {
		return nil, nil
	}
	var selected *profilePricePlan
	for _, measure := range profile.Measures {
		if measure.Component != "instance" {
			continue
		}
		for _, deployment := range measure.Deployments {
			if deployment.Type != "multi-tenant" {
				continue
			}
			if selected != nil || deployment.Plan == "" {
				return nil, fmt.Errorf("profile %s has ambiguous multi-tenant pricing plan", *entry.Name)
			}
			selected = &profilePricePlan{profile: *entry.Name, plan: deployment.Plan}
			gpu, gpuCard, instanceMeter := false, "", false
			for _, meter := range deployment.Meters {
				if meter.Unit == "PROFILE" {
					if meter.Quantity != *entry.Name {
						return nil, fmt.Errorf("profile %s has a different pricing profile", *entry.Name)
					}
					continue
				}
				if meter.Unit == "GPU_CARD" {
					gpuCard = meter.Quantity
					continue
				}
				quantity, parseErr := strconv.ParseFloat(meter.Quantity, 64)
				if parseErr != nil || quantity < 0 || math.IsInf(quantity, 0) || math.IsNaN(quantity) || meter.Unit == "" {
					return nil, fmt.Errorf("profile %s has an invalid %s pricing meter", *entry.Name, meter.Unit)
				}
				if quantity == 0 {
					continue
				}
				switch meter.Unit {
				case "INSTANCE_MULTI_TENANT":
					if quantity != 1 {
						return nil, fmt.Errorf("profile %s has invalid multi-tenant selector", *entry.Name)
					}
					instanceMeter = true
				case "INSTANCE_DEDICATED_HOST", "INSTANCE_RESERVATION":
					return nil, fmt.Errorf("profile %s multi-tenant plan enables another tenancy selector", *entry.Name)
				case "INSTANCE":
					if quantity != 1 {
						return nil, fmt.Errorf("profile %s has an invalid instance count", *entry.Name)
					}
				case "GPU":
					gpu = true
				case "IS_STORAGE", "IS_LARGE_STORAGE":
					selected.units = append(selected.units, meter.Unit+"_GIGABYTE_HOURS")
				default:
					selected.units = append(selected.units, meter.Unit+"_HOURS")
				}
			}
			if gpu {
				if gpuCard == "" {
					return nil, fmt.Errorf("profile %s has no GPU pricing model", *entry.Name)
				}
				selected.units = append(selected.units, gpuCard+"_HOURS")
			}
			if instanceMeter {
				selected.units = []string{"INSTANCE_HOURS_MULTI_TENANT"}
				selected.metric = "part-is.instance-hours-" + *entry.Name
			}
			if len(selected.units) == 0 {
				return nil, fmt.Errorf("profile %s has no billable pricing meters", *entry.Name)
			}
		}
	}
	return selected, nil
}

func profileHourlyPrice(quote *globalcatalogv1.PricingGet, profile *profilePricePlan) (float64, error) {
	if profile.metric != "" {
		if quote == nil {
			return 0, fmt.Errorf("no profile pricing response")
		}
		matched := false
		for _, metric := range quote.Metrics {
			if metric.ChargeUnitName != nil && *metric.ChargeUnitName == "INSTANCE_HOURS_MULTI_TENANT" {
				if metric.MetricID == nil || *metric.MetricID != profile.metric {
					return 0, fmt.Errorf("instance pricing metric belongs to another profile")
				}
				matched = true
			}
		}
		if !matched {
			return 0, fmt.Errorf("no multi-tenant instance price for profile %s", profile.profile)
		}
	}
	return compositeHourlyPrice(quote, profile.units)
}

func compositeDeployment(entry globalcatalogv1.CatalogEntry, plan, region string) bool {
	if entry.ID == nil || entry.Name == nil || entry.ParentID == nil || *entry.ParentID != plan || entry.Metadata == nil || entry.Metadata.Deployment == nil || entry.Metadata.Deployment.Location == nil || *entry.Metadata.Deployment.Location != region {
		return false
	}
	if entry.Disabled != nil && *entry.Disabled || entry.Active != nil && !*entry.Active {
		return false
	}
	for _, tag := range entry.Tags {
		if tag == "is.composite" {
			return true
		}
	}
	return false
}

func compositeHourlyPrice(quote *globalcatalogv1.PricingGet, units []string) (float64, error) {
	if quote == nil || quote.DeploymentLocationNoPriceAvailable != nil && *quote.DeploymentLocationNoPriceAvailable {
		return 0, fmt.Errorf("no pricing data found in API response")
	}
	metrics := map[string]globalcatalogv1.Metrics{}
	for _, metric := range quote.Metrics {
		if metric.ChargeUnitName == nil {
			continue
		}
		unit := *metric.ChargeUnitName
		if _, duplicate := metrics[unit]; duplicate {
			return 0, fmt.Errorf("duplicate pricing metric %s", unit)
		}
		metrics[unit] = metric
	}
	total := float64(0)
	seen := map[string]bool{}
	for _, unit := range units {
		if seen[unit] {
			return 0, fmt.Errorf("duplicate profile pricing meter %s", unit)
		}
		seen[unit] = true
		metric, exists := metrics[unit]
		if !exists {
			return 0, fmt.Errorf("no pricing data found in API response for %s", unit)
		}
		if metric.ChargeUnitQuantity == nil || *metric.ChargeUnitQuantity != 1 || metric.TierModel == nil || strings.ReplaceAll(strings.ToLower(*metric.TierModel), " ", "_") != "linear_tier" {
			return 0, fmt.Errorf("unsupported composite pricing units for %s", unit)
		}
		price, err := usdLinearPrice(metric)
		if err != nil {
			return 0, fmt.Errorf("pricing metric %s: %w", unit, err)
		}
		// IBM composite deployment amounts already include the profile's meter quantity.
		total += price
	}
	if len(units) == 0 || total <= 0 || math.IsInf(total, 0) || math.IsNaN(total) {
		return 0, fmt.Errorf("no positive hourly profile price found in API response")
	}
	return total, nil
}

func usdLinearPrice(metric globalcatalogv1.Metrics) (float64, error) {
	for _, country := range []string{"USA", "USD", ""} {
		for _, amount := range metric.Amounts {
			if amount.Currency == nil || *amount.Currency != "USD" || amount.Country != nil && *amount.Country != country || amount.Country == nil && country != "" {
				continue
			}
			if len(amount.Prices) != 1 || amount.Prices[0].QuantityTier == nil || *amount.Prices[0].QuantityTier != 1 || amount.Prices[0].Price == nil {
				return 0, fmt.Errorf("missing linear USD quote")
			}
			price := *amount.Prices[0].Price
			if price < 0 || math.IsInf(price, 0) || math.IsNaN(price) {
				return 0, fmt.Errorf("invalid USD quote")
			}
			return price, nil
		}
	}
	return 0, fmt.Errorf("no USD quote available")
}
