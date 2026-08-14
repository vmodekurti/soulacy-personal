package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/voice"
)

func testVoiceSidecar(t *testing.T) *voice.Sidecar {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"stt":true,"tts":true}`) })
	mux.HandleFunc("/transcribe", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"text":"spoken question"}`) })
	mux.HandleFunc("/synthesize", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("wav"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sidecar, err := voice.NewSidecar(srv.URL, "", "5s", false)
	if err != nil {
		t.Fatal(err)
	}
	return sidecar
}

type fakeMinter struct {
	ready  bool
	detail string
	model  string
	key    voice.EphemeralKey
	err    error
}

func (f *fakeMinter) Provider() string      { return "openai" }
func (f *fakeMinter) Ready() (bool, string) { return f.ready, f.detail }
func (f *fakeMinter) Model() string         { return f.model }
func (f *fakeMinter) Mint(context.Context) (voice.EphemeralKey, error) {
	return f.key, f.err
}

func TestVoiceStatus_NoMinter_Fallback(t *testing.T) {
	s := newTestGateway(t, "")
	status, body := gatewayJSON(t, s, "GET", "/api/v1/voice/status", "", "")
	if status != 200 {
		t.Fatalf("status = %d (fallback must be a clean 200)", status)
	}
	if body["available"] != false {
		t.Fatalf("available = %v, want false", body["available"])
	}
	if body["detail"] == "" {
		t.Fatal("fallback must explain how to enable voice")
	}
}

func TestVoiceStatus_MinterNotReady(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceMinter(&fakeMinter{ready: false, detail: "no API key configured"})
	status, body := gatewayJSON(t, s, "GET", "/api/v1/voice/status", "", "")
	if status != 200 || body["available"] != false {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if body["detail"] != "no API key configured" {
		t.Fatalf("detail = %v", body["detail"])
	}
}

func TestVoiceStatus_Ready(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceMinter(&fakeMinter{ready: true})
	status, body := gatewayJSON(t, s, "GET", "/api/v1/voice/status", "", "")
	if status != 200 || body["available"] != true || body["provider"] != "openai" {
		t.Fatalf("status=%d body=%v", status, body)
	}
}

func TestVoiceStatus_SidecarReady(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceSidecar(testVoiceSidecar(t))
	status, body := gatewayJSON(t, s, "GET", "/api/v1/voice/status", "", "")
	if status != 200 || body["available"] != true || body["provider"] != "sidecar" || body["mode"] != "pipeline" {
		t.Fatalf("status=%d body=%v", status, body)
	}
	status, body = gatewayJSON(t, s, "GET", "/api/v1/voice/capabilities", "", "")
	if status != 200 || body["stt"] != true || body["tts"] != true {
		t.Fatalf("status=%d body=%v", status, body)
	}
}

func TestVoiceSynthesize_Sidecar(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceSidecar(testVoiceSidecar(t))
	status, _ := gatewayRaw(t, s, "POST", "/api/v1/voice/synthesize", "", `{"text":"hello"}`)
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
}

func TestVoiceConfigCanBeSavedFromGUI(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestGatewayWithCfgPath(t, "", configPath)
	status, body := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "", `{
		"voice":{"provider":"sidecar","sidecar_url":"http://127.0.0.1:8081","voice":"af_heart","timeout":"60s","allow_remote":false}
	}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, body)
	}
	configView := body["config"].(map[string]any)
	voiceView := configView["voice"].(map[string]any)
	if voiceView["provider"] != "sidecar" || voiceView["sidecar_url"] != "http://127.0.0.1:8081" {
		t.Fatalf("voice view=%v", voiceView)
	}
}

func TestVoiceConfigRejectsImplicitRemoteSidecar(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestGatewayWithCfgPath(t, "", configPath)
	status, _ := gatewayJSON(t, s, http.MethodPatch, "/api/v1/config", "", `{
		"voice":{"provider":"sidecar","sidecar_url":"https://voice.example.com","allow_remote":false}
	}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", status)
	}
}

func TestVoiceReadinessNoMinterWarns(t *testing.T) {
	s := newTestGateway(t, "")
	got := s.voiceReadiness()
	if got.Status != "warn" || got.Ready || got.Enabled {
		t.Fatalf("voice readiness = %#v, want disabled warning", got)
	}
}

func TestVoiceReadinessConfiguredButNotReady(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceMinter(&fakeMinter{ready: false, detail: "no API key configured"})
	got := s.voiceReadiness()
	if got.Status != "warn" || got.Ready || !got.Enabled {
		t.Fatalf("voice readiness = %#v, want enabled warning", got)
	}
	if got.Detail != "no API key configured" {
		t.Fatalf("detail = %q", got.Detail)
	}
}

func TestVoiceReadinessReady(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceMinter(&fakeMinter{ready: true, model: "gpt-realtime-mini"})
	got := s.voiceReadiness()
	if got.Status != "ok" || !got.Ready || got.Provider != "openai" || got.Model != "gpt-realtime-mini" {
		t.Fatalf("voice readiness = %#v, want ready openai model", got)
	}
}

func TestVoiceEphemeral_NoMinter503(t *testing.T) {
	s := newTestGateway(t, "")
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/voice/ephemeral", "", "")
	if status != 503 {
		t.Fatalf("status = %d, want 503", status)
	}
}

func TestVoiceEphemeral_NotReady503(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceMinter(&fakeMinter{ready: false, detail: "no key"})
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/voice/ephemeral", "", "")
	if status != 503 {
		t.Fatalf("status = %d, want 503", status)
	}
}

func TestVoiceEphemeral_Success(t *testing.T) {
	s := newTestGateway(t, "")
	exp := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	s.SetVoiceMinter(&fakeMinter{ready: true, key: voice.EphemeralKey{
		Key: "ek_abc", ExpiresAt: exp, Model: "gpt-realtime-mini", Provider: "openai",
	}})
	status, body := gatewayJSON(t, s, "POST", "/api/v1/voice/ephemeral", "", "")
	if status != 200 {
		t.Fatalf("status = %d body=%v", status, body)
	}
	if body["key"] != "ek_abc" || body["model"] != "gpt-realtime-mini" || body["provider"] != "openai" {
		t.Fatalf("body = %v", body)
	}
}

func TestVoiceEphemeral_ProviderError502(t *testing.T) {
	s := newTestGateway(t, "")
	s.SetVoiceMinter(&fakeMinter{ready: true, err: errors.New("upstream down")})
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/voice/ephemeral", "", "")
	if status != 502 {
		t.Fatalf("status = %d, want 502", status)
	}
}

func TestVoiceEphemeral_PluginTokenDenied(t *testing.T) {
	// Voice routes are user-facing; plugin tokens stay outside (default-deny).
	s, _ := pluginGateway(t, nil)
	s.SetVoiceMinter(&fakeMinter{ready: true})
	tok := issueToken(t, s)
	status, _ := gatewayJSON(t, s, "POST", "/api/v1/voice/ephemeral", tok, "")
	if status != 403 {
		t.Fatalf("status = %d, want 403", status)
	}
}
