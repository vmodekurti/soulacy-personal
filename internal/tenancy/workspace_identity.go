package tenancy

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type WorkspaceIdentityProvider struct {
	WorkspaceID  string    `json:"workspace_id"`
	ProviderType string    `json:"provider_type"`
	Issuer       string    `json:"issuer"`
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"-"`
	Audience     string    `json:"audience"`
	Scopes       []string  `json:"scopes"`
	LockedAt     time.Time `json:"locked_at"`
}

type WorkspaceLoginConfig struct {
	WorkspaceID        string `json:"workspace_id"`
	WorkspaceSlug      string `json:"workspace_slug"`
	WorkspaceName      string `json:"workspace_name"`
	WorkspaceLogo      string `json:"workspace_logo,omitempty"`
	OrganizationID     string `json:"organization_id"`
	OrganizationName   string `json:"organization_name"`
	OrganizationLogo   string `json:"organization_logo,omitempty"`
	OrganizationStatus string `json:"organization_status"`
	WorkspaceStatus    string `json:"workspace_status"`
	IdentityStatus     string `json:"identity_status"`
	ProviderType       string `json:"provider_type,omitempty"`
}

type WorkspaceIdentitySetupRequest struct {
	SetupToken    string   `json:"setup_token"`
	ProviderType  string   `json:"provider_type"`
	Issuer        string   `json:"issuer"`
	ClientID      string   `json:"client_id"`
	ClientSecret  string   `json:"client_secret"`
	Audience      string   `json:"audience"`
	Scopes        []string `json:"scopes"`
	WorkspaceLogo string   `json:"workspace_logo,omitempty"`
}

type WorkspaceIdentityStore interface {
	WorkspaceLoginConfig(context.Context, string) (WorkspaceLoginConfig, error)
	ValidateWorkspaceSetupToken(context.Context, string, string) error
	ResolveWorkspaceIdentityProvider(context.Context, string) (WorkspaceIdentityProvider, error)
	ActivateWorkspaceIdentity(context.Context, Mutation, string, WorkspaceIdentitySetupRequest) (WorkspaceIdentityProvider, error)
}

// ValidateWorkspaceSetupToken is a cheap authorization check used before the
// gateway performs provider discovery. ActivateWorkspaceIdentity repeats the
// check under its transaction lock, so this is not relied on for atomicity.
func (s *PostgresStore) ValidateWorkspaceSetupToken(ctx context.Context, workspaceID, token string) error {
	hash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM workspace_setup_tokens t
		JOIN workspaces w ON w.id=t.workspace_id
		WHERE t.workspace_id=$1 AND t.token_hash=$2 AND t.consumed_at IS NULL
		AND t.expires_at>NOW() AND w.identity_status='pending' AND w.status='active'
		AND EXISTS(SELECT 1 FROM organizations o WHERE o.id=w.organization_id AND o.status='active')
	)`, strings.TrimSpace(workspaceID), hash[:]).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("workspace setup link is invalid or expired")
	}
	return nil
}

// ConfigureProviderEncryptionSecret derives an independent encryption key for
// tenant OIDC client secrets. The deployment key is never stored in Postgres.
func (s *PostgresStore) ConfigureProviderEncryptionSecret(secret string) {
	if s == nil {
		return
	}
	sum := sha256.Sum256([]byte("soulacy:workspace-oidc:" + secret))
	s.providerKey = sum[:]
}

func (s *PostgresStore) WorkspaceLoginConfig(ctx context.Context, workspaceID string) (WorkspaceLoginConfig, error) {
	var out WorkspaceLoginConfig
	err := s.pool.QueryRow(ctx, `SELECT w.id,w.slug,w.name,COALESCE(w.logo_data_url,''),w.organization_id,o.name,COALESCE(o.logo_data_url,''),o.status,w.status,w.identity_status,COALESCE(p.provider_type,'')
		FROM workspaces w JOIN organizations o ON o.id=w.organization_id LEFT JOIN workspace_identity_providers p ON p.workspace_id=w.id WHERE w.id=$1 OR w.slug=LOWER($1)`, strings.TrimSpace(workspaceID)).Scan(
		&out.WorkspaceID, &out.WorkspaceSlug, &out.WorkspaceName, &out.WorkspaceLogo, &out.OrganizationID, &out.OrganizationName, &out.OrganizationLogo, &out.OrganizationStatus, &out.WorkspaceStatus, &out.IdentityStatus, &out.ProviderType)
	return out, err
}

func (s *PostgresStore) ResolveWorkspaceIdentityProvider(ctx context.Context, workspaceID string) (WorkspaceIdentityProvider, error) {
	var out WorkspaceIdentityProvider
	var encrypted, scopesJSON []byte
	err := s.pool.QueryRow(ctx, `SELECT workspace_id,provider_type,issuer,client_id,client_secret_ciphertext,audience,scopes,locked_at FROM workspace_identity_providers WHERE workspace_id=$1 AND status='active'`, strings.TrimSpace(workspaceID)).Scan(
		&out.WorkspaceID, &out.ProviderType, &out.Issuer, &out.ClientID, &encrypted, &out.Audience, &scopesJSON, &out.LockedAt)
	if err != nil {
		return WorkspaceIdentityProvider{}, err
	}
	if len(scopesJSON) > 0 {
		_ = json.Unmarshal(scopesJSON, &out.Scopes)
	}
	if len(encrypted) > 0 {
		out.ClientSecret, err = s.decryptProviderSecret(encrypted)
		if err != nil {
			return WorkspaceIdentityProvider{}, err
		}
	}
	return out, nil
}

func (s *PostgresStore) ActivateWorkspaceIdentity(ctx context.Context, mutation Mutation, workspaceID string, req WorkspaceIdentitySetupRequest) (WorkspaceIdentityProvider, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	req.SetupToken, req.ProviderType, req.Issuer, req.ClientID = strings.TrimSpace(req.SetupToken), strings.ToLower(strings.TrimSpace(req.ProviderType)), strings.TrimRight(strings.TrimSpace(req.Issuer), "/"), strings.TrimSpace(req.ClientID)
	if req.Audience = strings.TrimSpace(req.Audience); req.Audience == "" {
		req.Audience = req.ClientID
	}
	if len(req.Scopes) == 0 {
		req.Scopes = []string{"openid", "profile", "email"}
	}
	if workspaceID == "" || req.SetupToken == "" || req.ProviderType == "" || req.Issuer == "" || req.ClientID == "" {
		return WorkspaceIdentityProvider{}, errors.New("workspace, setup token, provider, issuer, and client ID are required")
	}
	if err := validateLogoDataURL(req.WorkspaceLogo); err != nil {
		return WorkspaceIdentityProvider{}, err
	}
	encrypted, err := s.encryptProviderSecret(req.ClientSecret)
	if err != nil {
		return WorkspaceIdentityProvider{}, err
	}
	scopes, _ := json.Marshal(req.Scopes)
	tx, err := s.beginMutation(ctx, mutation)
	if err != nil {
		return WorkspaceIdentityProvider{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tokenHash := sha256.Sum256([]byte(req.SetupToken))
	var identityStatus string
	if err = tx.QueryRow(ctx, `SELECT w.identity_status FROM workspaces w JOIN organizations o ON o.id=w.organization_id JOIN workspace_setup_tokens t ON t.workspace_id=w.id WHERE w.id=$1 AND t.token_hash=$2 AND t.consumed_at IS NULL AND t.expires_at>NOW() AND w.status='active' AND o.status='active' FOR UPDATE`, workspaceID, tokenHash[:]).Scan(&identityStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return WorkspaceIdentityProvider{}, errors.New("workspace setup link is invalid or expired")
		}
		return WorkspaceIdentityProvider{}, err
	}
	if identityStatus != "pending" {
		return WorkspaceIdentityProvider{}, errors.New("workspace identity provider is already locked")
	}
	var orgID string
	if err = tx.QueryRow(ctx, `SELECT organization_id FROM workspaces WHERE id=$1`, workspaceID).Scan(&orgID); err != nil {
		return WorkspaceIdentityProvider{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workspace_identity_providers(workspace_id,organization_id,provider_type,issuer,client_id,client_secret_ciphertext,audience,scopes) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, workspaceID, orgID, req.ProviderType, req.Issuer, req.ClientID, encrypted, req.Audience, scopes); err != nil {
		return WorkspaceIdentityProvider{}, errors.New("workspace identity provider is already locked")
	}
	if _, err = tx.Exec(ctx, `UPDATE workspaces SET identity_status='active',logo_data_url=COALESCE($2,logo_data_url) WHERE id=$1`, workspaceID, nullableString(req.WorkspaceLogo)); err != nil {
		return WorkspaceIdentityProvider{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE workspace_setup_tokens SET consumed_at=NOW() WHERE workspace_id=$1`, workspaceID); err != nil {
		return WorkspaceIdentityProvider{}, err
	}
	out := WorkspaceIdentityProvider{WorkspaceID: workspaceID, ProviderType: req.ProviderType, Issuer: req.Issuer, ClientID: req.ClientID, Audience: req.Audience, Scopes: req.Scopes, LockedAt: time.Now().UTC()}
	if err = insertAudit(ctx, tx, mutation, "workspace.identity.activate", "workspace", workspaceID, nil, out); err != nil {
		return WorkspaceIdentityProvider{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return WorkspaceIdentityProvider{}, err
	}
	return out, nil
}

func (s *PostgresStore) encryptProviderSecret(value string) ([]byte, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	if len(s.providerKey) != 32 {
		return nil, errors.New("workspace provider encryption is unavailable")
	}
	block, err := aes.NewCipher(s.providerKey)
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
	return gcm.Seal(nonce, nonce, []byte(value), nil), nil
}

func (s *PostgresStore) decryptProviderSecret(value []byte) (string, error) {
	if len(value) == 0 {
		return "", nil
	}
	if len(s.providerKey) != 32 {
		return "", errors.New("workspace provider decryption is unavailable")
	}
	block, err := aes.NewCipher(s.providerKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(value) < gcm.NonceSize() {
		return "", errors.New("workspace provider secret is invalid")
	}
	plain, err := gcm.Open(nil, value[:gcm.NonceSize()], value[gcm.NonceSize():], nil)
	return string(plain), err
}
