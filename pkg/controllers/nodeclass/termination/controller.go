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
package termination

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// Controller reconciles IBMNodeClass deletion by terminating associated nodes
// +kubebuilder:rbac:groups=karpenter.ibm.sh,resources=ibmnodeclasses,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=karpenter.sh,resources=nodeclaims,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
type Controller struct {
	kubeClient client.Client
	recorder   record.EventRecorder
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client, recorder record.EventRecorder) (*Controller, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("kubeClient cannot be nil")
	}
	if recorder == nil {
		return nil, fmt.Errorf("recorder cannot be nil")
	}
	return &Controller{
		kubeClient: kubeClient,
		recorder:   recorder,
	}, nil
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	nc := &v1alpha1.IBMNodeClass{}
	if err := c.kubeClient.Get(ctx, req.NamespacedName, nc); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// If nodeclass is not being deleted, do nothing
	if nc.DeletionTimestamp == nil {
		return reconcile.Result{}, nil
	}

	// List all nodes using this nodeclass
	nodes := &v1.NodeList{}
	if err := c.kubeClient.List(ctx, nodes, client.MatchingLabels{
		"karpenter.ibm.sh/nodeclass": nc.Name,
	}); err != nil {
		return reconcile.Result{}, err
	}

	// Delete all nodes
	for _, node := range nodes.Items {
		if err := c.kubeClient.Delete(ctx, &node); err != nil && !errors.IsNotFound(err) {
			c.recorder.Event(nc, v1.EventTypeWarning, "FailedToDeleteNode",
				fmt.Sprintf("Failed to delete node %s: %v", node.Name, err))
			return reconcile.Result{}, err
		}
		c.recorder.Event(nc, v1.EventTypeNormal, "DeletedNode",
			fmt.Sprintf("Deleted node %s", node.Name))
	}

	return reconcile.Result{}, nil
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("nodeclass.termination").
		For(&v1alpha1.IBMNodeClass{}).
		Complete(c)
}
