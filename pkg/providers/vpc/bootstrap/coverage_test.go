/*
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

package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake" //nolint:staticcheck
	fakeClient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/cloudprovider/ibm"
)

func TestExtractVersionFromImage(t *testing.T) {
	provider := &VPCBootstrapProvider{}

	tests := []struct {
		name     string
		image    string
		expected string
	}{
		{
			name:     "image with v-prefixed tag",
			image:    "docker.io/calico/node:v3.26.0",
			expected: "v3.26.0",
		},
		{
			name:     "image without v prefix adds it",
			image:    "docker.io/calico/node:3.26.0",
			expected: "v3.26.0",
		},
		{
			name:     "image with latest tag",
			image:    "docker.io/calico/node:latest",
			expected: "latest",
		},
		{
			name:     "image with no tag returns latest",
			image:    "docker.io/calico/node",
			expected: "latest",
		},
		{
			name:     "registry with port in hostname",
			image:    "registry.example.com:5000/image:v1.2.3",
			expected: "v1.2.3",
		},
		{
			name:     "registry with port and no v prefix",
			image:    "registry.example.com:5000/myimage:2.0.0",
			expected: "v2.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := provider.extractVersionFromImage(tt.image)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDetectCNIPluginAndVersion(t *testing.T) {
	tests := []struct {
		name           string
		setupMocks     func(*fake.Clientset)
		expectedPlugin string
		expectedVer    string
		expectError    bool
		errorContains  string
	}{
		{
			name: "cilium detected from daemonset image tag",
			setupMocks: func(fc *fake.Clientset) {
				ds := &appsv1.DaemonSet{
					ObjectMeta: metav1.ObjectMeta{Name: "cilium", Namespace: "kube-system"},
					Spec: appsv1.DaemonSetSpec{
						Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "cilium"}},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"k8s-app": "cilium"}},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "cilium-agent", Image: "quay.io/cilium/cilium:v1.14.2"}},
							},
						},
					},
				}
				_, _ = fc.AppsV1().DaemonSets("kube-system").Create(context.Background(), ds, metav1.CreateOptions{})
			},
			expectedPlugin: "cilium",
			expectedVer:    "v1.14.2",
		},
		{
			name: "weave detected from daemonset image tag",
			setupMocks: func(fc *fake.Clientset) {
				ds := &appsv1.DaemonSet{
					ObjectMeta: metav1.ObjectMeta{Name: "weave-net", Namespace: "kube-system"},
					Spec: appsv1.DaemonSetSpec{
						Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"name": "weave-net"}},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"name": "weave-net"}},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "weave", Image: "weaveworks/weave-kube:2.8.1"}},
							},
						},
					},
				}
				_, _ = fc.AppsV1().DaemonSets("kube-system").Create(context.Background(), ds, metav1.CreateOptions{})
			},
			expectedPlugin: "weave",
			expectedVer:    "v2.8.1",
		},
		{
			name: "no CNI detected returns error",
			setupMocks: func(fc *fake.Clientset) {
				// No daemonsets, no configmaps
			},
			expectError:   true,
			errorContains: "no CNI plugin detected",
		},
		{
			name: "cilium with latest tag preserved",
			setupMocks: func(fc *fake.Clientset) {
				ds := &appsv1.DaemonSet{
					ObjectMeta: metav1.ObjectMeta{Name: "cilium", Namespace: "kube-system"},
					Spec: appsv1.DaemonSetSpec{
						Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "cilium"}},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"k8s-app": "cilium"}},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "cilium-agent", Image: "cilium/cilium:latest"}},
							},
						},
					},
				}
				_, _ = fc.AppsV1().DaemonSets("kube-system").Create(context.Background(), ds, metav1.CreateOptions{})
			},
			expectedPlugin: "cilium",
			expectedVer:    "latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
			k8sClient := fake.NewSimpleClientset()
			tt.setupMocks(k8sClient)

			provider := &VPCBootstrapProvider{k8sClient: k8sClient}

			plugin, ver, err := provider.detectCNIPluginAndVersion(ctx)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedPlugin, plugin)
				assert.Equal(t, tt.expectedVer, ver)
			}
		})
	}
}

func TestReportBootstrapStatus_UpdatePath(t *testing.T) {
	ctx := context.Background()
	//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
	k8sClient := fake.NewSimpleClientset()
	provider := &VPCBootstrapProvider{k8sClient: k8sClient}

	// First call creates the configmap via the create fallback path
	err := provider.ReportBootstrapStatus(ctx, "inst-1", "nc-1", "configuring", "kubelet-installed")
	assert.NoError(t, err)

	// Verify the initial configmap
	cm, err := k8sClient.CoreV1().ConfigMaps("karpenter").Get(ctx, "bootstrap-status-inst-1", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, "configuring", cm.Data["status"])
	assert.Equal(t, "kubelet-installed", cm.Data["phase"])

	// Second call should hit the Update path (configmap already exists)
	err = provider.ReportBootstrapStatus(ctx, "inst-1", "nc-1", "running", "kubelet-active")
	assert.NoError(t, err)

	// Verify updated values
	cm, err = k8sClient.CoreV1().ConfigMaps("karpenter").Get(ctx, "bootstrap-status-inst-1", metav1.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, "running", cm.Data["status"])
	assert.Equal(t, "kubelet-active", cm.Data["phase"])
}

func TestReportBootstrapStatus_NilK8sClient(t *testing.T) {
	provider := &VPCBootstrapProvider{k8sClient: nil}
	err := provider.ReportBootstrapStatus(context.Background(), "inst-1", "nc-1", "running", "active")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kubernetes client is required for status reporting")
}

func TestGetUserDataWithInstanceID_NilNodeClass(t *testing.T) {
	//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
	k8sClient := fake.NewSimpleClientset()
	provider := &VPCBootstrapProvider{k8sClient: k8sClient}

	_, err := provider.GetUserDataWithInstanceID(context.Background(), nil, types.NamespacedName{Name: "nc", Namespace: "default"}, "inst-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nodeClass cannot be nil")
}

func TestGetUserDataWithInstanceIDAndType_NilNodeClass(t *testing.T) {
	//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
	k8sClient := fake.NewSimpleClientset()
	provider := &VPCBootstrapProvider{k8sClient: k8sClient}

	_, err := provider.GetUserDataWithInstanceIDAndType(context.Background(), nil, types.NamespacedName{Name: "nc", Namespace: "default"}, "inst-1", "bx2-4x16")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nodeClass cannot be nil")
}

func TestGetNodeClaim_NilKubeClient(t *testing.T) {
	provider := &VPCBootstrapProvider{kubeClient: nil}
	_, err := provider.getNodeClaim(context.Background(), types.NamespacedName{Name: "nc-1", Namespace: "default"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kubeClient is nil")
}

func TestGetNodeClaim_NotFound(t *testing.T) {
	kubeClient := fakeClient.NewClientBuilder().Build()
	provider := &VPCBootstrapProvider{kubeClient: kubeClient}

	_, err := provider.getNodeClaim(context.Background(), types.NamespacedName{Name: "nonexistent", Namespace: "default"})
	assert.Error(t, err)
}

func TestNewVPCBootstrapProvider_FieldsSet(t *testing.T) {
	ibmClient := &ibm.Client{}
	//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
	k8sClient := fake.NewSimpleClientset()
	kubeClient := fakeClient.NewClientBuilder().Build()

	provider := NewVPCBootstrapProvider(ibmClient, k8sClient, kubeClient)

	assert.NotNil(t, provider)
	assert.Equal(t, ibmClient, provider.client)
	assert.Equal(t, k8sClient, provider.k8sClient)
	assert.Equal(t, kubeClient, provider.kubeClient)
	assert.NotNil(t, provider.vpcClientManager)
}

func TestGetLatestCNIVersion_UnsupportedPlugin(t *testing.T) {
	provider := &VPCBootstrapProvider{}
	_, err := provider.getLatestCNIVersion(context.Background(), "unknown")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported CNI plugin")
}

func TestGetUserDataWithInstanceIDAndType_DelegationChain(t *testing.T) {
	// Verify that GetUserData delegates to GetUserDataWithInstanceID which
	// delegates to GetUserDataWithInstanceIDAndType, and the nil check
	// catches it at the final level.
	//nolint:staticcheck // SA1019: NewSimpleClientset is deprecated but NewClientset requires generated apply configurations
	k8sClient := fake.NewSimpleClientset()
	provider := &VPCBootstrapProvider{k8sClient: k8sClient}

	// GetUserData -> GetUserDataWithInstanceID -> GetUserDataWithInstanceIDAndType
	_, err := provider.GetUserData(context.Background(), nil, types.NamespacedName{Name: "nc", Namespace: "default"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nodeClass cannot be nil")
}

func TestGetBootstrapStatus_NilK8sClient(t *testing.T) {
	provider := &VPCBootstrapProvider{k8sClient: nil}
	_, err := provider.GetBootstrapStatus(context.Background(), "inst-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "kubernetes client is required for status retrieval")
}
