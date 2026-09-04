package studio

import "strings"

// ModelAdvice describes the model Studio will build with, framed local-first and
// supportively (no model shaming). Studio is local-first by default and
// cloud-assisted by choice, so this advice: (a) names the builder model and
// whether it runs locally, (b) sets honest expectations for small local models
// WITHOUT pushing the user to the cloud, (c) surfaces whether a stronger
// frontier model is available as OPTIONAL assistance, and (d) flags when using
// the current builder would send the prompt off-box (the cloud-escalation gate).
type ModelAdvice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Configured is false when no builder model is resolvable.
	Configured bool `json:"configured"`
	// Local is true when the builder model runs on the user's machine.
	Local bool `json:"local"`
	// Severity: "ok" (ready), "info" (a supportive heads-up — never a warning),
	// or "block" (no usable model).
	Severity string `json:"severity"`
	// Message is the plain-language, supportive explanation.
	Message string `json:"message"`
	// LocalComplexityNote is shown for small local models: complex builds may
	// need extra checks/questions. Framed as a tradeoff, not a deficiency.
	LocalComplexityNote string `json:"local_complexity_note,omitempty"`
	// CloudEscalation, when set, means the current builder is a CLOUD model;
	// using it sends the prompt off-box. The GUI asks before proceeding.
	CloudEscalation bool `json:"cloud_escalation,omitempty"`
	// FrontierAvailable + FrontierProvider: a stronger cloud model IS configured
	// and can be OFFERED as optional assistance for complex builds (hybrid use).
	FrontierAvailable bool   `json:"frontier_available,omitempty"`
	FrontierProvider  string `json:"frontier_provider,omitempty"`
}

// strongModelHints are substrings of model names capable enough that no
// complexity note is needed. Matched case-insensitively against the model id.
var strongModelHints = []string{
	"gemini-2.5-pro", "gemini-2.0-pro", "gemini-1.5-pro", "gemini-pro",
	"gpt-4", "gpt-5", "o1", "o3", "o4",
	"claude-3.5", "claude-3-7", "claude-3.7", "claude-4", "claude-opus", "claude-sonnet", "sonnet", "opus",
	"deepseek-r1", "deepseek-v3", "qwen2.5-72b", "qwen-2.5-72b", "qwen2.5:72b", "llama-3.3-70b", "llama3.3:70b", "llama3:70b", "70b",
	"mistral-large", "command-r-plus", "grok-2", "grok-3", "grok-4", "mixtral",
}

// smallModelHints suggest a model that benefits from Studio's pre-planning +
// extra checks. Used ONLY to attach a supportive complexity note, never a warning.
var smallModelHints = []string{
	"flash", "mini", "small", "tiny", "1b", "3b", "7b", "8b", "9b", "scout", "phi", "gemma",
}

// AssessModel evaluates the builder provider/model, local-first. baseURL lets it
// classify locality (a self-hosted OpenAI-compatible endpoint counts as local).
// Pure + deterministic. Empty provider OR model means "not configured" (block).
func AssessModel(provider, model, baseURL string) ModelAdvice {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)

	if provider == "" || model == "" {
		return ModelAdvice{
			Provider: provider, Model: model, Configured: false,
			Severity: "block",
			Message:  "No builder model is set for Studio yet. Pick one with the ⚙ button — a local model keeps everything on your machine.",
		}
	}

	local := IsLocalProvider(provider, baseURL)
	adv := ModelAdvice{Provider: provider, Model: model, Configured: true, Local: local, Severity: "ok"}

	if local {
		adv.Message = "Building locally with " + model + " — your prompt stays on your machine."
		if isSmallModel(model) && !isStrongModel(model) {
			adv.Severity = "info"
			adv.LocalComplexityNote = "This is a compact local model. Studio pre-plans and may ask a couple of extra questions or run extra checks on complex builds to keep results reliable — no need to switch to the cloud."
		}
		return adv
	}

	// Cloud builder: this is allowed, but it's an off-box action — flag it so the
	// UI can ask first (cloud-escalation gate), framed as a privacy heads-up.
	adv.Severity = "info"
	adv.CloudEscalation = true
	adv.Message = "Your Studio builder model (" + provider + " / " + model + ") is a cloud model, so your prompt is sent to " + provider + " when you generate. You can switch to a local model anytime to keep everything on your machine."
	return adv
}

func isStrongModel(model string) bool {
	lm := strings.ToLower(model)
	for _, h := range strongModelHints {
		if strings.Contains(lm, h) {
			return true
		}
	}
	return false
}

func isSmallModel(model string) bool {
	lm := strings.ToLower(model)
	for _, h := range smallModelHints {
		if strings.Contains(lm, h) {
			return true
		}
	}
	return false
}
