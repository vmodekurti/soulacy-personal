// voice.go — provider-neutral voice routes. Realtime providers use ephemeral
// browser credentials; sidecars expose local STT/TTS over a small HTTP bridge.
package gateway

import (
	"context"
	"io"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/voice"
)

// VoiceMinter is the control-plane seam for realtime voice providers.
// internal/voice.OpenAIMinter satisfies it; tests use fakes.
type VoiceMinter interface {
	Provider() string
	Ready() (bool, string)
	Mint(ctx context.Context) (voice.EphemeralKey, error)
}

type voiceReadiness struct {
	Status   string `json:"status"`
	Score    int    `json:"score"`
	Enabled  bool   `json:"enabled"`
	Ready    bool   `json:"ready"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Detail   string `json:"detail"`
	Next     string `json:"next,omitempty"`
}

// SetVoiceMinter wires the realtime voice provider. Call after New(),
// before Start(). When nil the voice routes degrade gracefully
// (status: unavailable; ephemeral: 503).
func (s *Server) SetVoiceMinter(m VoiceMinter) {
	s.pluginMu.Lock()
	defer s.pluginMu.Unlock()
	s.voiceMinter = m
}

func (s *Server) voiceMinterRef() VoiceMinter {
	s.pluginMu.RLock()
	defer s.pluginMu.RUnlock()
	return s.voiceMinter
}

// SetVoiceSidecar wires a speech sidecar. Sidecars never receive LLM provider
// credentials and may be swapped independently of the selected agent model.
func (s *Server) SetVoiceSidecar(sidecar *voice.Sidecar) {
	s.pluginMu.Lock()
	defer s.pluginMu.Unlock()
	s.voiceSidecar = sidecar
}

func (s *Server) voiceSidecarRef() *voice.Sidecar {
	s.pluginMu.RLock()
	defer s.pluginMu.RUnlock()
	return s.voiceSidecar
}

// handleVoiceStatus reports realtime-voice availability for the Chat panel.
//
//	GET /api/v1/voice/status
func (s *Server) handleVoiceStatus(c *fiber.Ctx) error {
	if sidecar := s.voiceSidecarRef(); sidecar != nil {
		ready, detail := sidecar.Ready()
		out := fiber.Map{
			"available": ready,
			"provider":  "sidecar",
			"mode":      "pipeline",
			"model":     sidecar.Model(),
			"endpoint":  sidecar.URL(),
			"voice":     sidecar.Voice(),
		}
		if detail != "" {
			out["detail"] = detail
		}
		return c.JSON(out)
	}
	m := s.voiceMinterRef()
	if m == nil {
		return c.JSON(fiber.Map{
			"available": false,
			"provider":  "",
			"mode":      "disabled",
			"detail":    "Voice is not configured. Connect a local sidecar or enable a realtime provider.",
		})
	}
	ready, detail := m.Ready()
	out := fiber.Map{"available": ready, "provider": m.Provider(), "mode": "realtime"}
	if detail != "" {
		out["detail"] = detail
	}
	if mm, ok := m.(interface{ Model() string }); ok {
		out["model"] = mm.Model()
	}
	return c.JSON(out)
}

func (s *Server) voiceReadiness() voiceReadiness {
	provider := ""
	if s != nil && s.cfg != nil {
		provider = s.cfg.Voice.Provider
	}
	if sidecar := s.voiceSidecarRef(); sidecar != nil {
		ready, detail := sidecar.Ready()
		out := voiceReadiness{Status: "warn", Score: 60, Enabled: true, Ready: ready, Provider: "sidecar", Model: sidecar.Model(), Detail: detail}
		if ready {
			out.Status, out.Score = "ok", 90
			out.Detail = "Provider-neutral voice sidecar is ready; Chat uses local STT/TTS with the configured agent LLM."
			out.Next = "Run a microphone and playback test before launch."
		} else {
			out.Next = "Start the configured sidecar, then verify /api/v1/voice/status."
		}
		return out
	}
	m := s.voiceMinterRef()
	if m == nil {
		if provider == "" {
			return voiceReadiness{
				Status:  "warn",
				Score:   55,
				Enabled: false,
				Ready:   false,
				Detail:  "Voice is disabled; text chat and channel agents still work.",
				Next:    "Connect a local voice sidecar from Chat or run `sy voice configure`.",
			}
		}
		return voiceReadiness{
			Status:   "fail",
			Score:    35,
			Enabled:  true,
			Ready:    false,
			Provider: provider,
			Detail:   "Realtime voice is configured with an unsupported or unwired provider.",
			Next:     "Use voice.provider: openai for the current voice MVP, or install a compatible voice sidecar.",
		}
	}
	ready, detail := m.Ready()
	out := voiceReadiness{
		Enabled:  true,
		Ready:    ready,
		Provider: m.Provider(),
		Detail:   "Realtime voice control plane is configured.",
	}
	if mm, ok := m.(interface{ Model() string }); ok {
		out.Model = mm.Model()
	}
	if ready {
		out.Status = "ok"
		out.Score = 86
		out.Detail = "Chat can mint ephemeral realtime voice sessions; browser audio connects directly to the provider."
		out.Next = "Run one credential-backed voice session before launch if voice is part of the product promise."
		return out
	}
	out.Status = "warn"
	out.Score = 60
	out.Detail = detail
	out.Next = "Fix the voice provider credential, then verify /api/v1/voice/status reports available."
	return out
}

func (s *Server) handleVoiceCapabilities(c *fiber.Ctx) error {
	sidecar := s.voiceSidecarRef()
	if sidecar == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no voice sidecar configured"})
	}
	caps, err := sidecar.Capabilities(c.Context())
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(caps)
}

// handleVoiceTranscribe accepts browser-recorded audio and forwards it to the
// configured sidecar. The 16 MiB limit is enforced before proxying.
func (s *Server) handleVoiceTranscribe(c *fiber.Ctx) error {
	sidecar := s.voiceSidecarRef()
	if sidecar == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no voice sidecar configured"})
	}
	fh, err := c.FormFile("audio")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "multipart field 'audio' is required"})
	}
	if fh.Size > 16<<20 {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{"error": "audio exceeds 16 MiB limit"})
	}
	f, err := fh.Open()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "could not read audio"})
	}
	defer f.Close()
	contentType := fh.Header.Get("Content-Type")
	text, err := sidecar.Transcribe(c.Context(), contentType, io.LimitReader(f, 16<<20))
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"text": text})
}

func (s *Server) handleVoiceSynthesize(c *fiber.Ctx) error {
	sidecar := s.voiceSidecarRef()
	if sidecar == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "no voice sidecar configured"})
	}
	var in struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
	}
	if err := c.BodyParser(&in); err != nil || strings.TrimSpace(in.Text) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "text is required"})
	}
	audio, contentType, err := sidecar.Synthesize(c.Context(), in.Text, in.Voice)
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": err.Error()})
	}
	c.Set(fiber.HeaderContentType, contentType)
	return c.Send(audio)
}

// handleVoiceEphemeral mints a short-lived client key for the browser's
// direct WebRTC connection to the provider. The user's real API key never
// leaves the host.
//
//	POST /api/v1/voice/ephemeral
func (s *Server) handleVoiceEphemeral(c *fiber.Ctx) error {
	m := s.voiceMinterRef()
	if m == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "no realtime voice provider configured",
		})
	}
	if ready, detail := m.Ready(); !ready {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": "voice provider not ready: " + detail,
		})
	}
	key, err := m.Mint(c.Context())
	if err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	return c.JSON(fiber.Map{
		"key":        key.Key,
		"expires_at": key.ExpiresAt,
		"model":      key.Model,
		"provider":   key.Provider,
	})
}
