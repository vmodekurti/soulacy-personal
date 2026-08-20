package gateway

import (
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/auth"
	"github.com/soulacy/soulacy/internal/config"
)

func postureServer(t *testing.T, apiKey string, engine *auth.Engine) *Server {
	t.Helper()
	s := &Server{log: zap.NewNop(), authEngine: engine}
	s.setConfig(&config.Config{Server: config.ServerConfig{APIKey: apiKey}})
	return s
}

func newEngine(t *testing.T, cfg auth.Config, staticKey string) *auth.Engine {
	t.Helper()
	e, err := auth.New(cfg, staticKey, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

// Each case is a configuration the installed middleware actually handles. The
// bug being closed is that /ping answered from the config alone and so was
// wrong for whichever middleware it was not thinking about.
func TestPingReportsWhatTheMiddlewareWouldActuallyDo(t *testing.T) {
	tests := []struct {
		name       string
		apiKey     string
		engine     func(*testing.T) *auth.Engine
		wantStatus string
		wantMode   string
		wantDetail bool
	}{
		{
			name:       "no engine and no key is genuinely open",
			wantStatus: authPostureOpen,
			wantMode:   "none",
		},
		{
			name:       "no engine but a key is the legacy static check, which enforces",
			apiKey:     "secret",
			wantStatus: authPostureRequired,
			wantMode:   "apikey",
		},
		{
			name:   "engine with a key requires a credential",
			apiKey: "secret",
			engine: func(t *testing.T) *auth.Engine {
				return newEngine(t, auth.Config{Mode: "apikey"}, "secret")
			},
			wantStatus: authPostureRequired,
			wantMode:   "apikey",
		},
		{
			name: "engine with nothing to verify against is locked, not open",
			engine: func(t *testing.T) *auth.Engine {
				return newEngine(t, auth.Config{Mode: "apikey"}, "")
			},
			wantStatus: authPostureUnreachable,
			wantMode:   "apikey",
			wantDetail: true,
		},
		{
			name: "jwt mode with no verifier is locked, and says so in jwt terms",
			engine: func(t *testing.T) *auth.Engine {
				return newEngine(t, auth.Config{Mode: "jwt"}, "")
			},
			wantStatus: authPostureUnreachable,
			wantMode:   "jwt",
			wantDetail: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var engine *auth.Engine
			if tt.engine != nil {
				engine = tt.engine(t)
			}
			s := postureServer(t, tt.apiKey, engine)
			status, mode, detail := s.authPosture()
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", mode, tt.wantMode)
			}
			if tt.wantDetail && strings.TrimSpace(detail) == "" {
				t.Error("a locked-out deployment must say what to do about it; detail was empty")
			}
			if !tt.wantDetail && detail != "" {
				t.Errorf("detail = %q on a healthy posture", detail)
			}
		})
	}
}

// "unreachable" and "open" describe opposite problems. Collapsing them — which
// is what the original code did — sends an operator hunting an exposure that
// does not exist.
func TestLockedOutIsNeverReportedAsOpen(t *testing.T) {
	s := postureServer(t, "", newEngine(t, auth.Config{Mode: "apikey"}, ""))
	if status, _, _ := s.authPosture(); status == authPostureOpen {
		t.Fatal("a deployment that refuses every request was reported as open")
	}
}
