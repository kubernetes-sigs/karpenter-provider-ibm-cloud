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

package workerpool

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/httpclient"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/nodeclass"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

const (
	AllocationFinalizer  = "karpenter-ibm.sh/iks-allocation"
	AllocationAnnotation = "karpenter-ibm.sh/iks-allocation"
	reservationDataKey   = "allocation"
	ReservationPhaseKey  = "phase"

	ReservationCreationPendingKey = "creationPending"
	ReservationCreationStartedKey = "creationStarted"
	ReservationCleanupKey         = "cleanup"
	ReservationClusterIDKey       = "clusterID"
	ReservationPoolIDKey          = "poolID"
	ReservationAccountIDKey       = "accountID"
	ReservationRegionKey          = "region"

	// poolCreationWindow bounds how long an uncertain pool creation is retained. IKS lists a
	// pool by name as soon as createWorkerPool accepts it, so absence past this window means
	// the request created nothing.
	poolCreationWindow = 15 * time.Minute
)

var poolLocks [64]sync.Mutex
var accountPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Option func(*IKSWorkerPoolProvider)

func WithAPIReader(reader client.Reader) Option {
	return func(p *IKSWorkerPoolProvider) { p.apiReader = reader }
}

func WithIKSClient(iksClient ibm.IKSClientInterface) Option {
	return func(p *IKSWorkerPoolProvider) { p.iksClient = iksClient }
}

func WithInstanceTypeProvider(provider instancetype.Provider) Option {
	return func(p *IKSWorkerPoolProvider) { p.instanceTypeProvider = provider }
}

func WithAllocationNamespace(namespace string) Option {
	return func(p *IKSWorkerPoolProvider) { p.allocationNamespace = namespace }
}

func (p *IKSWorkerPoolProvider) SetAPIReader(reader client.Reader) {
	p.apiReader = reader
}

type Allocation struct {
	Version              int                         `json:"version,omitempty"`
	MinimumWriterVersion int                         `json:"minimumWriterVersion,omitempty"`
	ClaimName            string                      `json:"claimName"`
	ClaimUID             string                      `json:"claimUID"`
	NodeClassUID         string                      `json:"nodeClassUID"`
	ClusterUID           string                      `json:"clusterUID"`
	ClusterID            string                      `json:"clusterID"`
	AccountID            string                      `json:"accountID"`
	Region               string                      `json:"region"`
	Namespace            string                      `json:"namespace"`
	PoolName             string                      `json:"poolName"`
	PoolID               string                      `json:"poolID,omitempty"`
	WorkerID             string                      `json:"workerID,omitempty"`
	NodePool             string                      `json:"nodePool,omitempty"`
	Hash                 string                      `json:"hash"`
	Labels               map[string]string           `json:"labels,omitempty"`
	Capacity             corev1.ResourceList         `json:"capacity,omitempty"`
	Allocatable          corev1.ResourceList         `json:"allocatable,omitempty"`
	Request              ibm.WorkerPoolCreateRequest `json:"request"`
}

func DecodeAllocation(annotations map[string]string) (*Allocation, error) {
	data := annotations[AllocationAnnotation]
	if data == "" {
		return nil, nil
	}
	allocation := &Allocation{}
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	if operationErr := decoder.Decode(allocation); operationErr != nil {
		return nil, fmt.Errorf("decoding IKS allocation: %w", operationErr)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing IKS allocation")
	}
	if err := ownership.ValidateStateVersion(allocation.Version, allocation.MinimumWriterVersion); err != nil {
		return nil, err
	}
	if allocation.ClaimUID == "" || allocation.ClusterUID == "" || allocation.NodeClassUID == "" || allocation.ClusterID == "" || allocation.AccountID == "" || allocation.PoolName == "" || allocation.Namespace == "" {
		return nil, fmt.Errorf("IKS allocation is missing immutable identity")
	}
	if !accountPattern.MatchString(allocation.AccountID) || allocation.Region == "" || allocation.Request.Name != allocation.PoolName || allocation.Request.Flavor == "" || allocation.Request.SizePerZone != 1 || len(allocation.Request.Zones) != 1 || allocation.Request.Zones[0].ID == "" {
		return nil, fmt.Errorf("IKS allocation has an invalid provisioning snapshot")
	}
	return allocation, nil
}

func lockPool(clusterID, poolName string) func() {
	sum := sha256.Sum256([]byte(clusterID + "/" + poolName))
	lock := &poolLocks[int(sum[0])%len(poolLocks)]
	lock.Lock()
	return lock.Unlock
}

func ReservationKey(clusterID, poolName, namespace string) types.NamespacedName {
	sum := sha256.Sum256([]byte(clusterID + "/" + poolName))
	return types.NamespacedName{Namespace: namespace, Name: fmt.Sprintf("iks-pool-%x", sum[:20])}
}

func (p *IKSWorkerPoolProvider) reader() client.Reader {
	if p.apiReader != nil {
		return p.apiReader
	}
	return p.kubeClient
}

func (p *IKSWorkerPoolProvider) getIKSClient() (ibm.IKSClientInterface, error) {
	if p.iksClient != nil {
		return p.iksClient, nil
	}
	if p.client == nil {
		return nil, fmt.Errorf("IBM client is not initialized")
	}
	return p.client.GetIKSClient()
}

func (p *IKSWorkerPoolProvider) allocationNamespaceName() string {
	if p.allocationNamespace != "" {
		return p.allocationNamespace
	}
	if namespace := os.Getenv("POD_NAMESPACE"); namespace != "" {
		return namespace
	}
	return "karpenter"
}

func (p *IKSWorkerPoolProvider) createAllocation(ctx context.Context, nodeClaim *v1.NodeClaim) (*corev1.Node, error) {
	if p.kubeClient == nil {
		return nil, fmt.Errorf("kubernetes client not set")
	}
	if nodeClaim == nil || nodeClaim.Spec.NodeClassRef == nil {
		return nil, fmt.Errorf("NodeClaim must reference a NodeClass")
	}
	fresh := &v1.NodeClaim{}
	if operationErr := p.reader().Get(ctx, types.NamespacedName{Name: nodeClaim.Name}, fresh); operationErr == nil {
		if fresh.UID != nodeClaim.UID {
			return nil, fmt.Errorf("NodeClaim identity changed")
		}
		nodeClaim = fresh
	} else if !apierrors.IsNotFound(operationErr) {
		return nil, fmt.Errorf("reading NodeClaim: %w", operationErr)
	}
	allocation, err := DecodeAllocation(nodeClaim.Annotations)
	if err != nil {
		return nil, err
	}
	if allocation == nil {
		allocation, err = p.newAllocation(ctx, nodeClaim)
		if err != nil {
			return nil, err
		}
	}
	if !nodeClaim.DeletionTimestamp.IsZero() {
		return nil, fmt.Errorf("NodeClaim is terminating")
	}
	clusterUID, clusterErr := ownership.ClusterUID(ctx, p.reader())
	if clusterErr != nil {
		return nil, clusterErr
	}
	if clusterUID != allocation.ClusterUID {
		return nil, fmt.Errorf("IKS allocation belongs to another cluster")
	}
	unlock := lockPool(allocation.ClusterID, allocation.PoolName)
	defer unlock()
	if allocation.ClaimUID != string(nodeClaim.UID) {
		return nil, fmt.Errorf("allocation belongs to another NodeClaim")
	}
	if operationErr := p.checkpoint(ctx, nodeClaim, allocation); operationErr != nil {
		return nil, operationErr
	}
	reservation, err := p.reserve(ctx, allocation)
	if err != nil {
		return nil, err
	}
	if restoreErr := restoreReservation(allocation, reservation); restoreErr != nil {
		return nil, restoreErr
	}
	if reservation.Data[ReservationPhaseKey] == "deleting" {
		return nil, fmt.Errorf("IKS allocation is terminating")
	}
	iksClient, err := p.getIKSClient()
	if err != nil {
		return nil, fmt.Errorf("getting IKS client: %w", err)
	}
	if targetErr := p.validateTarget(iksClient, allocation.AccountID, allocation.Region); targetErr != nil {
		return nil, targetErr
	}
	pool, err := iksClient.GetWorkerPool(ctx, allocation.ClusterID, allocation.PoolName)
	if err != nil && !IsNotFound(err) {
		return nil, fmt.Errorf("looking up reserved worker pool: %w", err)
	}
	if IsNotFound(err) {
		if creationUncertain(reservation, time.Now()) {
			return nil, pending("worker pool creation outcome remains uncertain; waiting for authoritative visibility")
		}
		if allocation.PoolID != "" {
			return nil, fmt.Errorf("allocated worker pool %s disappeared", allocation.PoolID)
		}
		if operationErr := p.updateReservation(ctx, reservation, allocation, "creating"); operationErr != nil {
			return nil, operationErr
		}
		pool, err = iksClient.CreateWorkerPool(ctx, allocation.ClusterID, &allocation.Request)
		if err != nil {
			if rejectedCreate(err) {
				if operationErr := p.updateReservation(ctx, reservation, allocation, "rejected"); operationErr != nil {
					return nil, fmt.Errorf("recording rejected worker pool %s after %w: %v", allocation.PoolName, err, operationErr)
				}
			}
			return nil, fmt.Errorf("creating reserved worker pool %s: %w", allocation.PoolName, err)
		}
		if pool == nil || pool.ID == "" {
			return nil, pending("waiting for the reserved worker pool identity")
		}
		allocation.PoolID = pool.ID
		if operationErr := p.updateReservation(ctx, reservation, allocation, "active"); operationErr != nil {
			return nil, operationErr
		}
		if operationErr := p.checkpoint(ctx, nodeClaim, allocation); operationErr != nil {
			return nil, operationErr
		}
		pool, err = iksClient.GetWorkerPool(ctx, allocation.ClusterID, allocation.PoolID)
		if err != nil {
			return nil, fmt.Errorf("verifying created worker pool: %w", err)
		}
	}
	if operationErr := validatePool(pool, allocation); operationErr != nil {
		return nil, operationErr
	}
	if pool.SizePerZone != 1 {
		return nil, fmt.Errorf("reserved IKS pool no longer targets one worker")
	}
	allocation.PoolID = pool.ID
	workers, err := iksClient.ListWorkers(ctx, allocation.ClusterID)
	if err != nil {
		return nil, fmt.Errorf("listing allocated workers: %w", err)
	}
	var selected *ibm.IKSWorkerDetails
	for _, worker := range workers {
		if worker == nil || worker.PoolID != pool.ID || worker.Lifecycle.ActualState == "deleted" {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("isolated pool %s contains more than one worker", pool.ID)
		}
		if worker.ID == "" || worker.Location != allocation.Request.Zones[0].ID || worker.Flavor != allocation.Request.Flavor {
			return nil, fmt.Errorf("worker does not match the reserved zone and flavor")
		}
		primarySubnet := ""
		for _, networkInterface := range worker.NetworkInterfaces {
			if networkInterface.Primary {
				primarySubnet = networkInterface.SubnetID
				break
			}
		}
		if primarySubnet == "" {
			return nil, pending("waiting for the worker network identity")
		}
		if primarySubnet != allocation.Request.Zones[0].SubnetID {
			return nil, fmt.Errorf("worker subnet differs from the immutable allocation; retaining ownership")
		}
		selected = worker
	}
	if selected == nil {
		return nil, pending("waiting for the reserved worker to be provisioned")
	}
	if allocation.WorkerID != "" && allocation.WorkerID != selected.ID {
		return nil, fmt.Errorf("allocated worker identity changed")
	}
	allocation.WorkerID = selected.ID
	if operationErr := p.updateReservation(ctx, reservation, allocation, "active"); operationErr != nil {
		return nil, operationErr
	}
	if operationErr := p.checkpoint(ctx, nodeClaim, allocation); operationErr != nil {
		return nil, operationErr
	}
	result := nodeForAllocation(allocation, selected)
	registered, err := p.registeredNode(ctx, result.Spec.ProviderID)
	if err != nil {
		return nil, err
	}
	if registered == nil || registered.Status.Capacity.Cpu().IsZero() || registered.Status.Capacity.Memory().IsZero() || registered.Status.Allocatable.Cpu().IsZero() || registered.Status.Allocatable.Memory().IsZero() {
		// The worker exists, so the launch is complete. Core liveness allows 5 minutes to launch
		// but 15 to register, and IKS workers routinely take longer than 5 to join.
		if len(allocation.Capacity) == 0 || len(allocation.Allocatable) == 0 {
			return nil, pending("waiting for the allocated worker to register its resources")
		}
		result.Status.Capacity = allocation.Capacity.DeepCopy()
		result.Status.Allocatable = allocation.Allocatable.DeepCopy()
		return result, nil
	}
	result.Name = registered.Name
	result.Status = *registered.Status.DeepCopy()
	if !resources.Fits(nodeClaim.Spec.Resources.Requests, result.Status.Allocatable) {
		return nil, fmt.Errorf("registered IKS worker resources do not satisfy the NodeClaim requests")
	}
	for key, value := range allocation.Labels {
		if actual, exists := registered.Labels[key]; exists && actual != value {
			return nil, fmt.Errorf("registered IKS worker label %s differs from the provisioning snapshot", key)
		}
	}
	return result, nil
}

func pending(message string) error {
	return cloudprovider.NewCreateError(fmt.Errorf("%s", message), "WorkerProvisioning", message)
}

func (p *IKSWorkerPoolProvider) validateTarget(iksClient ibm.IKSClientInterface, accountID, region string) error {
	configuredAccount, configuredRegion := iksClient.GetAccountID(), iksClient.GetRegion()
	if configuredAccount != accountID || (region != "" && configuredRegion != region) {
		return fmt.Errorf("IKS client account or region differs from the immutable allocation; retaining ownership")
	}
	return nil
}

func (p *IKSWorkerPoolProvider) validateInstanceType(ctx context.Context, claim *v1.NodeClaim, nodeClass *v1alpha1.IBMNodeClass, flavor string) (map[string]string, error) {
	if p.instanceTypeProvider == nil {
		return nil, fmt.Errorf("IKS provisioning requires an instance type catalog")
	}
	instanceType, err := p.instanceTypeProvider.Get(ctx, flavor, nodeClass)
	if err != nil {
		return nil, fmt.Errorf("getting IKS flavor capacity: %w", err)
	}
	if instanceType == nil || instanceType.Name != flavor {
		return nil, fmt.Errorf("IKS flavor is missing from the instance type catalog")
	}
	if nodeClass.Spec.InstanceRequirements != nil {
		filtered, filterErr := p.instanceTypeProvider.FilterInstanceTypes(ctx, nodeClass.Spec.InstanceRequirements, nodeClass)
		if filterErr != nil {
			return nil, filterErr
		}
		found := false
		for _, candidate := range filtered {
			if candidate != nil && candidate.Name == flavor {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("IKS flavor %s does not meet NodeClass instance requirements", flavor)
		}
	}
	labels := map[string]string{}
	for key, requirement := range instanceType.Requirements {
		if requirement.Operator() == corev1.NodeSelectorOpIn && requirement.Len() == 1 {
			labels[key] = requirement.Values()[0]
		}
	}
	labels[corev1.LabelInstanceTypeStable] = flavor
	labels[corev1.LabelTopologyZone] = nodeClass.Spec.Zone
	labels[corev1.LabelTopologyRegion] = nodeClass.Spec.Region
	labels[v1.CapacityTypeLabelKey] = v1.CapacityTypeOnDemand
	for _, extra := range []map[string]string{claim.Labels, nodeClass.Spec.IKSDynamicPools.Labels} {
		for key, value := range extra {
			if actual, known := labels[key]; known && actual != value {
				return nil, fmt.Errorf("IKS label %s conflicts with the selected flavor or zone", key)
			}
			if !ownership.ReservedTag(key) {
				labels[key] = value
			}
		}
	}
	requirements := scheduling.NewNodeSelectorRequirementsWithMinValues(claim.Spec.Requirements...)
	if compatibilityErr := scheduling.NewLabelRequirements(labels).Compatible(requirements, scheduling.AllowUndefinedWellKnownLabels); compatibilityErr != nil {
		return nil, fmt.Errorf("IKS flavor or zone does not satisfy NodeClaim requirements: %w", compatibilityErr)
	}
	maximumPrice := 0.0
	priceLimited := nodeClass.Spec.InstanceRequirements != nil && nodeClass.Spec.InstanceRequirements.MaximumHourlyPrice != ""
	if priceLimited {
		maximumPrice, err = strconv.ParseFloat(nodeClass.Spec.InstanceRequirements.MaximumHourlyPrice, 64)
		if err != nil || maximumPrice < 0 || math.IsNaN(maximumPrice) || math.IsInf(maximumPrice, 0) {
			return nil, fmt.Errorf("IKS maximum hourly price is invalid")
		}
	}
	available, unknownPrice := false, false
	for _, offering := range instanceType.Offerings {
		if offering != nil && offering.Available && offering.Requirements.IsCompatible(scheduling.NewLabelRequirements(map[string]string{corev1.LabelTopologyZone: nodeClass.Spec.Zone, v1.CapacityTypeLabelKey: v1.CapacityTypeOnDemand}), scheduling.AllowUndefinedWellKnownLabels) {
			if priceLimited {
				if !(offering.Price > 0) || math.IsInf(offering.Price, 0) {
					unknownPrice = true
					continue
				}
				if offering.Price > maximumPrice {
					continue
				}
			}
			available = true
			break
		}
	}
	if !available && unknownPrice {
		return nil, fmt.Errorf("IKS offering price is unavailable; retry when pricing resolves")
	}
	if !available || !resources.Fits(claim.Spec.Resources.Requests, instanceType.Allocatable()) {
		return nil, fmt.Errorf("IKS flavor has no compatible capacity for the NodeClaim")
	}
	return labels, nil
}

func (p *IKSWorkerPoolProvider) newAllocation(ctx context.Context, claim *v1.NodeClaim) (*Allocation, error) {
	nodeClass := &v1alpha1.IBMNodeClass{}
	if operationErr := p.reader().Get(ctx, types.NamespacedName{Name: claim.Spec.NodeClassRef.Name}, nodeClass); operationErr != nil {
		return nil, fmt.Errorf("getting NodeClass %s: %w", claim.Spec.NodeClassRef.Name, operationErr)
	}
	clusterID := nodeClass.Spec.IKSClusterID
	if clusterID == "" {
		clusterID = os.Getenv("IKS_CLUSTER_ID")
	}
	if clusterID == "" {
		return nil, fmt.Errorf("IKS cluster ID not found in NodeClass or environment")
	}
	iksClient, err := p.getIKSClient()
	if err != nil {
		return nil, err
	}
	if !p.isDynamicPoolsEnabled(nodeClass) {
		return nil, fmt.Errorf("IKS provisioning requires iksDynamicPools.enabled: shared worker pools cannot provide exact NodeClaim deletion")
	}
	if claim.UID == "" || nodeClass.UID == "" {
		return nil, fmt.Errorf("NodeClaim and NodeClass require immutable UIDs")
	}
	ready := nodeClass.StatusConditions().Get("Ready")
	if !ready.IsTrue() || ready.ObservedGeneration != nodeClass.Generation {
		return nil, fmt.Errorf("NodeClass readiness is stale or unavailable")
	}
	clusterUID, err := ownership.ClusterUID(ctx, p.reader())
	if err != nil {
		return nil, err
	}
	accountID := os.Getenv("IBM_ACCOUNT_ID")
	if !accountPattern.MatchString(accountID) {
		return nil, fmt.Errorf("IBM_ACCOUNT_ID must be a 32-character hexadecimal account identity")
	}
	if p.client != nil && p.client.GetRegion() != "" && p.client.GetRegion() != nodeClass.Spec.Region {
		return nil, fmt.Errorf("IKS client region does not match NodeClass region")
	}
	flavor := nodeClass.Spec.InstanceProfile
	if flavor == "" {
		flavor = claim.Labels[corev1.LabelInstanceTypeStable]
	}
	if nodeClass.Spec.IKSWorkerPoolID != "" {
		template, templateErr := iksClient.GetWorkerPool(ctx, clusterID, nodeClass.Spec.IKSWorkerPoolID)
		if templateErr != nil {
			return nil, fmt.Errorf("getting worker pool template: %w", templateErr)
		}
		if template == nil {
			return nil, fmt.Errorf("worker pool template is missing")
		}
		if flavor != "" && template.Flavor != flavor {
			return nil, fmt.Errorf("worker pool template flavor conflicts with the requested instance profile")
		}
		flavor = template.Flavor
	}
	if flavor == "" || nodeClass.Spec.Zone == "" || nodeClass.Spec.VPC == "" || nodeClass.Spec.Subnet == "" {
		return nil, fmt.Errorf("isolated IKS pools require an instance profile, zone, VPC and subnet")
	}
	if !isInstanceTypeAllowed(flavor, nodeClass.Spec.IKSDynamicPools.AllowedInstanceTypes) {
		return nil, fmt.Errorf("instance type %s is not in allowed list", flavor)
	}
	resolvedLabels, err := p.validateInstanceType(ctx, claim, nodeClass, flavor)
	if err != nil {
		return nil, err
	}
	instanceType, err := p.instanceTypeProvider.Get(ctx, flavor, nodeClass)
	if err != nil {
		return nil, fmt.Errorf("getting IKS flavor capacity: %w", err)
	}
	labels := map[string]string{}
	for key, value := range nodeClass.Spec.IKSDynamicPools.Labels {
		if !ownership.ReservedTag(key) && !v1.WellKnownLabels.Has(key) {
			labels[key] = value
		}
	}
	labels[v1.NodePoolLabelKey] = claim.Labels[v1.NodePoolLabelKey]
	labels[ownership.ManagedLabel] = "true"
	labels[ownership.ProviderLabel] = "iks"
	labels[ownership.ClusterUIDLabel] = clusterUID
	labels[ownership.ClaimUIDLabel] = string(claim.UID)
	labels[ownership.NodeClassUIDLabel] = string(nodeClass.UID)
	hash, err := nodeclass.ProvisioningHash(nodeClass)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(clusterUID + "/" + string(claim.UID)))
	prefix := nodeClass.Spec.IKSDynamicPools.NamePrefix
	if prefix == "" {
		prefix = "karp"
	}
	if len(prefix) > 20 || !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(prefix) {
		return nil, fmt.Errorf("invalid IKS pool name prefix")
	}
	name := fmt.Sprintf("%s-%x", prefix, sum[:20])
	encrypted := true
	if nodeClass.Spec.IKSDynamicPools.DiskEncryption != nil {
		encrypted = *nodeClass.Spec.IKSDynamicPools.DiskEncryption
	}
	return &Allocation{
		Version: ownership.StateFormatVersion, MinimumWriterVersion: ownership.StateFormatVersion,
		ClaimName: claim.Name, ClaimUID: string(claim.UID), NodeClassUID: string(nodeClass.UID),
		ClusterUID: clusterUID, ClusterID: clusterID, AccountID: accountID, Region: nodeClass.Spec.Region,
		Namespace: p.allocationNamespaceName(), PoolName: name, NodePool: claim.Labels[v1.NodePoolLabelKey], Hash: hash, Labels: resolvedLabels,
		Capacity: instanceType.Capacity.DeepCopy(), Allocatable: instanceType.Allocatable().DeepCopy(),
		Request: ibm.WorkerPoolCreateRequest{Name: name, Flavor: flavor, SizePerZone: 1,
			Zones:  []ibm.WorkerPoolZone{{ID: nodeClass.Spec.Zone, SubnetID: nodeClass.Spec.Subnet}},
			Labels: labels, DiskEncryption: encrypted, VpcID: nodeClass.Spec.VPC},
	}, nil
}

func (p *IKSWorkerPoolProvider) checkpoint(ctx context.Context, claim *v1.NodeClaim, allocation *Allocation) error {
	if err := ownership.ValidateStateVersion(allocation.Version, allocation.MinimumWriterVersion); err != nil {
		return err
	}
	allocation.Version, allocation.MinimumWriterVersion = ownership.StateFormatVersion, ownership.StateFormatVersion
	fresh := &v1.NodeClaim{}
	if operationErr := p.reader().Get(ctx, types.NamespacedName{Name: allocation.ClaimName}, fresh); operationErr != nil {
		return fmt.Errorf("reading allocation owner: %w", operationErr)
	}
	if string(fresh.UID) != allocation.ClaimUID || !fresh.DeletionTimestamp.IsZero() {
		return fmt.Errorf("allocation owner is gone or terminating")
	}
	stored := fresh.DeepCopy()
	data, err := json.Marshal(allocation)
	if err != nil {
		return err
	}
	if fresh.Annotations == nil {
		fresh.Annotations = map[string]string{}
	}
	fresh.Annotations[AllocationAnnotation] = string(data)
	fresh.Annotations[ownership.BackendAnnotation] = "iks"
	fresh.Annotations[ownership.RegionAnnotation] = allocation.Region
	fresh.Annotations[ownership.ClusterIDAnnotation] = allocation.ClusterID
	fresh.Annotations[ownership.AccountIDAnnotation] = allocation.AccountID
	fresh.Annotations[ownership.PoolIDAnnotation] = allocation.PoolID
	fresh.Annotations[ownership.WorkerIDAnnotation] = allocation.WorkerID
	controllerutil.AddFinalizer(fresh, AllocationFinalizer)
	if operationErr := p.kubeClient.Patch(ctx, fresh, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); operationErr != nil {
		return fmt.Errorf("checkpointing allocation: %w", operationErr)
	}
	claim.ObjectMeta = *fresh.ObjectMeta.DeepCopy()
	return nil
}

func (p *IKSWorkerPoolProvider) reserve(ctx context.Context, allocation *Allocation) (*corev1.ConfigMap, error) {
	if err := ownership.ValidateStateVersion(allocation.Version, allocation.MinimumWriterVersion); err != nil {
		return nil, err
	}
	allocation.Version, allocation.MinimumWriterVersion = ownership.StateFormatVersion, ownership.StateFormatVersion
	key := ReservationKey(allocation.ClusterID, allocation.PoolName, allocation.Namespace)
	reservation := &corev1.ConfigMap{}
	err := p.reader().Get(ctx, key, reservation)
	if apierrors.IsNotFound(err) {
		data, marshalErr := json.Marshal(allocation)
		if marshalErr != nil {
			return nil, marshalErr
		}
		reservation = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace,
			Labels: map[string]string{ownership.ManagedLabel: "true", ownership.ProviderLabel: "iks", ownership.ClusterUIDLabel: allocation.ClusterUID,
				ownership.ClaimUIDLabel: allocation.ClaimUID, ownership.NodeClassUIDLabel: allocation.NodeClassUID}},
			Data: map[string]string{reservationDataKey: string(data), ReservationPhaseKey: "reserved"}}
		if operationErr := p.kubeClient.Create(ctx, reservation); operationErr != nil {
			return nil, fmt.Errorf("reserving worker pool: %w", operationErr)
		}
	} else if err != nil {
		return nil, fmt.Errorf("reading worker pool reservation: %w", err)
	}
	if reservation.Labels[ownership.ClaimUIDLabel] != allocation.ClaimUID || reservation.Labels[ownership.ClusterUIDLabel] != allocation.ClusterUID || reservation.Labels[ownership.NodeClassUIDLabel] != allocation.NodeClassUID {
		return nil, fmt.Errorf("worker pool reservation belongs to another owner")
	}
	return reservation, nil
}

func (p *IKSWorkerPoolProvider) updateReservation(ctx context.Context, reservation *corev1.ConfigMap, allocation *Allocation, phase string) error {
	if err := ownership.ValidateStateVersion(allocation.Version, allocation.MinimumWriterVersion); err != nil {
		return err
	}
	allocation.Version, allocation.MinimumWriterVersion = ownership.StateFormatVersion, ownership.StateFormatVersion
	stored := reservation.DeepCopy()
	data, err := json.Marshal(allocation)
	if err != nil {
		return err
	}
	reservation.Data[reservationDataKey] = string(data)
	if phase == "creating" {
		reservation.Data[ReservationCreationPendingKey] = "true"
		reservation.Data[ReservationCreationStartedKey] = time.Now().UTC().Format(time.RFC3339)
	}
	if phase == "active" || phase == "rejected" || allocation.PoolID != "" {
		reservation.Data[ReservationCreationPendingKey] = "false"
	}
	reservation.Data[ReservationPhaseKey] = phase
	if operationErr := p.kubeClient.Patch(ctx, reservation, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); operationErr != nil {
		return fmt.Errorf("updating worker pool reservation: %w", operationErr)
	}
	return nil
}

// creationUncertain reports whether an absent pool may still appear from an accepted create.
func creationUncertain(reservation *corev1.ConfigMap, now time.Time) bool {
	if reservation.Data[ReservationCreationPendingKey] != "true" {
		return false
	}
	started, err := time.Parse(time.RFC3339, reservation.Data[ReservationCreationStartedKey])
	return err != nil || now.Sub(started) <= poolCreationWindow
}

// rejectedCreate reports whether IKS definitively refused the request, so no pool can exist.
func rejectedCreate(err error) bool {
	var httpError *httpclient.IBMCloudError
	if !errors.As(err, &httpError) {
		return false
	}
	code := httpError.StatusCode
	return code >= 400 && code < 500 && code != http.StatusRequestTimeout && code != http.StatusConflict && code != http.StatusTooManyRequests
}

func restoreReservation(allocation *Allocation, reservation *corev1.ConfigMap) error {
	recorded, err := DecodeAllocation(map[string]string{AllocationAnnotation: reservation.Data[reservationDataKey]})
	if err != nil {
		return err
	}
	if recorded == nil {
		return fmt.Errorf("worker pool reservation has no allocation snapshot")
	}
	currentSnapshot, recordedSnapshot := *allocation, *recorded
	currentSnapshot.Version, currentSnapshot.MinimumWriterVersion = 0, 0
	recordedSnapshot.Version, recordedSnapshot.MinimumWriterVersion = 0, 0
	currentSnapshot.PoolID, currentSnapshot.WorkerID = "", ""
	recordedSnapshot.PoolID, recordedSnapshot.WorkerID = "", ""
	currentData, err := json.Marshal(currentSnapshot)
	if err != nil {
		return err
	}
	recordedData, err := json.Marshal(recordedSnapshot)
	if err != nil {
		return err
	}
	if string(currentData) != string(recordedData) {
		return fmt.Errorf("worker pool reservation snapshot changed")
	}
	if allocation.PoolID != "" && recorded.PoolID != "" && allocation.PoolID != recorded.PoolID {
		return fmt.Errorf("worker pool reservation identity changed")
	}
	if allocation.WorkerID != "" && recorded.WorkerID != "" && allocation.WorkerID != recorded.WorkerID {
		return fmt.Errorf("worker reservation identity changed")
	}
	if allocation.PoolID == "" {
		allocation.PoolID = recorded.PoolID
	}
	if allocation.WorkerID == "" {
		allocation.WorkerID = recorded.WorkerID
	}
	return nil
}

func validatePool(pool *ibm.WorkerPool, allocation *Allocation) error {
	if pool == nil || pool.ID == "" || pool.Name != allocation.PoolName || pool.AutoscaleEnabled || pool.SizePerZone > 1 || len(pool.Zones) != 1 || pool.Zones[0].ID != allocation.Request.Zones[0].ID || pool.Flavor != allocation.Request.Flavor {
		return fmt.Errorf("worker pool does not match the isolated allocation")
	}
	for key, value := range map[string]string{ownership.ManagedLabel: "true", ownership.ProviderLabel: "iks", ownership.ClusterUIDLabel: allocation.ClusterUID,
		ownership.ClaimUIDLabel: allocation.ClaimUID, ownership.NodeClassUIDLabel: allocation.NodeClassUID} {
		if pool.Labels[key] != value {
			return fmt.Errorf("worker pool ownership mismatch for %s", key)
		}
	}
	if allocation.PoolID != "" && pool.ID != allocation.PoolID {
		return fmt.Errorf("worker pool identity changed")
	}
	return nil
}

func nodeForAllocation(allocation *Allocation, worker *ibm.IKSWorkerDetails) *corev1.Node {
	data, _ := json.Marshal(allocation)
	node := workerNode(allocation.AccountID, allocation.ClusterID, allocation.Region, worker)
	node.Name = allocation.ClaimName
	for key, value := range allocation.Labels {
		node.Labels[key] = value
	}
	node.Labels[v1.NodePoolLabelKey] = allocation.NodePool
	node.Annotations = map[string]string{
		AllocationAnnotation: string(data), ownership.BackendAnnotation: "iks", ownership.RegionAnnotation: allocation.Region,
		ownership.ClusterIDAnnotation: allocation.ClusterID, ownership.PoolIDAnnotation: allocation.PoolID,
		ownership.WorkerIDAnnotation: allocation.WorkerID, ownership.AccountIDAnnotation: allocation.AccountID,
		v1alpha1.AnnotationIBMNodeClassHash: allocation.Hash, v1alpha1.AnnotationIBMNodeClassHashVersion: v1alpha1.IBMNodeClassHashVersion,
	}
	return node
}

func ParseProviderID(providerID string) (string, string, string, error) {
	if !strings.HasPrefix(providerID, "ibm://") {
		return "", "", "", fmt.Errorf("invalid IKS provider ID")
	}
	parts := strings.Split(strings.TrimPrefix(providerID, "ibm://"), "/")
	if len(parts) != 5 || !accountPattern.MatchString(parts[0]) || parts[1] != "" || parts[2] != "" || parts[3] == "" || parts[4] == "" {
		return "", "", "", fmt.Errorf("invalid IKS provider ID")
	}
	return parts[0], parts[3], parts[4], nil
}

func workerNode(accountID, clusterID, region string, worker *ibm.IKSWorkerDetails) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: worker.ID, Labels: map[string]string{
		corev1.LabelInstanceTypeStable: worker.Flavor, corev1.LabelTopologyZone: worker.Location,
		corev1.LabelTopologyRegion: region, v1.CapacityTypeLabelKey: v1.CapacityTypeOnDemand,
		ownership.ClusterIDAnnotation: clusterID, ownership.PoolIDAnnotation: worker.PoolID,
		ownership.WorkerIDAnnotation: worker.ID,
	}}, Spec: corev1.NodeSpec{ProviderID: fmt.Sprintf("ibm://%s///%s/%s", accountID, clusterID, worker.ID)}}
}

func IsNotFound(err error) bool {
	var httpError *httpclient.IBMCloudError
	if errors.As(err, &httpError) {
		// ibm.IsNotFound falls back to message matching, which would read a 5xx mentioning "not found" as absence.
		return httpError.StatusCode == http.StatusNotFound
	}
	return ibm.IsNotFound(err)
}

// inventory is a single fresh read of the claims and nodes that worker lookups resolve against.
type inventory struct {
	claims []v1.NodeClaim
	nodes  []corev1.Node
}

func (p *IKSWorkerPoolProvider) readInventory(ctx context.Context) (*inventory, error) {
	if p.reader() == nil {
		return nil, nil
	}
	claims := &v1.NodeClaimList{}
	if err := p.reader().List(ctx, claims); err != nil {
		return nil, err
	}
	nodes := &corev1.NodeList{}
	if err := p.reader().List(ctx, nodes); err != nil {
		return nil, fmt.Errorf("listing registered workers: %w", err)
	}
	return &inventory{claims: claims.Items, nodes: nodes.Items}, nil
}

func (p *IKSWorkerPoolProvider) getWorker(ctx context.Context, providerID string) (*corev1.Node, error) {
	current, err := p.readInventory(ctx)
	if err != nil {
		return nil, err
	}
	return p.getWorkerFrom(ctx, providerID, current)
}

func (p *IKSWorkerPoolProvider) getWorkerFrom(ctx context.Context, providerID string, current *inventory) (*corev1.Node, error) {
	accountID, clusterID, workerID, err := ParseProviderID(providerID)
	if err != nil {
		return nil, err
	}
	iksClient, err := p.getIKSClient()
	if err != nil {
		return nil, err
	}
	birthRegion := ""
	var birthAllocation *Allocation
	if current != nil {
		for _, claim := range current.claims {
			// A malformed checkpoint on another claim must not block lookups; if it is this
			// worker's, birthRegion stays empty and absence is not confirmed.
			allocation, decodeErr := DecodeAllocation(claim.Annotations)
			if decodeErr != nil {
				log.FromContext(ctx).Error(decodeErr, "Skipped NodeClaim with an unreadable IKS allocation", "nodeClaim", claim.Name)
				continue
			}
			if allocation != nil && allocation.AccountID == accountID && allocation.ClusterID == clusterID && allocation.WorkerID == workerID {
				birthRegion = allocation.Region
				birthAllocation = allocation
				break
			}
		}
	}
	if targetErr := p.validateTarget(iksClient, accountID, birthRegion); targetErr != nil {
		return nil, targetErr
	}
	worker, err := iksClient.GetWorkerDetails(ctx, clusterID, workerID)
	if IsNotFound(err) {
		if birthRegion == "" {
			return nil, fmt.Errorf("worker absence cannot be confirmed without immutable region identity: %w", err)
		}
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("worker %s not found: %w", workerID, err))
	}
	if err != nil {
		return nil, fmt.Errorf("getting worker %s: %w", workerID, err)
	}
	if worker == nil || worker.ID != workerID {
		return nil, fmt.Errorf("worker response has unexpected identity")
	}
	if worker.Lifecycle.ActualState == "deleted" {
		if birthRegion == "" {
			return nil, fmt.Errorf("worker deletion cannot be confirmed without immutable region identity")
		}
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("worker %s is deleted", workerID))
	}
	node := workerNode(accountID, clusterID, ibm.ExtractRegionFromZone(worker.Location), worker)
	if birthAllocation != nil {
		node = nodeForAllocation(birthAllocation, worker)
	}
	if current != nil {
		registered, err := registeredIn(current.nodes, providerID)
		if err != nil {
			return nil, err
		}
		if registered != nil {
			node.Name = registered.Name
			node.Status = *registered.Status.DeepCopy()
		}
	}
	return node, nil
}

func (p *IKSWorkerPoolProvider) registeredNode(ctx context.Context, providerID string) (*corev1.Node, error) {
	nodes := &corev1.NodeList{}
	if operationErr := p.reader().List(ctx, nodes); operationErr != nil {
		return nil, fmt.Errorf("listing registered workers: %w", operationErr)
	}
	return registeredIn(nodes.Items, providerID)
}

func registeredIn(nodes []corev1.Node, providerID string) (*corev1.Node, error) {
	var registered *corev1.Node
	for i := range nodes {
		if nodes[i].Spec.ProviderID != providerID {
			continue
		}
		if registered != nil {
			return nil, fmt.Errorf("multiple Nodes have the allocated provider ID")
		}
		registered = nodes[i].DeepCopy()
	}
	return registered, nil
}

func (p *IKSWorkerPoolProvider) listAllocations(ctx context.Context) ([]*corev1.Node, error) {
	if p.reader() == nil {
		return nil, fmt.Errorf("kubernetes client not set")
	}
	return p.listIndexedAllocations(ctx)
}

func (p *IKSWorkerPoolProvider) deleteAllocation(ctx context.Context, node *corev1.Node) error {
	if node == nil {
		return fmt.Errorf("node is required")
	}
	allocation, err := DecodeAllocation(node.Annotations)
	if err != nil {
		return err
	}
	if allocation == nil {
		if node.UID == "" {
			return fmt.Errorf("cluster ID or pool ID not found in durable IKS allocation; refusing shared-pool deletion")
		}
		return p.retireLegacy(ctx, &v1.NodeClaim{ObjectMeta: *node.ObjectMeta.DeepCopy(), Status: v1.NodeClaimStatus{ProviderID: node.Spec.ProviderID}}, false)
	}
	if allocation.ClaimUID != string(node.UID) {
		return fmt.Errorf("allocation owner identity mismatch")
	}
	if allocation.WorkerID == "" || node.Spec.ProviderID != fmt.Sprintf("ibm://%s///%s/%s", allocation.AccountID, allocation.ClusterID, allocation.WorkerID) {
		return fmt.Errorf("IKS deletion provider identity differs from the immutable allocation")
	}
	return p.cleanup(ctx, allocation)
}

func (p *IKSWorkerPoolProvider) Cleanup(ctx context.Context, claim *v1.NodeClaim) error {
	if claim.Status.ProviderID != "" {
		return p.ConfirmGone(ctx, claim)
	}
	allocation, err := DecodeAllocation(claim.Annotations)
	if err != nil {
		return err
	}
	if allocation == nil {
		return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("no IKS allocation"))
	}
	if allocation.ClaimUID != string(claim.UID) {
		return fmt.Errorf("allocation owner identity mismatch")
	}
	nodes := &corev1.NodeList{}
	if operationErr := p.reader().List(ctx, nodes); operationErr != nil {
		return operationErr
	}
	found := false
	for i := range nodes.Items {
		node := &nodes.Items[i]
		owned := node.Labels[ownership.ClaimUIDLabel] == allocation.ClaimUID && node.Labels[ownership.ClusterUIDLabel] == allocation.ClusterUID
		if allocation.WorkerID != "" {
			owned = owned || node.Spec.ProviderID == fmt.Sprintf("ibm://%s///%s/%s", allocation.AccountID, allocation.ClusterID, allocation.WorkerID)
		}
		if !owned {
			continue
		}
		found = true
		current, handoffErr := ownership.EstablishTermination(ctx, p.kubeClient, p.reader(), node, claim)
		if handoffErr != nil {
			return handoffErr
		}
		if !node.DeletionTimestamp.IsZero() {
			continue
		}
		if operationErr := p.kubeClient.Delete(ctx, current, client.Preconditions{UID: &current.UID, ResourceVersion: &current.ResourceVersion}); client.IgnoreNotFound(operationErr) != nil {
			return operationErr
		}
	}
	if found {
		return nil
	}
	return p.cleanup(ctx, allocation)
}

func (p *IKSWorkerPoolProvider) ConfirmGone(ctx context.Context, claim *v1.NodeClaim) error {
	allocation, err := DecodeAllocation(claim.Annotations)
	if err != nil {
		return err
	}
	if allocation == nil {
		return p.retireLegacy(ctx, claim, true)
	}
	if allocation.ClaimUID != string(claim.UID) {
		return fmt.Errorf("IKS allocation identity is missing or changed")
	}
	if claim.Status.ProviderID != fmt.Sprintf("ibm://%s///%s/%s", allocation.AccountID, allocation.ClusterID, allocation.WorkerID) {
		return fmt.Errorf("bound IKS provider identity differs from the immutable allocation")
	}
	unlock := lockPool(allocation.ClusterID, allocation.PoolName)
	defer unlock()
	clusterUID, err := ownership.ClusterUID(ctx, p.reader())
	if err != nil {
		return err
	}
	if clusterUID != allocation.ClusterUID {
		return fmt.Errorf("allocation belongs to another cluster")
	}
	iksClient, err := p.getIKSClient()
	if err != nil {
		return err
	}
	if targetErr := p.validateTarget(iksClient, allocation.AccountID, allocation.Region); targetErr != nil {
		return targetErr
	}
	_, err = iksClient.GetWorkerPool(ctx, allocation.ClusterID, allocation.PoolName)
	if !IsNotFound(err) {
		if err != nil {
			return err
		}
		return nil
	}
	if allocation.WorkerID == "" {
		return fmt.Errorf("bound IKS allocation has no worker identity")
	}
	worker, err := iksClient.GetWorkerDetails(ctx, allocation.ClusterID, allocation.WorkerID)
	if err == nil && worker != nil && worker.Lifecycle.ActualState == "deleted" {
		err = &httpclient.IBMCloudError{StatusCode: 404}
	}
	if !IsNotFound(err) {
		if err != nil {
			return err
		}
		return nil
	}
	reservation := &corev1.ConfigMap{}
	err = p.reader().Get(ctx, ReservationKey(allocation.ClusterID, allocation.PoolName, allocation.Namespace), reservation)
	if apierrors.IsNotFound(err) {
		return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("allocated pool and worker are absent"))
	}
	if err != nil {
		return err
	}
	if reservation.Labels[ownership.ClaimUIDLabel] != allocation.ClaimUID || reservation.Labels[ownership.ClusterUIDLabel] != allocation.ClusterUID || reservation.Labels[ownership.NodeClassUIDLabel] != allocation.NodeClassUID {
		return fmt.Errorf("reservation ownership changed")
	}
	if err := restoreReservation(allocation, reservation); err != nil {
		return err
	}
	if operationErr := p.kubeClient.Delete(ctx, reservation, client.Preconditions{UID: &reservation.UID, ResourceVersion: &reservation.ResourceVersion}); client.IgnoreNotFound(operationErr) != nil {
		return operationErr
	}
	return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("allocated pool and worker are absent"))
}

func (p *IKSWorkerPoolProvider) cleanup(ctx context.Context, allocation *Allocation) error {
	if p.kubeClient == nil {
		return fmt.Errorf("kubernetes client not set")
	}
	if err := p.verifyAllocationClaim(ctx, allocation); err != nil {
		return err
	}
	unlock := lockPool(allocation.ClusterID, allocation.PoolName)
	defer unlock()
	clusterUID, err := ownership.ClusterUID(ctx, p.reader())
	if err != nil {
		return err
	}
	if clusterUID != allocation.ClusterUID {
		return fmt.Errorf("allocation belongs to another Kubernetes cluster")
	}
	reservation, err := p.reserve(ctx, allocation)
	if err != nil {
		return err
	}
	if restoreErr := restoreReservation(allocation, reservation); restoreErr != nil {
		return restoreErr
	}
	if operationErr := p.updateReservation(ctx, reservation, allocation, "deleting"); operationErr != nil {
		return operationErr
	}
	iksClient, err := p.getIKSClient()
	if err != nil {
		return err
	}
	if targetErr := p.validateTarget(iksClient, allocation.AccountID, allocation.Region); targetErr != nil {
		return targetErr
	}
	pool, err := iksClient.GetWorkerPool(ctx, allocation.ClusterID, allocation.PoolName)
	if IsNotFound(err) {
		if creationUncertain(reservation, time.Now()) && allocation.PoolID == "" {
			return fmt.Errorf("pool creation outcome remains uncertain; retaining allocation reservation")
		}
		if allocation.WorkerID != "" {
			worker, workerErr := iksClient.GetWorkerDetails(ctx, allocation.ClusterID, allocation.WorkerID)
			if workerErr == nil && worker != nil && worker.Lifecycle.ActualState == "deleted" {
				workerErr = &httpclient.IBMCloudError{StatusCode: 404}
			}
			if !IsNotFound(workerErr) {
				if workerErr != nil {
					return workerErr
				}
				return fmt.Errorf("allocated worker still exists after pool removal")
			}
		}
		key := ReservationKey(allocation.ClusterID, allocation.PoolName, allocation.Namespace)
		if operationErr := p.kubeClient.Delete(ctx, reservation, client.Preconditions{UID: &reservation.UID, ResourceVersion: &reservation.ResourceVersion}); client.IgnoreNotFound(operationErr) != nil {
			return fmt.Errorf("releasing worker pool reservation %s: %w", key.Name, operationErr)
		}
		return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("allocated worker pool %s is absent", allocation.PoolName))
	}
	if err != nil {
		return fmt.Errorf("verifying allocated worker pool: %w", err)
	}
	if operationErr := validatePool(pool, allocation); operationErr != nil {
		return operationErr
	}
	allocation.PoolID = pool.ID
	if operationErr := p.updateReservation(ctx, reservation, allocation, "deleting"); operationErr != nil {
		return operationErr
	}
	workers, err := iksClient.ListWorkers(ctx, allocation.ClusterID)
	if err != nil {
		return err
	}
	var count int
	for _, worker := range workers {
		if worker == nil || worker.PoolID != pool.ID || worker.Lifecycle.ActualState == "deleted" {
			continue
		}
		count++
		if allocation.WorkerID != "" && worker.ID != allocation.WorkerID {
			return fmt.Errorf("isolated pool contains a worker outside this allocation")
		}
	}
	if count > 1 {
		return fmt.Errorf("isolated pool contains multiple workers")
	}
	if err := p.verifyAllocationClaim(ctx, allocation); err != nil {
		return err
	}
	nodes := &corev1.NodeList{}
	if err := p.reader().List(ctx, nodes); err != nil {
		return err
	}
	for _, node := range nodes.Items {
		if (node.Labels[ownership.ClaimUIDLabel] == allocation.ClaimUID && node.Labels[ownership.ClusterUIDLabel] == allocation.ClusterUID) || (allocation.WorkerID != "" && node.Spec.ProviderID == fmt.Sprintf("ibm://%s///%s/%s", allocation.AccountID, allocation.ClusterID, allocation.WorkerID)) {
			return fmt.Errorf("allocated Node must finish graceful termination before cloud deletion")
		}
	}
	if operationErr := iksClient.DeleteWorkerPool(ctx, allocation.ClusterID, pool.ID); operationErr != nil && !IsNotFound(operationErr) {
		return fmt.Errorf("deleting allocated worker pool: %w", operationErr)
	}
	return nil
}

func (p *IKSWorkerPoolProvider) verifyAllocationClaim(ctx context.Context, allocation *Allocation) error {
	claim := &v1.NodeClaim{}
	if err := p.reader().Get(ctx, client.ObjectKey{Name: allocation.ClaimName}, claim); err != nil {
		return err
	}
	if string(claim.UID) != allocation.ClaimUID {
		return fmt.Errorf("allocation claim UID changed")
	}
	recorded, err := DecodeAllocation(claim.Annotations)
	if err != nil {
		return err
	}
	if recorded == nil {
		return fmt.Errorf("allocation checkpoint disappeared")
	}
	currentSnapshot, recordedSnapshot := *allocation, *recorded
	currentSnapshot.Version, currentSnapshot.MinimumWriterVersion = 0, 0
	recordedSnapshot.Version, recordedSnapshot.MinimumWriterVersion = 0, 0
	currentSnapshot.PoolID, currentSnapshot.WorkerID = "", ""
	recordedSnapshot.PoolID, recordedSnapshot.WorkerID = "", ""
	currentJSON, err := json.Marshal(currentSnapshot)
	if err != nil {
		return err
	}
	recordedJSON, err := json.Marshal(recordedSnapshot)
	if err != nil {
		return err
	}
	if string(currentJSON) != string(recordedJSON) || (recorded.PoolID != "" && allocation.PoolID != "" && recorded.PoolID != allocation.PoolID) || (recorded.WorkerID != "" && allocation.WorkerID != "" && recorded.WorkerID != allocation.WorkerID) {
		return fmt.Errorf("allocation launch identity changed")
	}
	if claim.Status.ProviderID != "" && claim.Status.ProviderID != fmt.Sprintf("ibm://%s///%s/%s", allocation.AccountID, allocation.ClusterID, allocation.WorkerID) {
		return fmt.Errorf("allocation provider identity changed")
	}
	return nil
}

func TryDeleteEmptyPool(ctx context.Context, kubeClient client.Client, reader client.Reader, iksClient ibm.IKSClientInterface, clusterID, clusterUID, classUID, namespace string, candidate *ibm.WorkerPool, accountID, region string) (bool, error) {
	if kubeClient == nil || reader == nil || candidate == nil || candidate.ID == "" || candidate.Name == "" {
		return false, fmt.Errorf("pool cleanup requires a fresh reader and pool identity")
	}
	if !accountPattern.MatchString(accountID) || region == "" {
		return false, fmt.Errorf("pool cleanup requires immutable account and region identity")
	}
	if iksClient == nil || iksClient.GetAccountID() != accountID || iksClient.GetRegion() != region {
		return false, fmt.Errorf("pool cleanup client differs from checkpoint account or region")
	}
	unlock := lockPool(clusterID, candidate.Name)
	defer unlock()
	key := ReservationKey(clusterID, candidate.Name, namespace)
	reservation := &corev1.ConfigMap{}
	err := reader.Get(ctx, key, reservation)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	if err == nil {
		checkpoint, decodeErr := DecodePoolCleanupCheckpoint(reservation)
		if decodeErr != nil {
			return false, decodeErr
		}
		if checkpoint == nil {
			return false, nil
		}
		if checkpoint.ClusterUID != clusterUID || checkpoint.ClassUID != classUID {
			return false, fmt.Errorf("pool cleanup reservation ownership changed")
		}
		if checkpoint.AccountID != accountID || checkpoint.Region != region || checkpoint.ClusterID != clusterID || checkpoint.PoolID != candidate.ID {
			return false, fmt.Errorf("pool cleanup target identity changed")
		}
	}
	claims := &v1.NodeClaimList{}
	if operationErr := reader.List(ctx, claims); operationErr != nil {
		return false, operationErr
	}
	for _, claim := range claims.Items {
		allocation, decodeErr := DecodeAllocation(claim.Annotations)
		if decodeErr != nil {
			return false, decodeErr
		}
		if allocation != nil && allocation.ClusterID == clusterID && allocation.PoolName == candidate.Name {
			return false, nil
		}
		if claim.Annotations[ownership.ClusterIDAnnotation] == clusterID && claim.Annotations[ownership.PoolIDAnnotation] == candidate.ID {
			return false, nil
		}
	}
	fresh, err := iksClient.GetWorkerPool(ctx, clusterID, candidate.ID)
	if IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fresh == nil || fresh.ID != candidate.ID || fresh.Name != candidate.Name || fresh.SizePerZone != 0 || fresh.AutoscaleEnabled ||
		fresh.Labels[ownership.ManagedLabel] != "true" || fresh.Labels[ownership.ProviderLabel] != "iks" ||
		fresh.Labels[ownership.ClusterUIDLabel] != clusterUID || fresh.Labels[ownership.NodeClassUIDLabel] != classUID || fresh.Labels[ownership.ClaimUIDLabel] != "" {
		return false, nil
	}
	workers, err := iksClient.ListWorkers(ctx, clusterID)
	if err != nil {
		return false, err
	}
	for _, worker := range workers {
		if worker != nil && worker.PoolID == candidate.ID && worker.Lifecycle.ActualState != "deleted" {
			return false, nil
		}
	}
	if reservation.Name == "" {
		reservation = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace,
			Labels: map[string]string{ownership.ManagedLabel: "true", ownership.ProviderLabel: "iks", ownership.ClusterUIDLabel: clusterUID, ownership.NodeClassUIDLabel: classUID}},
			Data: map[string]string{ReservationVersionKey: "1", ReservationMinimumWriterVersionKey: "1", ReservationPhaseKey: "deleting", ReservationCleanupKey: "true", ReservationClusterIDKey: clusterID, ReservationPoolIDKey: candidate.ID, ReservationAccountIDKey: accountID, ReservationRegionKey: region}}
		if operationErr := kubeClient.Create(ctx, reservation); operationErr != nil {
			return false, operationErr
		}
	} else {
		stored := reservation.DeepCopy()
		reservation.Data[ReservationPhaseKey] = "deleting"
		reservation.Data[ReservationVersionKey] = "1"
		reservation.Data[ReservationMinimumWriterVersionKey] = "1"
		if operationErr := kubeClient.Patch(ctx, reservation, client.MergeFromWithOptions(stored, client.MergeFromWithOptimisticLock{})); operationErr != nil {
			return false, operationErr
		}
	}
	fresh, err = iksClient.GetWorkerPool(ctx, clusterID, candidate.ID)
	if err != nil {
		return false, err
	}
	if fresh == nil || fresh.SizePerZone != 0 || fresh.ActualSize != 0 || fresh.AutoscaleEnabled || fresh.ID != candidate.ID || fresh.Name != candidate.Name || fresh.Labels[ownership.ManagedLabel] != "true" || fresh.Labels[ownership.ProviderLabel] != "iks" || fresh.Labels[ownership.ClusterUIDLabel] != clusterUID || fresh.Labels[ownership.NodeClassUIDLabel] != classUID || fresh.Labels[ownership.ClaimUIDLabel] != "" {
		return false, nil
	}
	workers, err = iksClient.ListWorkers(ctx, clusterID)
	if err != nil {
		return false, err
	}
	for _, worker := range workers {
		if worker != nil && worker.PoolID == candidate.ID && worker.Lifecycle.ActualState != "deleted" {
			return false, nil
		}
	}
	current := &corev1.ConfigMap{}
	if readErr := reader.Get(ctx, key, current); readErr != nil {
		return false, readErr
	}
	if current.UID != reservation.UID || current.ResourceVersion != reservation.ResourceVersion {
		return false, fmt.Errorf("pool cleanup checkpoint changed before deletion")
	}
	checkpoint, decodeErr := DecodePoolCleanupCheckpoint(current)
	if decodeErr != nil {
		return false, decodeErr
	}
	if checkpoint == nil || checkpoint.ClusterUID != clusterUID || checkpoint.ClassUID != classUID ||
		checkpoint.ClusterID != clusterID || checkpoint.PoolID != candidate.ID || checkpoint.AccountID != accountID || checkpoint.Region != region {
		return false, fmt.Errorf("pool cleanup checkpoint target changed before deletion")
	}
	if operationErr := iksClient.DeleteWorkerPool(ctx, clusterID, candidate.ID); operationErr != nil && !IsNotFound(operationErr) {
		return false, operationErr
	}
	return true, nil
}
