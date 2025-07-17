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
//+kubebuilder:rbac:groups=karpenter.ibm.sh,resources=ibmnodeclasses,verbs=get;list;watch;patch;update
//+kubebuilder:rbac:groups=karpenter.ibm.sh,resources=ibmnodeclasses/status,verbs=get;update;patch
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;create;delete;update;patch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;list;create;update;patch
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch
//+kubebuilder:rbac:groups=storage.k8s.io,resources=csinodes,verbs=get;list;watch
//+kubebuilder:rbac:groups=storage.k8s.io,resources=volumeattachments,verbs=get;list;watch
//+kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

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

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cache"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/bootstrap"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/interruption"
	nodeorphancleanup "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/node/orphancleanup"
	nodeclaimgc "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/nodeclaim/garbagecollection"
	nodeclaimregistration "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/nodeclaim/registration"
	nodeclaimtagging "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/nodeclaim/tagging"
	nodeclasshash "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/nodeclass/hash"
	nodeclaasstatus "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/nodeclass/status"
	nodeclasstermination "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/nodeclass/termination"
	providersinstancetype "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/providers/instancetype"
	controllerspricing "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers/providers/pricing"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/common/instancetype"
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
	ibmClient *ibm.Client,
) []controller.Controller {
	// Create event recorder adapter
	recorderAdapter := &RecorderAdapter{recorder}
	logger := log.FromContext(ctx).WithName("controllers")

	controllers := []controller.Controller{}

	// Add IBM-specific controllers
	if hashCtrl, err := nodeclasshash.NewController(kubeClient); err != nil {
		logger.Error(err, "failed to create hash controller")
	} else {
		controllers = append(controllers, hashCtrl)
	}

	if statusCtrl, err := nodeclaasstatus.NewController(kubeClient); err != nil {
		logger.Error(err, "failed to create status controller")
	} else {
		controllers = append(controllers, statusCtrl)
	}

	if terminationCtrl, err := nodeclasstermination.NewController(kubeClient, recorderAdapter); err != nil {
		logger.Error(err, "failed to create termination controller")
	} else {
		controllers = append(controllers, terminationCtrl)
	}

	if pricingCtrl, err := controllerspricing.NewController(nil); err != nil {
		logger.Error(err, "failed to create pricing controller")
	} else {
		controllers = append(controllers, pricingCtrl)
	}

	// Add garbage collection controller
	garbageCollectionCtrl := nodeclaimgc.NewController(kubeClient, cloudProvider)
	controllers = append(controllers, garbageCollectionCtrl)

	// Add NodeClaim registration controller for proper labeling and status management
	if registrationCtrl, err := nodeclaimregistration.NewController(kubeClient); err != nil {
		logger.Error(err, "failed to create registration controller")
	} else {
		controllers = append(controllers, registrationCtrl)
	}

	// Add tagging controller (VPC mode only)
	if taggingCtrl, err := nodeclaimtagging.NewController(kubeClient); err != nil {
		logger.Error(err, "failed to create tagging controller")
	} else {
		controllers = append(controllers, taggingCtrl)
	}

	// Add instance type controller
	if instanceTypeCtrl, err := providersinstancetype.NewController(); err != nil {
		logger.Error(err, "failed to create instance type controller")
	} else {
		controllers = append(controllers, instanceTypeCtrl)
	}

	// Add interruption controller (always add for now)
	interruptionCtrl := interruption.NewController(kubeClient, recorderAdapter, unavailableOfferings)
	controllers = append(controllers, interruptionCtrl)

	// Add orphaned node cleanup controller (only if enabled and IBM client available)
	if ibmClient != nil && isOrphanCleanupEnabled() {
		orphanCleanupCtrl := nodeorphancleanup.NewController(kubeClient, ibmClient)
		controllers = append(controllers, orphanCleanupCtrl)
		logger.Info("enabled orphaned node cleanup controller")
	} else if ibmClient == nil {
		logger.Info("IBM client not available, skipping orphaned node cleanup controller")
	} else {
		logger.Info("orphaned node cleanup controller is disabled")
	}

	return controllers
}

// isOrphanCleanupEnabled checks if orphan cleanup is enabled via environment variable
func isOrphanCleanupEnabled() bool {
	return os.Getenv("KARPENTER_ENABLE_ORPHAN_CLEANUP") == "true"
}

// RegisterBootstrapController adds the bootstrap token controller to the manager
func RegisterBootstrapController(mgr manager.Manager) error {
	bootstrapCtrl := bootstrap.NewTokenController(mgr)
	if err := bootstrapCtrl.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setting up bootstrap token controller: %w", err)
	}
	return nil
}
