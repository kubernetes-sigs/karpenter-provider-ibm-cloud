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
package hash

import (
	"context"
	"fmt"

	"github.com/mitchellh/hashstructure/v2"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// Controller computes a hash of the IBMNodeClass spec and stores it in the status
// +kubebuilder:rbac:groups=karpenter.ibm.sh,resources=ibmnodeclasses,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=karpenter.ibm.sh,resources=ibmnodeclasses/status,verbs=get;update;patch
type Controller struct {
	kubeClient client.Client
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client) (*Controller, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("kubeClient cannot be nil")
	}
	return &Controller{
		kubeClient: kubeClient,
	}, nil
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	nc := &v1alpha1.IBMNodeClass{}
	if err := c.kubeClient.Get(ctx, req.NamespacedName, nc); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Skip reconciliation if the NodeClass is being deleted
	if nc.DeletionTimestamp != nil && !nc.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, fmt.Errorf("cannot reconcile IBMNodeClass being deleted")
	}

	// Compute hash of the spec
	hash, err := hashstructure.Hash(nc.Spec, hashstructure.FormatV2, nil)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to compute hash: %w", err)
	}

	// Convert hash to string
	hashString := fmt.Sprint(hash)

	// Store hash in annotations (following AWS pattern)
	stored := nc.DeepCopy()
	if nc.Annotations == nil {
		nc.Annotations = make(map[string]string)
	}

	// Check if hash has changed
	currentHash, exists := nc.Annotations[v1alpha1.AnnotationIBMNodeClassHash]
	currentVersion, versionExists := nc.Annotations[v1alpha1.AnnotationIBMNodeClassHashVersion]
	if !exists || currentHash != hashString || !versionExists || currentVersion != v1alpha1.IBMNodeClassHashVersion {
		nc.Annotations[v1alpha1.AnnotationIBMNodeClassHash] = hashString
		nc.Annotations[v1alpha1.AnnotationIBMNodeClassHashVersion] = v1alpha1.IBMNodeClassHashVersion
		if err := c.kubeClient.Patch(ctx, nc, client.MergeFrom(stored)); err != nil {
			return reconcile.Result{}, fmt.Errorf("failed to patch annotations: %w", err)
		}
	}

	return reconcile.Result{}, nil
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("nodeclass.hash").
		For(&v1alpha1.IBMNodeClass{}).
		WithEventFilter(predicate.NewPredicateFuncs(func(object client.Object) bool {
			return true // Only reconcile on spec changes
		})).
		Complete(c)
}
