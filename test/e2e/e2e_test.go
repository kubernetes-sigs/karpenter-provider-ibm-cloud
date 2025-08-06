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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	ibmcloud "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
)

const (
	testTimeout  = 10 * time.Minute
	pollInterval = 10 * time.Second
)

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

// E2ETestSuite contains the test environment
type E2ETestSuite struct {
	kubeClient           client.Client
	ibmClient            *ibm.Client
	cloudProvider        *ibmcloud.CloudProvider
	instanceTypeProvider instancetype.Provider

	// Test IBM Cloud resources
	testVPC    string
	testSubnet string
	testImage  string
	testRegion string
	testZone   string
}

// SetupE2ETestSuite initializes the test environment
func SetupE2ETestSuite(t *testing.T) *E2ETestSuite {
	// Skip if not running E2E tests
	if os.Getenv("RUN_E2E_TESTS") != "true" {
		t.Skip("Skipping E2E tests - set RUN_E2E_TESTS=true to run")
	}

	// Verify required environment variables
	requiredEnvVars := []string{
		"IBMCLOUD_API_KEY",
		"VPC_API_KEY",
		"IBMCLOUD_REGION",
		"TEST_VPC_ID",
		"TEST_SUBNET_ID",
		"TEST_IMAGE_ID",
		"TEST_ZONE",
		"TEST_SECURITY_GROUP_ID",
	}

	for _, envVar := range requiredEnvVars {
		if os.Getenv(envVar) == "" {
			t.Fatalf("Required environment variable %s is not set", envVar)
		}
	}

	// Create Kubernetes client using local kubeconfig
	cfg, err := config.GetConfigWithContext("kubernetes-admin@kubernetes")
	if err != nil {
		// Fallback to local kubeconfig file
		cfg, err = clientcmd.BuildConfigFromFlags("", "../../kubeconfig")
		require.NoError(t, err)
	}

	// Use the global scheme which has Karpenter v1 types registered automatically
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

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

	// Create IBM client
	ibmClient, err := ibm.NewClient()
	require.NoError(t, err)

	// Create providers using the provider factory pattern
	pricingProvider := pricing.NewIBMPricingProvider(ibmClient)
	instanceTypeProvider := instancetype.NewProvider(ibmClient, pricingProvider)
	subnetProvider := subnet.NewProvider(ibmClient)

	// Create cloud provider
	cloudProvider := ibmcloud.New(
		kubeClient,
		nil, // No event recorder needed for tests
		ibmClient,
		instanceTypeProvider,
		subnetProvider,
		nil, // Use default circuit breaker config for tests
	)

	return &E2ETestSuite{
		kubeClient:           kubeClient,
		ibmClient:            ibmClient,
		cloudProvider:        cloudProvider,
		instanceTypeProvider: instanceTypeProvider,
		testVPC:              os.Getenv("TEST_VPC_ID"),
		testSubnet:           os.Getenv("TEST_SUBNET_ID"),
		testImage:            os.Getenv("TEST_IMAGE_ID"),
		testRegion:           os.Getenv("IBMCLOUD_REGION"),
		testZone:             os.Getenv("TEST_ZONE"),
	}
}

func TestE2EFullWorkflow(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()
	_ = ctx

	testName := fmt.Sprintf("e2e-test-%d", time.Now().Unix())

	t.Logf("Starting E2E test: %s", testName)

	// Step 1: Create NodeClass
	nodeClass := suite.createTestNodeClass(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Step 2: Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Step 3: Create NodePool
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Step 4: Create NodeClaim
	nodeClaim := suite.createTestNodeClaim(t, testName, nodeClass.Name)
	t.Logf("Created NodeClaim: %s", nodeClaim.Name)

	// Step 5: Wait for instance to be created
	suite.waitForInstanceCreation(t, nodeClaim.Name)
	t.Logf("Instance created for NodeClaim: %s", nodeClaim.Name)

	// Step 6: Verify instance exists in IBM Cloud
	suite.verifyInstanceInIBMCloud(t, nodeClaim.Name)
	t.Logf("Verified instance exists in IBM Cloud")

	// Step 6.5: Dump bootstrap logs for debugging
	suite.dumpBootstrapLogs(t, nodeClaim.Name)
	t.Logf("Bootstrap logs dumped for debugging")

	// Step 7: Test instance deletion
	suite.deleteNodeClaim(t, nodeClaim.Name)
	t.Logf("Deleted NodeClaim: %s", nodeClaim.Name)

	// Step 8: Wait for instance to be deleted
	suite.waitForInstanceDeletion(t, nodeClaim.Name)
	t.Logf("Instance deleted for NodeClaim: %s", nodeClaim.Name)

	// Step 9: Clean up remaining resources ONLY after all verification is complete
	t.Logf("Cleaning up remaining test resources")
	suite.cleanupTestResources(t, testName)

	t.Logf("E2E test completed successfully: %s", testName)
}

func (s *E2ETestSuite) createTestNodeClass(t *testing.T, testName string) *v1alpha1.IBMNodeClass {
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclass", testName),
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            s.testRegion,
			Zone:              s.testZone,
			InstanceProfile:   "bx2-2x8", // Small instance for testing
			Image:             s.testImage,
			VPC:               s.testVPC,
			Subnet:            s.testSubnet,
			SecurityGroups:    []string{os.Getenv("TEST_SECURITY_GROUP_ID")},
			APIServerEndpoint: os.Getenv("KUBERNETES_API_SERVER_ENDPOINT"), // Internal cluster endpoint
			BootstrapMode:     &[]string{"cloud-init"}[0],                  // Explicit bootstrap mode
			ResourceGroup:     os.Getenv("IBM_RESOURCE_GROUP_ID"),          // Resource group from env
			SSHKeys:           []string{os.Getenv("IBM_SSH_KEY_ID")},       // SSH key for troubleshooting
			Tags: map[string]string{
				"test":       "e2e",
				"test-name":  testName,
				"created-by": "karpenter-e2e",
				"purpose":    "e2e-verification",
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClass)
	require.NoError(t, err)

	return nodeClass
}

func (s *E2ETestSuite) createTestNodePool(t *testing.T, testName, nodeClassName string) *karpv1.NodePool {
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodepool", testName),
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter.ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"bx2-2x8"},
							},
						},
					},
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodePool)
	require.NoError(t, err)

	return nodePool
}

func (s *E2ETestSuite) createTestNodeClaim(t *testing.T, testName, nodeClassName string) *karpv1.NodeClaim {
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclaim", testName),
		},
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: "karpenter.ibm.sh",
				Kind:  "IBMNodeClass",
				Name:  nodeClassName,
			},
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{
					NodeSelectorRequirement: corev1.NodeSelectorRequirement{
						Key:      "node.kubernetes.io/instance-type",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"bx2-2x8"},
					},
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClaim)
	require.NoError(t, err)

	return nodeClaim
}

func (s *E2ETestSuite) waitForNodeClassReady(t *testing.T, nodeClassName string) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var nodeClass v1alpha1.IBMNodeClass
		err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClassName}, &nodeClass)
		if err != nil {
			return false, err
		}

		// Check if Ready condition is True
		for _, condition := range nodeClass.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue {
				return true, nil
			}
		}

		return false, nil
	})

	require.NoError(t, err, "NodeClass should become ready within timeout")
}

func (s *E2ETestSuite) waitForInstanceCreation(t *testing.T, nodeClaimName string) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var nodeClaim karpv1.NodeClaim
		err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
		if err != nil {
			return false, err
		}

		// Check if ProviderID is set and instance is launched
		if nodeClaim.Status.ProviderID == "" {
			return false, nil
		}

		// Check if Launched condition is True
		for _, condition := range nodeClaim.Status.Conditions {
			if condition.Type == "Launched" && condition.Status == metav1.ConditionTrue {
				return true, nil
			}
		}

		return false, nil
	})

	require.NoError(t, err, "NodeClaim should be launched within timeout")
}

func (s *E2ETestSuite) verifyInstanceInIBMCloud(t *testing.T, nodeClaimName string) {
	ctx := context.Background()

	// Get the NodeClaim to extract ProviderID
	var nodeClaim karpv1.NodeClaim
	err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
	require.NoError(t, err)
	require.NotEmpty(t, nodeClaim.Status.ProviderID)

	t.Logf("Verifying instance with ProviderID: %s", nodeClaim.Status.ProviderID)

	// Add a small delay to ensure instance is fully available in IBM Cloud API
	time.Sleep(5 * time.Second)

	// Verify instance exists via cloud provider
	retrievedNodeClaim, err := s.cloudProvider.Get(ctx, nodeClaim.Status.ProviderID)
	require.NoError(t, err)
	assert.Equal(t, nodeClaim.Status.ProviderID, retrievedNodeClaim.Status.ProviderID)
}

func (s *E2ETestSuite) deleteNodeClaim(t *testing.T, nodeClaimName string) {
	ctx := context.Background()

	var nodeClaim karpv1.NodeClaim
	err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
	require.NoError(t, err)

	err = s.kubeClient.Delete(ctx, &nodeClaim)
	require.NoError(t, err)
}

func (s *E2ETestSuite) waitForInstanceDeletion(t *testing.T, nodeClaimName string) {
	// Use longer timeout for deletion as IBM Cloud instances can take time to clean up
	deletionTimeout := 15 * time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), deletionTimeout)
	defer cancel()

	err := wait.PollUntilContextTimeout(ctx, pollInterval, deletionTimeout, true, func(ctx context.Context) (bool, error) {
		var nodeClaim karpv1.NodeClaim
		err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
		if errors.IsNotFound(err) {
			return true, nil // NodeClaim was deleted
		}
		if err != nil {
			return false, err
		}

		// Log the current status to help debug timeout issues
		t.Logf("NodeClaim %s still exists, status: %+v", nodeClaimName, nodeClaim.Status)
		return false, nil // Still exists
	})

	require.NoError(t, err, "NodeClaim should be deleted within timeout")
}

func (s *E2ETestSuite) cleanupTestResources(t *testing.T, testName string) {
	ctx := context.Background()

	// List of resources to clean up
	resources := []struct {
		name    string
		obj     client.Object
		listObj client.ObjectList
	}{
		{
			name:    "NodeClaim",
			obj:     &karpv1.NodeClaim{},
			listObj: &karpv1.NodeClaimList{},
		},
		{
			name:    "NodePool",
			obj:     &karpv1.NodePool{},
			listObj: &karpv1.NodePoolList{},
		},
		{
			name:    "IBMNodeClass",
			obj:     &v1alpha1.IBMNodeClass{},
			listObj: &v1alpha1.IBMNodeClassList{},
		},
	}

	for _, resource := range resources {
		err := s.kubeClient.List(ctx, resource.listObj)
		if err != nil {
			t.Logf("Failed to list %s for cleanup: %v", resource.name, err)
			continue
		}

		// Extract items based on resource type
		var items []client.Object
		switch list := resource.listObj.(type) {
		case *karpv1.NodeClaimList:
			for i := range list.Items {
				items = append(items, &list.Items[i])
			}
		case *karpv1.NodePoolList:
			for i := range list.Items {
				items = append(items, &list.Items[i])
			}
		case *v1alpha1.IBMNodeClassList:
			for i := range list.Items {
				items = append(items, &list.Items[i])
			}
		}

		for _, item := range items {
			// Check if this resource belongs to our test
			if labels := item.GetLabels(); labels != nil {
				if labels["test-name"] == testName || labels["test"] == "e2e" {
					err := s.kubeClient.Delete(ctx, item)
					if err != nil && !errors.IsNotFound(err) {
						t.Logf("Failed to delete %s %s: %v", resource.name, item.GetName(), err)
					} else {
						t.Logf("Cleaned up %s: %s", resource.name, item.GetName())
					}
				}
			}

			// Also check by name prefix
			if len(item.GetName()) > len(testName) && item.GetName()[:len(testName)] == testName {
				err := s.kubeClient.Delete(ctx, item)
				if err != nil && !errors.IsNotFound(err) {
					t.Logf("Failed to delete %s %s: %v", resource.name, item.GetName(), err)
				} else {
					t.Logf("Cleaned up %s: %s", resource.name, item.GetName())
				}
			}
		}
	}
}

// TestE2ENodeClassValidation tests NodeClass validation
func TestE2ENodeClassValidation(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	testName := fmt.Sprintf("validation-test-%d", time.Now().Unix())

	// Test invalid NodeClass (invalid resource references)
	invalidNodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-invalid", testName),
			Labels: map[string]string{
				"test-name": testName,
			},
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:          "us-south",
			Zone:            "us-south-1",
			InstanceProfile: "bx2-2x8",
			Image:           "r010-00000000-0000-0000-0000-000000000000",           // Valid format but non-existent image
			VPC:             "r010-00000000-0000-0000-0000-000000000000",           // Valid format but non-existent VPC
			Subnet:          "02b7-00000000-0000-0000-0000-000000000000",           // Valid format but non-existent subnet
			SecurityGroups:  []string{"r010-00000000-0000-0000-0000-000000000000"}, // Valid format but non-existent security group
		},
	}

	err := suite.kubeClient.Create(ctx, invalidNodeClass)
	require.NoError(t, err)

	// Wait for status controller to process validation
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var updatedNodeClass v1alpha1.IBMNodeClass
	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: invalidNodeClass.Name}, &updatedNodeClass); getErr != nil {
			return false, getErr
		}

		// Check if validation has been processed (either Ready=True or Ready=False)
		for _, condition := range updatedNodeClass.Status.Conditions {
			if condition.Type == "Ready" {
				t.Logf("NodeClass validation processed: Ready=%s, Reason=%s, Message=%s",
					condition.Status, condition.Reason, condition.Message)
				return true, nil
			}
		}

		t.Logf("Waiting for status controller to process NodeClass validation...")
		return false, nil
	})

	require.NoError(t, err, "Status controller should process NodeClass validation within timeout")

	// Should have conditions indicating validation failure
	found := false
	for _, condition := range updatedNodeClass.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionFalse {
			found = true
			break
		}
	}
	assert.True(t, found, "Invalid NodeClass should have Ready=False condition")

	// Clean up test resources after validation is complete
	suite.cleanupTestResources(t, testName)
}

// TestE2EInstanceTypeSelection tests automatic instance type selection
func TestE2EInstanceTypeSelection(t *testing.T) {
	suite := SetupE2ETestSuite(t)

	testName := fmt.Sprintf("instancetype-test-%d", time.Now().Unix())

	// Test NodeClass with instance requirements instead of specific profile
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-autotype", testName),
			Labels: map[string]string{
				"test-name": testName,
			},
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:         suite.testRegion,
			Zone:           suite.testZone,
			Image:          suite.testImage,
			VPC:            suite.testVPC,
			Subnet:         suite.testSubnet,
			SecurityGroups: []string{os.Getenv("TEST_SECURITY_GROUP_ID")},
			InstanceRequirements: &v1alpha1.InstanceTypeRequirements{
				MinimumCPU:    2,
				MinimumMemory: 8,
				Architecture:  "amd64",
			},
		},
	}

	err := suite.kubeClient.Create(context.Background(), nodeClass)
	require.NoError(t, err)

	// Wait for autoplacement controller to process instance requirements
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var updatedNodeClass v1alpha1.IBMNodeClass
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		if getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &updatedNodeClass); getErr != nil {
			return false, getErr
		}

		// Check if autoplacement has been processed - either instance profile is set or status has selected types
		if updatedNodeClass.Spec.InstanceProfile != "" {
			t.Logf("Autoplacement controller set InstanceProfile: %s", updatedNodeClass.Spec.InstanceProfile)
			return true, nil
		}

		if len(updatedNodeClass.Status.SelectedInstanceTypes) > 0 {
			t.Logf("Autoplacement controller populated SelectedInstanceTypes: %v", updatedNodeClass.Status.SelectedInstanceTypes)
			return true, nil
		}

		// Check conditions for autoplacement status
		for _, condition := range updatedNodeClass.Status.Conditions {
			if condition.Type == "AutoPlacement" {
				t.Logf("AutoPlacement condition: Status=%s, Reason=%s, Message=%s",
					condition.Status, condition.Reason, condition.Message)
				if condition.Status == metav1.ConditionFalse {
					return false, fmt.Errorf("autoplacement failed: %s", condition.Message)
				}
			}
		}

		t.Logf("Waiting for autoplacement controller to process instance requirements...")
		return false, nil
	})

	// If autoplacement didn't work, debug by listing available instance types
	if err != nil || (updatedNodeClass.Spec.InstanceProfile == "" && len(updatedNodeClass.Status.SelectedInstanceTypes) == 0) {
		t.Logf("Autoplacement failed or didn't complete, debugging...")

		// List available instance types to debug
		debugCtx := context.Background()
		instanceTypes, listErr := suite.instanceTypeProvider.List(debugCtx)
		if listErr != nil {
			t.Logf("Failed to list instance types: %v", listErr)
		} else {
			t.Logf("Available instance types: %d", len(instanceTypes))
			for i, it := range instanceTypes {
				if i < 5 { // Log first 5
					t.Logf("  - %s", it.Name)
				}
			}
		}

		// Also check if we can filter instance types
		if nodeClass.Spec.InstanceRequirements != nil {
			filteredTypes, filterErr := suite.instanceTypeProvider.FilterInstanceTypes(debugCtx, nodeClass.Spec.InstanceRequirements)
			if filterErr != nil {
				t.Logf("Failed to filter instance types: %v", filterErr)
			} else {
				t.Logf("Filtered instance types: %d", len(filteredTypes))
			}
		}
	}

	require.NoError(t, err, "Autoplacement controller should process NodeClass within timeout")
	assert.NotEmpty(t, updatedNodeClass.Status.SelectedInstanceTypes, "Instance types should be auto-selected")
	t.Logf("Auto-selected instance types: %v", updatedNodeClass.Status.SelectedInstanceTypes)

	// Clean up test resources after validation is complete
	suite.cleanupTestResources(t, testName)
}

// BenchmarkE2EInstanceCreation benchmarks instance creation performance
func BenchmarkE2EInstanceCreation(b *testing.B) {
	if os.Getenv("RUN_E2E_BENCHMARKS") != "true" {
		b.Skip("Skipping E2E benchmarks - set RUN_E2E_BENCHMARKS=true to run")
	}

	suite := SetupE2ETestSuite(&testing.T{})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			testName := fmt.Sprintf("bench-%d-%d", time.Now().UnixNano(), b.N)

			// Create NodeClass
			nodeClass := suite.createTestNodeClass(&testing.T{}, testName)

			// Create NodeClaim
			nodeClaim := suite.createTestNodeClaim(&testing.T{}, testName, nodeClass.Name)

			// Wait for creation (this is what we're benchmarking)
			suite.waitForInstanceCreation(&testing.T{}, nodeClaim.Name)

			// Clean up
			suite.deleteNodeClaim(&testing.T{}, nodeClaim.Name)
			suite.waitForInstanceDeletion(&testing.T{}, nodeClaim.Name)
			suite.cleanupTestResources(&testing.T{}, testName)
		}
	})
}

// extractInstanceIDFromProviderID extracts the instance ID from a ProviderID
func extractInstanceIDFromProviderID(providerID string) string {
	// ProviderID format: "ibm:///region/instance_id"
	parts := strings.Split(providerID, "/")
	if len(parts) >= 4 {
		return parts[len(parts)-1]
	}
	return ""
}

// dumpBootstrapLogs attempts to SSH into the instance and dump bootstrap logs
func (s *E2ETestSuite) dumpBootstrapLogs(t *testing.T, nodeClaimName string) {
	ctx := context.Background()

	// Get the NodeClaim to extract ProviderID
	var nodeClaim karpv1.NodeClaim
	err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
	if err != nil {
		t.Logf("Failed to get NodeClaim for bootstrap log dump: %v", err)
		return
	}

	if nodeClaim.Status.ProviderID == "" {
		t.Logf("NodeClaim has no ProviderID, cannot dump bootstrap logs")
		return
	}

	instanceID := extractInstanceIDFromProviderID(nodeClaim.Status.ProviderID)
	if instanceID == "" {
		t.Logf("Failed to extract instance ID from ProviderID: %s", nodeClaim.Status.ProviderID)
		return
	}

	t.Logf("Attempting to dump bootstrap logs for instance: %s", instanceID)

	// Create floating IP for SSH access
	floatingIP, err := s.createFloatingIPForInstance(t, instanceID, nodeClaimName)
	if err != nil {
		t.Logf("Failed to create floating IP for bootstrap log dump: %v", err)

		// Check if it's the network attachment issue
		if strings.Contains(err.Error(), "network_attachment is used") {
			t.Logf("Instance uses network attachment - SSH troubleshooting not available for this configuration")
		} else {
			t.Logf("SSH troubleshooting not available: %v", err)
		}
		return
	}

	// Ensure floating IP cleanup
	defer func() {
		if floatingIP != "" {
			s.cleanupFloatingIP(t, floatingIP)
		}
	}()

	// Wait a bit for floating IP to be ready
	time.Sleep(10 * time.Second)

	// Try to SSH and dump logs with retries
	s.attemptBootstrapLogDump(t, floatingIP, instanceID, 5)
}

// createFloatingIPForInstance creates a floating IP and assigns it to the instance
func (s *E2ETestSuite) createFloatingIPForInstance(t *testing.T, instanceID, instanceName string) (string, error) {
	t.Logf("Creating floating IP for instance %s", instanceID)

	// First check if instance exists and get its details
	cmd := exec.Command("ibmcloud", "is", "instance", instanceID, "--output", "json")
	instanceOutput, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get instance details: %v", err)
	}

	// Use IBM Cloud CLI to create floating IP
	fipName := fmt.Sprintf("e2e-debug-%s", instanceName)
	cmd = exec.Command("ibmcloud", "is", "floating-ip-reserve",
		fipName,
		"--zone", s.testZone,
		"--output", "json")

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to create floating IP: %v", err)
	}

	// Parse the floating IP details from JSON output
	outputStr := string(output)
	if !strings.Contains(outputStr, `"address"`) {
		return "", fmt.Errorf("failed to parse floating IP from output: %s", outputStr)
	}

	// Extract floating IP address
	addressStart := strings.Index(outputStr, `"address": "`) + len(`"address": "`)
	if addressStart < len(`"address": "`) {
		return "", fmt.Errorf("failed to find address in output")
	}
	addressEnd := strings.Index(outputStr[addressStart:], `"`)
	if addressEnd == -1 {
		return "", fmt.Errorf("failed to parse address from output")
	}
	floatingIP := outputStr[addressStart : addressStart+addressEnd]

	// Extract floating IP ID
	fipIDStart := strings.Index(outputStr, `"id": "`) + len(`"id": "`)
	if fipIDStart < len(`"id": "`) {
		return "", fmt.Errorf("failed to find floating IP ID in output")
	}
	fipIDEnd := strings.Index(outputStr[fipIDStart:], `"`)
	if fipIDEnd == -1 {
		return "", fmt.Errorf("failed to parse floating IP ID from output")
	}
	fipID := outputStr[fipIDStart : fipIDStart+fipIDEnd]

	// Extract primary network interface ID from instance details
	instanceStr := string(instanceOutput)
	nicStart := strings.Index(instanceStr, `"primary_network_interface"`)
	if nicStart == -1 {
		return "", fmt.Errorf("failed to find primary network interface")
	}

	idStart := strings.Index(instanceStr[nicStart:], `"id": "`) + len(`"id": "`)
	if idStart < len(`"id": "`) {
		return "", fmt.Errorf("failed to find interface ID")
	}
	idStart += nicStart
	idEnd := strings.Index(instanceStr[idStart:], `"`)
	if idEnd == -1 {
		return "", fmt.Errorf("failed to parse interface ID")
	}
	nicID := instanceStr[idStart : idStart+idEnd]

	// Associate floating IP with instance using the FIP ID
	cmd = exec.Command("ibmcloud", "is", "instance-network-interface-floating-ip-add",
		instanceID, nicID, fipID)

	if output, err := cmd.CombinedOutput(); err != nil {
		// Check if it's the network attachment error we've seen
		if strings.Contains(string(output), "network_attachment is used") {
			return "", fmt.Errorf("cannot associate floating IP: instance uses network attachment (this is expected for some configurations)")
		}
		return "", fmt.Errorf("failed to associate floating IP: %v, output: %s", err, string(output))
	}

	t.Logf("Created and associated floating IP %s with instance %s", floatingIP, instanceID)
	return floatingIP, nil
}

// cleanupFloatingIP removes the floating IP
func (s *E2ETestSuite) cleanupFloatingIP(t *testing.T, floatingIP string) {
	t.Logf("Cleaning up floating IP: %s", floatingIP)

	// Get floating IP details to find ID
	cmd := exec.Command("ibmcloud", "is", "floating-ips", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		t.Logf("Failed to list floating IPs for cleanup: %v", err)
		return
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, floatingIP) {
		t.Logf("Floating IP %s not found for cleanup", floatingIP)
		return
	}

	// Find the ID for this floating IP (simplified extraction)
	ipIndex := strings.Index(outputStr, floatingIP)
	if ipIndex == -1 {
		return
	}

	// Look backwards for the ID field
	beforeIP := outputStr[:ipIndex]
	idStart := strings.LastIndex(beforeIP, `"id": "`) + len(`"id": "`)
	if idStart < len(`"id": "`) {
		t.Logf("Failed to find floating IP ID for cleanup")
		return
	}
	idEnd := strings.Index(beforeIP[idStart:], `"`)
	if idEnd == -1 {
		t.Logf("Failed to parse floating IP ID for cleanup")
		return
	}
	floatingIPID := beforeIP[idStart : idStart+idEnd]

	// Release the floating IP
	cmd = exec.Command("ibmcloud", "is", "floating-ip-release", floatingIPID, "--force")
	if err := cmd.Run(); err != nil {
		t.Logf("Failed to release floating IP %s: %v", floatingIPID, err)
	} else {
		t.Logf("Successfully released floating IP %s", floatingIP)
	}
}

// attemptBootstrapLogDump tries to SSH and dump bootstrap logs with retries
func (s *E2ETestSuite) attemptBootstrapLogDump(t *testing.T, floatingIP, instanceID string, maxRetries int) {
	sshKey := "~/.ssh/eb"

	logFiles := []string{
		"/var/log/cloud-init.log",
		"/var/log/cloud-init-output.log",
		"/var/log/karpenter-bootstrap.log",
		"/var/log/apt/history.log",
	}

	commands := []struct {
		name string
		cmd  string
	}{
		{"cloud-init status", "cloud-init status --long"},
		{"systemd failed services", "systemctl --failed"},
		{"journal errors", "journalctl -p err --no-pager -n 50"},
		{"network status", "ip addr show && ip route show"},
		{"DNS resolution", "nslookup archive.ubuntu.com"},
		{"metadata service", "curl -sf -m 5 http://169.254.169.254/metadata/v1/instance || echo 'Metadata service failed'"},
		{"disk space", "df -h"},
		{"memory usage", "free -m"},
		{"running processes", "ps aux | head -20"},
	}

	for retry := 1; retry <= maxRetries; retry++ {
		t.Logf("Bootstrap log dump attempt %d/%d for instance %s (IP: %s)", retry, maxRetries, instanceID, floatingIP)

		// Test SSH connectivity first
		testCmd := exec.Command("ssh", "-i", sshKey, "-o", "StrictHostKeyChecking=no",
			"-o", "ConnectTimeout=10", "-o", "BatchMode=yes",
			fmt.Sprintf("root@%s", floatingIP), "echo 'SSH_OK'")

		if output, err := testCmd.Output(); err != nil || !strings.Contains(string(output), "SSH_OK") {
			t.Logf("SSH connectivity test failed (attempt %d): %v", retry, err)
			if retry < maxRetries {
				time.Sleep(time.Duration(retry*15) * time.Second) // Exponential backoff
				continue
			} else {
				t.Logf("SSH never became available after %d attempts", maxRetries)
				return
			}
		}

		t.Logf("SSH connectivity confirmed for %s", floatingIP)

		// Dump log files
		for _, logFile := range logFiles {
			t.Logf("=== Dumping %s ===", logFile)
			cmd := exec.Command("ssh", "-i", sshKey, "-o", "StrictHostKeyChecking=no",
				"-o", "ConnectTimeout=10", fmt.Sprintf("root@%s", floatingIP),
				fmt.Sprintf("tail -50 %s 2>/dev/null || echo 'File %s not found or empty'", logFile, logFile))

			if output, err := cmd.Output(); err != nil {
				t.Logf("Failed to read %s: %v", logFile, err)
			} else {
				t.Logf("Contents of %s:\n%s", logFile, string(output))
			}
		}

		// Run diagnostic commands
		for _, cmdInfo := range commands {
			t.Logf("=== Running: %s ===", cmdInfo.name)
			cmd := exec.Command("ssh", "-i", sshKey, "-o", "StrictHostKeyChecking=no",
				"-o", "ConnectTimeout=10", fmt.Sprintf("root@%s", floatingIP), cmdInfo.cmd)

			if output, err := cmd.Output(); err != nil {
				t.Logf("Command '%s' failed: %v", cmdInfo.name, err)
			} else {
				t.Logf("Output of '%s':\n%s", cmdInfo.name, string(output))
			}
		}

		t.Logf("Bootstrap log dump completed successfully for instance %s", instanceID)
		return
	}
}
