package tenancy

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

var ErrInvalidLifecycleTransition = errors.New("tenancy: invalid lifecycle transition")

func platformLifecycleStatus(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != WorkspaceActive && value != WorkspaceSuspended {
		return "", errors.New("status must be active or suspended")
	}
	return value, nil
}

func lifecycleReason(status, reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if status == WorkspaceSuspended && reason == "" {
		return "", errors.New("a suspension reason is required")
	}
	if len(reason) > 500 {
		return "", errors.New("reason must be 500 characters or fewer")
	}
	return reason, nil
}

// SetOrganizationStatus places or removes a control-plane hold. Child
// workspace rows are deliberately untouched: removing the organization hold
// restores only workspaces whose own lifecycle is active.
func (s *PostgresStore) SetOrganizationStatus(ctx context.Context, mutation Mutation, organizationID, status, reason string) (Organization, error) {
	organizationID = strings.TrimSpace(organizationID)
	status, err := platformLifecycleStatus(status)
	if err != nil {
		return Organization{}, err
	}
	reason, err = lifecycleReason(status, reason)
	if err != nil {
		return Organization{}, err
	}
	tx, err := s.beginMutation(ctx, mutation)
	if err != nil {
		return Organization{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "organization:"+organizationID); err != nil {
		return Organization{}, err
	}
	var before Organization
	if err = tx.QueryRow(ctx, `SELECT id,name,status,COALESCE(logo_data_url,'') FROM organizations WHERE id=$1 FOR UPDATE`, organizationID).Scan(&before.ID, &before.Name, &before.Status, &before.LogoDataURL); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Organization{}, errors.New("organization not found")
		}
		return Organization{}, err
	}
	after := before
	after.Status = status
	if _, err = tx.Exec(ctx, `UPDATE organizations SET status=$2 WHERE id=$1`, organizationID, status); err != nil {
		return Organization{}, err
	}
	auditAfter := struct {
		Organization Organization `json:"organization"`
		Reason       string       `json:"reason,omitempty"`
	}{after, reason}
	if err = insertAudit(ctx, tx, mutation, "organization.status.update", "organization", organizationID, before, auditAfter); err != nil {
		return Organization{}, err
	}
	return after, tx.Commit(ctx)
}

// SetWorkspaceStatus changes only active/suspended. Deletion transitions stay
// exclusively in WorkspaceLifecycle so a platform hold cannot cancel or
// resurrect a deletion.
func (s *PostgresStore) SetWorkspaceStatus(ctx context.Context, mutation Mutation, workspaceID, status, reason string) (WorkspaceRecord, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	status, err := platformLifecycleStatus(status)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	reason, err = lifecycleReason(status, reason)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	tx, err := s.beginMutation(ctx, mutation)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	before, err := s.lockWorkspace(ctx, tx, workspaceID)
	if err != nil {
		return WorkspaceRecord{}, err
	}
	if before.Status != WorkspaceActive && before.Status != WorkspaceSuspended {
		return WorkspaceRecord{}, ErrInvalidLifecycleTransition
	}
	after := before
	after.Status = status
	if _, err = tx.Exec(ctx, `UPDATE workspaces SET status=$2 WHERE id=$1`, workspaceID, status); err != nil {
		return WorkspaceRecord{}, err
	}
	auditAfter := struct {
		Workspace WorkspaceRecord `json:"workspace"`
		Reason    string          `json:"reason,omitempty"`
	}{after, reason}
	if err = insertAudit(ctx, tx, mutation, "workspace.status.update", "workspace", workspaceID, before, auditAfter); err != nil {
		return WorkspaceRecord{}, err
	}
	return after, tx.Commit(ctx)
}
