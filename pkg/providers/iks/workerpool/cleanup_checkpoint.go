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
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

const (
	ReservationVersionKey              = "version"
	ReservationMinimumWriterVersionKey = "minimumWriterVersion"
)

type PoolCleanupCheckpoint struct {
	Version, MinimumWriterVersion        int
	ClusterID, PoolID, AccountID, Region string
	ClusterUID, ClassUID                 string
}

func DecodePoolCleanupCheckpoint(reservation *corev1.ConfigMap) (*PoolCleanupCheckpoint, error) {
	if reservation == nil || reservation.Data[ReservationCleanupKey] == "" {
		return nil, nil
	}
	for key := range reservation.Data {
		switch key {
		case ReservationVersionKey, ReservationMinimumWriterVersionKey, ReservationPhaseKey, ReservationCleanupKey,
			ReservationClusterIDKey, ReservationPoolIDKey, ReservationAccountIDKey, ReservationRegionKey:
		default:
			return nil, fmt.Errorf("pool cleanup checkpoint has an unsupported field %q", key)
		}
	}
	version := func(key string) (int, error) {
		raw, present := reservation.Data[key]
		if !present {
			return 0, nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil || strconv.Itoa(value) != raw {
			return 0, fmt.Errorf("pool cleanup checkpoint has invalid %s", key)
		}
		return value, nil
	}
	checkpoint := &PoolCleanupCheckpoint{
		ClusterID: reservation.Data[ReservationClusterIDKey], PoolID: reservation.Data[ReservationPoolIDKey],
		AccountID: reservation.Data[ReservationAccountIDKey], Region: reservation.Data[ReservationRegionKey],
		ClusterUID: reservation.Labels[ownership.ClusterUIDLabel], ClassUID: reservation.Labels[ownership.NodeClassUIDLabel],
	}
	var err error
	if checkpoint.Version, err = version(ReservationVersionKey); err != nil {
		return nil, err
	}
	if checkpoint.MinimumWriterVersion, err = version(ReservationMinimumWriterVersionKey); err != nil {
		return nil, err
	}
	if versionErr := ownership.ValidateStateVersion(checkpoint.Version, checkpoint.MinimumWriterVersion); versionErr != nil {
		return nil, versionErr
	}
	if reservation.Data[ReservationPhaseKey] != "deleting" || reservation.Data[ReservationCleanupKey] != "true" ||
		checkpoint.ClusterID == "" || checkpoint.PoolID == "" || !accountPattern.MatchString(checkpoint.AccountID) || checkpoint.Region == "" ||
		checkpoint.ClusterUID == "" || checkpoint.ClassUID == "" ||
		reservation.Labels[ownership.ManagedLabel] != "true" || reservation.Labels[ownership.ProviderLabel] != "iks" ||
		reservation.Labels[ownership.ClaimUIDLabel] != "" || len(reservation.OwnerReferences) != 0 {
		return nil, fmt.Errorf("pool cleanup checkpoint has invalid target or ownership")
	}
	return checkpoint, nil
}
