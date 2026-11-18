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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	commonMock "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types/mock"
	vpcinstance "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/instance"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

type csrResolver struct {
	provider commonTypes.VPCInstanceProvider
}

func (r csrResolver) GetVPCInstanceProvider() (commonTypes.VPCInstanceProvider, error) {
	return r.provider, nil
}

func makeCSR(t *testing.T, serving bool, key interface{}, alter func(*x509.CertificateRequest)) *certificatesv1.CertificateSigningRequest {
	t.Helper()
	if key == nil {
		var err error
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
	}
	template := &x509.CertificateRequest{Subject: pkix.Name{CommonName: "system:node:claim-a", Organization: []string{"system:nodes"}}}
	signer, user := certificatesv1.KubeAPIServerClientKubeletSignerName, "system:bootstrap:abcdef"
	groups := []string{"system:authenticated", "system:bootstrappers", commonTypes.BootstrapGroup}
	usages := []certificatesv1.KeyUsage{certificatesv1.UsageDigitalSignature}
	if _, ok := key.(*rsa.PrivateKey); ok {
		usages = append(usages, certificatesv1.UsageKeyEncipherment)
	}
	if serving {
		signer, user, groups = certificatesv1.KubeletServingSignerName, "system:node:claim-a", []string{"system:nodes", "system:authenticated"}
		usages = append(usages, certificatesv1.UsageServerAuth)
		template.DNSNames, template.IPAddresses = []string{"claim-a"}, []net.IP{net.ParseIP("10.240.0.9")}
	} else {
		usages = append(usages, certificatesv1.UsageClientAuth)
	}
	if alter != nil {
		alter(template)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	require.NoError(t, err)
	return &certificatesv1.CertificateSigningRequest{ObjectMeta: metav1.ObjectMeta{Name: "request-a", UID: "csr-uid", ResourceVersion: "1"}, Spec: certificatesv1.CertificateSigningRequestSpec{
		Request: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), SignerName: signer, Username: user, Groups: groups, Usages: usages,
	}}
}

func TestCSRRejectsAdditionalAuthority(t *testing.T) {
	uri, uriErr := url.Parse("spiffe://foreign/workload")
	require.NoError(t, uriErr)
	for _, test := range []struct {
		name    string
		serving bool
		alter   func(*x509.CertificateRequest)
		change  func(*certificatesv1.CertificateSigningRequest)
	}{
		{name: "client SAN", alter: func(r *x509.CertificateRequest) { r.DNSNames = []string{"other"} }},
		{name: "other node", alter: func(r *x509.CertificateRequest) { r.Subject.CommonName = "system:node:other" }},
		{name: "privileged organization", alter: func(r *x509.CertificateRequest) {
			r.Subject.Organization = append(r.Subject.Organization, "system:masters")
		}},
		{name: "extra subject", alter: func(r *x509.CertificateRequest) { r.Subject.Country = []string{"NL"} }},
		{name: "client custom attribute", alter: func(r *x509.CertificateRequest) {
			//nolint:staticcheck // Arbitrary PKCS#10 attributes must be rejected independently of extensions.
			r.Attributes = []pkix.AttributeTypeAndValueSET{{Type: asn1.ObjectIdentifier{1, 2, 3, 4}, Value: [][]pkix.AttributeTypeAndValue{{{Type: asn1.ObjectIdentifier{1, 2, 3, 5}, Value: "extra"}}}}}
		}},
		{name: "serving URI", serving: true, alter: func(r *x509.CertificateRequest) { r.URIs = []*url.URL{uri} }},
		{name: "serving email", serving: true, alter: func(r *x509.CertificateRequest) { r.EmailAddresses = []string{"a@example.com"} }},
		{name: "serving extra extension", serving: true, alter: func(r *x509.CertificateRequest) {
			r.ExtraExtensions = []pkix.Extension{{Id: asn1.ObjectIdentifier{2, 5, 29, 19}, Value: []byte{48, 0}}}
		}},
		{name: "extra usage", change: func(c *certificatesv1.CertificateSigningRequest) {
			c.Spec.Usages = append(c.Spec.Usages, certificatesv1.UsageCertSign)
		}},
		{name: "duplicate usage", change: func(c *certificatesv1.CertificateSigningRequest) {
			c.Spec.Usages = append(c.Spec.Usages, c.Spec.Usages[0])
		}},
		{name: "authenticated UID", change: func(c *certificatesv1.CertificateSigningRequest) { c.Spec.UID = "another" }},
		{name: "extra credential", change: func(c *certificatesv1.CertificateSigningRequest) {
			c.Spec.Extra = map[string]certificatesv1.ExtraValue{"other": {"value"}}
		}},
		{name: "unbounded lifetime", change: func(c *certificatesv1.CertificateSigningRequest) {
			v := int32(400 * 24 * 60 * 60)
			c.Spec.ExpirationSeconds = &v
		}},
		{name: "second PEM", change: func(c *certificatesv1.CertificateSigningRequest) {
			c.Spec.Request = append(c.Spec.Request, c.Spec.Request...)
		}},
		{name: "garbage suffix", change: func(c *certificatesv1.CertificateSigningRequest) {
			c.Spec.Request = append(c.Spec.Request, []byte("garbage")...)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := makeCSR(t, test.serving, nil, test.alter)
			if test.change != nil {
				test.change(c)
			}
			_, err := validateCSR(c, "system:node:claim-a")
			require.Error(t, err)
		})
	}
	for _, serving := range []bool{false, true} {
		c := makeCSR(t, serving, nil, nil)
		_, err := validateCSR(c, "system:node:claim-a")
		require.NoError(t, err)
	}
	rsaKey, keyErr := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, keyErr)
	for _, serving := range []bool{false, true} {
		_, err := validateCSR(makeCSR(t, serving, rsaKey, nil), "system:node:claim-a")
		require.NoError(t, err)
	}
	weakKey, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	_, err = validateCSR(makeCSR(t, false, weakKey, nil), "system:node:claim-a")
	require.Error(t, err)
}

func csrFixture(t *testing.T, serving bool) (*CSRController, *commonMock.MockVPCInstanceProvider, *karpv1.NodeClaim, *corev1.Node, *certificatesv1.CertificateSigningRequest) {
	t.Helper()
	claim := &karpv1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim-a", UID: "claim-uid", Annotations: map[string]string{}}}
	launch, err := json.Marshal(map[string]interface{}{"Version": 1, "MinimumWriterVersion": 1, "Name": ownership.InstanceName("cluster-uid", "claim-uid"), "ClusterUID": "cluster-uid", "ClaimUID": "claim-uid", "ClassUID": "class-uid", "AccountID": "account", "Region": "us-south"})
	require.NoError(t, err)
	claim.Annotations[vpcinstance.LaunchAnnotation] = string(launch)
	owner := *metav1.NewControllerRef(claim, schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}.WithKind("NodeClaim"))
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bootstrap-token-abcdef", Namespace: "kube-system", UID: "secret-uid", Labels: map[string]string{commonTypes.BootstrapTokenLabel: "true", ownership.ClaimUIDLabel: string(claim.UID), ownership.ClusterUIDLabel: "cluster-uid"}, Annotations: map[string]string{commonTypes.BootstrapClaimAnnotation: claim.Name}, OwnerReferences: []metav1.OwnerReference{owner}}, Type: corev1.SecretTypeBootstrapToken, Data: map[string][]byte{"token-id": []byte("abcdef"), "token-secret": []byte("0123456789abcdef"), "auth-extra-groups": []byte(commonTypes.BootstrapGroup), "usage-bootstrap-authentication": []byte("true"), "expiration": []byte(time.Now().Add(time.Hour).UTC().Format(time.RFC3339))}}
	record := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-bootstrap-claim-uid", Namespace: "kube-system", UID: "record-uid", OwnerReferences: []metav1.OwnerReference{owner}}, Data: map[string]string{"version": "1", "claimUID": string(claim.UID), "clusterUID": "cluster-uid", "tokenID": "abcdef", "secretUID": string(secret.UID)}}
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	scheme.AddKnownTypes(schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}, &karpv1.NodeClaim{}, &karpv1.NodeClaimList{})
	objects := []client.Object{claim, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}}}
	var node *corev1.Node
	if serving {
		claim.Status.ProviderID = "ibm:///us-south/instance-id"
		claim.Status.NodeName = claim.Name
		claim.StatusConditions().SetTrue(karpv1.ConditionTypeLaunched)
		claim.StatusConditions().SetTrue(karpv1.ConditionTypeRegistered)
		node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: claim.Name, UID: "node-uid", OwnerReferences: []metav1.OwnerReference{owner}}, Spec: corev1.NodeSpec{ProviderID: claim.Status.ProviderID}, Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeHostName, Address: claim.Name}, {Type: corev1.NodeInternalIP, Address: "10.240.0.9"}}}}
		objects = append(objects, node)
	}
	csr := makeCSR(t, serving, nil, nil)
	provider := commonMock.NewMockVPCInstanceProvider(gomock.NewController(t))
	return &CSRController{reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), client: kfake.NewSimpleClientset(secret, record, csr), providers: csrResolver{provider}}, provider, claim, node, csr
}

func csrVM() *vpcv1.Instance {
	return &vpcv1.Instance{Status: core.StringPtr("running"), PrimaryNetworkInterface: &vpcv1.NetworkInterfaceInstanceContextReference{PrimaryIP: &vpcv1.ReservedIPReference{Address: core.StringPtr("10.240.0.9")}}}
}

func TestCSRApprovalVerifiesBothIdentities(t *testing.T) {
	for _, serving := range []bool{false, true} {
		t.Run(fmt.Sprintf("serving=%t", serving), func(t *testing.T) {
			c, p, _, _, csr := csrFixture(t, serving)
			p.EXPECT().VerifyLaunchInstance(gomock.Any(), gomock.Any()).Return(csrVM(), nil)
			if serving {
				p.EXPECT().VerifyLaunchNetworkAddresses(gomock.Any(), gomock.Any(), gomock.Any()).Return([]string{"10.240.0.9"}, []string{}, nil)
			}
			_, err := c.Reconcile(context.Background(), ctrl.Request{NamespacedName: k8stypes.NamespacedName{Name: csr.Name}})
			require.NoError(t, err)
			fresh, err := c.client.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr.Name, metav1.GetOptions{})
			require.NoError(t, err)
			require.Len(t, fresh.Status.Conditions, 1)
			require.Equal(t, certificatesv1.CertificateApproved, fresh.Status.Conditions[0].Type)
		})
	}
}

func TestServingCSRRetriesUntilRegistrationAddsOwnership(t *testing.T) {
	c, p, _, node, csr := csrFixture(t, true)
	owner := node.OwnerReferences
	node.OwnerReferences = nil
	require.NoError(t, c.reader.(client.Client).Update(context.Background(), node))
	request := ctrl.Request{NamespacedName: k8stypes.NamespacedName{Name: csr.Name}}
	result, err := c.Reconcile(context.Background(), request)
	require.NoError(t, err)
	require.Positive(t, result.RequeueAfter)
	fresh, err := c.client.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Empty(t, fresh.Status.Conditions)
	node.OwnerReferences = owner
	require.NoError(t, c.reader.(client.Client).Update(context.Background(), node))
	p.EXPECT().VerifyLaunchInstance(gomock.Any(), gomock.Any()).Return(csrVM(), nil)
	p.EXPECT().VerifyLaunchNetworkAddresses(gomock.Any(), gomock.Any(), gomock.Any()).Return([]string{"10.240.0.9"}, []string{}, nil)
	_, err = c.Reconcile(context.Background(), request)
	require.NoError(t, err)
	fresh, err = c.client.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, fresh.Status.Conditions, 1)
	require.Equal(t, certificatesv1.CertificateApproved, fresh.Status.Conditions[0].Type)
}

func TestCSRDoesNotApproveChangedOrUnprovenIdentity(t *testing.T) {
	for _, scenario := range []string{"claim replacement", "secret replacement", "record replacement", "record token rotation", "cloud ownership absent", "node replacement", "unassigned IP", "foreign requester"} {
		t.Run(scenario, func(t *testing.T) {
			serving := scenario == "node replacement" || scenario == "unassigned IP"
			c, p, claim, node, csr := csrFixture(t, serving)
			if scenario == "foreign requester" {
				csr.Spec.Groups = append(csr.Spec.Groups, "system:masters")
				_, err := c.client.CertificatesV1().CertificateSigningRequests().Update(context.Background(), csr, metav1.UpdateOptions{})
				require.NoError(t, err)
			} else {
				p.EXPECT().VerifyLaunchInstance(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, *karpv1.NodeClaim) (*vpcv1.Instance, error) {
					ctx := context.Background()
					switch scenario {
					case "cloud ownership absent":
						return nil, fmt.Errorf("missing ownership tags")
					case "claim replacement":
						require.NoError(t, c.reader.(client.Client).Delete(ctx, claim))
						replacement := claim.DeepCopy()
						replacement.UID = "replacement"
						replacement.ResourceVersion = ""
						require.NoError(t, c.reader.(client.Client).Create(ctx, replacement))
					case "node replacement":
						require.NoError(t, c.reader.(client.Client).Delete(ctx, node))
						replacement := node.DeepCopy()
						replacement.UID = "replacement"
						replacement.ResourceVersion = ""
						require.NoError(t, c.reader.(client.Client).Create(ctx, replacement))
					case "secret replacement":
						s, err := c.client.CoreV1().Secrets("kube-system").Get(ctx, "bootstrap-token-abcdef", metav1.GetOptions{})
						require.NoError(t, err)
						s.UID = "replacement"
						_, err = c.client.CoreV1().Secrets("kube-system").Update(ctx, s, metav1.UpdateOptions{})
						require.NoError(t, err)
					case "record replacement", "record token rotation":
						r, err := c.client.CoreV1().ConfigMaps("kube-system").Get(ctx, "karpenter-bootstrap-claim-uid", metav1.GetOptions{})
						require.NoError(t, err)
						if scenario == "record replacement" {
							r.UID = "replacement"
						} else {
							r.Data["tokenID"] = "fedcba"
						}
						_, err = c.client.CoreV1().ConfigMaps("kube-system").Update(ctx, r, metav1.UpdateOptions{})
						require.NoError(t, err)
					}
					return csrVM(), nil
				})
				if serving {
					private := []string{"10.240.0.9"}
					if scenario == "unassigned IP" {
						private = []string{"10.240.0.10"}
					}
					p.EXPECT().VerifyLaunchNetworkAddresses(gomock.Any(), gomock.Any(), gomock.Any()).Return(private, []string{}, nil)
				}
			}
			_, err := c.Reconcile(context.Background(), ctrl.Request{NamespacedName: k8stypes.NamespacedName{Name: csr.Name}})
			require.NoError(t, err)
			fresh, err := c.client.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr.Name, metav1.GetOptions{})
			require.NoError(t, err)
			for _, condition := range fresh.Status.Conditions {
				require.NotEqual(t, certificatesv1.CertificateApproved, condition.Type)
			}
		})
	}
}

func TestCSRDecisionUsesAtomicApprovalPreconditions(t *testing.T) {
	for _, change := range []string{"none", "uid", "resourceVersion", "request"} {
		t.Run(change, func(t *testing.T) {
			csr := makeCSR(t, false, nil, nil)
			fresh := csr.DeepCopy()
			switch change {
			case "uid":
				fresh.UID = "replacement"
			case "resourceVersion":
				fresh.ResourceVersion = "2"
			case "request":
				fresh.Spec.Request = []byte("replacement")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPatch, r.Method)
				assert.True(t, strings.HasSuffix(r.URL.Path, "/approval"))
				assert.Equal(t, "application/json-patch+json", r.Header.Get("Content-Type"))
				body, err := io.ReadAll(r.Body)
				if !assert.NoError(t, err) {
					return
				}
				patch, err := jsonpatch.DecodePatch(body)
				if !assert.NoError(t, err) {
					return
				}
				current, err := json.Marshal(fresh)
				if !assert.NoError(t, err) {
					return
				}
				updated, err := patch.Apply(current)
				w.Header().Set("Content-Type", "application/json")
				if err != nil {
					w.WriteHeader(http.StatusConflict)
					assert.NoError(t, json.NewEncoder(w).Encode(&metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: metav1.StatusReasonConflict, Code: 409}))
					return
				}
				_, err = w.Write(updated)
				assert.NoError(t, err)
			}))
			defer server.Close()
			kc, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			require.NoError(t, err)
			err = (&CSRController{client: kc}).writeDecision(context.Background(), csr, certificatesv1.CertificateApproved, "VerifiedNodeClaim", "verified")
			if change == "none" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestServingCSRRejectsAddressesOutsideTheNode(t *testing.T) {
	for name, alter := range map[string]func(*x509.CertificateRequest){
		"other node DNS name": func(r *x509.CertificateRequest) { r.DNSNames = []string{"claim-b"} },
		"second DNS name":     func(r *x509.CertificateRequest) { r.DNSNames = append(r.DNSNames, "claim-b") },
		"extra IP":            func(r *x509.CertificateRequest) { r.IPAddresses = append(r.IPAddresses, net.ParseIP("10.240.0.10")) },
	} {
		t.Run(name, func(t *testing.T) {
			c, p, _, _, original := csrFixture(t, true)
			csr := makeCSR(t, true, nil, alter)
			original.Spec.Request = csr.Spec.Request
			_, err := c.client.CertificatesV1().CertificateSigningRequests().Update(context.Background(), original, metav1.UpdateOptions{})
			require.NoError(t, err)
			p.EXPECT().VerifyLaunchInstance(gomock.Any(), gomock.Any()).Return(csrVM(), nil).AnyTimes()
			p.EXPECT().VerifyLaunchNetworkAddresses(gomock.Any(), gomock.Any(), gomock.Any()).Return([]string{"10.240.0.9"}, []string{}, nil).AnyTimes()
			_, _ = c.Reconcile(context.Background(), ctrl.Request{NamespacedName: k8stypes.NamespacedName{Name: original.Name}})
			fresh, err := c.client.CertificatesV1().CertificateSigningRequests().Get(context.Background(), original.Name, metav1.GetOptions{})
			require.NoError(t, err)
			for _, condition := range fresh.Status.Conditions {
				require.NotEqual(t, certificatesv1.CertificateApproved, condition.Type)
			}
		})
	}
}

func TestCSRRejectsLaunchFromAnotherCluster(t *testing.T) {
	for _, serving := range []bool{false, true} {
		t.Run(fmt.Sprintf("serving=%t", serving), func(t *testing.T) {
			c, p, claim, _, csr := csrFixture(t, serving)
			launch, err := json.Marshal(map[string]interface{}{"Version": 1, "MinimumWriterVersion": 1, "Name": ownership.InstanceName("other-cluster", "claim-uid"), "ClusterUID": "other-cluster", "ClaimUID": "claim-uid", "ClassUID": "class-uid", "AccountID": "account", "Region": "us-south"})
			require.NoError(t, err)
			claim.Annotations[vpcinstance.LaunchAnnotation] = string(launch)
			require.NoError(t, c.reader.(client.Client).Update(context.Background(), claim))
			p.EXPECT().VerifyLaunchInstance(gomock.Any(), gomock.Any()).Return(csrVM(), nil).AnyTimes()
			p.EXPECT().VerifyLaunchNetworkAddresses(gomock.Any(), gomock.Any(), gomock.Any()).Return([]string{"10.240.0.9"}, []string{}, nil).AnyTimes()
			_, _ = c.Reconcile(context.Background(), ctrl.Request{NamespacedName: k8stypes.NamespacedName{Name: csr.Name}})
			fresh, err := c.client.CertificatesV1().CertificateSigningRequests().Get(context.Background(), csr.Name, metav1.GetOptions{})
			require.NoError(t, err)
			for _, condition := range fresh.Status.Conditions {
				require.NotEqual(t, certificatesv1.CertificateApproved, condition.Type)
			}
		})
	}
}
