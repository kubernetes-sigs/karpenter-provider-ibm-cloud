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

package ownership

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	BackendAnnotation   = "karpenter-ibm.sh/provider-mode"
	RegionAnnotation    = "karpenter-ibm.sh/region"
	ClusterIDAnnotation = "karpenter-ibm.sh/cluster-id"
	PoolIDAnnotation    = "karpenter-ibm.sh/worker-pool-id"
	WorkerIDAnnotation  = "karpenter-ibm.sh/worker-id"
	AccountIDAnnotation = "karpenter-ibm.sh/account-id"
	ClusterUIDLabel     = "karpenter-ibm.sh/cluster-uid"
	ClaimUIDLabel       = "karpenter-ibm.sh/nodeclaim-uid"
	NodeClassUIDLabel   = "karpenter-ibm.sh/nodeclass-uid"
	ProviderLabel       = "karpenter-ibm.sh/provider"
	ManagedLabel        = "karpenter.sh/managed"
	ClusterUIDTag       = "karpenter-ibm.sh.cluster-uid"
	ClaimUIDTag         = "karpenter-ibm.sh.nodeclaim-uid"
	NodeClassUIDTag     = "karpenter-ibm.sh.nodeclass-uid"
	ProviderTag         = "karpenter-ibm.sh.provider"
	ManagedTag          = "karpenter.sh.managed"
	RetainTag           = "karpenter-ibm.sh.retain"
)

func ClusterUID(ctx context.Context, reader client.Reader) (string, error) {
	if reader == nil {
		return "", fmt.Errorf("cluster ownership requires a Kubernetes reader")
	}
	namespace := &corev1.Namespace{}
	if err := reader.Get(ctx, types.NamespacedName{Name: "kube-system"}, namespace); err != nil {
		return "", fmt.Errorf("reading cluster identity: %w", err)
	}
	if namespace.UID == "" {
		return "", fmt.Errorf("kube-system namespace has no UID")
	}
	return string(namespace.UID), nil
}

func VPCTags(clusterUID, claimUID, classUID string) map[string]string {
	return map[string]string{
		ManagedTag:      "true",
		ProviderTag:     "vpc",
		ClusterUIDTag:   clusterUID,
		ClaimUIDTag:     claimUID,
		NodeClassUIDTag: classUID,
	}
}

// ReservedTag reports whether a tag key is owned by Karpenter and must not come from user configuration.
// "managed-by" is included because earlier releases used it to mark owned instances.
func ReservedTag(key string) bool {
	key = strings.ToLower(key)
	return key == "managed-by" || strings.HasPrefix(key, "karpenter.sh/") || strings.HasPrefix(key, "karpenter-ibm.sh/") ||
		strings.HasPrefix(key, "karpenter.sh.") || strings.HasPrefix(key, "karpenter-ibm.sh.")
}

func FormatTags(tags map[string]string) ([]string, error) {
	formatted := make([]string, 0, len(tags))
	for key, value := range tags {
		if key == "" || strings.Contains(key, ":") {
			return nil, fmt.Errorf("cloud tag keys must be nonempty and must not contain colons")
		}
		tag := key + ":" + value
		if err := ValidateTag(tag); err != nil {
			return nil, err
		}
		formatted = append(formatted, tag)
	}
	sort.Strings(formatted)
	return formatted, nil
}

func ValidateTag(tag string) error {
	if len(tag) == 0 || len(tag) > 128 {
		return fmt.Errorf("cloud tags must contain between 1 and 128 ASCII characters")
	}
	for _, character := range tag {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		switch character {
		case ' ', '_', '-', '.', ':':
		default:
			return fmt.Errorf("cloud tags permit only ASCII letters, digits, spaces, underscores, hyphens, periods, and colons")
		}
	}
	return nil
}

func InstanceName(clusterUID, claimUID string) string {
	identity := sha256.Sum256([]byte(clusterUID + "/" + claimUID))
	return fmt.Sprintf("karpenter-%x", identity[:26])
}

const StateFormatVersion = 1

func ValidateStateVersion(version, minimumWriter int) error {
	if version < 0 || version > StateFormatVersion || minimumWriter < 0 || minimumWriter > StateFormatVersion || (version == 0 && minimumWriter != 0) {
		return fmt.Errorf("unsupported durable state version %d (minimum writer %d); retaining state", version, minimumWriter)
	}
	return nil
}
