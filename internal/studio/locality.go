package studio

import (
	"net"
	"net/url"
	"strings"
)

// Provider locality (local-first pivot). Studio's promise is local-first by
// default, cloud-assisted by choice — so it must know which configured LLM
// providers run ON the user's machine versus which send prompts off-box. This
// classification drives the supportive model-fit notice and the cloud-escalation
// gate (we ask before a prompt leaves the machine).

// localProviderNames are provider IDs that always run locally.
var localProviderNames = map[string]bool{
	"ollama":   true,
	"llamacpp": true,
	"llama":    true,
	"local":    true,
	"lmstudio": true,
}

// cloudProviderNames are provider IDs that are cloud services by default. An
// explicit local base_url still overrides this (e.g. a self-hosted
// OpenAI-compatible server), handled in IsLocalProvider.
var cloudProviderNames = map[string]bool{
	"openai":     true,
	"anthropic":  true,
	"gemini":     true,
	"google":     true,
	"nvidia":     true,
	"groq":       true,
	"mistral":    true,
	"cohere":     true,
	"together":   true,
	"openrouter": true,
}

// IsLocalProvider reports whether a provider+base_url runs on the user's machine.
// Rules, in order:
//  1. A known local provider name (ollama, llamacpp, …) is local.
//  2. Otherwise, if the base_url host is a loopback / private / *.local / the
//     docker host alias, it is local (covers self-hosted OpenAI-compatible
//     servers like LM Studio or llama.cpp behind an openai provider entry).
//  3. A known cloud provider name with no local base_url is cloud.
//  4. Unknown provider with no base_url: treat as cloud (safe default — we'd
//     rather ask before sending than assume local).
func IsLocalProvider(name, baseURL string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if localProviderNames[n] {
		return true
	}
	if hostIsLocal(baseURL) {
		return true
	}
	// A known cloud provider with no local base_url is cloud (rule 3); any
	// unknown provider with no local base_url is treated as cloud too (rule 4,
	// the safe default — we'd rather ask before sending than assume local).
	if cloudProviderNames[n] {
		return false
	}
	return false
}

// ProviderKind returns "local" or "cloud" for display.
func ProviderKind(name, baseURL string) string {
	if IsLocalProvider(name, baseURL) {
		return "local"
	}
	return "cloud"
}

// hostIsLocal reports whether a base_url points at the local machine.
func hostIsLocal(baseURL string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		// base_url may have been given without a scheme; try a loose parse.
		host = strings.TrimSpace(baseURL)
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
	}
	host = strings.ToLower(host)
	switch host {
	case "localhost", "host.docker.internal", "0.0.0.0":
		return true
	}
	if strings.HasSuffix(host, ".local") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	return false
}
