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

//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=get;list;watch
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests/approval,verbs=update;patch
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=signers,resourceNames=kubernetes.io/kube-apiserver-client-kubelet;kubernetes.io/kubelet-serving,verbs=approve

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strings"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	commonTypes "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/common/types"
	vpcinstance "github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/providers/vpc/instance"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

type CSRProviderResolver interface {
	GetVPCInstanceProvider() (commonTypes.VPCInstanceProvider, error)
}

type CSRController struct {
	client    kubernetes.Interface
	reader    client.Reader
	providers CSRProviderResolver
	recorder  record.EventRecorder
}

var csrTokenIDPattern = regexp.MustCompile(`^[a-z0-9]{6}$`)
var credentialIDPattern = regexp.MustCompile(`^X509SHA256=[0-9a-f]{64}$`)

func NewCSRController(mgr manager.Manager, providers CSRProviderResolver) *CSRController {
	return &CSRController{client: kubernetes.NewForConfigOrDie(mgr.GetConfig()), reader: mgr.GetAPIReader(), providers: providers, recorder: eventRecorder{mgr.GetEventRecorder("bootstrap-csr")}}
}

func (c *CSRController) SetupWithManager(mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("bootstrap.csr").For(&certificatesv1.CertificateSigningRequest{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).Complete(c)
}

func pendingCSR(csr *certificatesv1.CertificateSigningRequest) bool {
	return csr.UID != "" && csr.DeletionTimestamp.IsZero() && len(csr.Status.Certificate) == 0 && len(csr.Status.Conditions) == 0
}

func (c *CSRController) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	csr, err := c.client.CertificatesV1().CertificateSigningRequests().Get(ctx, request.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if !pendingCSR(csr) || (csr.Spec.SignerName != certificatesv1.KubeAPIServerClientKubeletSignerName && csr.Spec.SignerName != certificatesv1.KubeletServingSignerName) {
		return ctrl.Result{}, nil
	}
	claim, node, token, err := c.resolveIdentity(ctx, csr)
	if err != nil {
		return ctrl.Result{}, err
	}
	if claim == nil {
		if csr.Spec.SignerName == certificatesv1.KubeletServingSignerName && strings.HasPrefix(csr.Spec.Username, "system:node:") &&
			exactStrings(csr.Spec.Groups, []string{"system:authenticated", "system:nodes"}) {
			pendingClaim := &karpv1.NodeClaim{}
			if pendingErr := c.reader.Get(ctx, client.ObjectKey{Name: strings.TrimPrefix(csr.Spec.Username, "system:node:")}, pendingClaim); pendingErr != nil {
				return ctrl.Result{}, client.IgnoreNotFound(pendingErr)
			}
			if pendingClaim.UID != "" && pendingClaim.DeletionTimestamp.IsZero() && pendingClaim.Annotations[vpcinstance.LaunchAnnotation] != "" {
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
		return ctrl.Result{}, nil
	}
	if !claim.DeletionTimestamp.IsZero() || claim.UID == "" || claim.Annotations[vpcinstance.LaunchAnnotation] == "" {
		return ctrl.Result{}, nil
	}
	parsed, err := validateCSR(csr, "system:node:"+claim.Name)
	if err != nil {
		return c.reject(ctx, csr, err)
	}
	identity, err := vpcinstance.ReadLaunchIdentity(claim)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	clusterUID, err := ownership.ClusterUID(ctx, c.reader)
	if err != nil {
		return ctrl.Result{}, err
	}
	if identity.ClusterUID != clusterUID {
		return c.reject(ctx, csr, fmt.Errorf("launch belongs to another cluster"))
	}
	var credentialRecord *corev1.ConfigMap
	if token != nil {
		if !commonTypes.BootstrapTokenOwnedBy(token, claim, clusterUID) {
			return c.reject(ctx, csr, fmt.Errorf("bootstrap credential ownership differs from claim"))
		}
		record, credentialErr := c.client.CoreV1().ConfigMaps("kube-system").Get(ctx, "karpenter-bootstrap-"+string(claim.UID), metav1.GetOptions{})
		if credentialErr != nil {
			return ctrl.Result{}, client.IgnoreNotFound(credentialErr)
		}
		if token.UID == "" || record.Data["version"] != "1" || record.Data["claimUID"] != string(claim.UID) || record.Data["clusterUID"] != clusterUID ||
			record.Data["tokenID"] != string(token.Data["token-id"]) || record.Data["secretUID"] != string(token.UID) ||
			len(record.OwnerReferences) != 1 || record.OwnerReferences[0].UID != claim.UID || record.OwnerReferences[0].Name != claim.Name ||
			record.OwnerReferences[0].Kind != "NodeClaim" || record.OwnerReferences[0].APIVersion != "karpenter.sh/v1" {
			return c.reject(ctx, csr, fmt.Errorf("bootstrap credential was not pinned by its claim"))
		}
		credentialRecord = record
		expires, expirationErr := time.Parse(time.RFC3339, string(token.Data["expiration"]))
		if expirationErr != nil || !expires.After(time.Now()) || expires.After(time.Now().Add(commonTypes.BootstrapTokenTTL+time.Minute)) {
			return c.reject(ctx, csr, fmt.Errorf("bootstrap credential is expired or unbounded"))
		}
		if claim.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue() {
			return c.reject(ctx, csr, fmt.Errorf("registered workers must use certificate renewal"))
		}
		if policyErr := commonTypes.CheckBootstrapApprovalPolicy(ctx, c.client, string(token.Data["token-id"])); policyErr != nil {
			return ctrl.Result{}, policyErr
		}
	} else if !claim.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue() || !claim.StatusConditions().Get(karpv1.ConditionTypeLaunched).IsTrue() ||
		claim.Status.NodeName != node.Name || claim.Status.ProviderID == "" || claim.Status.ProviderID != node.Spec.ProviderID {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	provider, err := c.providers.GetVPCInstanceProvider()
	if err != nil {
		return ctrl.Result{}, err
	}
	vm, err := provider.VerifyLaunchInstance(ctx, claim)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if vm == nil || vm.Status == nil || *vm.Status != "running" {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if node != nil {
		if vm.PrimaryNetworkInterface == nil || vm.PrimaryNetworkInterface.PrimaryIP == nil || vm.PrimaryNetworkInterface.PrimaryIP.Address == nil {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		private, floating, err := provider.VerifyLaunchNetworkAddresses(ctx, claim, vm)
		if err != nil {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		if err := validateServingAddresses(parsed, node, vm.PrimaryNetworkInterface.PrimaryIP.Address, private, floating); err != nil {
			return c.reject(ctx, csr, err)
		}
	}
	freshClaim := &karpv1.NodeClaim{}
	if err := c.reader.Get(ctx, client.ObjectKeyFromObject(claim), freshClaim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !sameClaimIdentity(claim, freshClaim) || !freshClaim.DeletionTimestamp.IsZero() {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if node != nil {
		freshNode := &corev1.Node{}
		if err := c.reader.Get(ctx, client.ObjectKeyFromObject(node), freshNode); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		if freshNode.UID != node.UID || !freshNode.DeletionTimestamp.IsZero() || freshNode.Spec.ProviderID != node.Spec.ProviderID ||
			!reflect.DeepEqual(freshNode.OwnerReferences, node.OwnerReferences) || !reflect.DeepEqual(freshNode.Status.Addresses, node.Status.Addresses) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	} else {
		freshToken, err := c.client.CoreV1().Secrets("kube-system").Get(ctx, token.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		if freshToken.UID != token.UID || !reflect.DeepEqual(freshToken.Data, token.Data) || !commonTypes.BootstrapTokenOwnedBy(freshToken, freshClaim, clusterUID) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		expires, _ := time.Parse(time.RFC3339, string(freshToken.Data["expiration"]))
		if !expires.After(time.Now()) {
			return ctrl.Result{}, nil
		}
		freshRecord, err := c.client.CoreV1().ConfigMaps("kube-system").Get(ctx, credentialRecord.Name, metav1.GetOptions{})
		if err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		if freshRecord.UID != credentialRecord.UID || !reflect.DeepEqual(freshRecord.OwnerReferences, credentialRecord.OwnerReferences) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		for _, key := range []string{"version", "claimUID", "clusterUID", "tokenID", "secretUID"} {
			if freshRecord.Data[key] != credentialRecord.Data[key] {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}
	}
	if err := c.writeDecision(ctx, csr, certificatesv1.CertificateApproved, "VerifiedNodeClaim", "Verified claim, cloud allocation and requested node identity"); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (c *CSRController) resolveIdentity(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) (*karpv1.NodeClaim, *corev1.Node, *corev1.Secret, error) {
	if csr.Spec.SignerName == certificatesv1.KubeAPIServerClientKubeletSignerName {
		id := strings.TrimPrefix(csr.Spec.Username, "system:bootstrap:")
		if !strings.HasPrefix(csr.Spec.Username, "system:bootstrap:") || !csrTokenIDPattern.MatchString(id) ||
			!exactStrings(csr.Spec.Groups, []string{"system:authenticated", "system:bootstrappers", commonTypes.BootstrapGroup}) {
			return nil, nil, nil, nil
		}
		token, err := c.client.CoreV1().Secrets("kube-system").Get(ctx, "bootstrap-token-"+id, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, nil, nil, nil
		}
		if err != nil {
			return nil, nil, nil, err
		}
		name := token.Annotations[commonTypes.BootstrapClaimAnnotation]
		if name == "" {
			return nil, nil, nil, nil
		}
		claim := &karpv1.NodeClaim{}
		if err := c.reader.Get(ctx, client.ObjectKey{Name: name}, claim); err != nil {
			return nil, nil, nil, client.IgnoreNotFound(err)
		}
		return claim, nil, token, nil
	}
	if !strings.HasPrefix(csr.Spec.Username, "system:node:") || !exactStrings(csr.Spec.Groups, []string{"system:authenticated", "system:nodes"}) {
		return nil, nil, nil, nil
	}
	node := &corev1.Node{}
	if err := c.reader.Get(ctx, client.ObjectKey{Name: strings.TrimPrefix(csr.Spec.Username, "system:node:")}, node); err != nil {
		return nil, nil, nil, client.IgnoreNotFound(err)
	}
	if node.UID == "" || !node.DeletionTimestamp.IsZero() || len(node.OwnerReferences) != 1 {
		return nil, nil, nil, nil
	}
	owner := node.OwnerReferences[0]
	if owner.APIVersion != "karpenter.sh/v1" || owner.Kind != "NodeClaim" || owner.UID == "" {
		return nil, nil, nil, nil
	}
	claim := &karpv1.NodeClaim{}
	if err := c.reader.Get(ctx, client.ObjectKey{Name: owner.Name}, claim); err != nil {
		return nil, nil, nil, client.IgnoreNotFound(err)
	}
	if claim.UID != owner.UID || node.Name != claim.Name {
		return nil, nil, nil, nil
	}
	return claim, node, nil, nil
}

func exactStrings(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	seen := map[string]bool{}
	for _, value := range actual {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	for _, value := range expected {
		if !seen[value] {
			return false
		}
	}
	return true
}

func sameClaimIdentity(first, second *karpv1.NodeClaim) bool {
	return first.UID == second.UID && first.Status.ProviderID == second.Status.ProviderID && first.Status.NodeName == second.Status.NodeName &&
		first.Annotations[vpcinstance.LaunchAnnotation] == second.Annotations[vpcinstance.LaunchAnnotation] &&
		reflect.DeepEqual(first.Spec.NodeClassRef, second.Spec.NodeClassRef) &&
		first.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue() == second.StatusConditions().Get(karpv1.ConditionTypeRegistered).IsTrue() &&
		first.StatusConditions().Get(karpv1.ConditionTypeLaunched).IsTrue() == second.StatusConditions().Get(karpv1.ConditionTypeLaunched).IsTrue()
}

func validateCSR(csr *certificatesv1.CertificateSigningRequest, commonName string) (*x509.CertificateRequest, error) {
	if len(csr.Spec.Request) == 0 || len(csr.Spec.Request) > 16*1024 || csr.Spec.UID != "" {
		return nil, fmt.Errorf("invalid CSR size or authenticated UID")
	}
	for key, values := range csr.Spec.Extra {
		if key != "authentication.kubernetes.io/credential-id" || len(values) != 1 || !credentialIDPattern.MatchString(values[0]) || csr.Spec.SignerName != certificatesv1.KubeletServingSignerName {
			return nil, fmt.Errorf("unexpected authenticated CSR identity")
		}
	}
	block, rest := pem.Decode(csr.Spec.Request)
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 ||
		!bytes.HasPrefix(bytes.TrimSpace(csr.Spec.Request), []byte("-----BEGIN CERTIFICATE REQUEST-----")) {
		return nil, fmt.Errorf("request must contain one plain CSR PEM block")
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid certificate request")
	}
	switch request.SignatureAlgorithm {
	case x509.SHA256WithRSA, x509.SHA384WithRSA, x509.SHA512WithRSA, x509.SHA256WithRSAPSS, x509.SHA384WithRSAPSS, x509.SHA512WithRSAPSS, x509.ECDSAWithSHA256, x509.ECDSAWithSHA384, x509.ECDSAWithSHA512:
	default:
		return nil, fmt.Errorf("unsupported CSR signature algorithm")
	}
	if err := request.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR signature is invalid")
	}
	usages := []string{string(certificatesv1.UsageDigitalSignature)}
	switch key := request.PublicKey.(type) {
	case *rsa.PublicKey:
		if key.N.BitLen() < 2048 || key.N.BitLen() > 8192 || key.E < 65537 || key.E%2 == 0 {
			return nil, fmt.Errorf("unsupported RSA key")
		}
		usages = append(usages, string(certificatesv1.UsageKeyEncipherment))
	case *ecdsa.PublicKey:
		if key.Curve != elliptic.P256() && key.Curve != elliptic.P384() {
			return nil, fmt.Errorf("unsupported elliptic curve")
		}
	default:
		return nil, fmt.Errorf("unsupported CSR key type")
	}
	serving := csr.Spec.SignerName == certificatesv1.KubeletServingSignerName
	if serving {
		usages = append(usages, string(certificatesv1.UsageServerAuth))
	} else {
		usages = append(usages, string(certificatesv1.UsageClientAuth))
	}
	actualUsages := make([]string, len(csr.Spec.Usages))
	for i, usage := range csr.Spec.Usages {
		actualUsages[i] = string(usage)
	}
	if !exactStrings(actualUsages, usages) {
		return nil, fmt.Errorf("CSR usages do not match the signer and key")
	}
	if request.Subject.CommonName != commonName || len(request.Subject.Organization) != 1 || request.Subject.Organization[0] != "system:nodes" || len(request.Subject.Names) != 2 {
		return nil, fmt.Errorf("CSR subject must identify exactly its allocated node")
	}
	for _, name := range request.Subject.Names {
		if !name.Type.Equal(asn1.ObjectIdentifier{2, 5, 4, 3}) && !name.Type.Equal(asn1.ObjectIdentifier{2, 5, 4, 10}) {
			return nil, fmt.Errorf("unexpected CSR subject attribute")
		}
	}
	if csr.Spec.ExpirationSeconds != nil && (*csr.Spec.ExpirationSeconds < 600 || *csr.Spec.ExpirationSeconds > 365*24*60*60) {
		return nil, fmt.Errorf("unsupported CSR certificate lifetime")
	}
	var info struct {
		Version    int
		Subject    asn1.RawValue
		PublicKey  asn1.RawValue
		Attributes []asn1.RawValue `asn1:"tag:0"`
	}
	if rest, err := asn1.Unmarshal(request.RawTBSCertificateRequest, &info); err != nil || len(rest) != 0 || info.Version != 0 {
		return nil, fmt.Errorf("invalid CSR attributes")
	}
	if !serving {
		if len(info.Attributes) != 0 || len(request.Extensions) != 0 || len(request.DNSNames) != 0 || len(request.IPAddresses) != 0 || len(request.EmailAddresses) != 0 || len(request.URIs) != 0 {
			return nil, fmt.Errorf("client CSR must not request extensions or SANs")
		}
		return request, nil
	}
	if len(info.Attributes) != 1 || len(request.Extensions) != 1 || !request.Extensions[0].Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 17}) || request.Extensions[0].Critical {
		return nil, fmt.Errorf("serving CSR must contain only a noncritical SAN extension")
	}
	var attribute struct {
		Type   asn1.ObjectIdentifier
		Values []asn1.RawValue `asn1:"set"`
	}
	if rest, err := asn1.Unmarshal(info.Attributes[0].FullBytes, &attribute); err != nil || len(rest) != 0 || !attribute.Type.Equal(asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 14}) || len(attribute.Values) != 1 {
		return nil, fmt.Errorf("unexpected serving CSR attribute")
	}
	var names []asn1.RawValue
	if rest, err := asn1.Unmarshal(request.Extensions[0].Value, &names); err != nil || len(rest) != 0 || len(names) == 0 {
		return nil, fmt.Errorf("invalid serving CSR SAN extension")
	}
	for _, name := range names {
		if name.Class != 2 || name.IsCompound || (name.Tag != 2 && name.Tag != 7) {
			return nil, fmt.Errorf("unsupported serving CSR SAN type")
		}
	}
	if len(request.EmailAddresses) != 0 || len(request.URIs) != 0 {
		return nil, fmt.Errorf("unsupported serving CSR SAN")
	}
	return request, nil
}

func validateServingAddresses(request *x509.CertificateRequest, node *corev1.Node, primaryIP *string, private, floating []string) error {
	if primaryIP == nil || net.ParseIP(*primaryIP) == nil || !exactStrings(request.DNSNames, []string{node.Name}) {
		return fmt.Errorf("serving CSR DNS identity differs from the allocated node")
	}
	privateSet, publicSet := map[string]bool{}, map[string]bool{}
	for _, ip := range private {
		privateSet[ip] = true
	}
	for _, ip := range floating {
		publicSet[ip] = true
	}
	nodeIPs, hostnames := []string{}, []string{}
	internalPrimary := false
	for _, address := range node.Status.Addresses {
		switch address.Type {
		case corev1.NodeHostName:
			hostnames = append(hostnames, address.Address)
		case corev1.NodeInternalIP, corev1.NodeExternalIP:
			ip := net.ParseIP(address.Address)
			if ip == nil {
				return fmt.Errorf("node has an invalid assigned address")
			}
			if address.Type == corev1.NodeInternalIP {
				if !privateSet[ip.String()] {
					return fmt.Errorf("node internal IP is not assigned to its instance")
				}
				internalPrimary = internalPrimary || ip.Equal(net.ParseIP(*primaryIP))
			} else if !publicSet[ip.String()] {
				return fmt.Errorf("node external IP is not assigned to its instance")
			}
			nodeIPs = append(nodeIPs, ip.String())
		default:
			return fmt.Errorf("unsupported node address type")
		}
	}
	if !internalPrimary || !exactStrings(hostnames, []string{node.Name}) {
		return fmt.Errorf("node hostname or primary internal IP differs from its instance")
	}
	requestedIPs := make([]string, len(request.IPAddresses))
	for i, ip := range request.IPAddresses {
		requestedIPs[i] = ip.String()
	}
	if !exactStrings(requestedIPs, nodeIPs) {
		return fmt.Errorf("serving CSR IP SANs differ from the independently verified node addresses")
	}
	return nil
}

func (c *CSRController) reject(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, reason error) (ctrl.Result, error) {
	if err := c.writeDecision(ctx, csr, certificatesv1.CertificateDenied, "BootstrapIdentityRejected", reason.Error()); err != nil {
		return ctrl.Result{}, err
	}
	if c.recorder != nil {
		c.recorder.Event(csr, corev1.EventTypeWarning, "BootstrapIdentityRejected", reason.Error())
	}
	return ctrl.Result{}, nil
}

func (c *CSRController) writeDecision(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, kind certificatesv1.RequestConditionType, reason, message string) error {
	patch, err := json.Marshal([]map[string]interface{}{
		{"op": "test", "path": "/metadata/uid", "value": csr.UID},
		{"op": "test", "path": "/metadata/resourceVersion", "value": csr.ResourceVersion},
		{"op": "test", "path": "/spec/request", "value": csr.Spec.Request},
		{"op": "add", "path": "/status/conditions", "value": []certificatesv1.CertificateSigningRequestCondition{{Type: kind, Status: corev1.ConditionTrue, Reason: reason, Message: message, LastUpdateTime: metav1.Now(), LastTransitionTime: metav1.Now()}}},
	})
	if err != nil {
		return err
	}
	_, err = c.client.CertificatesV1().CertificateSigningRequests().Patch(ctx, csr.Name, k8stypes.JSONPatchType, patch, metav1.PatchOptions{}, "approval")
	return err
}
