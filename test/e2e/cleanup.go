//go:build e2e
// +build e2e

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
package e2e

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

var legacyE2EName = regexp.MustCompile(`^(e2e-test|drift-stability|instance-selection|nodepool-instance-selection|validation-test|valid-nodeclass|cleanup-nodepool|cleanup-nodeclass|cleanup-orphaned|cleanup-ibmcloud|multizone-distribution|zone-anti-affinity|topology-spread|placement-strategy-validation|zone-failover|block-device-test|image-selector-test|startup-taints|startup-taint-removal|basic-taints|taint-values|taint-sync|unregistered-taint|subnet-drift-placement|sg-drift|sg-drift-default|consolidation-pdb|pdb-test|anti-affinity|node-affinity)-[0-9]{10,}(-[a-z0-9-]+)?$`)

func isE2EOwned(object client.Object) bool {
	if object.GetNamespace() == "karpenter" {
		return false
	}
	labels := object.GetLabels()
	if labels["test"] == "e2e" || labels["created-by"] == "karpenter-e2e" {
		return true
	}
	switch labels["purpose"] {
	case "e2e-verification", "instance-type-test", "nodepool-instancetype-test", "karpenter-test",
		"multi-zone-test", "block-device-test", "image-selector-test", "image-selector-placement-test",
		"placement-strategy-test", "default-sg-test":
		return true
	}
	return legacyE2EName.MatchString(object.GetName())
}

type cleanupScope struct {
	testName string
	poolUIDs map[string]types.UID
}

func (scope cleanupScope) directlyOwns(object client.Object) bool {
	if object.GetNamespace() == "karpenter" {
		return false
	}
	if scope.testName == "" {
		return isE2EOwned(object)
	}
	if name := object.GetLabels()["test-name"]; name != "" {
		return name == scope.testName
	}
	return isE2EOwned(object) && (object.GetName() == scope.testName || strings.HasPrefix(object.GetName(), scope.testName+"-"))
}

func (scope cleanupScope) owns(object client.Object) bool {
	if scope.testName != "" && object.GetLabels()["test-name"] != "" && object.GetLabels()["test-name"] != scope.testName {
		return false
	}
	if scope.directlyOwns(object) {
		return true
	}
	if _, ok := object.(*karpv1.NodeClaim); !ok {
		return false
	}
	for _, owner := range object.GetOwnerReferences() {
		if owner.Kind == "NodePool" && strings.HasPrefix(owner.APIVersion, "karpenter.sh/") && owner.UID != "" && scope.poolUIDs[owner.Name] == owner.UID {
			return true
		}
	}
	return false
}

func (s *E2ETestSuite) newCleanupScope(ctx context.Context, testName string) (cleanupScope, error) {
	scope := cleanupScope{testName: testName, poolUIDs: map[string]types.UID{}}
	pools := &karpv1.NodePoolList{}
	if err := s.kubeClient.List(ctx, pools); err != nil {
		return scope, fmt.Errorf("listing cleanup pools: %w", err)
	}
	for i := range pools.Items {
		pool := &pools.Items[i]
		if scope.directlyOwns(pool) {
			scope.poolUIDs[pool.Name] = pool.UID
		}
	}
	return scope, nil
}

func (s *E2ETestSuite) quiesceCleanupPools(ctx context.Context, scope cleanupScope) error {
	for name, expectedUID := range scope.poolUIDs {
		if expectedUID == "" {
			return fmt.Errorf("cleanup identity changed for NodePool %s", name)
		}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			current := &karpv1.NodePool{}
			if err := s.kubeClient.Get(ctx, client.ObjectKey{Name: name}, current); err != nil {
				return client.IgnoreNotFound(err)
			}
			if current.UID != expectedUID {
				return fmt.Errorf("cleanup identity changed for NodePool %s", name)
			}
			if !scope.directlyOwns(current) {
				return fmt.Errorf("cleanup ownership changed for NodePool %s", name)
			}
			if current.DeletionTimestamp != nil {
				return nil
			}
			if cpu, exists := current.Spec.Limits[corev1.ResourceCPU]; exists && cpu.IsZero() {
				return nil
			}
			stored := current.DeepCopy()
			if current.Spec.Limits == nil {
				current.Spec.Limits = karpv1.Limits{}
			}
			// Pending cluster workloads must not replace claims while teardown waits for their deletion.
			current.Spec.Limits[corev1.ResourceCPU] = resource.MustParse("0")
			return client.IgnoreNotFound(s.kubeClient.Patch(ctx, current, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})))
		}); err != nil {
			return fmt.Errorf("quiescing cleanup NodePool %s: %w", name, err)
		}
	}
	return nil
}

func (s *E2ETestSuite) cleanupObjects(ctx context.Context, list client.ObjectList, scope cleanupScope) ([]client.Object, error) {
	if err := s.kubeClient.List(ctx, list); err != nil {
		return nil, err
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return nil, err
	}
	var objects []client.Object
	for _, item := range items {
		object, ok := item.(client.Object)
		if !ok {
			return nil, fmt.Errorf("unsupported cleanup object %T", item)
		}
		if scope.owns(object) {
			objects = append(objects, object)
		}
	}
	return objects, nil
}

func (s *E2ETestSuite) deleteCleanupObject(ctx context.Context, object client.Object, scope cleanupScope) error {
	expectedUID := object.GetUID()
	if expectedUID == "" {
		return fmt.Errorf("cleanup identity changed for %T %s", object, object.GetName())
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := object.DeepCopyObject().(client.Object)
		if err := s.kubeClient.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
			return client.IgnoreNotFound(err)
		}
		if current.GetUID() != expectedUID {
			return fmt.Errorf("cleanup identity changed for %T %s", object, object.GetName())
		}
		if !scope.owns(current) {
			return fmt.Errorf("cleanup ownership changed for %T %s", object, object.GetName())
		}
		if current.GetDeletionTimestamp() != nil {
			return nil
		}
		uid, version := current.GetUID(), current.GetResourceVersion()
		return client.IgnoreNotFound(s.kubeClient.Delete(ctx, current, client.Preconditions{UID: &uid, ResourceVersion: &version}))
	})
}

func (s *E2ETestSuite) waitForCleanupObjectsGone(ctx context.Context, objects []client.Object) error {
	return wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		remaining := false
		for _, object := range objects {
			current := object.DeepCopyObject().(client.Object)
			if err := s.kubeClient.Get(ctx, client.ObjectKeyFromObject(object), current); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return false, err
			}
			if current.GetUID() != object.GetUID() {
				return false, fmt.Errorf("cleanup identity changed while waiting for %T %s", object, object.GetName())
			}
			remaining = true
		}
		return !remaining, nil
	})
}

func referencesClass(reference *karpv1.NodeClassReference, name string) bool {
	return reference != nil && reference.Group == v1alpha1.Group && reference.Kind == "IBMNodeClass" && reference.Name == name
}

func (s *E2ETestSuite) waitForCleanupDependents(ctx context.Context, object client.Object) error {
	return wait.PollUntilContextCancel(ctx, pollInterval, true, func(ctx context.Context) (bool, error) {
		claims := &karpv1.NodeClaimList{}
		if err := s.kubeClient.List(ctx, claims); err != nil {
			return false, err
		}
		switch object := object.(type) {
		case *karpv1.NodePool:
			for _, claim := range claims.Items {
				if claim.Labels[karpv1.NodePoolLabelKey] == object.Name {
					return false, nil
				}
				for _, owner := range claim.OwnerReferences {
					if owner.Kind == "NodePool" && owner.UID == object.UID && owner.Name == object.Name {
						return false, nil
					}
				}
			}
			nodes := &corev1.NodeList{}
			if err := s.kubeClient.List(ctx, nodes); err != nil {
				return false, err
			}
			for _, node := range nodes.Items {
				if node.Labels[karpv1.NodePoolLabelKey] == object.Name {
					return false, nil
				}
			}
		case *v1alpha1.IBMNodeClass:
			for _, claim := range claims.Items {
				if referencesClass(claim.Spec.NodeClassRef, object.Name) {
					return false, nil
				}
			}
			pools := &karpv1.NodePoolList{}
			if err := s.kubeClient.List(ctx, pools); err != nil {
				return false, err
			}
			for _, pool := range pools.Items {
				if referencesClass(pool.Spec.Template.Spec.NodeClassRef, object.Name) {
					return false, nil
				}
			}
		}
		return true, nil
	})
}

func (s *E2ETestSuite) cleanupSelectedResources(ctx context.Context, testName string) error {
	scope, err := s.newCleanupScope(ctx, testName)
	if err != nil {
		return err
	}
	if quiesceErr := s.quiesceCleanupPools(ctx, scope); quiesceErr != nil {
		return quiesceErr
	}
	claims, err := s.cleanupObjects(ctx, &karpv1.NodeClaimList{}, scope)
	if err != nil {
		return err
	}
	var instanceIDs []string
	for _, object := range claims {
		if id := vpcInstanceID(object.(*karpv1.NodeClaim).Status.ProviderID); id != "" {
			instanceIDs = append(instanceIDs, id)
		}
	}
	resources := []struct {
		name    string
		list    client.ObjectList
		timeout time.Duration
	}{
		{"PodDisruptionBudget", &policyv1.PodDisruptionBudgetList{}, time.Minute},
		{"DaemonSet", &appsv1.DaemonSetList{}, 2 * time.Minute},
		{"Deployment", &appsv1.DeploymentList{}, 2 * time.Minute},
		{"Pod", &corev1.PodList{}, 2 * time.Minute},
		{"NodeClaim", &karpv1.NodeClaimList{}, 5 * time.Minute},
		{"NodePool", &karpv1.NodePoolList{}, 2 * time.Minute},
		{"IBMNodeClass", &v1alpha1.IBMNodeClassList{}, 2 * time.Minute},
	}
	for _, resource := range resources {
		stageCtx, cancel := context.WithTimeout(ctx, resource.timeout)
		err := func() error {
			for {
				objects, err := s.cleanupObjects(stageCtx, resource.list, scope)
				if err != nil {
					return err
				}
				if len(objects) == 0 {
					return nil
				}
				for _, object := range objects {
					switch object.(type) {
					case *karpv1.NodePool, *v1alpha1.IBMNodeClass:
						if err := s.waitForCleanupDependents(stageCtx, object); err != nil {
							return err
						}
					}
					if err := s.deleteCleanupObject(stageCtx, object, scope); err != nil {
						return err
					}
				}
				if err := s.waitForCleanupObjectsGone(stageCtx, objects); err != nil {
					return err
				}
			}
		}()
		cancel()
		if err != nil {
			return fmt.Errorf("cleanup of %s failed; remaining resources and finalizers were preserved: %w", resource.name, err)
		}
	}
	// NodeClaim deletion completes once the provider accepts the delete; the instance itself
	// must also be gone before the test counts as cleaned up.
	if len(instanceIDs) == 0 || s.apiKey == "" || s.testVPC == "" {
		return nil
	}
	cloudCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if remaining, err := pollInstancesGone(cloudCtx, instanceIDs, 10*time.Second, s.getIBMCloudInstancesWithContext, nil); err != nil {
		return fmt.Errorf("IBM Cloud instances %v outlived their NodeClaims: %w", remaining, err)
	}
	return nil
}

// vpcInstanceID returns the instance ID of a VPC provider ID, or "" for other backends.
func vpcInstanceID(providerID string) string {
	parts := strings.Split(strings.TrimPrefix(providerID, "ibm:///"), "/")
	if !strings.HasPrefix(providerID, "ibm:///") || len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func (s *E2ETestSuite) deleteNodeClaim(t *testing.T, nodeClaimName string) {
	t.Helper()
	ctx := context.Background()
	scope, err := s.newCleanupScope(ctx, "")
	require.NoError(t, err)
	claim := &karpv1.NodeClaim{}
	require.NoError(t, s.kubeClient.Get(ctx, client.ObjectKey{Name: nodeClaimName}, claim))
	require.NoError(t, s.deleteCleanupObject(ctx, claim, scope))
}

func (s *E2ETestSuite) cleanupTestWorkload(t *testing.T, deploymentName, namespace string) {
	t.Helper()
	ctx := context.Background()
	deployment := &appsv1.Deployment{}
	if err := s.kubeClient.Get(ctx, client.ObjectKey{Name: deploymentName, Namespace: namespace}, deployment); err != nil {
		if !apierrors.IsNotFound(err) {
			t.Errorf("reading cleanup deployment %s: %v", deploymentName, err)
		}
		return
	}
	if err := s.deleteCleanupObject(ctx, deployment, cleanupScope{}); err != nil {
		t.Errorf("deleting cleanup deployment %s: %v", deploymentName, err)
	}
}

func (s *E2ETestSuite) cleanupTestResources(t *testing.T, testName string) {
	t.Helper()
	if testName == "" {
		t.Error("cleanup requires a nonempty test-name")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.cleanupSelectedResources(ctx, testName); err != nil {
		t.Errorf("cleanup for test %s: %v", testName, err)
	}
}

func (s *E2ETestSuite) cleanupAllStaleResources(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.cleanupSelectedResources(ctx, ""); err != nil {
		t.Errorf("cleanup of owned E2E resources: %v", err)
		return false
	}
	if err := s.waitForStaleResourcesGone(ctx, t); err != nil {
		t.Errorf("verifying E2E cleanup: %v", err)
		return false
	}
	return true
}

func (s *E2ETestSuite) waitForStaleResourcesGone(ctx context.Context, t *testing.T) error {
	t.Helper()
	scope, err := s.newCleanupScope(ctx, "")
	if err != nil {
		return err
	}
	for _, list := range []client.ObjectList{&appsv1.DeploymentList{}, &policyv1.PodDisruptionBudgetList{}, &corev1.PodList{}, &karpv1.NodeClaimList{}, &karpv1.NodePoolList{}, &v1alpha1.IBMNodeClassList{}} {
		objects, err := s.cleanupObjects(ctx, list, scope)
		if err != nil {
			return err
		}
		if err := s.waitForCleanupObjectsGone(ctx, objects); err != nil {
			return err
		}
	}
	return nil
}

func (s *E2ETestSuite) cleanupOrphanedKubernetesResources(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pods := &corev1.PodList{}
	if err := s.kubeClient.List(ctx, pods); err != nil {
		t.Errorf("listing failed E2E pods: %v", err)
		return
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodFailed && isE2EOwned(pod) {
			if err := s.deleteCleanupObject(ctx, pod, cleanupScope{}); err != nil {
				t.Errorf("deleting failed E2E pod %s: %v", pod.Name, err)
			}
		}
	}
}

func (s *E2ETestSuite) WithAutoCleanup(t *testing.T, testName string, testFunc func()) {
	t.Helper()
	defer s.cleanupTestResources(t, testName)
	testFunc()
}
