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

package loadbalancer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/loadbalancer"
)

// LoadBalancerProviderInterface defines the interface for load balancer operations
type LoadBalancerProviderInterface interface {
	RegisterInstance(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID, instanceIP string) error
	DeregisterInstance(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID string) error
	ValidateLoadBalancerConfiguration(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass) error
}

const (
	// LoadBalancerFinalizer is added to NodeClaims to ensure proper load balancer cleanup
	LoadBalancerFinalizer = "loadbalancer.nodeclaim.ibm.sh/finalizer"

	// LoadBalancerRegisteredAnnotation indicates the node has been registered with load balancers
	LoadBalancerRegisteredAnnotation = "loadbalancer.ibm.sh/registered"

	// LoadBalancerLastRegistrationTimeAnnotation tracks the last registration time
	LoadBalancerLastRegistrationTimeAnnotation = "loadbalancer.ibm.sh/last-registration"
)

// Controller reconciles NodeClaim load balancer registration
type Controller struct {
	client.Client
	vpcClient            *ibm.VPCClient
	loadBalancerProvider LoadBalancerProviderInterface
	logger               logr.Logger
}

// NewController creates a new load balancer controller
func NewController(client client.Client, vpcClient *ibm.VPCClient) *Controller {
	logger := log.Log.WithName("loadbalancer-controller")

	return &Controller{
		Client:               client,
		vpcClient:            vpcClient,
		loadBalancerProvider: loadbalancer.NewLoadBalancerProviderWithIBMClient(vpcClient, logger),
		logger:               logger,
	}
}

// Register sets up the controller with the Manager
func (c *Controller) Register(ctx context.Context, mgr manager.Manager) error {
	return builder.ControllerManagedBy(mgr).
		Named("nodeclaim-loadbalancer").
		For(&karpv1.NodeClaim{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(c.nodeToNodeClaim)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 10,
		}).
		Complete(c)
}

// Reconcile handles NodeClaim load balancer registration and deregistration
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := c.logger.WithValues("nodeclaim", req.NamespacedName)
	ctx = log.IntoContext(ctx, logger)

	// Get the NodeClaim
	var nodeClaim karpv1.NodeClaim
	if err := c.Get(ctx, req.NamespacedName, &nodeClaim); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("getting nodeclaim: %w", err)
	}

	// Get the associated NodeClass
	nodeClass, err := c.getNodeClass(ctx, &nodeClaim)
	if err != nil {
		logger.Error(err, "Failed to get NodeClass")
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if nodeClass == nil || nodeClass.Spec.LoadBalancerIntegration == nil || !nodeClass.Spec.LoadBalancerIntegration.Enabled {
		// Remove finalizer if load balancer integration is disabled
		if controllerutil.ContainsFinalizer(&nodeClaim, LoadBalancerFinalizer) {
			controllerutil.RemoveFinalizer(&nodeClaim, LoadBalancerFinalizer)
			return reconcile.Result{}, c.Update(ctx, &nodeClaim)
		}
		return reconcile.Result{}, nil
	}

	// Handle deletion
	if !nodeClaim.DeletionTimestamp.IsZero() {
		return c.handleDeletion(ctx, &nodeClaim, nodeClass, logger)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&nodeClaim, LoadBalancerFinalizer) {
		controllerutil.AddFinalizer(&nodeClaim, LoadBalancerFinalizer)
		if err := c.Update(ctx, &nodeClaim); err != nil {
			return reconcile.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
		return reconcile.Result{RequeueAfter: 1 * time.Second}, nil
	}

	// Handle registration
	return c.handleRegistration(ctx, &nodeClaim, nodeClass, logger)
}

// handleRegistration manages load balancer registration for the NodeClaim
func (c *Controller) handleRegistration(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.IBMNodeClass, logger logr.Logger) (reconcile.Result, error) {
	// Check if NodeClaim has an associated instance
	if nodeClaim.Status.ProviderID == "" {
		logger.Info("NodeClaim doesn't have provider ID yet, waiting")
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Extract instance ID from provider ID
	instanceID, err := c.extractInstanceID(nodeClaim.Status.ProviderID)
	if err != nil {
		logger.Error(err, "Failed to extract instance ID from provider ID", "providerID", nodeClaim.Status.ProviderID)
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Get the associated Node to extract IP address
	node, err := c.getNode(ctx, nodeClaim)
	if err != nil {
		logger.Error(err, "Failed to get associated Node")
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if node == nil {
		logger.Info("Associated Node not found yet, waiting")
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Get node internal IP
	instanceIP := c.getNodeInternalIP(node)
	if instanceIP == "" {
		logger.Info("Node internal IP not available yet, waiting")
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Check if already registered
	if isRegistered := c.isAlreadyRegistered(nodeClaim); isRegistered {
		logger.Info("NodeClaim already registered with load balancers")
		return reconcile.Result{}, nil
	}

	// Register with load balancers
	logger.Info("Registering NodeClaim with load balancers", "instanceID", instanceID, "instanceIP", instanceIP)

	if err := c.loadBalancerProvider.RegisterInstance(ctx, nodeClass, instanceID, instanceIP); err != nil {
		logger.Error(err, "Failed to register with load balancers")
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Mark as registered
	if err := c.markAsRegistered(ctx, nodeClaim); err != nil {
		logger.Error(err, "Failed to mark NodeClaim as registered")
		return reconcile.Result{RequeueAfter: 10 * time.Second}, nil
	}

	logger.Info("Successfully registered NodeClaim with load balancers")
	return reconcile.Result{}, nil
}

// handleDeletion manages load balancer deregistration during NodeClaim deletion
func (c *Controller) handleDeletion(ctx context.Context, nodeClaim *karpv1.NodeClaim, nodeClass *v1alpha1.IBMNodeClass, logger logr.Logger) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(nodeClaim, LoadBalancerFinalizer) {
		return reconcile.Result{}, nil
	}

	// Extract instance ID from provider ID
	if nodeClaim.Status.ProviderID != "" {
		instanceID, err := c.extractInstanceID(nodeClaim.Status.ProviderID)
		if err != nil {
			logger.Error(err, "Failed to extract instance ID, removing finalizer anyway", "providerID", nodeClaim.Status.ProviderID)
		} else {
			// Deregister from load balancers
			logger.Info("Deregistering NodeClaim from load balancers", "instanceID", instanceID)

			if err := c.loadBalancerProvider.DeregisterInstance(ctx, nodeClass, instanceID); err != nil {
				logger.Error(err, "Failed to deregister from load balancers, continuing with cleanup")
				// Don't return error to avoid blocking deletion
			} else {
				logger.Info("Successfully deregistered NodeClaim from load balancers")
			}
		}
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(nodeClaim, LoadBalancerFinalizer)
	if err := c.Update(ctx, nodeClaim); err != nil {
		return reconcile.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}

	return reconcile.Result{}, nil
}

// Helper methods

func (c *Controller) getNodeClass(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*v1alpha1.IBMNodeClass, error) {
	if nodeClaim.Spec.NodeClassRef == nil {
		return nil, fmt.Errorf("nodeclaim has no nodeclass reference")
	}

	if nodeClaim.Spec.NodeClassRef.Kind != "IBMNodeClass" {
		return nil, nil // Not an IBM NodeClass
	}

	var nodeClass v1alpha1.IBMNodeClass
	key := types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}

	if err := c.Get(ctx, key, &nodeClass); err != nil {
		return nil, fmt.Errorf("getting nodeclass %s: %w", key.Name, err)
	}

	return &nodeClass, nil
}

func (c *Controller) getNode(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*corev1.Node, error) {
	if nodeClaim.Status.NodeName == "" {
		return nil, nil
	}

	var node corev1.Node
	key := types.NamespacedName{Name: nodeClaim.Status.NodeName}

	if err := c.Get(ctx, key, &node); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("getting node %s: %w", key.Name, err)
	}

	return &node, nil
}

func (c *Controller) getNodeInternalIP(node *corev1.Node) string {
	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address
		}
	}
	return ""
}

func (c *Controller) extractInstanceID(providerID string) (string, error) {
	// IBM Cloud provider ID format: ibm:///us-south-1/instance-id
	// Extract the instance ID part
	parts := strings.Split(providerID, "/")
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid provider ID format: %s", providerID)
	}
	return parts[len(parts)-1], nil
}

func (c *Controller) isAlreadyRegistered(nodeClaim *karpv1.NodeClaim) bool {
	if nodeClaim.Annotations == nil {
		return false
	}
	registered, exists := nodeClaim.Annotations[LoadBalancerRegisteredAnnotation]
	return exists && registered == "true"
}

func (c *Controller) markAsRegistered(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	if nodeClaim.Annotations == nil {
		nodeClaim.Annotations = make(map[string]string)
	}

	nodeClaim.Annotations[LoadBalancerRegisteredAnnotation] = "true"
	nodeClaim.Annotations[LoadBalancerLastRegistrationTimeAnnotation] = time.Now().Format(time.RFC3339)

	return c.Update(ctx, nodeClaim)
}

func (c *Controller) nodeToNodeClaim(ctx context.Context, obj client.Object) []reconcile.Request {
	node, ok := obj.(*corev1.Node)
	if !ok {
		return nil
	}

	// Find NodeClaim that owns this Node
	var nodeClaims karpv1.NodeClaimList
	if err := c.List(ctx, &nodeClaims); err != nil {
		c.logger.Error(err, "Failed to list NodeClaims")
		return nil
	}

	for _, nc := range nodeClaims.Items {
		if nc.Status.NodeName == node.Name {
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Name: nc.Name,
				},
			}}
		}
	}

	return nil
}
