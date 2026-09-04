<script>
  // ShareView — a public, read-only render of a shared conversation. Reached at
  // /#share/<token> and rendered OUTSIDE the authenticated app shell, so no API
  // key is needed. Fetches the snapshot with a plain (unauthenticated) fetch.
  import { onMount } from 'svelte'
  import { parseMarkdown } from '../lib/markdown.js'

  export let token = ''

  let loading = true
  let error = ''
  let session = null

  onMount(async () => {
    try {
      // Plain fetch — never send the API key; the public route needs none.
      const res = await fetch(`/api/v1/shared/${encodeURIComponent(token)}`)
      if (!res.ok) {
        error = res.status === 404
          ? 'This shared conversation was not found. The link may be wrong or it may have been removed.'
          : `Could not load this conversation (error ${res.status}).`
        return
      }
      session = await res.json()
    } catch (_) {
      error = 'Could not reach the server to load this conversation.'
    } finally {
      loading = false
    }
  })

  const roleLabel = (r) => (r === 'user' ? 'User' : r === 'assistant' ? 'Assistant' : 'System')
  function fmtTs(ts) {
    if (!ts) return ''
    try { return new Date(ts).toLocaleString() } catch (_) { return '' }
  }
</script>

<div class="share-page">
  <header class="share-header">
    <div class="brand">Soulacy</div>
    {#if session}<h1>{session.title || 'Shared conversation'}</h1>{/if}
    {#if session?.agent_name}<div class="meta">Agent: {session.agent_name}</div>{/if}
    <div class="ro-badge">Read-only shared view</div>
  </header>

  {#if loading}
    <div class="state">Loading…</div>
  {:else if error}
    <div class="state err">{error}</div>
  {:else if session}
    <div class="thread">
      {#each session.messages || [] as m}
        <div class="msg role-{m.role}">
          <div class="msg-head">
            <span class="role">{roleLabel(m.role)}</span>
            {#if m.ts}<span class="ts">{fmtTs(m.ts)}</span>{/if}
          </div>
          <div class="bubble md">{@html parseMarkdown(m.text || '')}</div>
          {#if m.attachments?.length}
            <div class="atts">📎 {m.attachments.map(a => a.name).filter(Boolean).join(', ')}</div>
          {/if}
        </div>
      {/each}
    </div>
    <footer class="share-footer">
      Shared from Soulacy · <a href="/">Open Soulacy</a>
    </footer>
  {/if}
</div>

<style>
  .share-page { max-width: 820px; margin: 0 auto; padding: 28px 20px 60px; color: #e6e8f5; }
  .share-header { border-bottom: 1px solid #232743; padding-bottom: 14px; margin-bottom: 18px; }
  .brand { color: #8b85ff; font-weight: 700; letter-spacing: .04em; font-size: .8rem; text-transform: uppercase; }
  .share-header h1 { margin: 6px 0 2px; font-size: 1.4rem; }
  .meta { color: #8f96bb; font-size: .82rem; }
  .ro-badge { display: inline-block; margin-top: 8px; font-size: .68rem; color: #a7d3ff;
    background: rgba(80,140,255,.12); border: 1px solid rgba(80,140,255,.3); border-radius: 999px; padding: 2px 10px; }
  .state { color: #8f96bb; padding: 40px 0; text-align: center; }
  .state.err { color: #ff9a9a; }
  .thread { display: flex; flex-direction: column; gap: 16px; }
  .msg { border: 1px solid #232743; border-radius: 10px; background: #111426; padding: 12px 14px; }
  .msg.role-user { background: #141834; }
  .msg-head { display: flex; align-items: baseline; gap: 10px; margin-bottom: 6px; }
  .role { font-weight: 600; font-size: .78rem; color: #cfd3ee; }
  .msg.role-assistant .role { color: #72d9aa; }
  .ts { color: #6b7196; font-size: .7rem; }
  .bubble { font-size: .9rem; line-height: 1.5; word-break: break-word; }
  .atts { margin-top: 8px; color: #8f96bb; font-size: .74rem; }
  .share-footer { margin-top: 24px; padding-top: 14px; border-top: 1px solid #232743; color: #8f96bb; font-size: .78rem; }
  .share-footer a { color: #8b85ff; }
  /* Basic markdown element spacing for the read-only bubble. */
  .bubble.md :global(pre) { background: #0d0f1c; padding: 10px 12px; border-radius: 8px; overflow-x: auto; }
  .bubble.md :global(code) { font-family: ui-monospace, Menlo, monospace; font-size: .82em; }
  .bubble.md :global(a) { color: #8b85ff; }
  .bubble.md :global(table) { border-collapse: collapse; }
  .bubble.md :global(td), .bubble.md :global(th) { border: 1px solid #2a2f52; padding: 4px 8px; }
</style>
