package mcp

// env.go — what an MCP subprocess is allowed to see of the gateway's
// environment (MU-017 criterion 5: "MCP processes execute within workspace
// isolation and cannot inherit gateway credentials or unrestricted host
// access").
//
// The stdio transport used to build its child environment from os.Environ().
// That handed every MCP server — third-party code, installed by whoever — the
// gateway's entire environment: provider API keys, the static server key,
// database DSNs, cloud credentials picked up from the host. An MCP server is
// exactly the kind of component you install *because* you do not want to write
// it yourself, so "it inherits everything we hold" is the wrong default even
// in a single-tenant deployment, and in a multi-tenant one it is one tenant's
// extension holding another tenant's keys.
//
// So the child environment is an allow-list. What passes is what a process
// needs to run at all — an executable search path, a home directory, locale,
// a temp directory, and the TLS trust store — plus whatever the operator named
// explicitly. Everything else is withheld, and the transport logs how much,
// so a server that genuinely needs a variable fails in a way that names the
// fix instead of mysteriously misbehaving.

import (
	"sort"
	"strings"
)

// baseEnvAllowList is the set of variables every child receives when present.
//
// Deliberately not here: anything matching a credential shape. The list is
// membership-based rather than pattern-based because a deny-list of
// "*_KEY, *_TOKEN, *_SECRET" is a guess about naming, and the one credential
// whose variable is called something else is the one that leaks.
var baseEnvAllowList = []string{
	"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
	"TMPDIR", "TEMP", "TMP",
	// Without the trust store, every HTTPS call from the child fails with a
	// certificate error that looks like a network problem.
	"SSL_CERT_FILE", "SSL_CERT_DIR",
	// Proxies are infrastructure, not credentials — a child that cannot reach
	// the network through the operator's proxy is simply broken.
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
	// Interpreters need their own search paths to find installed packages.
	"NODE_PATH", "PYTHONPATH", "PYTHONHOME",
	// Windows needs these to resolve anything at all.
	"SystemRoot", "COMSPEC", "PATHEXT", "USERPROFILE",
}

// ProcessEnv builds the environment for one MCP subprocess.
//
// base is the parent environment (os.Environ() in production; injected in
// tests). withheld is the count of parent variables that were dropped, for a
// log line that tells an operator this happened without printing names — a
// variable name can itself be a hint about what a deployment holds.
func ProcessEnv(cfg ServerConfig, base []string) (env []string, withheld int) {
	allowed := map[string]bool{}
	for _, name := range baseEnvAllowList {
		allowed[name] = true
	}
	for _, name := range cfg.InheritEnv {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = true
		}
	}

	// Explicit Env wins over anything inherited: an operator who sets a
	// variable for this server means that value, not the gateway's.
	explicit := map[string]bool{}
	for k := range cfg.Env {
		explicit[k] = true
	}

	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || explicit[name] {
			continue
		}
		if cfg.InheritAll || allowed[name] {
			env = append(env, entry)
			continue
		}
		withheld++
	}

	names := make([]string, 0, len(cfg.Env))
	for k := range cfg.Env {
		names = append(names, k)
	}
	sort.Strings(names) // deterministic order keeps the child env reproducible
	for _, k := range names {
		env = append(env, k+"="+cfg.Env[k])
	}
	if cfg.InheritAll {
		withheld = 0
	}
	return env, withheld
}
