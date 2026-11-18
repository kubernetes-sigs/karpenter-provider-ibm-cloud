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

package ibm

import (
	"context"
	"fmt"
	"strings"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/globaltaggingv1"
	"github.com/kubernetes-sigs/karpenter-provider-ibm-cloud/pkg/utils/ownership"
)

func (c *VPCClient) GetInstanceUserTags(ctx context.Context, crn string) (map[string]string, error) {
	if crn == "" || c.tagging == nil {
		return nil, fmt.Errorf("independent cloud ownership tag reader is required")
	}
	tags := map[string]string{}
	const limit int64 = 100
	for offset := int64(0); ; {
		page, _, err := c.tagging.ListTagsWithContext(ctx, &globaltaggingv1.ListTagsOptions{
			AttachedTo: &crn, Providers: []string{"ghost"}, TagType: core.StringPtr("user"),
			Offset: &offset, Limit: core.Int64Ptr(limit),
		})
		if err != nil {
			return nil, fmt.Errorf("reading instance ownership tags: %w", err)
		}
		if page == nil {
			return nil, fmt.Errorf("ownership tag listing returned no page")
		}
		for _, item := range page.Items {
			if item.Name == nil {
				return nil, fmt.Errorf("ownership tag listing contains an unnamed tag")
			}
			key, value, ok := strings.Cut(*item.Name, ":")
			if !ok {
				continue
			}
			if old, exists := tags[key]; exists && old != value && ownership.ReservedTag(key) {
				return nil, fmt.Errorf("conflicting ownership tags for %s", key)
			}
			tags[key] = value
		}
		offset += int64(len(page.Items))
		if page.TotalCount != nil {
			if offset >= *page.TotalCount {
				return tags, nil
			}
			if len(page.Items) == 0 {
				return nil, fmt.Errorf("ownership tag listing is incomplete")
			}
		} else if len(page.Items) < int(limit) {
			return tags, nil
		}
	}
}
