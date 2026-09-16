<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'

  let secrets = []          // catalog rows from GET /secrets
  let loading = true
  let error   = ''
  let notice  = ''

  // Per-row entry buffer (name → typed value) and busy flags.
  let values  = {}          // name → value being entered/replaced
  let savingName = ''       // name currently being PUT
  let removingName = ''     // name currently being DELETEd

  // Add-custom-secret form
  let customName  = ''
  let customValue = ''
  let addingCustom = false

  // ── Gateway access keys ────────────────────────────────────────────────
  // The key you sign in with was minted on first run and echoed to a
  // terminal once. After that there was no way to see which keys existed, to
  // add one for a script or a second device, or to rotate one — the API had
  // supported all three since the beginning, and no screen called it. Someone
  // who lost the key was editing config.yaml by hand.
  let keys = []
  let keysError = ''
  let keysLoading = true
  let newKeyName = ''
  let creatingKey = false
  let revokingKeyId = ''
  // Shown exactly once, because that is the only time the server returns it.
  let freshKey = null

  async function loadKeys() {
    keysLoading = true
    try {
      const res = await api.admin.keys.list()
      keys = res.keys || []
      keysError = ''
    } catch (e) {
      keys = []
      keysError = e.status === 503
        ? 'Key management is not available on this gateway.'
        : (e.message || 'Could not load access keys.')
    } finally {
      keysLoading = false
    }
  }

  async function createKey() {
    const name = newKeyName.trim()
    if (!name || creatingKey) return
    creatingKey = true
    keysError = ''
    try {
      freshKey = await api.admin.keys.create(name)
      newKeyName = ''
      await loadKeys()
    } catch (e) {
      keysError = e.message || 'Could not create the key.'
    } finally {
      creatingKey = false
    }
  }

  async function revokeKey(k) {
    if (!confirm(`Revoke "${k.name}"? Anything still using this key stops working immediately.`)) return
    revokingKeyId = k.id
    keysError = ''
    try {
      await api.admin.keys.revoke(k.id)
      await loadKeys()
    } catch (e) {
      keysError = e.message || 'Could not revoke the key.'
    } finally {
      revokingKeyId = ''
    }
  }

  async function copyFreshKey() {
    try {
      await navigator.clipboard.writeText(freshKey.key)
      notice = 'Key copied to the clipboard.'
    } catch {
      keysError = 'Could not copy automatically — select the key and copy it.'
    }
  }

  // Display order + friendly labels for the known categories. Unknown
  // categories fall through to a generic "Other" section at the end.
  const CATEGORY_META = {
    llm:     { label: 'LLM provider keys', icon: '🤖' },
    channel: { label: 'Channel tokens',    icon: '📡' },
    server:  { label: 'Gateway / server',  icon: '🛡️' },
    tool:    { label: 'Tool & integration secrets', icon: '🧩' },
  }
  const CATEGORY_ORDER = ['llm', 'channel', 'server', 'tool']

  async function load() {
    loading = true
    error   = ''
    try {
      const res = await api.secrets.list()
      secrets = res.secrets || []
    } catch (e) {
      error = e.message
    } finally {
      loading = false
    }
  }

  function flash(msg) {
    notice = msg
    setTimeout(() => { notice = '' }, 3000)
  }

  async function save(name) {
    const value = values[name]
    if (!value) { error = 'Enter a value before saving.'; return }
    savingName = name
    error = ''; notice = ''
    try {
      await api.secrets.set(name, value)
      values = { ...values, [name]: '' }
      flash(`Saved "${name}".`)
      await load()
    } catch (e) {
      error = e.message
    } finally {
      savingName = ''
    }
  }

  async function remove(name) {
    removingName = name
    error = ''; notice = ''
    try {
      await api.secrets.delete(name)
      values = { ...values, [name]: '' }
      flash(`Removed "${name}".`)
      await load()
    } catch (e) {
      error = e.message
    } finally {
      removingName = ''
    }
  }

  async function addCustom() {
    const name = customName.trim()
    if (!name) { error = 'Custom secret name is required.'; return }
    if (!/^[A-Za-z0-9_]+$/.test(name)) {
      error = 'Name must use letters, numbers, and underscores (e.g. ALPHAVANTAGE_API_KEY).'
      return
    }
    if (!customValue) { error = 'Enter a value for the custom secret.'; return }
    addingCustom = true
    error = ''; notice = ''
    try {
      await api.secrets.set(name, customValue)
      customName = ''
      customValue = ''
      flash(`Saved "${name}".`)
      await load()
    } catch (e) {
      error = e.message
    } finally {
      addingCustom = false
    }
  }

  // Group catalog rows by category, preserving the preferred order and
  // appending any unrecognised categories under "Other".
  $: grouped = (() => {
    const buckets = {}
    for (const s of secrets) {
      const cat = s.category || 'other'
      ;(buckets[cat] ||= []).push(s)
    }
    const ordered = []
    for (const cat of CATEGORY_ORDER) {
      if (buckets[cat]) ordered.push([cat, buckets[cat]])
    }
    for (const cat of Object.keys(buckets)) {
      if (!CATEGORY_ORDER.includes(cat)) ordered.push([cat, buckets[cat]])
    }
    return ordered
  })()

  function catMeta(cat) {
    return CATEGORY_META[cat] || { label: cat.charAt(0).toUpperCase() + cat.slice(1), icon: '🔑' }
  }

  onMount(() => { load(); loadKeys() })
</script>

<div class="page">
  <div class="page-header">
    <h1>Secrets</h1>
    <div class="header-actions">
      <button class="btn-secondary" on:click={load} disabled={loading}>↺ Refresh</button>
    </div>
        <TourButton />
    </div>

  <div class="note-card">
    Secrets are stored encrypted in your workspace (<code>~/.soulacy/soulspace</code>)
    and are never written back to config files.
  </div>

  {#if error}<div class="banner err">{error}</div>{/if}
  {#if notice}<div class="banner ok">{notice}</div>{/if}

  <!-- Gateway access keys. Above the vault because this is the credential a
       person is most likely to have lost, and the one they cannot recover
       from any other screen. -->
  <div class="section keys-section">
    <div class="section-head">
      <h2>🔑 Gateway access keys</h2>
      <span class="section-sub">Used to sign in here, pair a device, or call the API from a script.</span>
    </div>

    {#if freshKey}
      <div class="fresh-key">
        <div class="fk-head">
          <strong>Copy this now — it is shown once.</strong>
          <button class="linkish" on:click={() => { freshKey = null }}>Done</button>
        </div>
        <code class="fk-value">{freshKey.key}</code>
        <button class="btn-secondary btn-sm" on:click={copyFreshKey}>Copy</button>
      </div>
    {/if}

    {#if keysError}<div class="banner err">{keysError}</div>{/if}

    {#if keysLoading}
      <div class="empty">Loading keys…</div>
    {:else if keys.length === 0 && !keysError}
      <div class="empty">No keys recorded. The key you are signed in with may predate key management.</div>
    {:else}
      <div class="key-list">
        {#each keys as k (k.id)}
          <div class="key-row" class:revoked={k.revoked_at}>
            <div class="key-main">
              <span class="key-name">{k.name}</span>
              <code class="key-prefix">{k.prefix}…</code>
            </div>
            <div class="key-side">
              {#if k.revoked_at}
                <span class="key-revoked">revoked</span>
              {:else}
                <button class="linkish danger" disabled={revokingKeyId === k.id} on:click={() => revokeKey(k)}>
                  {revokingKeyId === k.id ? 'Revoking…' : 'Revoke'}
                </button>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}

    <div class="key-add">
      <input
        type="text"
        placeholder="Name it — e.g. Laptop, Phone, Backup script"
        bind:value={newKeyName}
        on:keydown={(e) => { if (e.key === 'Enter') createKey() }}
      />
      <button class="btn-secondary btn-sm" disabled={!newKeyName.trim() || creatingKey} on:click={createKey}>
        {creatingKey ? 'Creating…' : 'New key'}
      </button>
    </div>
    <p class="key-note">
      To rotate: create a new key, switch over to it, then revoke the old one. Revoking is immediate.
    </p>
  </div>

  {#if loading}
    <div class="empty">Loading secrets…</div>
  {:else}
    {#each grouped as [cat, rows] (cat)}
      <div class="section">
        <h2 class="section-title">
          <span class="cat-icon">{catMeta(cat).icon}</span> {catMeta(cat).label}
        </h2>
        <div class="secret-list">
          {#each rows as s (s.name)}
            <div class="secret-row">
              <div class="secret-info">
                <div class="secret-head">
                  <code class="secret-name">{s.name}</code>
                  {#if s.set}
                    <span class="badge set">Set ✓</span>
                  {:else}
                    <span class="badge unset">Not set</span>
                  {/if}
                </div>
                {#if s.description}
                  <p class="secret-desc">{s.description}</p>
                {/if}
                {#if s.env_var}
                  <p class="secret-env">
                    Env fallback: <code>{s.env_var}</code>
                  </p>
                {/if}
              </div>
              <div class="secret-actions">
                <input
                  type="password"
                  class="secret-input"
                  autocomplete="off"
                  bind:value={values[s.name]}
                  placeholder={s.set ? '••••••  (leave blank to keep current)' : 'Enter value…'}
                />
                <button
                  class="btn-primary small-btn"
                  on:click={() => save(s.name)}
                  disabled={savingName === s.name || !values[s.name]}>
                  {savingName === s.name ? 'Saving…' : 'Save'}
                </button>
                {#if s.set}
                  <button
                    class="btn-danger small-btn"
                    on:click={() => remove(s.name)}
                    disabled={removingName === s.name}>
                    {removingName === s.name ? 'Removing…' : 'Remove'}
                  </button>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      </div>
    {/each}

    <!-- Add an arbitrary tool/integration secret by name -->
    <div class="section">
      <h2 class="section-title"><span class="cat-icon">➕</span> Add custom secret</h2>
      <p class="hint">
        For tool or integration keys not in the catalog above
        (e.g. <code>ALPHAVANTAGE_API_KEY</code>). Stored under the name you
        provide and resolved like any other secret.
      </p>
      <div class="custom-row">
        <input
          type="text"
          class="custom-name"
          placeholder="SECRET_NAME"
          bind:value={customName}
        />
        <input
          type="password"
          class="custom-value"
          autocomplete="off"
          placeholder="value…"
          bind:value={customValue}
        />
        <button
          class="btn-primary small-btn"
          on:click={addCustom}
          disabled={addingCustom || !customName.trim() || !customValue}>
          {addingCustom ? 'Saving…' : 'Add secret'}
        </button>
      </div>
    </div>
  {/if}
</div>

<style>
  /* Gateway access keys. */
  .keys-section { margin-bottom: 1.4rem; }
  .section-head { display: flex; align-items: baseline; gap: .6rem; flex-wrap: wrap; margin-bottom: .6rem; }
  .section-head h2 { font-size: .95rem; font-weight: 600; }
  .section-sub { color: var(--sl-text-faint); font-size: .76rem; }

  .fresh-key {
    border: 1px solid rgba(76,175,130,.4); background: rgba(76,175,130,.08);
    border-radius: 8px; padding: .7rem .85rem; margin-bottom: .7rem;
    display: flex; flex-direction: column; gap: .45rem; align-items: flex-start;
  }
  .fk-head { display: flex; justify-content: space-between; width: 100%; align-items: center; }
  .fk-head strong { color: #4caf82; font-size: .82rem; }
  .fk-value {
    width: 100%; word-break: break-all; background: #11131f; border: 1px solid var(--sl-line);
    border-radius: 6px; padding: .45rem .6rem; font-size: .78rem; color: var(--sl-text);
  }

  .key-list { display: flex; flex-direction: column; gap: .35rem; }
  .key-row {
    display: flex; align-items: center; justify-content: space-between; gap: 1rem;
    padding: .5rem .65rem; border: 1px solid var(--sl-line); border-radius: 8px; background: #11131f;
  }
  .key-row.revoked { opacity: .5; }
  .key-main { display: flex; align-items: baseline; gap: .6rem; flex-wrap: wrap; }
  .key-name { font-size: .85rem; }
  .key-prefix { color: var(--sl-text-faint); font-size: .74rem; }
  .key-revoked { color: var(--sl-text-faint); font-size: .72rem; }
  .danger { color: #f06060; }

  .key-add { display: flex; gap: .4rem; margin-top: .6rem; }
  .key-add input {
    flex: 1; background: #11131f; border: 1px solid var(--sl-line); border-radius: 6px;
    padding: .4rem .55rem; color: inherit; font-size: .8rem;
  }
  .key-note { color: var(--sl-text-faint); font-size: .72rem; margin-top: .4rem; }

  .page        { padding: 1.5rem; display: flex; flex-direction: column; gap: 1.25rem; }
  .page-header { display: flex; align-items: center; justify-content: space-between; }
  .page-header h1 { font-size: 1.2rem; font-weight: 600; }
  .header-actions { display: flex; gap: .5rem; }

  .note-card {
    background: rgba(108,99,255,.06); border: 1px solid rgba(108,99,255,.18);
    border-radius: 8px; padding: .7rem 1rem; font-size: .82rem; color: #9b96e8; line-height: 1.6;
  }
  .note-card code { background: #1c1f35; padding: .08rem .35rem; border-radius: 4px; font-size: .76rem; color: #8b85ff; }

  .banner { padding: .7rem 1rem; border-radius: 8px; font-size: .85rem; }
  .err { background: rgba(240,96,96,.1); border: 1px solid rgba(240,96,96,.3); color: #f06060; }
  .ok  { background: rgba(76,175,130,.1); border: 1px solid rgba(76,175,130,.3); color: #4caf82; }
  .empty { padding: 3rem; text-align: center; color: var(--sl-text-faint); }

  .section {
    background: var(--sl-surface); border: 1px solid var(--sl-line); border-radius: 10px;
    padding: 1.1rem 1.25rem; display: flex; flex-direction: column; gap: .85rem;
  }
  .section-title {
    font-size: .78rem; font-weight: 600; text-transform: uppercase; letter-spacing: .06em;
    color: #555a7a; display: flex; align-items: center; gap: .4rem;
  }
  .cat-icon { font-size: .95rem; }

  .secret-list { display: flex; flex-direction: column; gap: .65rem; }
  .secret-row {
    display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem;
    padding: .75rem .85rem; background: #10121f; border: 1px solid var(--sl-line); border-radius: 8px;
  }
  .secret-info { display: flex; flex-direction: column; gap: .3rem; min-width: 0; flex: 1; }
  .secret-head { display: flex; align-items: center; gap: .55rem; flex-wrap: wrap; }
  .secret-name { font-family: monospace; font-size: .82rem; color: #8b85ff; overflow-wrap: anywhere; }
  .secret-desc { font-size: .8rem; color: #c8cadf; line-height: 1.5; }
  .secret-env  { font-size: .72rem; color: var(--sl-text-faint); }
  .secret-env code { background: #1c1f35; padding: .05rem .3rem; border-radius: 4px; color: #7b82a8; }

  .badge { font-size: .65rem; padding: .12rem .5rem; border-radius: 999px; font-weight: 600; }
  .badge.set   { background: rgba(76,175,130,.18); color: #4caf82; }
  .badge.unset { background: #1c1f35; color: var(--sl-text-faint); }

  .secret-actions {
    display: flex; align-items: center; gap: .4rem; flex-shrink: 0;
    flex-wrap: wrap; justify-content: flex-end; max-width: 420px;
  }
  .secret-input { width: 220px; font-family: monospace; font-size: .8rem; }
  .small-btn { padding: .35rem .8rem; font-size: .78rem; border-radius: 6px; white-space: nowrap; }

  .hint { font-size: .78rem; color: var(--sl-text-faint); line-height: 1.6; }
  .hint code { background: #1c1f35; padding: .08rem .35rem; border-radius: 4px; font-size: .72rem; color: #8b85ff; }

  .custom-row { display: flex; gap: .4rem; align-items: center; flex-wrap: wrap; }
  .custom-name  { flex: 0 0 220px; font-family: monospace; font-size: .8rem; }
  .custom-value { flex: 1; min-width: 160px; font-family: monospace; font-size: .8rem; }

  @media (max-width: 720px) {
    .secret-row { flex-direction: column; }
    .secret-actions { max-width: none; width: 100%; justify-content: flex-start; }
    .secret-input { flex: 1; width: auto; min-width: 0; }
  }
</style>
