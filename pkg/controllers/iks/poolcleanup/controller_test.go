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

package poolcleanup

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

// mockIKSClient implements ibm.IKSClientInterface for testing pool cleanup.
type mockIKSClient struct {
	mu              sync.Mutex
	pools           []*ibm.WorkerPool
	listErr         error
	deleteErr       error
	deleteCalls     []string
	expectedCluster string
	t               *testing.T
}

func (m *mockIKSClient) ListWorkerPools(_ context.Context, clusterID string) ([]*ibm.WorkerPool, error) {
	if m.t != nil && m.expectedCluster != "" {
		assert.Equal(m.t, m.expectedCluster, clusterID, "ListWorkerPools called with wrong clusterID")
	}
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.pools, nil
}

func (m *mockIKSClient) DeleteWorkerPool(_ context.Context, clusterID string, poolID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.t != nil && m.expectedCluster != "" {
		assert.Equal(m.t, m.expectedCluster, clusterID, "DeleteWorkerPool called with wrong clusterID")
	}
	m.deleteCalls = append(m.deleteCalls, poolID)
	return m.deleteErr
}

func (m *mockIKSClient) getDeleteCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.deleteCalls))
	copy(out, m.deleteCalls)
	return out
}

func (m *mockIKSClient) GetClusterConfig(context.Context, string) (string, error) {
	return "", nil
}
func (m *mockIKSClient) GetWorkerDetails(context.Context, string, string) (*ibm.IKSWorkerDetails, error) {
	return nil, nil
}
func (m *mockIKSClient) GetVPCInstanceIDFromWorker(context.Context, string, string) (string, error) {
	return "", nil
}
func (m *mockIKSClient) GetWorkerPool(context.Context, string, string) (*ibm.WorkerPool, error) {
	return nil, nil
}
func (m *mockIKSClient) ResizeWorkerPool(context.Context, string, string, int) error {
	return nil
}
func (m *mockIKSClient) IncrementWorkerPool(context.Context, string, string) (int, error) {
	return 0, nil
}
func (m *mockIKSClient) DecrementWorkerPool(context.Context, string, string) (int, error) {
	return 0, nil
}
func (m *mockIKSClient) CreateWorkerPool(context.Context, string, *ibm.WorkerPoolCreateRequest) (*ibm.WorkerPool, error) {
	return nil, nil
}

// --- helpers ---

func newTestController() *Controller {
	return &Controller{
		poolTracking: make(map[string]*poolTrackingInfo),
	}
}

func managedPool(id string, sizePerZone, actualSize int) *ibm.WorkerPool {
	return &ibm.WorkerPool{
		ID:          id,
		Name:        "pool-" + id,
		SizePerZone: sizePerZone,
		ActualSize:  actualSize,
		Labels:      map[string]string{KarpenterManagedLabel: "true"},
	}
}

func nodeClassWithDynamicPools(clusterID string, cleanup *v1alpha1.IKSPoolCleanupPolicy) *v1alpha1.IBMNodeClass {
	return &v1alpha1.IBMNodeClass{
		Spec: v1alpha1.IBMNodeClassSpec{
			IKSClusterID: clusterID,
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{
				Enabled:       true,
				CleanupPolicy: cleanup,
			},
		},
	}
}

// --- isKarpenterManaged ---

func TestIsKarpenterManaged(t *testing.T) {
	c := newTestController()
	tests := []struct {
		name   string
		pool   *ibm.WorkerPool
		expect bool
	}{
		{"nil labels", &ibm.WorkerPool{Labels: nil}, false},
		{"empty labels", &ibm.WorkerPool{Labels: map[string]string{}}, false},
		{"wrong value", &ibm.WorkerPool{Labels: map[string]string{KarpenterManagedLabel: "false"}}, false},
		{"unrelated label", &ibm.WorkerPool{Labels: map[string]string{"team": "alpha"}}, false},
		{"correct label", &ibm.WorkerPool{Labels: map[string]string{KarpenterManagedLabel: "true"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, c.isKarpenterManaged(tt.pool))
		})
	}
}

// --- isDynamicPoolsEnabled ---

func TestIsDynamicPoolsEnabled(t *testing.T) {
	c := newTestController()
	tests := []struct {
		name   string
		nc     *v1alpha1.IBMNodeClass
		expect bool
	}{
		{"nil config", &v1alpha1.IBMNodeClass{}, false},
		{"disabled", &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{Enabled: false},
		}}, false},
		{"enabled", &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{Enabled: true},
		}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, c.isDynamicPoolsEnabled(tt.nc))
		})
	}
}

// --- isCleanupEnabled ---

func TestIsCleanupEnabled(t *testing.T) {
	c := newTestController()
	boolTrue := true
	boolFalse := false

	tests := []struct {
		name   string
		nc     *v1alpha1.IBMNodeClass
		expect bool
	}{
		{"nil config defaults to true", &v1alpha1.IBMNodeClass{}, true},
		{"nil cleanup policy defaults to true", &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{},
		}}, true},
		{"nil DeleteOnEmpty returns false", &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{
				CleanupPolicy: &v1alpha1.IKSPoolCleanupPolicy{},
			},
		}}, false},
		{"DeleteOnEmpty true", &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{
				CleanupPolicy: &v1alpha1.IKSPoolCleanupPolicy{DeleteOnEmpty: &boolTrue},
			},
		}}, true},
		{"DeleteOnEmpty false", &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{
				CleanupPolicy: &v1alpha1.IKSPoolCleanupPolicy{DeleteOnEmpty: &boolFalse},
			},
		}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, c.isCleanupEnabled(tt.nc))
		})
	}
}

// --- getEmptyPoolTTL ---

func TestGetEmptyPoolTTL(t *testing.T) {
	c := newTestController()
	tests := []struct {
		name   string
		nc     *v1alpha1.IBMNodeClass
		expect time.Duration
	}{
		{"nil config", &v1alpha1.IBMNodeClass{}, DefaultEmptyPoolTTL},
		{"nil cleanup policy", &v1alpha1.IBMNodeClass{Spec: v1alpha1.IBMNodeClassSpec{
			IKSDynamicPools: &v1alpha1.IKSDynamicPoolConfig{},
		}}, DefaultEmptyPoolTTL},
		{"empty string", nodeClassWithDynamicPools("c1", &v1alpha1.IKSPoolCleanupPolicy{EmptyPoolTTL: ""}), DefaultEmptyPoolTTL},
		{"30m", nodeClassWithDynamicPools("c1", &v1alpha1.IKSPoolCleanupPolicy{EmptyPoolTTL: "30m"}), 30 * time.Minute},
		{"1h", nodeClassWithDynamicPools("c1", &v1alpha1.IKSPoolCleanupPolicy{EmptyPoolTTL: "1h"}), time.Hour},
		{"5s", nodeClassWithDynamicPools("c1", &v1alpha1.IKSPoolCleanupPolicy{EmptyPoolTTL: "5s"}), 5 * time.Second},
		{"invalid duration falls back", nodeClassWithDynamicPools("c1", &v1alpha1.IKSPoolCleanupPolicy{EmptyPoolTTL: "invalid"}), DefaultEmptyPoolTTL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, c.getEmptyPoolTTL(tt.nc))
		})
	}
}

// --- processPoolState: state transitions ---

func TestProcessPoolState_FirstTimeEmpty_StartsTracking(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	pool := managedPool("p1", 0, 0)
	key := "cluster/p1"

	c.processPoolState(context.Background(), mock, "cluster", pool, key, 15*time.Minute, logr.Discard())

	require.Contains(t, c.poolTracking, key)
	assert.WithinDuration(t, time.Now(), c.poolTracking[key].emptyTime, time.Second)
	assert.Equal(t, 0, c.poolTracking[key].deletionErrors)
	assert.Empty(t, mock.getDeleteCalls())
}

func TestProcessPoolState_EmptyButTTLNotExpired_NoDeletion(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	pool := managedPool("p1", 0, 0)
	key := "cluster/p1"

	c.poolTracking[key] = &poolTrackingInfo{emptyTime: time.Now(), deletionErrors: 0}

	c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Hour, logr.Discard())

	assert.Contains(t, c.poolTracking, key)
	assert.Empty(t, mock.getDeleteCalls())
}

func TestProcessPoolState_TTLExpired_DeletionSucceeds(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	pool := managedPool("p1", 0, 0)
	key := "cluster/p1"

	c.poolTracking[key] = &poolTrackingInfo{emptyTime: time.Now().Add(-time.Hour)}

	c.processPoolState(context.Background(), mock, "cluster", pool, key, 15*time.Minute, logr.Discard())

	assert.NotContains(t, c.poolTracking, key)
	assert.Equal(t, []string{"p1"}, mock.getDeleteCalls())
}

func TestProcessPoolState_TTLExpired_DeletionFails_IncrementsErrors(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{deleteErr: fmt.Errorf("API unavailable")}
	pool := managedPool("p1", 0, 0)
	key := "cluster/p1"

	c.poolTracking[key] = &poolTrackingInfo{emptyTime: time.Now().Add(-time.Hour)}

	c.processPoolState(context.Background(), mock, "cluster", pool, key, 15*time.Minute, logr.Discard())

	require.Contains(t, c.poolTracking, key)
	assert.Equal(t, 1, c.poolTracking[key].deletionErrors)
	assert.Equal(t, []string{"p1"}, mock.getDeleteCalls())
}

func TestProcessPoolState_MaxRetriesExceeded_SkipsDeletion(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	pool := managedPool("p1", 0, 0)
	key := "cluster/p1"

	c.poolTracking[key] = &poolTrackingInfo{
		emptyTime:      time.Now().Add(-time.Hour),
		deletionErrors: maxDeletionRetries,
	}

	c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Minute, logr.Discard())

	assert.Contains(t, c.poolTracking, key)
	assert.Equal(t, maxDeletionRetries, c.poolTracking[key].deletionErrors)
	assert.Empty(t, mock.getDeleteCalls())
}

func TestProcessPoolState_PoolBecomesNonEmpty_RemovesTracking(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	pool := managedPool("p1", 1, 1)
	key := "cluster/p1"

	c.poolTracking[key] = &poolTrackingInfo{emptyTime: time.Now().Add(-time.Hour)}

	c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Minute, logr.Discard())

	assert.NotContains(t, c.poolTracking, key)
	assert.Empty(t, mock.getDeleteCalls())
}

func TestProcessPoolState_NonEmptyPoolNeverTracked_NoAction(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	pool := managedPool("p1", 2, 2)
	key := "cluster/p1"

	c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Minute, logr.Discard())

	assert.NotContains(t, c.poolTracking, key)
	assert.Empty(t, mock.getDeleteCalls())
}

func TestProcessPoolState_PartiallyEmpty_SizePerZoneNonZero(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	pool := managedPool("p1", 1, 0)
	key := "cluster/p1"

	c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Minute, logr.Discard())

	assert.NotContains(t, c.poolTracking, key)
	assert.Empty(t, mock.getDeleteCalls(), "partially empty pool should not be deleted")
}

func TestProcessPoolState_PartiallyEmpty_ActualSizeNonZero(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	pool := managedPool("p1", 0, 1)
	key := "cluster/p1"

	c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Minute, logr.Discard())

	assert.NotContains(t, c.poolTracking, key)
	assert.Empty(t, mock.getDeleteCalls(), "partially empty pool should not be deleted")
}

// --- retry escalation ---

func TestProcessPoolState_RetryEscalation(t *testing.T) {
	c := newTestController()
	pool := managedPool("p1", 0, 0)
	key := "cluster/p1"

	for i := 0; i < maxDeletionRetries; i++ {
		mock := &mockIKSClient{deleteErr: fmt.Errorf("fail-%d", i)}
		c.poolTracking[key] = &poolTrackingInfo{
			emptyTime:      time.Now().Add(-time.Hour),
			deletionErrors: i,
		}
		c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Minute, logr.Discard())
		require.Equal(t, i+1, c.poolTracking[key].deletionErrors, "retry %d", i)
	}

	// After max retries, deletion should be skipped entirely
	mock := &mockIKSClient{}
	c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Minute, logr.Discard())
	assert.Empty(t, mock.getDeleteCalls())
}

// --- full lifecycle ---

func TestFullLifecycle_EmptyToDeleted(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	pool := managedPool("p1", 0, 0)
	key := "cluster/p1"
	ttl := 5 * time.Minute

	// Cycle 1: first seen empty, starts TTL countdown
	c.processPoolState(context.Background(), mock, "cluster", pool, key, ttl, logr.Discard())
	require.Contains(t, c.poolTracking, key)
	assert.Empty(t, mock.getDeleteCalls())

	// Cycle 2: still empty, TTL not expired, no action
	c.processPoolState(context.Background(), mock, "cluster", pool, key, ttl, logr.Discard())
	assert.Contains(t, c.poolTracking, key)
	assert.Empty(t, mock.getDeleteCalls())

	// Simulate TTL expiry
	c.poolTracking[key].emptyTime = time.Now().Add(-10 * time.Minute)

	// Cycle 3: TTL expired, pool deleted
	c.processPoolState(context.Background(), mock, "cluster", pool, key, ttl, logr.Discard())
	assert.NotContains(t, c.poolTracking, key)
	assert.Equal(t, []string{"p1"}, mock.getDeleteCalls())
}

func TestFullLifecycle_RecoveryResetsTracking(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	key := "cluster/p1"
	ttl := 5 * time.Minute

	emptyPool := managedPool("p1", 0, 0)
	nonEmptyPool := managedPool("p1", 1, 1)

	// Pool starts empty
	c.processPoolState(context.Background(), mock, "cluster", emptyPool, key, ttl, logr.Discard())
	require.Contains(t, c.poolTracking, key)

	// Pool recovers (workload scheduled)
	c.processPoolState(context.Background(), mock, "cluster", nonEmptyPool, key, ttl, logr.Discard())
	assert.NotContains(t, c.poolTracking, key)

	// Pool becomes empty again: fresh tracking, reset error count
	c.processPoolState(context.Background(), mock, "cluster", emptyPool, key, ttl, logr.Discard())
	require.Contains(t, c.poolTracking, key)
	assert.WithinDuration(t, time.Now(), c.poolTracking[key].emptyTime, time.Second)
	assert.Equal(t, 0, c.poolTracking[key].deletionErrors)
	assert.Empty(t, mock.getDeleteCalls())
}

func TestFullLifecycle_FailureThenSuccess(t *testing.T) {
	c := newTestController()
	pool := managedPool("p1", 0, 0)
	key := "cluster/p1"

	// First attempt fails
	failMock := &mockIKSClient{deleteErr: fmt.Errorf("transient")}
	c.poolTracking[key] = &poolTrackingInfo{emptyTime: time.Now().Add(-time.Hour)}
	c.processPoolState(context.Background(), failMock, "cluster", pool, key, time.Minute, logr.Discard())
	require.Equal(t, 1, c.poolTracking[key].deletionErrors)

	// Second attempt succeeds
	successMock := &mockIKSClient{}
	c.processPoolState(context.Background(), successMock, "cluster", pool, key, time.Minute, logr.Discard())
	assert.NotContains(t, c.poolTracking, key)
	assert.Equal(t, []string{"p1"}, successMock.getDeleteCalls())
}

// --- cleanupEmptyPools ---

func TestCleanupEmptyPools_SkipsUnmanagedPools(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{
		pools: []*ibm.WorkerPool{
			{ID: "default", Name: "default-pool", Labels: nil},
			{ID: "user", Name: "user-pool", Labels: map[string]string{"team": "alpha"}},
		},
	}
	nc := nodeClassWithDynamicPools("cluster", nil)

	err := c.cleanupEmptyPools(context.Background(), mock, "cluster", nc)

	assert.NoError(t, err)
	assert.Empty(t, c.poolTracking)
	assert.Empty(t, mock.getDeleteCalls(), "unmanaged pools should never be deleted")
}

func TestCleanupEmptyPools_ProcessesManagedPools(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{
		t:               t,
		expectedCluster: "cluster",
		pools: []*ibm.WorkerPool{
			{ID: "default", Name: "default-pool"},
			managedPool("m1", 0, 0),
			managedPool("m2", 1, 1),
		},
	}
	nc := nodeClassWithDynamicPools("cluster", nil)

	err := c.cleanupEmptyPools(context.Background(), mock, "cluster", nc)

	assert.NoError(t, err)
	assert.Contains(t, c.poolTracking, "cluster/m1")
	assert.NotContains(t, c.poolTracking, "cluster/default")
	assert.NotContains(t, c.poolTracking, "cluster/m2")
}

func TestCleanupEmptyPools_ListError(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{listErr: fmt.Errorf("network error")}
	nc := nodeClassWithDynamicPools("cluster", nil)

	err := c.cleanupEmptyPools(context.Background(), mock, "cluster", nc)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "listing worker pools")
}

func TestCleanupEmptyPools_CustomTTLPreventsEarlyDeletion(t *testing.T) {
	c := newTestController()
	c.poolTracking["cluster/p1"] = &poolTrackingInfo{
		emptyTime: time.Now().Add(-10 * time.Minute),
	}
	mock := &mockIKSClient{
		pools: []*ibm.WorkerPool{managedPool("p1", 0, 0)},
	}
	// 30m TTL: 10 minutes elapsed < 30 minutes required
	nc := nodeClassWithDynamicPools("cluster", &v1alpha1.IKSPoolCleanupPolicy{EmptyPoolTTL: "30m"})

	err := c.cleanupEmptyPools(context.Background(), mock, "cluster", nc)

	assert.NoError(t, err)
	assert.Contains(t, c.poolTracking, "cluster/p1")
	assert.Empty(t, mock.getDeleteCalls())
}

func TestCleanupEmptyPools_CustomTTLAllowsDeletion(t *testing.T) {
	c := newTestController()
	c.poolTracking["cluster/p1"] = &poolTrackingInfo{
		emptyTime: time.Now().Add(-time.Hour),
	}
	mock := &mockIKSClient{
		t:               t,
		expectedCluster: "cluster",
		pools:           []*ibm.WorkerPool{managedPool("p1", 0, 0)},
	}
	// 30m TTL: 1 hour elapsed > 30 minutes required
	nc := nodeClassWithDynamicPools("cluster", &v1alpha1.IKSPoolCleanupPolicy{EmptyPoolTTL: "30m"})

	err := c.cleanupEmptyPools(context.Background(), mock, "cluster", nc)

	assert.NoError(t, err)
	assert.NotContains(t, c.poolTracking, "cluster/p1")
	assert.Equal(t, []string{"p1"}, mock.getDeleteCalls())
}

func TestCleanupEmptyPools_MultiplePoolsMixedState(t *testing.T) {
	c := newTestController()
	// p1: empty, TTL expired -> should delete
	c.poolTracking["cluster/p1"] = &poolTrackingInfo{emptyTime: time.Now().Add(-time.Hour)}
	// p2: empty, first time -> should start tracking
	// p3: non-empty -> no tracking
	mock := &mockIKSClient{
		pools: []*ibm.WorkerPool{
			managedPool("p1", 0, 0),
			managedPool("p2", 0, 0),
			managedPool("p3", 2, 2),
		},
	}
	nc := nodeClassWithDynamicPools("cluster", nil)

	err := c.cleanupEmptyPools(context.Background(), mock, "cluster", nc)

	assert.NoError(t, err)
	assert.NotContains(t, c.poolTracking, "cluster/p1")
	assert.Contains(t, c.poolTracking, "cluster/p2")
	assert.NotContains(t, c.poolTracking, "cluster/p3")
	assert.Equal(t, []string{"p1"}, mock.getDeleteCalls())
}

// --- Reconcile ---

func TestReconcile_NilIBMClient(t *testing.T) {
	c := NewController(nil, nil)
	result, err := c.Reconcile(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, time.Minute, result.RequeueAfter)
}

// --- concurrency ---

func TestConcurrent_ParallelPoolTracking(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	poolCount := 50

	var wg sync.WaitGroup
	for i := 0; i < poolCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pool := managedPool(fmt.Sprintf("p%d", idx), 0, 0)
			key := fmt.Sprintf("cluster/p%d", idx)
			c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Hour, logr.Discard())
		}(i)
	}
	wg.Wait()

	assert.Len(t, c.poolTracking, poolCount)
}

func TestConcurrent_ParallelDeletions(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}
	poolCount := 20

	for i := 0; i < poolCount; i++ {
		key := fmt.Sprintf("cluster/p%d", i)
		c.poolTracking[key] = &poolTrackingInfo{emptyTime: time.Now().Add(-time.Hour)}
	}

	var wg sync.WaitGroup
	for i := 0; i < poolCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pool := managedPool(fmt.Sprintf("p%d", idx), 0, 0)
			key := fmt.Sprintf("cluster/p%d", idx)
			c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Minute, logr.Discard())
		}(i)
	}
	wg.Wait()

	assert.Empty(t, c.poolTracking)
	assert.Len(t, mock.getDeleteCalls(), poolCount)
}

func TestConcurrent_MixedOperations(t *testing.T) {
	c := newTestController()
	mock := &mockIKSClient{}

	// Pre-populate half the pools as tracked (TTL expired)
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("cluster/p%d", i)
		c.poolTracking[key] = &poolTrackingInfo{emptyTime: time.Now().Add(-time.Hour)}
	}

	var wg sync.WaitGroup

	// Goroutines 0-9: process tracked pools (should delete)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pool := managedPool(fmt.Sprintf("p%d", idx), 0, 0)
			key := fmt.Sprintf("cluster/p%d", idx)
			c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Minute, logr.Discard())
		}(i)
	}

	// Goroutines 10-19: process new empty pools (should start tracking)
	for i := 10; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pool := managedPool(fmt.Sprintf("p%d", idx), 0, 0)
			key := fmt.Sprintf("cluster/p%d", idx)
			c.processPoolState(context.Background(), mock, "cluster", pool, key, time.Hour, logr.Discard())
		}(i)
	}

	wg.Wait()

	// Pools 0-9 should be deleted, pools 10-19 should be tracked
	assert.Len(t, c.poolTracking, 10)
	assert.Len(t, mock.getDeleteCalls(), 10)
	for i := 10; i < 20; i++ {
		assert.Contains(t, c.poolTracking, fmt.Sprintf("cluster/p%d", i))
	}
}
