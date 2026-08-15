package tenancy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleDeveloper = "developer"
	RoleOperator  = "operator"
	RoleViewer    = "viewer"
)

var (
	ErrInvitationInvalid = errors.New("invitation is invalid or expired")
	ErrLastOwner         = errors.New("workspace must retain at least one active owner")
	ErrRoleEscalation    = errors.New("role cannot be administered by this member")
)

// MemberManager is the workspace-membership control plane used by the API.
// Every workspace-scoped method accepts the verified workspace explicitly.
type MemberManager interface {
	CreateInvitation(context.Context, Mutation, string, string, string, string, string, time.Time) (Invitation, error)
	ListInvitations(context.Context, string) ([]Invitation, error)
	AcceptInvitation(context.Context, Mutation, string, string) (StoredMembership, error)
	ListMembers(context.Context, string) ([]StoredMembership, error)
	SetMembershipRole(context.Context, Mutation, string, string, string, string) (StoredMembership, error)
	SetMembershipStatusInWorkspace(context.Context, Mutation, string, string, string, string) (StoredMembership, error)
	ListMembershipAudit(context.Context, string, int) ([]MembershipAudit, error)
	CanRefreshUser(context.Context, string) bool
	PrimaryMembership(context.Context, string) (StoredMembership, bool)
}

type MembershipAudit struct {
	ID           string          `json:"id"`
	ActorSubject string          `json:"actor_subject"`
	RequestID    string          `json:"request_id"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Before       json.RawMessage `json:"before"`
	After        json.RawMessage `json:"after"`
	CreatedAt    time.Time       `json:"created_at"`
}

func IsMembershipRole(role string) bool { return roleRank(role) > 0 }

func CanAdministerRole(actorRole, targetRole string) bool {
	actor, target := roleRank(actorRole), roleRank(targetRole)
	return actor >= roleRank(RoleAdmin) && target > 0 && target <= actor
}

func roleRank(role string) int {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleDeveloper:
		return 3
	case RoleAdmin:
		return 4
	case RoleOwner:
		return 5
	default:
		return 0
	}
}

func (s *PostgresStore) ListInvitations(ctx context.Context, workspaceID string) ([]Invitation, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,organization_id,workspace_id,normalized_email,role,
		CASE WHEN status='pending' AND expires_at<=NOW() THEN 'expired' ELSE status END,expires_at
		FROM invitations WHERE workspace_id=$1 ORDER BY created_at DESC`, strings.TrimSpace(workspaceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invitation
	for rows.Next() {
		var invitation Invitation
		if err := rows.Scan(&invitation.ID, &invitation.OrganizationID, &invitation.WorkspaceID, &invitation.Email, &invitation.Role, &invitation.Status, &invitation.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, invitation)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListMembers(ctx context.Context, workspaceID string) ([]StoredMembership, error) {
	rows, err := s.pool.Query(ctx, `SELECT m.id,m.organization_id,m.workspace_id,m.user_id,m.role,m.status,COALESCE(u.normalized_email,''),u.display_name
		FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.workspace_id=$1 AND m.status<>'deleted' ORDER BY m.created_at`, strings.TrimSpace(workspaceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredMembership
	for rows.Next() {
		var membership StoredMembership
		if err := rows.Scan(&membership.ID, &membership.OrganizationID, &membership.WorkspaceID, &membership.UserID, &membership.Role, &membership.Status, &membership.Email, &membership.DisplayName); err != nil {
			return nil, err
		}
		out = append(out, membership)
	}
	return out, rows.Err()
}

func (s *PostgresStore) AcceptInvitation(ctx context.Context, mutation Mutation, token, userID string) (StoredMembership, error) {
	token, userID = strings.TrimSpace(token), strings.TrimSpace(userID)
	if token == "" || userID == "" {
		return StoredMembership{}, ErrInvitationInvalid
	}
	hash := sha256.Sum256([]byte(token))
	tx, err := s.beginMutation(ctx, mutation)
	if err != nil {
		return StoredMembership{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var invitation Invitation
	var acceptedBy *string
	err = tx.QueryRow(ctx, `SELECT id,organization_id,workspace_id,normalized_email,role,status,expires_at,accepted_by FROM invitations WHERE token_hash=$1 FOR UPDATE`, hash[:]).Scan(
		&invitation.ID, &invitation.OrganizationID, &invitation.WorkspaceID, &invitation.Email, &invitation.Role, &invitation.Status, &invitation.ExpiresAt, &acceptedBy)
	if err != nil {
		return StoredMembership{}, ErrInvitationInvalid
	}
	if invitation.Status == "accepted" && acceptedBy != nil && *acceptedBy == userID {
		var existing StoredMembership
		if err := scanMembership(tx.QueryRow(ctx, `SELECT id,organization_id,workspace_id,user_id,role,status FROM memberships WHERE workspace_id=$1 AND user_id=$2`, invitation.WorkspaceID, userID), &existing); err != nil {
			return StoredMembership{}, ErrInvitationInvalid
		}
		return existing, tx.Commit(ctx)
	}
	if invitation.Status != "pending" || !invitation.ExpiresAt.After(time.Now().UTC()) {
		if invitation.Status == "pending" {
			_, _ = tx.Exec(ctx, `UPDATE invitations SET status='expired' WHERE id=$1`, invitation.ID)
			_ = tx.Commit(ctx)
		}
		return StoredMembership{}, ErrInvitationInvalid
	}
	var userEmail *string
	if err := tx.QueryRow(ctx, `SELECT normalized_email FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&userEmail); err != nil || userEmail == nil || normalizeEmail(*userEmail) != invitation.Email {
		return StoredMembership{}, ErrInvitationInvalid
	}

	membership := StoredMembership{ID: newID("mem"), OrganizationID: invitation.OrganizationID, WorkspaceID: invitation.WorkspaceID, UserID: userID, Role: invitation.Role, Status: MembershipActive}
	if err := scanMembership(tx.QueryRow(ctx, `INSERT INTO memberships(id,organization_id,workspace_id,user_id,role,status)
		VALUES($1,$2,$3,$4,$5,'active') ON CONFLICT(workspace_id,user_id) DO UPDATE SET role=EXCLUDED.role,status='active',updated_at=NOW()
		RETURNING id,organization_id,workspace_id,user_id,role,status`, membership.ID, membership.OrganizationID, membership.WorkspaceID, membership.UserID, membership.Role), &membership); err != nil {
		return StoredMembership{}, err
	}
	before := invitation
	invitation.Status = "accepted"
	if _, err := tx.Exec(ctx, `UPDATE invitations SET status='accepted',accepted_by=$2,accepted_at=$3 WHERE id=$1`, invitation.ID, userID, mutationTime(mutation)); err != nil {
		return StoredMembership{}, err
	}
	if err := insertAudit(ctx, tx, mutation, "invitation.accept", "invitation", invitation.ID, before, invitation); err != nil {
		return StoredMembership{}, err
	}
	return membership, tx.Commit(ctx)
}

func (s *PostgresStore) SetMembershipRole(ctx context.Context, mutation Mutation, workspaceID, membershipID, actorRole, role string) (StoredMembership, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if !IsMembershipRole(role) {
		return StoredMembership{}, errors.New("unknown membership role")
	}
	return s.changeMembership(ctx, mutation, workspaceID, membershipID, actorRole, role, "", "membership.role")
}

func (s *PostgresStore) SetMembershipStatusInWorkspace(ctx context.Context, mutation Mutation, workspaceID, membershipID, actorRole, status string) (StoredMembership, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != MembershipActive && status != MembershipSuspended && status != MembershipDeleted {
		return StoredMembership{}, errors.New("membership status must be active, suspended, or deleted")
	}
	return s.changeMembership(ctx, mutation, workspaceID, membershipID, actorRole, "", status, "membership.status")
}

func (s *PostgresStore) changeMembership(ctx context.Context, mutation Mutation, workspaceID, membershipID, actorRole, role, status, action string) (StoredMembership, error) {
	tx, err := s.beginMutation(ctx, mutation)
	if err != nil {
		return StoredMembership{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "members:"+workspaceID); err != nil {
		return StoredMembership{}, err
	}
	var before StoredMembership
	if err := scanMembership(tx.QueryRow(ctx, `SELECT id,organization_id,workspace_id,user_id,role,status FROM memberships WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspaceID, membershipID), &before); err != nil {
		return StoredMembership{}, err
	}
	if !CanAdministerRole(actorRole, before.Role) || (role != "" && !CanAdministerRole(actorRole, role)) {
		return StoredMembership{}, ErrRoleEscalation
	}
	after := before
	if role != "" {
		after.Role = role
	}
	if status != "" {
		after.Status = status
	}
	if before.Role == RoleOwner && before.Status == MembershipActive && (after.Role != RoleOwner || after.Status != MembershipActive) {
		var owners int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM memberships WHERE workspace_id=$1 AND role='owner' AND status='active'`, workspaceID).Scan(&owners); err != nil {
			return StoredMembership{}, err
		}
		if owners <= 1 {
			return StoredMembership{}, ErrLastOwner
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE memberships SET role=$2,status=$3,updated_at=$4 WHERE id=$1`, membershipID, after.Role, after.Status, mutationTime(mutation)); err != nil {
		return StoredMembership{}, err
	}
	if err := insertAudit(ctx, tx, mutation, action, "membership", membershipID, before, after); err != nil {
		return StoredMembership{}, err
	}
	return after, tx.Commit(ctx)
}

func (s *PostgresStore) ListMembershipAudit(ctx context.Context, workspaceID string, limit int) ([]MembershipAudit, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT a.id,a.actor_subject,a.request_id,a.action,a.resource_type,a.resource_id,a.before_data,a.after_data,a.created_at
		FROM tenant_mutation_audit a WHERE
		(a.resource_type='membership' AND a.resource_id IN (SELECT id FROM memberships WHERE workspace_id=$1)) OR
		(a.resource_type='invitation' AND a.resource_id IN (SELECT id FROM invitations WHERE workspace_id=$1))
		ORDER BY a.created_at DESC LIMIT $2`, workspaceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MembershipAudit
	for rows.Next() {
		var entry MembershipAudit
		if err := rows.Scan(&entry.ID, &entry.ActorSubject, &entry.RequestID, &entry.Action, &entry.ResourceType, &entry.ResourceID, &entry.Before, &entry.After, &entry.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// PrimaryMembership returns the active membership a newly-issued token should
// act under, or ok=false when the user has none.
//
// A user can belong to several workspaces. Until MU-029 (switch workspaces in
// the GUI) gives them a way to choose, the token has to name one, and the
// oldest active membership is the stable answer: it does not change when
// someone is added to a second workspace, so a member's tokens do not silently
// start acting somewhere else because an admin invited them elsewhere.
//
// Suspended and deleted memberships are excluded. A user with only those has
// authenticated but has nowhere to act, and the caller must refuse rather than
// fall back — falling back means the personal workspace, which is the
// deployment's own.
func (s *PostgresStore) PrimaryMembership(ctx context.Context, userID string) (StoredMembership, bool) {
	var m StoredMembership
	err := s.pool.QueryRow(ctx, `SELECT id, organization_id, workspace_id, user_id, role, status
		FROM memberships WHERE user_id=$1 AND status='active'
		ORDER BY created_at ASC, id ASC LIMIT 1`, strings.TrimSpace(userID)).
		Scan(&m.ID, &m.OrganizationID, &m.WorkspaceID, &m.UserID, &m.Role, &m.Status)
	if err != nil {
		return StoredMembership{}, false
	}
	return m, true
}

// CanRefreshUser permits a global identity session while the user has an
// active membership, or while a verified-email invitation is pending so a new
// user can sign in and accept it. Workspace middleware still re-resolves the
// exact membership on every workspace request.
func (s *PostgresStore) CanRefreshUser(ctx context.Context, userID string) bool {
	var allowed bool
	err := s.pool.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM memberships WHERE user_id=$1 AND status='active') OR
		EXISTS(SELECT 1 FROM invitations i JOIN users u ON u.normalized_email=i.normalized_email
			WHERE u.id=$1 AND i.status='pending' AND i.expires_at>NOW())`, strings.TrimSpace(userID)).Scan(&allowed)
	return err == nil && allowed
}

// Assert the implementation stays aligned with the gateway control-plane contract.
var _ MemberManager = (*PostgresStore)(nil)
