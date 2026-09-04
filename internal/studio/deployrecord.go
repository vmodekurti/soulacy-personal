package studio

// deployrecord.go — the deployment record and its append-only history (ST-16).
//
// A save is not a deployment. Saving stages a definition; deploying asserts
// "this exact thing, built against these exact rules, proven by this exact
// test evidence, is the thing that may now run on a schedule". Without a
// record of those four facets the system cannot answer the only two questions
// that matter when a scheduled agent starts misbehaving at 03:00:
//
//   1. What changed? (workflow hash, rules version, provider configuration)
//   2. Was it ever proven to work? (the certification record)
//
// and it cannot perform the one repair that is always available: put the
// previous version back.
//
// Two properties are load-bearing:
//
//   - History is APPEND-ONLY. A rollback re-applies an older definition AS A
//     NEW VERSION rather than truncating history. Rewriting history to "undo"
//     a deployment is the failure mode where an incident review cannot see the
//     bad version that caused the incident — the same reasoning as
//     internal/agentmemory's RollbackProcedural.
//   - ProviderConfig holds NAMES ONLY. It records which provider and model each
//     role used, never the credential that reaches them. A deployment record is
//     read by support bundles, audit exports and incident reviews; a secret
//     that lands here has effectively been published, and no later redaction
//     can recall it. RedactProviderConfig is therefore applied on the way IN,
//     not on the way out.
//
// Persistence follows the library.go convention: pure, file-backed, rooted at
// a caller-supplied directory so it is fully unit-testable with t.TempDir()
// and has no dependency on the gateway or config.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/pkg/agent"
)

// ErrNoDeployment reports that an agent has never been deployed. Callers must
// distinguish this from a read failure: "never deployed" means the readiness
// gate has no opinion (the agent was not created through Studio), whereas an
// unreadable history means we cannot prove certification and must block.
var ErrNoDeployment = errors.New("studio: no deployment recorded for this agent")

// ErrNoPreviousDeployment reports that there is nothing to roll back to — the
// agent has exactly one deployment. Rolling back to nothing would leave the
// agent with no definition at all, which is strictly worse than the version
// the operator is unhappy with.
var ErrNoPreviousDeployment = errors.New("studio: no previous deployment to roll back to")

// DeploymentRecord is one immutable entry in an agent's deployment history.
// Every field answers a question an incident review asks; a record missing any
// of them degrades to a timestamp, which proves nothing.
type DeploymentRecord struct {
	AgentID string `json:"agent_id"`
	// Version is monotonic per agent, starting at 1. Assigned by the store, not
	// by the caller, so two concurrent deploys cannot both claim the same number.
	Version    int       `json:"version"`
	DeployedAt time.Time `json:"deployed_at"`
	DeployedBy string    `json:"deployed_by,omitempty"`
	// WorkflowHash is the content hash of the deployed definition. Two records
	// with the same hash deployed the same bytes, regardless of what the
	// version string claims.
	WorkflowHash string `json:"workflow_hash,omitempty"`
	// RulesVersion pins the SOUL authoring rulebook in force at deploy time.
	// Rules are editable, so "the agent still validates" is meaningless without
	// knowing which rulebook it was built and validated against.
	RulesVersion string `json:"rules_version,omitempty"`
	// ProviderConfig records the model/provider per role — NAMES ONLY, never
	// credentials. See RedactProviderConfig.
	ProviderConfig map[string]string `json:"provider_config,omitempty"`
	// Certification is the test evidence: which requirements were checked, the
	// real (non-dry) run that proved it, and the verdict. nil means the
	// deployment carries no evidence at all, which the readiness gate treats as
	// blocking for scheduled agents.
	Certification *CertificationRecord `json:"certification,omitempty"`
	// Definition is the full deployed definition, stored verbatim so a rollback
	// can restore it without depending on any other store still holding it.
	Definition json.RawMessage `json:"definition,omitempty"`
	Note       string          `json:"note,omitempty"`

	// RolledBackFrom / RolledBackTo are set only on a record produced by
	// Rollback: the version that was live, and the version whose content this
	// record re-applies. They exist so history reads as a narrative rather than
	// as an unexplained duplicate of an older definition.
	RolledBackFrom int `json:"rolled_back_from,omitempty"`
	RolledBackTo   int `json:"rolled_back_to,omitempty"`
}

// IsRollback reports whether this record was produced by Rollback.
func (r DeploymentRecord) IsRollback() bool { return r.RolledBackTo > 0 }

// AgentDefinition decodes the deployed definition. Used by a rollback caller to
// re-upsert the restored agent: the store records what to restore, applying it
// to the live loader stays the caller's separate, explicit act.
func (r DeploymentRecord) AgentDefinition() (agent.Definition, error) {
	var def agent.Definition
	if len(r.Definition) == 0 {
		return def, fmt.Errorf("studio: deployment %s v%d carries no definition", r.AgentID, r.Version)
	}
	if err := json.Unmarshal(r.Definition, &def); err != nil {
		return def, fmt.Errorf("studio: decode deployed definition %s v%d: %w", r.AgentID, r.Version, err)
	}
	return def, nil
}

// Certified reports whether this deployment carries passing test evidence.
func (r DeploymentRecord) Certified() bool {
	return r.Certification != nil && r.Certification.Certified
}

// ── building a record ───────────────────────────────────────────────────────

// NewDeploymentRecord assembles a record from what a save/deploy path holds:
// the definition it is about to persist, the rulebook in force, and the
// certification verdict for it. Version and DeployedAt are filled by the store.
//
// The definition is marshalled ONCE here, and the same bytes are both stored
// and hashed, so WorkflowHash can never disagree with the definition it labels.
func NewDeploymentRecord(def agent.Definition, rules string, cert *CertificationRecord, deployedBy, note string) (DeploymentRecord, error) {
	if strings.TrimSpace(def.ID) == "" {
		return DeploymentRecord{}, fmt.Errorf("studio: deployment record requires an agent id")
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return DeploymentRecord{}, fmt.Errorf("studio: marshal definition for deployment: %w", err)
	}
	return DeploymentRecord{
		AgentID:        def.ID,
		WorkflowHash:   hashBytes(raw, "w"),
		RulesVersion:   RulesVersion(rules),
		ProviderConfig: ProviderConfigFromDefinition(def),
		Certification:  cert,
		Definition:     json.RawMessage(raw),
		DeployedBy:     strings.TrimSpace(deployedBy),
		Note:           strings.TrimSpace(note),
	}, nil
}

// WorkflowHash is the content hash of a definition — the identity of what was
// actually deployed, independent of the human-maintained Version string (which
// people forget to bump).
func WorkflowHash(def agent.Definition) string {
	raw, err := json.Marshal(def)
	if err != nil {
		return ""
	}
	return hashBytes(raw, "w")
}

func hashBytes(b []byte, prefix string) string {
	sum := sha256.Sum256(b)
	return prefix + hex.EncodeToString(sum[:])[:12]
}

// ProviderConfigFromDefinition records the provider/model per role for one
// agent. Names only — see RedactProviderConfig for why.
func ProviderConfigFromDefinition(def agent.Definition) map[string]string {
	return ProviderConfigFromDefinitions(def, nil)
}

// ProviderConfigFromDefinitions records the provider/model for the deployed
// agent plus every peer agent it delegates to, because "which model ran this"
// is not answerable from the top-level agent alone once a workflow contains
// kind=agent nodes. peers may be nil.
func ProviderConfigFromDefinitions(def agent.Definition, peers map[string]agent.Definition) map[string]string {
	cfg := map[string]string{}
	put := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			cfg[k] = v
		}
	}
	put("llm.provider", def.LLM.Provider)
	put("llm.model", def.LLM.Model)
	// base_url is an endpoint, not a credential — but self-hosted endpoints are
	// routinely pasted with userinfo or a token query string, so it is stripped
	// to scheme://host/path before it is recorded.
	put("llm.base_url", sanitizeEndpoint(def.LLM.BaseURL))
	put("llm.reasoning_effort", def.LLM.ReasoningEffort)
	put("reasoning.strategy", def.Reasoning.Strategy)

	for id, peer := range peers {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		put("agent."+id+".provider", peer.LLM.Provider)
		put("agent."+id+".model", peer.LLM.Model)
	}
	return RedactProviderConfig(cfg)
}

// sanitizeEndpoint reduces a URL to scheme://host/path, dropping userinfo,
// query and fragment. A base_url like https://user:token@host/v1?key=… would
// otherwise write a live credential into an audit artefact.
func sanitizeEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// Unparseable: keep it only if it cannot plausibly carry a credential.
		if looksLikeSecretValue(raw) || strings.ContainsAny(raw, "@?#") {
			return redactedValue
		}
		return raw
	}
	clean := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	return clean.String()
}

// redactedValue is what replaces a dropped value. A marker rather than a
// deletion, so an operator reading the record can tell the difference between
// "this role had no provider configured" and "this value was withheld".
const redactedValue = "[redacted]"

// secretKeyHints are substrings that make a config KEY a credential carrier.
// Matching on the key is the cheap, reliable half of the check; the value
// heuristic below is the backstop for keys nobody anticipated.
var secretKeyHints = []string{
	"key", "token", "secret", "password", "passwd", "pwd", "passphrase",
	"credential", "cred", "auth", "bearer", "cookie", "session",
	"signature", "salt", "private", "sid",
}

// secretValuePrefixes are the issued-token shapes that identify themselves.
var secretValuePrefixes = []string{
	"sk-", "sk_", "pk_", "rk_", "ghp_", "gho_", "ghs_", "ghu_", "github_pat_",
	"xoxb-", "xoxp-", "xoxa-", "xapp-", "AKIA", "ASIA", "AIza", "ya29.",
	"hf_", "glpat-", "shpat_", "eyJ", "-----BEGIN",
}

// RedactProviderConfig returns a copy of cfg with every credential-looking
// entry replaced by "[redacted]".
//
// The failure mode this prevents: a deployment record is a durable, exportable
// audit artefact — it ends up in support bundles and incident tickets. A secret
// written here is a secret leaked, and unlike a log line it cannot be rotated
// away by trimming retention, because the record is meant to be kept forever.
// So redaction happens on the way in; nothing unredacted is ever persisted.
func RedactProviderConfig(cfg map[string]string) map[string]string {
	if cfg == nil {
		return nil
	}
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if looksLikeSecretKey(k) || looksLikeSecretValue(v) {
			out[k] = redactedValue
			continue
		}
		out[k] = v
	}
	return out
}

func looksLikeSecretKey(key string) bool {
	k := strings.ToLower(key)
	for _, hint := range secretKeyHints {
		if strings.Contains(k, hint) {
			return true
		}
	}
	return false
}

// looksLikeSecretValue is deliberately conservative about what it does NOT
// flag: model identifiers such as "claude-3-5-sonnet-20241022" are long,
// hyphenated and digit-bearing, and flagging them would redact the very field
// the record exists to capture. So opaque-token detection requires either a
// known issued-token prefix, a mixed-case high-entropy run, or a long hex run —
// none of which a lowercase model name produces.
func looksLikeSecretValue(v string) bool {
	s := strings.TrimSpace(v)
	if s == "" {
		return false
	}
	for _, p := range secretValuePrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	// A URL carrying userinfo is a credential no matter what the key says.
	if u, err := url.Parse(s); err == nil && u.User != nil {
		return true
	}
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '"' || r == '\''
	}) {
		if highEntropyToken(tok) {
			return true
		}
	}
	return false
}

// highEntropyToken reports whether tok looks machine-generated rather than
// human-chosen: a long mixed-case alphanumeric run, or a long pure-hex run.
func highEntropyToken(tok string) bool {
	run := 0
	upper, lower, digit, hexOnly := false, false, false, true
	flush := func() bool {
		defer func() { run, upper, lower, digit, hexOnly = 0, false, false, false, true }()
		if run >= 20 && upper && lower && digit {
			return true
		}
		if run >= 32 && hexOnly && digit {
			return true
		}
		return false
	}
	for _, r := range tok {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
			if r > 'F' {
				hexOnly = false
			}
			run++
		case r >= 'a' && r <= 'z':
			lower = true
			if r > 'f' {
				hexOnly = false
			}
			run++
		case r >= '0' && r <= '9':
			digit = true
			run++
		case r == '+' || r == '/' || r == '=' || r == '_':
			hexOnly = false
			run++
		default:
			// '-' and '.' break the run: they are how human-readable model
			// names are spelled, so treating them as part of one token is what
			// would misclassify "claude-3-5-sonnet-20241022".
			if flush() {
				return true
			}
		}
	}
	return flush()
}

// ── the store ───────────────────────────────────────────────────────────────

// deploymentFileExt is the on-disk extension for one agent's history file.
const deploymentFileExt = ".json"

// deploymentHistory is the on-disk shape: the whole history in one file per
// agent. One file keeps version assignment and append atomic under a single
// rename, which a directory-of-records layout could not guarantee.
type deploymentHistory struct {
	AgentID string             `json:"agent_id"`
	Records []DeploymentRecord `json:"records"`
}

// DeploymentStore is a file-backed, append-only deployment history rooted at a
// caller-supplied directory.
type DeploymentStore struct {
	root string
	// mu serialises read-modify-write cycles so two concurrent deploys cannot
	// both read version N and both write N+1, losing one of them.
	mu sync.Mutex
}

// NewDeploymentStore returns a store rooted at dir. The directory is created
// lazily on the first write, so constructing a store is never an error and a
// read-only deployment never leaves an empty directory behind.
func NewDeploymentStore(dir string) *DeploymentStore {
	return &DeploymentStore{root: strings.TrimSpace(dir)}
}

// DeploymentsDir is the workspace-relative home of deployment history. Kept
// here (next to the store) so every caller — gateway, app wiring, CLI — derives
// the same path instead of each inventing its own.
func DeploymentsDir(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, "studio", "deployments")
}

// Record appends rec to the agent's history and returns the stored copy with
// Version and DeployedAt filled in. The caller's Version is ignored: the store
// owns version assignment, because it is the only place that can see the
// current head under a lock.
func (s *DeploymentStore) Record(rec DeploymentRecord) (DeploymentRecord, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return DeploymentRecord{}, fmt.Errorf("studio: deployments root is required")
	}
	agentID := strings.TrimSpace(rec.AgentID)
	if agentID == "" {
		return DeploymentRecord{}, fmt.Errorf("studio: deployment record requires an agent id")
	}
	rec.AgentID = agentID
	// Belt and braces: a caller that built ProviderConfig by hand still cannot
	// persist a secret through this door.
	rec.ProviderConfig = RedactProviderConfig(rec.ProviderConfig)

	s.mu.Lock()
	defer s.mu.Unlock()

	hist, err := s.loadLocked(agentID)
	if err != nil {
		return DeploymentRecord{}, err
	}
	rec.Version = 1
	if n := len(hist.Records); n > 0 {
		rec.Version = hist.Records[n-1].Version + 1
	}
	if rec.DeployedAt.IsZero() {
		rec.DeployedAt = time.Now().UTC()
	} else {
		rec.DeployedAt = rec.DeployedAt.UTC()
	}
	hist.AgentID = agentID
	hist.Records = append(hist.Records, rec)
	if err := s.saveLocked(agentID, hist); err != nil {
		return DeploymentRecord{}, err
	}
	return rec, nil
}

// History returns every recorded deployment for agentID, oldest first. An agent
// that was never deployed returns an empty slice and no error — "not a Studio
// deployment" is a normal state, not a failure.
func (s *DeploymentStore) History(agentID string) ([]DeploymentRecord, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return nil, fmt.Errorf("studio: deployments root is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	hist, err := s.loadLocked(strings.TrimSpace(agentID))
	if err != nil {
		return nil, err
	}
	return hist.Records, nil
}

// Latest returns the currently deployed version. ErrNoDeployment when the agent
// has never been deployed.
func (s *DeploymentStore) Latest(agentID string) (DeploymentRecord, error) {
	recs, err := s.History(agentID)
	if err != nil {
		return DeploymentRecord{}, err
	}
	if len(recs) == 0 {
		return DeploymentRecord{}, ErrNoDeployment
	}
	return recs[len(recs)-1], nil
}

// Previous returns the deployment immediately before the current one — the
// thing Rollback restores. ErrNoPreviousDeployment when there is only one.
func (s *DeploymentStore) Previous(agentID string) (DeploymentRecord, error) {
	recs, err := s.History(agentID)
	if err != nil {
		return DeploymentRecord{}, err
	}
	switch len(recs) {
	case 0:
		return DeploymentRecord{}, ErrNoDeployment
	case 1:
		return DeploymentRecord{}, ErrNoPreviousDeployment
	}
	return recs[len(recs)-2], nil
}

// Rollback re-applies the previous deployment AS A NEW VERSION and returns it.
// The caller is responsible for putting the returned Definition back into the
// live agent loader — the store records intent and history; applying it stays an
// explicit act, exactly as Certify never enables anything by itself.
//
// History is never rewritten. Deleting the bad version would erase the evidence
// an incident review needs, and would make the same rollback ambiguous if it
// happened twice.
func (s *DeploymentStore) Rollback(agentID, deployedBy string) (DeploymentRecord, error) {
	prev, err := s.Previous(agentID)
	if err != nil {
		return DeploymentRecord{}, err
	}
	current, err := s.Latest(agentID)
	if err != nil {
		return DeploymentRecord{}, err
	}
	restored := DeploymentRecord{
		AgentID:      prev.AgentID,
		DeployedBy:   strings.TrimSpace(deployedBy),
		WorkflowHash: prev.WorkflowHash,
		RulesVersion: prev.RulesVersion,
		// The certification travels with the content it certified: rolling back
		// to a certified version restores a certified deployment, and rolling
		// back to an uncertified one honestly restores an uncertified state
		// rather than laundering it through the rollback.
		ProviderConfig: prev.ProviderConfig,
		Certification:  prev.Certification,
		Definition:     prev.Definition,
		Note: fmt.Sprintf("rollback from v%d to v%d",
			current.Version, prev.Version),
		RolledBackFrom: current.Version,
		RolledBackTo:   prev.Version,
	}
	return s.Record(restored)
}

// ── readiness (consumed by the scheduler gate) ──────────────────────────────

// ScheduleReadiness is the deployment-record view of whether an agent may run
// on a schedule. It is deliberately free of any scheduler type so this package
// stays independent of the scheduler; the adapter lives at the wiring site.
type ScheduleReadiness struct {
	// Deployed reports that a deployment record exists at all. False means this
	// agent was not deployed through Studio, and the gate must have NO opinion —
	// blocking every hand-written YAML agent would be a catastrophic regression.
	Deployed bool
	// Version is the deployment version consulted, for the event payload.
	Version int
	// Blocked reports that scheduled execution must not proceed.
	Blocked bool
	// Summary is the one-line reason, suitable for a log or a status row.
	Summary string
	// Failed lists the unmet requirements, each carrying its own repair action.
	Failed []CertRequirement
}

// ScheduleReadiness reads the current deployment for agentID and reports
// whether it may run on a schedule. It reads from disk on every call — no
// caching — precisely so that an operator who fixes the blocker and re-certifies
// is unblocked on the NEXT tick, without restarting the gateway.
//
// Failure modes are resolved deliberately:
//   - never deployed        → Deployed=false, no opinion (non-Studio agent)
//   - history unreadable    → Blocked, because we cannot prove certification and
//     failing open here would defeat the entire gate
//   - deployed, no evidence → Blocked for scheduled agents, with a runnable fix
func (s *DeploymentStore) ScheduleReadiness(agentID string) ScheduleReadiness {
	rec, err := s.Latest(agentID)
	switch {
	case errors.Is(err, ErrNoDeployment):
		return ScheduleReadiness{}
	case err != nil:
		return ScheduleReadiness{
			Deployed: true,
			Blocked:  true,
			Summary:  "deployment history could not be read: " + err.Error(),
			Failed: []CertRequirement{{
				ID: "deployment_readable", Title: "Deployment record is readable",
				Detail: err.Error(),
				Fix:    "Repair or remove the agent's deployment history file, then re-deploy from Studio.",
				Action: "open_studio",
			}},
		}
	}
	out := ScheduleReadiness{Deployed: true, Version: rec.Version}
	if rec.Certification == nil {
		out.Blocked = true
		out.Summary = "this deployment carries no test evidence"
		out.Failed = []CertRequirement{{
			ID: "certified", Title: "Deployment is certified",
			Detail: "no certification was recorded for the deployed version",
			Fix:    "Run this agent live once and certify it before enabling its schedule.",
			Action: "run_live",
		}}
		return out
	}
	out.Blocked = rec.Certification.BlocksScheduling()
	out.Summary = rec.Certification.Summary()
	out.Failed = rec.Certification.FailedRequirements()
	return out
}

// ── on-disk plumbing ────────────────────────────────────────────────────────

func (s *DeploymentStore) loadLocked(agentID string) (deploymentHistory, error) {
	hist := deploymentHistory{AgentID: agentID}
	path, err := s.pathFor(agentID)
	if err != nil {
		return hist, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return hist, nil
		}
		return hist, fmt.Errorf("studio: read deployment history: %w", err)
	}
	if err := json.Unmarshal(data, &hist); err != nil {
		// Do NOT silently start a fresh history here. A corrupt file that reads
		// as "never deployed" would hand an uncertified agent a clean bill of
		// health and let its schedule fire.
		return deploymentHistory{}, fmt.Errorf("studio: parse deployment history for %q: %w", agentID, err)
	}
	if hist.AgentID == "" {
		hist.AgentID = agentID
	}
	return hist, nil
}

func (s *DeploymentStore) saveLocked(agentID string, hist deploymentHistory) error {
	path, err := s.pathFor(agentID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("studio: create deployments dir: %w", err)
	}
	data, err := json.MarshalIndent(hist, "", "  ")
	if err != nil {
		return fmt.Errorf("studio: marshal deployment history: %w", err)
	}
	// Atomic-ish write: temp file in the same dir, then rename over the target,
	// so a crash mid-write cannot truncate an agent's entire deployment history.
	tmp, err := os.CreateTemp(s.root, "deploy-*.tmp")
	if err != nil {
		return fmt.Errorf("studio: temp deployment history: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("studio: write deployment history: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("studio: close deployment history: %w", err)
	}
	// 0600: the record embeds the full definition, including system prompts.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("studio: chmod deployment history: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("studio: persist deployment history: %w", err)
	}
	return nil
}

// pathFor is the single chokepoint guarding every filesystem touch against
// traversal via a hostile agent id.
func (s *DeploymentStore) pathFor(agentID string) (string, error) {
	if strings.TrimSpace(s.root) == "" {
		return "", fmt.Errorf("studio: deployments root is required")
	}
	if !validDraftID(agentID) {
		return "", fmt.Errorf("studio: invalid agent id %q for deployment history", agentID)
	}
	return filepath.Join(s.root, agentID+deploymentFileExt), nil
}
