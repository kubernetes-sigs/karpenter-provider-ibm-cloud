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

package subnet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSubnetCacheSeparatesRegions(t *testing.T) {
	ctrl := gomock.NewController(t)
	p, mock := newTestProvider(ctrl)
	first := makeSubnet("same-id", "us-south-1", "10.0.0.0/24", "available", 256, 40)
	second := makeSubnet("same-id", "eu-de-2", "10.1.0.0/24", "available", 256, 80)
	gomock.InOrder(
		mock.EXPECT().GetSubnetWithContext(gomock.Any(), gomock.Any()).Return(&first, okResponse(), nil),
		mock.EXPECT().GetSubnetWithContext(gomock.Any(), gomock.Any()).Return(&second, okResponse(), nil),
	)
	ctx := context.Background()
	one, err := p.GetSubnet(ctx, "same-id", "us-south")
	require.NoError(t, err)
	require.Equal(t, "us-south-1", one.Zone)
	two, err := p.GetSubnet(ctx, "same-id", "eu-de")
	require.NoError(t, err)
	require.Equal(t, "eu-de-2", two.Zone)
	one, err = p.GetSubnet(ctx, "same-id", "us-south")
	require.NoError(t, err)
	require.Equal(t, "us-south-1", one.Zone)
	two, err = p.GetSubnet(ctx, "same-id", "eu-de")
	require.NoError(t, err)
	require.Equal(t, "eu-de-2", two.Zone)
}
