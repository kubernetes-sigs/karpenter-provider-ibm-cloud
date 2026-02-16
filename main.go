package main

import (
	ibmcloud "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/controllers"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/operator"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/operator/options"

	_ "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/metrics"

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
		ctx,
		op.GetClient(),
		op.EventRecorder,
		op.ProviderFactory.GetClient(),
		op.ProviderFactory.GetInstanceTypeProvider(),
		op.ProviderFactory.GetSubnetProvider(),
		circuitBreakerConfig,
	)
	cloudProvider := metrics.Decorate(ibmCloudProvider)
	clusterState := state.NewCluster(op.Clock, op.GetClient(), cloudProvider)

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
			op.ProviderFactory.GetSubnetProvider(),
			op.ProviderFactory.GetClient(),
		)...).
		Start(ctx)
}
