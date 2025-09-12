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
	return ctrl.NewWebhookManagedBy(mgr).
		For(nc).
		Complete()
}

// +kubebuilder:webhook:path=/validate-karpenter-ibm-sh-v1alpha1-ibmnodeclass,mutating=false,failurePolicy=fail,sideEffects=None,groups=karpenter.ibm.sh,resources=ibmnodeclasses,verbs=create;update,versions=v1alpha1,name=vibmnodeclass.kb.io,admissionReviewVersions=v1

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
