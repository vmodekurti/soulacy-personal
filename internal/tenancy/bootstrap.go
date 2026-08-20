package tenancy

import (
	"context"
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
	OwnerEmail       string `json:"owner_email"`
	OwnerDisplayName string `json:"owner_display_name"`
}

type BootstrapResult struct {
	Organization Organization     `json:"organization"`
	Workspace    Workspace        `json:"workspace"`
	User         User             `json:"user"`
	Membership   StoredMembership `json:"membership"`
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
	if _, err = tx.Exec(ctx, `INSERT INTO workspaces(id,organization_id,name) VALUES($1,$2,$3)`, result.Workspace.ID, result.Organization.ID, result.Workspace.Name); err != nil {
		return BootstrapResult{}, err
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
