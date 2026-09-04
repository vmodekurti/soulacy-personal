// pyerror.js — turn a raw run error into a plain-English explanation, a
// suggested fix, and (where one exists) a CONCRETE ACTION the UI can offer.
// Pure & unit-tested.
//
// Originally this only knew Python tracebacks (Guided Studio Builder, Story 6).
// But the live-run error box uses it for EVERY failure, including engine-level
// ones — consent refusals, missing tools, bad credentials, timeouts. Those fell
// through to the generic fallback, which prints the raw Go error and advises
// "Check the code around the reported line." For a consent refusal that advice
// is actively wrong: the code is fine, it just needs approval, and the only
// button on offer ("Self-correct workflow") would ask a model to rewrite code
// that isn't broken.
//
// So PLATFORM_RULES run FIRST and may attach an `action`, which the UI turns
// into a real button. The wording mirrors internal/runtime/flowdiagnosis.go's
// classifyFlowError so the GUI and the server's own diagnosis agree.
//
// Shape: { summary, fix, action? }
//   action = { kind: 'consent', nodeId }   → open the Review & grant dialog
//          | { kind: 'timeout',  nodeId }  → focus the node's timeout field
//          | { kind: 'tools' }             → open the agent's tool settings
//          | { kind: 'providers' }         → open Providers / Secrets
//          | { kind: 'channels' }          → open channel settings

// nodeFromError pulls the flow node id out of an engine error, which formats
// it as: flow: node "curate_source_pack": …
function nodeFromError(text) {
  const m = text.match(/node\s+"([^"]+)"/)
  return m ? m[1] : ''
}

// capsFromConsentError reads the capability list the engine reported, e.g.
// `runs beyond guardrails ([network dynamic])`.
function capsFromConsentError(text) {
  const m = text.match(/beyond guardrails\s*\(\[([^\]]*)\]\)/)
  if (!m) return []
  return m[1].split(/\s+/).map((s) => s.trim()).filter(Boolean)
}

// capabilityPhrase renders capability tokens as something a non-developer can
// act on, rather than echoing the raw token list.
function capabilityPhrase(caps) {
  const parts = []
  if (caps.includes('network')) parts.push('makes network calls')
  if (caps.includes('system')) parts.push('runs commands or writes files')
  if (caps.includes('dynamic')) parts.push('uses dynamic code the scanner can’t read through')
  if (!parts.length) return 'goes beyond the read-only guardrails'
  if (parts.length === 1) return parts[0]
  return parts.slice(0, -1).join(', ') + ' and ' + parts[parts.length - 1]
}

const PLATFORM_RULES = [
  {
    // Consent is matched first: it is the one platform error with a one-click
    // resolution, and its text also contains words other rules would claim.
    match: /consent:\s*node\s*"([^"]+)"\s*runs beyond guardrails/,
    build: (m, text) => {
      const node = m[1]
      const caps = capsFromConsentError(text)
      return {
        summary: `The step “${node}” ${capabilityPhrase(caps)}, so Soulacy blocked it until you approve it. Nothing is wrong with the code — it just hasn’t been granted permission yet.`,
        fix: 'Review the code and grant it, below. The grant is bound to this exact code, so editing the block later asks again.',
        action: { kind: 'consent', nodeId: node },
      }
    },
  },
  {
    match: /consent:\s*node\s*"([^"]+)"\s*code changed since it was consented/,
    build: (m) => ({
      summary: `The code in “${m[1]}” changed after you approved it, so the previous approval no longer applies.`,
      fix: 'Review the updated code and grant it again.',
      action: { kind: 'consent', nodeId: m[1] },
    }),
  },
  {
    match: /consent:\s*node\s*"([^"]+)"\s*needs "?([a-z]+)"?\s*capability/,
    build: (m) => ({
      summary: `The step “${m[1]}” needs the “${m[2]}” permission, but the approval on file didn’t include it.`,
      fix: 'Grant it again and make sure the listed permission is included.',
      action: { kind: 'consent', nodeId: m[1] },
    }),
  },
  {
    // The server ceiling — NOT resolvable from Studio, so no action button.
    match: /allow_system_tools|needs host execution but the server ceiling is off|requires the 'system' capability/,
    build: (m, text) => ({
      summary: `This workflow wants to run commands on the host machine${nodeFromError(text) ? ` (step “${nodeFromError(text)}”)` : ''}, which the server has switched off.`,
      fix: 'This one isn’t fixable from Studio: an operator must set runtime.allow_system_tools: true in config.yaml and restart the gateway. Safer alternative — use a built-in tool instead of shelling out.',
    }),
  },
  {
    // The exact failure behind the 30000ms web_search deaths.
    match: /Client\.Timeout exceeded|context deadline exceeded/,
    build: (m, text) => {
      const node = nodeFromError(text)
      const isSearch = /web_search/.test(text)
      const out = {
        summary: isSearch
          ? 'The search provider didn’t answer in time. This is usually the provider being slow rather than a problem with your workflow — and it gets much more likely when a fan-out runs several searches at once.'
          : `The step${node ? ` “${node}”` : ''} ran longer than its time limit and was stopped.`,
        fix: isSearch
          ? 'Give the search more time: add a timeout_s argument to this block (e.g. 90), or raise search.timeout in config.yaml to apply it everywhere. Lowering the fan-out’s max-parallel also helps.'
          : 'Raise this step’s timeout in the inspector, or reduce how much it has to do.',
      }
      if (node) out.action = { kind: 'timeout', nodeId: node }
      return out
    },
  },
  {
    match: /no such tool|tool not found|unknown tool|not installed/i,
    build: (m, text) => ({
      summary: `This workflow calls a tool that isn’t available to this agent${nodeFromError(text) ? ` (step “${nodeFromError(text)}”)` : ''}.`,
      fix: 'Add the tool to the agent’s allowed tools, install the MCP server or skill that provides it, or swap the block for a tool you already have.',
      action: { kind: 'tools' },
    }),
  },
  {
    match: /\b401\b|unauthorized|invalid api key|no .{0,20}api key|credentials/i,
    build: () => ({
      summary: 'A provider or integration rejected the request because its credentials are missing or invalid.',
      fix: 'Open Providers (or Secrets) and re-test the key for this provider. If the gateway loads keys at boot, restart it after saving.',
      action: { kind: 'providers' },
    }),
  },
  {
    match: /chat not found|bot is not allowed|channel .{0,40}not configured|send failed through channel/i,
    build: () => ({
      summary: 'The message was built successfully but the channel refused to deliver it — usually the destination id, or the bot’s access to it.',
      fix: 'For a Telegram DM, message the bot once with /start. For a group, add the bot, give it post access, and use the numeric chat id (groups usually start with -100).',
      action: { kind: 'channels' },
    }),
  },
  {
    match: /tool args for .{0,40}not valid JSON|arguments .{0,40}not valid JSON|invalid tool input/i,
    build: (m, text) => ({
      summary: `A block passed arguments that weren’t valid JSON${nodeFromError(text) ? ` (step “${nodeFromError(text)}”)` : ''}.`,
      fix: 'Wire the upstream value into a typed input port instead of hand-writing a template, or add an extraction block that produces clean JSON first.',
    }),
  },
]

const RULES = [
  {
    match: /KeyError:\s*['"]?([^'"\n]+)/,
    explain: (m) => `The code tried to read a field called “${m[1]}” from its input, but that field wasn't there.`,
    fix: (m) => `Use inputs.get("${m[1]}") instead of inputs["${m[1]}"], or make sure the upstream step produces “${m[1]}”.`,
  },
  {
    match: /NameError:\s*name ['"]([^'"]+)['"] is not defined/,
    explain: (m) => `The code used “${m[1]}” before it was defined (a typo or a missing import).`,
    fix: (m) => `Define “${m[1]}” first, or import the module it comes from.`,
  },
  {
    match: /IndentationError|TabError/,
    explain: () => 'The code has inconsistent indentation.',
    fix: () => 'Use 4 spaces per level and don’t mix tabs with spaces.',
  },
  {
    match: /SyntaxError:\s*(.+)/,
    explain: (m) => `Python couldn't parse the code: ${m[1].trim()}.`,
    fix: () => 'Check for a missing colon, bracket, or quote near the reported line.',
  },
  {
    match: /TypeError:\s*(.+)/,
    explain: (m) => `A value was the wrong type for the operation: ${m[1].trim()}.`,
    fix: () => 'Convert the value first (e.g. int(x), str(x)) or check it isn’t None.',
  },
  {
    match: /ZeroDivisionError/,
    explain: () => 'The code divided by zero.',
    fix: () => 'Guard the division, e.g. `x / y if y else 0`.',
  },
  {
    match: /ModuleNotFoundError:\s*No module named ['"]([^'"]+)['"]/,
    explain: (m) => `The code imports “${m[1]}”, which isn't available in the sandbox.`,
    fix: (m) => `Remove the dependency on “${m[1]}”, or use only the standard library.`,
  },
  {
    match: /JSONDecodeError|Expecting value/,
    explain: () => 'The input wasn’t valid JSON when the code tried to read it.',
    fix: () => 'Make sure the upstream step outputs JSON, or parse defensively.',
  },
  {
    match: /timed out|deadline exceeded|timeout/i,
    explain: () => 'The step ran longer than its time limit and was stopped.',
    fix: () => 'Reduce the work, or raise the step’s timeout in its settings.',
  },
]

// explainPythonError returns { summary, fix, action? } for a raw error string.
// Platform/engine errors are matched FIRST (only they can carry an action);
// Python traceback rules run next; the last traceback line is the fallback.
export function explainPythonError(raw) {
  const text = String(raw || '')
  if (!text.trim()) return { summary: 'The step failed without an error message.', fix: 'Run it again, or add a print() to see what happened.' }
  for (const rule of PLATFORM_RULES) {
    const m = text.match(rule.match)
    if (m) return rule.build(m, text)
  }
  for (const rule of RULES) {
    const m = text.match(rule.match)
    if (m) return { summary: rule.explain(m), fix: rule.fix(m) }
  }
  // Last line of a traceback is usually the most informative.
  const lines = text.trim().split('\n').filter(Boolean)
  return { summary: lines[lines.length - 1] || 'The step failed.', fix: 'Check the code around the reported line.' }
}
