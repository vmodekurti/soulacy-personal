package config

import (
	"strings"

	"github.com/soulacy/soulacy/internal/platform"
)

// ShellGrantWithheldReason explains an allow_system_agents list that this
// deployment refused to honour. Empty when nothing was withheld.
//
// It is a field rather than a log line because the person who needs it is
// usually not reading logs: they set the grant, the agent still says it cannot
// run a command, and the only place they will look is the screen.
var ShellGrantWithheldReason string

// applyManagedPlatformPolicy withholds the shell grant on a managed platform.
//
// allow_system_agents hands an agent shell_exec, run_script, write_file and
// friends. On a laptop that is a considered choice by someone who can open a
// terminal and undo it. On Railway, Fly, Render, Cloud Run or Heroku it is
// three things at once: the gateway is usually reachable from the internet,
// the operator has no shell of their own to repair what an agent breaks, and
// the container is rebuilt from an image they may not control.
//
// So the grant is refused there. It is applied at load, once, rather than at
// each of the four places that read the list — a policy that depends on every
// reader remembering it is a policy that will be forgotten by one of them.
//
// This withholds privilege on a hint (an environment variable the platform
// sets). That is the safe direction for a guess to fail: the worst case is an
// operator on a managed platform who cannot grant shell and is told exactly
// why, rather than an internet-facing agent holding a shell nobody meant to
// give it.
func applyManagedPlatformPolicy(cfg *Config, p platform.Info) {
	ShellGrantWithheldReason = ""
	if cfg == nil || !p.IsManaged() || len(cfg.Runtime.AllowSystemAgents) == 0 {
		return
	}
	withheld := strings.Join(cfg.Runtime.AllowSystemAgents, ", ")
	cfg.Runtime.AllowSystemAgents = nil
	ShellGrantWithheldReason = "allow_system_agents (" + withheld + ") is not honoured on " + p.Name +
		": a managed platform gives you no shell of your own to undo what an agent does, and the gateway is usually reachable from the internet. " +
		"Install Skills and MCP servers with package_install, which needs no grant, or run Soulacy somewhere you own the host."
}
