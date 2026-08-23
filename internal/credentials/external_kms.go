package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
)

// KMSAuditEvent contains metadata only; plaintext and ciphertext are never
// included. Callers can persist it in the deployment audit log.
type KMSAuditEvent struct {
	Provider, Operation, WorkspaceID, KeyID string
	Succeeded                               bool
	ErrorClass                              string
	At                                      time.Time
}

type KMSAuditor func(KMSAuditEvent)

// AWSKMS is an envelope-key adapter backed by AWS KMS. The AWS SDK default
// credential chain supplies workload identity automatically (IRSA, ECS task
// roles, EC2 instance profiles, or locally configured credentials).
type AWSKMS struct {
	client  *awskms.Client
	keyID   string
	auditor KMSAuditor
}

func NewAWSKMS(ctx context.Context, keyID string, auditor KMSAuditor) (*AWSKMS, error) {
	if strings.TrimSpace(keyID) == "" {
		return nil, fmt.Errorf("credentials: aws KMS key id is required")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("credentials: AWS workload identity: %w", err)
	}
	return &AWSKMS{client: awskms.NewFromConfig(cfg), keyID: keyID, auditor: auditor}, nil
}

func (k *AWSKMS) WrapKey(ctx context.Context, workspaceID string, dataKey []byte) ([]byte, error) {
	out, err := k.client.Encrypt(ctx, &awskms.EncryptInput{KeyId: &k.keyID, Plaintext: dataKey,
		EncryptionContext: map[string]string{"application": "soulacy", "workspace_id": workspaceID}})
	k.audit("wrap", workspaceID, err)
	if err != nil {
		return nil, fmt.Errorf("credentials: AWS KMS encrypt: %w", err)
	}
	return out.CiphertextBlob, nil
}

func (k *AWSKMS) UnwrapKey(ctx context.Context, workspaceID string, wrapped []byte) ([]byte, error) {
	out, err := k.client.Decrypt(ctx, &awskms.DecryptInput{KeyId: &k.keyID, CiphertextBlob: wrapped,
		EncryptionContext: map[string]string{"application": "soulacy", "workspace_id": workspaceID}})
	k.audit("unwrap", workspaceID, err)
	if err != nil {
		return nil, fmt.Errorf("credentials: AWS KMS decrypt: %w", err)
	}
	return out.Plaintext, nil
}

func (k *AWSKMS) audit(op, workspace string, err error) {
	if k.auditor != nil {
		k.auditor(kmsAudit("aws-kms", op, workspace, k.keyID, err))
	}
}

// VaultTransit wraps keys using HashiCorp Vault Transit. It supports either a
// supplied token or Kubernetes workload identity. Tokens obtained from the
// Kubernetes auth endpoint are cached only in memory and never logged.
type VaultTransit struct {
	addr, mount, key, token, role, jwtPath string
	client                                 *http.Client
	auditor                                KMSAuditor
	mu                                     sync.Mutex
}

type VaultTransitConfig struct {
	Address, Mount, Key, Token, KubernetesRole, JWTPath string
	HTTPClient                                          *http.Client
	Auditor                                             KMSAuditor
}

func NewVaultTransit(cfg VaultTransitConfig) (*VaultTransit, error) {
	if strings.TrimSpace(cfg.Address) == "" || strings.TrimSpace(cfg.Key) == "" {
		return nil, fmt.Errorf("credentials: Vault address and transit key are required")
	}
	if cfg.Mount == "" {
		cfg.Mount = "transit"
	}
	if cfg.JWTPath == "" {
		cfg.JWTPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &VaultTransit{addr: strings.TrimRight(cfg.Address, "/"), mount: strings.Trim(cfg.Mount, "/"), key: cfg.Key,
		token: cfg.Token, role: cfg.KubernetesRole, jwtPath: cfg.JWTPath, client: cfg.HTTPClient, auditor: cfg.Auditor}, nil
}

func (v *VaultTransit) WrapKey(ctx context.Context, workspaceID string, dataKey []byte) ([]byte, error) {
	var response struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	err := v.call(ctx, "/v1/"+v.mount+"/encrypt/"+v.key, map[string]string{
		"plaintext": base64.StdEncoding.EncodeToString(dataKey), "context": base64.StdEncoding.EncodeToString([]byte(workspaceID)),
	}, &response)
	v.audit("wrap", workspaceID, err)
	if err != nil {
		return nil, err
	}
	if response.Data.Ciphertext == "" {
		return nil, fmt.Errorf("credentials: Vault transit returned empty ciphertext")
	}
	return []byte(response.Data.Ciphertext), nil
}

func (v *VaultTransit) UnwrapKey(ctx context.Context, workspaceID string, wrapped []byte) ([]byte, error) {
	var response struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	err := v.call(ctx, "/v1/"+v.mount+"/decrypt/"+v.key, map[string]string{
		"ciphertext": string(wrapped), "context": base64.StdEncoding.EncodeToString([]byte(workspaceID)),
	}, &response)
	v.audit("unwrap", workspaceID, err)
	if err != nil {
		return nil, err
	}
	plain, err := base64.StdEncoding.DecodeString(response.Data.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("credentials: decode Vault plaintext: %w", err)
	}
	return plain, nil
}

func (v *VaultTransit) tokenFor(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if strings.TrimSpace(v.token) != "" {
		return v.token, nil
	}
	if strings.TrimSpace(v.role) == "" {
		return "", fmt.Errorf("credentials: Vault token or Kubernetes role is required")
	}
	jwt, err := os.ReadFile(v.jwtPath)
	if err != nil {
		return "", fmt.Errorf("credentials: read workload identity token: %w", err)
	}
	var response struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := v.callWithoutAuth(ctx, "/v1/auth/kubernetes/login", map[string]string{"role": v.role, "jwt": strings.TrimSpace(string(jwt))}, &response); err != nil {
		return "", err
	}
	if response.Auth.ClientToken == "" {
		return "", fmt.Errorf("credentials: Vault workload identity returned no token")
	}
	v.token = response.Auth.ClientToken
	return v.token, nil
}

func (v *VaultTransit) call(ctx context.Context, path string, body any, out any) error {
	token, err := v.tokenFor(ctx)
	if err != nil {
		return err
	}
	return v.do(ctx, path, token, body, out)
}
func (v *VaultTransit) callWithoutAuth(ctx context.Context, path string, body any, out any) error {
	return v.do(ctx, path, "", body, out)
}
func (v *VaultTransit) do(ctx context.Context, path, token string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.addr+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("credentials: Vault request: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("credentials: Vault status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("credentials: Vault response: %w", err)
	}
	return nil
}
func (v *VaultTransit) audit(op, workspace string, err error) {
	if v.auditor != nil {
		v.auditor(kmsAudit("vault-transit", op, workspace, v.key, err))
	}
}

func kmsAudit(provider, op, workspace, key string, err error) KMSAuditEvent {
	e := KMSAuditEvent{Provider: provider, Operation: op, WorkspaceID: workspace, KeyID: key, Succeeded: err == nil, At: time.Now().UTC()}
	if err != nil {
		e.ErrorClass = "provider_error"
	}
	return e
}

// VerifyKeyWrapper is a startup health check. Production callers use it to
// fail closed before serving requests with an unavailable KMS.
func VerifyKeyWrapper(ctx context.Context, wrapper KeyWrapper) error {
	probe := make([]byte, 32)
	for i := range probe {
		probe[i] = byte(i)
	}
	wrapped, err := wrapper.WrapKey(ctx, "__startup_probe__", probe)
	if err != nil {
		return err
	}
	plain, err := wrapper.UnwrapKey(ctx, "__startup_probe__", wrapped)
	if err != nil {
		return err
	}
	if !bytes.Equal(probe, plain) {
		return fmt.Errorf("credentials: KMS startup probe mismatch")
	}
	return nil
}
