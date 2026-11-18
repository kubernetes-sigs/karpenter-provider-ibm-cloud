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
	"encoding/json"
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
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers"
	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/loadbalancer"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/instance"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

// LoadBalancerProviderInterface defines the interface for load balancer operations
type LoadBalancerProviderInterface interface {
	VerifyAccount(context.Context, string) error
	ResolveTargets(context.Context, *v1alpha1.IBMNodeClass, string) ([]loadbalancer.ResolvedTarget, error)
	RegisterTargets(context.Context, *loadbalancer.Snapshot) error
	DeregisterTargets(context.Context, *loadbalancer.Snapshot) error
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
	apiReader            client.Reader
	loadBalancerProvider LoadBalancerProviderInterface
	logger               logr.Logger
}

// NewController creates a new load balancer controller
func NewController(kubeClient client.Client, vpcClient *ibm.VPCClient, readers ...client.Reader) *Controller {
	reader := client.Reader(kubeClient)
	if len(readers) > 0 && readers[0] != nil {
		reader = readers[0]
	}
	logger := log.Log.WithName("loadbalancer-controller")

	return &Controller{
		Client:    kubeClient,
		apiReader: reader,
		vpcClient: vpcClient,

		logger: logger,
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
	claim := &karpv1.NodeClaim{}
	if err := c.apiReader.Get(ctx, req.NamespacedName, claim); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if claim.Spec.NodeClassRef == nil || claim.Spec.NodeClassRef.Kind != "IBMNodeClass" || claim.Spec.NodeClassRef.Group != v1alpha1.Group {
		return reconcile.Result{}, nil
	}
	snapshot, err := loadbalancer.DecodeSnapshot(claim.Annotations[loadbalancer.SnapshotAnnotation])
	if err != nil {
		return reconcile.Result{}, err
	}
	if snapshot != nil {
		if validationErr := c.validateSnapshot(ctx, claim, snapshot); validationErr != nil {
			return reconcile.Result{}, validationErr
		}
		if !claim.DeletionTimestamp.IsZero() {
			return c.handleDeletion(ctx, claim, nil, c.logger)
		}
		return c.registerSnapshot(ctx, claim, snapshot)
	}
	if !claim.DeletionTimestamp.IsZero() {
		// Every release registers only a joined Node's internal IP, so a claim that never
		// registered cannot own a pool member.
		if controllerutil.ContainsFinalizer(claim, LoadBalancerFinalizer) && claim.Status.ProviderID != "" && claim.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue() {
			return reconcile.Result{}, fmt.Errorf("legacy load balancer finalizer has no immutable targets; restore verified original target snapshot before retirement")
		}
		return c.removeFinalizer(ctx, claim)
	}
	class, err := c.getNodeClass(ctx, claim)
	if err != nil {
		return reconcile.Result{}, err
	}
	if claim.Status.ProviderID == "" && claim.Annotations[instance.LaunchAnnotation] == "" && class != nil {
		mode, err := providers.ResolveProviderMode(class)
		if err != nil {
			return reconcile.Result{}, err
		}
		if mode == commonTypes.IKSMode {
			return reconcile.Result{}, nil
		}
	}
	if claim.Annotations[ownership.BackendAnnotation] == "iks" || (strings.HasPrefix(claim.Status.ProviderID, "ibm://") && !strings.HasPrefix(claim.Status.ProviderID, "ibm:///")) {
		if controllerutil.ContainsFinalizer(claim, LoadBalancerFinalizer) {
			return reconcile.Result{}, fmt.Errorf("legacy IKS load balancer finalizer requires verified original membership cleanup")
		}
		return reconcile.Result{}, nil
	}
	if class == nil || class.Spec.LoadBalancerIntegration == nil || !class.Spec.LoadBalancerIntegration.Enabled {
		if controllerutil.ContainsFinalizer(claim, LoadBalancerFinalizer) && claim.Annotations[LoadBalancerRegisteredAnnotation] == "true" {
			return reconcile.Result{}, fmt.Errorf("legacy registered load balancer claim requires its original target snapshot")
		}
		return c.removeFinalizer(ctx, claim)
	}
	if !controllerutil.ContainsFinalizer(claim, LoadBalancerFinalizer) {
		before := claim.DeepCopy()
		controllerutil.AddFinalizer(claim, LoadBalancerFinalizer)
		if err := c.Patch(ctx, claim, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{RequeueAfter: time.Second}, nil
	}
	return c.handleRegistration(ctx, claim, class, c.logger)
}

func (c *Controller) provider(region string) (LoadBalancerProviderInterface, error) {
	if c.loadBalancerProvider != nil {
		return c.loadBalancerProvider, nil
	}
	if c.vpcClient == nil {
		return nil, fmt.Errorf("regional load balancer client is unavailable")
	}
	regional, err := c.vpcClient.ForRegion(region)
	if err != nil {
		return nil, err
	}
	return loadbalancer.NewLoadBalancerProviderWithIBMClient(regional, c.logger), nil
}

func (c *Controller) validateSnapshot(ctx context.Context, claim *karpv1.NodeClaim, snapshot *loadbalancer.Snapshot) error {
	instanceID, parseErr := c.extractInstanceID(snapshot.ProviderID)
	if parseErr != nil || instanceID != snapshot.InstanceID {
		return fmt.Errorf("load balancer instance differs from provider identity")
	}
	if snapshot.ClaimUID != string(claim.UID) || snapshot.ProviderID != claim.Status.ProviderID {
		return fmt.Errorf("load balancer snapshot claim or provider identity changed")
	}
	cluster, err := ownership.ClusterUID(ctx, c.apiReader)
	if err != nil {
		return err
	}
	if cluster != snapshot.ClusterUID {
		return fmt.Errorf("load balancer snapshot belongs to another cluster")
	}
	identity, err := instance.ReadLaunchIdentity(claim)
	if err != nil {
		return err
	}
	if identity.AccountID != snapshot.AccountID || identity.ClassUID != snapshot.ClassUID || identity.Region != snapshot.Region || identity.ClusterUID != snapshot.ClusterUID {
		return fmt.Errorf("load balancer snapshot differs from immutable launch")
	}
	provider, err := c.provider(snapshot.Region)
	if err != nil {
		return err
	}
	return provider.VerifyAccount(ctx, snapshot.AccountID)
}

func (c *Controller) handleRegistration(ctx context.Context, claim *karpv1.NodeClaim, class *v1alpha1.IBMNodeClass, _ logr.Logger) (reconcile.Result, error) {
	if claim.Status.ProviderID == "" {
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}
	instanceID, err := c.extractInstanceID(claim.Status.ProviderID)
	if err != nil {
		return reconcile.Result{}, err
	}
	node, err := c.getNode(ctx, claim)
	if err != nil {
		return reconcile.Result{}, err
	}
	if node == nil || c.getNodeInternalIP(node) == "" {
		return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
	}
	if node.Spec.ProviderID != claim.Status.ProviderID {
		return reconcile.Result{}, fmt.Errorf("load balancer Node identity differs from claim")
	}
	identity, err := instance.ReadLaunchIdentity(claim)
	if err != nil {
		return reconcile.Result{}, err
	}
	if identity.ClassUID != string(class.UID) {
		return reconcile.Result{}, fmt.Errorf("load balancer NodeClass was recreated")
	}
	provider, err := c.provider(identity.Region)
	if err != nil {
		return reconcile.Result{}, err
	}
	if verificationErr := provider.VerifyAccount(ctx, identity.AccountID); verificationErr != nil {
		return reconcile.Result{}, verificationErr
	}
	targets, err := provider.ResolveTargets(ctx, class, identity.AccountID)
	if err != nil {
		return reconcile.Result{}, err
	}
	config := class.Spec.LoadBalancerIntegration
	auto := true
	if config.AutoDeregister != nil {
		auto = *config.AutoDeregister
	}
	timeout := int32(300)
	if config.RegistrationTimeout != nil {
		timeout = *config.RegistrationTimeout
	}
	snapshot := &loadbalancer.Snapshot{Version: 1, MinimumWriterVersion: 1, ClaimUID: string(claim.UID), ClassUID: identity.ClassUID, ClusterUID: identity.ClusterUID, AccountID: identity.AccountID, Region: identity.Region, ProviderID: claim.Status.ProviderID, InstanceID: instanceID, AutoDeregister: auto, RegistrationTimeout: timeout, Targets: targets}
	if validationErr := c.validateSnapshot(ctx, claim, snapshot); validationErr != nil {
		return reconcile.Result{}, validationErr
	}
	before := claim.DeepCopy()
	if claim.Annotations == nil {
		claim.Annotations = map[string]string{}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return reconcile.Result{}, err
	}
	claim.Annotations[loadbalancer.SnapshotAnnotation] = string(encoded)
	if err := c.Patch(ctx, claim, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{RequeueAfter: time.Second}, nil
}

func (c *Controller) registerSnapshot(ctx context.Context, claim *karpv1.NodeClaim, snapshot *loadbalancer.Snapshot) (reconcile.Result, error) {
	if c.isAlreadyRegistered(claim) {
		return reconcile.Result{}, nil
	}
	fresh := &karpv1.NodeClaim{}
	if readErr := c.apiReader.Get(ctx, client.ObjectKeyFromObject(claim), fresh); readErr != nil {
		return reconcile.Result{}, readErr
	}
	if fresh.UID != claim.UID || fresh.ResourceVersion != claim.ResourceVersion || !fresh.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, fmt.Errorf("claim changed before load balancer registration")
	}
	provider, err := c.provider(snapshot.Region)
	if err != nil {
		return reconcile.Result{}, err
	}
	if err := provider.RegisterTargets(ctx, snapshot); err != nil {
		return reconcile.Result{}, err
	}
	if err := c.markAsRegistered(ctx, claim); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (c *Controller) handleDeletion(ctx context.Context, claim *karpv1.NodeClaim, _ *v1alpha1.IBMNodeClass, _ logr.Logger) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(claim, LoadBalancerFinalizer) {
		return reconcile.Result{}, nil
	}
	snapshot, err := loadbalancer.DecodeSnapshot(claim.Annotations[loadbalancer.SnapshotAnnotation])
	if err != nil {
		return reconcile.Result{}, err
	}
	if snapshot == nil {
		return reconcile.Result{}, fmt.Errorf("load balancer cleanup requires immutable target snapshot")
	}
	if validationErr := c.validateSnapshot(ctx, claim, snapshot); validationErr != nil {
		return reconcile.Result{}, validationErr
	}
	fresh := &karpv1.NodeClaim{}
	if readErr := c.apiReader.Get(ctx, client.ObjectKeyFromObject(claim), fresh); readErr != nil {
		return reconcile.Result{}, readErr
	}
	if fresh.UID != claim.UID || fresh.ResourceVersion != claim.ResourceVersion || fresh.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, fmt.Errorf("claim changed before load balancer cleanup")
	}
	provider, err := c.provider(snapshot.Region)
	if err != nil {
		return reconcile.Result{}, err
	}
	if err := provider.DeregisterTargets(ctx, snapshot); err != nil {
		return reconcile.Result{}, err
	}
	return c.removeFinalizer(ctx, claim)
}

func (c *Controller) removeFinalizer(ctx context.Context, claim *karpv1.NodeClaim) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(claim, LoadBalancerFinalizer) {
		return reconcile.Result{}, nil
	}
	before := claim.DeepCopy()
	controllerutil.RemoveFinalizer(claim, LoadBalancerFinalizer)
	return reconcile.Result{}, c.Patch(ctx, claim, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
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

	if err := c.apiReader.Get(ctx, key, &nodeClass); err != nil {
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

	if err := c.apiReader.Get(ctx, key, &node); err != nil {
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
	parts := strings.Split(providerID, "/")
	if len(parts) != 5 || parts[0] != "ibm:" || parts[1] != "" || parts[2] != "" || parts[3] == "" || parts[4] == "" {
		return "", fmt.Errorf("invalid VPC provider ID: %s", providerID)
	}
	return parts[4], nil
}

func (c *Controller) isAlreadyRegistered(nodeClaim *karpv1.NodeClaim) bool {
	if nodeClaim.Annotations == nil {
		return false
	}
	registered, exists := nodeClaim.Annotations[LoadBalancerRegisteredAnnotation]
	return exists && registered == "true"
}

func (c *Controller) markAsRegistered(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	stored := nodeClaim.DeepCopy()
	if nodeClaim.Annotations == nil {
		nodeClaim.Annotations = make(map[string]string)
	}

	nodeClaim.Annotations[LoadBalancerRegisteredAnnotation] = "true"
	nodeClaim.Annotations[LoadBalancerLastRegistrationTimeAnnotation] = time.Now().Format(time.RFC3339)

	return c.Patch(ctx, nodeClaim, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{}))
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
