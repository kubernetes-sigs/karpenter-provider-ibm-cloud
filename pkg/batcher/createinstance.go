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

package batcher

import (
	"context"
	"fmt"
	"time"

	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/vpcclient"
	"github.com/mitchellh/hashstructure/v2"
)

type CreateInstanceInput struct {
	Prototype vpcv1.InstancePrototypeIntf
	Zone      string
}

type CreateInstanceBatcher struct {
	batcher          *Batcher[CreateInstanceInput, vpcv1.Instance]
	vpcClientManager *vpcclient.Manager
}

func NewCreateInstanceBatcher(ctx context.Context, vpcClientManager *vpcclient.Manager) *CreateInstanceBatcher {
	c := &CreateInstanceBatcher{vpcClientManager: vpcClientManager}

	options := Options[CreateInstanceInput, vpcv1.Instance]{
		Name:          "create_instance",
		IdleTimeout:   50 * time.Millisecond,
		MaxTimeout:    1 * time.Second,
		MaxItems:      100,
		RequestHasher: createInstanceHasher,
		BatchExecutor: c.execCreateInstanceBatch(),
	}

	c.batcher = NewBatcher(ctx, options)
	return c
}

func (b *CreateInstanceBatcher) CreateInstance(ctx context.Context, prototype vpcv1.InstancePrototypeIntf, zone string) (*vpcv1.Instance, error) {
	res := b.batcher.Add(ctx, &CreateInstanceInput{
		Prototype: prototype,
		Zone:      zone,
	})
	return res.Output, res.Err
}

func createInstanceHasher(_ context.Context, req *CreateInstanceInput) (uint64, error) {
	if req == nil {
		return 0, nil
	}
	return hashstructure.Hash(req.Zone, hashstructure.FormatV2, nil)
}

func (b *CreateInstanceBatcher) execCreateInstanceBatch() BatchExecutor[CreateInstanceInput, vpcv1.Instance] {
	return func(ctx context.Context, inputs []*CreateInstanceInput) []Result[vpcv1.Instance] {
		results := make([]Result[vpcv1.Instance], len(inputs))

		vpcClient, err := b.vpcClientManager.GetVPCClient(ctx)
		if err != nil {
			for i := range inputs {
				results[i] = Result[vpcv1.Instance]{Err: fmt.Errorf("getting VPC client: %w", err)}
			}
			return results
		}

		for i, req := range inputs {
			if req == nil || req.Prototype == nil {
				results[i] = Result[vpcv1.Instance]{Err: fmt.Errorf("nil instance prototype")}
				continue
			}
			out, err := vpcClient.CreateInstance(ctx, req.Prototype)
			results[i] = Result[vpcv1.Instance]{Output: out, Err: err}
		}

		return results
	}
}
