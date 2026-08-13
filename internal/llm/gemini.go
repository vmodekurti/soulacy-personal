// gemini.go — Google Gemini provider (generativelanguage.googleapis.com).
//
// Gemini's :generateContent endpoint shape:
//
//   - Roles: "user" and "model" (no "system" — system_instruction is a
//     top-level field, similar to Anthropic's `system`).
//   - Content is a list of "parts": text, functionCall, functionResponse,
//     inlineData (for images/audio).
//   - Tool results travel as a user-role message with a functionResponse part
//     keyed by the function name.
//   - Structured outputs: a top-level generationConfig.responseSchema field
//     (JSON Schema subset) plus responseMimeType="application/json".
//
// Spec: https://ai.google.dev/api/generate-content
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

type GeminiProvider struct {
	baseURL        string // default https://generativelanguage.googleapis.com
	apiKey         string
	model          string
	thinkingBudget int    // 0=off, -1=auto, N=tokens
	safetyLevel    string // ""|"default"|"off"|"strict"
	client         *http.Client
}

func NewGeminiProvider(baseURL, apiKey, model string) *GeminiProvider {
	return NewGeminiProviderWithOptions(baseURL, apiKey, model, 0, "")
}

// dropOrphanFunctionResponses removes functionResponse parts that are NOT
// immediately preceded by a model turn containing a functionCall. Gemini
// rejects the whole request ("function response turn comes immediately after a
// function call turn", HTTP 400) when a function response stands alone — which
// happens when upstream context-window trimming drops a function-call turn but
// keeps its (often large) result. A turn left with no parts after stripping is
// removed entirely.
func dropOrphanFunctionResponses(contents []map[string]any) []map[string]any {
	hasFunctionCall := func(turn map[string]any) bool {
		if turn == nil || turn["role"] != "model" {
			return false
		}
		parts, _ := turn["parts"].([]map[string]any)
		for _, p := range parts {
			if _, ok := p["functionCall"]; ok {
				return true
			}
		}
		return false
	}
	out := make([]map[string]any, 0, len(contents))
	for _, turn := range contents {
		parts, _ := turn["parts"].([]map[string]any)
		hasFR := false
		for _, p := range parts {
			if _, ok := p["functionResponse"]; ok {
				hasFR = true
				break
			}
		}
		if !hasFR {
			out = append(out, turn)
			continue
		}
		if len(out) > 0 && hasFunctionCall(out[len(out)-1]) {
			out = append(out, turn) // properly paired — keep as-is
			continue
		}
		// Orphan: strip the functionResponse parts, keep any other parts.
		kept := make([]map[string]any, 0, len(parts))
		for _, p := range parts {
			if _, ok := p["functionResponse"]; ok {
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) > 0 {
			turn["parts"] = kept
			out = append(out, turn)
		}
	}
	return out
}

// NewGeminiProviderWithOptions creates a GeminiProvider with extended settings.
//
//	thinkingBudget: 0=off (default), -1=auto, N=token budget for reasoning
//	safetyLevel:    ""|"default"=Gemini defaults, "off"=BLOCK_NONE, "strict"=BLOCK_LOW_AND_ABOVE
func NewGeminiProviderWithOptions(baseURL, apiKey, model string, thinkingBudget int, safetyLevel string) *GeminiProvider {
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	if model == "" {
		model = "gemini-2.5-pro"
	}
	return &GeminiProvider{
		baseURL:        baseURL,
		apiKey:         apiKey,
		model:          model,
		thinkingBudget: thinkingBudget,
		safetyLevel:    safetyLevel,
		client:         SharedHTTPClient(180 * time.Second),
	}
}

func (p *GeminiProvider) ID() string { return "google" }

func (p *GeminiProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}

	origToWire, wireToOrig := buildGeminiToolMaps(req.Tools)

	// Translate ChatMessage list → Gemini contents + system_instruction.
	//
	// Gemini enforces strict turn alternation:
	//   user → model → user → model → …
	// When the model emits function calls, ALL tool results must arrive in a
	// SINGLE user turn immediately after (one functionResponse part per call).
	// Emitting each tool result as its own separate user turn violates this and
	// produces a 400 "function call turn comes immediately after a user turn".
	var systemPrompt string
	contents := make([]map[string]any, 0, len(req.Messages))

	for i := 0; i < len(req.Messages); {
		m := req.Messages[i]
		switch m.Role {
		case "system":
			if systemPrompt != "" {
				systemPrompt += "\n\n"
			}
			systemPrompt += m.Content
			i++
		case "user":
			text := m.Content
			if text == "" {
				text = "." // Gemini rejects empty text parts
			}
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": []map[string]any{{"text": text}},
			})
			i++
		case "assistant":
			parts := []map[string]any{}
			if m.Content != "" {
				parts = append(parts, map[string]any{"text": m.Content})
			}
			for _, tc := range m.ToolCalls {
				name := tc.Name
				if wire, ok := origToWire[name]; ok {
					name = wire
				}
				part := map[string]any{
					"functionCall": map[string]any{
						"name": name,
						"args": tc.Arguments,
					},
				}
				// Gemini 2.5 thinking models: thoughtSignature is a Part-level
				// field (not nested inside functionCall). Must be echoed back
				// verbatim or Gemini returns 400 INVALID_ARGUMENT.
				if tc.ThoughtSignature != "" {
					part["thoughtSignature"] = tc.ThoughtSignature
				}
				parts = append(parts, part)
			}
			// Skip turns with no parts — Gemini rejects "parts": [].
			if len(parts) > 0 {
				contents = append(contents, map[string]any{
					"role":  "model",
					"parts": parts,
				})
			}
			i++
		case "tool":
			// Collect ALL consecutive tool messages into one user turn so
			// Gemini sees a single functionResponse batch, not N separate turns.
			parts := []map[string]any{}
			for i < len(req.Messages) && req.Messages[i].Role == "tool" {
				tm := req.Messages[i]
				name := tm.Name
				if wire, ok := origToWire[name]; ok {
					name = wire
				}
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"name": name,
						"response": map[string]any{
							"name":    name,
							"content": tm.Content,
						},
					},
				})
				i++
			}
			contents = append(contents, map[string]any{
				"role":  "user",
				"parts": parts,
			})
		default:
			i++
		}
	}

	// Drop orphaned function responses BEFORE merging so any resulting
	// same-role adjacency is cleaned up by the merge pass below.
	contents = dropOrphanFunctionResponses(contents)

	// Post-process contents to satisfy Gemini's strict alternation rules:
	//   1. Merge consecutive same-role turns into one (two model turns in a row
	//      from e.g. a thinking text turn + tool-call turn cause a 400).
	//   2. Ensure the sequence starts with a user turn (Gemini rejects sequences
	//      that open with a model turn).
	if len(contents) > 0 {
		merged := make([]map[string]any, 0, len(contents))
		for _, turn := range contents {
			if len(merged) == 0 {
				merged = append(merged, turn)
				continue
			}
			last := merged[len(merged)-1]
			if last["role"] == turn["role"] {
				// Append parts from this turn into the previous same-role turn.
				if ep, ok := last["parts"].([]map[string]any); ok {
					if np, ok2 := turn["parts"].([]map[string]any); ok2 {
						last["parts"] = append(ep, np...)
					}
				}
			} else {
				merged = append(merged, turn)
			}
		}
		// Gemini requires the first turn to be from the user.
		if merged[0]["role"] != "user" {
			merged = append([]map[string]any{
				{"role": "user", "parts": []map[string]any{{"text": "."}}},
			}, merged...)
		}
		contents = merged
	}

	genCfg := map[string]any{
		"temperature": req.Temperature,
	}
	if req.TopP > 0 {
		genCfg["topP"] = req.TopP
	}
	if req.MaxTokens > 0 {
		genCfg["maxOutputTokens"] = req.MaxTokens
	}
	// Thinking config: use the provider's configured budget.
	// 0 = disabled (safe default for tool-use agents — avoids thoughtSignature
	// round-trip issues), -1 = auto, N = explicit token budget.
	// NOTE: when thinking is enabled the model attaches a thoughtSignature to
	// each functionCall part which must be echoed back verbatim; the engine
	// already handles this via message.ToolCall.ThoughtSignature.
	if p.thinkingBudget == 0 {
		// Some models (like gemini-2.5-pro) require thinking and reject budget=0 with a 400 error.
		// For those models, we omit thinkingConfig entirely so it uses the model's default thinking.
		// For other models, we can explicitly set budget=0 to disable thinking.
		if strings.Contains(strings.ToLower(model), "pro") || strings.Contains(strings.ToLower(model), "thinking") {
			// omit thinkingConfig
		} else {
			genCfg["thinkingConfig"] = map[string]any{"thinkingBudget": 0}
		}
	} else if p.thinkingBudget == -1 {
		genCfg["thinkingConfig"] = map[string]any{"thinkingMode": "AUTO"}
	} else {
		genCfg["thinkingConfig"] = map[string]any{"thinkingBudget": p.thinkingBudget}
	}

	// Structured outputs
	switch req.ResponseFormat {
	case "json":
		genCfg["responseMimeType"] = "application/json"
	case "json_schema":
		genCfg["responseMimeType"] = "application/json"
		if req.JSONSchema != nil {
			genCfg["responseSchema"] = sanitizeSchemaForGemini(req.JSONSchema)
		}
	}

	body := map[string]any{
		"contents":         contents,
		"generationConfig": genCfg,
	}
	if systemPrompt != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []map[string]any{{"text": systemPrompt}},
		}
	}
	// Safety settings — only injected when explicitly configured.
	// "off" sets all harm categories to BLOCK_NONE (needed for most agent/dev work).
	// "strict" sets BLOCK_LOW_AND_ABOVE.
	// "" / "default" leaves Gemini defaults untouched.
	if p.safetyLevel == "off" {
		categories := []string{
			"HARM_CATEGORY_HARASSMENT",
			"HARM_CATEGORY_HATE_SPEECH",
			"HARM_CATEGORY_SEXUALLY_EXPLICIT",
			"HARM_CATEGORY_DANGEROUS_CONTENT",
			"HARM_CATEGORY_CIVIC_INTEGRITY",
		}
		ss := make([]map[string]any, len(categories))
		for i, cat := range categories {
			ss[i] = map[string]any{"category": cat, "threshold": "BLOCK_NONE"}
		}
		body["safetySettings"] = ss
	} else if p.safetyLevel == "strict" {
		categories := []string{
			"HARM_CATEGORY_HARASSMENT",
			"HARM_CATEGORY_HATE_SPEECH",
			"HARM_CATEGORY_SEXUALLY_EXPLICIT",
			"HARM_CATEGORY_DANGEROUS_CONTENT",
		}
		ss := make([]map[string]any, len(categories))
		for i, cat := range categories {
			ss[i] = map[string]any{"category": cat, "threshold": "BLOCK_LOW_AND_ABOVE"}
		}
		body["safetySettings"] = ss
	}
	if len(req.Tools) > 0 {
		funcs := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			name := t.Name
			if wire, ok := origToWire[name]; ok {
				name = wire
			}
			funcs[i] = map[string]any{
				"name":        name,
				"description": t.Description,
				"parameters":  sanitizeSchemaForGemini(t.Parameters),
			}
		}
		body["tools"] = []map[string]any{{"functionDeclarations": funcs}}
	}

	// Use the x-goog-api-key header rather than a ?key=... URL param so the
	// key can't end up in any access log along the outbound chain.
	// (PRODUCTION_AUDIT → HIGH/Security)
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:generateContent",
		p.baseURL, url.PathEscape(model))

	strippedOptional := map[string]bool{}
	var bodyBytes []byte
	var providerRequestID string
	for {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("google: marshal request: %w", err)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		httpReq.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(payload)), nil
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-goog-api-key", p.apiKey)

		resp, err := DoWithRetry(ctx, p.client, httpReq, RetryConfig{})
		if err != nil {
			return nil, fmt.Errorf("google: request failed: %w", err)
		}
		bodyBytes, _ = io.ReadAll(resp.Body)
		providerRequestID = firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("x-goog-request-id"))
		_ = resp.Body.Close()

		if resp.StatusCode < 300 {
			break
		}
		param := geminiUnsupportedOptionalParam(bodyBytes)
		if param != "" && !strippedOptional[param] {
			if param == "safetySettings" {
				delete(body, "safetySettings")
			} else if cfg, ok := body["generationConfig"].(map[string]any); ok {
				delete(cfg, param)
			}
			strippedOptional[param] = true
			continue
		}
		if len(bodyBytes) == 0 {
			return nil, fmt.Errorf("google: http %d (empty response body — request may be malformed or too large)", resp.StatusCode)
		}
		return nil, fmt.Errorf("google: http %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"` // internal reasoning — skip in visible content
					// thoughtSignature lives on the Part, not inside functionCall.
					// Gemini 2.5 thinking models attach it here and require it
					// to be echoed back at the same level in conversation history.
					ThoughtSignature string `json:"thoughtSignature"`
					FunctionCall     *struct {
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall,omitempty"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount        int `json:"promptTokenCount"`
			CandidatesTokenCount    int `json:"candidatesTokenCount"`
			TotalTokenCount         int `json:"totalTokenCount"`
			ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
			ToolUsePromptTokenCount int `json:"toolUsePromptTokenCount"`
			CachedContentTokenCount int `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("google: decode: %w", err)
	}

	r := &CompletionResponse{
		InputTokens:         max(0, result.UsageMetadata.PromptTokenCount-result.UsageMetadata.CachedContentTokenCount-result.UsageMetadata.ToolUsePromptTokenCount),
		OutputTokens:        result.UsageMetadata.CandidatesTokenCount,
		TotalTokens:         result.UsageMetadata.TotalTokenCount,
		ReasoningTokens:     result.UsageMetadata.ThoughtsTokenCount,
		ToolUsePromptTokens: result.UsageMetadata.ToolUsePromptTokenCount,
		CacheReadTokens:     result.UsageMetadata.CachedContentTokenCount,
		ProviderRequestID:   providerRequestID,
	}
	if len(result.Candidates) == 0 {
		return r, nil
	}

	for i, part := range result.Candidates[0].Content.Parts {
		// Skip internal thought parts — they are not user-visible text.
		if part.Thought {
			continue
		}
		if part.Text != "" {
			if r.Content != "" {
				r.Content += "\n"
			}
			r.Content += part.Text
		}
		if part.FunctionCall != nil {
			name := part.FunctionCall.Name
			if orig, ok := wireToOrig[name]; ok {
				name = orig
			}
			r.ToolCalls = append(r.ToolCalls, message.ToolCall{
				ID:               fmt.Sprintf("call_%d_%s", i, name),
				Name:             name,
				Arguments:        part.FunctionCall.Args,
				ThoughtSignature: part.ThoughtSignature, // on the Part, not inside FunctionCall
			})
		}
	}
	return r, nil
}

// Models lists the available Gemini models for this API key.
func (p *GeminiProvider) Models(ctx context.Context) ([]string, error) {
	// Header auth, same rationale as Complete().
	endpoint := fmt.Sprintf("%s/v1beta/models", p.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return []string{p.model}, nil
	}
	httpReq.Header.Set("x-goog-api-key", p.apiKey)
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return []string{p.model}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return []string{p.model}, nil
	}
	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return []string{p.model}, nil
	}
	ids := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		// Only models that can do generateContent are useful here.
		ok := false
		for _, meth := range m.SupportedGenerationMethods {
			if meth == "generateContent" {
				ok = true
				break
			}
		}
		if !ok {
			continue
		}
		// Strip "models/" prefix.
		name := m.Name
		if len(name) > 7 && name[:7] == "models/" {
			name = name[7:]
		}
		ids = append(ids, name)
	}
	if len(ids) == 0 {
		return []string{p.model}, nil
	}
	return ids, nil
}

// sanitizeSchemaForGemini strips JSON Schema constructs Gemini doesn't accept
// and repairs the required/properties contract that Gemini enforces strictly:
// every name in `required` must exist as a key in `properties`.
//
// Key subtlety: `properties` is NOT a schema itself — it maps property NAMES
// to sub-schemas. We must NOT recursively call sanitizeSchemaForGemini on the
// properties map as a whole (that would treat property names as schema keywords
// and strip them). Instead we iterate property names, sanitize each sub-schema,
// and re-assemble.
func sanitizeSchemaForGemini(s map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	allowed := map[string]bool{
		"type": true, "format": true, "description": true, "nullable": true,
		"enum": true, "items": true, "properties": true, "required": true,
		"minimum": true, "maximum": true, "minItems": true, "maxItems": true,
		"minLength": true, "maxLength": true, "pattern": true,
	}

	out := make(map[string]any, len(s))
	for k, v := range s {
		if !allowed[k] {
			continue
		}
		if k == "properties" {
			// Preserve property names; only sanitize their sub-schemas.
			if propsMap, ok := v.(map[string]any); ok {
				sanitized := make(map[string]any, len(propsMap))
				for propName, propSchema := range propsMap {
					if sm, ok := propSchema.(map[string]any); ok {
						sanitized[propName] = sanitizeSchemaForGemini(sm)
					} else {
						sanitized[propName] = propSchema
					}
				}
				out["properties"] = sanitized
			}
			continue
		}
		switch vv := v.(type) {
		case map[string]any:
			out[k] = sanitizeSchemaForGemini(vv)
		case []any:
			if k == "type" {
				var nonNullType any
				hasNull := false
				for _, item := range vv {
					if s, ok := item.(string); ok && s == "null" {
						hasNull = true
					} else {
						nonNullType = item
					}
				}
				if nonNullType != nil {
					out["type"] = nonNullType
				}
				if hasNull {
					out["nullable"] = true
				}
				continue
			}

			arr := make([]any, len(vv))
			for i, item := range vv {
				if m, ok := item.(map[string]any); ok {
					arr[i] = sanitizeSchemaForGemini(m)
				} else {
					arr[i] = item
				}
			}
			out[k] = arr
		default:
			out[k] = v
		}
	}

	// Gemini rejects `required` entries whose names are absent from `properties`.
	// Filter to only the intersection.
	if props, hasProps := out["properties"].(map[string]any); hasProps {
		if req, hasReq := out["required"].([]any); hasReq {
			filtered := make([]any, 0, len(req))
			for _, r := range req {
				if name, ok := r.(string); ok {
					if _, defined := props[name]; defined {
						filtered = append(filtered, name)
					}
				}
			}
			if len(filtered) > 0 {
				out["required"] = filtered
			} else {
				delete(out, "required")
			}
		}
	}

	return out
}

func geminiUnsupportedOptionalParam(body []byte) string {
	msg := strings.ToLower(string(body))
	if msg == "" {
		return ""
	}
	if !(strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "unrecognized") ||
		strings.Contains(msg, "unknown") ||
		strings.Contains(msg, "deprecated") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "invalid_argument") ||
		strings.Contains(msg, "bad request") ||
		strings.Contains(msg, "bad_request")) {
		return ""
	}
	for _, candidate := range []struct {
		key     string
		markers []string
	}{
		{"thinkingConfig", []string{"thinkingconfig", "thinking_config", "thinking budget", "thinkingbudget", "thinking mode", "thinkingmode"}},
		{"responseSchema", []string{"responseschema", "response_schema", "response schema"}},
		{"responseMimeType", []string{"responsemimetype", "response_mime_type", "response mime", "response mime type"}},
		{"topP", []string{"topp", "top_p", "top p"}},
		{"temperature", []string{"temperature"}},
		{"maxOutputTokens", []string{"maxoutputtokens", "max_output_tokens", "max output tokens"}},
		{"safetySettings", []string{"safetysettings", "safety_settings", "safety settings", "block_none"}},
	} {
		for _, marker := range candidate.markers {
			if strings.Contains(msg, marker) {
				return candidate.key
			}
		}
	}
	return ""
}

func buildGeminiToolMaps(tools []ToolSchema) (map[string]string, map[string]string) {
	origToWire := map[string]string{}
	wireToOrig := map[string]string{}
	used := map[string]int{}
	for _, t := range tools {
		wire := geminiSafeToolName(t.Name)
		if n := used[wire]; n > 0 {
			used[wire] = n + 1
			suffix := fmt.Sprintf("_%d", n+1)
			maxBase := 64 - len(suffix)
			if maxBase < 1 {
				maxBase = 1
			}
			if len(wire) > maxBase {
				wire = wire[:maxBase]
			}
			wire += suffix
		} else {
			used[wire] = 1
		}
		origToWire[t.Name] = wire
		wireToOrig[wire] = t.Name
	}
	return origToWire, wireToOrig
}

func geminiSafeToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "tool"
	}
	var b strings.Builder
	b.Grow(len(name))
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		case r == '_':
			b.WriteRune(r)
		default:
			if i == 0 {
				b.WriteByte('a')
			} else {
				b.WriteByte('_')
			}
		}
		if b.Len() >= 64 {
			break
		}
	}
	out := b.String()
	if out == "" {
		return "tool"
	}
	return out
}
