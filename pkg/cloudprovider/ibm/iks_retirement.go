/*
Copyright 2024 The Kubernetes Authors.

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
)

// RemoveWorker retires one worker without changing its shared pool target size.
func (c *IKSClient) RemoveWorker(ctx context.Context, cluster, worker string) error {
	if cluster == "" || worker == "" {
		return fmt.Errorf("cluster and worker identity are required")
	}
	token, err := c.client.iamClient.GetToken(ctx)
	if err != nil {
		return fmt.Errorf("getting IAM token: %w", err)
	}
	// Request contract of V2RemoveWorker: https://github.com/IBM-Cloud/container-services-go-sdk/blob/61af133026540c0708a1e3f48659e3016d3d186a/kubernetesserviceapiv1/kubernetes_service_api_v1.go#L13491
	return c.httpClient.PostJSON(ctx, "/removeWorker", token, map[string]string{"cluster": cluster, "workerID": worker}, nil)
}
