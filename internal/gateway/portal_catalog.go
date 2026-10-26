package gateway

// Keep this an explicit catalog: a new HTTP route does not silently become an
// agent capability. Each action reuses the existing portal handler and policy.
type portalActionSpec struct {
	Name        string `json:"action"`
	Mode        string `json:"tool_mode"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description"`
	Params      string `json:"params,omitempty"`
	Body        string `json:"body,omitempty"`
}

var portalCatalog = []portalActionSpec{
	{"agents.list", "read", "GET", "/agents", "List installed agents; resolve names to IDs.", "", ""},
	{"agents.get", "read", "GET", "/agents/:id", "Read a complete saved agent before editing.", "id (required)", ""},
	{"agents.update", "update", "PATCH", "/agents/:id", "Edit ordinary agents only: name, description, system_prompt, llm, enabled, schedule, max_turns, budget, run_timeout, step_timeout or tags. Protected/privileged agents and permission/tool grants cannot be changed. Nested objects merge.", "id (required)", "Only allowed fields, e.g. {llm:{model:...}, max_turns:0, budget:{max_tokens:0,max_llm_calls:0}}. Read agents.get for field names."},
	{"agents.create", "update", "POST", "/agents", "Create an agent from a complete definition. Prefer build_agent for natural-language creation.", "", "id, name, system_prompt, trigger (channel or cron), channels ([http] only), llm, enabled, schedule; no tool/permission grants"},
	{"agents.enable", "update", "POST", "/agents/:id/enable", "Enable an agent and its configured schedule.", "id (required)", ""},
	{"agents.disable", "update", "POST", "/agents/:id/disable", "Disable an agent.", "id (required)", ""},
	{"agents.delete", "confirm", "DELETE", "/agents/:id", "Delete an agent.", "id (required)", ""},
	{"agents.run", "confirm", "POST", "/agents/:id/trigger", "Run an agent now; configured delivery may send its result.", "id (required)", ""},
	{"agents.versions", "read", "GET", "/agents/:id/versions", "List saved agent versions.", "id (required)", ""},
	{"agents.actions", "read", "GET", "/agents/:id/actions", "Read detailed execution events and tool errors.", "id (required), limit, types, durable", ""},
	{"agents.files", "read", "GET", "/agents/:id/files", "List published agent artifacts and links.", "id (required)", ""},
	{"runs.list", "read", "GET", "/runs/ledger", "Search job execution results, failures, generated podcast/audio links and outputs, newest first. Use agent_id to narrow a job. Pagination covers scanned history; event_truncated means older events may exist.", "agent_id, session_id, run_id, q (text), status, after (RFC3339), before (RFC3339), scope (all/automation), limit, offset, event_limit", ""},
	{"runs.events", "read", "GET", "/runs/events", "Inspect execution event details.", "agent_id, session_id, limit", ""},
	{"runs.active", "read", "GET", "/activity/running", "List currently running jobs.", "", ""},
	{"runs.diagnosis", "read", "GET", "/studio/run-diagnosis", "Get a run failure diagnosis.", "agent_id, session_id", ""},
	{"schedule.list", "read", "GET", "/schedule", "Inspect scheduled jobs and next executions.", "", ""},
	{"schedule.status", "read", "GET", "/schedule/status", "Inspect scheduler status.", "", ""},
	{"schedule.pause", "update", "POST", "/schedule/:id/pause", "Pause a Genie-owned scheduled monitor. Resume with agents.enable.", "id (required)", ""},
	{"schedule.delete", "confirm", "DELETE", "/schedule/:id", "Cancel a monitor.", "id (required)", ""},
	{"providers.list", "read", "GET", "/providers", "List configured LLM providers, models and credential readiness. Keys are redacted.", "", ""},
	{"providers.models", "read", "GET", "/providers/:id/models", "Discover models from a provider.", "id (required)", ""},
	{"providers.save", "update", "POST", "/providers/:id", "Add a provider or edit one that is unused and has no stored credentials. Providers in use by the gateway, Studio or any agent are protected. Known IDs use their native protocol; custom IDs with base_url use OpenAI-compatible protocol. Enter required API keys securely at /#providers, never in chat. Verify readiness afterward.", "id (required)", "base_url, model, local, region, retention, prompt_caching, thinking_budget, parallel_tool_calls, keep_alive (optional). Omitted values preserved."},
	{"providers.model", "update", "POST", "/providers/:id/model", "Set an unused provider's model. Providers in use or with credentials are protected.", "id (required)", "model (required)"},
	{"providers.delete", "confirm", "DELETE", "/providers/:id", "Remove an unused provider only; providers in use or with credentials are protected.", "id (required)", ""},
	{"providers.pull", "confirm", "POST", "/providers/:id/models/pull", "Download a model; may use substantial disk and bandwidth.", "id (required)", "model (required)"},
	{"providers.pull_status", "read", "GET", "/providers/:id/models/pull/:job", "Check a model download.", "id (required), job (required)", ""},
	{"settings.get", "read", "GET", "/config", "Read workspace settings with credentials redacted.", "", ""},
	{"channels.list", "read", "GET", "/channels", "Inspect delivery channels.", "", ""},
	{"channels.readiness", "read", "GET", "/channels/delivery-readiness", "Check delivery readiness.", "", ""},
	{"knowledge.list", "read", "GET", "/knowledge", "List knowledge bases.", "", ""},
	{"knowledge.create", "update", "POST", "/knowledge", "Create a knowledge base.", "", "name (required), description"},
	{"knowledge.documents", "read", "GET", "/knowledge/:kb/documents", "List documents.", "kb (required)", ""},
	{"knowledge.search", "read", "POST", "/knowledge/:kb/search", "Search knowledge content.", "kb (required)", "query (required), top_k"},
	{"knowledge.delete", "confirm", "DELETE", "/knowledge/:kb", "Delete a knowledge base.", "kb (required)", ""},
	{"knowledge.delete_document", "confirm", "DELETE", "/knowledge/:kb/documents/:doc", "Delete a document.", "kb (required), doc (required)", ""},
	{"skills.list", "read", "GET", "/skills", "List installed skills.", "", ""},
	{"skills.get", "read", "GET", "/skills/:name", "Read a skill.", "name (required)", ""},
	{"skills.rescan", "update", "POST", "/skills/rescan", "Refresh installed skills.", "", ""},
	{"skills.agensi_status", "read", "GET", "/skills/agensi/status", "Check whether the authenticated Agensi skill catalog is connected.", "", ""},
	{"skills.agensi_connect", "update", "POST", "/skills/agensi/connect", "Start Agensi OAuth sign-in and return the secure authorization link. The user must open it and click Allow; never request a token in chat.", "", ""},
	{"skills.agensi_search", "read", "POST", "/skills/agensi/search", "Search Agensi after sign-in. Search is never attempted anonymously.", "", "query (required)"},
	{"mcp.list", "read", "GET", "/mcp", "List tool servers; credentials are redacted.", "", ""},
	{"connectors.list", "read", "GET", "/connectors", "List website connectors.", "", ""},
	{"plugins.list", "read", "GET", "/plugins/installed", "List installed plugins.", "", ""},
	{"workboard.list", "read", "GET", "/workboard/tasks", "List workboard tasks.", "status, agent_id", ""},
	{"workboard.get", "read", "GET", "/workboard/tasks/:id", "Inspect a task.", "id (required)", ""},
	{"workboard.create", "update", "POST", "/workboard/tasks", "Create a task.", "", "title, description, agent_id"},
	{"workboard.update", "update", "PATCH", "/workboard/tasks/:id", "Update a task.", "id (required)", "Changed task fields; inspect workboard.get first"},
	{"workboard.runs", "read", "GET", "/workboard/tasks/:id/runs", "Find task execution results.", "id (required)", ""},
	{"workboard.artifacts", "read", "GET", "/workboard/tasks/:id/artifacts", "Find generated task files and links.", "id (required)", ""},
	{"workboard.run", "confirm", "POST", "/workboard/tasks/:id/run", "Execute a workboard task.", "id (required)", ""},
	{"workboard.delete", "confirm", "DELETE", "/workboard/tasks/:id", "Delete a task.", "id (required)", ""},
	{"history.search", "read", "GET", "/history/search", "Search previous conversation messages.", "q, agent_id, limit", ""},
	{"history.session", "read", "GET", "/history/:session_id", "Read an accessible conversation.", "session_id (required)", ""},
	{"artifacts.list", "read", "GET", "/chat/artifacts", "Find chat-generated files.", "agent_id, session_id", ""},
	{"memory.list", "read", "GET", "/memory/facts", "Inspect remembered facts.", "q, limit", ""},
	{"memory.update", "update", "PATCH", "/memory/facts/:id", "Correct a remembered fact.", "id (required)", "Changed fact fields"},
	{"memory.delete", "confirm", "DELETE", "/memory/facts/:id", "Forget a remembered fact.", "id (required)", ""},
	{"person.get", "read", "GET", "/person/model", "Inspect About You.", "", ""},
	{"person.senses", "read", "GET", "/person/senses", "Inspect personal sensing preferences.", "", ""},
	{"person.update_sense", "update", "PUT", "/person/senses/:sense", "Change a sensing preference.", "sense (required)", "enabled (boolean)"},
	{"costs.status", "read", "GET", "/costs/status", "Inspect cost controls and spending status.", "", ""},
	{"costs.usage", "read", "GET", "/costs/usage", "Inspect model usage.", "agent_id, days", ""},
	{"system.health", "read", "GET", "/health", "Inspect gateway health.", "", ""},
	{"system.doctor", "read", "GET", "/doctor", "Inspect provider and runtime diagnostics.", "", ""},
	{"system.updates", "read", "GET", "/system/updates/status", "Inspect software version, updates and active runs.", "", ""},
	{"system.service", "read", "GET", "/admin/service", "Inspect start-on-login service status.", "", ""},
}
