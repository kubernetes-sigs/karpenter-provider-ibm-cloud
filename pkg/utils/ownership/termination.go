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

package ownership

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	podutils "sigs.k8s.io/karpenter/pkg/utils/pod"
)

func EstablishTermination(ctx context.Context, kube client.Client, reader client.Reader, node *corev1.Node, claim *karpv1.NodeClaim, retireFinalizers ...string) (*corev1.Node, error) {
	if claim == nil || claim.UID == "" || claim.Spec.NodeClassRef == nil {
		return nil, fmt.Errorf("drain ownership requires a persisted claim and NodeClass reference")
	}
	freshClaim := &karpv1.NodeClaim{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(claim), freshClaim); err != nil {
		return nil, err
	}
	if freshClaim.UID != claim.UID || freshClaim.Status.ProviderID != claim.Status.ProviderID {
		return nil, fmt.Errorf("claim identity changed before drain handoff")
	}
	current := &corev1.Node{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
		return nil, err
	}
	if current.UID != node.UID || current.Spec.ProviderID != node.Spec.ProviderID {
		return nil, fmt.Errorf("node identity changed before drain handoff")
	}
	for _, owner := range current.OwnerReferences {
		if owner.Kind == "NodeClaim" && owner.UID != claim.UID {
			return nil, fmt.Errorf("node belongs to another claim")
		}
	}
	if !current.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(current, karpv1.TerminationFinalizer) {
		return nil, fmt.Errorf("deleting Node cannot acquire a new termination finalizer")
	}
	stored := current.DeepCopy()
	controllerutil.AddFinalizer(current, karpv1.TerminationFinalizer)
	for _, finalizer := range retireFinalizers {
		controllerutil.RemoveFinalizer(current, finalizer)
	}
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	classLabel := karpv1.NodeClassLabelKey(freshClaim.Spec.NodeClassRef.GroupKind())
	if value := current.Labels[classLabel]; value != "" && value != freshClaim.Spec.NodeClassRef.Name {
		return nil, fmt.Errorf("node class identity differs before drain handoff")
	}
	if value := current.Labels[karpv1.NodePoolLabelKey]; value != "" && value != freshClaim.Labels[karpv1.NodePoolLabelKey] {
		return nil, fmt.Errorf("node pool identity differs before drain handoff")
	}
	current.Labels[classLabel] = freshClaim.Spec.NodeClassRef.Name
	current.Labels[karpv1.NodePoolLabelKey] = freshClaim.Labels[karpv1.NodePoolLabelKey]
	if !slices.ContainsFunc(current.OwnerReferences, func(owner metav1.OwnerReference) bool { return owner.UID == claim.UID && owner.Kind == "NodeClaim" }) {
		current.OwnerReferences = append(current.OwnerReferences, metav1.OwnerReference{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: claim.Name, UID: claim.UID, BlockOwnerDeletion: ptr.To(true)})
	}
	if err := kube.Patch(ctx, current, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
		return nil, err
	}
	return current, nil
}

func DrainLegacyNode(ctx context.Context, kube client.Client, reader client.Reader, node *corev1.Node) (bool, error) {
	current := &corev1.Node{}
	if err := reader.Get(ctx, client.ObjectKeyFromObject(node), current); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if current.UID != node.UID || current.Spec.ProviderID != node.Spec.ProviderID || current.DeletionTimestamp.IsZero() {
		return false, fmt.Errorf("legacy Node identity or deletion state changed")
	}
	stored := current.DeepCopy()
	current.Spec.Unschedulable = true
	if !slices.ContainsFunc(current.Spec.Taints, func(taint corev1.Taint) bool { return taint.MatchTaint(&karpv1.DisruptedNoScheduleTaint) }) {
		current.Spec.Taints = append(current.Spec.Taints, karpv1.DisruptedNoScheduleTaint)
	}
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	current.Labels[corev1.LabelNodeExcludeBalancers] = "karpenter"
	if err := kube.Patch(ctx, current, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); err != nil {
		return false, err
	}
	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods, client.MatchingFields{"spec.nodeName": node.Name}); err != nil {
		return false, err
	}
	waiting := false
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !podutils.IsWaitingEviction(pod, clock.RealClock{}) {
			continue
		}
		waiting = true
		if !pod.DeletionTimestamp.IsZero() || pod.Annotations[karpv1.DoNotDisruptAnnotationKey] == "true" {
			continue
		}
		eviction := &policyv1.Eviction{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace}, DeleteOptions: &metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}}}
		if err := kube.SubResource("eviction").Create(ctx, pod, eviction); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsTooManyRequests(err) {
			return false, err
		}
	}
	if waiting || time.Since(current.DeletionTimestamp.Time) < 5*time.Second {
		return false, nil
	}
	attachments := &storagev1.VolumeAttachmentList{}
	if err := reader.List(ctx, attachments); err != nil {
		return false, err
	}
	for _, attachment := range attachments.Items {
		if attachment.Spec.NodeName == node.Name {
			return false, nil
		}
	}
	return true, nil
}
