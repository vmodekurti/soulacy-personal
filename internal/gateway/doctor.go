package gateway

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/secrets"
	"github.com/soulacy/soulacy/internal/studio"
)

type doctorProviderCheck struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
	Remedy     string `json:"remedy,omitempty"`
	Registered bool   `json:"registered"`
	KeySource  string `json:"key_source"`
	BaseURL    string `json:"base_url,omitempty"`
	Model      string `json:"model,omitempty"`
	// Reachable and ModelPresent are the answers to the only two questions
	// that decide whether this provider can actually serve a request. Before
	// they existed the check asked whether a provider block appeared in
	// config, named a model, and had a key when remote — all of which are
	// satisfied by the config the product writes for itself at first run. So
	// a machine with no model runtime at all reported "Provider ollama is
	// ready", and onboarding, which only redirects when the provider step is
	// todo, never opened the setup wizard for the person who needed it most.
	Reachable    *bool `json:"reachable,omitempty"`
	ModelPresent *bool `json:"model_present,omitempty"`
}

type doctorChannelCheck struct {
	ID          string              `json:"id"`
	Status      string              `json:"status"`
	Detail      string              `json:"detail"`
	Enabled     bool                `json:"enabled"`
	Configured  bool                `json:"configured"`
	Registered  bool                `json:"registered"`
	Connected   bool                `json:"connected"`
	Diagnostics []channelDiagnostic `json:"diagnostics,omitempty"`
}

func (s *Server) handleDoctor(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"providers": s.providerDoctorChecks(c),
		"channels":  s.channelDoctorChecks(),
		"vault":     s.vaultDoctorCheck(c),
		// What this deployment can and cannot do. A platform without shell
		// access cannot follow half the install instructions written for a
		// laptop, and saying so is cheaper than bundling every heavy
		// dependency into the image on the chance somebody needs it.
		"deployment": s.deploymentDoctor(),
	})
}

type doctorVaultCheck struct {
	Status  string `json:"status"`
	Detail  string `json:"detail"`
	Remedy  string `json:"remedy,omitempty"`
	Secrets int    `json:"secrets"`
}

// vaultDoctorCheck verifies the encrypted credential vault is present and
// actually decrypts (sy doctor v2 parity, surfaced in the GUI Doctor).
func (s *Server) vaultDoctorCheck(c *fiber.Ctx) doctorVaultCheck {
	mgr := secrets.New(s.CredentialVault())
	if !mgr.Enabled() {
		return doctorVaultCheck{
			Status: "warn",
			Detail: "no encrypted vault; secrets resolve from config/env only",
			Remedy: "run `sy setup` or save a secret to initialize the vault",
		}
	}
	names, err := mgr.List(c.Context())
	if err != nil {
		return doctorVaultCheck{
			Status: "fail",
			Detail: "vault is present but could not be listed: " + err.Error(),
			Remedy: "the vault may be unreadable with the current master secret",
		}
	}
	// Prove decryption end-to-end on the first stored secret.
	if len(names) > 0 {
		if _, ok := mgr.Get(c.Context(), names[0]); !ok {
			return doctorVaultCheck{
				Status:  "fail",
				Detail:  "vault entries cannot be decrypted with the current master secret",
				Remedy:  "restore the vault master-secret file or re-enter the affected secrets",
				Secrets: len(names),
			}
		}
	}
	return doctorVaultCheck{
		Status:  "ok",
		Detail:  "vault decrypts and is consistent",
		Secrets: len(names),
	}
}

func (s *Server) providerDoctorChecks(c *fiber.Ctx) []doctorProviderCheck {
	registered := map[string]bool{}
	if s.llmRouter != nil {
		for _, id := range s.llmRouter.ProviderIDs() {
			registered[id] = true
		}
	}
	// Source, not Set. Doctor's whole point here is to warn when a key is
	// readable but sitting in plaintext config rather than the vault, so it
	// needs "in the vault" specifically — Set now means "resolves from anywhere",
	// which would report a plaintext key as safely stored and silently drop the
	// migration warning.
	vaultSet := map[string]bool{}
	mgr := secrets.New(s.CredentialVault())
	if mgr.Enabled() {
		for _, d := range mgr.Catalog(c.Context(), s.cfg) {
			if d.Category == secrets.CategoryLLM && d.Source == secrets.SourceVault {
				vaultSet[d.Name] = true
			}
		}
	}

	ids := make([]string, 0, len(s.cfg.LLM.Providers))
	for id := range s.cfg.LLM.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var out []doctorProviderCheck
	for _, id := range ids {
		pc := s.cfg.LLM.Providers[id]
		keyName := "llm.providers." + id + ".api_key"
		envName := strings.ToUpper(id) + "_API_KEY"
		envSet := os.Getenv(envName) != ""
		local := studio.IsLocalProvider(id, pc.BaseURL)
		source := "missing"
		switch {
		case vaultSet[keyName]:
			source = "vault"
		case envSet:
			source = "env:" + envName
		case strings.TrimSpace(pc.APIKey) != "":
			source = "config/runtime"
		case local:
			source = "not required"
		}

		check := doctorProviderCheck{
			ID: id, Registered: registered[id], KeySource: source,
			BaseURL: pc.BaseURL, Model: pc.Model,
			Status: "ok", Detail: "provider is usable",
		}
		if !check.Registered {
			check.Status = "fail"
			check.Detail = "provider is configured but not registered in the live router"
			check.Remedy = "restart the gateway so the provider is registered"
		} else if strings.TrimSpace(pc.Model) == "" {
			check.Status = "warn"
			check.Detail = "provider has no default model"
			check.Remedy = "select and save a default model"
		} else if !local && source == "missing" {
			check.Status = "fail"
			check.Detail = "remote provider has no API key in vault, env, or runtime config"
			check.Remedy = "save the provider API key again; it should appear as vault-backed in Secrets"
		} else if !local && source == "config/runtime" {
			check.Status = "warn"
			check.Detail = "provider key is only present in runtime/config, not confirmed in the encrypted vault"
			check.Remedy = "re-save the API key from Providers or Secrets so it survives restarts"
		} else if !local && source != "vault" {
			check.Status = "warn"
			check.Detail = "provider works through " + source + ", not the encrypted vault"
			check.Remedy = "store the key in Secrets for restart-safe operation"
		}
		s.probeProvider(c, &check)
		out = append(out, check)
	}
	return out
}

// probeLiveProviders can be turned off in tests that do not want outbound
// calls. Production always probes: a readiness report that never leaves the
// process is not a readiness report.
var probeLiveProviders = true

// providerProbeTimeout is short because /doctor is on the onboarding path and
// a slow answer there is its own bad first impression. A provider that cannot
// answer in this long is not going to serve a chat turn either.
const providerProbeTimeout = 3 * time.Second

// probeProvider asks the provider what models it has, and checks that the one
// this install is configured to use is among them.
//
// It only downgrades a verdict, never upgrades one: an existing failure about
// a missing key is more useful than "unreachable", which is its consequence.
func (s *Server) probeProvider(c *fiber.Ctx, check *doctorProviderCheck) {
	if !probeLiveProviders || check.Status == "fail" || s.llmRouter == nil {
		return
	}
	prov := s.llmRouter.Provider(check.ID)
	if prov == nil {
		return
	}
	lister, ok := prov.(interface {
		Models(context.Context) ([]string, error)
	})
	if !ok {
		return // nothing to ask; leave the verdict as it stands
	}

	ctx, cancel := context.WithTimeout(c.Context(), providerProbeTimeout)
	defer cancel()
	models, err := lister.Models(ctx)
	if err != nil {
		no := false
		check.Reachable = &no
		check.Status = "fail"
		check.Detail = "provider is configured but not answering at " + check.BaseURL
		check.Remedy = localProviderRemedy(check.ID, check.BaseURL)
		return
	}
	yes := true
	check.Reachable = &yes

	if strings.TrimSpace(check.Model) == "" {
		return // an earlier branch already reported the missing default
	}
	if !modelInList(check.Model, models) {
		no := false
		check.ModelPresent = &no
		check.Status = "fail"
		check.Detail = "the configured model " + check.Model + " is not installed on this provider"
		if studio.IsLocalProvider(check.ID, check.BaseURL) {
			check.Remedy = "install it from Providers, or pick one of the models this machine already has"
		} else {
			check.Remedy = "choose a model this provider offers"
		}
		return
	}
	present := true
	check.ModelPresent = &present
}

// modelInList tolerates Ollama's implicit :latest, so a config naming
// "nomic-embed-text" matches an installed "nomic-embed-text:latest".
func modelInList(want string, have []string) bool {
	want = strings.TrimSpace(want)
	for _, h := range have {
		if h == want || strings.TrimSuffix(h, ":latest") == strings.TrimSuffix(want, ":latest") {
			return true
		}
	}
	return false
}

func localProviderRemedy(id, baseURL string) string {
	if studio.IsLocalProvider(id, baseURL) {
		return "start the local model runtime, then reload this page"
	}
	return "check the base URL and that this machine can reach it"
}

func (s *Server) channelDoctorChecks() []doctorChannelCheck {
	statuses := map[string]channels.AdapterStatus{}
	if s.channels != nil {
		statuses = s.channels.Statuses()
	}
	out := make([]doctorChannelCheck, 0, len(channelSpecs))
	for _, spec := range channelSpecs {
		cfg := s.cfg.Channels[spec.ID]
		enabled := spec.Always
		if v, ok := cfg["enabled"].(bool); ok {
			enabled = v
		}
		configured := false
		for _, f := range spec.Fields {
			if valuePresent(cfg[f.Key]) {
				configured = true
				break
			}
		}
		bots := maskChannelBots(spec, cfg, statuses, s.loader)
		if len(bots) > 0 {
			configured = true
		}
		st, registered := statuses[spec.ID]
		diagnostics := channelDiagnostics(spec, cfg, enabled, registered, st, bots)
		status := "ok"
		detail := "channel is usable"
		for _, d := range diagnostics {
			if d.Severity == "fail" {
				status = "fail"
				detail = d.Message
				break
			}
			if d.Severity == "warn" && status != "fail" {
				status = "warn"
				detail = d.Message
			}
			if d.Severity == "info" && status == "ok" {
				detail = d.Message
			}
		}
		if spec.Always {
			detail = "always-on channel"
		}
		out = append(out, doctorChannelCheck{
			ID: spec.ID, Status: status, Detail: detail,
			Enabled: enabled, Configured: configured, Registered: registered, Connected: st.Connected,
			Diagnostics: diagnostics,
		})
	}
	return out
}

// agentDisableReason is why boot validation switched an agent off. The
// validator already words these well ("model X was not found for provider Y",
// "run `ollama pull X`"); the only thing missing was a way for them to leave
// the log file.
type agentDisableReason struct {
	Field   string `json:"field,omitempty"`
	Problem string `json:"problem"`
	Fix     string `json:"fix,omitempty"`
}
