package tenancy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

var ErrAlreadyBootstrapped = errors.New("tenant catalog is already bootstrapped")
var ErrBootstrapBlocked = errors.New("tenant catalog is partially initialized")

type BootstrapState struct {
	Required      bool  `json:"required"`
	Blocked       bool  `json:"blocked"`
	Organizations int64 `json:"organizations"`
	Workspaces    int64 `json:"workspaces"`
	Memberships   int64 `json:"memberships"`
}

type BootstrapRequest struct {
	OrganizationName string `json:"organization_name"`
	WorkspaceName    string `json:"workspace_name"`
	WorkspaceSlug    string `json:"workspace_slug,omitempty"`
	OwnerEmail       string `json:"owner_email"`
	OwnerDisplayName string `json:"owner_display_name"`
	OrganizationLogo string `json:"organization_logo,omitempty"`
	WorkspaceLogo    string `json:"workspace_logo,omitempty"`
}

type BootstrapResult struct {
	Organization Organization     `json:"organization"`
	Workspace    Workspace        `json:"workspace"`
	User         User             `json:"user"`
	Membership   StoredMembership `json:"membership"`
	SetupToken   string           `json:"setup_token,omitempty"`
}

type BootstrapManager interface {
	BootstrapState(context.Context) (BootstrapState, error)
	BootstrapFirstOwner(context.Context, Mutation, BootstrapRequest) (BootstrapResult, error)
}

func (s *PostgresStore) BootstrapState(ctx context.Context) (BootstrapState, error) {
	if s == nil || s.pool == nil {
		return BootstrapState{}, errors.New("tenancy store is unavailable")
	}
	var state BootstrapState
	err := s.pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM organizations), (SELECT COUNT(*) FROM workspaces), (SELECT COUNT(*) FROM memberships)`).Scan(&state.Organizations, &state.Workspaces, &state.Memberships)
	if err != nil {
		return BootstrapState{}, err
	}
	state.Required = state.Organizations == 0 && state.Workspaces == 0 && state.Memberships == 0
	state.Blocked = !state.Required && state.Memberships == 0
	return state, nil
}

// BootstrapFirstOwner uses one transaction and a global advisory lock so two
// setup tabs can never create two initial tenants.
func (s *PostgresStore) BootstrapFirstOwner(ctx context.Context, mutation Mutation, req BootstrapRequest) (BootstrapResult, error) {
	if s == nil || s.pool == nil {
		return BootstrapResult{}, errors.New("tenancy store is unavailable")
	}
	if strings.TrimSpace(mutation.ActorSubject) == "" || strings.TrimSpace(mutation.RequestID) == "" {
		return BootstrapResult{}, errors.New("mutation actor and request ID are required")
	}
	req.OrganizationName = strings.TrimSpace(req.OrganizationName)
	req.WorkspaceName = strings.TrimSpace(req.WorkspaceName)
	req.WorkspaceSlug = strings.ToLower(strings.TrimSpace(req.WorkspaceSlug))
	req.OwnerEmail = normalizeEmail(req.OwnerEmail)
	req.OwnerDisplayName = strings.TrimSpace(req.OwnerDisplayName)
	if req.OrganizationName == "" || req.WorkspaceName == "" || req.OwnerEmail == "" || req.OwnerDisplayName == "" || !strings.Contains(req.OwnerEmail, "@") {
		return BootstrapResult{}, errors.New("organization name, workspace name, valid owner email, and owner display name are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "soulacy:first-owner-bootstrap"); err != nil {
		return BootstrapResult{}, err
	}
	var organizations, workspaces, memberships int64
	if err = tx.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM organizations), (SELECT COUNT(*) FROM workspaces), (SELECT COUNT(*) FROM memberships)`).Scan(&organizations, &workspaces, &memberships); err != nil {
		return BootstrapResult{}, err
	}
	if memberships > 0 {
		return BootstrapResult{}, ErrAlreadyBootstrapped
	}
	if organizations > 0 || workspaces > 0 {
		return BootstrapResult{}, ErrBootstrapBlocked
	}

	result := BootstrapResult{Organization: Organization{ID: newID("org"), Name: req.OrganizationName}, Workspace: Workspace{ID: newID("ws"), Name: req.WorkspaceName}, User: User{Email: req.OwnerEmail, DisplayName: req.OwnerDisplayName}}
	result.Workspace.OrganizationID = result.Organization.ID
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE normalized_email=$1 FOR UPDATE`, req.OwnerEmail).Scan(&result.User.ID); errors.Is(err, pgx.ErrNoRows) {
		result.User.ID = newID("usr")
		_, err = tx.Exec(ctx, `INSERT INTO users(id,normalized_email,display_name) VALUES($1,$2,$3)`, result.User.ID, result.User.Email, result.User.DisplayName)
	} else if err == nil {
		_, err = tx.Exec(ctx, `UPDATE users SET display_name=$2 WHERE id=$1`, result.User.ID, result.User.DisplayName)
	}
	if err != nil {
		return BootstrapResult{}, err
	}
	result.Membership = StoredMembership{ID: newID("mem"), OrganizationID: result.Organization.ID, WorkspaceID: result.Workspace.ID, UserID: result.User.ID, Role: "owner", Status: MembershipActive, Email: result.User.Email, DisplayName: result.User.DisplayName}
	if _, err = tx.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,$2)`, result.Organization.ID, result.Organization.Name); err != nil {
		return BootstrapResult{}, err
	}
	result.Workspace.Slug = req.WorkspaceSlug
	if result.Workspace.Slug == "" {
		result.Workspace.Slug = workspaceSlug(result.Workspace.Name, result.Workspace.ID)
	} else if err = validateWorkspaceSlug(result.Workspace.Slug); err != nil {
		return BootstrapResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workspaces(id,slug,organization_id,name) VALUES($1,$2,$3,$4)`, result.Workspace.ID, result.Workspace.Slug, result.Organization.ID, result.Workspace.Name); err != nil {
		return BootstrapResult{}, workspaceInsertError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO memberships(id,organization_id,workspace_id,user_id,role,status) VALUES($1,$2,$3,$4,'owner','active')`, result.Membership.ID, result.Organization.ID, result.Workspace.ID, result.User.ID); err != nil {
		return BootstrapResult{}, err
	}
	if err = insertAudit(ctx, tx, mutation, "tenant.bootstrap", "organization", result.Organization.ID, nil, result); err != nil {
		return BootstrapResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return BootstrapResult{}, err
	}
	return result, nil
}

// ProvisionOrganization creates a new tenant and its first workspace/owner
// after initial bootstrap. Unlike BootstrapFirstOwner it does not require an
// empty catalog, and it never grants the platform actor membership.
func (s *PostgresStore) ProvisionOrganization(ctx context.Context, mutation Mutation, req BootstrapRequest) (BootstrapResult, error) {
	return s.provisionTenant(ctx, mutation, "tenant.provision", req, "")
}

// ProvisionWorkspace adds a workspace to an existing organization and assigns
// a designated first owner. The deployment operator remains outside it.
func (s *PostgresStore) ProvisionWorkspace(ctx context.Context, mutation Mutation, organizationID string, req WorkspaceProvisionRequest) (BootstrapResult, error) {
	if s == nil || s.pool == nil {
		return BootstrapResult{}, errors.New("tenancy store is unavailable")
	}
	organizationID = strings.TrimSpace(organizationID)
	if organizationID == "" {
		return BootstrapResult{}, errors.New("organization ID is required")
	}
	var organizationName string
	if err := s.pool.QueryRow(ctx, `SELECT name FROM organizations WHERE id=$1`, organizationID).Scan(&organizationName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BootstrapResult{}, errors.New("organization not found")
		}
		return BootstrapResult{}, err
	}
	return s.provisionTenant(ctx, mutation, "workspace.provision", BootstrapRequest{
		OrganizationName: organizationName, WorkspaceName: req.WorkspaceName,
		WorkspaceSlug: req.WorkspaceSlug,
		OwnerEmail:    req.OwnerEmail, OwnerDisplayName: req.OwnerDisplayName,
		WorkspaceLogo: req.WorkspaceLogo,
	}, organizationID)
}

func (s *PostgresStore) provisionTenant(ctx context.Context, mutation Mutation, action string, req BootstrapRequest, existingOrganizationID string) (BootstrapResult, error) {
	if s == nil || s.pool == nil {
		return BootstrapResult{}, errors.New("tenancy store is unavailable")
	}
	if strings.TrimSpace(mutation.ActorSubject) == "" || strings.TrimSpace(mutation.RequestID) == "" {
		return BootstrapResult{}, errors.New("mutation actor and request ID are required")
	}
	req.OrganizationName, req.WorkspaceName = strings.TrimSpace(req.OrganizationName), strings.TrimSpace(req.WorkspaceName)
	req.WorkspaceSlug = strings.ToLower(strings.TrimSpace(req.WorkspaceSlug))
	req.OwnerEmail, req.OwnerDisplayName = normalizeEmail(req.OwnerEmail), strings.TrimSpace(req.OwnerDisplayName)
	if err := validateLogoDataURL(req.OrganizationLogo); err != nil {
		return BootstrapResult{}, err
	}
	if err := validateLogoDataURL(req.WorkspaceLogo); err != nil {
		return BootstrapResult{}, err
	}
	if req.OrganizationName == "" || req.WorkspaceName == "" || req.OwnerEmail == "" || req.OwnerDisplayName == "" || !strings.Contains(req.OwnerEmail, "@") {
		return BootstrapResult{}, errors.New("organization name, workspace name, valid owner email, and owner display name are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockKey := "soulacy:provision:" + existingOrganizationID
	if existingOrganizationID == "" {
		lockKey = "soulacy:provision-organizations"
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return BootstrapResult{}, err
	}
	result := BootstrapResult{Organization: Organization{ID: existingOrganizationID, Name: req.OrganizationName, LogoDataURL: req.OrganizationLogo}, Workspace: Workspace{ID: newID("ws"), Name: req.WorkspaceName, LogoDataURL: req.WorkspaceLogo, IdentityStatus: "pending"}, User: User{Email: req.OwnerEmail, DisplayName: req.OwnerDisplayName}}
	if result.Organization.ID == "" {
		result.Organization.ID = newID("org")
		if _, err = tx.Exec(ctx, `INSERT INTO organizations(id,name,logo_data_url) VALUES($1,$2,$3)`, result.Organization.ID, result.Organization.Name, nullableString(result.Organization.LogoDataURL)); err != nil {
			return BootstrapResult{}, err
		}
	} else {
		if err = tx.QueryRow(ctx, `SELECT name,status FROM organizations WHERE id=$1 FOR UPDATE`, result.Organization.ID).Scan(&result.Organization.Name, &result.Organization.Status); err != nil {
			return BootstrapResult{}, err
		}
		if result.Organization.Status != WorkspaceActive {
			return BootstrapResult{}, errors.New("organization is suspended")
		}
	}
	result.Workspace.OrganizationID = result.Organization.ID
	result.Workspace.Slug = req.WorkspaceSlug
	if result.Workspace.Slug == "" {
		result.Workspace.Slug = workspaceSlug(result.Workspace.Name, result.Workspace.ID)
	} else if err = validateWorkspaceSlug(result.Workspace.Slug); err != nil {
		return BootstrapResult{}, err
	}
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE normalized_email=$1 FOR UPDATE`, req.OwnerEmail).Scan(&result.User.ID); errors.Is(err, pgx.ErrNoRows) {
		result.User.ID = newID("usr")
		_, err = tx.Exec(ctx, `INSERT INTO users(id,normalized_email,display_name) VALUES($1,$2,$3)`, result.User.ID, result.User.Email, result.User.DisplayName)
	} else if err == nil {
		_, err = tx.Exec(ctx, `UPDATE users SET display_name=$2 WHERE id=$1`, result.User.ID, result.User.DisplayName)
	}
	if err != nil {
		return BootstrapResult{}, err
	}
	result.Membership = StoredMembership{ID: newID("mem"), OrganizationID: result.Organization.ID, WorkspaceID: result.Workspace.ID, UserID: result.User.ID, Role: RoleOwner, Status: MembershipActive, Email: result.User.Email, DisplayName: result.User.DisplayName}
	if _, err = tx.Exec(ctx, `INSERT INTO workspaces(id,slug,organization_id,name,logo_data_url,identity_status) VALUES($1,$2,$3,$4,$5,'pending')`, result.Workspace.ID, result.Workspace.Slug, result.Organization.ID, result.Workspace.Name, nullableString(result.Workspace.LogoDataURL)); err != nil {
		return BootstrapResult{}, workspaceInsertError(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO memberships(id,organization_id,workspace_id,user_id,role,status) VALUES($1,$2,$3,$4,'owner','active')`, result.Membership.ID, result.Organization.ID, result.Workspace.ID, result.User.ID); err != nil {
		return BootstrapResult{}, err
	}
	result.SetupToken, err = newWorkspaceSetupToken()
	if err != nil {
		return BootstrapResult{}, err
	}
	setupHash := sha256.Sum256([]byte(result.SetupToken))
	if _, err = tx.Exec(ctx, `INSERT INTO workspace_setup_tokens(workspace_id,token_hash,expires_at) VALUES($1,$2,NOW()+INTERVAL '7 days')`, result.Workspace.ID, setupHash[:]); err != nil {
		return BootstrapResult{}, err
	}
	// The raw setup token is a one-time credential. Return it to the platform
	// administrator once, but never copy it into durable audit JSON.
	auditResult := result
	auditResult.SetupToken = ""
	if err = insertAudit(ctx, tx, mutation, action, "workspace", result.Workspace.ID, nil, auditResult); err != nil {
		return BootstrapResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return BootstrapResult{}, err
	}
	return result, nil
}

func newWorkspaceSetupToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func validateLogoDataURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len(value) > 700000 {
		return errors.New("logo must be smaller than 500 KB")
	}
	allowed := []string{"data:image/png;base64,", "data:image/jpeg;base64,", "data:image/webp;base64,"}
	for _, prefix := range allowed {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			return nil
		}
	}
	return errors.New("logo must be a PNG, JPEG, or WebP image")
}
