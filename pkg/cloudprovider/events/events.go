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

package events

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/events"
)

func NodeClaimFailedToResolveNodeClass(nodeClaim *karpv1.NodeClaim) events.Event {
	name := "<unknown>"
	if nodeClaim != nil {
		name = nodeClaim.Name
	}
	return events.Event{
		InvolvedObject: nodeClaim,
		Type:           corev1.EventTypeWarning,
		Reason:         "FailedToResolveNodeClass",
		Message:        fmt.Sprintf("Failed to resolve NodeClass for NodeClaim %s", name),
	}
}

func NodePoolFailedToResolveNodeClass(nodePool *karpv1.NodePool) events.Event {
	name := "<unknown>"
	if nodePool != nil {
		name = nodePool.Name
	}
	return events.Event{
		InvolvedObject: nodePool,
		Type:           corev1.EventTypeWarning,
		Reason:         "FailedToResolveNodeClass",
		Message:        fmt.Sprintf("Failed to resolve NodeClass for NodePool %s", name),
	}
}

func NodeClaimCircuitBreakerBlocked(nodeClaim *karpv1.NodeClaim, reason string) events.Event {
	name := "<unknown>"
	if nodeClaim != nil {
		name = nodeClaim.Name
	}
	return events.Event{
		InvolvedObject: nodeClaim,
		Type:           corev1.EventTypeWarning,
		Reason:         "CircuitBreakerBlocked",
		Message:        fmt.Sprintf("Circuit breaker blocked provisioning for NodeClaim %s: %s", name, reason),
	}
}

func NodeClaimFailedValidation(nodeClaim *karpv1.NodeClaim, reason string) events.Event {
	name := "<unknown>"
	if nodeClaim != nil {
		name = nodeClaim.Name
	}
	return events.Event{
		InvolvedObject: nodeClaim,
		Type:           corev1.EventTypeWarning,
		Reason:         "FailedValidation",
		Message:        fmt.Sprintf("NodeClaim %s failed validation: %s", name, reason),
	}
}
