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

package loadbalancer

import (
	"context"
	"fmt"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/apis/v1alpha1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "LoadBalancer Controller Test Suite")
}

var _ = Describe("LoadBalancer Controller", func() {
	var (
		ctx                      context.Context
		controller               *Controller
		client                   client.Client
		mockLoadBalancerProvider *MockLoadBalancerProvider
		nodeClaim                *karpv1.NodeClaim
		nodeClass                *v1alpha1.IBMNodeClass
		node                     *corev1.Node
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())

		// Add Karpenter v1 types manually
		gv := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
		scheme.AddKnownTypes(gv,
			&karpv1.NodeClaim{},
			&karpv1.NodeClaimList{},
			&karpv1.NodePool{},
			&karpv1.NodePoolList{},
		)
		metav1.AddToGroupVersion(scheme, gv)

		client = fake.NewClientBuilder().WithScheme(scheme).Build()
		mockLoadBalancerProvider = &MockLoadBalancerProvider{}

		// Create a real controller but we'll override the provider behavior in tests
		controller = NewController(client, &ibm.VPCClient{})
		// Replace with mock for testing
		controller.loadBalancerProvider = mockLoadBalancerProvider

		nodeClass = &v1alpha1.IBMNodeClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodeclass",
			},
			Spec: v1alpha1.IBMNodeClassSpec{
				LoadBalancerIntegration: &v1alpha1.LoadBalancerIntegration{
					Enabled: true,
					TargetGroups: []v1alpha1.LoadBalancerTarget{
						{
							LoadBalancerID: "lb-123",
							PoolName:       "pool-1",
							Port:           80,
						},
					},
				},
			},
		}

		nodeClaim = &karpv1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-nodeclaim",
			},
			Spec: karpv1.NodeClaimSpec{
				NodeClassRef: &karpv1.NodeClassReference{
					Kind: "IBMNodeClass",
					Name: "test-nodeclass",
				},
			},
			Status: karpv1.NodeClaimStatus{
				ProviderID: "ibm:///us-south-1/instance-123",
				NodeName:   "test-node",
			},
		}

		node = &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-node",
			},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{
					{
						Type:    corev1.NodeInternalIP,
						Address: "10.0.0.1",
					},
				},
			},
		}
	})

	Describe("Reconcile", func() {
		Context("when load balancer integration is enabled", func() {
			BeforeEach(func() {
				Expect(client.Create(ctx, nodeClass)).To(Succeed())
				Expect(client.Create(ctx, nodeClaim)).To(Succeed())
				Expect(client.Create(ctx, node)).To(Succeed())
			})

			It("should add finalizer and register with load balancers", func() {
				mockLoadBalancerProvider.RegisterInstanceFunc = func(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID, instanceIP string) error {
					Expect(instanceID).To(Equal("instance-123"))
					Expect(instanceIP).To(Equal("10.0.0.1"))
					return nil
				}

				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: "test-nodeclaim",
					},
				}

				// First reconcile - adds finalizer
				result, err := controller.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				// Should requeue to add finalizer
				// Check RequeueAfter is set (controller uses RequeueAfter instead of deprecated Requeue)
				Expect(result.RequeueAfter).To(BeNumerically(">", 0))

				// Verify finalizer was added
				var updatedNodeClaim karpv1.NodeClaim
				Expect(client.Get(ctx, req.NamespacedName, &updatedNodeClaim)).To(Succeed())
				Expect(updatedNodeClaim.Finalizers).To(ContainElement(LoadBalancerFinalizer))

				// Second reconcile - performs registration
				result, err = controller.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				// Should not requeue
				Expect(result.RequeueAfter).To(BeZero())

				// Verify registration annotation was added
				Expect(client.Get(ctx, req.NamespacedName, &updatedNodeClaim)).To(Succeed())
				Expect(updatedNodeClaim.Annotations).To(HaveKey(LoadBalancerRegisteredAnnotation))
				Expect(updatedNodeClaim.Annotations[LoadBalancerRegisteredAnnotation]).To(Equal("true"))
			})

			It("should handle registration errors gracefully", func() {
				mockLoadBalancerProvider.RegisterInstanceFunc = func(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID, instanceIP string) error {
					return fmt.Errorf("registration failed")
				}

				// Add finalizer first
				nodeClaim.Finalizers = []string{LoadBalancerFinalizer}
				Expect(client.Update(ctx, nodeClaim)).To(Succeed())

				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: "test-nodeclaim",
					},
				}

				result, err := controller.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(Equal(30 * time.Second))

				// Verify no registration annotation was added
				var updatedNodeClaim karpv1.NodeClaim
				Expect(client.Get(ctx, req.NamespacedName, &updatedNodeClaim)).To(Succeed())
				Expect(updatedNodeClaim.Annotations).NotTo(HaveKey(LoadBalancerRegisteredAnnotation))
			})
		})

		Context("when load balancer integration is disabled", func() {
			BeforeEach(func() {
				nodeClass.Spec.LoadBalancerIntegration.Enabled = false
				Expect(client.Create(ctx, nodeClass)).To(Succeed())
				nodeClaim.Finalizers = []string{LoadBalancerFinalizer}
				Expect(client.Create(ctx, nodeClaim)).To(Succeed())
			})

			It("should remove finalizer when integration is disabled", func() {
				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: "test-nodeclaim",
					},
				}

				result, err := controller.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				// Should not requeue
				Expect(result.RequeueAfter).To(BeZero())

				// Verify finalizer was removed
				var updatedNodeClaim karpv1.NodeClaim
				Expect(client.Get(ctx, req.NamespacedName, &updatedNodeClaim)).To(Succeed())
				Expect(updatedNodeClaim.Finalizers).NotTo(ContainElement(LoadBalancerFinalizer))
			})
		})

		Context("when NodeClaim is being deleted", func() {
			BeforeEach(func() {
				Expect(client.Create(ctx, nodeClass)).To(Succeed())
				nodeClaim.Finalizers = []string{LoadBalancerFinalizer}
				now := metav1.Now()
				nodeClaim.DeletionTimestamp = &now
				Expect(client.Create(ctx, nodeClaim)).To(Succeed())
			})

			PIt("should deregister from load balancers and remove finalizer", func() {
				mockLoadBalancerProvider.DeregisterInstanceFunc = func(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID string) error {
					Expect(instanceID).To(Equal("instance-123"))
					return nil
				}

				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: "test-nodeclaim",
					},
				}

				result, err := controller.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				// Should not requeue
				Expect(result.RequeueAfter).To(BeZero())

				// Verify finalizer was removed
				var updatedNodeClaim karpv1.NodeClaim
				Expect(client.Get(ctx, req.NamespacedName, &updatedNodeClaim)).To(Succeed())
				Expect(updatedNodeClaim.Finalizers).NotTo(ContainElement(LoadBalancerFinalizer))
			})

			PIt("should continue with cleanup even if deregistration fails", func() {
				mockLoadBalancerProvider.DeregisterInstanceFunc = func(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID string) error {
					return fmt.Errorf("deregistration failed")
				}

				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: "test-nodeclaim",
					},
				}

				result, err := controller.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				// Should not requeue
				Expect(result.RequeueAfter).To(BeZero())

				// Verify finalizer was still removed
				var updatedNodeClaim karpv1.NodeClaim
				Expect(client.Get(ctx, req.NamespacedName, &updatedNodeClaim)).To(Succeed())
				Expect(updatedNodeClaim.Finalizers).NotTo(ContainElement(LoadBalancerFinalizer))
			})
		})

		Context("when NodeClaim is not ready", func() {
			BeforeEach(func() {
				Expect(client.Create(ctx, nodeClass)).To(Succeed())
				nodeClaim.Status.ProviderID = ""
				Expect(client.Create(ctx, nodeClaim)).To(Succeed())
			})

			It("should requeue when provider ID is not available", func() {
				// Add finalizer first
				nodeClaim.Finalizers = []string{LoadBalancerFinalizer}
				Expect(client.Update(ctx, nodeClaim)).To(Succeed())

				req := reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name: "test-nodeclaim",
					},
				}

				result, err := controller.Reconcile(ctx, req)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(Equal(30 * time.Second))
			})
		})
	})

	Describe("Helper Methods", func() {
		Context("extractInstanceID", func() {
			It("should extract instance ID from valid provider ID", func() {
				providerID := "ibm:///us-south-1/instance-123"
				instanceID, err := controller.extractInstanceID(providerID)
				Expect(err).NotTo(HaveOccurred())
				Expect(instanceID).To(Equal("instance-123"))
			})

			It("should return error for invalid provider ID", func() {
				providerID := "invalid"
				_, err := controller.extractInstanceID(providerID)
				Expect(err).To(HaveOccurred())
			})
		})

		Context("getNodeInternalIP", func() {
			It("should return internal IP address", func() {
				ip := controller.getNodeInternalIP(node)
				Expect(ip).To(Equal("10.0.0.1"))
			})

			It("should return empty string when no internal IP", func() {
				node.Status.Addresses = []corev1.NodeAddress{
					{
						Type:    corev1.NodeExternalIP,
						Address: "1.2.3.4",
					},
				}
				ip := controller.getNodeInternalIP(node)
				Expect(ip).To(Equal(""))
			})
		})

		Context("isAlreadyRegistered", func() {
			It("should return true when registered annotation is present", func() {
				nodeClaim.Annotations = map[string]string{
					LoadBalancerRegisteredAnnotation: "true",
				}
				Expect(controller.isAlreadyRegistered(nodeClaim)).To(BeTrue())
			})

			It("should return false when annotation is missing", func() {
				Expect(controller.isAlreadyRegistered(nodeClaim)).To(BeFalse())
			})

			It("should return false when annotation is false", func() {
				nodeClaim.Annotations = map[string]string{
					LoadBalancerRegisteredAnnotation: "false",
				}
				Expect(controller.isAlreadyRegistered(nodeClaim)).To(BeFalse())
			})
		})
	})
})

// MockLoadBalancerProvider for testing
type MockLoadBalancerProvider struct {
	RegisterInstanceFunc   func(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID, instanceIP string) error
	DeregisterInstanceFunc func(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID string) error
}

func (m *MockLoadBalancerProvider) RegisterInstance(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID, instanceIP string) error {
	if m.RegisterInstanceFunc != nil {
		return m.RegisterInstanceFunc(ctx, nodeClass, instanceID, instanceIP)
	}
	return nil
}

func (m *MockLoadBalancerProvider) DeregisterInstance(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass, instanceID string) error {
	if m.DeregisterInstanceFunc != nil {
		return m.DeregisterInstanceFunc(ctx, nodeClass, instanceID)
	}
	return nil
}

func (m *MockLoadBalancerProvider) ValidateLoadBalancerConfiguration(ctx context.Context, nodeClass *v1alpha1.IBMNodeClass) error {
	return nil
}

// MockVPCClient for testing
type MockVPCClient struct{}
