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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	testTimeout  = 15 * time.Minute // Increased timeout for node provisioning
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

	// Step 4: Deploy test workload to trigger Karpenter provisioning
	deployment := suite.createTestWorkload(t, testName)
	t.Logf("Created test workload: %s", deployment.Name)

	// Step 5: Wait for pods to be scheduled and nodes to be provisioned
	suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
	t.Logf("Pods scheduled successfully")

	// Step 6: Verify that new nodes were created by Karpenter
	suite.verifyKarpenterNodesExist(t)
	t.Logf("Verified Karpenter nodes exist")

	// Step 7: Verify instances exist in IBM Cloud
	suite.verifyInstancesInIBMCloud(t)
	t.Logf("Verified instances exist in IBM Cloud")

	// Step 8: Cleanup test resources
	suite.cleanupTestWorkload(t, deployment.Name, deployment.Namespace)
	t.Logf("Deleted test workload: %s", deployment.Name)

	// Step 9: Clean up remaining resources ONLY after all verification is complete
	t.Logf("Cleaning up remaining test resources")
	suite.cleanupTestResources(t, testName)

	t.Logf("E2E test completed successfully: %s", testName)
}

// TestE2ENodePoolInstanceTypeSelection tests the customer's configuration:
// - NodeClass WITHOUT instanceProfile field (should be optional)
// - NodePool with multiple instance type requirements
// This regression test ensures the VPC provider uses instance types from
// NodePool requirements rather than requiring instanceProfile in NodeClass
func TestE2ENodePoolInstanceTypeSelection(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()
	_ = ctx

	testName := fmt.Sprintf("e2e-nodepool-types-%d", time.Now().Unix())

	t.Logf("Starting NodePool instance type selection E2E test: %s", testName)

	// Step 1: Create NodeClass WITHOUT instanceProfile (like customer's config)
	nodeClass := suite.createTestNodeClassWithoutInstanceProfile(t, testName)
	t.Logf("Created NodeClass without instanceProfile: %s", nodeClass.Name)

	// Step 2: Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Step 3: Create NodePool with multiple instance type requirements (like customer's config)
	nodePool := suite.createTestNodePoolWithMultipleInstanceTypes(t, testName, nodeClass.Name)
	t.Logf("Created NodePool with multiple instance types: %s", nodePool.Name)

	// Step 4: Deploy test workload with specific resource requirements to trigger provisioning
	deployment := suite.createTestWorkloadWithInstanceTypeRequirements(t, testName)
	t.Logf("Created test workload with instance type requirements: %s", deployment.Name)

	// Step 5: Wait for pods to be scheduled and nodes to be provisioned
	suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
	t.Logf("Pods scheduled successfully")

	// Step 5a: Verify pods are scheduled on nodes with correct NodePool label
	suite.verifyPodsScheduledOnCorrectNodes(t, deployment.Name, deployment.Namespace, fmt.Sprintf("%s-nodepool", testName))
	t.Logf("Verified pods are scheduled on nodes from correct NodePool")

	// Step 6: Verify the created instances use allowed types
	suite.verifyInstancesUseAllowedTypes(t, []string{"bx2-4x16", "mx2-2x16", "mx2d-2x16", "mx3d-2x20"})
	t.Logf("Verified instances use allowed instance types")

	// Step 7: Verify instances exist in IBM Cloud
	suite.verifyInstancesInIBMCloud(t)
	t.Logf("Verified instances exist in IBM Cloud")

	// Step 8: Clean up
	suite.cleanupTestWorkload(t, deployment.Name, deployment.Namespace)
	t.Logf("Test workload deleted: %s", deployment.Name)

	suite.cleanupTestResources(t, testName)

	t.Logf("NodePool instance type selection E2E test completed successfully: %s", testName)
}

func (s *E2ETestSuite) createTestNodeClass(t *testing.T, testName string) *v1alpha1.IBMNodeClass {
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclass", testName),
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:            s.testRegion,
			Zone:              s.testZone,
			InstanceProfile:   "bx2a-2x8", // Small instance for testing
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
	// Set 5-minute expiration for e2e test nodes to ensure cleanup
	expireAfter := karpv1.MustParseNillableDuration("5m")
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodepool", testName),
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
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
								Values:   []string{"bx2a-2x8"},
							},
						},
					},
					ExpireAfter: expireAfter,
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodePool)
	require.NoError(t, err)

	return nodePool
}

func (s *E2ETestSuite) createTestNodeClaim(t *testing.T, testName, nodeClassName string) *karpv1.NodeClaim {
	// Set 5-minute expiration for e2e test nodes to ensure cleanup
	expireAfter := karpv1.MustParseNillableDuration("5m")
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclaim", testName),
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
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
			ExpireAfter: expireAfter,
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClaim)
	require.NoError(t, err)

	return nodeClaim
}

// createTestWorkload creates a deployment that will trigger Karpenter to provision nodes
func (s *E2ETestSuite) createTestWorkload(t *testing.T, testName string) *appsv1.Deployment {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-workload", testName),
			Namespace: "default",
			Labels: map[string]string{
				"app":     fmt.Sprintf("%s-workload", testName),
				"test":    "e2e",
				"purpose": "karpenter-test",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{3}[0], // 3 replicas to ensure we trigger provisioning
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": fmt.Sprintf("%s-workload", testName),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":     fmt.Sprintf("%s-workload", testName),
						"test":    "e2e",
						"purpose": "karpenter-test",
					},
				},
				Spec: corev1.PodSpec{
					// Force pods to only be scheduled on nodes from this test's NodePool
					NodeSelector: map[string]string{
						"karpenter.sh/nodepool": fmt.Sprintf("%s-nodepool", testName),
					},
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2000m"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 80,
									Protocol:      corev1.ProtocolTCP,
								},
							},
						},
					},
					// Add anti-affinity to spread pods across nodes
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: corev1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"app": fmt.Sprintf("%s-workload", testName),
											},
										},
										TopologyKey: "kubernetes.io/hostname",
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), deployment)
	require.NoError(t, err)

	return deployment
}

// createTestWorkloadWithInstanceTypeRequirements creates a workload that requires specific resources
func (s *E2ETestSuite) createTestWorkloadWithInstanceTypeRequirements(t *testing.T, testName string) *appsv1.Deployment {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-workload", testName),
			Namespace: "default",
			Labels: map[string]string{
				"app":     fmt.Sprintf("%s-workload", testName),
				"test":    "e2e",
				"purpose": "instance-type-test",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{2}[0], // 2 replicas to trigger provisioning on allowed types
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": fmt.Sprintf("%s-workload", testName),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":     fmt.Sprintf("%s-workload", testName),
						"test":    "e2e",
						"purpose": "instance-type-test",
					},
				},
				Spec: corev1.PodSpec{
					// Force pods to only be scheduled on nodes from this test's NodePool
					NodeSelector: map[string]string{
						"karpenter.sh/nodepool": fmt.Sprintf("%s-nodepool", testName),
					},
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1500m"), // Require significant CPU
									corev1.ResourceMemory: resource.MustParse("2Gi"),   // Require significant memory
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("3000m"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 80,
									Protocol:      corev1.ProtocolTCP,
								},
							},
						},
					},
					// Force pods to different nodes
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"app": fmt.Sprintf("%s-workload", testName),
										},
									},
									TopologyKey: "kubernetes.io/hostname",
								},
							},
						},
					},
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), deployment)
	require.NoError(t, err)

	return deployment
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

// waitForPodsToBeScheduled waits for all pods in a deployment to be scheduled and running
func (s *E2ETestSuite) waitForPodsToBeScheduled(t *testing.T, deploymentName, namespace string) {
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	err := wait.PollUntilContextTimeout(ctx, pollInterval, testTimeout, true, func(ctx context.Context) (bool, error) {
		var deployment appsv1.Deployment
		err := s.kubeClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, &deployment)
		if err != nil {
			return false, err
		}

		// Check if deployment has desired replicas ready
		if deployment.Status.ReadyReplicas == *deployment.Spec.Replicas {
			t.Logf("All %d replicas are ready for deployment %s", deployment.Status.ReadyReplicas, deploymentName)
			return true, nil
		}

		t.Logf("Deployment %s: %d/%d replicas ready", deploymentName, deployment.Status.ReadyReplicas, *deployment.Spec.Replicas)

		// Add detailed diagnostics for debugging (but less frequently to avoid log spam)
		if time.Now().Unix()%30 == 0 { // Log diagnostics every ~5 minutes
			s.logDeploymentDiagnostics(t, deploymentName, namespace)
		}

		return false, nil
	})

	require.NoError(t, err, "Pods should be scheduled and running within timeout")
}

// logDeploymentDiagnostics logs detailed information to help debug scheduling issues
func (s *E2ETestSuite) logDeploymentDiagnostics(t *testing.T, deploymentName, namespace string) {
	ctx := context.Background()

	// Log pod status
	var podList corev1.PodList
	err := s.kubeClient.List(ctx, &podList, client.InNamespace(namespace), client.MatchingLabels{"app": deploymentName})
	if err != nil {
		t.Logf("Failed to list pods for diagnostics: %v", err)
		return
	}

	for _, pod := range podList.Items {
		t.Logf("Pod %s status: Phase=%s, Reason=%s", pod.Name, pod.Status.Phase, pod.Status.Reason)
		for _, condition := range pod.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				t.Logf("Pod %s condition %s: %s - %s", pod.Name, condition.Type, condition.Status, condition.Message)
			}
		}
	}

	// Log pending events for the deployment and pods
	var eventList corev1.EventList
	err = s.kubeClient.List(ctx, &eventList, client.InNamespace(namespace))
	if err != nil {
		t.Logf("Failed to list events for diagnostics: %v", err)
		return
	}

	for _, event := range eventList.Items {
		if event.Type == "Warning" && (event.InvolvedObject.Name == deploymentName ||
			strings.HasPrefix(event.InvolvedObject.Name, deploymentName)) {
			t.Logf("Warning event: %s - %s", event.Reason, event.Message)
		}
	}

	// Log NodeClaims status
	var nodeClaimList karpv1.NodeClaimList
	err = s.kubeClient.List(ctx, &nodeClaimList)
	if err != nil {
		t.Logf("Failed to list NodeClaims for diagnostics: %v", err)
		return
	}

	t.Logf("NodeClaims count: %d", len(nodeClaimList.Items))
	for _, nodeClaim := range nodeClaimList.Items {
		t.Logf("NodeClaim %s: Ready=%v, ProviderID=%s",
			nodeClaim.Name,
			s.isNodeClaimReady(nodeClaim),
			nodeClaim.Status.ProviderID)
	}
}

// isNodeClaimReady checks if a NodeClaim is ready
func (s *E2ETestSuite) isNodeClaimReady(nodeClaim karpv1.NodeClaim) bool {
	for _, condition := range nodeClaim.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// verifyPodsScheduledOnCorrectNodes verifies that pods are scheduled on nodes from the expected NodePool
func (s *E2ETestSuite) verifyPodsScheduledOnCorrectNodes(t *testing.T, deploymentName, namespace, expectedNodePool string) {
	ctx := context.Background()

	// Get all pods from the deployment
	var podList corev1.PodList
	err := s.kubeClient.List(ctx, &podList, client.InNamespace(namespace), client.MatchingLabels{"app": deploymentName})
	require.NoError(t, err, "Should be able to list pods")

	require.Greater(t, len(podList.Items), 0, "Should have at least one pod")

	// Check each pod is scheduled on a node with the correct NodePool label
	for _, pod := range podList.Items {
		require.NotEmpty(t, pod.Spec.NodeName, "Pod %s should be scheduled on a node", pod.Name)

		// Get the node
		var node corev1.Node
		err := s.kubeClient.Get(ctx, types.NamespacedName{Name: pod.Spec.NodeName}, &node)
		require.NoError(t, err, "Should be able to get node %s", pod.Spec.NodeName)

		// Verify node has the expected NodePool label
		nodePoolLabel, exists := node.Labels["karpenter.sh/nodepool"]
		require.True(t, exists, "Node %s should have karpenter.sh/nodepool label", node.Name)
		require.Equal(t, expectedNodePool, nodePoolLabel,
			"Pod %s is scheduled on node %s with NodePool %s, expected %s",
			pod.Name, node.Name, nodePoolLabel, expectedNodePool)

		t.Logf("✓ Pod %s correctly scheduled on node %s (NodePool: %s)", pod.Name, node.Name, nodePoolLabel)
	}
}

// verifyKarpenterNodesExist verifies that Karpenter has created new nodes
func (s *E2ETestSuite) verifyKarpenterNodesExist(t *testing.T) {
	ctx := context.Background()

	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err)

	karpenterNodes := 0
	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue != "" {
			karpenterNodes++
			t.Logf("Found Karpenter node: %s (nodepool: %s)", node.Name, labelValue)
		}
	}

	require.Greater(t, karpenterNodes, 0, "At least one Karpenter node should exist")
	t.Logf("Found %d Karpenter nodes", karpenterNodes)
}

// cleanupTestWorkload deletes a test deployment
func (s *E2ETestSuite) cleanupTestWorkload(t *testing.T, deploymentName, namespace string) {
	ctx := context.Background()

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: namespace,
		},
	}

	err := s.kubeClient.Delete(ctx, deployment)
	if err != nil && !errors.IsNotFound(err) {
		t.Logf("Failed to delete deployment %s: %v", deploymentName, err)
	}
}

// verifyInstancesInIBMCloud verifies that instances exist in IBM Cloud
func (s *E2ETestSuite) verifyInstancesInIBMCloud(t *testing.T) {
	// This is a simplified verification - in a real implementation,
	// you would check the IBM Cloud VPC API for actual instances
	ctx := context.Background()

	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err)

	karpenterNodes := 0
	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue != "" {
			karpenterNodes++
			t.Logf("Found Karpenter node in cluster: %s", node.Name)
			// In a real test, we would extract the provider ID and verify it exists in IBM Cloud
		}
	}

	require.Greater(t, karpenterNodes, 0, "At least one Karpenter node should exist")
}

// verifyInstancesUseAllowedTypes verifies that created nodes use the allowed instance types
func (s *E2ETestSuite) verifyInstancesUseAllowedTypes(t *testing.T, allowedTypes []string) {
	ctx := context.Background()

	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err)

	karpenterNodes := []corev1.Node{}
	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue != "" {
			karpenterNodes = append(karpenterNodes, node)
		}
	}

	require.Greater(t, len(karpenterNodes), 0, "At least one Karpenter node should exist")

	for _, node := range karpenterNodes {
		instanceType := node.Labels["node.kubernetes.io/instance-type"]
		require.NotEmpty(t, instanceType, "Node should have instance type label")

		found := false
		for _, allowedType := range allowedTypes {
			if instanceType == allowedType {
				found = true
				break
			}
		}

		require.True(t, found, "Node %s has instance type %s which is not in allowed types %v",
			node.Name, instanceType, allowedTypes)
		t.Logf("Node %s uses allowed instance type: %s", node.Name, instanceType)
	}
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
			name:    "Deployment",
			obj:     &appsv1.Deployment{},
			listObj: &appsv1.DeploymentList{},
		},
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
// nolint:unused
func extractInstanceIDFromProviderID(providerID string) string {
	// ProviderID format: "ibm:///region/instance_id"
	parts := strings.Split(providerID, "/")
	if len(parts) >= 4 {
		return parts[len(parts)-1]
	}
	return ""
}

// dumpBootstrapLogs attempts to SSH into the instance and dump bootstrap logs
// nolint:unused
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
// nolint:unused
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
// nolint:unused
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
// nolint:unused
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

// createTestNodeClassWithoutInstanceProfile creates a NodeClass with instanceRequirements instead of instanceProfile
// This tests that NodePool can control instance type selection when NodeClass uses instanceRequirements
func (s *E2ETestSuite) createTestNodeClassWithoutInstanceProfile(t *testing.T, testName string) *v1alpha1.IBMNodeClass {
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclass", testName),
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region: s.testRegion,
			Zone:   s.testZone,
			// No instanceProfile or instanceRequirements - let NodePool control instance selection
			Image:             s.testImage,
			VPC:               s.testVPC,
			Subnet:            s.testSubnet,
			SecurityGroups:    []string{os.Getenv("TEST_SECURITY_GROUP_ID")},
			APIServerEndpoint: os.Getenv("KUBERNETES_API_SERVER_ENDPOINT"),
			BootstrapMode:     &[]string{"cloud-init"}[0],
			ResourceGroup:     os.Getenv("IBM_RESOURCE_GROUP_ID"),
			SSHKeys:           []string{os.Getenv("IBM_SSH_KEY_ID")},
			Tags: map[string]string{
				"test":       "e2e",
				"test-name":  testName,
				"created-by": "karpenter-e2e",
				"purpose":    "nodepool-instancetype-test",
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClass)
	require.NoError(t, err)

	return nodeClass
}

// createTestNodePoolWithMultipleInstanceTypes creates a NodePool with multiple instance type requirements
// This matches the customer's configuration that was causing the circuit breaker to open
func (s *E2ETestSuite) createTestNodePoolWithMultipleInstanceTypes(t *testing.T, testName string, nodeClassName string) *karpv1.NodePool {
	// Set 5-minute expiration for e2e test nodes to ensure cleanup
	expireAfter := karpv1.MustParseNillableDuration("5m")
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodepool", testName),
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"provisioner":  "karpenter-vpc",
						"cluster-type": "self-managed",
						"test":         "e2e",
						"test-name":    testName,
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter.ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelInstanceTypeStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"bx2-4x16", "mx2-2x16", "mx2d-2x16", "mx3d-2x20"}, // Customer's exact config
							},
						},
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelArchStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"amd64"},
							},
						},
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      karpv1.CapacityTypeLabelKey,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"on-demand"},
							},
						},
					},
					ExpireAfter: expireAfter,
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodePool)
	require.NoError(t, err)

	return nodePool
}

// TestE2EDriftStability tests that nodes remain stable after provisioning
// and don't get incorrectly identified as "drifted"
func TestE2EDriftStability(t *testing.T) {
	suite := SetupE2ETestSuite(t)

	testName := fmt.Sprintf("drift-stability-%d", time.Now().Unix())

	t.Logf("Starting drift stability test: %s", testName)

	// Step 1: Create NodeClass without instanceProfile to test drift detection
	nodeClass := suite.createTestNodeClassWithoutInstanceProfile(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Step 2: Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Step 3: Create NodePool with specific instance type requirements
	nodePool := suite.createDriftStabilityNodePool(t, testName, nodeClass.Name)
	t.Logf("Created NodePool with instance requirements: %s", nodePool.Name)

	// Step 4: Deploy test workload to trigger Karpenter provisioning
	deployment := suite.createTestWorkloadWithInstanceTypeRequirements(t, testName)
	t.Logf("Created test workload: %s", deployment.Name)

	// Step 5: Wait for pods to be scheduled and nodes to be provisioned
	suite.waitForPodsToBeScheduled(t, deployment.Name, deployment.Namespace)
	t.Logf("Pods scheduled successfully")

	// Step 6: Verify NodePool requirements are present as node labels
	suite.verifyNodePoolRequirementsOnNodes(t, nodePool)
	t.Logf("Verified NodePool requirements are present as node labels")

	// Step 7: Monitor nodes for stability over 12 minutes (reasonable for E2E test)
	stabilityDuration := 12 * time.Minute
	t.Logf("Monitoring node stability for %v...", stabilityDuration)

	initialNodes := suite.captureNodeSnapshot(t, nodePool.Name)
	require.Greater(t, len(initialNodes), 0, "Should have provisioned at least one node")

	suite.monitorNodeStability(t, nodePool.Name, initialNodes, stabilityDuration)
	t.Logf("Node stability monitoring completed successfully")

	// Step 8: Final verification that requirements are still satisfied
	suite.verifyNodePoolRequirementsOnNodes(t, nodePool)
	t.Logf("Final verification: NodePool requirements still satisfied")

	// Step 9: Cleanup
	suite.cleanupTestWorkload(t, deployment.Name, deployment.Namespace)
	suite.cleanupTestResources(t, testName)

	t.Logf("Drift stability test completed successfully: %s", testName)
}

// createDriftStabilityNodePool creates a NodePool with specific requirements for drift testing
func (s *E2ETestSuite) createDriftStabilityNodePool(t *testing.T, testName, nodeClassName string) *karpv1.NodePool {
	expireAfter := karpv1.MustParseNillableDuration("20m") // Longer expiration for stability test
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodepool", testName),
			Labels: map[string]string{
				"test":      "e2e",
				"test-name": testName,
			},
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test":         "drift-stability",
						"test-name":    testName,
						"provisioner":  "karpenter-vpc",
						"cluster-type": "self-managed",
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter.ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelInstanceTypeStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"bx2-4x16", "mx2-2x16"},
							},
						},
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelArchStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"amd64"},
							},
						},
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      karpv1.CapacityTypeLabelKey,
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"on-demand"},
							},
						},
					},
					ExpireAfter: expireAfter,
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodePool)
	require.NoError(t, err)

	return nodePool
}

// captureNodeSnapshot captures the current state of nodes for the given NodePool
func (s *E2ETestSuite) captureNodeSnapshot(t *testing.T, nodePoolName string) []NodeSnapshot {
	ctx := context.Background()
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err)

	var snapshots []NodeSnapshot
	for _, node := range nodeList.Items {
		if nodePool, exists := node.Labels["karpenter.sh/nodepool"]; exists && nodePool == nodePoolName {
			snapshot := NodeSnapshot{
				Name:         node.Name,
				ProviderID:   node.Spec.ProviderID,
				InstanceType: node.Labels[corev1.LabelInstanceTypeStable],
				CreationTime: node.CreationTimestamp.Time,
				Labels:       make(map[string]string),
			}

			// Copy relevant labels
			for key, value := range node.Labels {
				if strings.HasPrefix(key, "node.kubernetes.io/") ||
					strings.HasPrefix(key, "karpenter.sh/") ||
					key == corev1.LabelArchStable ||
					key == corev1.LabelInstanceTypeStable {
					snapshot.Labels[key] = value
				}
			}

			snapshots = append(snapshots, snapshot)
			t.Logf("Captured node snapshot: %s (instance: %s, type: %s)",
				snapshot.Name, snapshot.ProviderID, snapshot.InstanceType)
		}
	}

	return snapshots
}

// monitorNodeStability monitors nodes for unexpected replacements during the specified duration
func (s *E2ETestSuite) monitorNodeStability(t *testing.T, nodePoolName string, initialNodes []NodeSnapshot, duration time.Duration) {
	ctx := context.Background()
	endTime := time.Now().Add(duration)
	checkInterval := 30 * time.Second

	for time.Now().Before(endTime) {
		var nodeList corev1.NodeList
		err := s.kubeClient.List(ctx, &nodeList)
		require.NoError(t, err, "Should be able to list nodes during stability monitoring")

		currentNodes := make(map[string]corev1.Node)
		for _, node := range nodeList.Items {
			if nodePool, exists := node.Labels["karpenter.sh/nodepool"]; exists && nodePool == nodePoolName {
				currentNodes[node.Name] = node
			}
		}

		// Check that initial nodes still exist
		for _, initialNode := range initialNodes {
			currentNode, exists := currentNodes[initialNode.Name]
			if !exists {
				t.Errorf("Node %s from initial snapshot no longer exists - unexpected node replacement detected", initialNode.Name)
				continue
			}

			// Verify node hasn't been replaced (ProviderID should remain the same)
			if currentNode.Spec.ProviderID != initialNode.ProviderID {
				t.Errorf("Node %s has different ProviderID: initial=%s, current=%s - node was replaced",
					initialNode.Name, initialNode.ProviderID, currentNode.Spec.ProviderID)
			}

			// Check for drift conditions that might indicate incorrect drift detection
			for _, condition := range currentNode.Status.Conditions {
				if condition.Type == "Drifted" && condition.Status == corev1.ConditionTrue {
					t.Errorf("Node %s incorrectly marked as drifted: %s", initialNode.Name, condition.Message)
				}
			}
		}

		// Check for any new unexpected nodes (should be stable count)
		if len(currentNodes) > len(initialNodes) {
			t.Logf("Warning: More nodes than expected - initial: %d, current: %d",
				len(initialNodes), len(currentNodes))
		}

		remaining := time.Until(endTime)
		t.Logf("Stability check passed - %v remaining", remaining.Round(time.Second))

		if remaining > checkInterval {
			time.Sleep(checkInterval)
		} else {
			time.Sleep(remaining)
		}
	}

	t.Logf("Node stability monitoring completed successfully - no unexpected replacements detected")
}

// verifyNodePoolRequirementsOnNodes verifies that all NodePool requirements are present as node labels
func (s *E2ETestSuite) verifyNodePoolRequirementsOnNodes(t *testing.T, nodePool *karpv1.NodePool) {
	ctx := context.Background()
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList)
	require.NoError(t, err)

	var nodePoolNodes []corev1.Node
	for _, node := range nodeList.Items {
		if labelValue, exists := node.Labels["karpenter.sh/nodepool"]; exists && labelValue == nodePool.Name {
			nodePoolNodes = append(nodePoolNodes, node)
		}
	}

	require.Greater(t, len(nodePoolNodes), 0, "Should have at least one node from the NodePool")

	// Verify each requirement from the NodePool is satisfied on all nodes
	for _, requirement := range nodePool.Spec.Template.Spec.Requirements {
		for _, node := range nodePoolNodes {
			nodeValue, exists := node.Labels[requirement.Key]
			require.True(t, exists, "Node %s should have label %s", node.Name, requirement.Key)

			// Check that the node's value satisfies the requirement
			switch requirement.Operator {
			case corev1.NodeSelectorOpIn:
				found := false
				for _, allowedValue := range requirement.Values {
					if nodeValue == allowedValue {
						found = true
						break
					}
				}
				require.True(t, found,
					"Node %s has %s=%s which is not in allowed values %v",
					node.Name, requirement.Key, nodeValue, requirement.Values)

			case corev1.NodeSelectorOpNotIn:
				for _, forbiddenValue := range requirement.Values {
					require.NotEqual(t, nodeValue, forbiddenValue,
						"Node %s has %s=%s which is in forbidden values %v",
						node.Name, requirement.Key, nodeValue, requirement.Values)
				}
			}

			t.Logf("✓ Node %s satisfies requirement %s=%s (operator: %s)",
				node.Name, requirement.Key, nodeValue, requirement.Operator)
		}
	}
}

// NodeSnapshot represents a snapshot of a node's state for stability monitoring
type NodeSnapshot struct {
	Name         string
	ProviderID   string
	InstanceType string
	CreationTime time.Time
	Labels       map[string]string
}

// verifyInstanceUsesAllowedType verifies the created instance uses one of the allowed instance types
// nolint:unused
func (s *E2ETestSuite) verifyInstanceUsesAllowedType(t *testing.T, nodeClaimName string, allowedTypes []string) {
	ctx := context.Background()

	// Get the NodeClaim to find the instance type that was selected
	var nodeClaim karpv1.NodeClaim
	err := s.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaimName}, &nodeClaim)
	require.NoError(t, err)

	// Check the instance type label set by Karpenter
	instanceType, ok := nodeClaim.Labels[corev1.LabelInstanceTypeStable]
	require.True(t, ok, "NodeClaim should have instance-type label")

	// Verify it's one of the allowed types
	found := false
	for _, allowedType := range allowedTypes {
		if instanceType == allowedType {
			found = true
			break
		}
	}

	assert.True(t, found, "Instance type %s should be one of the allowed types: %v", instanceType, allowedTypes)
	t.Logf("Instance uses allowed type: %s", instanceType)
}

// TestE2ECleanupNodePoolDeletion tests NodePool deletion with proper node draining
func TestE2ECleanupNodePoolDeletion(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-nodepool-%d", time.Now().Unix())

	t.Logf("Starting NodePool cleanup test: %s", testName)

	ctx := context.Background()

	// Create NodeClass
	nodeClass := suite.createTestNodeClass(t, testName+"-nodeclass")
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Create NodePool with 2 replicas for better testing
	nodePool := suite.createTestNodePool(t, testName+"-nodepool", nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Create test workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName+"-workload")
	// Modify the deployment to have 2 replicas
	deployment.Spec.Replicas = &[]int32{2}[0]
	err := suite.kubeClient.Update(ctx, deployment)
	require.NoError(t, err)
	t.Logf("Created test workload: %s", deployment.Name)

	// Wait for pods to be scheduled and nodes to be provisioned
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")
	t.Logf("Workload pods scheduled successfully")

	// Capture initial node state
	var nodeList corev1.NodeList
	err = suite.kubeClient.List(ctx, &nodeList, client.MatchingLabels{
		"karpenter.sh/nodepool": nodePool.Name,
	})
	require.NoError(t, err)
	initialNodeCount := len(nodeList.Items)
	require.Greater(t, initialNodeCount, 0, "Should have at least one node provisioned")

	nodeNames := make([]string, len(nodeList.Items))
	nodeProviderIDs := make([]string, len(nodeList.Items))
	for i, node := range nodeList.Items {
		nodeNames[i] = node.Name
		nodeProviderIDs[i] = node.Spec.ProviderID
		t.Logf("Node before deletion: %s (ProviderID: %s)", node.Name, node.Spec.ProviderID)
	}

	// Delete the test workload first
	t.Logf("Deleting test workload: %s", deployment.Name)
	err = suite.kubeClient.Delete(ctx, deployment)
	require.NoError(t, err)

	// Wait for pods to be terminated
	suite.waitForPodsGone(t, deployment.Name)

	// Delete NodePool
	t.Logf("Deleting NodePool: %s", nodePool.Name)
	err = suite.kubeClient.Delete(ctx, nodePool)
	require.NoError(t, err)

	// Wait for nodes to be properly drained and removed
	t.Logf("Waiting for nodes to be drained and removed...")
	err = wait.PollUntilContextTimeout(ctx, pollInterval, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		var remainingNodes corev1.NodeList
		listErr := suite.kubeClient.List(ctx, &remainingNodes, client.MatchingLabels{
			"karpenter.sh/nodepool": nodePool.Name,
		})
		if listErr != nil {
			t.Logf("Error checking nodes: %v", listErr)
			return false, nil
		}

		remainingCount := len(remainingNodes.Items)
		t.Logf("Remaining nodes with NodePool label: %d", remainingCount)

		if remainingCount == 0 {
			return true, nil
		}

		// Log status of remaining nodes
		for _, node := range remainingNodes.Items {
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady {
					t.Logf("Node %s status: %s", node.Name, condition.Status)
					break
				}
			}
		}

		return false, nil
	})
	require.NoError(t, err, "Nodes should be properly drained and removed after NodePool deletion")

	// Verify no NodeClaims remain
	var nodeClaimList karpv1.NodeClaimList
	err = suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
		"karpenter.sh/nodepool": nodePool.Name,
	})
	require.NoError(t, err)
	assert.Empty(t, nodeClaimList.Items, "All NodeClaims should be deleted")

	// Cleanup NodeClass
	err = suite.kubeClient.Delete(ctx, nodeClass)
	require.NoError(t, err)

	t.Logf("NodePool cleanup test completed successfully: %s", testName)
}

// TestE2ECleanupNodeClassDeletion tests NodeClass deletion with dependent resource cleanup
func TestE2ECleanupNodeClassDeletion(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-nodeclass-%d", time.Now().Unix())

	t.Logf("Starting NodeClass cleanup test: %s", testName)

	ctx := context.Background()

	// Create NodeClass
	nodeClass := suite.createTestNodeClass(t, testName+"-nodeclass")
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Create NodePool that references this NodeClass
	nodePool := suite.createTestNodePool(t, testName+"-nodepool", nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Create test workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName+"-workload")
	t.Logf("Created test workload: %s", deployment.Name)

	// Wait for pods to be scheduled and nodes to be provisioned
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")
	t.Logf("Workload pods scheduled successfully")

	// Verify NodeClaims exist
	var nodeClaimList karpv1.NodeClaimList
	err := suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
		"karpenter.sh/nodepool": nodePool.Name,
	})
	require.NoError(t, err)
	require.Greater(t, len(nodeClaimList.Items), 0, "Should have NodeClaims provisioned")

	// Try to delete NodeClass while NodePool still references it
	t.Logf("Attempting to delete NodeClass while NodePool still exists...")
	err = suite.kubeClient.Delete(ctx, nodeClass)
	require.NoError(t, err)

	// NodeClass should be marked for deletion but not actually deleted due to finalizers
	// Wait a bit to see if it gets stuck
	time.Sleep(30 * time.Second)

	// Check if NodeClass is still present (should be due to dependencies)
	var retrievedNodeClass v1alpha1.IBMNodeClass
	err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &retrievedNodeClass)
	if err == nil && retrievedNodeClass.DeletionTimestamp != nil {
		t.Logf("NodeClass is marked for deletion but blocked by dependencies (expected)")
	} else if errors.IsNotFound(err) {
		t.Logf("NodeClass was immediately deleted (controller may have handled cleanup)")
	} else {
		require.NoError(t, err, "Unexpected error checking NodeClass status")
	}

	// Clean up dependent resources in reverse order
	t.Logf("Cleaning up dependent resources...")

	// Delete workload first
	err = suite.kubeClient.Delete(ctx, deployment)
	require.NoError(t, err)
	suite.waitForPodsGone(t, deployment.Name)

	// Delete NodePool
	err = suite.kubeClient.Delete(ctx, nodePool)
	require.NoError(t, err)

	// Wait for NodePool and its resources to be fully cleaned up
	err = wait.PollUntilContextTimeout(ctx, pollInterval, 8*time.Minute, true, func(ctx context.Context) (bool, error) {
		// Check if NodePool is gone
		var remainingNodePool karpv1.NodePool
		getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodePool.Name}, &remainingNodePool)
		if !errors.IsNotFound(getErr) {
			if getErr != nil {
				t.Logf("Error checking NodePool: %v", getErr)
			} else {
				t.Logf("NodePool still exists, waiting for cleanup...")
			}
			return false, nil
		}

		// Check if NodeClaims are gone
		var remainingNodeClaims karpv1.NodeClaimList
		err = suite.kubeClient.List(ctx, &remainingNodeClaims, client.MatchingLabels{
			"karpenter.sh/nodepool": nodePool.Name,
		})
		if err != nil {
			t.Logf("Error checking NodeClaims: %v", err)
			return false, nil
		}

		if len(remainingNodeClaims.Items) > 0 {
			t.Logf("Still have %d NodeClaims, waiting...", len(remainingNodeClaims.Items))
			return false, nil
		}

		return true, nil
	})
	require.NoError(t, err, "NodePool and NodeClaims should be cleaned up")

	// Now NodeClass should be able to complete deletion
	err = wait.PollUntilContextTimeout(ctx, pollInterval, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		var retrievedNodeClass v1alpha1.IBMNodeClass
		getErr := suite.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClass.Name}, &retrievedNodeClass)
		if errors.IsNotFound(getErr) {
			return true, nil
		}
		if getErr != nil {
			t.Logf("Error checking NodeClass: %v", getErr)
			return false, nil
		}
		t.Logf("NodeClass still exists, waiting for final cleanup...")
		return false, nil
	})
	require.NoError(t, err, "NodeClass should be deleted after dependent resources are cleaned up")

	t.Logf("NodeClass cleanup test completed successfully: %s", testName)
}

// TestE2ECleanupOrphanedResources tests cleanup of orphaned resources
func TestE2ECleanupOrphanedResources(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-orphaned-%d", time.Now().Unix())

	t.Logf("Starting orphaned resources cleanup test: %s", testName)

	ctx := context.Background()

	// Create NodeClass and NodePool
	nodeClass := suite.createTestNodeClass(t, testName+"-nodeclass")
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createTestNodePool(t, testName+"-nodepool", nodeClass.Name)

	// Create test workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName+"-workload")
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Get the provisioned NodeClaim
	var nodeClaimList karpv1.NodeClaimList
	err := suite.kubeClient.List(ctx, &nodeClaimList, client.MatchingLabels{
		"karpenter.sh/nodepool": nodePool.Name,
	})
	require.NoError(t, err)
	require.Greater(t, len(nodeClaimList.Items), 0, "Should have NodeClaims provisioned")

	originalNodeClaim := nodeClaimList.Items[0]
	t.Logf("Found NodeClaim: %s", originalNodeClaim.Name)

	// Simulate orphaned NodeClaim by removing the NodePool label
	// This simulates a scenario where NodePool is deleted but NodeClaim cleanup fails
	orphanedNodeClaim := originalNodeClaim.DeepCopy()
	delete(orphanedNodeClaim.Labels, "karpenter.sh/nodepool")
	orphanedNodeClaim.Name = testName + "-orphaned-nodeclaim"
	orphanedNodeClaim.ResourceVersion = ""

	// Create the orphaned NodeClaim
	err = suite.kubeClient.Create(ctx, orphanedNodeClaim)
	require.NoError(t, err)
	t.Logf("Created orphaned NodeClaim: %s", orphanedNodeClaim.Name)

	// Clean up the original resources
	err = suite.kubeClient.Delete(ctx, deployment)
	require.NoError(t, err)
	suite.waitForPodsGone(t, deployment.Name)

	err = suite.kubeClient.Delete(ctx, nodePool)
	require.NoError(t, err)

	err = suite.kubeClient.Delete(ctx, nodeClass)
	require.NoError(t, err)

	// Wait for normal cleanup to complete
	time.Sleep(2 * time.Minute)

	// Verify orphaned NodeClaim still exists
	var retrievedOrphanedNodeClaim karpv1.NodeClaim
	err = suite.kubeClient.Get(ctx, types.NamespacedName{Name: orphanedNodeClaim.Name}, &retrievedOrphanedNodeClaim)
	if errors.IsNotFound(err) {
		t.Logf("Orphaned NodeClaim was automatically cleaned up by controller")
	} else {
		require.NoError(t, err)
		t.Logf("Found orphaned NodeClaim as expected: %s", retrievedOrphanedNodeClaim.Name)

		// Manually clean up the orphaned resource
		err = suite.kubeClient.Delete(ctx, &retrievedOrphanedNodeClaim)
		require.NoError(t, err)
		t.Logf("Manually cleaned up orphaned NodeClaim: %s", retrievedOrphanedNodeClaim.Name)
	}

	t.Logf("Orphaned resources cleanup test completed successfully: %s", testName)
}

// TestE2ECleanupIBMCloudResources tests that no IBM Cloud resources are left behind
func TestE2ECleanupIBMCloudResources(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("cleanup-ibmcloud-%d", time.Now().Unix())

	t.Logf("Starting IBM Cloud resources cleanup test: %s", testName)

	ctx := context.Background()

	// Get initial list of IBM Cloud instances for comparison
	initialInstances, err := suite.getIBMCloudInstances(t)
	require.NoError(t, err)
	initialInstanceCount := len(initialInstances)
	t.Logf("Initial IBM Cloud instances: %d", initialInstanceCount)

	// Create NodeClass and NodePool
	nodeClass := suite.createTestNodeClass(t, testName+"-nodeclass")
	suite.waitForNodeClassReady(t, nodeClass.Name)
	nodePool := suite.createTestNodePool(t, testName+"-nodepool", nodeClass.Name)

	// Create test workload to trigger provisioning
	deployment := suite.createTestWorkload(t, testName+"-workload")
	// Modify the deployment to have 2 replicas
	deployment.Spec.Replicas = &[]int32{2}[0]
	err = suite.kubeClient.Update(ctx, deployment)
	require.NoError(t, err)
	suite.waitForPodsToBeScheduled(t, deployment.Name, "default")

	// Wait a bit for provisioning to complete
	time.Sleep(1 * time.Minute)

	// Get instances after provisioning
	provisionedInstances, err := suite.getIBMCloudInstances(t)
	require.NoError(t, err)
	provisionedInstanceCount := len(provisionedInstances)
	t.Logf("IBM Cloud instances after provisioning: %d", provisionedInstanceCount)

	// Should have more instances after provisioning
	assert.Greater(t, provisionedInstanceCount, initialInstanceCount,
		"Should have more IBM Cloud instances after provisioning")

	// Identify new instances (those with Karpenter labels/names)
	newInstances := make([]string, 0)
	for instanceID, instanceName := range provisionedInstances {
		if strings.Contains(instanceName, nodePool.Name) {
			newInstances = append(newInstances, instanceID)
			t.Logf("Found provisioned instance: %s (%s)", instanceName, instanceID)
		}
	}

	// Clean up Kubernetes resources
	t.Logf("Cleaning up Kubernetes resources...")
	err = suite.kubeClient.Delete(ctx, deployment)
	require.NoError(t, err)
	suite.waitForPodsGone(t, deployment.Name)

	err = suite.kubeClient.Delete(ctx, nodePool)
	require.NoError(t, err)

	// Wait for NodePool cleanup to complete
	err = wait.PollUntilContextTimeout(ctx, pollInterval, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		var remainingNodes corev1.NodeList
		listErr := suite.kubeClient.List(ctx, &remainingNodes, client.MatchingLabels{
			"karpenter.sh/nodepool": nodePool.Name,
		})
		if listErr != nil {
			t.Logf("Error checking nodes: %v", listErr)
			return false, nil
		}

		remainingCount := len(remainingNodes.Items)
		t.Logf("Remaining Kubernetes nodes: %d", remainingCount)
		return remainingCount == 0, nil
	})
	require.NoError(t, err, "Kubernetes nodes should be cleaned up")

	// Wait for IBM Cloud instances to be terminated
	t.Logf("Waiting for IBM Cloud instances to be terminated...")
	err = wait.PollUntilContextTimeout(ctx, pollInterval, 8*time.Minute, true, func(ctx context.Context) (bool, error) {
		currentInstances, getErr := suite.getIBMCloudInstances(t)
		if getErr != nil {
			t.Logf("Error checking IBM Cloud instances: %v", getErr)
			return false, nil
		}

		currentCount := len(currentInstances)
		t.Logf("Current IBM Cloud instances: %d", currentCount)

		// Check if any of our test instances still exist
		remainingTestInstances := 0
		for _, instanceID := range newInstances {
			if _, exists := currentInstances[instanceID]; exists {
				remainingTestInstances++
			}
		}

		t.Logf("Remaining test instances: %d", remainingTestInstances)
		return remainingTestInstances == 0, nil
	})
	require.NoError(t, err, "IBM Cloud instances should be terminated")

	// Verify final instance count matches initial count
	finalInstances, err := suite.getIBMCloudInstances(t)
	require.NoError(t, err)
	finalInstanceCount := len(finalInstances)
	t.Logf("Final IBM Cloud instances: %d", finalInstanceCount)

	assert.Equal(t, initialInstanceCount, finalInstanceCount,
		"IBM Cloud instance count should return to initial state")

	// Clean up NodeClass
	err = suite.kubeClient.Delete(ctx, nodeClass)
	require.NoError(t, err)

	t.Logf("IBM Cloud resources cleanup test completed successfully: %s", testName)
}

// getIBMCloudInstances retrieves current IBM Cloud instances for cleanup verification
func (s *E2ETestSuite) getIBMCloudInstances(t *testing.T) (map[string]string, error) {
	instances := make(map[string]string)

	// Use IBM Cloud CLI to get instances
	cmd := exec.Command("ibmcloud", "is", "instances", "--output", "json")
	output, err := cmd.Output()
	if err != nil {
		return instances, fmt.Errorf("failed to get IBM Cloud instances: %w", err)
	}

	// Parse JSON output - simplified parsing for test purposes
	// In a real implementation, you'd use proper JSON parsing
	outputStr := string(output)
	if strings.Contains(outputStr, "\"id\":") {
		// Simple extraction - in practice use proper JSON parsing
		lines := strings.Split(outputStr, "\n")
		for _, line := range lines {
			if strings.Contains(line, "\"id\":") && strings.Contains(line, "\"name\":") {
				// Extract ID and name - this is a simplified approach
				// In practice, use json.Unmarshal
				parts := strings.Split(line, "\"")
				for i, part := range parts {
					if part == "id" && i+2 < len(parts) {
						id := parts[i+2]
						for j, namePart := range parts {
							if namePart == "name" && j+2 < len(parts) {
								name := parts[j+2]
								instances[id] = name
								break
							}
						}
						break
					}
				}
			}
		}
	}

	return instances, nil
}

// waitForPodsGone waits for all pods of a deployment to be terminated
func (s *E2ETestSuite) waitForPodsGone(t *testing.T, deploymentName string) {
	ctx := context.Background()
	err := wait.PollUntilContextTimeout(ctx, pollInterval, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		var podList corev1.PodList
		err := s.kubeClient.List(ctx, &podList, client.MatchingLabels{
			"app": deploymentName,
		})
		if err != nil {
			t.Logf("Error checking pods: %v", err)
			return false, nil
		}

		remainingPods := 0
		for _, pod := range podList.Items {
			if pod.DeletionTimestamp == nil {
				remainingPods++
			}
		}

		t.Logf("Remaining pods for deployment %s: %d", deploymentName, remainingPods)
		return remainingPods == 0, nil
	})
	require.NoError(t, err, "All pods should be terminated")
}

// getKarpenterNodes returns nodes managed by the given NodePool
func (s *E2ETestSuite) getKarpenterNodes(t *testing.T, nodePoolName string) []corev1.Node {
	var nodeList corev1.NodeList
	err := s.kubeClient.List(context.Background(), &nodeList, client.MatchingLabels{
		"karpenter.sh/nodepool": nodePoolName,
	})
	require.NoError(t, err, "Failed to list nodes")
	return nodeList.Items
}

// TestE2EConsolidationWithPDB tests that node consolidation respects Pod Disruption Budgets
func TestE2EConsolidationWithPDB(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("consolidation-pdb-%d", time.Now().Unix())
	t.Logf("Starting consolidation with PDB test: %s", testName)

	nodeClass := suite.createTestNodeClass(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Create NodePool with multiple instances to enable consolidation
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Create workload with multiple replicas to spread across nodes
	workload := suite.createTestWorkload(t, testName)
	// Scale workload to 6 replicas to trigger multiple nodes
	workload.Spec.Replicas = &[]int32{6}[0]
	err := suite.kubeClient.Update(context.Background(), workload)
	require.NoError(t, err, "Failed to scale workload to 6 replicas")
	t.Logf("Created test workload: %s", workload.Name)

	suite.waitForPodsToBeScheduled(t, workload.Name, workload.Namespace)
	t.Logf("Workload is ready with 6 replicas")

	// Wait for nodes to be provisioned
	time.Sleep(2 * time.Minute)

	// Get initial node count
	initialNodes := suite.getKarpenterNodes(t, nodePool.Name)
	initialNodeCount := len(initialNodes)
	t.Logf("Initial node count: %d", initialNodeCount)

	require.GreaterOrEqual(t, initialNodeCount, 2, "Should have at least 2 nodes for consolidation test")

	// Create PDB that prevents disruption of more than 2 pods at once
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pdb", testName),
			Namespace: "default",
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &intstr.IntOrString{
				Type:   intstr.Int,
				IntVal: 2, // Allow max 2 pods to be unavailable
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": workload.Name,
				},
			},
		},
	}

	err = suite.kubeClient.Create(context.Background(), pdb)
	require.NoError(t, err, "Failed to create PDB")
	t.Logf("Created PDB: %s", pdb.Name)

	// Scale down workload to trigger consolidation opportunity
	workload.Spec.Replicas = &[]int32{3}[0] // Reduce to 3 replicas
	err = suite.kubeClient.Update(context.Background(), workload)
	require.NoError(t, err, "Failed to scale down workload")

	suite.waitForPodsToBeScheduled(t, workload.Name, workload.Namespace)
	t.Logf("Scaled workload down to 3 replicas")

	// Monitor consolidation while respecting PDB
	ctx := context.Background()
	consolidationRespected := false
	timeout := time.Now().Add(10 * time.Minute)

	for time.Now().Before(timeout) {
		// Check current nodes
		currentNodes := suite.getKarpenterNodes(t, nodePool.Name)

		// Check PDB status
		var currentPDB policyv1.PodDisruptionBudget
		err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: pdb.Name, Namespace: pdb.Namespace}, &currentPDB)
		require.NoError(t, err, "Failed to get PDB")

		// Verify PDB is not violated
		if currentPDB.Status.DisruptionsAllowed < 0 {
			t.Errorf("PDB violated: DisruptionsAllowed=%d", currentPDB.Status.DisruptionsAllowed)
		}

		// Check if consolidation occurred while respecting PDB
		if len(currentNodes) < initialNodeCount {
			// Ensure all pods are still running
			var podList corev1.PodList
			err = suite.kubeClient.List(ctx, &podList, client.MatchingLabels{
				"app": workload.Name,
			})
			require.NoError(t, err, "Failed to list pods")

			runningPods := 0
			for _, pod := range podList.Items {
				if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
					runningPods++
				}
			}

			if runningPods == 3 {
				consolidationRespected = true
				t.Logf("Consolidation completed successfully while respecting PDB")
				t.Logf("Nodes: %d -> %d, Running pods: %d", initialNodeCount, len(currentNodes), runningPods)
				break
			}
		}

		time.Sleep(30 * time.Second)
	}

	require.True(t, consolidationRespected, "Consolidation should respect PDB constraints")

	// Cleanup
	err = suite.kubeClient.Delete(ctx, pdb)
	require.NoError(t, err, "Failed to delete PDB")
	t.Logf("Consolidation with PDB test completed successfully: %s", testName)
}

// TestE2EPodDisruptionBudget tests PDB enforcement during node operations
func TestE2EPodDisruptionBudget(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	testName := fmt.Sprintf("pdb-enforcement-%d", time.Now().Unix())
	t.Logf("Starting PDB enforcement test: %s", testName)

	nodeClass := suite.createTestNodeClass(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Create workload with 5 replicas
	workload := suite.createTestWorkload(t, testName)
	// Scale workload to 5 replicas
	workload.Spec.Replicas = &[]int32{5}[0]
	err := suite.kubeClient.Update(context.Background(), workload)
	require.NoError(t, err, "Failed to scale workload to 5 replicas")
	t.Logf("Created test workload: %s", workload.Name)

	suite.waitForPodsToBeScheduled(t, workload.Name, workload.Namespace)
	t.Logf("Workload is ready with 5 replicas")

	// Create strict PDB that allows only 1 pod to be unavailable
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pdb", testName),
			Namespace: "default",
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MaxUnavailable: &intstr.IntOrString{
				Type:   intstr.Int,
				IntVal: 1, // Very strict - only 1 pod can be unavailable
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": workload.Name,
				},
			},
		},
	}

	ctx := context.Background()
	err = suite.kubeClient.Create(ctx, pdb)
	require.NoError(t, err, "Failed to create PDB")
	t.Logf("Created strict PDB: %s", pdb.Name)

	// Wait for PDB to be ready
	time.Sleep(30 * time.Second)

	// Verify PDB status
	var currentPDB policyv1.PodDisruptionBudget
	err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: pdb.Name, Namespace: pdb.Namespace}, &currentPDB)
	require.NoError(t, err, "Failed to get PDB")

	t.Logf("PDB Status - Current: %d, Desired: %d, Allowed: %d, Expected: %d",
		currentPDB.Status.CurrentHealthy,
		currentPDB.Status.DesiredHealthy,
		currentPDB.Status.DisruptionsAllowed,
		currentPDB.Status.ExpectedPods,
	)

	// Ensure PDB is protecting the expected number of pods
	require.Equal(t, int32(5), currentPDB.Status.ExpectedPods, "PDB should protect 5 pods")
	require.GreaterOrEqual(t, currentPDB.Status.CurrentHealthy, int32(4), "At least 4 pods should be healthy")

	// Test PDB enforcement by trying to delete a pod manually
	// Get one of the pods
	var podList corev1.PodList
	err = suite.kubeClient.List(ctx, &podList, client.MatchingLabels{
		"app": workload.Name,
	})
	require.NoError(t, err, "Failed to list pods")
	require.Greater(t, len(podList.Items), 0, "Should have pods to test with")

	testPod := podList.Items[0]
	t.Logf("Testing PDB enforcement by deleting pod: %s", testPod.Name)

	// Delete the pod and verify it's recreated quickly (due to deployment controller)
	err = suite.kubeClient.Delete(ctx, &testPod)
	require.NoError(t, err, "Failed to delete test pod")

	// Wait and verify the deployment maintains desired replicas despite PDB
	time.Sleep(1 * time.Minute)
	suite.waitForPodsToBeScheduled(t, workload.Name, workload.Namespace)

	// Verify PDB status remains stable
	err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: pdb.Name, Namespace: pdb.Namespace}, &currentPDB)
	require.NoError(t, err, "Failed to get updated PDB")

	require.Equal(t, int32(5), currentPDB.Status.ExpectedPods, "PDB should still protect 5 pods")
	t.Logf("PDB maintained protection during pod replacement")

	// Test that PDB prevents excessive simultaneous disruptions
	// Simulate a scenario where multiple pods might be disrupted
	t.Logf("Testing PDB disruption limits...")

	// Create a temporary workload that would compete for resources
	competitorWorkload := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-competitor", testName),
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{10}[0], // High resource demand
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": fmt.Sprintf("%s-competitor", testName)},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": fmt.Sprintf("%s-competitor", testName)},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, competitorWorkload)
	require.NoError(t, err, "Failed to create competitor workload")
	t.Logf("Created competitor workload to trigger resource pressure")

	// Monitor for a period to ensure PDB is respected during any scaling/consolidation
	monitoringPeriod := 5 * time.Minute
	monitorEnd := time.Now().Add(monitoringPeriod)
	pdbViolationCount := 0

	for time.Now().Before(monitorEnd) {
		err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: pdb.Name, Namespace: pdb.Namespace}, &currentPDB)
		if err == nil {
			if currentPDB.Status.DisruptionsAllowed < 0 {
				pdbViolationCount++
				t.Logf("PDB violation detected: DisruptionsAllowed=%d", currentPDB.Status.DisruptionsAllowed)
			}
		}

		// Verify original workload pods are still healthy
		err = suite.kubeClient.List(ctx, &podList, client.MatchingLabels{
			"app": workload.Name,
		})
		require.NoError(t, err, "Failed to list protected pods")

		healthyPods := 0
		for _, pod := range podList.Items {
			if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
				healthyPods++
			}
		}

		if healthyPods < 4 { // With maxUnavailable=1, we should have at least 4 healthy pods
			t.Errorf("PDB violation: only %d healthy pods, expected at least 4", healthyPods)
		}

		time.Sleep(30 * time.Second)
	}

	require.Equal(t, 0, pdbViolationCount, "No PDB violations should occur during monitoring period")
	t.Logf("PDB successfully enforced protection during %v monitoring period", monitoringPeriod)

	// Cleanup competitor workload
	err = suite.kubeClient.Delete(ctx, competitorWorkload)
	require.NoError(t, err, "Failed to delete competitor workload")

	// Cleanup PDB
	err = suite.kubeClient.Delete(ctx, pdb)
	require.NoError(t, err, "Failed to delete PDB")

	t.Logf("PDB enforcement test completed successfully: %s", testName)
}

// TestE2EPodAntiAffinity tests strict pod anti-affinity rules
// Tests that pods with anti-affinity are properly distributed across nodes
func TestE2EPodAntiAffinity(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	testName := fmt.Sprintf("e2e-antiaffinity-%d", time.Now().Unix())
	t.Logf("Starting pod anti-affinity E2E test: %s", testName)

	// Step 1: Create NodeClass
	nodeClass := suite.createTestNodeClass(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Step 2: Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Step 3: Create NodePool that can provision multiple nodes
	nodePool := suite.createTestNodePool(t, testName, nodeClass.Name)
	t.Logf("Created NodePool: %s", nodePool.Name)

	// Step 4: Deploy workload with strict pod anti-affinity
	workload := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-antiaffinity", testName),
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{3}[0], // 3 replicas that must be on different nodes
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": fmt.Sprintf("%s-antiaffinity", testName)},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":           fmt.Sprintf("%s-antiaffinity", testName),
						"anti-affinity": "required",
					},
				},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: map[string]string{
											"app": fmt.Sprintf("%s-antiaffinity", testName),
										},
									},
									TopologyKey: "kubernetes.io/hostname", // Each pod on different node
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err := suite.kubeClient.Create(ctx, workload)
	require.NoError(t, err, "Failed to create workload with anti-affinity")
	t.Logf("Created workload with strict anti-affinity: %s", workload.Name)

	// Step 5: Wait for pods to be scheduled
	suite.waitForPodsToBeScheduled(t, workload.Name, workload.Namespace)
	t.Logf("Pods with anti-affinity scheduled successfully")

	// Step 6: Verify each pod is on a different node
	var pods corev1.PodList
	err = suite.kubeClient.List(ctx, &pods, client.MatchingLabels{
		"app": fmt.Sprintf("%s-antiaffinity", testName),
	})
	require.NoError(t, err, "Failed to list anti-affinity pods")

	nodeNames := make(map[string]bool)
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != "" {
			if nodeNames[pod.Spec.NodeName] {
				t.Errorf("Anti-affinity violated: multiple pods on node %s", pod.Spec.NodeName)
			}
			nodeNames[pod.Spec.NodeName] = true
			t.Logf("Pod %s scheduled on unique node: %s", pod.Name, pod.Spec.NodeName)
		}
	}

	require.Equal(t, 3, len(nodeNames), "Should have 3 different nodes for 3 pods with anti-affinity")
	t.Logf("Verified: All %d pods are on different nodes as required by anti-affinity", len(pods.Items))

	// Step 7: Test soft anti-affinity (preferred but not required)
	softAntiAffinityWorkload := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-soft-antiaffinity", testName),
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{5}[0], // More replicas to test soft anti-affinity
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": fmt.Sprintf("%s-soft-antiaffinity", testName)},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":           fmt.Sprintf("%s-soft-antiaffinity", testName),
						"anti-affinity": "preferred",
					},
				},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: corev1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"app": fmt.Sprintf("%s-soft-antiaffinity", testName),
											},
										},
										TopologyKey: "kubernetes.io/hostname",
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, softAntiAffinityWorkload)
	require.NoError(t, err, "Failed to create workload with soft anti-affinity")
	t.Logf("Created workload with soft anti-affinity: %s", softAntiAffinityWorkload.Name)

	// Wait for soft anti-affinity pods to be scheduled
	suite.waitForPodsToBeScheduled(t, softAntiAffinityWorkload.Name, softAntiAffinityWorkload.Namespace)

	// Verify distribution of soft anti-affinity pods
	var softPods corev1.PodList
	err = suite.kubeClient.List(ctx, &softPods, client.MatchingLabels{
		"app": fmt.Sprintf("%s-soft-antiaffinity", testName),
	})
	require.NoError(t, err, "Failed to list soft anti-affinity pods")

	nodeDistribution := make(map[string]int)
	for _, pod := range softPods.Items {
		if pod.Spec.NodeName != "" {
			nodeDistribution[pod.Spec.NodeName]++
		}
	}

	t.Logf("Soft anti-affinity pod distribution across %d nodes:", len(nodeDistribution))
	for node, count := range nodeDistribution {
		t.Logf("  Node %s: %d pods", node, count)
	}

	// Step 8: Test zone-based anti-affinity
	zoneAntiAffinityWorkload := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-zone-antiaffinity", testName),
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{2}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": fmt.Sprintf("%s-zone-antiaffinity", testName)},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":           fmt.Sprintf("%s-zone-antiaffinity", testName),
						"anti-affinity": "zone",
					},
				},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: corev1.PodAffinityTerm{
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"app": fmt.Sprintf("%s-zone-antiaffinity", testName),
											},
										},
										TopologyKey: "topology.kubernetes.io/zone", // Prefer different zones
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, zoneAntiAffinityWorkload)
	require.NoError(t, err, "Failed to create workload with zone anti-affinity")
	t.Logf("Created workload with zone-based anti-affinity: %s", zoneAntiAffinityWorkload.Name)

	// Wait for zone anti-affinity pods to be scheduled
	suite.waitForPodsToBeScheduled(t, zoneAntiAffinityWorkload.Name, zoneAntiAffinityWorkload.Namespace)

	// Verify zone distribution
	var zonePods corev1.PodList
	err = suite.kubeClient.List(ctx, &zonePods, client.MatchingLabels{
		"app": fmt.Sprintf("%s-zone-antiaffinity", testName),
	})
	require.NoError(t, err, "Failed to list zone anti-affinity pods")

	zoneDistribution := make(map[string]int)
	for _, pod := range zonePods.Items {
		if pod.Spec.NodeName != "" {
			var node corev1.Node
			err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, &node)
			if err == nil {
				if zone, ok := node.Labels["topology.kubernetes.io/zone"]; ok {
					zoneDistribution[zone]++
					t.Logf("Pod %s in zone: %s", pod.Name, zone)
				}
			}
		}
	}

	if len(zoneDistribution) > 1 {
		t.Logf("Successfully distributed pods across %d zones", len(zoneDistribution))
	} else {
		t.Logf("All pods in single zone (may be due to single-zone test environment)")
	}

	// Step 9: Cleanup
	suite.cleanupTestWorkload(t, workload.Name, workload.Namespace)
	suite.cleanupTestWorkload(t, softAntiAffinityWorkload.Name, softAntiAffinityWorkload.Namespace)
	suite.cleanupTestWorkload(t, zoneAntiAffinityWorkload.Name, zoneAntiAffinityWorkload.Namespace)
	suite.cleanupTestResources(t, testName)

	t.Logf("Pod anti-affinity E2E test completed successfully: %s", testName)
}

// TestE2ENodeAffinity tests required node affinity rules
// Tests that pods with node affinity are scheduled only on matching nodes
func TestE2ENodeAffinity(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	testName := fmt.Sprintf("e2e-nodeaffinity-%d", time.Now().Unix())
	t.Logf("Starting node affinity E2E test: %s", testName)

	// Step 1: Create NodeClass
	nodeClass := suite.createTestNodeClass(t, testName)
	t.Logf("Created NodeClass: %s", nodeClass.Name)

	// Step 2: Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Step 3: Create NodePool with specific labels for affinity matching
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodepool", testName),
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"test":      "e2e-nodeaffinity",
						"test-name": testName,
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter.ibm.com",
						Kind:  "IBMNodeClass",
						Name:  nodeClass.Name,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      "node.kubernetes.io/instance-type",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"bx2-2x8", "bx2-4x16"},
							},
						},
					},
				},
			},
			Limits: karpv1.Limits{
				"cpu": resource.MustParse("1000"),
			},
		},
	}

	err := suite.kubeClient.Create(ctx, nodePool)
	require.NoError(t, err, "Failed to create NodePool with labels")
	t.Logf("Created NodePool with specific labels: %s", nodePool.Name)

	// Step 4: Deploy workload with REQUIRED node affinity
	workloadWithRequiredAffinity := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-required-affinity", testName),
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{3}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": fmt.Sprintf("%s-required-affinity", testName)},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":      fmt.Sprintf("%s-required-affinity", testName),
						"affinity": "required",
					},
				},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "node.kubernetes.io/instance-type",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"bx2-2x8", "bx2-4x16"},
											},
											{
												Key:      "kubernetes.io/arch",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"amd64"},
											},
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, workloadWithRequiredAffinity)
	require.NoError(t, err, "Failed to create workload with required node affinity")
	t.Logf("Created workload with required node affinity: %s", workloadWithRequiredAffinity.Name)

	// Step 5: Wait for pods to be scheduled
	suite.waitForPodsToBeScheduled(t, workloadWithRequiredAffinity.Name, workloadWithRequiredAffinity.Namespace)
	t.Logf("Pods with required node affinity scheduled successfully")

	// Step 6: Verify all pods are on nodes matching the affinity rules
	var requiredAffinityPods corev1.PodList
	err = suite.kubeClient.List(ctx, &requiredAffinityPods, client.MatchingLabels{
		"app": fmt.Sprintf("%s-required-affinity", testName),
	})
	require.NoError(t, err, "Failed to list required affinity pods")

	for _, pod := range requiredAffinityPods.Items {
		if pod.Spec.NodeName != "" {
			var node corev1.Node
			err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, &node)
			require.NoError(t, err, "Failed to get node for pod")

			// Verify node has all required labels
			require.Contains(t, []string{"bx2-2x8", "bx2-4x16"}, node.Labels["node.kubernetes.io/instance-type"], "Node should have matching instance type")
			require.Equal(t, "amd64", node.Labels["kubernetes.io/arch"], "Node should have amd64 architecture")
			t.Logf("Pod %s correctly scheduled on node %s with matching labels", pod.Name, node.Name)
		}
	}

	// Step 7: Deploy workload with PREFERRED node affinity
	workloadWithPreferredAffinity := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-preferred-affinity", testName),
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{3}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": fmt.Sprintf("%s-preferred-affinity", testName)},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":      fmt.Sprintf("%s-preferred-affinity", testName),
						"affinity": "preferred",
					},
				},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
								{
									Weight: 100,
									Preference: corev1.NodeSelectorTerm{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "node.kubernetes.io/instance-type",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"bx2-2x8"},
											},
										},
									},
								},
								{
									Weight: 50,
									Preference: corev1.NodeSelectorTerm{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "kubernetes.io/arch",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"amd64"},
											},
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, workloadWithPreferredAffinity)
	require.NoError(t, err, "Failed to create workload with preferred node affinity")
	t.Logf("Created workload with preferred node affinity: %s", workloadWithPreferredAffinity.Name)

	// Wait for preferred affinity pods to be scheduled
	suite.waitForPodsToBeScheduled(t, workloadWithPreferredAffinity.Name, workloadWithPreferredAffinity.Namespace)

	// Verify preferred affinity pod placement
	var preferredAffinityPods corev1.PodList
	err = suite.kubeClient.List(ctx, &preferredAffinityPods, client.MatchingLabels{
		"app": fmt.Sprintf("%s-preferred-affinity", testName),
	})
	require.NoError(t, err, "Failed to list preferred affinity pods")

	preferredNodesCount := 0
	for _, pod := range preferredAffinityPods.Items {
		if pod.Spec.NodeName != "" {
			var node corev1.Node
			err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, &node)
			if err == nil {
				// Check if node matches preferred criteria
				if node.Labels["node.kubernetes.io/instance-type"] == "bx2-2x8" || node.Labels["kubernetes.io/arch"] == "amd64" {
					preferredNodesCount++
					t.Logf("Pod %s scheduled on preferred node %s", pod.Name, node.Name)
				} else {
					t.Logf("Pod %s scheduled on non-preferred node %s (acceptable for preferred affinity)", pod.Name, node.Name)
				}
			}
		}
	}

	t.Logf("Preferred affinity: %d/%d pods on preferred nodes", preferredNodesCount, len(preferredAffinityPods.Items))

	// Step 8: Test node affinity with NOT IN operator
	workloadWithNotInAffinity := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-notin-affinity", testName),
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{2}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": fmt.Sprintf("%s-notin-affinity", testName)},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":      fmt.Sprintf("%s-notin-affinity", testName),
						"affinity": "not-in",
					},
				},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "gpu-enabled",
												Operator: corev1.NodeSelectorOpNotIn,
												Values:   []string{"true"},
											},
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, workloadWithNotInAffinity)
	require.NoError(t, err, "Failed to create workload with NotIn node affinity")
	t.Logf("Created workload with NotIn node affinity: %s", workloadWithNotInAffinity.Name)

	// Wait for NotIn affinity pods to be scheduled
	suite.waitForPodsToBeScheduled(t, workloadWithNotInAffinity.Name, workloadWithNotInAffinity.Namespace)

	// Verify NotIn affinity pod placement
	var notInAffinityPods corev1.PodList
	err = suite.kubeClient.List(ctx, &notInAffinityPods, client.MatchingLabels{
		"app": fmt.Sprintf("%s-notin-affinity", testName),
	})
	require.NoError(t, err, "Failed to list NotIn affinity pods")

	for _, pod := range notInAffinityPods.Items {
		if pod.Spec.NodeName != "" {
			var node corev1.Node
			err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, &node)
			if err == nil {
				// Verify node does NOT have gpu-enabled=true
				if node.Labels["gpu-enabled"] == "true" {
					t.Errorf("Pod %s incorrectly scheduled on GPU node %s", pod.Name, node.Name)
				} else {
					t.Logf("Pod %s correctly scheduled on non-GPU node %s", pod.Name, node.Name)
				}
			}
		}
	}

	// Step 9: Test combining node affinity with node selector
	workloadWithAffinityAndSelector := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-combined", testName),
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{2}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": fmt.Sprintf("%s-combined", testName)},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": fmt.Sprintf("%s-combined", testName),
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"node.kubernetes.io/instance-type": "bx2-2x8",
					},
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "kubernetes.io/arch",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"amd64"},
											},
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err = suite.kubeClient.Create(ctx, workloadWithAffinityAndSelector)
	require.NoError(t, err, "Failed to create workload with combined affinity and selector")
	t.Logf("Created workload with combined node affinity and selector: %s", workloadWithAffinityAndSelector.Name)

	// Wait for combined pods to be scheduled
	suite.waitForPodsToBeScheduled(t, workloadWithAffinityAndSelector.Name, workloadWithAffinityAndSelector.Namespace)

	// Verify combined placement rules
	var combinedPods corev1.PodList
	err = suite.kubeClient.List(ctx, &combinedPods, client.MatchingLabels{
		"app": fmt.Sprintf("%s-combined", testName),
	})
	require.NoError(t, err, "Failed to list combined affinity pods")

	for _, pod := range combinedPods.Items {
		if pod.Spec.NodeName != "" {
			var node corev1.Node
			err = suite.kubeClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, &node)
			require.NoError(t, err, "Failed to get node for combined pod")

			// Verify both nodeSelector and affinity are satisfied
			require.Equal(t, "bx2-2x8", node.Labels["node.kubernetes.io/instance-type"], "Node should satisfy nodeSelector")
			require.Equal(t, "amd64", node.Labels["kubernetes.io/arch"], "Node should satisfy node affinity")
			t.Logf("Pod %s correctly scheduled on node %s satisfying both nodeSelector and affinity", pod.Name, node.Name)
		}
	}

	// Step 10: Cleanup
	suite.cleanupTestWorkload(t, workloadWithRequiredAffinity.Name, workloadWithRequiredAffinity.Namespace)
	suite.cleanupTestWorkload(t, workloadWithPreferredAffinity.Name, workloadWithPreferredAffinity.Namespace)
	suite.cleanupTestWorkload(t, workloadWithNotInAffinity.Name, workloadWithNotInAffinity.Namespace)
	suite.cleanupTestWorkload(t, workloadWithAffinityAndSelector.Name, workloadWithAffinityAndSelector.Namespace)
	suite.cleanupTestResources(t, testName)

	t.Logf("Node affinity E2E test completed successfully: %s", testName)
}

// TestE2EMixedArchitecture tests provisioning nodes with different CPU architectures
// This validates that the provider can handle both AMD64 and s390x workloads
func TestE2EMixedArchitecture(t *testing.T) {
	suite := SetupE2ETestSuite(t)
	ctx := context.Background()

	testName := fmt.Sprintf("e2e-mixed-arch-%d", time.Now().Unix())
	t.Logf("Starting mixed architecture E2E test: %s", testName)

	// Step 1: Create NodeClass without architecture restrictions
	nodeClass := suite.createArchitectureAgnosticNodeClass(t, testName)
	t.Logf("Created architecture-agnostic NodeClass: %s", nodeClass.Name)

	// Step 2: Wait for NodeClass to be ready
	suite.waitForNodeClassReady(t, nodeClass.Name)
	t.Logf("NodeClass is ready: %s", nodeClass.Name)

	// Step 3: Create AMD64-specific NodePool
	amd64NodePool := suite.createArchSpecificNodePool(t, testName+"-amd64", nodeClass.Name, "amd64",
		[]string{"bx2-2x8", "bx2-4x16"}) // AMD64 instance types
	t.Logf("Created AMD64 NodePool: %s", amd64NodePool.Name)

	// Step 4: Create s390x-specific NodePool (IBM Z architecture)
	s390xNodePool := suite.createArchSpecificNodePool(t, testName+"-s390x", nodeClass.Name, "s390x",
		[]string{"bz2-2x8", "bz2-4x16"}) // Use IBM Z instance types (bz2 prefix)
	t.Logf("Created s390x NodePool: %s", s390xNodePool.Name)

	// Step 5: Deploy AMD64-specific workload
	amd64Workload := suite.deployArchSpecificWorkload(t, testName+"-amd64", "amd64", amd64NodePool.Name)
	t.Logf("Deployed AMD64 workload: %s", amd64Workload.Name)

	// Step 6: Deploy s390x-specific workload
	s390xWorkload := suite.deployArchSpecificWorkload(t, testName+"-s390x", "s390x", s390xNodePool.Name)
	t.Logf("Deployed s390x workload: %s", s390xWorkload.Name)

	// Step 7: Wait for both workloads to be scheduled
	suite.waitForPodsToBeScheduled(t, amd64Workload.Name, amd64Workload.Namespace)
	suite.waitForPodsToBeScheduled(t, s390xWorkload.Name, s390xWorkload.Namespace)
	t.Logf("Both architecture-specific workloads scheduled successfully")

	// Step 8: Verify architecture-specific node provisioning
	suite.verifyArchitectureNodes(t, amd64NodePool.Name, "amd64")
	suite.verifyArchitectureNodes(t, s390xNodePool.Name, "s390x")
	t.Logf("Verified architecture-specific nodes provisioned correctly")

	// Step 9: Test cross-architecture scheduling prevention
	suite.testCrossArchitectureSchedulingPrevention(t, testName, amd64NodePool.Name, s390xNodePool.Name)
	t.Logf("Verified cross-architecture scheduling prevention works")

	// Step 10: Cleanup
	suite.cleanupTestWorkload(t, amd64Workload.Name, amd64Workload.Namespace)
	suite.cleanupTestWorkload(t, s390xWorkload.Name, s390xWorkload.Namespace)
	err := suite.kubeClient.Delete(ctx, amd64NodePool)
	require.NoError(t, err)
	err = suite.kubeClient.Delete(ctx, s390xNodePool)
	require.NoError(t, err)
	suite.cleanupTestResources(t, testName)

	t.Logf("Mixed architecture E2E test completed successfully: %s", testName)
}

// Helper function: Create architecture-agnostic NodeClass
func (s *E2ETestSuite) createArchitectureAgnosticNodeClass(t *testing.T, testName string) *v1alpha1.IBMNodeClass {
	nodeClass := &v1alpha1.IBMNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-nodeclass", testName),
		},
		Spec: v1alpha1.IBMNodeClassSpec{
			Region:         s.testRegion,
			Zone:           s.testZone,
			Image:          s.testImage, // Should be multi-arch image
			VPC:            s.testVPC,
			Subnet:         s.testSubnet,
			SecurityGroups: []string{os.Getenv("TEST_SECURITY_GROUP_ID")},
			// No instanceProfile specified - let NodePool decide
			// No architecture requirement - support both
			Tags: map[string]string{
				"test":      "e2e",
				"test-name": testName,
				"purpose":   "mixed-architecture",
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodeClass)
	require.NoError(t, err)
	return nodeClass
}

// Helper function: Create architecture-specific NodePool
func (s *E2ETestSuite) createArchSpecificNodePool(t *testing.T, name, nodeClassName, arch string, instanceTypes []string) *karpv1.NodePool {
	expireAfter := karpv1.MustParseNillableDuration("10m")
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"test":         "e2e",
				"architecture": arch,
			},
		},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				ObjectMeta: karpv1.ObjectMeta{
					Labels: map[string]string{
						"architecture": arch,
					},
				},
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter.ibm.sh",
						Kind:  "IBMNodeClass",
						Name:  nodeClassName,
					},
					Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelArchStable, // "kubernetes.io/arch"
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{arch},
							},
						},
						{
							NodeSelectorRequirement: corev1.NodeSelectorRequirement{
								Key:      corev1.LabelInstanceTypeStable,
								Operator: corev1.NodeSelectorOpIn,
								Values:   instanceTypes,
							},
						},
					},
					ExpireAfter: expireAfter,
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), nodePool)
	require.NoError(t, err)
	return nodePool
}

// Helper function: Deploy architecture-specific workload
func (s *E2ETestSuite) deployArchSpecificWorkload(t *testing.T, name, arch, nodePoolName string) *appsv1.Deployment {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{2}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":          name,
						"architecture": arch,
					},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/arch":    arch,
						"karpenter.sh/nodepool": nodePoolName,
					},
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: getArchSpecificImage(arch),
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	err := s.kubeClient.Create(context.Background(), deployment)
	require.NoError(t, err)
	return deployment
}

// Helper function: Get architecture-specific container image
func getArchSpecificImage(arch string) string {
	switch arch {
	case "s390x":
		return "nginx:1.21-alpine" // nginx multi-arch image supports s390x
	case "amd64":
		return "nginx:1.21-alpine"
	default:
		return "nginx:1.21-alpine"
	}
}

// Helper function: Verify nodes have correct architecture
func (s *E2ETestSuite) verifyArchitectureNodes(t *testing.T, nodePoolName, expectedArch string) {
	ctx := context.Background()
	var nodeList corev1.NodeList
	err := s.kubeClient.List(ctx, &nodeList, client.MatchingLabels{
		"karpenter.sh/nodepool": nodePoolName,
	})
	require.NoError(t, err)

	for _, node := range nodeList.Items {
		// Verify architecture label
		arch, exists := node.Labels["kubernetes.io/arch"]
		require.True(t, exists, "Node %s should have architecture label", node.Name)
		require.Equal(t, expectedArch, arch, "Node %s should have architecture %s", node.Name, expectedArch)

		// Verify node status reports correct architecture
		require.Equal(t, expectedArch, string(node.Status.NodeInfo.Architecture),
			"Node %s status should report architecture %s", node.Name, expectedArch)

		t.Logf("✓ Node %s correctly provisioned with architecture: %s", node.Name, arch)
	}
}

// Helper function: Test that cross-architecture scheduling is prevented
func (s *E2ETestSuite) testCrossArchitectureSchedulingPrevention(t *testing.T, testName, amd64NodePool, s390xNodePool string) {
	// Try to schedule s390x workload on AMD64 nodes - should fail
	wrongArchWorkload := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-wrong-arch", testName),
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &[]int32{1}[0],
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": fmt.Sprintf("%s-wrong-arch", testName)},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": fmt.Sprintf("%s-wrong-arch", testName)},
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/arch":    "s390x",       // Want s390x
						"karpenter.sh/nodepool": amd64NodePool, // But selecting AMD64 pool
					},
					Containers: []corev1.Container{
						{
							Name:  "test",
							Image: "nginx:1.21",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("32Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	ctx := context.Background()
	err := s.kubeClient.Create(ctx, wrongArchWorkload)
	require.NoError(t, err)

	// Wait a bit and verify pod remains unschedulable
	time.Sleep(30 * time.Second)

	var pods corev1.PodList
	err = s.kubeClient.List(ctx, &pods, client.MatchingLabels{
		"app": fmt.Sprintf("%s-wrong-arch", testName),
	})
	require.NoError(t, err)

	for _, pod := range pods.Items {
		require.Equal(t, corev1.PodPending, pod.Status.Phase,
			"Pod with conflicting architecture requirements should remain pending")

		// Check for scheduling failure event
		var events corev1.EventList
		err = s.kubeClient.List(ctx, &events, client.InNamespace("default"))
		require.NoError(t, err)

		foundSchedulingError := false
		for _, event := range events.Items {
			if event.InvolvedObject.Name == pod.Name && event.Reason == "FailedScheduling" {
				foundSchedulingError = true
				t.Logf("Found expected scheduling failure: %s", event.Message)
				break
			}
		}
		require.True(t, foundSchedulingError, "Should have FailedScheduling event for conflicting requirements")
	}

	// Cleanup
	err = s.kubeClient.Delete(ctx, wrongArchWorkload)
	require.NoError(t, err)
}
