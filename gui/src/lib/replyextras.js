// replyextras.js — what sits under a reply in the chat-first shell: a card for
// each file the work produced, and a few one-tap next steps.
//
// The gateway does not send suggested follow-ups, so the next steps here are
// read off the SHAPE of the reply (a list, a long answer, a file), never off
// its meaning. That keeps them honest: a chip only appears when the shape makes
// the action obviously available, and a reply that ends by asking the person
// something gets none — the next move there is to answer.

const KINDS = [
  [/\.(md|markdown|txt|rtf|docx?|pdf|pages)$/i, 'Document'],
  [/\.(csv|tsv|xlsx?|numbers)$/i, 'Spreadsheet'],
  [/\.(pptx?|key)$/i, 'Presentation'],
  [/\.(png|jpe?g|gif|webp|svg|heic)$/i, 'Image'],
  [/\.(mp3|wav|m4a|aac|ogg|flac)$/i, 'Audio'],
  [/\.(mp4|mov|webm|mkv)$/i, 'Video'],
  [/\.(json|ya?ml|xml|toml)$/i, 'Data'],
  [/\.(html?|css|js|ts|py|go|sh|sql)$/i, 'Code'],
  [/\.(zip|tar|gz|tgz)$/i, 'Archive'],
]

/** A plain word for what a file is, from its name. */
export function artifactKind(name) {
  const n = String(name || '')
  for (const [re, kind] of KINDS) if (re.test(n)) return kind
  return 'File'
}

/** "weekly-preparation_brief.md" → "Weekly preparation brief". */
export function artifactTitle(name) {
  const base = String(name || '').split(/[\\/]/).pop().replace(/\.[a-z0-9]{1,8}$/i, '')
  const words = base.replace(/[_-]+/g, ' ').replace(/\s+/g, ' ').trim()
  if (!words) return 'Untitled file'
  return words.charAt(0).toUpperCase() + words.slice(1)
}

export function formatBytes(n) {
  const bytes = Number(n)
  if (!Number.isFinite(bytes) || bytes <= 0) return ''
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

/** The card's second line: what it is, how big, and that it is ready. */
export function artifactSubtitle({ name = '', size_bytes } = {}) {
  return [artifactKind(name), formatBytes(size_bytes), 'Ready to open'].filter(Boolean).join(' · ')
}

const FOLLOW_UPS = {
  plan:      { label: 'Turn this into a plan',     prompt: 'Turn this into a step-by-step plan, with what to do first and by when.' },
  prioritize:{ label: 'Help me prioritize',        prompt: 'Rank these by importance and tell me which one to start with, and why.' },
  shorter:   { label: 'Give me the short version', prompt: 'Summarize that in three sentences.' },
  file:      { label: 'Walk me through the file',  prompt: 'Walk me through what is in the file you produced.' },
}

/**
 * Up to three next steps for a reply, most specific first.
 * @param {string} text      the reply, as markdown
 * @param {{hasFiles?: boolean, limit?: number}} [opts]
 */
export function followUpsFor(text, { hasFiles = false, limit = 3 } = {}) {
  const body = String(text || '').replace(/```[\s\S]*?```/g, '').trim()
  if (!body && !hasFiles) return []
  const lines = body.split('\n').map((l) => l.trim()).filter(Boolean)
  // The reply hands the turn back with a question: answering is the next step.
  if (lines.length && /\?\s*$/.test(lines[lines.length - 1])) return []

  const numbered = lines.filter((l) => /^\d+[.)]\s+\S/.test(l)).length
  const bulleted = lines.filter((l) => /^[-*•]\s+\S/.test(l)).length
  const out = []
  if (numbered >= 2) out.push(FOLLOW_UPS.plan)
  else if (bulleted >= 3) out.push(FOLLOW_UPS.prioritize)
  if (hasFiles) out.push(FOLLOW_UPS.file)
  if (body.length > 900) out.push(FOLLOW_UPS.shorter)
  return out.slice(0, limit)
}
