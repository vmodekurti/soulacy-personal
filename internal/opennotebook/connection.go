// Package opennotebook contains the small amount of connection metadata that
// Soulacy needs to register the optional Open Notebook MCP adapter. The MCP
// server implementation lives separately in internal/opennotebookmcp.
package opennotebook

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	DefaultBaseURL = "http://127.0.0.1:5055"
	ExecutableName = "open-notebook-mcp"
)

// ValidateBaseURL accepts only a root HTTP(S) URL on a loopback host.
func ValidateBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("the Open Notebook URL must be a valid http:// or https:// URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("the Open Notebook URL must not contain credentials, query parameters, or a fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("the Open Notebook URL must use localhost or a loopback IP address")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// FindExecutable locates the optional standalone adapter. It first honors
// PATH, then checks beside the current executable for daemon environments
// whose PATH is intentionally minimal.
func FindExecutable() (string, error) {
	if found, err := exec.LookPath(ExecutableName); err == nil {
		if absolute, err := filepath.Abs(found); err == nil {
			return absolute, nil
		}
	}
	if self, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(self), ExecutableName)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s is not installed; install the optional standalone adapter and place it on PATH or next to the Soulacy binary", ExecutableName)
}
