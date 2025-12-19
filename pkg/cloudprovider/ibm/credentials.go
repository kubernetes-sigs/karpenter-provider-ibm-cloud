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
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SecureCredentialManager provides secure storage and retrieval of credentials
type SecureCredentialManager interface {
	GetVPCAPIKey(ctx context.Context) (string, error)
	GetIBMAPIKey(ctx context.Context) (string, error)
	RotateCredentials(ctx context.Context) error
	ClearCredentials()
}

// CredentialProvider defines how credentials are sourced
type CredentialProvider interface {
	GetCredentials(ctx context.Context) (*Credentials, error)
}

// Credentials holds IBM Cloud credentials
type Credentials struct {
	VPCAPIKey string
	IBMAPIKey string
	Region    string
}

// SecureCredentialStore implements SecureCredentialManager with encryption
type SecureCredentialStore struct {
	mu          sync.RWMutex
	provider    CredentialProvider
	encryptKey  []byte
	credentials *encryptedCredentials
	lastRefresh time.Time
	ttl         time.Duration
}

type encryptedCredentials struct {
	vpcAPIKey []byte
	ibmAPIKey []byte
	region    string
}

// NewSecureCredentialStore creates a new secure credential store
func NewSecureCredentialStore(provider CredentialProvider, ttl time.Duration) (*SecureCredentialStore, error) {
	// Generate encryption key
	key := make([]byte, 32) // AES-256
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating encryption key: %w", err)
	}

	return &SecureCredentialStore{
		provider:   provider,
		encryptKey: key,
		ttl:        ttl,
	}, nil
}

// GetVPCAPIKey retrieves the VPC API key
func (s *SecureCredentialStore) GetVPCAPIKey(ctx context.Context) (string, error) {
	s.mu.RLock()
	needsRefresh := s.credentials == nil || time.Since(s.lastRefresh) > s.ttl
	s.mu.RUnlock()

	if needsRefresh {
		if err := s.refreshCredentials(ctx); err != nil {
			return "", fmt.Errorf("refreshing credentials: %w", err)
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.credentials == nil || s.credentials.vpcAPIKey == nil {
		return "", fmt.Errorf("VPC API key not available")
	}

	decrypted, err := s.decrypt(s.credentials.vpcAPIKey)
	if err != nil {
		return "", fmt.Errorf("decrypting VPC API key: %w", err)
	}

	return string(decrypted), nil
}

// GetIBMAPIKey retrieves the IBM API key
func (s *SecureCredentialStore) GetIBMAPIKey(ctx context.Context) (string, error) {
	s.mu.RLock()
	needsRefresh := s.credentials == nil || time.Since(s.lastRefresh) > s.ttl
	s.mu.RUnlock()

	if needsRefresh {
		if err := s.refreshCredentials(ctx); err != nil {
			return "", fmt.Errorf("refreshing credentials: %w", err)
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.credentials == nil || s.credentials.ibmAPIKey == nil {
		return "", fmt.Errorf("IBM API key not available")
	}

	decrypted, err := s.decrypt(s.credentials.ibmAPIKey)
	if err != nil {
		return "", fmt.Errorf("decrypting IBM API key: %w", err)
	}

	return string(decrypted), nil
}

// GetRegion retrieves the region
func (s *SecureCredentialStore) GetRegion(ctx context.Context) (string, error) {
	s.mu.RLock()
	needsRefresh := s.credentials == nil || time.Since(s.lastRefresh) > s.ttl
	s.mu.RUnlock()

	if needsRefresh {
		if err := s.refreshCredentials(ctx); err != nil {
			return "", fmt.Errorf("refreshing credentials: %w", err)
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.credentials == nil {
		return "", fmt.Errorf("credentials not available")
	}

	return s.credentials.region, nil
}

// RotateCredentials forces a credential refresh
func (s *SecureCredentialStore) RotateCredentials(ctx context.Context) error {
	return s.refreshCredentials(ctx)
}

// ClearCredentials removes stored credentials from memory
func (s *SecureCredentialStore) ClearCredentials() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Overwrite encrypted data before clearing
	if s.credentials != nil {
		if s.credentials.vpcAPIKey != nil {
			for i := range s.credentials.vpcAPIKey {
				s.credentials.vpcAPIKey[i] = 0
			}
		}
		if s.credentials.ibmAPIKey != nil {
			for i := range s.credentials.ibmAPIKey {
				s.credentials.ibmAPIKey[i] = 0
			}
		}
	}

	s.credentials = nil
	s.lastRefresh = time.Time{}
}

func (s *SecureCredentialStore) refreshCredentials(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	creds, err := s.provider.GetCredentials(ctx)
	if err != nil {
		return fmt.Errorf("getting credentials from provider: %w", err)
	}

	// Encrypt credentials
	encVPC, err := s.encrypt([]byte(creds.VPCAPIKey))
	if err != nil {
		return fmt.Errorf("encrypting VPC API key: %w", err)
	}

	encIBM, err := s.encrypt([]byte(creds.IBMAPIKey))
	if err != nil {
		return fmt.Errorf("encrypting IBM API key: %w", err)
	}

	// Clear old credentials
	if s.credentials != nil {
		if s.credentials.vpcAPIKey != nil {
			for i := range s.credentials.vpcAPIKey {
				s.credentials.vpcAPIKey[i] = 0
			}
		}
		if s.credentials.ibmAPIKey != nil {
			for i := range s.credentials.ibmAPIKey {
				s.credentials.ibmAPIKey[i] = 0
			}
		}
	}

	s.credentials = &encryptedCredentials{
		vpcAPIKey: encVPC,
		ibmAPIKey: encIBM,
		region:    creds.Region,
	}
	s.lastRefresh = time.Now()

	// Don't modify the provider's credentials object
	// The encryption is already done, so we don't need to clear anything

	return nil
}

func (s *SecureCredentialStore) encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.encryptKey)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (s *SecureCredentialStore) decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.encryptKey)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// EnvironmentCredentialProvider sources credentials from environment variables
type EnvironmentCredentialProvider struct{}

func (e *EnvironmentCredentialProvider) GetCredentials(ctx context.Context) (*Credentials, error) {
	vpcAPIKey := os.Getenv("VPC_API_KEY")
	if vpcAPIKey == "" {
		return nil, fmt.Errorf("VPC_API_KEY environment variable is required")
	}

	ibmAPIKey := os.Getenv("IBMCLOUD_API_KEY")
	if ibmAPIKey == "" {
		return nil, fmt.Errorf("IBMCLOUD_API_KEY environment variable is required")
	}

	region := os.Getenv("IBMCLOUD_REGION")
	if region == "" {
		return nil, fmt.Errorf("IBMCLOUD_REGION environment variable is required")
	}

	return &Credentials{
		VPCAPIKey: vpcAPIKey,
		IBMAPIKey: ibmAPIKey,
		Region:    region,
	}, nil
}

// KubernetesSecretProvider sources credentials from a Kubernetes secret
type KubernetesSecretProvider struct {
	client    client.Client
	namespace string
	name      string
}

func NewKubernetesSecretProvider(client client.Client, namespace, name string) *KubernetesSecretProvider {
	return &KubernetesSecretProvider{
		client:    client,
		namespace: namespace,
		name:      name,
	}
}

func (k *KubernetesSecretProvider) GetCredentials(ctx context.Context) (*Credentials, error) {
	secret := &corev1.Secret{}
	if err := k.client.Get(ctx, types.NamespacedName{
		Namespace: k.namespace,
		Name:      k.name,
	}, secret); err != nil {
		return nil, fmt.Errorf("getting secret %s/%s: %w", k.namespace, k.name, err)
	}

	vpcAPIKey, ok := secret.Data["vpcApiKey"]
	if !ok {
		return nil, fmt.Errorf("vpcApiKey not found in secret")
	}

	ibmAPIKey, ok := secret.Data["ibmApiKey"]
	if !ok {
		return nil, fmt.Errorf("ibmApiKey not found in secret")
	}

	region, ok := secret.Data["region"]
	if !ok {
		return nil, fmt.Errorf("region not found in secret")
	}

	return &Credentials{
		VPCAPIKey: string(vpcAPIKey),
		IBMAPIKey: string(ibmAPIKey),
		Region:    string(region),
	}, nil
}

// Base64Provider wraps another provider and base64 decodes the credentials
type Base64Provider struct {
	provider CredentialProvider
}

func NewBase64Provider(provider CredentialProvider) *Base64Provider {
	return &Base64Provider{provider: provider}
}

func (b *Base64Provider) GetCredentials(ctx context.Context) (*Credentials, error) {
	creds, err := b.provider.GetCredentials(ctx)
	if err != nil {
		return nil, err
	}

	vpcDecoded, err := base64.StdEncoding.DecodeString(creds.VPCAPIKey)
	if err != nil {
		return nil, fmt.Errorf("decoding VPC API key: %w", err)
	}

	ibmDecoded, err := base64.StdEncoding.DecodeString(creds.IBMAPIKey)
	if err != nil {
		return nil, fmt.Errorf("decoding IBM API key: %w", err)
	}

	return &Credentials{
		VPCAPIKey: string(vpcDecoded),
		IBMAPIKey: string(ibmDecoded),
		Region:    creds.Region,
	}, nil
}
