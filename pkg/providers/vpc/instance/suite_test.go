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

package instance_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
	"github.com/pfeifferj/karpenter-provider-ibm-cloud/pkg/providers/vpc/instance"
)

func TestVPCInstanceProvider(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "VPC Instance Provider")
}

var _ = Describe("VPC Instance Provider", func() {
	var (
		client     *ibm.Client
		kubeClient *fake.ClientBuilder
	)

	BeforeEach(func() {
		client = &ibm.Client{}
		kubeClient = fake.NewClientBuilder()
	})

	Describe("Constructor", func() {
		It("should create a new VPC instance provider with valid inputs", func() {
			fakeClient := kubeClient.Build()
			provider, err := instance.NewVPCInstanceProvider(client, fakeClient)

			Expect(err).ToNot(HaveOccurred())
			Expect(provider).ToNot(BeNil())
		})

		It("should return an error with nil IBM client", func() {
			fakeClient := kubeClient.Build()
			provider, err := instance.NewVPCInstanceProvider(nil, fakeClient)

			Expect(err).To(HaveOccurred())
			Expect(provider).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("IBM client cannot be nil"))
		})

		It("should return an error with nil kube client", func() {
			provider, err := instance.NewVPCInstanceProvider(client, nil)

			Expect(err).To(HaveOccurred())
			Expect(provider).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("kubernetes client cannot be nil"))
		})
	})

	Describe("Options Pattern", func() {
		It("should apply kubernetes client option correctly", func() {
			fakeClient := kubeClient.Build()
			provider, err := instance.NewVPCInstanceProvider(client, fakeClient)

			Expect(err).ToNot(HaveOccurred())
			Expect(provider).ToNot(BeNil())
		})
	})
})
