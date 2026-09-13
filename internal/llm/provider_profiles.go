package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

func (o *OllamaProvider) DefaultModel() string    { return o.model }
func (p *OpenAIProvider) DefaultModel() string    { return p.model }
func (p *AnthropicProvider) DefaultModel() string { return p.model }
func (p *GeminiProvider) DefaultModel() string    { return p.model }

// readProfileJSON uses only the configured provider endpoint. It never follows
// redirects (in particular with credentials), retries, loads models or performs
// inference. Model templates and descriptions are intentionally not retained.
func readProfileJSON(ctx context.Context, client *http.Client, method, endpoint, apiKey string, body []byte, dst any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("unsupported metadata endpoint")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("x-goog-api-key", apiKey)
	}
	bounded := *client
	bounded.Timeout = modelProfileTimeout
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := bounded.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("metadata status %d", resp.StatusCode)
	}
	const maxBytes = 2 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxBytes {
		return fmt.Errorf("metadata response too large")
	}
	return json.Unmarshal(data, dst)
}

func reportedSupport(present bool) Support {
	if present {
		return SupportYes
	}
	return SupportNo
}

// https://docs.ollama.com/api-reference/show-model-details
func (o *OllamaProvider) ProfileModel(ctx context.Context, model string) (ModelProfile, error) {
	p := UnknownModelProfile(o.ID(), model)
	p.Source, p.JSONMode = "adapter", SupportYes // API format support, not a quality score.
	if configured := o.contextLimit(); configured > 0 {
		p.ContextTokens, p.ContextSource = configured, "configured"
	}
	body, _ := json.Marshal(map[string]string{"model": model})
	var result struct {
		Capabilities *[]string                  `json:"capabilities"`
		ModelInfo    map[string]json.RawMessage `json:"model_info"`
	}
	if err := readProfileJSON(ctx, o.client, http.MethodPost, strings.TrimRight(o.baseURL, "/")+"/api/show", "", body, &result); err != nil {
		return p, err
	}
	p.Source = "provider_metadata"
	if result.Capabilities != nil {
		p.Chat = reportedSupport(slices.Contains(*result.Capabilities, "completion"))
		p.NativeTools = reportedSupport(slices.Contains(*result.Capabilities, "tools"))
		p.Reasoning = reportedSupport(slices.Contains(*result.Capabilities, "thinking"))
		p.Vision = reportedSupport(slices.Contains(*result.Capabilities, "vision"))
	}
	var architecture string
	_ = json.Unmarshal(result.ModelInfo["general.architecture"], &architecture)
	var contextTokens int
	_ = json.Unmarshal(result.ModelInfo[architecture+".context_length"], &contextTokens)
	if architecture != "" && contextTokens > 0 && contextTokens <= 16*1024*1024 {
		if p.ContextTokens > 0 {
			p.ContextTokens = min(contextTokens, p.ContextTokens)
			p.ContextSource = "provider_and_configured"
		}
		// Without a positive num_ctx the server chooses its serving window.
		// The model's architectural maximum is not evidence of that choice.
	}
	return p, nil
}

// https://ai.google.dev/api/models. These are separate input/output ceilings,
// not a shared context window. Missing capability fields remain unknown.
func (p *GeminiProvider) ProfileModel(ctx context.Context, model string) (ModelProfile, error) {
	profile := UnknownModelProfile(p.ID(), model)
	var result struct {
		Name     string    `json:"name"`
		Input    int       `json:"inputTokenLimit"`
		Output   int       `json:"outputTokenLimit"`
		Methods  *[]string `json:"supportedGenerationMethods"`
		Thinking *bool     `json:"thinking"`
	}
	endpoint := strings.TrimRight(p.baseURL, "/") + "/v1beta/models/" + url.PathEscape(model)
	if err := readProfileJSON(ctx, p.client, http.MethodGet, endpoint, p.apiKey, nil, &result); err != nil {
		return profile, err
	}
	if result.Name != "models/"+model {
		return profile, fmt.Errorf("mismatched model metadata")
	}
	profile.Source, profile.ContextSource = "provider_metadata", "provider_metadata"
	profile.InputTokens, profile.OutputTokens = result.Input, result.Output
	if result.Methods != nil {
		profile.Chat = reportedSupport(slices.Contains(*result.Methods, "generateContent"))
	}
	if result.Thinking != nil {
		profile.Reasoning = reportedSupport(*result.Thinking)
	}
	return profile, nil
}
