// engine.go — the agent execution loop.
// The Engine is the heart of Soulacy. It receives a message, assembles the
// full context (system prompt + memory + history + tools), fires the LLM, and
// if the LLM requests tool calls, executes them in a sandboxed Python subprocess
// before re-entering the loop. This continues until the LLM produces a plain
// text response or the max_turns limit is hit.
package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/intent"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/mcp"
	"github.com/soulacy/soulacy/internal/policy"
	"github.com/soulacy/soulacy/internal/sandbox"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// EventSink receives structured events as they happen during agent execution.
func (e *Engine) runTool(ctx context.Context, def *agent.Definition, sessionID string, call message.ToolCall) (string, error) {
	out, err := e.runToolDispatch(ctx, def, sessionID, call)
	if obs := toolObserverFrom(ctx); obs != nil {
		obs(call, out, err != nil)
	}
	return out, err
}

func (e *Engine) runToolDispatch(ctx context.Context, def *agent.Definition, sessionID string, call message.ToolCall) (string, error) {
	if !callerAllowsTool(ctx, call.Name) {
		return "", fmt.Errorf("caller is not permitted to execute tool %q", call.Name)
	}
	// Tool policy runs first so a denied high-risk action (shell/file/network)
	// never reaches any handler, regardless of tool category. Prompt decisions
	// reuse the same confirm channel as the deterministic guardrail.
	if def != nil && def.Policy.Enabled {
		action, reason := policy.Evaluate(policyConfigFor(def), call.Name, call.Arguments)
		switch action {
		case policy.ActionDeny:
			e.log.Warn("policy denied tool execution",
				zap.String("agent", def.ID), zap.String("tool", call.Name), zap.String("reason", reason))
			e.logAudit(ctx, def, call, "", time.Now(), true, nil)
			return "", fmt.Errorf("policy denied execution: %s", reason)
		case policy.ActionPrompt:
			if err := e.dynamicConfirm(ctx, def, call, reason); err != nil {
				return "", err
			}
		}
	}

	// S3 (Cohort F) — tool-call intent gate. Runs after policy so an
	// explicit policy deny short-circuits first, but before every
	// other guard so we can refuse "the last untrusted page told us
	// to shell out" without paying the tool cost. The gate is a
	// no-op for non-high-risk tools and can be disabled with
	// security.intent_gate: off on the agent.
	if evalOK, decision := e.evaluateIntent(def, sessionID, call); evalOK {
		switch decision.Decision {
		case intent.Deny:
			e.log.Warn("intent gate denied tool execution",
				zap.String("agent", agentIDOf(def)),
				zap.String("tool", call.Name),
				zap.String("reason", decision.Reason),
				zap.Bool("injection_influenced", decision.InjectionInfluenced))
			e.logAudit(ctx, def, call, "", time.Now(), true, nil)
			e.emitIntentDecision(ctx, agentIDOf(def), sessionID, call, decision)
			return "", fmt.Errorf("intent gate denied execution: %s", decision.Reason)
		case intent.Prompt:
			e.emitIntentDecision(ctx, agentIDOf(def), sessionID, call, decision)
			if err := e.dynamicConfirm(ctx, def, call, decision.Reason); err != nil {
				return "", err
			}
		case intent.Allow:
			// No-op — record the decision only if injection was
			// influential, so the trace shows the gate ran and let it
			// through. Everyday allows stay silent to keep the log lean.
			if decision.InjectionInfluenced {
				e.emitIntentDecision(ctx, agentIDOf(def), sessionID, call, decision)
			}
		}
	}

	// Dry-run: simulate side-effecting tool calls (shell/file-write/network/
	// MCP/plugin) instead of executing them. Read-only tools still run so the
	// agent can gather context. Applies when the agent opts in OR the request does.
	if (def != nil && def.DryRun) || dryRunFrom(ctx) {
		if isSideEffectingTool(call.Name) {
			result := dryRunResult(call)
			e.logAudit(ctx, def, call, result, time.Now(), false, nil)
			return result, nil
		}
	}

	// MU-021 criterion 6. A worker that dies mid-run leaves a record in
	// `running`, and whether that run may be retried depends on one fact: did
	// it already do something the outside world can see? Recorded HERE, at the
	// single dispatch point, and BEFORE the call rather than after — a tool
	// that starts a transfer and then times out has still made the call.
	//
	// After the dry-run branch above on purpose: a dry run returns the
	// simulation without executing, so it is not a side effect and must not
	// make an otherwise-retryable run unretryable.
	if isSideEffectingTool(call.Name) {
		e.recordSideEffect(ctx, call.Name)
	}

	// MCP tools — namespaced as mcp__<server>__<tool>. Route to the MCP client.
	if client := e.mcpFor(ctx); client != nil && strings.HasPrefix(call.Name, mcp.FullNamePrefix) {
		if !mcpToolAllowed(def, call.Name) {
			return "", fmt.Errorf("MCP tool %q is not allowed for agent %q", call.Name, def.ID)
		}
		tctx, cancel := context.WithTimeout(ctx, e.effectiveToolTimeout(ctx))
		defer cancel()
		out, callErr := client.Call(tctx, call.Name, call.Arguments)
		return out, toolTimeoutError(call.Name, e.toolTimeout, ctx.Err(), callErr)
	}

	// Plugin tools — namespaced as plugin__<pluginID>__<tool>. Execute as a
	// Python subprocess using the handler path from the plugin manifest. In
	// Team/Scale the source is copied into the isolated worker request; the
	// worker is never given a gateway-host path to open.
	if provider := e.plugins(ctx); strings.HasPrefix(call.Name, "plugin__") && provider != nil {
		if !pluginToolAllowed(def, call.Name) {
			return "", fmt.Errorf("plugin tool %q is not explicitly granted to agent %q", call.Name, def.ID)
		}
		for _, pt := range provider.AllTools() {
			if pt.Name != call.Name {
				continue
			}
			if !strings.HasPrefix(pt.Handler, "python:") {
				return "", fmt.Errorf("plugin tool %q: unsupported handler scheme %q", call.Name, pt.Handler)
			}
			rest := strings.TrimPrefix(pt.Handler, "python:")
			parts := strings.SplitN(rest, "::", 2)
			if len(parts) != 2 {
				return "", fmt.Errorf("plugin tool %q: malformed handler %q", call.Name, pt.Handler)
			}
			pyFile, funcName := parts[0], parts[1]
			argsJSON, _ := json.Marshal(call.Arguments)
			script := fmt.Sprintf(`
import sys as _sys, json, importlib.util
# Redirect stdout → stderr so any print() inside the tool code does not
# corrupt the JSON result we write at the very end.
_orig_stdout = _sys.stdout
_sys.stdout = _sys.stderr
args = json.loads(_sys.stdin.read())
spec = importlib.util.spec_from_file_location("tool", %q)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
result = getattr(mod, %q)(**args)
_sys.stdout = _orig_stdout
print(result if isinstance(result, str) else json.dumps(result))
`, pyFile, funcName)
			if e.requireIsolatedExecutor {
				source, readErr := os.ReadFile(pyFile)
				if readErr != nil {
					return "", fmt.Errorf("plugin tool %q: read isolated source: %w", call.Name, readErr)
				}
				script = embeddedPythonModule(source, pyFile, funcName)
				if e.pyExecutor == nil {
					return "", fmt.Errorf("plugin tool %q: isolated execution worker is unavailable", call.Name)
				}
				tctx, cancel := context.WithTimeout(ctx, e.effectiveToolTimeout(ctx))
				defer cancel()
				out, runErr := e.pyExecutor.Run(tctx, "", "", script, argsJSON)
				if runErr != nil {
					return "", fmt.Errorf("plugin tool %q (isolated worker): %w", call.Name, runErr)
				}
				return strings.TrimSpace(out), nil
			}

			// SEC-5: scrub env to base allowlist + agent-declared names.
			limits := e.sandboxLimits
			limits.EnvAllow = def.Env
			argv := sandbox.Wrap(e.selfPath, limits, []string{e.pythonBin, "-c", script})
			tctx, cancel := context.WithTimeout(ctx, e.effectiveToolTimeout(ctx))
			defer cancel()
			workDir, wdErr := e.toolWorkDir(ctx)
			if wdErr != nil {
				return "", fmt.Errorf("plugin tool %q: %w", call.Name, wdErr)
			}
			cmd := exec.CommandContext(tctx, argv[0], argv[1:]...)
			cmd.Dir = workDir
			cmd.Stdin = bytes.NewReader(argsJSON)
			cmd.Env = sandbox.FilteredEnv(os.Environ(), def.Env)
			out, err := cmd.Output()
			if err != nil {
				return "", fmt.Errorf("plugin tool %q: %w", call.Name, err)
			}
			return strings.TrimSpace(string(out)), nil
		}
		return "", fmt.Errorf("plugin tool %q not found in any loaded plugin", call.Name)
	}

	// Peer-agent tools — namespaced as agent__<peer-id>. Route through Handle
	// on a fresh sub-session. NOTE: no e.toolTimeout wrap here — the sub-agent's
	// own RunTimeout (via its caller chain) bounds it. The parent's context
	// deadline also still applies.
	if strings.HasPrefix(call.Name, AgentToolPrefix) {
		return e.runAgentCall(ctx, def, call.Name, call.Arguments)
	}

	// Check built-in Go tools first (read_skill, read_skill_file, etc.)
	for _, b := range e.builtins {
		if b.Name != call.Name {
			continue
		}

		// Confirmation gate: pause and ask the user before executing tools
		// that are listed in def.ConfirmTools (or "*" for all built-ins).
		if err := e.maybeConfirm(ctx, def, call); err != nil {
			return "", err
		}

		tstart := time.Now()
		tctx, cancel := context.WithTimeout(ctx, e.effectiveToolTimeout(ctx))
		defer cancel()
		var result string
		var err error
		if requiresPrivilegedIsolation(b.Name) {
			result, err = e.executePrivilegedBuiltin(tctx, b.Name, func(runCtx context.Context) (string, error) {
				return b.Handler(runCtx, call.Arguments)
			})
		} else {
			result, err = b.Handler(tctx, call.Arguments)
		}

		// Audit log every built-in call.
		e.logAudit(ctx, def, call, result, tstart, false, err)

		return result, err
	}

	// Check system tools (SEC-3 partition). systemToolsFor returns the SAFE
	// (read-only) built-ins unconditionally, and the privileged SYSTEM
	// built-ins (shell_exec, run_script, install_library, write_file,
	// download_file) only when the server permits system tools AND the agent
	// holds the "system" capability. A privileged tool call from an agent
	// without the capability therefore falls through to "tool not defined".
	for _, b := range e.systemToolsFor(def) {
		if b.Name != call.Name {
			continue
		}

		// Deterministic path-based guardrail for privileged system tools
		guardrailConfirmed := false
		if isPrivilegedSystemTool(b.Name) {
			action, reason, err := e.deterministicGuardrail(ctx, def, sessionID, call)
			if err != nil {
				return "", err
			}
			if action == GuardrailActionDeny {
				e.log.Warn("guardrail denied tool execution", zap.String("tool", call.Name), zap.String("reason", reason))
				return "", fmt.Errorf("guardrail denied execution: %s", reason)
			} else if action == GuardrailActionConfirm {
				if err := e.dynamicConfirm(ctx, def, call, reason); err != nil {
					return "", err
				}
				// The guardrail already obtained an explicit user approval for
				// this exact call. Skip the static ConfirmTools gate below so the
				// operator isn't prompted twice for one tool call: the two gates
				// mint independent call_ids, and the second prompt's pending
				// approval would otherwise never be resolved, hanging the run.
				guardrailConfirmed = true
			}
		}

		// Confirmation gate: pause and ask the user before executing tools that
		// are listed in def.ConfirmTools (or "*" for all built-ins). Skipped when
		// the guardrail above already confirmed this exact call.
		if !guardrailConfirmed {
			if err := e.maybeConfirm(ctx, def, call); err != nil {
				return "", err
			}
		}

		tstart := time.Now()
		tctx, cancel := context.WithTimeout(ctx, e.effectiveToolTimeout(ctx))
		defer cancel()
		var result string
		var err error
		if requiresPrivilegedIsolation(b.Name) {
			result, err = e.executePrivilegedBuiltin(tctx, b.Name, func(runCtx context.Context) (string, error) {
				return b.Handler(runCtx, call.Arguments)
			})
		} else {
			result, err = b.Handler(tctx, call.Arguments)
		}

		// Audit log every built-in call.
		e.logAudit(ctx, def, call, result, tstart, false, err)

		return result, err
	}

	// Find the agent's Python tool definition
	var toolDef *agent.ToolDef
	for i := range def.Tools {
		if def.Tools[i].Name == call.Name {
			toolDef = &def.Tools[i]
			break
		}
	}
	if toolDef == nil {
		if isPrivilegedSystemTool(call.Name) {
			return "", fmt.Errorf("tool %q requires the 'system' capability in the agent's SOUL.yaml and server-level authorization (allow_system_agents)", call.Name)
		}
		return "", fmt.Errorf("tool %q not defined in agent %q", call.Name, def.ID)
	}

	// Serialize arguments to pass as JSON via stdin
	argsJSON, _ := json.Marshal(call.Arguments)

	// Build a tiny Python bootstrap that imports the tool file and calls the function
	var script string
	if toolDef.Inline != "" {
		script = toolDef.Inline
	} else if toolDef.PythonFile != "" {
		// Expand a leading ~ to the home directory — Python's importlib does NOT
		// do this, so an unexpanded "~/..." path would fail to load.
		pyFile := toolDef.PythonFile
		if strings.HasPrefix(pyFile, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				pyFile = filepath.Join(home, pyFile[2:])
			}
		}
		// Privilege boundary: reject paths outside the configured allowlist.
		// This prevents a crafted SOUL.yaml from executing arbitrary host files.
		// The check is skipped when AllowedToolDirs is empty (default single-user
		// mode where all SOUL.yaml authors are already trusted operators).
		if len(e.allowedToolDirs) > 0 {
			clean := filepath.Clean(pyFile)
			allowed := false
			for _, dir := range e.allowedToolDirs {
				prefix := filepath.Clean(dir) + string(filepath.Separator)
				if strings.HasPrefix(clean, prefix) || clean == filepath.Clean(dir) {
					allowed = true
					break
				}
			}
			if !allowed {
				return "", fmt.Errorf(
					"tool %q: python_file %q is outside the configured allowed_tool_dirs — "+
						"update runtime.allowed_tool_dirs in config.yaml to permit this path",
					call.Name, pyFile,
				)
			}
		}
		if e.requireIsolatedExecutor {
			source, readErr := os.ReadFile(pyFile)
			if readErr != nil {
				return "", fmt.Errorf("tool %q: read isolated source: %w", call.Name, readErr)
			}
			script = embeddedPythonModule(source, pyFile, call.Name)
		} else {
			script = fmt.Sprintf(`
import sys as _sys, json, importlib.util
# Redirect stdout → stderr so any print() inside the tool code does not
# corrupt the JSON result we write at the very end.
_orig_stdout = _sys.stdout
_sys.stdout = _sys.stderr
args = json.loads(_sys.stdin.read())
spec = importlib.util.spec_from_file_location("tool", %q)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
result = getattr(mod, %q)(**args)
_sys.stdout = _orig_stdout
print(result if isinstance(result, str) else json.dumps(result))
`, pyFile, call.Name)
		}
	} else {
		return "", fmt.Errorf("tool %q has neither python_file nor inline", call.Name)
	}

	// Timeout precedence (most specific wins): a per-NODE override (FlowNode.Timeout,
	// carried on the context) beats a per-TOOL timeout (toolDef.Timeout, e.g. "30m"),
	// which beats the global runtime.tool_timeout. This lets a developer fix one slow
	// block — a notebooklm audio/research poll, a large export — without weakening the
	// global safety net for every other node.
	timeout := e.toolTimeout
	if toolDef.Timeout != "" {
		if d, perr := time.ParseDuration(toolDef.Timeout); perr == nil && d > 0 {
			timeout = d
		} else {
			e.log.Warn("tool: invalid timeout, using global default",
				zap.String("tool", call.Name),
				zap.String("timeout", toolDef.Timeout),
			)
		}
	}
	if d, ok := toolTimeoutOverride(ctx); ok {
		timeout = d // the node's own budget is the most specific — it wins
	}
	retries := pythonToolRetries(toolDef)
	backoff := pythonToolRetryBackoff(toolDef)
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
			e.emit(ctx, message.Event{
				Type:      "tool.log",
				AgentID:   def.ID,
				SessionID: sessionID,
				Payload: map[string]any{
					"call_id": call.ID,
					"name":    call.Name,
					"line":    fmt.Sprintf("retrying after failure (attempt %d of %d)", attempt+1, retries+1),
				},
				Timestamp: time.Now().UTC(),
			})
		}

		tctx, cancel := context.WithTimeout(ctx, timeout)
		out, err := e.runPythonToolOnce(tctx, ctx, def, sessionID, call, script, argsJSON)
		cancel()
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	return "", lastErr
}

func (e *Engine) runPythonToolOnce(tctx, auditCtx context.Context, def *agent.Definition, sessionID string, call message.ToolCall, script string, argsJSON []byte) (string, error) {
	if e.requireIsolatedExecutor {
		if e.pyExecutor == nil {
			return "", fmt.Errorf("tool %q: isolated execution worker is unavailable", call.Name)
		}
		if requested := strings.ToLower(strings.TrimSpace(def.Execution.Backend)); requested != "" && requested != "worker" {
			return "", fmt.Errorf("tool %q: execution backend %q is not permitted in a multi-user deployment", call.Name, def.Execution.Backend)
		}
		tstart := time.Now()
		out, runErr := e.pyExecutor.Run(tctx, "", "", script, argsJSON)
		e.logAudit(auditCtx, def, call, out, tstart, false, runErr)
		if runErr != nil {
			return "", fmt.Errorf("tool %q (isolated worker): %w", call.Name, runErr)
		}
		return strings.TrimSpace(out), nil
	}

	// Per-agent execution backend: when the agent explicitly selected a
	// registered non-default backend (docker/ssh/…), run the tool's python
	// through it instead of the local sandboxed subprocess. `script` is already
	// a complete, self-contained program (import bootstrap for file tools, or
	// the inline source) that reads JSON args from stdin and prints its result,
	// so it maps directly onto the backend's inline entrypoint. Agents that
	// leave `execution.backend` unset keep the byte-for-byte local path below.
	if be := e.selectedNamedBackend(def); be != nil {
		tstart := time.Now()
		out, rerr := be.Run(tctx, "", "", script, argsJSON)
		e.logAudit(auditCtx, def, call, out, tstart, false, rerr)
		if rerr != nil {
			return "", fmt.Errorf("tool %q (%s backend): %w", call.Name, def.Execution.Backend, rerr)
		}
		return strings.TrimSpace(out), nil
	}

	// The default Personal configuration uses the same disposable OCI boundary
	// as privileged builtins. Team/Scale returned through the mandatory worker
	// branch above; this closes the last default path that could otherwise run
	// tenant-authored Python beside the gateway with only POSIX rlimits.
	if e.privilegedRunner != nil && e.privilegedRunner.Mode() == "docker" {
		workDir, wdErr := e.toolWorkDir(tctx)
		if wdErr != nil {
			return "", wdErr
		}
		pythonBin := filepath.Base(strings.TrimSpace(e.pythonBin))
		if pythonBin == "" || pythonBin == "." {
			pythonBin = "python3"
		}
		tstart := time.Now()
		out, runErr := e.runPrivilegedCommand(tctx, PrivilegedCommand{
			Argv:       []string{pythonBin, "-c", script},
			WorkingDir: workDir,
			Env:        sandbox.FilteredEnv(os.Environ(), def.Env),
			Stdin:      argsJSON,
		}, 0)
		e.logAudit(auditCtx, def, call, out, tstart, false, runErr)
		if runErr != nil {
			return "", fmt.Errorf("tool %q (docker sandbox): %w", call.Name, runErr)
		}
		return strings.TrimSpace(out), nil
	}

	// PRODUCTION_AUDIT → F1 (2026-05-27): wrap the python invocation in
	// the soulacy __exec-sandbox subcommand to apply CPU/memory/FD/file
	// caps before execve. When sandboxing is disabled OR we couldn't
	// resolve our own binary path at boot, sandbox.Wrap returns the
	// original argv unchanged — the engine doesn't have to branch.
	//
	// SEC-5: carry the agent's declared env allowlist into the sandbox wrapper
	// (--env= flags) AND set cmd.Env directly so the non-sandboxed path is also
	// scrubbed. Either way the tool sees only BaseEnvAllowlist + def.Env, never
	// the gateway's full environment.
	limits := e.sandboxLimits
	limits.EnvAllow = def.Env
	argv := sandbox.Wrap(e.selfPath, limits, []string{e.pythonBin, "-c", script})
	workDir, wdErr := e.toolWorkDir(tctx)
	if wdErr != nil {
		return "", wdErr
	}
	cmd := exec.CommandContext(tctx, argv[0], argv[1:]...)
	cmd.Dir = workDir
	cmd.Stdin = bytes.NewReader(argsJSON)
	cmd.Env = sandbox.FilteredEnv(os.Environ(), def.Env)

	// Stream stderr line-by-line into the actionlog as `tool.log` events so
	// the GUI/CLI can see long-running tools make progress instead of
	// silence-until-completion. Python tools just need to write progress to
	// sys.stderr (with flush) — every line surfaces as one log row in the
	// trace. Stdout remains buffered (it's the tool's return value).
	// (Observed 2026-05-28: ai_daily_pipeline took 10+ min producing zero
	// log rows mid-run; you could watch the agent work in NotebookLM but
	// nothing reached the actionlog. Fixed by piping stderr through here.)
	//
	// On error path, we also keep the last few stderr lines for the LLM-
	// visible error message — same UX as before, just rebuilt from the
	// streamed lines instead of a buffer.
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("tool execution: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("tool execution: start: %w", err)
	}

	// Reader goroutine. Each non-empty stderr line becomes one tool.log
	// event keyed by the tool name + call ID, so the action-log timeline
	// shows the right ordering. Bounded buffer for the LLM-visible error
	// summary — we keep the last 32 lines, plenty for a stacktrace.
	const tailKeepLines = 32
	var tailMu sync.Mutex
	var tailLines []string
	pipeDone := make(chan struct{})
	go func() {
		defer close(pipeDone)
		sc := bufio.NewScanner(stderrPipe)
		// Long lines (full tracebacks) still get one event each. Cap at 64
		// KiB per line so a runaway tool can't memory-bomb us.
		sc.Buffer(make([]byte, 4096), 64*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			tailMu.Lock()
			tailLines = append(tailLines, line)
			if len(tailLines) > tailKeepLines {
				tailLines = tailLines[len(tailLines)-tailKeepLines:]
			}
			tailMu.Unlock()
			e.emit(auditCtx, message.Event{
				Type: "tool.log", AgentID: def.ID, SessionID: sessionID,
				Payload: map[string]any{
					"call_id": call.ID,
					"name":    call.Name,
					"line":    line,
				},
				Timestamp: time.Now().UTC(),
			})
		}
	}()

	// CRITICAL: drain the stderr pipe BEFORE calling cmd.Wait. Go's os/exec
	// closes the pipe inside Wait, which interrupts any in-progress reads.
	// We wait for the reader goroutine to see natural EOF (which happens
	// when the child process exits and the kernel closes the write end of
	// the pipe), then reap the process. This pattern is documented in the
	// os/exec docs explicitly: "it is incorrect to call Wait before all
	// reads from the pipe have completed." A run-tool first hand-wired
	// this in the wrong order on 2026-05-28 — symptom: zero tool.log
	// events emitted despite the python script flushing stderr correctly.
	<-pipeDone
	runErr := cmd.Wait()

	if runErr != nil {
		tailMu.Lock()
		errMsg := strings.TrimSpace(strings.Join(tailLines, "\n"))
		tailMu.Unlock()
		if errMsg == "" {
			errMsg = runErr.Error()
		}
		if len(errMsg) > 4000 {
			errMsg = errMsg[len(errMsg)-4000:]
		}
		return "", fmt.Errorf("tool execution failed (%v): %s", runErr, errMsg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// embeddedPythonModule makes a Python file self-contained for an execution
// worker that intentionally has no access to the gateway filesystem. Base64
// avoids turning source text into a quoted-code injection problem.
func embeddedPythonModule(source []byte, filename, function string) string {
	return fmt.Sprintf(`
import sys as _sys, json, base64, types
_orig_stdout = _sys.stdout
_sys.stdout = _sys.stderr
args = json.loads(_sys.stdin.read())
mod = types.ModuleType("tool")
mod.__file__ = %q
exec(compile(base64.b64decode(%q), %q, "exec"), mod.__dict__)
result = getattr(mod, %q)(**args)
_sys.stdout = _orig_stdout
print(result if isinstance(result, str) else json.dumps(result))
`, filename, base64.StdEncoding.EncodeToString(source), filename, function)
}

// allToolSchemas combines the agent's Python tools with the engine's built-in
// Go tools. The skill built-ins (read_skill, read_skill_file) are only offered
// when the agent has opted into skills (def.Skills non-empty), so agents that
// don't use skills aren't tempted to call them.
//
// channel is the inbound message's Channel field ("http", "telegram", etc.).
// System tools (shell_exec, run_script, …) are only offered when ALL three
// conditions hold:
//  1. runtime.allow_system_tools = true  (server-level permit)
//  2. def.SystemTools = true             (per-agent opt-in)
//  3. channel == "http"                  (local web GUI only — never on bot channels)
func (e *Engine) allToolSchemas(def *agent.Definition, channel string) []llm.ToolSchema {
	return e.allToolSchemasForContext(context.Background(), def, channel)
}

func (e *Engine) allToolSchemasForContext(ctx context.Context, def *agent.Definition, channel string) []llm.ToolSchema {
	schemas := make([]llm.ToolSchema, 0, len(def.Tools)+len(e.builtins))

	// Python tools defined in the agent's SOUL.yaml
	for _, t := range def.Tools {
		if !callerAllowsTool(ctx, t.Name) {
			continue
		}
		schemas = append(schemas, llm.ToolSchema{
			Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		})
	}

	// Built-in Go tools, gated by capability AND optionally by the agent's
	// `builtins:` allowlist:
	//   - def.Builtins == nil           → default gating only (back-compat)
	//   - def.Builtins == &[]           → NO built-ins (peer-only orchestrator)
	//   - def.Builtins == &[names…]     → only those names (still subject to gate)
	//   - def.Builtins == &["*"/"all"]  → same as nil (all gated built-ins)
	// Gates themselves (per BuiltinTool.Gate):
	//   - ""          always offered (e.g. web_search — provider-agnostic)
	//   - "skills"    only when the agent opted into skills (def.Skills)
	//   - "knowledge" only when the agent declared at least one KB (def.Knowledge)
	var allow map[string]bool
	wildcardBuiltins := def.Builtins == nil
	if def.Builtins != nil {
		allow = make(map[string]bool, len(*def.Builtins))
		for _, n := range *def.Builtins {
			if n == "*" || n == "all" {
				wildcardBuiltins = true
				continue
			}
			allow[n] = true
		}
	}
	for _, b := range e.builtins {
		if !callerAllowsTool(ctx, b.Name) {
			continue
		}
		// Allowlist filter first (cheap reject).
		if !wildcardBuiltins && !allow[b.Name] {
			continue
		}
		switch b.Gate {
		case "skills":
			if len(e.effectiveSkillNames(ctx, def)) == 0 {
				continue
			}
		case "knowledge":
			if len(def.Knowledge) == 0 {
				continue
			}
		}
		schemas = append(schemas, llm.ToolSchema{
			Name: b.Name, Description: b.Description, Parameters: b.Parameters,
		})
	}

	// MCP tools from connected servers are offered according to the agent's
	// mcp_servers / mcp_tools allowlists. For backwards compatibility, agents
	// that omit both fields still see every connected MCP tool.
	if client := e.mcpFor(ctx); client != nil {
		for _, t := range client.AllTools() {
			if !mcpToolAllowed(def, t.FullName()) || !callerAllowsTool(ctx, t.FullName()) {
				continue
			}
			schemas = append(schemas, llm.ToolSchema{
				Name:        t.FullName(),
				Description: t.Description,
				Parameters:  t.InputSchema,
			})
		}
	}

	// Plugin tools from installed plugins (namespaced as plugin__<id>__<tool>).
	if provider := e.plugins(ctx); provider != nil {
		for _, pt := range provider.AllTools() {
			if !pluginToolAllowed(def, pt.Name) || !callerAllowsTool(ctx, pt.Name) {
				continue
			}
			schemas = append(schemas, llm.ToolSchema{
				Name:        pt.Name,
				Description: pt.Description,
				Parameters:  pt.Parameters,
			})
		}
	}

	// System tools (SEC-3 partition). Bot channels (telegram, discord, slack,
	// whatsapp) are ALWAYS excluded — only the local HTTP/web channel may use
	// OS-level built-ins, and this cannot be overridden by agent config alone.
	//
	// Within the http channel, systemToolsFor applies the SEC-3 gating:
	//   - SAFE (read-only) built-ins — read_file, list_dir, find_files,
	//     fetch_url, http_request, env_get, sys_info — are always offered.
	//   - SYSTEM (privileged) built-ins — shell_exec, run_script,
	//     install_library, write_file, download_file — are offered ONLY when
	//     the server permits (runtime.allow_system_tools) AND the agent
	//     declares the "system" capability (capabilities: [system], or the
	//     legacy system_tools: true alias).
	//
	// An explicit `builtins: []` (peer-only orchestrator) suppresses the
	// ambient SAFE system tools too — an agent that opted out of ALL Go-native
	// built-ins should not be handed read_file/list_dir/etc. behind its back.
	// A PRIVILEGED tool, by contrast, is only ever present when the agent made
	// a deliberate `capabilities: [system]` (or system_tools) grant, so it is
	// NOT suppressed by builtins: [] — the explicit privileged opt-in wins.
	// A named allowlist (`builtins: [read_file]`) admits only those names.
	suppressSafe := def.Builtins != nil && len(*def.Builtins) == 0
	if channel == "http" {
		for _, st := range e.systemToolsFor(def) {
			if !callerAllowsTool(ctx, st.Name) {
				continue
			}
			priv := isPrivilegedSystemTool(st.Name)
			if !priv {
				// SAFE tool: respect builtins: [] and any named allowlist.
				if suppressSafe || (!wildcardBuiltins && !allow[st.Name]) {
					continue
				}
			}
			schemas = append(schemas, llm.ToolSchema{
				Name:        st.Name,
				Description: st.Description,
				Parameters:  st.Parameters,
			})
		}
	}

	// Peer agents exposed as tools (namespaced as agent__<id>). Built
	// dynamically because each parent agent gets a DIFFERENT subset of peers
	// depending on its def.Agents list, so we can't preregister them in
	// e.builtins like the other tools.
	schemas = append(schemas, e.buildAgentCallSchemas(def)...)

	return schemas
}

// mcpToolAllowed reports whether an agent may see/call a namespaced MCP tool.
//
// MCP is default-deny: a tool must match an explicit server or full-name grant.
func mcpToolAllowed(def *agent.Definition, fullName string) bool {
	if def == nil {
		return false
	}
	if def.MCPServers == nil && def.MCPTools == nil {
		return false
	}
	serverID, ok := mcpServerFromFullName(fullName)
	if !ok {
		return false
	}
	if allowMCPServer(def.MCPServers, serverID) {
		return true
	}
	return allowMCPTool(def.MCPTools, fullName)
}

func pluginToolAllowed(def *agent.Definition, fullName string) bool {
	if def == nil || def.PluginTools == nil {
		return false
	}
	for _, allowed := range *def.PluginTools {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || allowed == "all" || allowed == fullName {
			return true
		}
	}
	return false
}

func mcpServerFromFullName(fullName string) (string, bool) {
	if !strings.HasPrefix(fullName, mcp.FullNamePrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(fullName, mcp.FullNamePrefix)
	parts := strings.SplitN(rest, "__", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0], true
}

func allowMCPServer(allowlist *[]string, serverID string) bool {
	if allowlist == nil {
		return false
	}
	for _, allowed := range *allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || allowed == "all" {
			return true
		}
		if sanitizeMCPID(allowed) == serverID {
			return true
		}
	}
	return false
}

func allowMCPTool(allowlist *[]string, fullName string) bool {
	if allowlist == nil {
		return false
	}
	for _, allowed := range *allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || allowed == "all" {
			return true
		}
		if allowed == fullName {
			return true
		}
	}
	return false
}

func sanitizeMCPID(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, s)
}

// --- Multi-agent (agent-as-tool) ----------------------------------------------
//
// An agent whose SOUL.yaml lists `agents: [other-id, ...]` can invoke each of
// those peers as a tool named `agent__<id>`. The tool's `message` argument is
// delivered to the peer as the inbound user message; the peer runs its own
// loop (including its own tool/skill/KB usage) and its final reply text is
// returned to the caller as the tool result.

// AgentToolPrefix namespaces peer-agent tools so they're unmistakable in the
// tool catalog and tool-call routing.
const AgentToolPrefix = "agent__"
