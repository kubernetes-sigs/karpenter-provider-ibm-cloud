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
	"encoding/base64"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// MockCredentialProvider for testing
type MockCredentialProvider struct {
	creds *Credentials
	err   error
	calls int
}

func (m *MockCredentialProvider) GetCredentials(ctx context.Context) (*Credentials, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.creds, nil
}

func TestSecureCredentialStore_GetVPCAPIKey(t *testing.T) {
	mockProvider := &MockCredentialProvider{
		creds: &Credentials{
			VPCAPIKey: "test-vpc-key",
			IBMAPIKey: "test-ibm-key",
			Region:    "us-south",
		},
	}

	store, err := NewSecureCredentialStore(mockProvider, time.Hour)
	require.NoError(t, err)

	ctx := context.Background()
	key, err := store.GetVPCAPIKey(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-vpc-key", key)
	assert.Equal(t, 1, mockProvider.calls)

	// Second call should use cached credentials
	key, err = store.GetVPCAPIKey(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-vpc-key", key)
	assert.Equal(t, 1, mockProvider.calls) // No additional call
}

func TestSecureCredentialStore_GetIBMAPIKey(t *testing.T) {
	mockProvider := &MockCredentialProvider{
		creds: &Credentials{
			VPCAPIKey: "test-vpc-key",
			IBMAPIKey: "test-ibm-key",
			Region:    "us-south",
		},
	}

	store, err := NewSecureCredentialStore(mockProvider, time.Hour)
	require.NoError(t, err)

	ctx := context.Background()
	key, err := store.GetIBMAPIKey(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-ibm-key", key)
}

func TestSecureCredentialStore_RotateCredentials(t *testing.T) {
	mockProvider := &MockCredentialProvider{
		creds: &Credentials{
			VPCAPIKey: "test-vpc-key",
			IBMAPIKey: "test-ibm-key",
			Region:    "us-south",
		},
	}

	store, err := NewSecureCredentialStore(mockProvider, time.Hour)
	require.NoError(t, err)

	ctx := context.Background()

	// Get initial credentials
	key, err := store.GetVPCAPIKey(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-vpc-key", key)
	assert.Equal(t, 1, mockProvider.calls)

	// Update mock credentials (simulating external credential rotation)
	mockProvider.creds = &Credentials{
		VPCAPIKey: "new-vpc-key",
		IBMAPIKey: "new-ibm-key",
		Region:    "us-south",
	}

	// Rotate credentials - should fetch the new credentials
	err = store.RotateCredentials(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 2, mockProvider.calls)

	// Get should return new credentials from cache (no additional call)
	key, err = store.GetVPCAPIKey(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "new-vpc-key", key)
	assert.Equal(t, 2, mockProvider.calls) // No additional call since cache is fresh
}

func TestSecureCredentialStore_ClearCredentials(t *testing.T) {
	mockProvider := &MockCredentialProvider{
		creds: &Credentials{
			VPCAPIKey: "test-vpc-key",
			IBMAPIKey: "test-ibm-key",
			Region:    "us-south",
		},
	}

	store, err := NewSecureCredentialStore(mockProvider, time.Hour)
	require.NoError(t, err)

	ctx := context.Background()

	// Get credentials to cache them
	_, err = store.GetVPCAPIKey(ctx)
	assert.NoError(t, err)

	// Clear credentials
	store.ClearCredentials()

	// Next get should require refresh
	_, err = store.GetVPCAPIKey(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 2, mockProvider.calls) // Should have made another call
}

func TestSecureCredentialStore_ErrorHandling(t *testing.T) {
	mockProvider := &MockCredentialProvider{
		err: fmt.Errorf("provider error"),
	}

	store, err := NewSecureCredentialStore(mockProvider, time.Hour)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = store.GetVPCAPIKey(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "provider error")
}

func TestSecureCredentialStore_TTLExpiration(t *testing.T) {
	mockProvider := &MockCredentialProvider{
		creds: &Credentials{
			VPCAPIKey: "test-vpc-key",
			IBMAPIKey: "test-ibm-key",
			Region:    "us-south",
		},
	}

	// Use very short TTL for testing
	store, err := NewSecureCredentialStore(mockProvider, 100*time.Millisecond)
	require.NoError(t, err)

	ctx := context.Background()

	// Get credentials
	_, err = store.GetVPCAPIKey(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 1, mockProvider.calls)

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Should refresh credentials
	_, err = store.GetVPCAPIKey(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 2, mockProvider.calls)
}

func TestEnvironmentCredentialProvider(t *testing.T) {
	// Save original env vars
	origVPC := os.Getenv("VPC_API_KEY")
	origIBM := os.Getenv("IBM_API_KEY")
	origRegion := os.Getenv("IBM_REGION")
	defer func() {
		_ = os.Setenv("VPC_API_KEY", origVPC)
		_ = os.Setenv("IBM_API_KEY", origIBM)
		_ = os.Setenv("IBM_REGION", origRegion)
	}()

	tests := []struct {
		name      string
		env       map[string]string
		wantError bool
		errorMsg  string
	}{
		{
			name: "all credentials present",
			env: map[string]string{
				"VPC_API_KEY":      "vpc-key",
				"IBMCLOUD_API_KEY": "ibm-key",
				"IBMCLOUD_REGION":  "us-south",
			},
			wantError: false,
		},
		{
			name: "missing VPC_API_KEY",
			env: map[string]string{
				"IBMCLOUD_API_KEY": "ibm-key",
				"IBMCLOUD_REGION":  "us-south",
			},
			wantError: true,
			errorMsg:  "VPC_API_KEY environment variable is required",
		},
		{
			name: "missing IBMCLOUD_API_KEY",
			env: map[string]string{
				"VPC_API_KEY":     "vpc-key",
				"IBMCLOUD_REGION": "us-south",
			},
			wantError: true,
			errorMsg:  "IBMCLOUD_API_KEY environment variable is required",
		},
		{
			name: "missing IBMCLOUD_REGION",
			env: map[string]string{
				"VPC_API_KEY":      "vpc-key",
				"IBMCLOUD_API_KEY": "ibm-key",
			},
			wantError: true,
			errorMsg:  "IBMCLOUD_REGION environment variable is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear env vars
			_ = os.Unsetenv("VPC_API_KEY")
			_ = os.Unsetenv("IBMCLOUD_API_KEY")
			_ = os.Unsetenv("IBMCLOUD_REGION")

			// Set test env vars
			for k, v := range tt.env {
				_ = os.Setenv(k, v)
			}

			provider := &EnvironmentCredentialProvider{}
			creds, err := provider.GetCredentials(context.Background())

			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.env["VPC_API_KEY"], creds.VPCAPIKey)
				assert.Equal(t, tt.env["IBMCLOUD_API_KEY"], creds.IBMAPIKey)
				assert.Equal(t, tt.env["IBMCLOUD_REGION"], creds.Region)
			}
		})
	}
}

func TestKubernetesSecretProvider(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name      string
		secret    *corev1.Secret
		wantError bool
		errorMsg  string
	}{
		{
			name: "valid secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ibm-credentials",
					Namespace: "karpenter",
				},
				Data: map[string][]byte{
					"vpcApiKey": []byte("vpc-key"),
					"ibmApiKey": []byte("ibm-key"),
					"region":    []byte("us-south"),
				},
			},
			wantError: false,
		},
		{
			name: "missing vpcApiKey",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ibm-credentials",
					Namespace: "karpenter",
				},
				Data: map[string][]byte{
					"ibmApiKey": []byte("ibm-key"),
					"region":    []byte("us-south"),
				},
			},
			wantError: true,
			errorMsg:  "vpcApiKey not found in secret",
		},
		{
			name: "missing ibmApiKey",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ibm-credentials",
					Namespace: "karpenter",
				},
				Data: map[string][]byte{
					"vpcApiKey": []byte("vpc-key"),
					"region":    []byte("us-south"),
				},
			},
			wantError: true,
			errorMsg:  "ibmApiKey not found in secret",
		},
		{
			name: "missing region",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ibm-credentials",
					Namespace: "karpenter",
				},
				Data: map[string][]byte{
					"vpcApiKey": []byte("vpc-key"),
					"ibmApiKey": []byte("ibm-key"),
				},
			},
			wantError: true,
			errorMsg:  "region not found in secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var objects []runtime.Object
			if tt.secret != nil {
				objects = append(objects, tt.secret)
			}

			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			provider := NewKubernetesSecretProvider(client, "karpenter", "ibm-credentials")
			creds, err := provider.GetCredentials(context.Background())

			if tt.wantError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, "vpc-key", creds.VPCAPIKey)
				assert.Equal(t, "ibm-key", creds.IBMAPIKey)
				assert.Equal(t, "us-south", creds.Region)
			}
		})
	}
}

func TestBase64Provider(t *testing.T) {
	mockProvider := &MockCredentialProvider{
		creds: &Credentials{
			VPCAPIKey: base64.StdEncoding.EncodeToString([]byte("vpc-key")),
			IBMAPIKey: base64.StdEncoding.EncodeToString([]byte("ibm-key")),
			Region:    "us-south",
		},
	}

	provider := NewBase64Provider(mockProvider)
	creds, err := provider.GetCredentials(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, "vpc-key", creds.VPCAPIKey)
	assert.Equal(t, "ibm-key", creds.IBMAPIKey)
	assert.Equal(t, "us-south", creds.Region)
}

func TestBase64Provider_InvalidBase64(t *testing.T) {
	mockProvider := &MockCredentialProvider{
		creds: &Credentials{
			VPCAPIKey: "not-base64!",
			IBMAPIKey: base64.StdEncoding.EncodeToString([]byte("ibm-key")),
			Region:    "us-south",
		},
	}

	provider := NewBase64Provider(mockProvider)
	_, err := provider.GetCredentials(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "decoding VPC API key")
}

func TestSecureCredentialStore_EncryptionDecryption(t *testing.T) {
	store, err := NewSecureCredentialStore(nil, time.Hour)
	require.NoError(t, err)

	testData := []byte("test-secret-data")

	// Encrypt data
	encrypted, err := store.encrypt(testData)
	assert.NoError(t, err)
	assert.NotEqual(t, testData, encrypted)

	// Decrypt data
	decrypted, err := store.decrypt(encrypted)
	assert.NoError(t, err)
	assert.Equal(t, testData, decrypted)
}

func TestSecureCredentialStore_ConcurrentAccess(t *testing.T) {
	mockProvider := &MockCredentialProvider{
		creds: &Credentials{
			VPCAPIKey: "test-vpc-key",
			IBMAPIKey: "test-ibm-key",
			Region:    "us-south",
		},
	}

	store, err := NewSecureCredentialStore(mockProvider, time.Hour)
	require.NoError(t, err)

	ctx := context.Background()

	// Test basic concurrent read access (no clearing)
	done := make(chan bool)
	for i := 0; i < 3; i++ {
		go func() {
			for j := 0; j < 10; j++ {
				key, keyErr := store.GetVPCAPIKey(ctx)
				assert.NoError(t, keyErr)
				assert.Equal(t, "test-vpc-key", key)
			}
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 3; i++ {
		<-done
	}

	// Test that credentials work before clearing
	keyBefore, err := store.GetVPCAPIKey(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-vpc-key", keyBefore)

	// Test single clear and refresh (not concurrent)
	store.ClearCredentials()

	// Debug: check provider state
	t.Logf("Provider calls: %d, Provider creds: %+v", mockProvider.calls, mockProvider.creds)

	// Verify store can refresh after clearing
	key, err := store.GetVPCAPIKey(ctx)
	if err != nil {
		t.Logf("Error getting VPC key after clear: %v", err)
	}
	assert.NoError(t, err)
	assert.Equal(t, "test-vpc-key", key)
}
