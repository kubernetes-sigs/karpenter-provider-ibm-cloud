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
package main

import (
	"os"

	ibmcloud "github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/controllers"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/operator"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/operator/options"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/metrics"
	corecontrollers "sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	coreoperator "sigs.k8s.io/karpenter/pkg/operator"
)

func main() {
	// Create core operator (handles options context properly)
	coreCtx, coreOp := coreoperator.NewOperator()
	ctx, op := operator.NewOperator(coreCtx, coreOp)

	// Get options from context and extract circuit breaker config
	var circuitBreakerConfig *ibmcloud.CircuitBreakerConfig
	if opts := options.FromContext(ctx); opts != nil {
		cbConfig := opts.GetCircuitBreakerConfig()
		if cbConfig != nil {
			// Convert options.CircuitBreakerConfig to cloudprovider.CircuitBreakerConfig
			circuitBreakerConfig = &ibmcloud.CircuitBreakerConfig{
				FailureThreshold:       cbConfig.FailureThreshold,
				FailureWindow:          cbConfig.FailureWindow,
				RecoveryTimeout:        cbConfig.RecoveryTimeout,
				HalfOpenMaxRequests:    cbConfig.HalfOpenMaxRequests,
				RateLimitPerMinute:     cbConfig.RateLimitPerMinute,
				MaxConcurrentInstances: cbConfig.MaxConcurrentInstances,
			}
		}
	}

	// Create IBM cloud provider using providers from the factory
	ibmCloudProvider := ibmcloud.New(
		op.GetClient(),
		op.EventRecorder,
		op.ProviderFactory.GetClient(),
		op.ProviderFactory.GetInstanceTypeProvider(),
		op.ProviderFactory.GetSubnetProvider(),
		circuitBreakerConfig,
	)
	cloudProvider := metrics.Decorate(ibmCloudProvider)
	clusterState := state.NewCluster(op.Clock, op.GetClient(), cloudProvider)

	// Register bootstrap controller
	if err := controllers.RegisterBootstrapController(op.Manager); err != nil {
		log.FromContext(ctx).Error(err, "failed to register bootstrap controller")
		os.Exit(1)
	}

	// Register controllers using the proper operator pattern
	op.
		WithControllers(ctx, corecontrollers.NewControllers(
			ctx,
			op.Manager,
			op.Clock,
			op.GetClient(),
			op.EventRecorder,
			cloudProvider,
			ibmCloudProvider,
			clusterState,
			op.InstanceTypeStore,
		)...).
		WithControllers(ctx, controllers.NewControllers(
			ctx,
			op.Manager,
			op.Clock,
			op.GetClient(),
			op.KubernetesClient,
			op.EventRecorder,
			op.UnavailableOfferings,
			cloudProvider,
			op.ProviderFactory.GetInstanceTypeProvider(),
			op.ProviderFactory.GetClient(),
		)...).
		Start(ctx)
}
