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
package controllers

//+kubebuilder:rbac:groups=karpenter.sh,resources=nodepools,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=karpenter.sh,resources=nodepools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=karpenter.sh,resources=nodeclaims,verbs=get;list;watch;create;delete;update;patch
//+kubebuilder:rbac:groups=karpenter.sh,resources=nodeclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=karpenter-ibm.sh,resources=ibmnodeclasses,verbs=get;list;watch;patch;update
//+kubebuilder:rbac:groups=karpenter-ibm.sh,resources=ibmnodeclasses/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;create;delete;update;patch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch
//+kubebuilder:rbac:groups=storage.k8s.io,resources=csinodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=storage.k8s.io,resources=volumeattachments,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=persistentvolumes,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
//+kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps;secrets,verbs=get;list;watch;create;update;patch;delete,namespace=karpenter
//+kubebuilder:rbac:groups="",resources=services;endpoints,verbs=get;list;watch,namespace=kube-system
//+kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create,namespace=kube-system
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete,namespace=kube-node-lease

import (
	"context"
	"fmt"
	"os"

	"github.com/awslabs/operatorpkg/controller"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/bootstrap"
	iksallocation "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/iks/allocation"
	ikspoolcleanup "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/iks/poolcleanup"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/interruption"
	nodeorphancleanup "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/node/orphancleanup"
	vpcallocation "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclaim/allocation"
	nodeclaimgc "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclaim/garbagecollection"
	nodeclaimloadbalancer "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclaim/loadbalancer"
	nodeclaimregistration "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclaim/registration"
	nodeclaimstartuptaint "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclaim/startuptaint"
	nodeclaimtagging "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclaim/tagging"
	nodeclassautoplacement "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclass/autoplacement"
	nodeclasshash "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclass/hash"
	nodeclassstatus "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclass/status"
	nodeclasstermination "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/nodeclass/termination"
	providersinstancetype "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/providers/instancetype"
	controllerspricing "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/providers/pricing"
	spotpreemption "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers/spot/preemption"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/pricing"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/subnet"
)

// RecorderAdapter adapts between events.Recorder and record.EventRecorder
type RecorderAdapter struct {
	events.Recorder
}

// Event implements record.EventRecorder
func (r *RecorderAdapter) Event(object runtime.Object, eventtype, reason, message string) {
	r.Publish(events.Event{
		InvolvedObject: object,
		Type:           eventtype,
		Reason:         reason,
		Message:        message,
	})
}

// Eventf implements record.EventRecorder
func (r *RecorderAdapter) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	r.Publish(events.Event{
		InvolvedObject: object,
		Type:           eventtype,
		Reason:         reason,
		Message:        fmt.Sprintf(messageFmt, args...),
	})
}

// AnnotatedEventf implements record.EventRecorder
func (r *RecorderAdapter) AnnotatedEventf(object runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
	r.Publish(events.Event{
		InvolvedObject: object,
		Type:           eventtype,
		Reason:         reason,
		Message:        fmt.Sprintf(messageFmt, args...),
	})
}

func NewControllers(
	ctx context.Context,
	mgr manager.Manager,
	clk clock.Clock,
	kubeClient client.Client,
	kubernetesClient kubernetes.Interface,
	recorder events.Recorder,
	unavailableOfferings *cache.UnavailableOfferings,
	cloudProvider cloudprovider.CloudProvider,
	instanceTypeProvider instancetype.Provider,
	subnetProvider subnet.Provider,
	ibmClient *ibm.Client,
	factories ...*providers.ProviderFactory,
) []controller.Controller {
	// Create event recorder adapter
	recorderAdapter := &RecorderAdapter{recorder}
	logger := log.FromContext(ctx).WithName("controllers")

	controllers := []controller.Controller{}
	var providerFactory *providers.ProviderFactory
	if len(factories) != 0 && factories[0] != nil {
		providerFactory = factories[0]
	} else if ibmClient != nil {
		providerFactory = providers.NewProviderFactory(ctx, ibmClient, kubeClient, kubernetesClient, unavailableOfferings, providers.WithAPIReader(mgr.GetAPIReader()))
	}

	// Add IBM-specific controllers
	if hashCtrl, err := nodeclasshash.NewController(kubeClient, mgr.GetAPIReader()); err != nil {
		logger.Error(err, "failed to create hash controller")
	} else {
		controllers = append(controllers, hashCtrl)
	}

	// Autoplacement controller self-registers with the manager via builder.ControllerManagedBy().Complete()
	// inside NewController, so it does not need to be appended to the controllers slice.
	if _, err := nodeclassautoplacement.NewController(mgr, instanceTypeProvider, subnetProvider); err != nil {
		logger.Error(err, "failed to create autoplacement controller")
	} else {
		logger.Info("Successfully registered autoplacement controller")
	}

	if statusCtrl, err := nodeclassstatus.NewController(kubeClient, mgr.GetAPIReader()); err != nil {
		logger.Error(err, "failed to create status controller")
	} else {
		controllers = append(controllers, statusCtrl)
	}

	if terminationCtrl, err := nodeclasstermination.NewController(kubeClient, recorderAdapter); err != nil {
		logger.Error(err, "failed to create termination controller")
	} else {
		controllers = append(controllers, terminationCtrl)
	}

	// Add garbage collection controller
	garbageCollectionCtrl := nodeclaimgc.NewController(kubeClient, cloudProvider, mgr.GetAPIReader())
	controllers = append(controllers, garbageCollectionCtrl)

	// Migrates nodes registered by the removed provider registration controller onto core lifecycle ownership
	if registrationCtrl, err := nodeclaimregistration.NewControllerWithLifecycle(kubeClient, mgr.GetAPIReader(), cloudProvider, providerFactory); err != nil {
		logger.Error(err, "failed to create registration controller")
	} else {
		controllers = append(controllers, registrationCtrl)
	}

	// Removes finalizers and labels left by the removed startup taint controller
	startupTaintCtrl := nodeclaimstartuptaint.NewController(kubeClient, mgr.GetAPIReader())
	if err := startupTaintCtrl.Register(ctx, mgr); err != nil {
		logger.Error(err, "failed to register startup taint lifecycle controller")
	} else {
		logger.Info("Registered startup taint lifecycle controller")
	}

	// Add tagging controller (VPC mode only)
	if taggingCtrl, err := nodeclaimtagging.NewController(kubeClient, ibmClient); err != nil {
		logger.Error(err, "failed to create tagging controller")
	} else {
		controllers = append(controllers, taggingCtrl)
	}

	// Add load balancer controller (VPC mode only)
	if ibmClient != nil {
		vpcClient, err := ibmClient.GetVPCClient(ctx)
		if err != nil {
			logger.Error(err, "failed to get VPC client for load balancer controller")
		} else {
			loadBalancerCtrl := nodeclaimloadbalancer.NewController(kubeClient, vpcClient, mgr.GetAPIReader())
			if err := loadBalancerCtrl.Register(ctx, mgr); err != nil {
				logger.Error(err, "failed to register load balancer controller")
			} else {
				logger.Info("Registered load balancer controller")
			}
		}
	}

	// Add instance type controller
	if instanceTypeCtrl, err := providersinstancetype.NewController(instanceTypeProvider); err != nil {
		logger.Error(err, "failed to create instance type controller")
	} else {
		controllers = append(controllers, instanceTypeCtrl)
	}

	if providerFactory != nil {
		controllers = append(controllers, vpcallocation.NewController(kubeClient, mgr.GetAPIReader(), providerFactory))
	}
	interruptionCtrl := interruption.NewController(kubeClient, recorderAdapter, unavailableOfferings, mgr.GetAPIReader())
	controllers = append(controllers, interruptionCtrl)

	if ibmClient != nil {
		spotPreemptionCtrl := spotpreemption.NewController(kubeClient, recorderAdapter, unavailableOfferings, ibmClient)
		controllers = append(controllers, spotPreemptionCtrl)
	}

	// Add pricing controller with the real provider from the factory when available
	var pricingProvider pricing.Provider
	if providerFactory != nil {
		pricingProvider = providerFactory.GetPricingProvider()
	}
	if pricingCtrl, err := controllerspricing.NewController(pricingProvider); err != nil {
		logger.Error(err, "failed to create pricing controller")
	} else {
		controllers = append(controllers, pricingCtrl)
	}

	// Add orphaned node cleanup controller (only if enabled and IBM client available)
	if ibmClient != nil && isOrphanCleanupEnabled() {
		orphanCleanupCtrl := nodeorphancleanup.NewController(kubeClient, ibmClient, mgr.GetAPIReader())
		controllers = append(controllers, orphanCleanupCtrl)
		logger.Info("Enabled orphaned node cleanup controller")
	} else if ibmClient == nil {
		logger.Info("IBM client not available, Skipped orphaned node cleanup controller")
	} else {
		logger.Info("Orphaned node cleanup controller is disabled")
	}

	// Add IKS pool cleanup controller for dynamic pool lifecycle management
	if ibmClient != nil {
		controllers = append(controllers, iksallocation.NewController(kubeClient, mgr.GetAPIReader(), providerFactory))
		poolCleanupCtrl := ikspoolcleanup.NewController(kubeClient, ibmClient, mgr.GetAPIReader())
		if err := poolCleanupCtrl.Register(ctx, mgr); err != nil {
			logger.Error(err, "failed to register IKS pool cleanup controller")
		} else {
			logger.Info("Enabled IKS pool cleanup controller")
		}
	}

	return controllers
}

// isOrphanCleanupEnabled checks if orphan cleanup is enabled via environment variable
func isOrphanCleanupEnabled() bool {
	return os.Getenv("KARPENTER_ENABLE_ORPHAN_CLEANUP") == "true"
}

// RegisterBootstrapController adds the bootstrap token controller to the manager
func RegisterBootstrapController(mgr manager.Manager, factory *providers.ProviderFactory) error {
	if factory == nil {
		return fmt.Errorf("bootstrap controllers require a provider factory")
	}
	bootstrapCtrl := bootstrap.NewTokenController(mgr)
	if err := bootstrapCtrl.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up bootstrap token controller: %w", err)
	}
	if err := bootstrap.NewCSRController(mgr, factory).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up verified CSR controller: %w", err)
	}
	return nil
}
