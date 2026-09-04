package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/agentvalidate"
	"github.com/soulacy/soulacy/internal/pkgregistry"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/secrets"
	"github.com/soulacy/soulacy/pkg/agent"
)

// regexpMustCompilePackage panics on an invalid regex — used only for the
// package-level `var` initializers below. Wraps regexp.MustCompile so a
// future rewrite (e.g. lazy init) has a single seam.
func regexpMustCompilePackage(expr string) *regexp.Regexp {
	return regexp.MustCompile(expr)
}

type agentPackageFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Text   string `json:"text,omitempty"`
}

type agentPackageSecret struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type agentPackageManifest struct {
	AgentID       string               `json:"agent_id"`
	Name          string               `json:"name"`
	Version       string               `json:"version,omitempty"`
	Description   string               `json:"description,omitempty"`
	Trigger       string               `json:"trigger"`
	Surfaces      []string             `json:"surfaces,omitempty"`
	Providers     []string             `json:"providers,omitempty"`
	Models        []string             `json:"models,omitempty"`
	Channels      []string             `json:"channels,omitempty"`
	Skills        []string             `json:"skills,omitempty"`
	Knowledge     []string             `json:"knowledge,omitempty"`
	PeerAgents    []string             `json:"peer_agents,omitempty"`
	Builtins      []string             `json:"builtins,omitempty"`
	MCPServers    []string             `json:"mcp_servers,omitempty"`
	MCPTools      []string             `json:"mcp_tools,omitempty"`
	Env           []string             `json:"env,omitempty"`
	Capabilities  []string             `json:"capabilities,omitempty"`
	Secrets       []agentPackageSecret `json:"secrets,omitempty"`
	FilesRequired []string             `json:"files_required,omitempty"`
	EvalSuites    []string             `json:"eval_suites,omitempty"`
	SamplePrompts []string             `json:"sample_prompts,omitempty"`
	Warnings      []string             `json:"warnings,omitempty"`

	// ── v2 additions (Story 7 Bucket 7A) ──────────────────────────────────
	// PackageID is the namespaced package identifier `<publisher>/<package>`.
	// Required in schema v2. Distinct from AgentID so a publisher can rename
	// what they ship without changing the deployed agent's own id.
	PackageID string `json:"package_id,omitempty"`
	// PriorVersion forms a linked list so the changelog surface can compute
	// "since your last install" without walking the whole history. Optional.
	PriorVersion string `json:"prior_version,omitempty"`
	// ReleasedAt is when the publisher cut this version. RFC3339 UTC.
	ReleasedAt string `json:"released_at,omitempty"`
	// Changelog is a short plain-text summary shown at import time.
	Changelog string `json:"changelog,omitempty"`
	// Publisher is the identity that signed and cut this version. See §4.5.
	Publisher *agentPackagePublisher `json:"publisher,omitempty"`
	// Requires is the hard-requirements block enforced at import time. See
	// docs/PACKAGE_VERSIONING_DESIGN.md §4.2.
	Requires *agentPackageRequires `json:"requires,omitempty"`
}

// agentPackagePublisher captures who signed a v2 package.
type agentPackagePublisher struct {
	ID           string `json:"id"`
	DisplayName  string `json:"display_name,omitempty"`
	SignatureKey string `json:"signature_key,omitempty"` // "ed25519:HEX-OR-B64"
	TrustLevel   string `json:"trust_level,omitempty"`   // "official" | "community"
}

// agentPackageRequires bundles the install-time gate declared by a v2 package.
type agentPackageRequires struct {
	SoulacyVersion string                      `json:"soulacy_version,omitempty"`
	Providers      []agentPackageRequireItem   `json:"providers,omitempty"`
	Channels       []agentPackageRequireItem   `json:"channels,omitempty"`
	Secrets        []agentPackageRequireSecret `json:"secrets,omitempty"`
	MCPServers     []agentPackageRequireItem   `json:"mcp_servers,omitempty"`
	PeerAgents     []agentPackageRequireItem   `json:"peer_agents,omitempty"`
}

type agentPackageRequireItem struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

type agentPackageRequireSecret struct {
	Key      string `json:"key"` // e.g. "telegram.bot_token"
	Label    string `json:"label,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Kind     string `json:"kind,omitempty"` // "provider_secret" | "channel_secret" | "env" | "other"
	Provider string `json:"provider,omitempty"`
}

type agentPackageIntegrity struct {
	Algorithm string `json:"algorithm,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Signature string `json:"signature,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	Verified  bool   `json:"verified,omitempty"`
}

type agentPackageResponse struct {
	SchemaVersion string                `json:"schema_version"`
	ExportedAt    time.Time             `json:"exported_at"`
	Manifest      agentPackageManifest  `json:"manifest"`
	SOULYAML      string                `json:"soul_yaml"`
	Files         []agentPackageFile    `json:"files,omitempty"`
	Integrity     agentPackageIntegrity `json:"integrity,omitempty"`
}

type agentPackageRequirement struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
}

type agentPackageInspectResponse struct {
	SchemaVersion string                    `json:"schema_version"`
	Manifest      agentPackageManifest      `json:"manifest"`
	Agent         agent.Definition          `json:"agent"`
	Validation    agentvalidate.Report      `json:"validation"`
	Requirements  []agentPackageRequirement `json:"requirements"`
	Files         []agentPackageFile        `json:"files,omitempty"`
	Integrity     agentPackageIntegrity     `json:"integrity,omitempty"`
	Warnings      []string                  `json:"warnings,omitempty"`
	Importable    bool                      `json:"importable"`
}

type agentPackageImportRequest struct {
	Package    agentPackageResponse `json:"package"`
	IDOverride string               `json:"id_override,omitempty"`
	Overwrite  bool                 `json:"overwrite,omitempty"`
	Disabled   bool                 `json:"disabled,omitempty"`
	// AcknowledgeMissing lets the operator proceed with an import that has
	// one or more "missing" requirements (secrets, providers, channels,
	// peer agents). Without this flag a v2 import with any missing entry
	// is refused with 409 so the GUI can render the requirements list and
	// prompt for confirmation. v1 packages are import-flow-unchanged.
	AcknowledgeMissing bool `json:"acknowledge_missing,omitempty"`
}

// PackageSchemaV1 is the legacy schema-version tag; PackageSchemaV2 is the
// current one with required calendar version + Requires block. Kept as
// exported constants so tests, `sy package validate`, and the export path
// share the same string.
const (
	PackageSchemaV1 = "soulacy.agent.package/v1"
	PackageSchemaV2 = "soulacy.agent.package/v2"

	// PackageV1CutoffDate is when v1 imports flip from warn → error. Change
	// this in ONE place if the deprecation window shifts. See
	// docs/PACKAGE_VERSIONING_DESIGN.md §5.1.
	PackageV1CutoffDate = "2027-06-01"
)

// calendarVersionRE matches Soulacy's calendar-versioning format YYYY.MM.DD
// with an optional .PATCH suffix. See docs/PACKAGE_VERSIONING_DESIGN.md §4.1.
var calendarVersionRE = regexpMustCompilePackage(`^(\d{4})\.(\d{2})\.(\d{2})(?:\.(\d+))?$`)

// packageNamespaceRE matches the two-segment namespaced package_id form
// `<publisher>/<package>`; publisher is 2-32 lowercase alnum+hyphen chars,
// package is 1-63 lowercase alnum+hyphen chars starting with alnum. See
// docs/PACKAGE_VERSIONING_DESIGN.md §4.5.
var packageNamespaceRE = regexpMustCompilePackage(`^([a-z0-9-]{2,32})/([a-z0-9][a-z0-9-]{0,62})$`)

// validateCalendarVersion returns nil when the string matches the calendar
// versioning regex AND parses into a real-looking date. Same-day .PATCH
// suffixes are accepted without upper bound.
func validateCalendarVersion(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("version is required")
	}
	m := calendarVersionRE.FindStringSubmatch(v)
	if m == nil {
		return fmt.Errorf("version %q does not match YYYY.MM.DD[.PATCH]", v)
	}
	// Basic sanity: month 01-12, day 01-31; we don't reject Feb 30 here
	// because publishers may date-shift releases, but we do refuse
	// obviously-broken values.
	// time.Parse gives us both the boundary check and a natural way to
	// spot future-dated releases.
	if _, err := time.Parse("2006.01.02", m[1]+"."+m[2]+"."+m[3]); err != nil {
		return fmt.Errorf("version %q has an invalid date component: %w", v, err)
	}
	return nil
}

// validatePackageNamespace enforces the `<publisher>/<package>` shape from
// §4.5. Empty is rejected — v2 requires it. Reserved namespaces (currently
// just "soulacy") pass through the regex; enforcement of who can publish
// under a reserved namespace happens at the registry (in Bucket 7B).
func validatePackageNamespace(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("package_id is required for schema v2")
	}
	if !packageNamespaceRE.MatchString(id) {
		return fmt.Errorf("package_id %q must be `<publisher>/<package>` with lowercase alnum+hyphen segments", id)
	}
	return nil
}

func (s *Server) handleGetAgentPackage(c *fiber.Ctx) error {
	id := c.Params("id")
	def := s.loader.Get(id)
	if def == nil {
		return s.errMsg(c, fiber.StatusNotFound, "agent not found")
	}

	pkg, err := buildAgentPackage(def)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	c.Set(fiber.HeaderContentType, "application/json")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="`+safeDownloadName(def.ID)+`.soulacy-agent.json"`)
	return c.JSON(pkg)
}

func (s *Server) handleInspectAgentPackage(c *fiber.Ctx) error {
	pkg, err := parseAgentPackageBody(c.Body())
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	inspected, err := s.inspectAgentPackage(pkg)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(inspected)
}

func (s *Server) handleImportAgentPackage(c *fiber.Ctx) error {
	var req agentPackageImportRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	pkg := &req.Package
	if pkg.SchemaVersion == "" && strings.TrimSpace(pkg.SOULYAML) == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "package is required")
	}
	if req.IDOverride != "" {
		var def agent.Definition
		if err := yaml.Unmarshal([]byte(pkg.SOULYAML), &def); err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
		def.ID = strings.TrimSpace(req.IDOverride)
		raw, err := yaml.Marshal(&def)
		if err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
		pkg = &agentPackageResponse{
			SchemaVersion: pkg.SchemaVersion,
			ExportedAt:    pkg.ExportedAt,
			Manifest:      pkg.Manifest,
			SOULYAML:      string(raw),
			Files:         pkg.Files,
		}
		pkg.Manifest.AgentID = def.ID
		pkg.Manifest.Name = def.Name
		sum, err := packageContentChecksum(pkg)
		if err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
		pkg.Integrity = agentPackageIntegrity{Algorithm: "sha256", SHA256: sum}
	}
	inspected, err := s.inspectAgentPackage(pkg)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if !inspected.Validation.Valid {
		return c.Status(fiber.StatusBadRequest).JSON(inspected)
	}
	if isProtectedSystemAgent(inspected.Agent.ID) {
		return protectedSystemAgentResponse(c)
	}
	// Story 7 Bucket 7A — v1 cutoff. Past PackageV1CutoffDate, v1 imports
	// are refused unconditionally; before then, they only warn (the warning
	// is already appended to inspected.Warnings by inspectAgentPackage).
	if pkg.SchemaVersion == PackageSchemaV1 && time.Now().UTC().After(packageV1CutoffTime()) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error":        "v1 packages are refused after " + PackageV1CutoffDate + " — ask the publisher to re-publish as v2",
			"requirements": inspected.Requirements,
			"warnings":     inspected.Warnings,
		})
	}
	// Story 7 Bucket 7A — install-time secret + provider + channel + peer
	// gate. When the v2 manifest declares a `requires` block, `missing`
	// entries block import unless the operator explicitly acknowledged them.
	if pkg.SchemaVersion == PackageSchemaV2 && !req.AcknowledgeMissing {
		if missing := collectMissingRequirements(inspected.Requirements); len(missing) > 0 {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{
				"error":                 "package has missing requirements; set acknowledge_missing:true to import anyway",
				"missing":               missing,
				"requirements":          inspected.Requirements,
				"warnings":              inspected.Warnings,
				"needs_acknowledgement": true,
			})
		}
	}
	if s.loader.Get(inspected.Agent.ID) != nil && !req.Overwrite {
		return s.errMsg(c, fiber.StatusConflict, "agent already exists; set overwrite=true or choose a different id")
	}

	def := inspected.Agent.Clone()
	if def.LLM.Provider == "" {
		def.LLM.Provider = s.cfg.LLM.DefaultProvider
	}
	if req.Disabled {
		def.Enabled = false
	} else if !def.Enabled {
		def.Enabled = true
	}

	dir := ""
	if len(s.cfg.AgentDirs) > 0 {
		dir = s.cfg.AgentDirs[0]
	}
	if dir == "" {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "no agent directory configured")
	}
	if err := writePackageFiles(dir, def.ID, inspected.Files); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	// Story 7 Bucket 7A — write the .soulacy-package.json sidecar so the
	// origin, version, publisher, and (if applicable) missing-acknowledgement
	// state are recorded next to the agent. 7C will walk this file to
	// populate the Package tab and to build the rollback list.
	if err := writePackageSidecar(dir, def.ID, pkg, req.AcknowledgeMissing); err != nil {
		// Sidecar write is best-effort — an import that already wrote
		// SOUL.yaml should not fail because the sidecar couldn't be
		// persisted. Log at Warn so ops can spot filesystem issues.
		s.log.Warn("package sidecar write failed", zap.String("agent", def.ID), zap.Error(err))
	}
	if err := s.loader.Upsert(dir, def); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if err := s.scheduler.RegisterAgent(def); err != nil {
		s.log.Warn("scheduler registration failed", zap.String("agent", def.ID), zap.Error(err))
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"agent":        def,
		"requirements": inspected.Requirements,
		"warnings":     inspected.Warnings,
	})
}

// packageV1CutoffTime parses PackageV1CutoffDate at call time so tests can
// override PackageV1CutoffDate. Always UTC.
func packageV1CutoffTime() time.Time {
	t, err := time.Parse("2006-01-02", PackageV1CutoffDate)
	if err != nil {
		// Should never happen — the constant is validated by tests.
		return time.Now().UTC().Add(-1 * time.Hour)
	}
	return t
}

// collectMissingRequirements filters an inspected package's requirement list
// down to the entries that block import in v2 mode. `verify` and `declared`
// are informational; anything not "available"/"configured"/"built_in"/
// "packaged" from a required_* kind is treated as missing.
func collectMissingRequirements(reqs []agentPackageRequirement) []agentPackageRequirement {
	var out []agentPackageRequirement
	for _, r := range reqs {
		if !strings.HasPrefix(r.Kind, "required_") {
			continue
		}
		switch r.Status {
		case "available", "configured", "built_in", "packaged":
			continue
		}
		out = append(out, r)
	}
	return out
}

// packageSidecar is the .soulacy-package.json format stored next to SOUL.yaml
// on import. Story 7 Bucket 7A ships the write side; 7B/7C will add reader
// helpers for the Package tab + rollback picker.
type packageSidecar struct {
	SchemaVersion       string                 `json:"schema_version"`
	PackageID           string                 `json:"package_id,omitempty"`
	InstalledVersion    string                 `json:"installed_version,omitempty"`
	InstalledFrom       string                 `json:"installed_from,omitempty"`
	InstalledAt         time.Time              `json:"installed_at"`
	Publisher           *agentPackagePublisher `json:"publisher,omitempty"`
	SignatureVerified   bool                   `json:"signature_verified,omitempty"`
	TrustLevelAtInstall string                 `json:"trust_level_at_install,omitempty"`
	AcknowledgedMissing bool                   `json:"acknowledged_missing,omitempty"`
}

// writePackageSidecar persists the .soulacy-package.json file next to SOUL.yaml.
// It is safe to call for both v1 and v2 packages — v1 packages just have most
// fields empty, which is honest signal for the operator.
func writePackageSidecar(agentRoot, agentID string, pkg *agentPackageResponse, ackedMissing bool) error {
	if pkg == nil {
		return nil
	}
	base := filepath.Join(agentRoot, agentID)
	if err := os.MkdirAll(base, 0755); err != nil {
		return err
	}
	sc := packageSidecar{
		SchemaVersion:       pkg.SchemaVersion,
		PackageID:           pkg.Manifest.PackageID,
		InstalledVersion:    pkg.Manifest.Version,
		InstalledAt:         time.Now().UTC(),
		SignatureVerified:   pkg.Integrity.Verified,
		AcknowledgedMissing: ackedMissing,
	}
	if pkg.Manifest.Publisher != nil {
		clone := *pkg.Manifest.Publisher
		sc.Publisher = &clone
		sc.TrustLevelAtInstall = pkg.Manifest.Publisher.TrustLevel
	}
	raw, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(base, ".soulacy-package.json"), append(raw, '\n'), 0644)
}

func parseAgentPackageBody(body []byte) (*agentPackageResponse, error) {
	var wrapper struct {
		Package agentPackageResponse `json:"package"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && wrapper.Package.SchemaVersion != "" {
		return &wrapper.Package, nil
	}
	var pkg agentPackageResponse
	if err := json.Unmarshal(body, &pkg); err != nil {
		return nil, err
	}
	return &pkg, nil
}

func (s *Server) inspectAgentPackage(pkg *agentPackageResponse) (*agentPackageInspectResponse, error) {
	if pkg == nil {
		return nil, errors.New("package is required")
	}
	switch pkg.SchemaVersion {
	case PackageSchemaV1, PackageSchemaV2:
		// supported
	default:
		return nil, fmt.Errorf("unsupported package schema %q", pkg.SchemaVersion)
	}
	// Story 7 Bucket 7A: v1 packages are still importable but generate a
	// deprecation warning; after PackageV1CutoffDate the gate flips to a
	// hard error at handleImportAgentPackage.
	var v1Warning string
	if pkg.SchemaVersion == PackageSchemaV1 {
		v1Warning = "package uses deprecated schema \"" + PackageSchemaV1 + "\"; v1 packages will be refused after " + PackageV1CutoffDate + " — ask the publisher to re-publish as v2 (see docs/PACKAGE_VERSIONING_DESIGN.md)"
	}
	// v2-specific structural checks. These are separate from agentvalidate
	// (which validates the embedded SOUL.yaml) because they cover metadata
	// the SOUL.yaml doesn't carry.
	if pkg.SchemaVersion == PackageSchemaV2 {
		if err := validateCalendarVersion(pkg.Manifest.Version); err != nil {
			return nil, fmt.Errorf("v2 manifest: %w", err)
		}
		if err := validatePackageNamespace(pkg.Manifest.PackageID); err != nil {
			return nil, fmt.Errorf("v2 manifest: %w", err)
		}
	}
	var def agent.Definition
	if err := yaml.Unmarshal([]byte(pkg.SOULYAML), &def); err != nil {
		return nil, fmt.Errorf("SOUL.yaml parse failed: %w", err)
	}
	report := agentvalidate.Bytes([]byte(pkg.SOULYAML), "package:SOUL.yaml", s.agentValidationOptions(context.TODO()))
	requirements := s.agentPackageRequirements(pkg, &def)
	warnings := append([]string(nil), pkg.Manifest.Warnings...)
	if v1Warning != "" {
		warnings = append(warnings, v1Warning)
	}
	integrity := pkg.Integrity
	if integrity.SHA256 != "" {
		actual, err := packageContentChecksum(pkg)
		if err != nil {
			return nil, fmt.Errorf("package integrity checksum failed: %w", err)
		}
		if !strings.EqualFold(actual, integrity.SHA256) {
			return nil, fmt.Errorf("package integrity mismatch: expected %s, got %s", integrity.SHA256, actual)
		}
		if integrity.Signature != "" {
			if integrity.PublicKey == "" {
				return nil, errors.New("package signature is present but integrity.public_key is missing")
			}
			pub, err := packageSigningKey(integrity.PublicKey)
			if err != nil {
				return nil, err
			}
			if err := pkgregistry.VerifySignature(pub, integrity.SHA256, integrity.Signature); err != nil {
				return nil, err
			}
			integrity.Verified = true
		}
	}
	for _, file := range pkg.Files {
		if strings.TrimSpace(file.Path) == "" {
			warnings = append(warnings, "package contains a file with an empty path")
		}
		if strings.Contains(file.Path, "..") || filepath.IsAbs(file.Path) {
			warnings = append(warnings, "package file path is unsafe and will not be imported: "+file.Path)
		}
	}
	return &agentPackageInspectResponse{
		SchemaVersion: pkg.SchemaVersion,
		Manifest:      pkg.Manifest,
		Agent:         def,
		Validation:    report,
		Requirements:  requirements,
		Files:         pkg.Files,
		Integrity:     integrity,
		Warnings:      sortedUnique(warnings),
		Importable:    report.Valid,
	}, nil
}

func (s *Server) agentPackageRequirements(pkg *agentPackageResponse, def *agent.Definition) []agentPackageRequirement {
	var reqs []agentPackageRequirement
	addReq := func(kind, name, status, desc string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		reqs = append(reqs, agentPackageRequirement{Kind: kind, Name: name, Status: status, Description: desc})
	}
	for _, provider := range sortedUnique(pkg.Manifest.Providers) {
		status := "missing"
		if s.cfg != nil {
			if _, ok := s.cfg.LLM.Providers[provider]; ok {
				status = "configured"
			}
		}
		if s.llmRouter != nil && s.llmRouter.Provider(provider) != nil {
			status = "available"
		}
		addReq("provider", provider, status, "LLM provider used by this agent.")
	}
	for _, ch := range sortedUnique(pkg.Manifest.Channels) {
		status := "missing"
		if ch == "http" {
			status = "built_in"
		} else if s.channels != nil {
			if statuses := s.channels.Statuses(); statuses[ch].Connected || statuses[ch].Detail != "" {
				status = "available"
			}
		} else if s.cfg != nil {
			if _, ok := s.cfg.Channels[ch]; ok {
				status = "configured"
			}
		}
		addReq("channel", ch, status, "Channel adapter or bot mapping used by this agent.")
	}
	for _, skill := range sortedUnique(pkg.Manifest.Skills) {
		status := "declared"
		if hasSkill(s.skillLoader, skill) {
			status = "available"
		}
		addReq("skill", skill, status, "Install this skill if the agent depends on its instructions.")
	}
	for _, kb := range sortedUnique(pkg.Manifest.Knowledge) {
		addReq("knowledge", kb, "verify", "Create or map this knowledge base on the target runtime.")
	}
	for _, peer := range sortedUnique(pkg.Manifest.PeerAgents) {
		status := "missing"
		if s.loader.Get(peer) != nil {
			status = "available"
		}
		addReq("peer_agent", peer, status, "Import or create this peer agent if the package invokes it.")
	}
	// Legacy secret list — informational for v1 packages; unchanged.
	for _, secret := range pkg.Manifest.Secrets {
		addReq("secret", secret.Name, "verify", secret.Description)
	}
	// Story 7 Bucket 7A — v2 hard-requirements block. When the manifest
	// declares `requires.secrets`, `requires.providers`, etc. we look up
	// live state (vault, provider registry, channel registry, loader)
	// and mark each requirement with a concrete status. `handleImport…`
	// refuses import when anything is `missing` unless the request
	// carries `acknowledge_missing:true`.
	if req := pkg.Manifest.Requires; req != nil {
		vaultKeys := s.credentialVaultKeys()
		for _, sec := range req.Secrets {
			status := "missing"
			key := strings.TrimSpace(sec.Key)
			if key != "" && vaultKeys[key] {
				status = "available"
			}
			label := sec.Label
			if strings.TrimSpace(label) == "" {
				label = key
			}
			desc := sec.Reason
			if strings.TrimSpace(desc) == "" {
				desc = "Required secret declared by the package manifest."
			}
			addReq("required_secret", label, status, desc)
		}
		for _, p := range req.Providers {
			status := "missing"
			if s.cfg != nil {
				if _, ok := s.cfg.LLM.Providers[p.ID]; ok {
					status = "configured"
				}
			}
			if s.llmRouter != nil && s.llmRouter.Provider(p.ID) != nil {
				status = "available"
			}
			addReq("required_provider", p.ID, status, p.Reason)
		}
		for _, ch := range req.Channels {
			status := "missing"
			if ch.ID == "http" {
				status = "built_in"
			} else if s.channels != nil {
				if statuses := s.channels.Statuses(); statuses[ch.ID].Connected || statuses[ch.ID].Detail != "" {
					status = "available"
				}
			} else if s.cfg != nil {
				if _, ok := s.cfg.Channels[ch.ID]; ok {
					status = "configured"
				}
			}
			addReq("required_channel", ch.ID, status, ch.Reason)
		}
		for _, m := range req.MCPServers {
			// We don't have a live "is MCP server connected" oracle here
			// without preflight input; mark declared for now. Bucket 7B
			// wires the connected-set through so this becomes concrete.
			addReq("required_mcp_server", m.ID, "declared", m.Reason)
		}
		for _, p := range req.PeerAgents {
			status := "missing"
			if s.loader != nil && s.loader.Get(p.ID) != nil {
				status = "available"
			}
			addReq("required_peer_agent", p.ID, status, p.Reason)
		}
	}
	for _, file := range sortedUnique(append(append([]string{}, pkg.Manifest.FilesRequired...), append(pkg.Manifest.EvalSuites, pkg.Manifest.SamplePrompts...)...)) {
		status := "missing"
		for _, packaged := range pkg.Files {
			if filepath.ToSlash(packaged.Path) == filepath.ToSlash(file) {
				status = "packaged"
				break
			}
		}
		addReq("file", file, status, "Portable file bundled with this package, such as a tool, eval suite, or sample prompt.")
	}
	if def != nil && s.loader.Get(def.ID) != nil {
		addReq("agent_id", def.ID, "conflict", "An agent with this ID already exists.")
	}
	sort.Slice(reqs, func(i, j int) bool {
		if reqs[i].Kind == reqs[j].Kind {
			return reqs[i].Name < reqs[j].Name
		}
		return reqs[i].Kind < reqs[j].Kind
	})
	return reqs
}

// credentialVaultKeys returns the set of secret names currently stored in the
// vault, so v2 `requires.secrets` can be checked without repeatedly calling
// Get. Empty map when the vault is unavailable — every requirement then
// classifies as "missing", which is the correct default.
func (s *Server) credentialVaultKeys() map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	mgr := secrets.New(s.CredentialVault())
	if !mgr.Enabled() {
		return out
	}
	// A short-lived context is enough — vault reads are local.
	names, err := mgr.List(context.TODO())
	if err != nil {
		return out
	}
	for _, n := range names {
		out[n] = true
	}
	return out
}

func hasSkill(loader runtime.SkillLoader, id string) bool {
	if loader == nil || strings.TrimSpace(id) == "" {
		return false
	}
	if loader.Get(id) != nil {
		return true
	}
	for _, skill := range loader.All() {
		if skill.Name == id {
			return true
		}
	}
	return false
}

func writePackageFiles(agentRoot, agentID string, files []agentPackageFile) error {
	base := filepath.Join(agentRoot, agentID)
	for _, file := range files {
		rel := filepath.Clean(filepath.FromSlash(file.Path))
		if rel == "." || rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			continue
		}
		full := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return err
		}
		data := []byte(file.Text)
		if file.SHA256 != "" {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != file.SHA256 {
				return fmt.Errorf("package file %s checksum mismatch", file.Path)
			}
		}
		if err := os.WriteFile(full, data, 0644); err != nil {
			return err
		}
	}
	return nil
}

func buildAgentPackage(def *agent.Definition) (*agentPackageResponse, error) {
	clone := def.Clone()
	manifest := agentPackageManifest{
		AgentID:      clone.ID,
		Name:         clone.Name,
		Version:      clone.Version,
		Description:  clone.Description,
		Trigger:      string(clone.Trigger),
		Surfaces:     sortedUnique(clone.EffectiveSurfaces()),
		Skills:       sortedUnique(clone.Skills),
		Knowledge:    sortedUnique(clone.Knowledge),
		PeerAgents:   sortedUnique(clone.Agents),
		Env:          sortedUnique(clone.Env),
		Capabilities: sortedUnique(agentPackageCapabilities(clone)),
	}

	addString(&manifest.Providers, clone.LLM.Provider)
	addString(&manifest.Models, clone.LLM.Model)
	for _, provider := range clone.LLM.AllowedProviders {
		addString(&manifest.Providers, provider)
	}
	for _, ch := range clone.Channels {
		addString(&manifest.Channels, ch)
	}
	if clone.Schedule != nil && clone.Schedule.Output != nil {
		addString(&manifest.Channels, clone.Schedule.Output.Channel)
	}
	if clone.NotifyOnFailure != nil {
		addString(&manifest.Channels, clone.NotifyOnFailure.Channel)
	}
	if clone.Builtins != nil {
		manifest.Builtins = sortedUnique(*clone.Builtins)
	}
	if clone.MCPServers != nil {
		manifest.MCPServers = sortedUnique(*clone.MCPServers)
	}
	if clone.MCPTools != nil {
		manifest.MCPTools = sortedUnique(*clone.MCPTools)
	}
	manifest.Providers = sortedUnique(manifest.Providers)
	manifest.Models = sortedUnique(manifest.Models)
	manifest.Channels = sortedUnique(manifest.Channels)

	for _, provider := range manifest.Providers {
		manifest.Secrets = append(manifest.Secrets, agentPackageSecret{
			Kind:        "provider_api_key",
			Name:        "llm.providers." + provider + ".api_key",
			Description: "Configure this provider in Soulacy before running the package.",
		})
	}
	for _, ch := range manifest.Channels {
		if ch != "" && ch != "http" {
			manifest.Secrets = append(manifest.Secrets, agentPackageSecret{
				Kind:        "channel_credentials",
				Name:        "channels." + ch,
				Description: "Configure the channel adapter or mapped bot before using this package.",
			})
		}
	}
	if clone.Security != nil && clone.Security.Passphrase != "" {
		manifest.Secrets = append(manifest.Secrets, agentPackageSecret{
			Kind:        "agent_passphrase",
			Name:        "security.passphrase",
			Description: "Set a passphrase after import; the exported package redacts it.",
		})
		clone.Security.Passphrase = ""
	}
	for _, env := range manifest.Env {
		manifest.Secrets = append(manifest.Secrets, agentPackageSecret{
			Kind:        "environment",
			Name:        env,
			Description: "This environment variable must exist in the target runtime.",
		})
	}
	for _, tool := range clone.Tools {
		addString(&manifest.FilesRequired, tool.PythonFile)
	}

	files, warnings := packageToolFiles(clone)
	supportFiles, supportWarnings := packageAgentSupportFiles(clone)
	files = mergePackageFiles(files, supportFiles)
	warnings = append(warnings, supportWarnings...)
	for _, file := range supportFiles {
		switch {
		case strings.HasPrefix(file.Path, "evals/"):
			addString(&manifest.EvalSuites, file.Path)
		case strings.HasPrefix(file.Path, "prompts/"), strings.HasPrefix(file.Path, "samples/"):
			addString(&manifest.SamplePrompts, file.Path)
		}
	}
	manifest.FilesRequired = sortedUnique(manifest.FilesRequired)
	manifest.EvalSuites = sortedUnique(manifest.EvalSuites)
	manifest.SamplePrompts = sortedUnique(manifest.SamplePrompts)
	manifest.Warnings = append(manifest.Warnings, warnings...)

	raw, err := yaml.Marshal(clone)
	if err != nil {
		return nil, err
	}
	pkg := &agentPackageResponse{
		SchemaVersion: "soulacy.agent.package/v1",
		ExportedAt:    time.Now().UTC(),
		Manifest:      manifest,
		SOULYAML:      string(raw),
		Files:         files,
	}
	sum, err := packageContentChecksum(pkg)
	if err != nil {
		return nil, err
	}
	pkg.Integrity = agentPackageIntegrity{Algorithm: "sha256", SHA256: sum}
	return pkg, nil
}

func packageContentChecksum(pkg *agentPackageResponse) (string, error) {
	if pkg == nil {
		return "", errors.New("package is required")
	}
	clone := *pkg
	clone.Integrity = agentPackageIntegrity{}
	data, err := json.Marshal(clone)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func packageSigningKey(hexKey string) ([]byte, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil {
		return nil, fmt.Errorf("package integrity public_key is not hex: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("package integrity public_key must be 32 bytes (got %d)", len(raw))
	}
	return raw, nil
}

func packageToolFiles(def *agent.Definition) ([]agentPackageFile, []string) {
	var files []agentPackageFile
	var warnings []string
	base := filepath.Dir(def.SourcePath)
	if def.SourcePath == "" || base == "." {
		base = ""
	}
	seen := map[string]bool{}
	for _, tool := range def.Tools {
		p := strings.TrimSpace(tool.PythonFile)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if base == "" {
			warnings = append(warnings, "tool "+tool.Name+" references "+p+" but the agent has no source directory")
			continue
		}
		full := filepath.Clean(p)
		if !filepath.IsAbs(full) {
			full = filepath.Join(base, full)
		}
		rel, err := filepath.Rel(base, full)
		if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
			warnings = append(warnings, "tool "+tool.Name+" references a file outside the agent directory: "+p)
			continue
		}
		data, err := readSmallPackageFile(full)
		if err != nil {
			warnings = append(warnings, "tool "+tool.Name+" file could not be packaged: "+p+" ("+err.Error()+")")
			continue
		}
		sum := sha256.Sum256(data)
		files = append(files, agentPackageFile{
			Path:   filepath.ToSlash(rel),
			SHA256: hex.EncodeToString(sum[:]),
			Bytes:  len(data),
			Text:   string(data),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, warnings
}

func packageAgentSupportFiles(def *agent.Definition) ([]agentPackageFile, []string) {
	var files []agentPackageFile
	var warnings []string
	base := filepath.Dir(def.SourcePath)
	if def.SourcePath == "" || base == "." {
		return nil, nil
	}
	specs := []struct {
		dir  string
		exts map[string]bool
	}{
		{dir: "evals", exts: map[string]bool{".yaml": true, ".yml": true, ".json": true}},
		{dir: "prompts", exts: map[string]bool{".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true}},
		{dir: "samples", exts: map[string]bool{".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true}},
	}
	for _, spec := range specs {
		root := filepath.Join(base, spec.dir)
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				if strings.HasPrefix(info.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !spec.exts[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			rel, err := filepath.Rel(base, path)
			if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
				warnings = append(warnings, "support file is outside the agent directory: "+path)
				return nil
			}
			data, err := readSmallPackageFile(path)
			if err != nil {
				warnings = append(warnings, "support file could not be packaged: "+filepath.ToSlash(rel)+" ("+err.Error()+")")
				return nil
			}
			sum := sha256.Sum256(data)
			files = append(files, agentPackageFile{
				Path:   filepath.ToSlash(rel),
				SHA256: hex.EncodeToString(sum[:]),
				Bytes:  len(data),
				Text:   string(data),
			})
			return nil
		})
		if err != nil {
			warnings = append(warnings, "support directory could not be scanned: "+spec.dir+" ("+err.Error()+")")
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, warnings
}

func mergePackageFiles(groups ...[]agentPackageFile) []agentPackageFile {
	seen := map[string]bool{}
	var out []agentPackageFile
	for _, group := range groups {
		for _, file := range group {
			key := filepath.ToSlash(file.Path)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			file.Path = key
			out = append(out, file)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func readSmallPackageFile(path string) ([]byte, error) {
	const maxPackageFileBytes = 512 * 1024
	data, err := osReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxPackageFileBytes {
		return nil, fiber.NewError(fiber.StatusRequestEntityTooLarge, "file exceeds 512 KiB package limit")
	}
	return data, nil
}

var osReadFile = os.ReadFile

func agentPackageCapabilities(def *agent.Definition) []string {
	out := append([]string(nil), def.Capabilities...)
	if def.SystemTools || def.AllowShell {
		out = append(out, "system")
	}
	if def.Unattended {
		out = append(out, "unattended")
	}
	return out
}

func addString(xs *[]string, v string) {
	v = strings.TrimSpace(v)
	if v != "" {
		*xs = append(*xs, v)
	}
}

func sortedUnique(xs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func safeDownloadName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "agent"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "agent"
	}
	return out
}
