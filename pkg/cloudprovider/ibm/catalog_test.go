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
	"errors"
	"testing"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/globalcatalogv1"
)

type mockIAMClient struct {
	token string
	err   error
}

func (m *mockIAMClient) GetToken(ctx context.Context) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.token, nil
}

type mockGlobalCatalogClient struct {
	getEntryResponse    *globalcatalogv1.CatalogEntry
	listEntriesResponse *globalcatalogv1.EntrySearchResult
	pricingResponse     *globalcatalogv1.PricingGet
	listOptions         []*globalcatalogv1.ListCatalogEntriesOptions
	pricingOptions      *globalcatalogv1.GetPricingOptions
	childPage           func(*globalcatalogv1.GetChildObjectsOptions) *globalcatalogv1.EntrySearchResult
	err                 error
}

func (m *mockGlobalCatalogClient) GetCatalogEntryWithContext(_ context.Context, _ *globalcatalogv1.GetCatalogEntryOptions) (*globalcatalogv1.CatalogEntry, *core.DetailedResponse, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	return m.getEntryResponse, &core.DetailedResponse{}, nil
}

func (m *mockGlobalCatalogClient) ListCatalogEntriesWithContext(_ context.Context, options *globalcatalogv1.ListCatalogEntriesOptions) (*globalcatalogv1.EntrySearchResult, *core.DetailedResponse, error) {
	m.listOptions = append(m.listOptions, options)
	if m.err != nil {
		return nil, nil, m.err
	}
	return m.listEntriesResponse, &core.DetailedResponse{}, nil
}

func (m *mockGlobalCatalogClient) GetPricingWithContext(_ context.Context, options *globalcatalogv1.GetPricingOptions) (*globalcatalogv1.PricingGet, *core.DetailedResponse, error) {
	m.pricingOptions = options
	return m.pricingResponse, &core.DetailedResponse{}, m.err
}

func (m *mockGlobalCatalogClient) GetChildObjectsWithContext(_ context.Context, options *globalcatalogv1.GetChildObjectsOptions) (*globalcatalogv1.EntrySearchResult, *core.DetailedResponse, error) {
	if m.childPage != nil {
		return m.childPage(options), &core.DetailedResponse{}, m.err
	}
	return m.listEntriesResponse, &core.DetailedResponse{}, m.err
}

func TestCatalogPricingDeploymentPaginationStaysWithinPlanAndRegion(t *testing.T) {
	var offsets []int64
	mock := &mockGlobalCatalogClient{childPage: func(options *globalcatalogv1.GetChildObjectsOptions) *globalcatalogv1.EntrySearchResult {
		if *options.ID != "plan" || *options.Q != "eu-de" || *options.Kind != "deployment" || !*options.Complete {
			t.Fatalf("incorrect child query: %+v", options)
		}
		offsets = append(offsets, *options.Offset)
		count := int64(100)
		if *options.Offset == 200 {
			count = 1
		}
		return &globalcatalogv1.EntrySearchResult{Count: core.Int64Ptr(201), Resources: make([]globalcatalogv1.CatalogEntry, count)}
	}}
	client := &GlobalCatalogClient{iamClient: &mockIAMClient{token: "token"}, client: mock, currentToken: "token"}
	entries, err := client.ListPricingDeployments(context.Background(), "plan", "eu-de")
	if err != nil || len(entries) != 201 {
		t.Fatalf("list: entries=%d, err=%v", len(entries), err)
	}
	if len(offsets) != 3 || offsets[0] != 0 || offsets[1] != 100 || offsets[2] != 200 {
		t.Fatalf("incorrect pagination: %v", offsets)
	}
}

func TestCatalogInstanceProfileQueryAndTypedRegionalPricing(t *testing.T) {
	mock := &mockGlobalCatalogClient{
		listEntriesResponse: &globalcatalogv1.EntrySearchResult{Resources: []globalcatalogv1.CatalogEntry{{ID: core.StringPtr("bx2-4x16")}}},
		pricingResponse:     &globalcatalogv1.PricingGet{DeploymentID: core.StringPtr("deployment")},
	}
	client := &GlobalCatalogClient{iamClient: &mockIAMClient{token: "token"}, client: mock, currentToken: "token"}
	entries, err := client.ListInstanceTypes(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("list: entries=%v, err=%v", entries, err)
	}
	if *mock.listOptions[0].Q != "kind:instance.profile" || !*mock.listOptions[0].Complete {
		t.Fatalf("incorrect catalog query: %+v", mock.listOptions[0])
	}
	quote, err := client.GetPricing(context.Background(), "deployment", "eu-de")
	if err != nil || quote != mock.pricingResponse {
		t.Fatalf("quote=%v, err=%v", quote, err)
	}
	if *mock.pricingOptions.ID != "deployment" || *mock.pricingOptions.DeploymentRegion != "eu-de" {
		t.Fatalf("incorrect regional pricing target: %+v", mock.pricingOptions)
	}
}

func TestNewGlobalCatalogClient(t *testing.T) {
	iamClient := &IAMClient{token: "test-token"}
	client := NewGlobalCatalogClient(iamClient)

	if client == nil {
		t.Fatal("expected non-nil client")
		return
	}

	if client.iamClient != iamClient {
		t.Error("expected iamClient to be set")
	}
}

func TestEnsureClient(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		tokenErr error
		wantErr  bool
	}{
		{
			name:    "successful client initialization",
			token:   "test-token",
			wantErr: false,
		},
		{
			name:     "IAM token error",
			tokenErr: errors.New("token error"),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockIAM := &mockIAMClient{
				token: tt.token,
				err:   tt.tokenErr,
			}
			client := &GlobalCatalogClient{
				iamClient: mockIAM,
			}
			cl, err := client.ensureClient(context.Background())

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				if cl != nil {
					t.Error("expected nil client on error")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if cl == nil {
					t.Error("expected non-nil client returned")
				}
				if client.client == nil {
					t.Error("expected client to be initialized")
				}
			}
		})
	}
}

func TestGetInstanceType(t *testing.T) {
	ctx := context.Background()
	testID := "test-instance-id"
	testName := "test-instance"

	mockEntry := &globalcatalogv1.CatalogEntry{
		Name: &testName,
		ID:   &testID,
	}

	tests := []struct {
		name        string
		id          string
		token       string
		tokenErr    error
		mockCatalog globalCatalogClientInterface
		wantErr     bool
		wantEntry   *globalcatalogv1.CatalogEntry
	}{
		{
			name:  "successful instance type request",
			id:    testID,
			token: "test-token",
			mockCatalog: &mockGlobalCatalogClient{
				getEntryResponse: mockEntry,
			},
			wantEntry: mockEntry,
		},
		{
			name:     "IAM token error",
			id:       testID,
			tokenErr: errors.New("token error"),
			wantErr:  true,
		},
		{
			name:  "catalog API error",
			id:    testID,
			token: "test-token",
			mockCatalog: &mockGlobalCatalogClient{
				err: errors.New("API error"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockIAM := &mockIAMClient{
				token: tt.token,
				err:   tt.tokenErr,
			}
			client := &GlobalCatalogClient{
				iamClient: mockIAM,
			}
			if tt.mockCatalog != nil {
				client.client = tt.mockCatalog
				client.currentToken = tt.token
			}

			entry, err := client.GetInstanceType(ctx, tt.id)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				if entry != nil {
					t.Error("expected nil entry")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if entry != tt.wantEntry {
					t.Errorf("got entry %v, want %v", entry, tt.wantEntry)
				}
			}
		})
	}
}

func TestListInstanceTypes(t *testing.T) {
	ctx := context.Background()
	testName := "test-instance"
	testID := "test-id"

	mockEntries := []globalcatalogv1.CatalogEntry{
		{
			Name: &testName,
			ID:   &testID,
		},
	}

	mockResult := &globalcatalogv1.EntrySearchResult{
		Resources: mockEntries,
	}

	tests := []struct {
		name        string
		token       string
		tokenErr    error
		mockCatalog globalCatalogClientInterface
		wantErr     bool
		wantEntries []globalcatalogv1.CatalogEntry
	}{
		{
			name:  "successful list request",
			token: "test-token",
			mockCatalog: &mockGlobalCatalogClient{
				listEntriesResponse: mockResult,
			},
			wantEntries: mockEntries,
		},
		{
			name:     "IAM token error",
			tokenErr: errors.New("token error"),
			wantErr:  true,
		},
		{
			name:  "catalog API error",
			token: "test-token",
			mockCatalog: &mockGlobalCatalogClient{
				err: errors.New("API error"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockIAM := &mockIAMClient{
				token: tt.token,
				err:   tt.tokenErr,
			}
			client := &GlobalCatalogClient{
				iamClient: mockIAM,
			}
			if tt.mockCatalog != nil {
				client.client = tt.mockCatalog
				client.currentToken = tt.token
			}

			entries, err := client.ListInstanceTypes(ctx)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				if entries != nil {
					t.Error("expected nil entries")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if len(entries) != len(tt.wantEntries) {
					t.Errorf("got %d entries, want %d", len(entries), len(tt.wantEntries))
				}
			}
		})
	}
}
