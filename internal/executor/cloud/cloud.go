// Package cloud resolves named cloud-sandbox presets (Modal, RunPod, Daytona)
// into the wrapper command that the command executor runs Python through. Each
// preset encodes the provider's conventional "execute a Python process in my
// sandbox" invocation; the operator supplies a target (pod id / workspace /
// app) and, where relevant, the CLI must be installed and authenticated on the
// host. Unknown presets return ok=false so the caller can fall back.
package cloud

import "strings"

// Preset returns the wrapper argv for a named cloud provider. target is the
// provider-specific handle (RunPod pod id, Daytona workspace, Modal app/env).
// extraCLI, when non-empty, overrides the default CLI binary name.
func Preset(name, target, cliOverride string) (runner []string, ok bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	target = strings.TrimSpace(target)
	cli := strings.TrimSpace(cliOverride)

	switch name {
	case "runpod":
		bin := orDefault(cli, "runpodctl")
		// runpodctl exec python --pod-id <id> -- ; the executor appends python -c.
		r := []string{bin, "exec", "python"}
		if target != "" {
			r = append(r, "--pod-id", target)
		}
		return append(r, "--"), true
	case "daytona":
		bin := orDefault(cli, "daytona")
		// daytona ssh <workspace> -- ; run a command in the workspace.
		r := []string{bin, "ssh"}
		if target != "" {
			r = append(r, target)
		}
		return append(r, "--"), true
	case "modal":
		bin := orDefault(cli, "modal")
		// modal shell runs a command inside a Modal container image; target is an
		// optional image/app reference.
		r := []string{bin, "shell"}
		if target != "" {
			r = append(r, target)
		}
		return append(r, "--cmd"), true
	default:
		return nil, false
	}
}

// Names returns the supported preset names.
func Names() []string { return []string{"modal", "runpod", "daytona"} }

// IsPreset reports whether name is a known cloud preset.
func IsPreset(name string) bool {
	_, ok := Preset(name, "", "")
	return ok
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
