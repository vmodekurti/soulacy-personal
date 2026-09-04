package sandbox

import (
	"os"
	"strings"
)

// BaseEnvAllowlist is the set of environment variable NAMES that are always
// passed through to a spawned tool subprocess (SEC-5). These are operationally
// required for almost any program to run correctly but carry no application
// secrets:
//
//   PATH   — command resolution (python3, node, …)
//   HOME   — user home dir; many tools write caches/config here
//   LANG   — locale; avoids mojibake / encoding errors in text tools
//   TMPDIR — temp file location; tools that write scratch files need it
//
// Everything NOT on this list (or NOT explicitly declared via a per-agent
// `env:` allowlist) is withheld — most importantly gateway secrets such as
// ANTHROPIC_API_KEY, OPENAI_API_KEY, database URLs, etc.
var BaseEnvAllowlist = []string{
	"PATH",
	"HOME",
	"LANG",
	"TMPDIR",
}

// FilteredEnv builds the environment slice (KEY=VALUE entries) that a tool
// subprocess should receive. It draws values from parentEnv (typically
// os.Environ()) and keeps only:
//
//   - names in BaseEnvAllowlist, plus
//   - names in allowExtra (the agent's declared `env:` allowlist).
//
// Names with no value present in parentEnv are silently skipped. The result is
// safe to assign to exec.Cmd.Env or to pass to syscall.Exec — secrets the
// gateway holds are not propagated unless explicitly allow-listed.
func FilteredEnv(parentEnv []string, allowExtra []string) []string {
	allow := make(map[string]bool, len(BaseEnvAllowlist)+len(allowExtra))
	for _, k := range BaseEnvAllowlist {
		allow[k] = true
	}
	for _, k := range allowExtra {
		k = strings.TrimSpace(k)
		if k != "" {
			allow[k] = true
		}
	}

	out := make([]string, 0, len(allow))
	for _, kv := range parentEnv {
		// Split on the first '=' only; values may themselves contain '='.
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		if allow[kv[:eq]] {
			out = append(out, kv)
		}
	}
	return out
}

// FilteredEnviron is a convenience wrapper that filters the CURRENT process
// environment. Used at the sandbox execve boundary (wrap_unix.go) as a
// defense-in-depth fallback so a tool process never inherits the gateway's
// full environment even if a caller forgot to set cmd.Env.
func FilteredEnviron(allowExtra []string) []string {
	return FilteredEnv(os.Environ(), allowExtra)
}
