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
	Token     string         `json:"token"`
	Version   int            `json:"version"`
	CreatedAt time.Time      `json:"created_at"`
	Title     string         `json:"title"`
	AgentName string         `json:"agent_name,omitempty"`
	Messages  []shareMessage `json:"messages"`
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

	snap := sharedSession{
		Token:     uuid.New().String(),
		Version:   1,
		CreatedAt: time.Now().UTC(),
		Title:     strings.TrimSpace(req.Title),
		AgentName: strings.TrimSpace(req.AgentName),
		Messages:  req.Messages,
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
	pruneShares(dir)
	if err := os.WriteFile(filepath.Join(dir, snap.Token+".json"), data, 0o600); err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "could not save share: "+err.Error())
	}
	return c.JSON(fiber.Map{"token": snap.Token, "path": "/#share/" + snap.Token})
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

// pruneShares deletes expired snapshots and, if the directory is still over the
// cap, the oldest remaining ones. Errors are ignored throughout: pruning is
// housekeeping, and failing a user's share because a stale file could not be
// removed would be the wrong trade.
func pruneShares(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type aged struct {
		name string
		mod  time.Time
	}
	cutoff := time.Now().Add(-shareTTL)
	var live []aged
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
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
