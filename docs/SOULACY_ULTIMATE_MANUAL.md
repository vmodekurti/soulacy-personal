# Soulacy: Ultimate Master Technical Manual

Welcome to the ultimate system manual and documentation hub for **Soulacy**—the self-hosted declarative AI agent gateway compiled in **Go**. 

This document serves as the absolute, definitive reference for every module, package, API structure, command-line utility, and configuration key in the Soulacy framework. It outlines both developer integrations and runtime systems-engineering details.

---

## 1. Core Architectural Philosophy

Soulacy is engineered from the ground up to solve the three primary paint points of modern AI agent platforms (like CrewAI, AutoGen, or LangChain):
1. **Memory & Resource Bloat**: Imperative Python frameworks require hundreds of megabytes of RAM just to boot, taking 5+ seconds to spin up virtual environments. Soulacy is compiled into a single static Go binary, booting in **under 10ms** and running active chat adapters and engines under **less than 20MB of RAM**.
2. **Horizontal Deployment Friction**: Rather than managing complex multi-file codebase setups, deep dependency node trees, and package lockfiles, Soulacy utilizes a strictly **declarative config-over-code model (`SOUL.yaml`)**. You write configuration, and the Go gateway manages the execution runtime automatically.
3. **Privacy & Air-Gap Compliance**: Soulacy is 100% self-hosted. By pairing with local model APIs (like Ollama or LocalAI), storing vector embeddings in a local SQLite file (`sqlite-vec`), and securing credentials via machine-locked hardware keys, it operates completely offline.

```
       ┌─────────────────────────────────────────────────────────┐
       │                Triggers & Inbound Events                │
       │    Slack  ·  Telegram  ·  Cron Schedules  ·  Webhooks    │
       └────────────────────────────┬────────────────────────────┘
                                    │
                                    ▼
       ┌─────────────────────────────────────────────────────────┐
       │                High-Performance Gateway                 │
       │            Fiber REST  ·  WebSockets Server             │
       └────────────────────────────┬────────────────────────────┘
                                    │
                                    ▼
       ┌─────────────────────────────────────────────────────────┐
       │                 Runtime Engine Core                     │
       │        LLM Loops  ·  Resumable DAG Workflows            │
       └──────────────┬─────────────────────────────┬────────────┘
                      │                             │
                      ▼                             ▼
       ┌────────────────────────────┐  ┌─────────────────────────┐
       │     Isolated Executors     │  │     Data & Storage      │
       │   cgroups  ·  Subprocesses │  │ SQLite WAL  · sqlite-vec│
       │   MCP JSON-RPC Clients     │  │ Cryptographic KMS Vault │
       └────────────────────────────┘  └─────────────────────────┘
```

---

## 2. Package-by-Package Blueprint Dissection

Every module inside Soulacy’s `/internal` folder coordinates a specific structural slice of the gateway. Below is the comprehensive directory blueprint detailing each package's purpose, design patterns, and Go file mappings.

### 1. `internal/actionlog`
* **Purpose**: Persists every single tool execution, LLM turn, prompt token usage, and pipeline state transition.
* **Core Files**: `logger.go`, `storage.go`, `types.go`.
* **Systems Design**: Writes structured events asynchronously using buffered Go channels to prevent database locking friction during high-frequency agent runs. It feeds the visual timeline inside the Svelte GUI in real time.

### 2. `internal/audit`
* **Purpose**: Provides strict enterprise-grade compliance logs.
* **Core Files**: `audit.go`, `file_sink.go`, `types.go`.
* **Systems Design**: Fully logs raw LLM prompt requests, cost metrics, and user-denied tools to a tamper-resistant local log file. Essential for compliance audits in secure industries (finance, healthcare).

### 3. `internal/auth`
* **Purpose**: Secures REST endpoints and WebSocket channels.
* **Core Files**: `middleware.go`, `key_store.go`.
* **Systems Design**: Implements highly optimized API key verification gates, inspecting HTTP headers (`X-API-Key` or `Authorization`) using fast constant-time comparisons (`subtle.ConstantTimeCompare`) to mitigate timing attacks.

### 4. `internal/builder`
* **Purpose**: Coordinates the conversational agent generator.
* **Core Files**: `builder.go`, `catalog.go`.
* **Systems Design**: Analyzes user-described natural language requirements, scans live local tool lists, auto-generates missing Python handlers, and outputs a complete, deployment-ready `SOUL.yaml` pipeline definition.

### 5. `internal/channels`
* **Purpose**: Integrates out-of-the-box chat platform adapters.
* **Core Files**: `telegram.go`, `slack.go`, `discord.go`, `adapter.go`, `router.go`.
* **Systems Design**: Listens to active chat networks. Maps incoming text messages and attachments (images, PDFs, coordinates) into unified, strongly typed `message.Part` structs, caching files directly inside the session-scoped resource store.

### 6. `internal/config`
* **Purpose**: Unifies all gateway and engine properties.
* **Core Files**: `config.go`, `loader.go`.
* **Systems Design**: Implements global property loading using **Viper**, allowing any configuration value inside `config.yaml` to be overridden dynamically via environment variables prefixed with `SOULACY_` (e.g. `SOULACY_SERVER_PORT`).

### 7. `internal/costs`
* **Purpose**: Tracks exact LLM execution spending.
* **Core Files**: `tracker.go`, `prices.go`.
* **Systems Design**: Aggregates token usage counts (prompt tokens, completion tokens) reported by providers. Multiplies counts by internal price models in real time, persisting usage budgets per agent and per session.

### 8. `internal/credentials`
* **Purpose**: A highly secure, local-first secret vault.
* **Core Files**: `vault.go`, `kms.go`, `api.go`.
* **Systems Design**: Implements a zero-config platform KMS. Derives per-agent keys dynamically using HKDF-SHA256 based on platform machine UUIDs (`ioreg` on macOS, `/etc/machine-id` on Linux), encrypting credentials using `AES-256-GCM` before writing to SQLite.

### 9. `internal/executor`
* **Purpose**: Spawns and reaps external tool subprocesses.
* **Core Files**: `backend.go`, `subprocess.go`.
* **Systems Design**: Manages shell and Python command executions. It redirects process standard out to memory buffers while spawning dedicated scanner goroutines to read the process standard error line-by-line to stream active progress updates.

### 10. `internal/gateway`
* **Purpose**: The gateway server entrypoint.
* **Core Files**: `server.go`, `router.go`, `websocket.go`.
* **Systems Design**: Built using high-performance **Fiber (fasthttp)**. Leverages WebSockets to stream engine events, logs, and interactive tool confirmation prompts to connected dashboard clients with microsecond response times.

### 11. `internal/knowledge`
* **Purpose**: Manages document collections and semantic RAG.
* **Core Files**: `service.go`, `ingest.go`, `search.go`.
* **Systems Design**: Directs text extraction and chunking. Performs linear k-NN brute-force vector scans using the compiled `sqlite-vec` extension inside your local SQLite storage.

### 12. `internal/llm`
* **Purpose**: Unified connector mapping models to LLM providers.
* **Core Files**: `client.go`, `openai.go`, `ollama.go`, `gemini.go`, `anthropic.go`.
* **Systems Design**: Maps standardized chat messages into provider-specific payloads. Supports streaming responses and enforces output format compliance using re-prompt loops if JSON validations fail.

### 13. `internal/mcp`
* **Purpose**: JSON-RPC client for Model Context Protocol.
* **Core Files**: `client.go`, `transport.go`, `registry.go`.
* **Systems Design**: Spawns stdio-based MCP servers as child processes or connects to HTTP endpoints. Translates standard MCP schemas into tool signatures that Soulacy agents can dynamically search and invoke.

### 14. `internal/memory`
* **Purpose**: Manages conversational history and context windows.
* **Core Files**: `store.go`, `window.go`.
* **Systems Design**: Persists chat histories in SQLite. Enforces sliding token-window limits, dynamically truncating old messages or summarizing historical segments using recursive LLM passes to prevent model context crashes.

### 15. `internal/metrics`
* **Purpose**: Logs real-time gateway performance data.
* **Core Files**: `prometheus.go`, `instrumentation.go`.
* **Systems Design**: Registers Prometheus metrics for server execution latencies, LLM call costs, tool invocation error rates, and active WebSocket connection counts. Exposes an `/metrics` endpoint for Grafana scrapers.

### 16. `internal/plugins`
* **Purpose**: Packs and loads agent extensions.
* **Core Files**: `manifest.go`, `loader.go`.
* **Systems Design**: Dynamically discovers and registers packaged tool directories, exposing custom Python script-handlers as executable agent capabilities.

### 17. `internal/queue`
* **Purpose**: Schedules and guarantees run executions.
* **Core Files**: `broker.go`, `recover.go`, `types.go`.
* **Systems Design**: An in-memory channel-backed queue. `recover.go` scans SQLite on startup for incomplete or interrupted agent runs, re-enqueuing them automatically to ensure crash resilience.

### 18. `internal/rbac`
* **Purpose**: Implements role-based access control.
* **Core Files**: `roles.go`, `permissions.go`.
* **Systems Design**: Restricts access to sensitive APIs (e.g. creating/deleting agents, editing credentials) based on user roles (Admin, Operator, Viewer) carried via signed JWT tokens.

### 19. `internal/runtime`
* **Purpose**: Core orchestration engine.
* **Core Files**: `engine.go`, `loader.go`, `workflow.go`, `checkpoint.go`, `confirm.go`.
* **Systems Design**: Coordinates LLM reasoning loops, sub-agent peer calls (`agentCallDepth` context limits), interactive confirmation pauses, and sequential step workflow checkpointing.

### 20. `internal/sandbox`
* **Purpose**: Enforces host security limits on subprocess tools.
* **Core Files**: `wrap.go`, `limits.go`.
* **Systems Design**: Wraps tool executions using standard OS boundaries. It applies cgroup and kernel limits on maximum memory bytes, CPU shares, process forks, and active file descriptors before executing commands.

### 21. `internal/scheduler`
* **Purpose**: Manages cron and one-shot activation events.
* **Core Files**: `runner.go`, `cron.go`.
* **Systems Design**: Spawns long-running tickers that evaluate agent schedules. When a cron trigger is met, it formats and pushes a start run payload straight into the central queue.

### 22. `internal/session`
* **Purpose**: Stateful context and session resource management.
* **Core Files**: `session.go`, `resource_store.go`.
* **Systems Design**: Manages session-scoped directories where inbound media attachments are cached. Provides clean file access isolation to secure tools.

### 23. `internal/skills`
* **Purpose**: Packages groups of tools and instructions.
* **Core Files**: `catalog.go`, `importer.go`.
* **Systems Design**: Scans a directory for packaged skill assets, dynamically offering them as tools to agents that have declared interest in `SOUL.yaml`.

### 24. `internal/sqlitex`
* **Purpose**: Centralized SQLite pool wrapper.
* **Core Files**: `pool.go`, `config.go`.
* **Systems Design**: Enforces safe multi-threaded SQLite operations by running databases in **Write-Ahead Logging (WAL)** mode, combining it with connection pooling and strict BusyTimeout rules.

### 25. `internal/telemetry`
* **Purpose**: Provides OpenTelemetry system traces.
* **Core Files**: `tracer.go`, `exporter.go`.
* **Systems Design**: Exports standard OTEL spans for LLM response times, tool latencies, and database queries, allowing developers to identify system bottlenecks via Jaeger or Honeycomb.

### 26. `internal/templates`
* **Purpose**: Renders dynamic prompt and tool variables.
* **Core Files**: `engine.go`, `functions.go`.
* **Systems Design**: Parses Go standard `text/template` tags, interpolating trigger data, step outputs, and session memories into unified prompt buffers.

### 27. `internal/vector`
* **Purpose**: Pure mathematical vector utilities.
* **Core Files**: `math.go`, `distance.go`.
* **Systems Design**: Implements standard cosine similarity and Euclidean distance algorithms in Go, acting as a fallback when Cgo/sqlite-vec is disabled.

### 28. `internal/webui`
* **Purpose**: Distributes the Svelte dashboard assets.
* **Core Files**: `dist.go`, `embed.go`.
* **Systems Design**: Embeds compiled, static Svelte frontend assets directly into the Go binary using `go:embed`. Serves the entire dashboard portal through Fiber without requiring secondary web servers.

---

## 3. The Master `SOUL.yaml` Configuration Reference

The declarative configuration file `SOUL.yaml` is the single source of truth for an agent's behaviors. Below is the comprehensive specification of every schema node.

```yaml
# ==========================================
# SOUL.yaml - Master Schema Reference
# ==========================================

# Schema version
version: "1.0"

# Unique identifier (used in URLs, paths, and CLI commands)
id: "ops-assistant"

# Human-readable display name
name: "DevOps Operations Assistant"

# Detailed description (injected as peer-tool metadata)
description: "High-performance SRE responder that queries pods and checks runbooks."

# Opt-in tags for organizational filtering
tags:
  - "ops"
  - "sre"

# Custom metadata key-values
labels:
  environment: "production"
  tier: "backend"

# Trigger selection: channel / cron / webhook / oneshot / internal
trigger: "webhook"

# Required channel bindings when trigger is 'channel'
channels:
  - "telegram"
  - "slack"

# Ticker settings when trigger is 'cron' or 'oneshot'
schedule:
  cron: "*/15 * * * *"  # standard 5-field cron
  timeout: "5m"        # max run timeout (e.g. 5m, 1h)

# Conversational instructions
system_prompt: |
  You are a helpful DevSecOps assistant. Perform diagnostics carefully.
  Always explain commands before running them.

# LLM reasoning configuration
llm:
  provider: "ollama"               # ollama, openai, gemini, anthropic
  model: "qwen-2.5-72b-instruct"  # specific model target
  temperature: 0.2                 # creativity boundary
  max_tokens: 4096                 # response length limit
  base_url: "http://localhost:11434" # custom endpoint override (OpenRouter/DashScope)
  
  # Opt-in provider security whitelist
  allowed_providers:
    - "ollama"
    - "openai"

  # Force specific tool use on the FIRST turn only (auto, none, or tool name)
  tool_choice: "auto"

  # JSON Schema constraint forcing structured JSON outputs
  output_schema:
    type: "object"
    properties:
      remedy_command: {type: "string"}
      risk_score: {type: "integer"}
    required: ["remedy_command", "risk_score"]

# Custom Python or subprocess tools declared directly on the agent
tools:
  - name: "check_logs"
    description: "Inspects logs for specific keywords."
    python_file: "~/tools/check_logs.py"
    timeout: "30s" # overrides global timeout for this tool
    parameters:
      type: "object"
      properties:
        pod_name: {type: "string"}
      required: ["pod_name"]

# Opt-in skills registry discovery (use ["*"] to enable all)
skills:
  - "k8s-diagnostics"

# Opt-in local RAG search databases
knowledge:
  - "runbooks"

# Multi-agent peer tool injection (use ["*"] to expose all)
agents:
  - "code-reviewer"

# Strict built-in tool filter selection (use [] to allow none)
builtins:
  - "web_search"
  - "kb_search"

# System tools double opt-in flag (shell_exec, run_script, install_library)
system_tools: true

# Built-in tools requiring explicit developer authorization before execution
confirm_tools:
  - "shell_exec"
  - "write_file"

# Memory window retention constraints
memory:
  read_scopes: ["session", "global"]
  write_scopes: ["session"]
  max_tokens: 40

# Wall-clock duration limits
max_turns: 15        # maximum reasoning cycles before aborting
run_timeout: "10m"   # total run wall-clock timeout (e.g. 5m, 1h)
stream_reply: true   # stream tokens back to client
enabled: true        # active status toggle

# Failure notification routing configuration
notify_on_failure:
  channel: "telegram"
  to: "8546291328" # chat ID
  include_error: true
  template: "🚨 Soulacy agent {agent_id} failed: {error}"
```

---

## 4. Complete CLI Manual (`sy` CLI)

Soulacy includes a high-performance command-line interface, **`sy`** (compiled directly within your single binary footprint), which allows you to inspect gateway states, run interactive chat sessions, and manage vector indices directly from your terminal.

### 1. Gateway Server Control (`sy server`)
* **Start the Gateway**:
  ```bash
  sy server start --port 18789 --config ~/.soulacy/config.yaml
  ```
* **Verify Server Status**:
  ```bash
  sy server status
  ```
  Returns status details, active port configurations, loaded agents, and thread pool metrics.

### 2. Conversational Agent Interaction (`sy chat`)
* **Start an Interactive Chat**:
  ```bash
  sy chat --agent researcher
  ```
  Launches a live terminal chat session with the specified agent.
* **Pass a Single Prompt**:
  ```bash
  sy chat --agent researcher "Analyze the latest release notes"
  ```

### 3. Agent Lifecycle Administration (`sy agent`)
* **List Loaded Agents**:
  ```bash
  sy agent list
  ```
* **Inspect an Agent's Schema**:
  ```bash
  sy agent inspect researcher
  ```
* **Hot-Reload All Configurations**:
  ```bash
  sy agent reload
  ```
  Forces the `Loader` to parse directories and reload all `SOUL.yaml` changes immediately.

### 4. Semantic RAG & Vector Indexing (`sy knowledge`)
* **Create a Vector Database**:
  ```bash
  sy knowledge create --name runbooks
  ```
* **Ingest a Markdown File**:
  ```bash
  sy knowledge ingest --kb runbooks --file ./docs/incident_remedy.md
  ```
  Extracts, chunks, embeds, and indexes the document using `sqlite-vec`.
* **Query a Knowledge Base**:
  ```bash
  sy knowledge query --kb runbooks "How to restart pods in CrashLoopBackOff"
  ```

### 5. Live Stream Diagnostics (`sy logs`)
* **Stream Gateway Logs**:
  ```bash
  sy logs --follow --level debug
  ```
  Displays real-time logs, including LLM token stream timings, tool execution standard error logs, and client subscription connections.

### 6. Cloud Deployment (`sy deploy`)
* **Deploy to Hosted Cloud**:
  ```bash
  sy deploy --agent researcher --target prod
  ```
  Serializes your local YAML definitions and packages them straight to your hosted Soulacy Cloud instance.

---

## 5. Built-in Go-Native Tools Reference

Soulacy includes a suite of Go-native built-in tools that are injected into your agent's capability catalog based on your `SOUL.yaml` configurations.

### 1. `fetch_url`
* **Description**: Perfroms a secure HTTP GET request to retrieve a URL body as plaintext.
* **Arguments**:
  * `url` (String, Required): The target address to fetch.
  * `max_bytes` (Integer, Optional): Limits response size to prevent memory overload (default: 1,048,576 bytes / 1MB).
* **System Design**: Auto-detects bare GitHub repository links and redirects them straight to the raw README file to save model token steps.

### 2. `http_request`
* **Description**: Full-featured HTTP client supporting REST method operations.
* **Arguments**:
  * `method` (String, Required): GET, POST, PUT, PATCH, or DELETE.
  * `url` (String, Required): Target endpoint.
  * `body` (String, Optional): Raw request body.
  * `content_type` (String, Optional): Request headers Content-Type (e.g. `application/json`).
  * `headers` (Map, Optional): Custom HTTP headers.

### 3. `download_file`
* **Description**: Downloads any asset directly to the local filesystem.
* **Arguments**:
  * `url` (String, Required): Asset source URL.
  * `dest_path` (String, Required): File destination path.
* **System Design**: Recursively generates parent directories automatically if they do not exist.

### 4. `shell_exec`
* **Description**: Runs arbitrary shell commands on the host machine.
* **Arguments**:
  * `command` (String, Required): Command string to execute.
  * `working_dir` (String, Optional): Target execution directory.
  * `timeout_seconds` (Integer, Optional): Execution timeout limit (default: 60s, max: 600s).
* **Double Opt-In Requirement**: Requires both `system_tools: true` in the agent's `SOUL.yaml` AND `runtime.allow_system_tools: true` in the gateway's `config.yaml` to execute.

### 5. `read_file`
* **Description**: Reads a local file's content.
* **Arguments**:
  * `path` (String, Required): Target file path. Supports environment variable expansion and home directory tilde expansion (`~/`).
  * `max_bytes` (Integer, Optional): Maximum bytes to read (default: 1MB).

### 6. `write_file`
* **Description**: Writes or appends text to a local file.
* **Arguments**:
  * `path` (String, Required): Target file path.
  * `content` (String, Required): Text to write.
  * `append` (Boolean, Optional): If true, appends to the file instead of overwriting.

---

## 6. Enterprise Deployment Playbook

For production environments requiring high availability, secure tenant separation, and zero-downtime upgrades, use the following operational blueprints:

```
                          [ Client / Chat Platforms ]
                                       │
                                       ▼
                       [ Reverse Proxy: Nginx / Traefik ]
                                       │
                    ┌──────────────────┴──────────────────┐
                    ▼                                     ▼
        [ Soulacy Gateway: Node A ]           [ Soulacy Gateway: Node B ]
             (Active Master)                       (Passive Standby)
                    │                                     │
                    └──────────────────┬──────────────────┘
                                       │
                                       ▼
                         [ Shared Storage Gateway ]
                       - Postgres Cluster with RLS
                       - Qdrant Vector Cluster
```

### 1. High Availability: Active-Passive Warm Standby
To deploy Soulacy in a resilient, high-availability cluster:
* **Shared Storage Configuration**:
  Configure all gateway nodes in `config.yaml` to point to a centralized **PostgreSQL database cluster** and a **Qdrant vector cluster** rather than local SQLite files.
* **Health Check & Load Balancing**:
  Expose the gateway's `/health` endpoint to your reverse proxy (e.g. Traefik, Nginx). Configure the load balancer to route all user chat traffic and webhooks strictly to the active master node.
* **Failover Orchestration**:
  If the active master node fails, the load balancer automatically redirects traffic to the warm standby node. Since state is persisted in PostgreSQL, the standby node reads active run configurations and resumes workflows seamlessly.

### 2. Multi-Tenancy via PostgreSQL Row-Level Security (RLS)
To host 10,000+ separate business customers safely on a single cluster:
* **The Strategy**: Do not shard SQLite files dynamically, as this can exhaust system file descriptors and crash the OS. Instead, configure Soulacy to use a unified PostgreSQL database with **Row-Level Security (RLS)**.
* **Tenant Isolation**:
  Create a `tenant_id` column on all storage tables (`runs`, `messages`, `credentials`, `knowledge`). Enforce RLS policies so that a database connection authenticated under Tenant A can never query or modify records belonging to Tenant B:
  ```sql
  CREATE POLICY tenant_isolation_policy ON runs
    USING (tenant_id = current_setting('app.current_tenant'));
  ```

### 3. Dynamic Tool Sandboxing in Production
To execute custom, untrusted Python tool scripts safely without risking host server compromise:
* **Dockerized Executor Pool**:
  Instead of spawning subprocesses directly on the host VPS, configure your `executor` to delegate commands to an isolated, ephemeral Docker container pool.
* **wazero WASM Execution**:
  For ultra-low latency sandboxing without the overhead of Docker containers, compile your custom tool scripts to **WebAssembly (WASM)**. Soulacy's Go engine can execute WASM modules inside the same process memory space using `wazero` with microsecond efficiency and strict memory containment.
