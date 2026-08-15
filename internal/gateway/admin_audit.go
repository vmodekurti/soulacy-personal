package gateway

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/pkg/message"
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
	s.actions.Append(message.Event{
		Type:      "admin.audit",
		AgentID:   adminAuditAgentID,
		SessionID: rec.RequestID,
		Timestamp: rec.Timestamp,
		Payload:   rec,
	})
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
	events, durable, err := s.actionLog(c).QueryEvents(adminAuditAgentID, "", limit, adminAuditEventTypes())
	if !durable {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "admin audit requires a durable action log backend")
	}
	if err != nil {
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
	return c.JSON(fiber.Map{
		"available": true,
		"source":    "action-log",
		"count":     len(records),
		"events":    records,
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

func scrubAuditDetails(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		lower := strings.ToLower(key)
		if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") {
			out[key] = "***"
			continue
		}
		out[key] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

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
