package gateway

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/redact"
	"github.com/soulacy/soulacy/internal/wsroot"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/sdk/storage"
)

const adminAuditAgentID = "_system"

type adminAuditRecord struct {
	Timestamp      time.Time      `json:"timestamp"`
	Action         string         `json:"action"`
	Resource       string         `json:"resource"`
	Target         string         `json:"target,omitempty"`
	Actor          string         `json:"actor,omitempty"`
	Role           string         `json:"role,omitempty"`
	PrincipalKind  string         `json:"principal_kind,omitempty"`
	CredentialID   string         `json:"credential_id,omitempty"`
	OrganizationID string         `json:"organization_id,omitempty"`
	WorkspaceID    string         `json:"workspace_id,omitempty"`
	RequestID      string         `json:"request_id,omitempty"`
	Status         string         `json:"status"`
	Details        map[string]any `json:"details,omitempty"`
}

func responseAuditStatus(c *fiber.Ctx, err error) string {
	if err != nil || c == nil || c.Response().StatusCode() >= fiber.StatusBadRequest {
		return "failed"
	}
	return "ok"
}

// auditActor identifies who is making a request, for any record that needs to
// name a person: the admin audit log, and the rules store's Author field.
//
// Shared rather than duplicated so those two can never disagree about who did
// something. Falls back to "api-key" — an explicit "authenticated, but only by
// a shared key" rather than a blank that reads as a lost value.
func (s *Server) auditActor(c *fiber.Ctx) string {
	if c != nil {
		if claims := auth.ClaimsFromCtx(c); claims != nil {
			if a := strings.TrimSpace(claims.Subject); a != "" {
				return a
			}
			if a := strings.TrimSpace(claims.Email); a != "" {
				return a
			}
		}
	}
	return "api-key"
}

func (s *Server) recordAdminAudit(c *fiber.Ctx, action, resource, target, status string, details map[string]any) {
	if s == nil || s.actions == nil {
		return
	}
	rec := adminAuditRecord{
		Timestamp: time.Now().UTC(),
		Action:    strings.TrimSpace(action),
		Resource:  strings.TrimSpace(resource),
		Target:    strings.TrimSpace(target),
		Status:    strings.TrimSpace(status),
		Details:   scrubAuditDetails(details),
	}
	if rec.Status == "" {
		rec.Status = "ok"
	}
	if c != nil {
		if requestID, ok := c.Locals("request_id").(string); ok {
			rec.RequestID = requestID
		}
		if identity, ok := requestIdentity(c); ok {
			rec.Role, rec.PrincipalKind, rec.CredentialID = identity.Role(), identity.PrincipalKind(), identity.CredentialID()
			rec.OrganizationID, rec.WorkspaceID = identity.OrganizationID(), identity.WorkspaceID()
		} else if claims := auth.ClaimsFromCtx(c); claims != nil {
			rec.Role = strings.TrimSpace(claims.Role)
			rec.PrincipalKind, rec.CredentialID = principalKind(claims), credentialID(claims)
			rec.OrganizationID, rec.WorkspaceID = claims.OrganizationID, claims.WorkspaceID
		}
	}
	rec.Actor = s.auditActor(c)
	// The event carries the workspace, not just the record payload. Reads of
	// the audit trail are workspace-scoped, so an event appended without one
	// lands in the personal workspace and becomes invisible to the tenant
	// whose action produced it — an audit record that silently disappears is
	// worse than one that leaks, because nothing surfaces the loss.
	event := message.Event{
		Type:        "admin.audit",
		WorkspaceID: wsroot.Normalize(rec.WorkspaceID),
		AgentID:     adminAuditAgentID,
		SessionID:   rec.RequestID,
		Timestamp:   rec.Timestamp,
		Payload:     rec,
	}
	// Through the durable surface when the backend has one. Append's contract
	// is "never block, drop when the queue is full", which is right for run
	// telemetry and wrong here: an audit record that vanishes under load
	// leaves no trace of its own absence, so the trail silently stops being
	// able to say an action did not happen.
	if durable, ok := s.actions.(storage.DurableActionLogBackend); ok {
		if !durable.AppendDurable(event) {
			// The backend has already logged the loss at Error with the event
			// details. Repeating it here with the ACTION named is the part an
			// operator can act on — "an audit record was lost" is not
			// actionable; "credential.create by usr_x was not recorded" is.
			s.logger().Error("admin audit record was LOST — this action is not in the audit trail",
				zap.String("action", rec.Action),
				zap.String("resource", rec.Resource),
				zap.String("target", rec.Target),
				zap.String("actor", rec.Actor),
				zap.String("workspace", rec.WorkspaceID),
				zap.String("request_id", rec.RequestID))
		}
		return
	}
	// No durable surface. Falling through silently would make the guarantee
	// depend on which backend happens to be wired, which is the shape of a
	// promise nobody can check.
	s.logger().Warn("action log has no durable append; audit records may be dropped under load",
		zap.String("action", rec.Action))
	s.actions.Append(event)
}

func adminAuditEventTypes() map[string]bool {
	return map[string]bool{"admin.audit": true}
}

func (s *Server) handleAdminAudit(c *fiber.Ctx) error {
	if s.actions == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "admin audit not available (action log disabled)")
	}
	limit := c.QueryInt("limit", 100)
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	// MU-031 criterion 4. A cursor walks the trail backwards a page at a time.
	//
	// Keyset, not offset: this table is append-only under load, so an offset
	// moves under every reader and the page boundary skips or repeats rows.
	// An audit trail that repeats records is one an investigator cannot count.
	cursor := strings.TrimSpace(c.Query("cursor"))
	events, next, paginated, err := s.actionLog(c).QueryEventsPage(adminAuditAgentID, "", limit, adminAuditEventTypes(), cursor)
	if !paginated {
		if cursor != "" {
			// The caller asked to continue and this backend cannot. Answering
			// with an unpaginated first page would tell them they had seen the
			// whole trail.
			return s.errMsg(c, fiber.StatusServiceUnavailable, "this action log backend cannot paginate the audit trail")
		}
		var durable bool
		events, durable, err = s.actionLog(c).QueryEvents(adminAuditAgentID, "", limit, adminAuditEventTypes())
		if !durable {
			return s.errMsg(c, fiber.StatusServiceUnavailable, "admin audit requires a durable action log backend")
		}
	}
	if err != nil {
		if errors.Is(err, actionlog.ErrInvalidCursor) {
			// A malformed cursor is a client bug or tampering. An empty page
			// would make both look like the end of the data.
			return s.errMsg(c, fiber.StatusBadRequest, "cursor is not a valid page cursor")
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	records := make([]adminAuditRecord, 0, len(events))
	for _, ev := range events {
		rec, ok := adminAuditRecordFromPayload(ev.Payload)
		if !ok {
			continue
		}
		if rec.Timestamp.IsZero() {
			rec.Timestamp = ev.Timestamp
		}
		records = append(records, rec)
	}
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].Timestamp.After(records[j].Timestamp)
	})
	// MU-031 criterion 4: reads of the trail are themselves audited.
	//
	// Reading an audit trail is not a neutral act. It is how somebody finds
	// out what is being watched, and — for an insider — the step before
	// deciding whether an action will be noticed. Today it left no trace at
	// all, so the one question the trail could not answer was "who has been
	// looking at it".
	//
	// Recorded AFTER the read succeeds, so a refused or failed read does not
	// produce a record implying it was served. The record names the size of
	// what was returned rather than its contents: what matters is that a
	// person pulled the trail and how much of it, not a copy of it inside
	// itself.
	s.recordAdminAudit(c, "audit.read", "audit", "", "ok", map[string]any{
		"limit":     limit,
		"returned":  len(records),
		"paginated": paginated,
		"continued": cursor != "",
	})
	return c.JSON(fiber.Map{
		"available": true,
		"source":    "action-log",
		"count":     len(records),
		"events":    records,
		// Empty means the end of the TRAIL, not the end of this page. A client
		// that treats a non-empty cursor as "there is more" must be able to
		// trust that, or it loops on a page that ended exactly on the last row.
		"next_cursor": next,
	})
}

func adminAuditRecordFromPayload(payload any) (adminAuditRecord, bool) {
	switch v := payload.(type) {
	case adminAuditRecord:
		return v, true
	case *adminAuditRecord:
		if v == nil {
			return adminAuditRecord{}, false
		}
		return *v, true
	case map[string]any:
		var rec adminAuditRecord
		raw, err := json.Marshal(v)
		if err != nil {
			return adminAuditRecord{}, false
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return adminAuditRecord{}, false
		}
		return rec, true
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return adminAuditRecord{}, false
		}
		var rec adminAuditRecord
		if err := json.Unmarshal(raw, &rec); err != nil {
			return adminAuditRecord{}, false
		}
		return rec, rec.Action != "" || rec.Resource != ""
	}
}

// scrubAuditDetails removes secret-looking values from an audit record's
// change summary.
//
// IT RECURSES, and that is not a refinement. The first version inspected
// top-level keys only, so a detail map with any nesting at all —
// `{"credential": {"api_key": "sk_live_…"}}` — was written through unchanged:
// the redaction ran, matched nothing, and produced a record that reads as
// scrubbed. internal/audit/redactArgs was rewritten for exactly this reason
// and its comment says so; this one had not been.
//
// The downstream redact.Value in actionlog.Append does catch it in practice,
// which is why nothing had leaked. That makes this a defence-in-depth layer
// that silently was not defending — the worse kind, because the layer below is
// the only thing anyone would find if they looked.
func scrubAuditDetails(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := scrubAuditMap(in, 0)
	if len(out) == 0 {
		return nil
	}
	return out
}

// auditScrubMaxDepth bounds the walk. A cyclic or pathologically nested detail
// map must not be able to hang a request through the audit path.
const auditScrubMaxDepth = 8

func scrubAuditMap(in map[string]any, depth int) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		if redact.SecretKeyName(key) {
			// A secret-named key holding a MAP is descended into rather than
			// blanked, because the name describes the group and the secret is
			// a leaf inside it. `credential: {name, id, api_key}` blanked
			// wholesale costs the audit record the two fields that make it
			// traceable — and this codebase's credential call sites nest
			// exactly like that, so wholesale blanking would make the records
			// for the most sensitive actions the least informative.
			//
			// Anything else under such a key is blanked: a scalar under
			// `token` IS the token, and a LIST under `secrets` is a list of
			// them, with no benign siblings to preserve.
			if nested, ok := v.(map[string]any); ok {
				out[key] = scrubAuditMap(nested, depth+1)
				continue
			}
			out[key] = "***"
			continue
		}
		out[key] = scrubAuditValue(v, depth)
	}
	return out
}

func scrubAuditValue(v any, depth int) any {
	if depth >= auditScrubMaxDepth {
		// Dropping the subtree rather than returning it unexamined: an audit
		// record missing a deeply nested detail is a smaller problem than one
		// containing a secret.
		return "***"
	}
	switch typed := v.(type) {
	case map[string]any:
		return scrubAuditMap(typed, depth+1)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = scrubAuditValue(item, depth+1)
		}
		return out
	default:
		return v
	}
}

// The key-name test lives in internal/redact, shared with every other
// redactor. The list this file carried did not know about `bearer`, `cookie`,
// `passphrase`, `dsn` or `connection_string`.

func configPatchSections(p PatchableConfig) []string {
	sections := make([]string, 0, 12)
	if p.Server != nil {
		sections = append(sections, "server")
	}
	if p.Runtime != nil {
		sections = append(sections, "runtime")
	}
	if p.UI != nil {
		sections = append(sections, "ui")
	}
	if p.Executor != nil {
		sections = append(sections, "executor")
	}
	if p.LLM != nil {
		sections = append(sections, "llm")
	}
	if p.Log != nil {
		sections = append(sections, "log")
	}
	if p.Search != nil {
		sections = append(sections, "search")
	}
	if p.Voice != nil {
		sections = append(sections, "voice")
	}
	if p.Costs != nil {
		sections = append(sections, "costs")
	}
	if p.Ops != nil {
		sections = append(sections, "ops")
	}
	if p.Deployment != nil {
		sections = append(sections, "deployment")
	}
	if p.Security != nil {
		sections = append(sections, "security")
	}
	if p.AgentDirs != nil {
		sections = append(sections, "agent_dirs")
	}
	if p.SkillDirs != nil {
		sections = append(sections, "skill_dirs")
	}
	if p.PluginsConfig != nil {
		sections = append(sections, "plugins_config")
	}
	return sections
}

func channelSettingKeys(settings map[string]any, spec *channelSpec) []string {
	if len(settings) == 0 {
		return nil
	}
	secret := map[string]bool{}
	if spec != nil {
		for _, f := range spec.Fields {
			if f.Secret {
				secret[f.Key] = true
			}
		}
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		label := k
		if secret[k] {
			label = k + " (secret)"
		}
		keys = append(keys, label)
	}
	sort.Strings(keys)
	return keys
}

// H1 — auditStringList, auditCount, and auditFmt were unused helper funcs
// left behind by an earlier admin-audit refactor. Deleted to satisfy the
// unused-linter. auditBoolPtrSet retained (still called from configPatchSections).
func auditBoolPtrSet(v *bool) bool {
	return v != nil
}

// logger returns a usable logger even when the Server was constructed
// directly, which several tests and the share-view path do.
//
// It exists because the loudest thing this file does — reporting a LOST audit
// record — ran straight into a nil dereference on those servers. A fail-loud
// path that panics instead of reporting is worse than a silent one: the
// original failure is a missing record, and the replacement is a crashed
// request that says nothing about it.
func (s *Server) logger() *zap.Logger {
	if s == nil || s.log == nil {
		return zap.NewNop()
	}
	return s.log
}

// auditing wraps a handler so its outcome reaches the audit trail (MU-031
// criterion 2).
//
// A WRAPPER RATHER THAN A LINE IN EACH HANDLER, for the reason every other
// cross-cutting concern on this branch became one: nineteen handlers each
// remembering to record is eighteen chances to forget, and the record that
// gets forgotten is the one nobody misses until an investigation needs it.
// Registering the wrapper at the route also puts the audit decision next to
// the authorization decision, which is where a reviewer is already looking.
//
// `targetParam` names the path parameter identifying the thing acted on, or ""
// when the action has no single target. It is read AFTER the handler runs but
// from the same request, so a handler that rewrites params cannot change what
// was recorded.
//
// The status comes from the response, so a REFUSED action is recorded as
// refused rather than not recorded at all. "Somebody tried and was stopped" is
// one of the more interesting lines an audit trail carries, and a trail of
// successes cannot show an attack that failed.
func (s *Server) auditing(action, resource, targetParam string, handler fiber.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		err := handler(c)
		target := ""
		if targetParam != "" {
			target = strings.TrimSpace(c.Params(targetParam))
		}
		s.recordAdminAudit(c, action, resource, target, responseAuditStatus(c, err), nil)
		return err
	}
}
