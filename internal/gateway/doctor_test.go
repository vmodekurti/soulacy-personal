package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/secrets"
)

func TestGatewayDoctorProviderVaultBackedOK(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.cfg.LLM.Providers = map[string]config.ProviderConfig{
		"test": {BaseURL: "https://api.example.com/v1", Model: "fake-model"},
	}
	v := newMemVault()
	s.SetCredentialVault(v)
	if err := secrets.New(v).Set(context.Background(), "llm.providers.test.api_key", "sk-test"); err != nil {
		t.Fatalf("seed vault: %v", err)
	}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/doctor", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("doctor status = %d body=%v", status, body)
	}
	check := firstProviderCheck(t, body)
	if check["status"] != "ok" {
		t.Fatalf("status = %v, want ok; check=%v", check["status"], check)
	}
	if check["key_source"] != "vault" {
		t.Fatalf("key_source = %v, want vault; check=%v", check["key_source"], check)
	}
}

func TestGatewayDoctorProviderRuntimeKeyWarns(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.cfg.LLM.Providers = map[string]config.ProviderConfig{
		"test": {BaseURL: "https://api.example.com/v1", Model: "fake-model", APIKey: "sk-runtime"},
	}
	s.SetCredentialVault(newMemVault())

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/doctor", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("doctor status = %d body=%v", status, body)
	}
	check := firstProviderCheck(t, body)
	if check["status"] != "warn" {
		t.Fatalf("status = %v, want warn; check=%v", check["status"], check)
	}
	if check["key_source"] != "config/runtime" {
		t.Fatalf("key_source = %v, want config/runtime; check=%v", check["key_source"], check)
	}
}

func TestGatewayDoctorProviderMissingKeyFails(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")
	s.cfg.LLM.Providers = map[string]config.ProviderConfig{
		"test": {BaseURL: "https://api.example.com/v1", Model: "fake-model"},
	}
	s.SetCredentialVault(newMemVault())

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/doctor", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("doctor status = %d body=%v", status, body)
	}
	check := firstProviderCheck(t, body)
	if check["status"] != "fail" {
		t.Fatalf("status = %v, want fail; check=%v", check["status"], check)
	}
	if check["key_source"] != "missing" {
		t.Fatalf("key_source = %v, want missing; check=%v", check["key_source"], check)
	}
}

func TestGatewayDoctorLocalProviderDoesNotRequireKey(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.LLM.Providers = map[string]config.ProviderConfig{
		"ollama": {BaseURL: "http://localhost:11434", Model: "llama3"},
	}
	s.SetCredentialVault(newMemVault())

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/doctor", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("doctor status = %d body=%v", status, body)
	}
	check := firstProviderCheck(t, body)
	if check["key_source"] != "not required" {
		t.Fatalf("key_source = %v, want not required; check=%v", check["key_source"], check)
	}
}

func TestGatewayDoctorIncludesChannelDiagnostics(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfg.Channels = map[string]map[string]any{
		"telegram": {
			"enabled":       true,
			"outbound_only": true,
		},
	}

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/doctor", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("doctor status = %d body=%v", status, body)
	}
	channels, ok := body["channels"].([]any)
	if !ok || len(channels) == 0 {
		t.Fatalf("channels missing: %v", body)
	}
	var telegram map[string]any
	for _, raw := range channels {
		row, ok := raw.(map[string]any)
		if ok && row["id"] == "telegram" {
			telegram = row
			break
		}
	}
	if telegram == nil {
		t.Fatalf("telegram channel check missing: %v", channels)
	}
	if telegram["status"] != "fail" {
		t.Fatalf("telegram status = %v, want fail; row=%v", telegram["status"], telegram)
	}
	diagnostics, ok := telegram["diagnostics"].([]any)
	if !ok || len(diagnostics) == 0 {
		t.Fatalf("telegram diagnostics missing: %v", telegram)
	}
}

func firstProviderCheck(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	providers, ok := body["providers"].([]any)
	if !ok || len(providers) == 0 {
		t.Fatalf("providers missing or empty: %v", body)
	}
	check, ok := providers[0].(map[string]any)
	if !ok {
		t.Fatalf("provider check is not an object: %v", providers[0])
	}
	return check
}
