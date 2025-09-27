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
	"os"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
)

const (
	testTimeout  = 15 * time.Minute // Increased timeout for node provisioning
	pollInterval = 10 * time.Second
)

// E2ETestSuite contains the test environment
type E2ETestSuite struct {
	kubeClient client.Client

	// Test IBM Cloud resources
	testVPC           string
	testSubnet        string
	testImage         string
	testRegion        string
	testZone          string
	testSecurityGroup string
	testResourceGroup string
	testSshKeyId      string
	APIServerEndpoint string
}

// NodeSnapshot represents a snapshot of a node's state for stability monitoring
type NodeSnapshot struct {
	Name         string
	ProviderID   string
	InstanceType string
	CreationTime time.Time
	Labels       map[string]string
}

func init() {
	// Initialize logger for controller-runtime to avoid warnings
	zapConfig := zap.NewDevelopmentConfig()

	// Reduce noise in e2e tests unless verbose mode is enabled
	if os.Getenv("E2E_VERBOSE") != "true" {
		zapConfig.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	zapLogger, _ := zapConfig.Build()
	log.SetLogger(zapr.NewLogger(zapLogger))
}

// SetupE2ETestSuite initializes the test environment with pre-test cleanup
func SetupE2ETestSuite(t *testing.T) *E2ETestSuite {
	// Skip if not running E2E tests
	if os.Getenv("RUN_E2E_TESTS") != "true" {
		t.Skip("Skipping E2E tests - set RUN_E2E_TESTS=true to run")
	}

	// Verify required environment variables
	requiredEnvVars := []string{
		"IBMCLOUD_API_KEY",
		"IBMCLOUD_REGION",
		"TEST_VPC_ID",
		"TEST_SUBNET_ID",
		"TEST_IMAGE_ID",
		"TEST_ZONE",
		"TEST_SECURITY_GROUP_ID",
		"KUBERNETES_API_SERVER_ENDPOINT",
		"IBM_RESOURCE_GROUP_ID",
		"IBM_SSH_KEY_ID",
	}

	for _, envVar := range requiredEnvVars {
		if os.Getenv(envVar) == "" {
			t.Fatalf("Required environment variable %s is not set", envVar)
		}
	}

	// Create Kubernetes client using kubeconfig from environment or default
	cfg, err := config.GetConfig()
	if err != nil {
		// Fallback to explicit kubeconfig if KUBECONFIG env is not set
		kubeconfigPath := os.Getenv("KUBECONFIG")
		if kubeconfigPath == "" {
			kubeconfigPath = "../../kubecofig_test"
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		require.NoError(t, err)
	}

	// Use the global scheme which has Karpenter v1 types registered automatically
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, policyv1.AddToScheme(scheme))

	// Add Karpenter v1 types manually with proper group version registration
	karpenterGV := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	metav1.AddToGroupVersion(scheme, karpenterGV)
	scheme.AddKnownTypes(karpenterGV,
		&karpv1.NodePool{},
		&karpv1.NodePoolList{},
		&karpv1.NodeClaim{},
		&karpv1.NodeClaimList{})

	kubeClient, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	suite := &E2ETestSuite{
		kubeClient:        kubeClient,
		testVPC:           os.Getenv("TEST_VPC_ID"),
		testSubnet:        os.Getenv("TEST_SUBNET_ID"),
		testImage:         os.Getenv("TEST_IMAGE_ID"),
		testRegion:        os.Getenv("IBMCLOUD_REGION"),
		testZone:          os.Getenv("TEST_ZONE"),
		testSecurityGroup: os.Getenv("TEST_SECURITY_GROUP_ID"),
		testResourceGroup: os.Getenv("IBM_RESOURCE_GROUP_ID"),
		testSshKeyId:      os.Getenv("IBM_SSH_KEY_ID"),
		APIServerEndpoint: os.Getenv("KUBERNETES_API_SERVER_ENDPOINT"),
	}

	// Skip complex cleanup for simple tests
	if os.Getenv("E2E_SIMPLE_MODE") != "true" {
		// 🧹 CRITICAL: Pre-test cleanup to prevent circuit breaker issues
		t.Logf("🧹 Performing PRE-TEST cleanup to prevent stale resource issues...")
		suite.cleanupAllStaleResources(t)

		// Wait for cleanup to complete and verify cluster state
		suite.verifyCleanState(t)
	} else {
		t.Logf("✅ Skipping complex cleanup (simple mode)")
	}

	return suite
}

// verifyCleanState checks that cluster is clean before starting tests
func (s *E2ETestSuite) verifyCleanState(t *testing.T) {
	ctx := context.Background()

	// Check for any remaining E2E resources
	var nodeClassList v1alpha1.IBMNodeClassList
	err := s.kubeClient.List(ctx, &nodeClassList)
	if err == nil && len(nodeClassList.Items) > 0 {
		t.Logf("⚠️ Warning: Found %d IBMNodeClass resources before test start:", len(nodeClassList.Items))
		for _, nc := range nodeClassList.Items {
			t.Logf("  - %s (VPC: %s, Age: %s)", nc.Name, nc.Spec.VPC, time.Since(nc.CreationTimestamp.Time).Round(time.Second))
		}
		// Force cleanup if any remain
		if len(nodeClassList.Items) > 0 {
			t.Logf("🧹 Force cleaning remaining IBMNodeClasses...")
			s.cleanupAllStaleResources(t)
		}
	}

	var nodeClaimList karpv1.NodeClaimList
	err = s.kubeClient.List(ctx, &nodeClaimList)
	if err == nil && len(nodeClaimList.Items) > 0 {
		t.Logf("⚠️ Warning: Found %d NodeClaim resources before test start", len(nodeClaimList.Items))
		for _, nc := range nodeClaimList.Items {
			t.Logf("  - %s (Conditions: %d)", nc.Name, len(nc.Status.Conditions))
		}
	}

	t.Logf("✅ Cluster state verified - ready for testing")
}
