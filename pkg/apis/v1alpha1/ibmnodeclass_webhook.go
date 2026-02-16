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

package v1alpha1

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	// IBM Cloud resource ID pattern - supports variable-length region numbers (r006, r010, r042, etc.)
	ibmResourceIDPattern = regexp.MustCompile(`^r[0-9]+-[a-zA-Z0-9]{8}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{12}$`)
	// IBM Cloud subnet ID pattern - subnets use 4-digit prefix instead of r###-
	ibmSubnetIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{4}-[a-zA-Z0-9]{8}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{4}-[a-zA-Z0-9]{12}$`)
	// API server endpoint pattern
	apiServerEndpointPattern = regexp.MustCompile(`^https?://[a-zA-Z0-9.-]+:\d+$`)
)

// SetupWebhookWithManager sets up the webhook with the manager
func (nc *IBMNodeClass) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, nc).
		WithCustomValidator(nc).
		Complete()
}

// +kubebuilder:webhook:path=/validate-karpenter-ibm-sh-v1alpha1-ibmnodeclass,mutating=false,failurePolicy=fail,sideEffects=None,groups=karpenter-ibm.sh,resources=ibmnodeclasses,verbs=create;update,versions=v1alpha1,name=vibmnodeclass.kb.io,admissionReviewVersions=v1

var _ admission.CustomValidator = &IBMNodeClass{}

// ValidateCreate implements admission.CustomValidator
func (nc *IBMNodeClass) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	nodeClass, ok := obj.(*IBMNodeClass)
	if !ok {
		return nil, fmt.Errorf("expected IBMNodeClass, got %T", obj)
	}
	return nc.validateNodeClass(nodeClass)
}

// ValidateUpdate implements admission.CustomValidator
func (nc *IBMNodeClass) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newNodeClass, ok := newObj.(*IBMNodeClass)
	if !ok {
		return nil, fmt.Errorf("expected IBMNodeClass, got %T", newObj)
	}
	return nc.validateNodeClass(newNodeClass)
}

// ValidateDelete implements admission.CustomValidator
func (nc *IBMNodeClass) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (nc *IBMNodeClass) validateNodeClass(nodeClass *IBMNodeClass) (admission.Warnings, error) {
	var warnings admission.Warnings
	var errs []string

	// Validate API server endpoint
	if nodeClass.Spec.APIServerEndpoint == "" {
		errs = append(errs, "apiServerEndpoint is required for nodes to join the cluster")
	} else if !apiServerEndpointPattern.MatchString(nodeClass.Spec.APIServerEndpoint) {
		errs = append(errs, fmt.Sprintf("apiServerEndpoint '%s' is not a valid URL (expected format: https://IP:PORT or http://IP:PORT)", nodeClass.Spec.APIServerEndpoint))
	}

	// Validate bootstrap mode
	if nodeClass.Spec.BootstrapMode == nil {
		warnings = append(warnings, "bootstrapMode not specified, defaulting to 'cloud-init'")
	} else if *nodeClass.Spec.BootstrapMode != "cloud-init" && *nodeClass.Spec.BootstrapMode != "iks" && *nodeClass.Spec.BootstrapMode != "user-data" {
		errs = append(errs, fmt.Sprintf("invalid bootstrapMode '%s' (valid values: cloud-init, iks, user-data)", *nodeClass.Spec.BootstrapMode))
	}

	warnings = append(warnings, nc.validateSecurityGroups(nodeClass, &errs)...)
	warnings = append(warnings, nc.validateImage(nodeClass)...)
	errs = append(errs, nc.validateSSHKeys(nodeClass)...)
	errs = append(errs, nc.validateSubnet(nodeClass)...)
	errs = append(errs, nc.validateVPC(nodeClass)...)

	// OneOf constraint validation - prevent configurations known to cause oneOf errors
	errs = append(errs, nc.validateOneOfConstraints(nodeClass)...)
	warnings = append(warnings, nc.generateOneOfWarnings(nodeClass)...)

	if len(errs) > 0 {
		return warnings, fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
	}

	return warnings, nil
}

func (nc *IBMNodeClass) validateSecurityGroups(nodeClass *IBMNodeClass, errs *[]string) admission.Warnings {
	var warnings admission.Warnings

	if len(nodeClass.Spec.SecurityGroups) == 0 {
		warnings = append(warnings, "no security groups specified, will use VPC default security group")
	} else {
		for _, sg := range nodeClass.Spec.SecurityGroups {
			if !ibmResourceIDPattern.MatchString(sg) {
				*errs = append(*errs, fmt.Sprintf("security group '%s' is not a valid IBM Cloud resource ID (format: r###-########-####-####-####-############)", sg))
			}
		}
	}
	return warnings
}

func (nc *IBMNodeClass) validateImage(nodeClass *IBMNodeClass) admission.Warnings {
	var warnings admission.Warnings

	if nodeClass.Spec.Image != "" && !ibmResourceIDPattern.MatchString(nodeClass.Spec.Image) {
		warnings = append(warnings, fmt.Sprintf("image '%s' appears to be a name, not an ID. Image resolution by name may be slower and less reliable than using IDs", nodeClass.Spec.Image))
	}
	return warnings
}

func (nc *IBMNodeClass) validateSSHKeys(nodeClass *IBMNodeClass) []string {
	var errs []string

	for _, key := range nodeClass.Spec.SSHKeys {
		if !ibmResourceIDPattern.MatchString(key) {
			errs = append(errs, fmt.Sprintf("SSH key '%s' is not a valid IBM Cloud resource ID (format: r###-########-####-####-####-############)", key))
		}
	}
	return errs
}

func (nc *IBMNodeClass) validateSubnet(nodeClass *IBMNodeClass) []string {
	var errs []string

	if nodeClass.Spec.Subnet != "" {
		if !ibmSubnetIDPattern.MatchString(nodeClass.Spec.Subnet) {
			errs = append(errs, fmt.Sprintf("subnet '%s' is not a valid IBM Cloud subnet ID (format: ####-########-####-####-####-############)", nodeClass.Spec.Subnet))
		}
	}
	return errs
}

func (nc *IBMNodeClass) validateVPC(nodeClass *IBMNodeClass) []string {
	var errs []string

	if nodeClass.Spec.VPC == "" {
		errs = append(errs, "vpc is required")
	} else if !ibmResourceIDPattern.MatchString(nodeClass.Spec.VPC) {
		errs = append(errs, fmt.Sprintf("vpc '%s' is not a valid IBM Cloud resource ID", nodeClass.Spec.VPC))
	}
	return errs
}

// validateOneOfConstraints validates configurations that might cause IBM VPC oneOf constraint errors
func (nc *IBMNodeClass) validateOneOfConstraints(nodeClass *IBMNodeClass) []string {
	var errs []string

	// Validate block device mappings for oneOf constraints
	if bdmErrs := nc.validateBlockDeviceMappings(nodeClass); len(bdmErrs) > 0 {
		errs = append(errs, bdmErrs...)
	}

	// Validate instance configuration for oneOf constraints
	if instErrs := nc.validateInstanceConfiguration(nodeClass); len(instErrs) > 0 {
		errs = append(errs, instErrs...)
	}

	return errs
}

// generateOneOfWarnings generates warnings about configurations that might cause oneOf issues
func (nc *IBMNodeClass) generateOneOfWarnings(nodeClass *IBMNodeClass) admission.Warnings {
	var warnings admission.Warnings

	// Warn about dynamic instance type selection risks
	if nodeClass.Spec.InstanceProfile == "" {
		warnings = append(warnings,
			"Dynamic instance type selection detected (no instanceProfile specified). "+
				"This configuration has been known to cause 'oneOf constraint' errors with IBM VPC API. "+
				"STRONG RECOMMENDATION: Specify a static instanceProfile (e.g., 'bx2-2x8') to prevent provisioning failures.")
	}

	// Warn about complex block device mapping combinations
	if len(nodeClass.Spec.BlockDeviceMappings) > 1 {
		hasRootVolume := false
		for _, mapping := range nodeClass.Spec.BlockDeviceMappings {
			if mapping.RootVolume {
				hasRootVolume = true
				break
			}
		}
		if !hasRootVolume {
			warnings = append(warnings,
				"Multiple block device mappings specified without marking any as root volume. "+
					"This may cause volume attachment oneOf constraint errors. "+
					"Ensure exactly one mapping has RootVolume=true.")
		}
	}

	return warnings
}

// validateBlockDeviceMappings validates block device mapping configurations for oneOf compliance
func (nc *IBMNodeClass) validateBlockDeviceMappings(nodeClass *IBMNodeClass) []string {
	var errs []string

	if len(nodeClass.Spec.BlockDeviceMappings) == 0 {
		// No explicit mappings - will use defaults, which is fine
		return errs
	}

	// Rule 1: Exactly one root volume must be specified if any mappings exist
	rootVolumeCount := 0
	for _, mapping := range nodeClass.Spec.BlockDeviceMappings {
		if mapping.RootVolume {
			rootVolumeCount++
		}
	}

	if rootVolumeCount == 0 {
		errs = append(errs,
			"block device mappings specified but no root volume marked: "+
				"exactly one mapping must have RootVolume=true to satisfy IBM VPC oneOf constraints")
	} else if rootVolumeCount > 1 {
		errs = append(errs,
			fmt.Sprintf("multiple root volumes specified (%d): "+
				"exactly one mapping must have RootVolume=true to satisfy IBM VPC oneOf constraints", rootVolumeCount))
	}

	// Rule 2: Validate individual volume specifications for oneOf requirements
	for i, mapping := range nodeClass.Spec.BlockDeviceMappings {
		if volErrs := nc.validateVolumeSpec(mapping.VolumeSpec, i, mapping.RootVolume); len(volErrs) > 0 {
			errs = append(errs, volErrs...)
		}
	}

	return errs
}

// validateVolumeSpec validates a VolumeSpec for oneOf constraint requirements
func (nc *IBMNodeClass) validateVolumeSpec(volSpec *VolumeSpec, index int, isRoot bool) []string {
	var errs []string

	if volSpec == nil {
		// No volume spec means defaults will be used - this is valid
		return errs
	}

	// Rule: Volume must specify exactly one of: capacity, snapshot, or existing volume
	// For now we only support capacity-based volumes, so just validate that capacity is reasonable

	if volSpec.Capacity != nil {
		capacity := *volSpec.Capacity
		if isRoot {
			// Root volumes: 10GB to 250GB typically
			if capacity < 10 || capacity > 250 {
				errs = append(errs,
					fmt.Sprintf("root volume capacity %dGB in mapping %d is outside recommended range (10-250GB): "+
						"this may cause IBM VPC oneOf constraint validation to fail", capacity, index))
			}
		} else {
			// Data volumes: 10GB to 16000GB typically
			if capacity < 10 || capacity > 16000 {
				errs = append(errs,
					fmt.Sprintf("data volume capacity %dGB in mapping %d is outside valid range (10-16000GB): "+
						"this will cause IBM VPC oneOf constraint validation to fail", capacity, index))
			}
		}
	}

	// Validate profile if specified
	if volSpec.Profile != nil {
		profile := *volSpec.Profile
		validProfiles := []string{"general-purpose", "5iops-tier", "10iops-tier", "custom"}
		isValidProfile := false
		for _, valid := range validProfiles {
			if profile == valid {
				isValidProfile = true
				break
			}
		}
		if !isValidProfile {
			errs = append(errs,
				fmt.Sprintf("volume profile '%s' in mapping %d is not recognized: "+
					"use one of %v to avoid IBM VPC oneOf constraint errors", profile, index, validProfiles))
		}
	}

	return errs
}

// validateInstanceProfile validates instance profile configuration for oneOf compliance
func (nc *IBMNodeClass) validateInstanceProfile(nodeClass *IBMNodeClass) []string {
	var errs []string

	// IBM Cloud instance profile format validation
	// Valid formats: bx2-2x8, cx2-4x8, mx2-8x64, etc.
	instanceProfilePattern := regexp.MustCompile(`^[a-z0-9]+[0-9a-z]*-[0-9]+x[0-9]+$`)

	if nodeClass.Spec.InstanceProfile != "" {
		// Validate format
		if !instanceProfilePattern.MatchString(nodeClass.Spec.InstanceProfile) {
			errs = append(errs,
				fmt.Sprintf("instanceProfile '%s' has invalid format: "+
					"must match pattern 'family-vcpuxmemory' (e.g., 'bx2-2x8', 'cx2-4x16'). "+
					"Invalid instance profiles cause IBM VPC oneOf constraint errors",
					nodeClass.Spec.InstanceProfile))
		}

		// Check for common invalid patterns that cause oneOf errors
		if strings.Contains(nodeClass.Spec.InstanceProfile, " ") {
			errs = append(errs,
				fmt.Sprintf("instanceProfile '%s' contains spaces: "+
					"instance profiles cannot contain spaces and will cause IBM VPC oneOf constraint validation to fail",
					nodeClass.Spec.InstanceProfile))
		}

		// Validate known instance families
		parts := strings.Split(nodeClass.Spec.InstanceProfile, "-")
		if len(parts) >= 2 {
			validFamilies := []string{"bx2", "bx3d", "cx2", "cx3d", "mx2", "mx3d", "ux2d", "vx2d", "ox2", "gx2", "gx3"}
			family := parts[0]
			isValidFamily := false
			for _, validFamily := range validFamilies {
				if family == validFamily {
					isValidFamily = true
					break
				}
			}
			if !isValidFamily {
				errs = append(errs,
					fmt.Sprintf("instanceProfile '%s' uses unknown instance family '%s': "+
						"use one of %v to avoid potential IBM VPC API issues",
						nodeClass.Spec.InstanceProfile, family, validFamilies))
			}
		}

		// Validate specific configurations known to cause issues
		if strings.HasPrefix(nodeClass.Spec.InstanceProfile, "cx2-") {
			// CX2 instances have specific constraints
			parts := strings.Split(nodeClass.Spec.InstanceProfile, "-")
			if len(parts) == 2 {
				vcpuMemPart := parts[1]
				// Check for valid CX2 configurations
				validCX2 := []string{"2x4", "4x8", "8x16", "16x32", "32x64", "48x96"}
				isValidCX2 := false
				for _, valid := range validCX2 {
					if vcpuMemPart == valid {
						isValidCX2 = true
						break
					}
				}
				if !isValidCX2 {
					errs = append(errs,
						fmt.Sprintf("instanceProfile '%s' uses invalid CX2 configuration '%s': "+
							"valid CX2 configurations are %v",
							nodeClass.Spec.InstanceProfile, vcpuMemPart, validCX2))
				}
			}
		}
	}

	return errs
}

// validateInstanceConfiguration validates instance-level configurations for oneOf compliance
func (nc *IBMNodeClass) validateInstanceConfiguration(nodeClass *IBMNodeClass) []string {
	var errs []string

	// Rule 1: Validate instance profile for oneOf constraint compliance
	if instProfileErrs := nc.validateInstanceProfile(nodeClass); len(instProfileErrs) > 0 {
		errs = append(errs, instProfileErrs...)
	}

	// Rule 2: Resource group must be specified for VNI oneOf constraint compliance
	// This is critical based on our previous debugging
	if nodeClass.Spec.ResourceGroup == "" {
		errs = append(errs,
			"resourceGroup is required: missing resource group causes IBM VPC oneOf constraint errors "+
				"in Virtual Network Interface (VNI) prototype creation")
	}

	// Rule 3: Validate zone and region consistency
	if nodeClass.Spec.Zone != "" && nodeClass.Spec.Region != "" {
		// Basic zone format validation - zones should be region-based
		expectedPrefix := nodeClass.Spec.Region + "-"
		if !regexp.MustCompile(`^` + regexp.QuoteMeta(expectedPrefix) + `\d+$`).MatchString(nodeClass.Spec.Zone) {
			errs = append(errs,
				fmt.Sprintf("zone '%s' does not match region '%s': "+
					"zone should be in format '%s1', '%s2', etc. to avoid IBM VPC API errors",
					nodeClass.Spec.Zone, nodeClass.Spec.Region, expectedPrefix, expectedPrefix))
		}
	}

	// Rule 4: Validate security groups format for oneOf compliance
	for i, sgID := range nodeClass.Spec.SecurityGroups {
		if !ibmResourceIDPattern.MatchString(sgID) {
			errs = append(errs,
				fmt.Sprintf("security group %d '%s' is not a valid IBM Cloud resource ID: "+
					"invalid resource IDs may cause oneOf constraint errors during VNI creation", i, sgID))
		}
	}

	return errs
}
