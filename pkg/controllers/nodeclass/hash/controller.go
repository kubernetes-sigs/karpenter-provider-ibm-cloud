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
	"encoding/json"
	"fmt"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/nodeclass"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

// Controller computes a hash of the IBMNodeClass spec and stores it in the status
// +kubebuilder:rbac:groups=karpenter-ibm.sh,resources=ibmnodeclasses,verbs=get;list;watch;patch;update
// +kubebuilder:rbac:groups=karpenter-ibm.sh,resources=ibmnodeclasses/status,verbs=get;update;patch
type Controller struct {
	kubeClient client.Client
	apiReader  client.Reader
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client, readers ...client.Reader) (*Controller, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("kubeClient cannot be nil")
	}
	reader := client.Reader(kubeClient)
	if len(readers) != 0 && readers[0] != nil {
		reader = readers[0]
	}
	return &Controller{kubeClient: kubeClient, apiReader: reader}, nil
}

// Reconcile executes a control loop for the resource
func (c *Controller) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	nc := &v1alpha1.IBMNodeClass{}
	if err := c.apiReader.Get(ctx, req.NamespacedName, nc); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Skip reconciliation if the NodeClass is being deleted
	if nc.DeletionTimestamp != nil && !nc.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}
	if _, err := nodeclass.ReadHashMigration(nc); err != nil {
		return reconcile.Result{}, err
	}

	hashString, err := nodeclass.ProvisioningHash(nc)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("computing provisioning hash: %w", err)
	}
	currentHash, currentVersion := nc.Annotations[v1alpha1.AnnotationIBMNodeClassHash], nc.Annotations[v1alpha1.AnnotationIBMNodeClassHashVersion]
	if currentVersion != "" && currentVersion != "1" && currentVersion != v1alpha1.IBMNodeClassHashVersion {
		return reconcile.Result{}, fmt.Errorf("unsupported NodeClass hash version %s; retaining state", currentVersion)
	}
	if currentVersion != v1alpha1.IBMNodeClassHashVersion {
		if err := c.migrateClaims(ctx, nc, hashString); err != nil {
			return reconcile.Result{}, err
		}
	} else if currentHash == hashString && nc.Annotations[nodeclass.HashMigrationAnnotation] == "" {
		return reconcile.Result{}, nil
	}

	stored := nc.DeepCopy()
	if nc.Annotations == nil {
		nc.Annotations = map[string]string{}
	}
	delete(nc.Annotations, nodeclass.HashMigrationAnnotation)
	nc.Annotations[v1alpha1.AnnotationIBMNodeClassHash] = hashString
	nc.Annotations[v1alpha1.AnnotationIBMNodeClassHashVersion] = v1alpha1.IBMNodeClassHashVersion
	if err := c.kubeClient.Patch(ctx, nc, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to patch annotations: %w", err)
	}
	return reconcile.Result{}, nil
}

// migrateClaims restamps claims hashed under version 1 so the version bump alone does not drift
// them. The checkpoint pins both hashes so a restart mid-migration resumes against the same spec.
func (c *Controller) migrateClaims(ctx context.Context, nc *v1alpha1.IBMNodeClass, hashString string) error {
	legacyHash, err := nodeclass.LegacyHash(nc)
	if err != nil {
		return fmt.Errorf("computing legacy hash: %w", err)
	}
	migration, err := nodeclass.ReadHashMigration(nc)
	if err != nil {
		return err
	}
	if migration == nil {
		migration = &nodeclass.HashMigration{Version: ownership.StateFormatVersion, MinimumWriterVersion: ownership.StateFormatVersion, LegacyHash: legacyHash, ProvisioningHash: hashString}
		value, err := json.Marshal(migration)
		if err != nil {
			return err
		}
		stored := nc.DeepCopy()
		if nc.Annotations == nil {
			nc.Annotations = map[string]string{}
		}
		nc.Annotations[nodeclass.HashMigrationAnnotation] = string(value)
		if err := c.kubeClient.Patch(ctx, nc, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
	}
	claims := &karpv1.NodeClaimList{}
	if err := c.apiReader.List(ctx, claims); err != nil {
		return err
	}
	for i := range claims.Items {
		claim := &claims.Items[i]
		if claim.Spec.NodeClassRef == nil || claim.Spec.NodeClassRef.Name != nc.Name || claim.Spec.NodeClassRef.Group != v1alpha1.Group ||
			claim.Annotations[v1alpha1.AnnotationIBMNodeClassHashVersion] != "1" ||
			claim.Annotations[v1alpha1.AnnotationIBMNodeClassHash] != migration.LegacyHash ||
			claim.StatusConditions().Get(karpv1.ConditionTypeDrifted).IsTrue() {
			continue
		}
		storedClaim := claim.DeepCopy()
		claim.Annotations[v1alpha1.AnnotationIBMNodeClassHash] = migration.ProvisioningHash
		claim.Annotations[v1alpha1.AnnotationIBMNodeClassHashVersion] = v1alpha1.IBMNodeClassHashVersion
		if err := c.kubeClient.Patch(ctx, claim, client.MergeFromWithOptions(storedClaim, client.MergeFromWithOptimisticLock{})); err != nil {
			return fmt.Errorf("migrating hash for NodeClaim %s: %w", claim.Name, err)
		}
	}
	return nil
}

// Register registers the controller with the manager
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		Named("nodeclass.hash").
		For(&v1alpha1.IBMNodeClass{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(c)
}
