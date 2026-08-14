package runtime

import (
	"strings"
	"testing"
)

func TestResponseModeSystemPromptVoice(t *testing.T) {
	prompt := responseModeSystemPrompt(map[string]string{"response.mode": "voice"})
	for _, want := range []string{"<spoken_response>", "<display_response>", "never exceed 140 words", "Do not mention voice mode"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("voice response prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestSplitVoiceResponseKeepsWrittenAndSpokenPresentationsSeparate(t *testing.T) {
	raw := `<spoken_response>
Micron looks strong overall, but I'd watch today's low volume.
</spoken_response>
<display_response>
## Micron analysis

| Metric | Value |
| --- | --- |
| Forward P/E | 6.2 |
</display_response>`
	display, spoken := splitVoiceResponse(raw)
	if spoken != "Micron looks strong overall, but I'd watch today's low volume." {
		t.Fatalf("spoken = %q", spoken)
	}
	if !strings.Contains(display, "## Micron analysis") || !strings.Contains(display, "| Forward P/E | 6.2 |") {
		t.Fatalf("display = %q", display)
	}
	if strings.Contains(display, "spoken_response") {
		t.Fatalf("display leaked voice envelope: %q", display)
	}
}

func TestSplitVoiceResponseFallsBackWhenEnvelopeIsMissing(t *testing.T) {
	const raw = "## Ordinary answer\n\nNothing special."
	display, spoken := splitVoiceResponse(raw)
	if display != raw || spoken != "" {
		t.Fatalf("display=%q spoken=%q", display, spoken)
	}
}

func TestResponseModeSystemPromptDoesNotAffectTextChat(t *testing.T) {
	for _, meta := range []map[string]string{nil, {}, {"response.mode": "text"}, {"response.mode": "unknown"}} {
		if got := responseModeSystemPrompt(meta); got != "" {
			t.Fatalf("responseModeSystemPrompt(%v) = %q, want empty", meta, got)
		}
	}
}
