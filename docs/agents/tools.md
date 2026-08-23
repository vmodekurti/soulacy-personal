# Agent Tools

Tools give your agent hands: Python scripts you write, Go-native built-ins, MCP tools, and peer agents, all exposed through one catalog the model picks from.

## Quick Start

```yaml title="agents/weather/SOUL.yaml (excerpt)"
tools:
  - name: get_weather
    description: Fetch the current weather for a location.
    python_file: tools/get_weather.py
    timeout: 30s
    retries: 1
    retry_backoff: 1s
    parameters:
      type: object
      properties:
        location: { type: string }
      required: [location]
```

```python title="tools/get_weather.py"
def get_weather(location):
    # do work, return a string or anything JSON-serializable
    return {"location": location, "temp_c": 21}
```

The runtime builds each agent's catalog from five sources — Python tools from
`SOUL.yaml`, Go-native built-ins, MCP tools, peer agents (`agent__<id>`), and
plugin tools. The model only ever sees tools admitted by the agent definition
and gateway config.

## Python Tools

Each entry under `tools:` needs:

| Field | Notes |
|-------|-------|
| `name` | Tool name (snake_case). Must match a **function of the same name** in the Python file. |
| `description` | What the LLM reads to decide when to call it. Be specific. |
| `python_file` | Path to the script. Relative paths resolve from the `SOUL.yaml` directory; `~/` expands to your home directory. |
| `inline` | Alternative to `python_file`: a Python script embedded in YAML. |
| `parameters` | JSON Schema object describing the arguments. |
| `timeout` | Per-tool override of the global `runtime.tool_timeout`. Go duration syntax: `30s`, `5m`, `1h`. |
| `retries` | Number of extra attempts after a Python tool fails. Defaults to `0`; capped at `5`. |
| `retry_backoff` | Wait before each retry. Defaults to `1s`; capped at `30s`. |

How a call executes:

1. The engine serializes the model's arguments to JSON and passes them on
   **stdin**; your function is called as `get_weather(**args)`.
2. The function's return value becomes the tool result — strings pass through,
   anything else is JSON-encoded.
3. `print()` inside your tool goes to stderr, not the result — and every
   stderr line streams live into the Activity log as a `tool.log` event, so
   long-running tools can report progress with
   `print("step 2/5…", file=sys.stderr, flush=True)`.

!!! tip
    Set `timeout` per tool instead of weakening the global default. A
    NotebookLM export that legitimately takes 20 minutes gets `timeout: 30m`;
    everything else keeps the safety net.

!!! tip
    Use `retries` only for idempotent, transient work such as fetching a URL,
    parsing a flaky API response, or reading a remote document. Do not retry
    tools that send messages, write durable records, place orders, or perform
    any side effect that would be harmful if repeated.

## The Sandbox

In Team and Scale, Python tools are submitted to the remote execution plane and
run in a digest-pinned, signature-verified container under a hardened OCI
runtime. The gateway refuses local fallback. Privileged built-ins use a fresh
read-only, unnetworked container for each call.

Personal mode also uses a disposable Docker container by default. Only the
explicit Personal-only `runtime.sandbox.mode: unsandboxed` escape hatch uses
the local compatibility guard, where Soulacy re-execs itself as a hidden
wrapper that applies syscall-level rlimits. Default caps for that guard:

| Limit | Default |
|-------|---------|
| CPU time | 30 s |
| Memory (address space) | 512 MB |
| Open file descriptors | 256 |
| Largest single file written | 64 MB |

The local guard is not a tenant boundary and is unavailable as a Team/Scale
execution backend. Operators can tune or disable it only for Personal mode.

## Built-ins

`builtins` controls which Go-native tools the engine offers:

| YAML | Mode |
|------|------|
| Field absent | **Default** — gated built-ins are auto-injected when their prerequisites are met. |
| `builtins: []` | **None** — no built-ins. Right choice for peer-only orchestrators. |
| `builtins: [web_search, kb_search]` | **Restricted** — only the listed built-ins, still subject to their gates. |
| `builtins: ["*"]` or `["all"]` | Same as default, written explicitly. |

Common built-ins and their gates:

| Tool | Gate |
|------|------|
| `web_search` | Web search API key configured. |
| `kb_search` | Agent declares `knowledge:`. |
| `kb_write` | Agent declares `knowledge:`. |
| `queue_create`, `queue_names`, `queue_put`, `queue_take`, `queue_list`, `queue_clear` | Always available unless `builtins` restricts them. |
| `channel.send` | Channel registry configured. |
| `read_skill`, `read_skill_file` | Agent declares `skills:`. |
| `shell_exec`, `run_script`, `read_file`, `write_file`, `list_dir`, `install_library` | System tools — double opt-in (below). |

### Ephemeral Queues

Use the queue built-ins when an agent or Studio workflow needs temporary
handoff state but should not receive filesystem access:

- `queue_create` creates a named queue.
- `queue_names` lists current queues and item counts.
- `queue_put` stores a JSON value in memory.
- `queue_take` returns and removes the oldest item.
- `queue_list` inspects queued items without removing them.
- `queue_clear` deletes all items in a queue.

Queues are created automatically by `queue_put`, so `queue_create` is optional.
If a queue name is omitted, Soulacy uses the `default` queue. Queues are
process-local, bounded, and expire items automatically. They are not persisted
across gateway restarts. For durable searchable content, use `kb_write`; for
generated files, use a deliberately scoped filesystem or artifact tool rather
than broad `write_file` access.

Example restricted agent:

```yaml
builtins:
  - queue_create
  - queue_names
  - queue_put
  - queue_take
  - queue_list
  - queue_clear
```

Typical handoff:

```json
{"queue":"pending_docs","item":{"url":"https://example.com/doc","tags":["governance"]}}
```

Then a downstream step calls `queue_take` with `{"queue":"pending_docs"}`.
For a simple one-buffer workflow, both calls may omit `queue` and use the
default queue.

## System Tools

OS-level tools require **both** sides to opt in:

1. The agent ID is listed in `runtime.allow_system_agents` in `config.yaml`.
2. The agent declares `capabilities: [system]` in `SOUL.yaml` (the legacy
   `system_tools: true` field remains an agent-side alias).

```yaml
# config.yaml
runtime:
  allow_system_agents: [maintenance-agent]
  filesystem_roots: [/var/lib/soulacy/workspace]
```

!!! warning
    System tools execute with the gateway's OS permissions. On shared or
    internet-exposed deployments, leave them off and use narrower Python or
    MCP tools instead. Keep `filesystem_roots` narrow even for an approved
    system agent.

## Confirmation Gates

`confirm_tools` lists built-in tools that must pause for explicit approval
before executing:

```yaml
confirm_tools:
  - shell_exec
  - write_file
```

When the model calls a gated tool, the engine emits a `tool_confirm` event,
shows an approval card in the Chat page, and waits for your decision before
proceeding. `confirm_tools: ["*"]` gates every built-in call. Gates apply to
built-in and system tools (Python and MCP tools run without a gate — restrict
those via allowlists instead).

## MCP Tools

Tools from connected MCP servers are auto-injected under the name
`mcp__<server>__<tool>` — e.g. `mcp__github__search_repositories`. With no
allowlist configured, agents see every connected MCP tool. Once either field
below is present, MCP becomes deny-by-default:

```yaml
mcp_servers: [github]                           # allow whole servers…
mcp_tools: [mcp__github__search_repositories]   # …or individual tools
```

A tool is allowed when **either** list admits it. Use `mcp_servers: []` to
expose none, or `["*"]` / `["all"]` to allow everything explicitly.

For browser automation, use the **Browser** quick-start in the MCP page. It runs
a Playwright MCP sidecar and exposes navigation/click/type/snapshot tools to
agents. See [Browser Automation](browser-automation.md).

## Peer-Agent Tools

`agents: [researcher]` registers `agent__researcher` as a callable tool. See
[Peer Agents & Built-ins](peers-builtins.md) for delegation patterns and
`llm.tool_choice` forcing.

## Editing Tools in the GUI

The Agents page editor has a full tool builder — no YAML required:

- **+ Add tool** creates a tool card with name, description, timeout, and a
  JSON parameters-schema editor.
- The **Python file** field is a picker: type a path or hit **📂 Browse** to
  pick from the tool catalog of discovered scripts (picking one pre-fills the
  name and description).
- A hint chip row shows which **MCP tools are auto-injected** for the agent.
- Built-ins are a tri-state radio: *Default*, *None (peer-only orchestrator)*,
  or *Restricted* with a named allowlist.

## Safety Checklist

- Prefer explicit `builtins`, `mcp_servers`, and `mcp_tools` allowlists in production.
- Use `builtins: []` for orchestrators that should only call peers.
- Keep `system_tools` off unless the agent truly needs host access.
- Add `confirm_tools` for destructive or expensive actions.
- Run `sy agent validate path/to/SOUL.yaml` before deploying.
