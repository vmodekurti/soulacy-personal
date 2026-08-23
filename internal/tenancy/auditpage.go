package tenancy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// auditpage.go — keyset pagination for the membership audit trail (MU-031
// criterion 4).
//
// KEYSET, NOT OFFSET, for the reason the action-event trail uses it: an offset
// cursor skips or repeats rows whenever a write lands between two pages, and
// this table is append-only under load, so the page boundary moves under every
// reader. An audit trail that repeats records is one an investigator cannot
// count; one that skips them is worse.
//
// The audit ID is a STRING here, not the integer the action log uses, so the
// cursor carries (created_at, id) with a text comparison on the tie-break.
// That is fine because the IDs are generated with a fixed prefix and a
// monotonic-ish random suffix, and — more importantly — the tie-break only has
// to be TOTAL and STABLE, not meaningful. Two rows with the same microsecond
// need a deterministic order, and any deterministic order will do as long as
// the query and the cursor agree on it.

// ErrInvalidAuditCursor reports a cursor this package did not produce.
//
// Distinct from "no more results" on purpose. A malformed cursor is a client
// bug or a tampering attempt, and answering it with an empty page makes both
// look like the end of the data — an investigator would conclude the trail
// stopped there.
var ErrInvalidAuditCursor = errors.New("tenancy: invalid audit page cursor")

// auditCursorVersion prefixes the encoding so a future change to the key can
// be told from corruption rather than silently mis-parsed.
const auditCursorVersion = "v1"

type auditCursor struct {
	createdAt time.Time
	id        string
}

func encodeAuditCursor(c auditCursor) string {
	raw := fmt.Sprintf("%s|%d|%s", auditCursorVersion, c.createdAt.UTC().UnixNano(), c.id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeAuditCursor(encoded string) (auditCursor, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return auditCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return auditCursor{}, ErrInvalidAuditCursor
	}
	// SplitN with 3, not Split: an audit ID may itself contain the separator,
	// and a plain Split would reject a legitimate cursor for a row whose ID
	// happens to have a pipe in it — a failure that would appear only for some
	// rows, long after this was written.
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 || parts[0] != auditCursorVersion {
		return auditCursor{}, ErrInvalidAuditCursor
	}
	nanos, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return auditCursor{}, ErrInvalidAuditCursor
	}
	if strings.TrimSpace(parts[2]) == "" {
		return auditCursor{}, ErrInvalidAuditCursor
	}
	return auditCursor{createdAt: time.Unix(0, nanos).UTC(), id: parts[2]}, nil
}

// maxMembershipAuditPage bounds one page. A caller asking for more is CLAMPED
// to it rather than reset to the default — asking for more and receiving fewer
// than the default is the behaviour this replaced, and it is the kind of bug
// that makes a client add a retry loop instead of a bug report.
//
// AuditPage is one page of the membership audit trail.
type AuditPage struct {
	Entries []MembershipAudit `json:"entries"`
	// NextCursor is empty when there is no further page. Emitted only when a
	// LOOKAHEAD row proved there is more, never merely because the page was
	// full: a cursor handed out at an exact boundary makes the last page
	// indistinguishable from a full one, and the client fetches an empty page
	// to find out.
	NextCursor string `json:"next_cursor,omitempty"`
}

// ListMembershipAuditPage returns one keyset page of the workspace's
// membership audit trail, newest first.
func (s *PostgresStore) ListMembershipAuditPage(ctx context.Context, workspaceID string, limit int, cursor string) (AuditPage, error) {
	if s == nil || s.pool == nil {
		return AuditPage{}, errors.New("tenancy store is unavailable")
	}
	if limit <= 0 {
		limit = 100
	} else if limit > maxMembershipAuditPage {
		limit = maxMembershipAuditPage
	}
	position, err := decodeAuditCursor(cursor)
	if err != nil {
		return AuditPage{}, err
	}

	// The scope predicate is identical to the unpaginated query's, and that is
	// load-bearing rather than tidy: a page whose filter differs from the
	// list's would return rows the caller cannot see anywhere else, or hide
	// rows they can.
	const scope = `((a.resource_type='membership' AND a.resource_id IN (SELECT id FROM memberships WHERE workspace_id=$1)) OR
		(a.resource_type='invitation' AND a.resource_id IN (SELECT id FROM invitations WHERE workspace_id=$1)) OR
		(a.resource_type='workspace' AND a.resource_id=$1))`

	// Read limit+1. The extra row is never returned; it is the only way to
	// know whether a next cursor exists without a second query, and without it
	// the last page and a full page are indistinguishable.
	lookahead := limit + 1

	var rows interface {
		Next() bool
		Scan(...any) error
		Err() error
		Close()
	}
	if position.id == "" {
		rows, err = s.pool.Query(ctx, `SELECT a.id,a.actor_subject,a.request_id,a.action,a.resource_type,a.resource_id,a.before_data,a.after_data,a.created_at
			FROM tenant_mutation_audit a WHERE `+scope+`
			ORDER BY a.created_at DESC, a.id DESC LIMIT $2`, workspaceID, lookahead)
	} else {
		// `created_at < $2 OR (created_at = $2 AND id < $3)` is the keyset
		// predicate for a DESC order. The equality arm is what makes rows
		// sharing a timestamp paginate correctly instead of being skipped in
		// batches — which is the failure an audit trail cannot tolerate,
		// because bursts of membership changes share a timestamp by nature.
		rows, err = s.pool.Query(ctx, `SELECT a.id,a.actor_subject,a.request_id,a.action,a.resource_type,a.resource_id,a.before_data,a.after_data,a.created_at
			FROM tenant_mutation_audit a WHERE `+scope+`
			AND (a.created_at < $2 OR (a.created_at = $2 AND a.id < $3))
			ORDER BY a.created_at DESC, a.id DESC LIMIT $4`, workspaceID, position.createdAt, position.id, lookahead)
	}
	if err != nil {
		return AuditPage{}, err
	}
	defer rows.Close()

	var page AuditPage
	for rows.Next() {
		var entry MembershipAudit
		if err := rows.Scan(&entry.ID, &entry.ActorSubject, &entry.RequestID, &entry.Action,
			&entry.ResourceType, &entry.ResourceID, &entry.Before, &entry.After, &entry.CreatedAt); err != nil {
			return AuditPage{}, err
		}
		page.Entries = append(page.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return AuditPage{}, err
	}
	if len(page.Entries) > limit {
		last := page.Entries[limit-1]
		page.Entries = page.Entries[:limit]
		page.NextCursor = encodeAuditCursor(auditCursor{createdAt: last.CreatedAt, id: last.ID})
	}
	return page, nil
}

// ListPlatformAuditPage returns deployment-created tenant lifecycle changes
// across the catalog. The API layer intentionally projects these rows down to
// metadata and never returns Before or After to the deployment operator.
func (s *PostgresStore) ListPlatformAuditPage(ctx context.Context, limit int, cursor string) (AuditPage, error) {
	if s == nil || s.pool == nil {
		return AuditPage{}, errors.New("tenancy store is unavailable")
	}
	if limit <= 0 {
		limit = 100
	} else if limit > maxMembershipAuditPage {
		limit = maxMembershipAuditPage
	}
	position, err := decodeAuditCursor(cursor)
	if err != nil {
		return AuditPage{}, err
	}
	const actions = `('tenant.bootstrap','tenant.provision','workspace.provision','organization.status.update','workspace.status.update','workspace.address.update')`
	lookahead := limit + 1
	var rows interface {
		Next() bool
		Scan(...any) error
		Err() error
		Close()
	}
	if position.id == "" {
		rows, err = s.pool.Query(ctx, `SELECT id,actor_subject,request_id,action,resource_type,resource_id,before_data,after_data,created_at
			FROM tenant_mutation_audit WHERE action IN `+actions+`
			ORDER BY created_at DESC,id DESC LIMIT $1`, lookahead)
	} else {
		rows, err = s.pool.Query(ctx, `SELECT id,actor_subject,request_id,action,resource_type,resource_id,before_data,after_data,created_at
			FROM tenant_mutation_audit WHERE action IN `+actions+`
			AND (created_at < $1 OR (created_at = $1 AND id < $2))
			ORDER BY created_at DESC,id DESC LIMIT $3`, position.createdAt, position.id, lookahead)
	}
	if err != nil {
		return AuditPage{}, err
	}
	defer rows.Close()
	var page AuditPage
	for rows.Next() {
		var entry MembershipAudit
		if err := rows.Scan(&entry.ID, &entry.ActorSubject, &entry.RequestID, &entry.Action,
			&entry.ResourceType, &entry.ResourceID, &entry.Before, &entry.After, &entry.CreatedAt); err != nil {
			return AuditPage{}, err
		}
		page.Entries = append(page.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return AuditPage{}, err
	}
	if len(page.Entries) > limit {
		last := page.Entries[limit-1]
		page.Entries = page.Entries[:limit]
		page.NextCursor = encodeAuditCursor(auditCursor{createdAt: last.CreatedAt, id: last.ID})
	}
	return page, nil
}
