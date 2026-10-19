package llm

import (
	"net"
	"net/url"
	"strings"
)

// AllowsUnauthenticatedOpenAIEndpoint permits keyless local model servers. A
// missing cloud API key remains a setup error; the default OpenAI URL never
// qualifies. The same rule applies during startup and live provider setup.
func AllowsUnauthenticatedOpenAIEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
