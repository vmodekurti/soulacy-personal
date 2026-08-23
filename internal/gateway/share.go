// share.go — shareable read-only chat sessions.
//
// A user can turn the current conversation into a link anyone can open without
// an API key. handleCreateShare (authed) persists a snapshot of the thread
// keyed by an unguessable token; handleShareView (PUBLIC) serves that snapshot
// as JSON so the SPA can render a read-only view at /#share/<token>.
//
// Snapshots are plain JSON files under <workspace>/data/shares/<token>.json so
// they survive restarts, matching the gateway's existing "write a small JSON
// blob to the workspace" persistence style.

package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// shareTokenRe guards the public path parameter: tokens are UUIDs, so anything
// else (including path-traversal attempts like "..") is rejected before we ever
// build a filesystem path.
var shareTokenRe = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

// maxShareBytes caps the request body so a shared session can't be used to write
// arbitrarily large files.
const maxShareBytes = 4 << 20 // 4 MiB

type shareMessage struct {
	Role        string `json:"role"`
	Text        string `json:"text"`
	Via         string `json:"via,omitempty"`
	TS          any    `json:"ts,omitempty"`
	Attachments []struct {
		Name string `json:"name"`
	} `json:"attachments,omitempty"`
}

type sharedSession struct {
	Token   string `json:"token"`
	Version int    `json:"version"`
	// WorkspaceID and Creator record who published the link. A share is
	// deliberately readable by anyone holding the token — that is the whole
	// feature — so isolation here is not about the public read. It is about
	// three things a tenant needs and did not have: knowing which shares of
	// theirs exist, being the only one who can revoke them, and having the act
	// of publishing a conversation appear in their audit trail.
	WorkspaceID string         `json:"workspace_id,omitempty"`
	Creator     string         `json:"creator,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
	Title       string         `json:"title"`
	AgentName   string         `json:"agent_name,omitempty"`
	Messages    []shareMessage `json:"messages"`
}

// shareSummary is the listing form: enough to recognise and revoke a link,
// without replaying its contents.
type shareSummary struct {
	Token     string    `json:"token"`
	Title     string    `json:"title"`
	AgentName string    `json:"agent_name,omitempty"`
	Creator   string    `json:"creator,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Messages  int       `json:"messages"`
}

func sharesDir() (string, error) {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(ws.Data, "shares")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// handleCreateShare (authed) stores a snapshot of the client's current thread
// and returns its token + the shareable path.
func (s *Server) handleCreateShare(c *fiber.Ctx) error {
	if len(c.Body()) > maxShareBytes {
		return s.errMsg(c, fiber.StatusRequestEntityTooLarge, "conversation is too large to share")
	}
	var req struct {
		Title     string         `json:"title"`
		AgentName string         `json:"agent_name"`
		Messages  []shareMessage `json:"messages"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if len(req.Messages) == 0 {
		return s.errMsg(c, fiber.StatusBadRequest, "nothing to share yet — the conversation is empty")
	}

	workspaceID, creator := s.shareScope(c)
	now := time.Now().UTC()
	snap := sharedSession{
		Token:       uuid.New().String(),
		Version:     2,
		WorkspaceID: workspaceID,
		Creator:     creator,
		CreatedAt:   now,
		ExpiresAt:   now.Add(shareTTL),
		Title:       strings.TrimSpace(req.Title),
		AgentName:   strings.TrimSpace(req.AgentName),
		Messages:    req.Messages,
	}
	if snap.Title == "" {
		snap.Title = "Shared conversation"
	}

	dir, err := sharesDir()
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "share storage is unavailable: "+err.Error())
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "could not encode share")
	}
	// Prune before writing. Nothing else ever touched this directory: there was
	// no expiry, no per-caller cap and no delete route, and POST /chat/share is
	// gated on chat:READ — the weakest permission in the table. A single
	// authenticated viewer could therefore grow the disk by ~2.4 GB/min, forever,
	// within the rate limiter's budget.
	pruneShares(dir, workspaceID)
	if err := os.WriteFile(filepath.Join(dir, snap.Token+".json"), data, 0o600); err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "could not save share: "+err.Error())
	}
	// Publishing a conversation to an unauthenticated URL is exactly the kind
	// of act an owner needs to be able to review after the fact.
	s.recordAdminAudit(c, "share.create", "chat-share", snap.Token, "ok", map[string]any{
		"agent_name": snap.AgentName, "messages": len(snap.Messages),
		"expires_at": snap.ExpiresAt.Format(time.RFC3339),
	})
	return c.JSON(fiber.Map{
		"token": snap.Token, "path": "/#share/" + snap.Token,
		"expires_at": snap.ExpiresAt.Format(time.RFC3339),
	})
}

// handleShareView (PUBLIC — registered outside the authenticated group) returns
// a stored snapshot as JSON. The token is validated against a strict UUID shape
// before any filesystem access.
func (s *Server) handleShareView(c *fiber.Ctx) error {
	token := c.Params("token")
	if !shareTokenRe.MatchString(token) {
		return s.errMsg(c, fiber.StatusNotFound, "share not found")
	}
	dir, err := sharesDir()
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "share storage is unavailable")
	}
	data, err := os.ReadFile(filepath.Join(dir, token+".json"))
	if err != nil {
		return s.errMsg(c, fiber.StatusNotFound, "this shared conversation was not found (it may have been removed)")
	}
	// Expiry is enforced on read, not only by the prune sweep. Pruning runs
	// when someone creates a share, so on a quiet deployment an expired link
	// could otherwise stay live indefinitely.
	var snap sharedSession
	if json.Unmarshal(data, &snap) == nil && !snap.ExpiresAt.IsZero() && time.Now().UTC().After(snap.ExpiresAt) {
		_ = os.Remove(filepath.Join(dir, token+".json"))
		return s.errMsg(c, fiber.StatusNotFound, "this shared conversation has expired")
	}
	c.Set("Content-Type", "application/json; charset=utf-8")
	// Shared snapshots are immutable; let clients/CDNs cache briefly.
	c.Set("Cache-Control", "public, max-age=300")
	return c.Send(data)
}

const (
	// shareTTL is how long a shared snapshot stays readable. A share is a link
	// someone pastes into a chat; a month is generous for that and finite, which
	// the previous "forever" was not.
	shareTTL = 30 * 24 * time.Hour
	// maxShares bounds the directory even when shares are created faster than the
	// TTL retires them. That, not the TTL, is what actually caps the disk.
	maxShares = 2000
)

// pruneShares deletes expired snapshots anywhere in the directory, then
// enforces the count cap *within the calling workspace only*.
//
// Expiry is deployment-wide because an expired share is garbage in every
// tenant. The count cap is not: a global cap would let one tenant's burst of
// shares evict another tenant's live links, breaking URLs that people have
// already pasted elsewhere. That is both a denial of service and a lever one
// tenant could pull against another, so the cap is per workspace. The cost is
// that total share disk now scales with the number of workspaces, which the
// tenancy layer already bounds.
//
// Errors are ignored throughout: pruning is housekeeping, and failing a user's
// share because a stale file could not be removed would be the wrong trade.
func pruneShares(dir, workspaceID string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type aged struct {
		name string
		mod  time.Time
	}
	now := time.Now().UTC()
	var live []aged
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		snap, err := readShare(dir, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		if shareExpired(snap, info, now) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		if snapWorkspace(snap) != workspaceID {
			continue
		}
		live = append(live, aged{name: e.Name(), mod: info.ModTime()})
	}
	if len(live) < maxShares {
		return
	}
	sort.Slice(live, func(i, j int) bool { return live[i].mod.Before(live[j].mod) })
	for _, a := range live[:len(live)-maxShares+1] {
		_ = os.Remove(filepath.Join(dir, a.name))
	}
}

// shareExpired treats a v1 snapshot (no ExpiresAt) as expiring shareTTL after
// its file mtime, which is how expiry worked before the field existed.
func shareExpired(snap sharedSession, info os.FileInfo, now time.Time) bool {
	if !snap.ExpiresAt.IsZero() {
		return now.After(snap.ExpiresAt)
	}
	return info.ModTime().Before(now.Add(-shareTTL))
}

// snapWorkspace maps a pre-tenant snapshot to the personal workspace, which is
// what a single-user installation's shares were.
func snapWorkspace(snap sharedSession) string {
	return wsroot.Normalize(snap.WorkspaceID)
}

func readShare(dir, token string) (sharedSession, error) {
	data, err := os.ReadFile(filepath.Join(dir, token+".json"))
	if err != nil {
		return sharedSession{}, err
	}
	var snap sharedSession
	if err := json.Unmarshal(data, &snap); err != nil {
		return sharedSession{}, err
	}
	return snap, nil
}

func purgeWorkspaceShares(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	dir, err := sharesDir()
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	workspaceID = wsroot.Normalize(workspaceID)
	var removed workspacepurge.Removed
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		token := strings.TrimSuffix(entry.Name(), ".json")
		snap, err := readShare(dir, token)
		if err != nil || snapWorkspace(snap) != workspaceID {
			continue
		}
		if info, statErr := entry.Info(); statErr == nil {
			removed.Bytes += info.Size()
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
		removed.Rows++
	}
	removed.Note = "public share links revoked and snapshots purged"
	return removed, nil
}

// shareScope is the workspace and person a share request acts for.
func (s *Server) shareScope(c *fiber.Ctx) (workspaceID, creator string) {
	if c != nil {
		if identity, ok := requestIdentity(c); ok {
			return wsroot.Normalize(identity.WorkspaceID()), identity.Subject()
		}
	}
	return wsroot.PersonalWorkspaceID, ""
}

// handleListShares returns the calling workspace's live shares. Revocation is
// only meaningful if an owner can first see what exists.
func (s *Server) handleListShares(c *fiber.Ctx) error {
	dir, err := sharesDir()
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "share storage is unavailable")
	}
	workspaceID, _ := s.shareScope(c)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return c.JSON(fiber.Map{"shares": []shareSummary{}, "count": 0})
	}
	now := time.Now().UTC()
	out := make([]shareSummary, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		snap, err := readShare(dir, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil || snapWorkspace(snap) != workspaceID {
			continue
		}
		info, err := e.Info()
		if err != nil || shareExpired(snap, info, now) {
			continue
		}
		out = append(out, shareSummary{
			Token: snap.Token, Title: snap.Title, AgentName: snap.AgentName,
			Creator: snap.Creator, CreatedAt: snap.CreatedAt, ExpiresAt: snap.ExpiresAt,
			Messages: len(snap.Messages),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return c.JSON(fiber.Map{"shares": out, "count": len(out)})
}

// handleRevokeShare deletes one of the calling workspace's shares.
//
// A share another workspace owns reads as "not found" — the same answer a
// token that never existed gives — so tokens cannot be probed through this
// route, and one tenant cannot take down another's link.
func (s *Server) handleRevokeShare(c *fiber.Ctx) error {
	token := c.Params("token")
	if !shareTokenRe.MatchString(token) {
		return s.errMsg(c, fiber.StatusNotFound, "share not found")
	}
	dir, err := sharesDir()
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "share storage is unavailable")
	}
	workspaceID, _ := s.shareScope(c)
	snap, err := readShare(dir, token)
	if err != nil || snapWorkspace(snap) != workspaceID {
		return s.errMsg(c, fiber.StatusNotFound, "share not found")
	}
	if err := os.Remove(filepath.Join(dir, token+".json")); err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "could not revoke share")
	}
	s.recordAdminAudit(c, "share.revoke", "chat-share", token, "ok", map[string]any{
		"agent_name": snap.AgentName, "created_at": snap.CreatedAt.Format(time.RFC3339),
	})
	return c.JSON(fiber.Map{"revoked": token})
}
