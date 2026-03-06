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
package tagging

import (
	"context"
	"fmt"
	"strings"

	"github.com/awslabs/operatorpkg/reconciler"
	"github.com/awslabs/operatorpkg/singleton"
	v1 "k8s.io/api/core/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	vpcProvider "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/instance"
)

// Controller reconciles NodeClaim objects to ensure proper tagging of IBM Cloud instances
// +kubebuilder:rbac:groups=karpenter.sh,resources=nodeclaims,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=karpenter-ibm.sh,resources=ibmnodeclasses,verbs=get;list;watch
type Controller struct {
	kubeClient client.Client
	ibmClient  *ibm.Client
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client) (*Controller, error) {
	// Create IBM client for tagging operations
	ibmClient, err := ibm.NewClient()
	if err != nil {
		return nil, fmt.Errorf("creating IBM client: %w", err)
	}

	return &Controller{
		kubeClient: kubeClient,
		ibmClient:  ibmClient,
	}, nil
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context) (reconciler.Result, error) {
	// List all NodeClaims
	nodeClaimList := &karpenterv1.NodeClaimList{}
	if err := c.kubeClient.List(ctx, nodeClaimList); err != nil {
		return reconciler.Result{}, err
	}

	for _, nodeClaim := range nodeClaimList.Items {
		// Skip if node name is not set
		if nodeClaim.Status.NodeName == "" {
			continue
		}

		// Get the node
		node := &v1.Node{}
		if err := c.kubeClient.Get(ctx, client.ObjectKey{Name: nodeClaim.Status.NodeName}, node); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue
			}
			return reconciler.Result{}, err
		}

		// Extract provider ID
		if node.Spec.ProviderID == "" {
			continue
		}

		// Get the NodeClass to determine if this is a VPC instance
		nodeClass := &v1alpha1.IBMNodeClass{}
		if err := c.kubeClient.Get(ctx, k8stypes.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
			log.FromContext(ctx).Error(err, "Failed to get NodeClass", "nodeclass", nodeClaim.Spec.NodeClassRef.Name)
			continue
		}

		// Only process VPC instances (IKS tagging not supported)
		if !c.isVPCMode(nodeClass) {
			continue
		}

		// Build tags map
		tags := map[string]string{
			"karpenter-ibm.sh/nodeclaim": nodeClaim.Name,
			"karpenter-ibm.sh/nodepool":  nodeClaim.Labels["karpenter.sh/nodepool"],
		}

		// Add custom tags from nodeclaim requirements
		for _, req := range nodeClaim.Spec.Requirements {
			if req.Key == "karpenter-ibm.sh/tags" {
				for _, value := range req.Values {
					parts := strings.SplitN(value, "=", 2)
					if len(parts) == 2 {
						tags[parts[0]] = parts[1]
					}
				}
			}
		}

		// Update instance tags using VPC provider
		if err := c.updateVPCInstanceTags(ctx, node.Spec.ProviderID, tags); err != nil {
			log.FromContext(ctx).Error(err, "Failed to update VPC instance tags", "provider_id", node.Spec.ProviderID)
			continue
		}
	}

	return reconciler.Result{}, nil
}

// isVPCMode determines if a NodeClass is configured for VPC mode
func (c *Controller) isVPCMode(nodeClass *v1alpha1.IBMNodeClass) bool {
	// Check if IKS cluster ID is provided (implies IKS mode)
	if nodeClass.Spec.IKSClusterID != "" {
		return false
	}

	// Check bootstrap mode
	if nodeClass.Spec.BootstrapMode != nil {
		switch *nodeClass.Spec.BootstrapMode {
		case "iks-api":
			return false
		case "cloud-init":
			return true
		}
	}

	// Default to VPC mode if VPC is specified
	return nodeClass.Spec.VPC != ""
}

// updateVPCInstanceTags updates tags on a VPC instance
func (c *Controller) updateVPCInstanceTags(ctx context.Context, providerID string, tags map[string]string) error {
	// Create VPC provider for this specific operation
	vpcProvider, err := vpcProvider.NewVPCInstanceProvider(ctx, c.ibmClient, c.kubeClient)
	if err != nil {
		return fmt.Errorf("creating VPC provider: %w", err)
	}

	// Use the VPC provider to update tags
	return vpcProvider.UpdateTags(ctx, providerID, tags)
}

// Name returns the name of the controller
func (c *Controller) Name() string {
	return "nodeclaim.tagging"
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return builder.ControllerManagedBy(m).
		Named("nodeclaim.tagging").
		For(&karpenterv1.NodeClaim{}).
		Owns(&v1.Node{}).
		Complete(singleton.AsReconciler(c))
}
