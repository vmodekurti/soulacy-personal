// explainCommand.js
//
// Turns an agent tool-confirmation request into plain-English lines describing
// what running it would actually do. Used by the "Action Required" approval
// modal so the user can read intent in human terms before approving.
//
// Deterministic and dependency-free on purpose: this gates a security decision,
// so the explanation must be instant and cannot be shaped by the agent/LLM.

// Names of tools that execute a raw shell command we should describe.
const SHELL_TOOLS = new Set(['shell_exec', 'shell', 'bash', 'sh', 'exec', 'run_shell'])

// Per-command summaries. Each entry is (argv) => string | null.
// `argv` is the whitespace-split tokens of a single command segment.
const COMMANDS = {
  cd:     (a) => `change directory to ${a[1] || '~'}`,
  ls:     (a) => `list files${tail(a) ? ` in ${tail(a)}` : ''}`,
  cat:    (a) => `print the contents of ${tail(a) || 'a file'}`,
  rm:     (a) => `${a.includes('-rf') || a.includes('-r') ? 'recursively delete' : 'delete'} ${tail(a) || 'file(s)'}${a.includes('-f') || a.includes('-rf') ? ' (forced, no prompt)' : ''}`,
  mv:     (a) => `move/rename ${a[1] || 'a file'}${a[2] ? ` to ${a[2]}` : ''}`,
  cp:     (a) => `copy ${a[1] || 'a file'}${a[2] ? ` to ${a[2]}` : ''}`,
  mkdir:  (a) => `create directory ${tail(a) || ''}`.trim(),
  touch:  (a) => `create/update timestamp on ${tail(a) || 'a file'}`,
  echo:   (a) => `print text to output`,
  tail:   (a) => `show the last lines of ${nonFlag(a.slice(1)) || 'the output'}`,
  head:   (a) => `show the first lines of ${nonFlag(a.slice(1)) || 'the output'}`,
  grep:   (a) => `search text for a pattern`,
  curl:   (a) => `make a network request to ${firstUrl(a) || 'a URL'}`,
  wget:   (a) => `download from ${firstUrl(a) || 'a URL'}`,
  python: (a) => `run the Python script/command ${nonFlag(a.slice(1)) || ''}`.trim(),
  python3:(a) => `run the Python script/command ${nonFlag(a.slice(1)) || ''}`.trim(),
  pip:    (a) => `manage Python packages (${a[1] || 'pip'})`,
  pip3:   (a) => `manage Python packages (${a[1] || 'pip'})`,
  node:   (a) => `run the Node.js script ${nonFlag(a.slice(1)) || ''}`.trim(),
  npm:    (a) => `run npm (${a[1] || 'command'})`,
  npx:    (a) => `run an npm package binary (${a[1] || ''})`.trim(),
  git:    (a) => `run git ${a[1] || ''}`.trim(),
  docker: (a) => `run docker ${a[1] || ''}`.trim(),
  kill:   (a) => `terminate a process`,
  chmod:  (a) => `change file permissions on ${tail(a) || 'a file'}`,
  sudo:   (a) => `run a command with elevated (admin) privileges`,
}

function tail(a) { return a.slice(1).filter((t) => !t.startsWith('-')).join(' ') }
function nonFlag(a) { return a.filter((t) => !t.startsWith('-')).join(' ') }
function firstUrl(a) { return a.find((t) => /^https?:\/\//.test(t)) || '' }

// Split a shell command into top-level segments on && ; | (best-effort, not a
// full parser — good enough to describe intent without executing anything).
function splitSegments(cmd) {
  return cmd
    .split(/\s*(?:\|\||&&|;|\|)\s*/)
    .map((s) => s.trim())
    .filter(Boolean)
}

// Remove shell redirection operators and their targets from a segment so they
// don't get mistaken for command arguments (e.g. `2>&1`, `> out.log`, `< in`).
function stripRedirections(seg) {
  return seg
    .replace(/\d*>>?\s*&\s*\d+/g, ' ') // 2>&1, >&2
    .replace(/\d*>>?\s*\S+/g, ' ')      // > file, 2> file, >> file
    .replace(/<\s*\S+/g, ' ')            // < file
    .replace(/\s+/g, ' ')
    .trim()
}

function describeSegment(rawSeg) {
  const seg = stripRedirections(rawSeg)
  if (!seg) return null
  const tokens = seg.split(/\s+/)
  let i = 0
  // Skip leading env-var assignments like FOO=bar.
  while (i < tokens.length && /^[A-Za-z_][A-Za-z0-9_]*=/.test(tokens[i])) i++
  const argv = tokens.slice(i)
  if (argv.length === 0) return null
  const base = argv[0].split('/').pop() // handle ./venv/bin/python -> python
  const fn = COMMANDS[base]
  if (fn) {
    const out = fn(argv)
    if (out) return out
  }
  return `run \`${base}\``
}

// Explain a raw shell command string as an array of plain-English steps.
export function explainShellCommand(cmd) {
  if (!cmd || typeof cmd !== 'string') return []
  const segs = splitSegments(cmd)
  const lines = []
  for (const seg of segs) {
    const d = describeSegment(seg)
    if (d) lines.push(d)
  }
  // Note output redirections, which are easy to miss in a long command.
  if (/\d*>&\d+/.test(cmd)) lines.push('merge error output into normal output')
  // File redirections: target must be a filename, not another descriptor (&N).
  if (/>>\s*[^&\s]/.test(cmd)) lines.push('append output to a file')
  else if (/[^>&\d]>\s*[^&\s]/.test(cmd) || /^>\s*[^&\s]/.test(cmd)) lines.push('write output to a file (overwriting it)')
  return lines
}

// Top-level entry: given a confirm request shaped like
//   { tool: 'shell_exec', args: { command, timeout_seconds } }
// return { summary, steps } where summary is a one-liner and steps is an array.
export function explainConfirmRequest(req) {
  if (!req || typeof req !== 'object') return { summary: '', steps: [] }
  const tool = String(req.tool || '')
  const args = req.args || {}

  if (SHELL_TOOLS.has(tool)) {
    const cmd = args.command ?? args.cmd ?? args.script ?? ''
    const steps = explainShellCommand(cmd)
    const summary = steps.length
      ? `This runs a shell command that will: ${joinSteps(steps)}.`
      : 'This runs a shell command on your machine.'
    const out = { summary, steps }
    if (args.timeout_seconds) out.timeout = `Allowed up to ${args.timeout_seconds}s to run.`
    return out
  }

  // Generic fallback for non-shell tools: describe the tool + its inputs.
  const keys = Object.keys(args)
  const inputs = keys.length ? ` using ${keys.join(', ')}` : ''
  return {
    summary: `This calls the ${tool || 'agent'} tool${inputs}.`,
    steps: [],
  }
}

function joinSteps(steps) {
  if (steps.length === 1) return steps[0]
  return steps.slice(0, -1).join('; ') + '; then ' + steps[steps.length - 1]
}
