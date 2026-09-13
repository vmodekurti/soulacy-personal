package safeundo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

func cleanLabel(s string, limit int) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) == s && s != "" && len(s) <= limit && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

func Validate(c Config) error {
	if len(c.Resources) > 64 {
		return fmt.Errorf("%w: at most 64 resources", ErrInvalid)
	}
	ids, urls := map[string]bool{}, map[string]bool{}
	for _, r := range c.Resources {
		u, err := url.Parse(r.URL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" || len(r.URL) > 2048 {
			return fmt.Errorf("%w: resource URL must be an exact URL without credentials, query or fragment", ErrInvalid)
		}
		loopback := net.ParseIP(u.Hostname())
		if u.Scheme != "https" && !(u.Scheme == "http" && r.AllowLoopbackHTTP && loopback != nil && loopback.IsLoopback()) {
			return fmt.Errorf("%w: HTTPS required (test-only HTTP requires an explicit loopback IP)", ErrInvalid)
		}
		key := r.AgentID + "/" + r.ID
		if !identifier.MatchString(r.ID) || !identifier.MatchString(r.AgentID) || !cleanLabel(r.Name, 120) || ids[key] || urls[r.URL] || !r.ConditionalWrites {
			return fmt.Errorf("%w: unique resources, names, agent IDs and verified conditional_writes are required", ErrInvalid)
		}
		if r.TokenEnv != "" && !envName.MatchString(r.TokenEnv) {
			return fmt.Errorf("%w: invalid token environment name", ErrInvalid)
		}
		if r.Kind != "json_record" && r.Kind != "webdav_text" {
			return fmt.Errorf("%w: unsupported resource kind", ErrInvalid)
		}
		if (r.Kind == "json_record" && (len(r.Fields) == 0 || len(r.Fields) > MaxFields)) || (r.Kind == "webdav_text" && len(r.Fields) != 0) {
			return fmt.Errorf("%w: JSON resources require an explicit field allowlist", ErrInvalid)
		}
		seen := map[string]bool{}
		for _, field := range r.Fields {
			if !cleanLabel(field, 128) || seen[field] {
				return fmt.Errorf("%w: invalid or duplicate field", ErrInvalid)
			}
			seen[field] = true
		}
		ids[key], urls[r.URL] = true, true
	}
	return nil
}

func binding(r Resource) string {
	fields := append([]string(nil), r.Fields...)
	slices.Sort(fields)
	// Credential rotation is allowed. Changing the target, account variable,
	// field scope or network authority invalidates previously approved plans.
	b, _ := json.Marshal([]any{r.AgentID, r.ID, r.Kind, r.URL, r.TokenEnv, fields, r.AllowPrivateHost, r.AllowLoopbackHTTP, r.ConditionalWrites})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
