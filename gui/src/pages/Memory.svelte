<script>
  import TourButton from '../lib/TourButton.svelte'
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { diffLines, diffStats, sourceBadge } from '../lib/rulediff.js'

  // ── State ──────────────────────────────────────────────────────────────────
  let agentStats   = []
  let selectedID   = ''
  let activeTab    = 'episodic'

  // Episodic
  let episodic     = []
  let epSearch     = ''
  let epLoading    = false
  let expandedIDs  = new Set()

  // Procedural
  let procRules    = ''
  let procDraft    = ''
  let procSaving   = false
  // Rulebook history (E23)
  let rbVersions = []
  let rbLocked = false
  let rbShowHistory = false
  let rbDiff = null        // {version, lines, stats}
  let rbBusy = false

  async function loadRulebook() {
    try {
      const res = await api.brainMemory.rulebook(selectedID)
      rbVersions = res.versions || []
      rbLocked = !!res.locked
    } catch { rbVersions = []; rbLocked = false }
  }

  async function toggleLock() {
    rbBusy = true
    try {
      await api.brainMemory.rulebookLock(selectedID, !rbLocked)
      rbLocked = !rbLocked
    } catch (e) { alert(e.message) } finally { rbBusy = false }
  }

  async function viewDiff(v) {
    rbBusy = true
    try {
      const res = await api.brainMemory.rulebookVersion(selectedID, v.version)
      const lines = diffLines(res.rules, procRules)
      rbDiff = { version: v.version, lines, stats: diffStats(lines) }
    } catch (e) { alert(e.message) } finally { rbBusy = false }
  }

  async function rollbackTo(v) {
    if (!confirm(`Roll back ${selectedID} to rulebook v${v.version}? This creates a new version.`)) return
    rbBusy = true
    try {
      await api.brainMemory.rulebookRollback(selectedID, v.version)
      rbDiff = null
      await loadProcedural()
      await loadRulebook()
    } catch (e) { alert(e.message) } finally { rbBusy = false }
  }
  let procPreview  = false

  // Context preview
  let previewQuery  = ''
  let previewResult = null
  let previewing    = false

  // Learning proposals
  let proposals      = []
  let learningSummary = null
  let learningEvidence = null
  let fleetLearningEvidence = null
  let learningBusy   = false
  let reflectingRuns = false
  let proposalStatus = 'pending'
  // Story 8 AC4 — time-window filter on Learning. Values are passed straight
  // to the server's `since=` query which accepts extended-duration strings
  // ("24h", "7d", "30d"). Empty string = no window (all time).
  const learningWindowOptions = [
    { value: '',    label: 'All time' },
    { value: '24h', label: 'Last 24h' },
    { value: '7d',  label: 'Last 7 days' },
    { value: '30d', label: 'Last 30 days' },
  ]
  let learningWindow = ''
  let editingProposalID = ''
  let proposalEdit = { title: '', content: '', skill_name: '' }

  // Story 8 AC3 — per-proposal override for "promote to Studio lessons"
  // (unified lesson surface: Studio-repair-accepted + Brain-Memory-accepted).
  // Keyed by proposal id → boolean. Unset entries fall through to the
  // server's kind-based default (skill = true, everything else = false).
  let promoteOverrides = {}
  function promoteChoice(p) {
    if (!p) return false
    if (Object.prototype.hasOwnProperty.call(promoteOverrides, p.id)) {
      return promoteOverrides[p.id]
    }
    // Server serializes the resolved value as promote_to_studio_lessons_effective.
    if (typeof p.promote_to_studio_lessons_effective === 'boolean') return p.promote_to_studio_lessons_effective
    return p.kind === 'skill'
  }
  function togglePromoteChoice(p) {
    promoteOverrides = { ...promoteOverrides, [p.id]: !promoteChoice(p) }
  }

  // Write episodic modal
  let showWrite    = false
  let writeContent = ''
  let writeTags    = ''
  let writing      = false

  // Confirm clear modal
  let showClearConfirm = false
  let clearing     = false

  let error        = null
  let notice       = null
  let brainEnabled = true

  // ── Derived ───────────────────────────────────────────────────────────────
  $: filteredEp = epSearch.trim()
    ? episodic.filter(r =>
        r.content?.toLowerCase().includes(epSearch.toLowerCase()) ||
        (r.tags || []).some(t => t.toLowerCase().includes(epSearch.toLowerCase()))
      )
    : episodic

  $: selectedAgent = agentStats.find(a => a.agent_id === selectedID)
  $: procDirty = procDraft !== procRules

  // ── API helpers ───────────────────────────────────────────────────────────
  async function loadOverview() {
    try {
      const res = await api.brainMemory.stats()
      brainEnabled = res.enabled !== false
      agentStats   = res.agents || []
      if (agentStats.length && !selectedID) {
        selectedID = agentStats[0].agent_id
      }
    } catch (e) { error = e.message }
  }

  async function loadTab() {
    error = null; notice = null
    if (!selectedID) return
    if (activeTab === 'episodic')   await loadEpisodic()
    if (activeTab === 'procedural') { await loadProcedural(); await loadRulebook() }
    if (activeTab === 'preview')    previewResult = null
    if (activeTab === 'learning')   await loadLearning()
  }

  async function loadEpisodic() {
    epLoading = true
    try {
      const res = await api.brainMemory.episodic(selectedID)
      episodic = res.records || []
    } catch (e) { error = e.message }
    epLoading = false
  }

  async function loadProcedural() {
    try {
      const res = await api.brainMemory.procedural(selectedID)
      procRules = res.rules || ''
      procDraft = procRules
    } catch (e) { error = e.message }
  }

  async function saveProcedural() {
    procSaving = true; error = null
    try {
      await api.brainMemory.updateProcedural(selectedID, procDraft)
      procRules = procDraft
      notice = 'Procedural rules saved.'
      setTimeout(() => notice = null, 2500)
      await loadOverview()
    } catch (e) { error = e.message }
    procSaving = false
  }

  async function clearProcedural() {
    if (!confirm('Clear procedural rules for ' + selectedID + '?')) return
    await api.brainMemory.clearProcedural(selectedID).catch(e => error = e.message)
    procRules = ''; procDraft = ''
    await loadOverview()
  }

  async function clearAllEpisodic() {
    clearing = true; error = null
    try {
      await api.brainMemory.clearEpisodic(selectedID)
      episodic = []; showClearConfirm = false
      notice = 'Episodic records cleared.'
      setTimeout(() => notice = null, 2500)
      await loadOverview()
    } catch (e) { error = e.message }
    clearing = false
  }

  async function writeEpisodic() {
    if (!writeContent.trim()) return
    writing = true; error = null
    try {
      const tags = writeTags.split(',').map(t => t.trim()).filter(Boolean)
      await api.brainMemory.writeEpisodic(selectedID, writeContent.trim(), tags)
      writeContent = ''; writeTags = ''; showWrite = false
      await loadEpisodic()
      await loadOverview()
    } catch (e) { error = e.message }
    writing = false
  }

  async function runContextPreview() {
    if (!previewQuery.trim()) return
    previewing = true; error = null; previewResult = null
    try {
      previewResult = await api.brainMemory.contextPreview(selectedID, previewQuery)
    } catch (e) { error = e.message }
    previewing = false
  }

  async function loadLearning() {
    learningBusy = true
    try {
      // The per-agent view narrows by both agent and window; the fleet-wide
      // evidence panel narrows only by window so cross-agent trends remain
      // visible in the selected time range.
      const [res, summaryRes, evidenceRes, fleetEvidenceRes] = await Promise.all([
        api.brainMemory.learningProposals(selectedID, proposalStatus, 100, learningWindow),
        api.brainMemory.learningSummary(selectedID, learningWindow),
        api.brainMemory.learningEvidence(selectedID, learningWindow),
        api.brainMemory.learningEvidence('', learningWindow),
      ])
      proposals = res.proposals || []
      learningSummary = summaryRes.summary || null
      learningEvidence = evidenceRes.evidence || null
      fleetLearningEvidence = fleetEvidenceRes.evidence || null
    } catch (e) { error = e.message }
    learningBusy = false
  }

  async function reflectRecentRuns() {
    if (!selectedID) return
    reflectingRuns = true
    error = null
    notice = null
    try {
      const res = await api.brainMemory.reflectRecentRuns(selectedID)
      const created = res.created || 0
      const reviewed = res.reviewed || 0
      notice = created
        ? `Created ${created} learning proposal${created === 1 ? '' : 's'} from ${reviewed} recent run${reviewed === 1 ? '' : 's'}.`
        : `Reviewed ${reviewed} recent run${reviewed === 1 ? '' : 's'}; no new proposals were needed.`
      proposalStatus = 'pending'
      await loadLearning()
    } catch (e) { error = e.message }
    reflectingRuns = false
  }

  async function acceptProposal(p) {
    learningBusy = true; error = null
    try {
      const promote = promoteChoice(p)
      await api.brainMemory.acceptLearning(p.id, { promoteToStudioLessons: promote })
      const promoteNote = promote ? ' Studio will use it in future generations.' : ''
      notice = (p.kind === 'skill'
        ? 'Skill installed and added to the live catalog.'
        : p.kind === 'procedure'
          ? 'Procedure added to the rulebook.'
          : 'Learning saved to semantic memory.') + promoteNote
      // Clear the local override so the fresh server response is authoritative.
      if (Object.prototype.hasOwnProperty.call(promoteOverrides, p.id)) {
        const copy = { ...promoteOverrides }
        delete copy[p.id]
        promoteOverrides = copy
      }
      await loadLearning()
      await loadOverview()
      if (activeTab === 'learning') setTimeout(() => notice = null, 2500)
    } catch (e) { error = e.message }
    learningBusy = false
  }

  function startEditProposal(p) {
    editingProposalID = p.id
    proposalEdit = {
      title: p.title || '',
      content: p.content || '',
      skill_name: p.meta?.skill_name || '',
    }
  }

  function cancelEditProposal() {
    editingProposalID = ''
    proposalEdit = { title: '', content: '', skill_name: '' }
  }

  async function saveProposalEdit(p) {
    if (!proposalEdit.content.trim()) return
    learningBusy = true; error = null
    try {
      const meta = {}
      if (p.kind === 'skill' && proposalEdit.skill_name.trim()) meta.skill_name = proposalEdit.skill_name.trim()
      await api.brainMemory.updateLearning(p.id, {
        title: proposalEdit.title.trim(),
        content: proposalEdit.content,
        meta,
      })
      notice = 'Learning proposal updated.'
      cancelEditProposal()
      await loadLearning()
      setTimeout(() => notice = null, 2200)
    } catch (e) { error = e.message }
    learningBusy = false
  }

  async function rejectProposal(p) {
    learningBusy = true; error = null
    try {
      await api.brainMemory.rejectLearning(p.id)
      await loadLearning()
    } catch (e) { error = e.message }
    learningBusy = false
  }

  async function toggleDisableProposal(p) {
    learningBusy = true; error = null
    try {
      await api.brainMemory.disableLearning(p.id, !p.disabled)
      await loadLearning()
    } catch (e) { error = e.message }
    learningBusy = false
  }

  function toggleExpand(id) {
    if (expandedIDs.has(id)) expandedIDs.delete(id)
    else expandedIDs.add(id)
    expandedIDs = new Set(expandedIDs)
  }

  function fmtDate(iso) {
    if (!iso) return '—'
    try {
      const d = new Date(iso)
      return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
        + ' ' + d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    } catch { return iso }
  }

  function relTime(iso) {
    if (!iso) return ''
    try {
      const secs = Math.floor((Date.now() - new Date(iso)) / 1000)
      if (secs < 60) return 'just now'
      if (secs < 3600) return Math.floor(secs/60) + 'm ago'
      if (secs < 86400) return Math.floor(secs/3600) + 'h ago'
      return Math.floor(secs/86400) + 'd ago'
    } catch { return '' }
  }

  function markdownToHtml(md) {
    return md
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/^### (.+)$/gm, '<h3>$1</h3>')
      .replace(/^## (.+)$/gm, '<h2>$1</h2>')
      .replace(/^# (.+)$/gm, '<h1>$1</h1>')
      .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
      .replace(/\*(.+?)\*/g, '<em>$1</em>')
      .replace(/`([^`]+)`/g, '<code>$1</code>')
      .replace(/^- (.+)$/gm, '<li>$1</li>')
      .replace(/\n\n/g, '</p><p>')
  }

  function pct(v) {
    return Math.round((Number(v) || 0) * 100) + '%'
  }

  function topEntries(obj, limit = 3) {
    return Object.entries(obj || {})
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .slice(0, limit)
  }

  function agentName(id) {
    const a = agentStats.find(x => x.agent_id === id)
    return a?.agent_name || id
  }

  function maxTrend(ev) {
    return Math.max(1, ...((ev?.trend || []).map(b => Math.max(b.runs || 0, b.skill_uses || 0, b.errors || 0, b.accepted || 0))))
  }

  function trendLabel(iso) {
    if (!iso) return ''
    try {
      return new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
    } catch { return iso }
  }

  $: if (selectedID || activeTab) loadTab()
  onMount(loadOverview)
</script>

<div class="page">
  <!-- Header -->
  <div class="page-header">
    <div class="title-row">
      <span class="page-icon">🧠</span>
      <h1>Learning</h1>
      <span class="subtitle">Memory · Procedures · Review queue</span>
    </div>
    <div class="hdr-actions">
      <select bind:value={selectedID} class="agent-select">
        {#each agentStats as a}
          <option value={a.agent_id}>{a.agent_name || a.agent_id}</option>
        {/each}
        {#if !agentStats.length}<option value="">No agents</option>{/if}
      </select>
      <button class="btn-icon" title="Refresh" on:click={loadOverview}>↺</button>
    </div>
        <TourButton />
    </div>

  {#if !brainEnabled}
    <div class="banner warn">
      ⚠ Learning memory is not enabled. Set <code>SOULACY_MEMORY_DIR</code> and restart Soulacy.
    </div>
  {/if}
  {#if error}  <div class="banner err">⚠ {error}</div>{/if}
  {#if notice} <div class="banner ok">✓ {notice}</div>{/if}

  <!-- Stats row -->
  {#if selectedAgent}
    <div class="stats-row">
      <div class="stat-card">
        <div class="stat-val">{selectedAgent.episodic_count}</div>
        <div class="stat-lbl">Episodic records</div>
      </div>
      <div class="stat-card">
        <div class="stat-val {selectedAgent.has_procedural ? 'green' : 'dim'}">{selectedAgent.has_procedural ? '✓ Active' : '—'}</div>
        <div class="stat-lbl">Procedural rules</div>
      </div>
      <div class="stat-card">
        <div class="stat-val dim small">{selectedAgent.last_activity ? relTime(selectedAgent.last_activity) : 'never'}</div>
        <div class="stat-lbl">Last activity</div>
      </div>
      <div class="stat-card mono-val">
        <div class="stat-val dim small">{selectedAgent.agent_id}</div>
        <div class="stat-lbl">Agent ID</div>
      </div>
    </div>
  {/if}

  <!-- Agent chips -->
  {#if agentStats.length > 1}
    <div class="agent-grid">
      {#each agentStats as a}
        <button class="agent-chip {a.agent_id === selectedID ? 'active' : ''}"
          on:click={() => { selectedID = a.agent_id }}>
          <span class="chip-name">{a.agent_name || a.agent_id}</span>
          <div class="chip-badges">
            {#if a.episodic_count > 0}<span class="cbadge ep">{a.episodic_count}</span>{/if}
            {#if a.has_procedural}<span class="cbadge proc">proc</span>{/if}
          </div>
        </button>
      {/each}
    </div>
  {/if}

  <!-- Tabs -->
  <div class="tabs">
    <button class="tab {activeTab==='episodic'?'active':''}" on:click={() => activeTab='episodic'}>
      🕐 Episodic {#if episodic.length}<span class="tab-count">{episodic.length}</span>{/if}
    </button>
    <button class="tab {activeTab==='procedural'?'active':''}" on:click={() => activeTab='procedural'}>
      📋 Procedural {#if procDirty}<span class="tab-dot"></span>{/if}
    </button>
    <button class="tab {activeTab==='preview'?'active':''}" on:click={() => activeTab='preview'}>
      🔍 Context Preview
    </button>
    <button class="tab {activeTab==='learning'?'active':''}" on:click={() => activeTab='learning'}>
      ✨ Learning {#if proposals.length}<span class="tab-count">{proposals.length}</span>{/if}
    </button>
  </div>

  <!-- ══ EPISODIC ══════════════════════════════════════════════════════════ -->
  {#if activeTab === 'episodic'}
    <div class="tab-toolbar">
      <input class="search-input" bind:value={epSearch} placeholder="Search records…" />
      <div style="flex:1"></div>
      <button class="btn-secondary" on:click={() => showWrite=true}>+ Write</button>
      {#if episodic.length}<button class="btn-danger-outline" on:click={() => showClearConfirm=true}>Clear all</button>{/if}
    </div>

    {#if epLoading}
      <div class="empty-state"><div class="spinner"></div></div>
    {:else if filteredEp.length === 0}
      <div class="empty-state">
        <div class="empty-icon">🕐</div>
        <p>{epSearch ? 'No records match.' : 'No episodic records yet. Tasks run with brain_memory.episodic.enabled: true will appear here.'}</p>
      </div>
    {:else}
      <div class="timeline">
        {#each filteredEp as rec (rec.id || rec.timestamp)}
          {@const xp = expandedIDs.has(rec.id || rec.timestamp)}
          <div class="tl-item {xp?'expanded':''}">
            <div class="tl-dot"></div>
            <div class="tl-card" role="button" tabindex="0" aria-expanded={xp}
                 on:click={() => toggleExpand(rec.id || rec.timestamp)}
                 on:keydown={(e) => (e.key === 'Enter' || e.key === ' ') && (e.preventDefault(), toggleExpand(rec.id || rec.timestamp))}>
              <div class="tl-header">
                <span class="tl-time">{fmtDate(rec.timestamp)}</span>
                <span class="tl-rel">{relTime(rec.timestamp)}</span>
                <div style="flex:1"></div>
                {#each (rec.tags||[]) as tag}<span class="tag">{tag}</span>{/each}
                <span class="tl-chevron">{xp?'▲':'▼'}</span>
              </div>
              <div class="tl-preview">{rec.content?.slice(0, xp?99999:180)}{!xp&&(rec.content?.length||0)>180?'…':''}</div>
              {#if xp && rec.id}
                <div class="tl-meta">
                  <span class="meta-item">ID: <code>{rec.id.slice(0,12)}…</code></span>
                  <span class="meta-item">Type: <code>{rec.type||'episodic'}</code></span>
                </div>
              {/if}
            </div>
          </div>
        {/each}
      </div>
      <div class="list-footer">{filteredEp.length} record{filteredEp.length!==1?'s':''}{epSearch&&filteredEp.length!==episodic.length?' (filtered from '+episodic.length+')':''}</div>
    {/if}
  {/if}

  <!-- ══ PROCEDURAL ════════════════════════════════════════════════════════ -->
  {#if activeTab === 'procedural'}
    <div class="tab-toolbar">
      <span class="proc-info">Operating rules injected as <code>## Operating rules</code> in the system prompt.</span>
      <div style="flex:1"></div>
      <button class="btn-icon {procPreview?'active':''}" on:click={() => procPreview=!procPreview} title="Toggle preview">{procPreview?'✎':'👁'}</button>
      {#if procDirty}
        <button class="btn-secondary" on:click={() => { procDraft=procRules }}>Reset</button>
        <button class="btn-primary" on:click={saveProcedural} disabled={procSaving}>{procSaving?'Saving…':'Save'}</button>
      {/if}
      {#if procRules}<button class="btn-danger-outline" on:click={clearProcedural} disabled={rbLocked}>Clear</button>{/if}
      <button class="btn-secondary {rbLocked?'locked':''}" on:click={toggleLock} disabled={rbBusy}
        title={rbLocked ? 'Rules are FROZEN — auto-updates and edits refused' : 'Freeze rules against drift'}>
        {rbLocked ? '🔒 Locked' : '🔓 Lock'}
      </button>
      <button class="btn-secondary" on:click={() => rbShowHistory=!rbShowHistory}>
        ⧗ History ({rbVersions.length})
      </button>
    </div>
    {#if rbLocked}
      <div class="proc-hint locked-hint">
        <span class="hint-icon">🔒</span>
        <p>This rulebook is locked: auto-updates from the reasoning loop and manual edits are refused until unlocked (drift control, Story E23).</p>
      </div>
    {/if}

    <div class="proc-pane {procPreview?'split':''}">
      <div class="proc-editor-wrap">
        <textarea class="proc-editor" bind:value={procDraft}
          placeholder="# Operating Rules&#10;&#10;- Always cite sources&#10;- Be concise&#10;&#10;The agent auto-updates these after each task when auto_update: true."
          spellcheck="false"></textarea>
        {#if procDirty}<div class="dirty-badge">unsaved</div>{/if}
      </div>
      {#if procPreview}
        <div class="proc-preview">
          {#if procDraft.trim()}{@html markdownToHtml(procDraft)}{:else}<div class="empty-state-sm">Nothing to preview.</div>{/if}
        </div>
      {/if}
    </div>

    {#if !procRules && !procDirty}
      <div class="proc-hint">
        <span class="hint-icon">💡</span>
        <p>No rules yet. Set <code>brain_memory.procedural.auto_update: true</code> in SOUL.yaml and they will be generated after each task.</p>
      </div>
    {/if}

    {#if rbShowHistory}
      <div class="rb-history">
        <h3>Rulebook history</h3>
        {#if rbVersions.length === 0}
          <p class="proc-info">No versions recorded yet — every save or auto-update from now on lands here.</p>
        {:else}
          {#each rbVersions as v (v.version)}
            {@const badge = sourceBadge(v.source)}
            <div class="rb-row">
              <span class="rb-ver">v{v.version}</span>
              <span class="rb-badge {badge.cls}">{badge.label}</span>
              <span class="rb-meta">{new Date(v.created_at).toLocaleString()} · {v.size} B</span>
              <div style="flex:1"></div>
              <button class="btn-secondary rb-btn" on:click={() => viewDiff(v)} disabled={rbBusy}>Diff vs current</button>
              <button class="btn-secondary rb-btn" on:click={() => rollbackTo(v)} disabled={rbBusy || rbLocked}>Roll back</button>
            </div>
          {/each}
        {/if}
        {#if rbDiff}
          <div class="rb-diff">
            <div class="rb-diff-head">
              <strong>v{rbDiff.version} → current</strong>
              <span class="rb-meta">+{rbDiff.stats.added} −{rbDiff.stats.removed}</span>
              <div style="flex:1"></div>
              <button class="btn-secondary rb-btn" on:click={() => rbDiff=null}>✕</button>
            </div>
            <pre class="rb-diff-body">{#each rbDiff.lines as l}<span class="dl {l.type}">{l.type==='add'?'+ ':l.type==='del'?'− ':'  '}{l.text}\n</span>{/each}</pre>
          </div>
        {/if}
      </div>
    {/if}
  {/if}

  <!-- ══ CONTEXT PREVIEW ═══════════════════════════════════════════════════ -->
  {#if activeTab === 'preview'}
    <div class="preview-pane">
      <p class="preview-intro">Preview exactly what memory context will be injected into the system prompt for a given task. Uses <code>BuildContextBlock()</code> with the current memory store.</p>
      <div class="preview-form">
        <textarea class="preview-input" bind:value={previewQuery} rows="3"
          placeholder="e.g. Research the latest developments in agentic AI frameworks…"></textarea>
        <button class="btn-primary" on:click={runContextPreview}
          disabled={previewing||!previewQuery.trim()}>{previewing?'Previewing…':'Preview context →'}</button>
      </div>
      {#if previewResult}
        <div class="preview-meta">
          <div class="meta-chip"><span class="mc-num">{previewResult.episodic_count}</span><span class="mc-lbl">episodic</span></div>
          <div class="meta-chip"><span class="mc-num">{previewResult.semantic_count}</span><span class="mc-lbl">semantic</span></div>
          <div class="meta-chip"><span class="mc-num {previewResult.has_procedural?'green':'dim'}">{previewResult.has_procedural?'✓':'—'}</span><span class="mc-lbl">procedural</span></div>
          <div class="meta-chip"><span class="mc-num">~{previewResult.token_estimate}</span><span class="mc-lbl">tokens</span></div>
        </div>
        {#if previewResult.context_block}
          <div class="context-block-label">Injected context block:</div>
          <pre class="context-block">{previewResult.context_block}</pre>
        {:else}
          <div class="empty-state-sm">No memory to inject for this query yet.</div>
        {/if}
      {/if}
    </div>
  {/if}

  <!-- ══ LEARNING PROPOSALS ════════════════════════════════════════════════ -->
  {#if activeTab === 'learning'}
    <div class="tab-toolbar">
      <span class="proc-info">Review post-run proposals before Soulacy writes them into memory or rules.</span>
      <div style="flex:1"></div>
      <select bind:value={proposalStatus} on:change={loadLearning}>
        <option value="pending">Pending</option>
        <option value="accepted">Accepted</option>
        <option value="rejected">Rejected</option>
        <option value="">All</option>
      </select>
      <select bind:value={learningWindow} on:change={loadLearning} title="Time window">
        {#each learningWindowOptions as opt}
          <option value={opt.value}>{opt.label}</option>
        {/each}
      </select>
      <button class="btn-secondary" on:click={reflectRecentRuns} disabled={learningBusy || reflectingRuns || !selectedID}>
        {reflectingRuns ? 'Reflecting…' : 'Reflect recent runs'}
      </button>
      <button class="btn-secondary" on:click={loadLearning} disabled={learningBusy}>{learningBusy?'Refreshing…':'Refresh'}</button>
    </div>
    {#if learningSummary}
      <div class="learning-health">
        <div class="lh-card" class:urgent={learningSummary.pending > 0}>
          <div class="lh-val">{learningSummary.pending || 0}</div>
          <div class="lh-label">Pending review</div>
        </div>
        <div class="lh-card">
          <div class="lh-val green">{learningSummary.accepted || 0}</div>
          <div class="lh-label">Accepted</div>
        </div>
        <div class="lh-card">
          <div class="lh-val">{learningSummary.installed_skills || 0}</div>
          <div class="lh-label">Installed skills</div>
        </div>
        <div class="lh-card">
          <div class="lh-val">{pct(learningSummary.average_confidence)}</div>
          <div class="lh-label">Avg confidence</div>
        </div>
        <div class="lh-card">
          <div class="lh-val green">{learningSummary.background_runs || 0}</div>
          <div class="lh-label">Auto-reflections</div>
          {#if learningSummary.latest_background}
            <div class="lh-sub">{relTime(learningSummary.latest_background)}</div>
          {/if}
        </div>
        <div class="lh-card wide">
          <div class="lh-provenance">
            <span>Sources</span>
            {#each topEntries(learningSummary.by_source) as [k, v]}
              <code>{k}: {v}</code>
            {:else}
              <code>none yet</code>
            {/each}
          </div>
          <div class="lh-provenance">
            <span>Tools</span>
            {#each topEntries(learningSummary.by_tool) as [k, v]}
              <code>{k}: {v}</code>
            {:else}
              <code>none captured</code>
            {/each}
          </div>
        </div>
      </div>
    {/if}
    {#if learningEvidence && (learningEvidence.accepted_skills > 0 || (learningEvidence.repeated_errors && learningEvidence.repeated_errors.length))}
      <div class="learning-evidence">
        <div class="le-head">
          <span class="le-title">Is it working?</span>
          <span class="le-sub">Evidence that accepted learnings reduce repeat work and repeat failures.</span>
        </div>
        <div class="le-grid">
          <div class="le-metric">
            <div class="le-val green">{learningEvidence.reused_skills || 0}<span class="le-of">/{learningEvidence.accepted_skills || 0}</span></div>
            <div class="le-label">Accepted skills reused</div>
          </div>
          <div class="le-metric">
            <div class="le-val">{learningEvidence.total_skill_uses || 0}</div>
            <div class="le-label">Total skill reuses</div>
          </div>
          <div class="le-metric">
            <div class="le-val" class:green={(learningEvidence.error_reduction || 0) > 0}>{pct(learningEvidence.error_reduction)}</div>
            <div class="le-label">Repeat-error reduction</div>
            <div class="le-sub2">{learningEvidence.errors_before || 0} → {learningEvidence.errors_after || 0} after learning</div>
          </div>
        </div>
        {#if learningEvidence.skill_reuse && learningEvidence.skill_reuse.filter(s => s.uses > 0).length}
          <div class="le-list">
            <span class="le-list-title">Reused skills</span>
            {#each learningEvidence.skill_reuse.filter(s => s.uses > 0) as s}
              <div class="le-row">
                <code>{s.skill_name}</code>
                <span class="le-row-meta">{s.uses} use{s.uses === 1 ? '' : 's'} · {s.sessions} session{s.sessions === 1 ? '' : 's'}{#if s.last_used_at} · {relTime(s.last_used_at)}{/if}</span>
              </div>
            {/each}
          </div>
        {/if}
        {#if learningEvidence.repeated_errors && learningEvidence.repeated_errors.length}
          <div class="le-list">
            <span class="le-list-title">Recurring failures</span>
            {#each learningEvidence.repeated_errors.slice(0, 5) as e}
              <div class="le-row">
                <code class="le-err" title={e.sample || e.signature}>{e.sample || e.signature}</code>
                <span class="le-row-meta" class:green={e.after < e.before}>{e.before} → {e.after}{#if e.before > 0} ({pct(e.reduction)} fewer){/if}</span>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
    {#if fleetLearningEvidence && ((fleetLearningEvidence.agents && fleetLearningEvidence.agents.length) || (fleetLearningEvidence.trend && fleetLearningEvidence.trend.length))}
      <div class="learning-evidence fleet">
        <div class="le-head">
          <span class="le-title">Across agents</span>
          <span class="le-sub">Portfolio view of accepted learning, reuse, and recurring-error movement across Soulacy.</span>
        </div>
        {#if fleetLearningEvidence.trend && fleetLearningEvidence.trend.length}
          <div class="trend-grid" style={`--trend-max:${maxTrend(fleetLearningEvidence)}`}>
            {#each fleetLearningEvidence.trend as b}
              <div class="trend-week" title={`${trendLabel(b.start)}: ${b.runs || 0} runs, ${b.skill_uses || 0} skill uses, ${b.errors || 0} errors, ${b.accepted || 0} accepted`}>
                <div class="trend-bars">
                  <span class="bar runs" style={`height:${Math.max(3, ((b.runs || 0) / maxTrend(fleetLearningEvidence)) * 46)}px`}></span>
                  <span class="bar reuse" style={`height:${Math.max(3, ((b.skill_uses || 0) / maxTrend(fleetLearningEvidence)) * 46)}px`}></span>
                  <span class="bar errors" style={`height:${Math.max(3, ((b.errors || 0) / maxTrend(fleetLearningEvidence)) * 46)}px`}></span>
                </div>
                <span>{trendLabel(b.start)}</span>
              </div>
            {/each}
          </div>
          <div class="trend-legend">
            <span><i class="runs"></i>Runs</span>
            <span><i class="reuse"></i>Skill reuse</span>
            <span><i class="errors"></i>Errors</span>
          </div>
        {/if}
        {#if fleetLearningEvidence.agents && fleetLearningEvidence.agents.length}
          <div class="agent-evidence-grid">
            {#each fleetLearningEvidence.agents.slice(0, 8) as a}
              <div class="agent-evidence-card" class:active={a.agent_id === selectedID}>
                <div class="agent-evidence-head">
                  <strong>{agentName(a.agent_id)}</strong>
                  {#if a.last_activity}<span>{relTime(a.last_activity)}</span>{/if}
                </div>
                <div class="agent-evidence-metrics">
                  <span>{a.accepted || 0}<em>accepted</em></span>
                  <span>{a.total_skill_uses || 0}<em>uses</em></span>
                  <span class:green={(a.error_reduction || 0) > 0}>{pct(a.error_reduction)}<em>fewer errors</em></span>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
    {#if learningBusy}
      <div class="empty-state"><div class="spinner"></div></div>
    {:else if proposals.length === 0}
      <div class="empty-state">
        <div class="empty-icon">✨</div>
        <p>No {proposalStatus || ''} learning proposals. Add <code>learning.enabled: true</code> to an agent to create reviewable post-run learnings.</p>
      </div>
    {:else}
      <div class="proposal-list">
        {#each proposals as p (p.id)}
          <div class="proposal-card">
            <div class="proposal-head">
              <span class="proposal-kind {p.kind === 'skill' ? 'skill' : ''}">{p.kind}</span>
              {#if editingProposalID === p.id}
                <input class="proposal-title-input" bind:value={proposalEdit.title} placeholder="Proposal title" />
              {:else}
                <strong>{p.title}</strong>
              {/if}
              <span class="tl-rel">{relTime(p.created_at)}</span>
              <div style="flex:1"></div>
              <span class="tag">{Math.round((p.confidence || 0) * 100)}%</span>
            </div>
            {#if p.kind === 'skill'}
              <div class="skill-install-meta">
                <span>Skill</span>
                {#if editingProposalID === p.id}
                  <input class="proposal-skill-input" bind:value={proposalEdit.skill_name} placeholder="skill-name" />
                {:else}
                  <code>{p.meta?.skill_name || 'generated-skill'}</code>
                {/if}
                {#if p.meta?.installed_path}
                  <span>Installed</span>
                  <code>{p.meta.installed_path}</code>
                {/if}
              </div>
            {/if}
            {#if editingProposalID === p.id}
              <textarea class="proposal-editor" bind:value={proposalEdit.content} rows="9"></textarea>
            {:else}
              <pre class="proposal-content">{p.content}</pre>
            {/if}
            {#if p.why}
              <div class="proposal-why"><strong>Why it matters:</strong> {p.why}</div>
            {/if}
            <div class="proposal-actions">
              <span class="meta-item">Agent: <code>{p.affected_agent || p.agent_id || '—'}</code></span>
              <span class="meta-item">Session: <code>{(p.session_id || '').slice(0,12) || '—'}</code></span>
              <span class="meta-item">Source: <code>{p.source || '—'}</code></span>
              {#if p.meta?.tools_used}
                <span class="meta-item">Tools: <code>{p.meta.tools_used}</code></span>
              {/if}
              {#if p.disabled}<span class="meta-item disabled-tag">disabled</span>{/if}
              <div style="flex:1"></div>
              {#if p.status === 'pending'}
                {#if editingProposalID === p.id}
                  <button class="btn-secondary" on:click={cancelEditProposal} disabled={learningBusy}>Cancel</button>
                  <button class="btn-primary" on:click={() => saveProposalEdit(p)} disabled={learningBusy || !proposalEdit.content.trim()}>Save</button>
                {:else}
                  <label class="meta-item" title="When on, this proposal is also added to Studio's lesson store so future workflow generations see it. Default: on for skills, off for procedures.">
                    <input type="checkbox" checked={promoteChoice(p)} on:change={() => togglePromoteChoice(p)} />
                    Promote to Studio lessons
                  </label>
                  <button class="btn-secondary" on:click={() => startEditProposal(p)} disabled={learningBusy}>Edit</button>
                  <button class="btn-secondary" on:click={() => rejectProposal(p)} disabled={learningBusy}>Reject</button>
                  <button class="btn-primary" on:click={() => acceptProposal(p)} disabled={learningBusy}>
                    {p.kind === 'skill' ? 'Install skill' : 'Accept'}
                  </button>
                {/if}
              {:else if p.status === 'accepted'}
                <button class="btn-secondary" on:click={() => toggleDisableProposal(p)} disabled={learningBusy}
                        title={p.disabled ? 'Re-enable this learning' : 'Turn this learning off without deleting it'}>
                  {p.disabled ? 'Enable' : 'Disable'}
                </button>
                <span class="proposal-status">{p.status}</span>
              {:else}
                <span class="proposal-status">{p.status}</span>
              {/if}
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

<!-- Write modal -->
{#if showWrite}
  <div class="modal-bg" role="presentation" on:click|self={() => showWrite=false}
       on:keydown={(e) => e.key === 'Escape' && (showWrite = false)}>
    <div class="modal">
      <div class="modal-header"><h2>Write episodic record</h2><button class="modal-close" on:click={() => showWrite=false}>✕</button></div>
      <div class="modal-body">
        <label class="modal-label" for="ep-write-content">Content</label>
        <textarea id="ep-write-content" class="modal-textarea" bind:value={writeContent} rows="5"
          placeholder="Task: Researched X.&#10;Output: Found that Y…"></textarea>
        <label class="modal-label" style="margin-top:.6rem" for="ep-write-tags">Tags <span class="optional">(comma-separated)</span></label>
        <input id="ep-write-tags" class="modal-input" bind:value={writeTags} placeholder="research, ml" />
      </div>
      <div class="modal-footer">
        <button class="btn-secondary" on:click={() => showWrite=false}>Cancel</button>
        <button class="btn-primary" on:click={writeEpisodic} disabled={writing||!writeContent.trim()}>{writing?'Writing…':'Write record'}</button>
      </div>
    </div>
  </div>
{/if}

<!-- Clear confirm modal -->
{#if showClearConfirm}
  <div class="modal-bg" role="presentation" on:click|self={() => showClearConfirm=false}
       on:keydown={(e) => e.key === 'Escape' && (showClearConfirm = false)}>
    <div class="modal modal-sm">
      <div class="modal-header"><h2>Clear all episodic records?</h2><button class="modal-close" on:click={() => showClearConfirm=false}>✕</button></div>
      <div class="modal-body"><p>Permanently delete all <strong>{episodic.length}</strong> records for <strong>{selectedID}</strong>. This cannot be undone.</p></div>
      <div class="modal-footer">
        <button class="btn-secondary" on:click={() => showClearConfirm=false}>Cancel</button>
        <button class="btn-danger" on:click={clearAllEpisodic} disabled={clearing}>{clearing?'Clearing…':'Clear all'}</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .page{padding:1.5rem;display:flex;flex-direction:column;gap:.9rem;height:100%;overflow:hidden}
  .page-header{display:flex;align-items:center;justify-content:space-between;flex-shrink:0;flex-wrap:wrap;gap:.75rem}
  .title-row{display:flex;align-items:center;gap:.6rem}
  .page-icon{font-size:1.35rem}
  h1{font-size:1.15rem;font-weight:700;margin:0}
  .subtitle{font-size:.73rem;color:#6b7294}
  .hdr-actions{display:flex;gap:.5rem;align-items:center}
  .agent-select{min-width:180px}
  .banner{padding:.65rem 1rem;border-radius:8px;font-size:.82rem;flex-shrink:0}
  .banner.err{background:rgba(240,96,96,.1);border:1px solid rgba(240,96,96,.3);color:#f06060}
  .banner.ok{background:rgba(76,175,130,.1);border:1px solid rgba(76,175,130,.3);color:#4caf82}
  .banner.warn{background:rgba(240,160,96,.1);border:1px solid rgba(240,160,96,.3);color:#f0a060}
  code{font-family:monospace;font-size:.8em;background:rgba(255,255,255,.07);padding:.1em .3em;border-radius:3px}
  .stats-row{display:flex;gap:.65rem;flex-shrink:0;flex-wrap:wrap}
  .stat-card{background:#141626;border:1px solid #1a1e36;border-radius:10px;padding:.65rem 1.1rem;min-width:100px;text-align:center}
  .stat-val{font-size:1.4rem;font-weight:700;color:#c5c9e8}
  .stat-val.green{color:#4caf82}.stat-val.dim{color:#6b7294}.stat-val.small{font-size:.9rem}
  .stat-lbl{font-size:.67rem;color:#6b7294;margin-top:.15rem;text-transform:uppercase;letter-spacing:.04em}
  .agent-grid{display:flex;gap:.45rem;flex-wrap:wrap;flex-shrink:0}
  .agent-chip{background:#141626;border:1px solid #1a1e36;border-radius:8px;padding:.35rem .75rem;cursor:pointer;display:flex;flex-direction:column;align-items:flex-start;gap:.18rem;transition:border-color .15s,background .15s}
  .agent-chip:hover{border-color:#6c63ff44;background:#1a1e36}.agent-chip.active{border-color:#6c63ff;background:rgba(108,99,255,.1)}
  .chip-name{font-size:.8rem;font-weight:600;color:#c5c9e8}.chip-badges{display:flex;gap:.28rem}
  .cbadge{font-size:.63rem;padding:.1rem .38rem;border-radius:999px;font-weight:600}
  .cbadge.ep{background:rgba(108,99,255,.2);color:#9b95ff}.cbadge.proc{background:rgba(76,175,130,.2);color:#4caf82}
  .tabs{display:flex;gap:.2rem;flex-shrink:0;border-bottom:1px solid #1a1e36}
  .tab{padding:.5rem 1rem;border-radius:6px 6px 0 0;border:1px solid transparent;border-bottom:none;font-size:.82rem;cursor:pointer;color:#6b7294;background:transparent;transition:color .15s,background .15s;display:flex;align-items:center;gap:.35rem}
  .tab:hover{color:#c5c9e8;background:#141626}.tab.active{color:#c5c9e8;background:#141626;border-color:#1a1e36;border-bottom-color:#141626;margin-bottom:-1px}
  .tab-count{background:rgba(108,99,255,.25);color:#9b95ff;font-size:.63rem;padding:.08rem .32rem;border-radius:999px;font-weight:700}
  .tab-dot{width:6px;height:6px;border-radius:50%;background:#f0a060}
  .tab-toolbar{display:flex;gap:.55rem;align-items:center;flex-shrink:0;padding:.65rem 0 .2rem}
  .search-input{flex:1;max-width:260px}
  .proc-info{font-size:.78rem;color:#6b7294;flex:1}
  .btn-icon{padding:.38rem .65rem;background:#141626;border:1px solid #1a1e36;border-radius:6px;cursor:pointer;font-size:.82rem;color:#6b7294}
  .btn-icon:hover,.btn-icon.active{color:#c5c9e8;border-color:#6c63ff44}
  .btn-danger-outline{padding:.38rem .85rem;background:transparent;border:1px solid rgba(240,96,96,.4);border-radius:6px;color:#f06060;cursor:pointer;font-size:.8rem}
  .btn-danger-outline:hover{background:rgba(240,96,96,.1)}
  .btn-danger{padding:.45rem 1rem;background:rgba(240,96,96,.15);border:1px solid rgba(240,96,96,.5);border-radius:6px;color:#f06060;cursor:pointer;font-size:.82rem}
  .btn-danger:hover:not(:disabled){background:rgba(240,96,96,.25)}
  .empty-state{flex:1;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:.65rem;padding:2.5rem 2rem}
  .empty-icon{font-size:2.2rem;opacity:.35}.empty-state p{color:#6b7294;font-size:.83rem;text-align:center;max-width:360px}
  .empty-state-sm{padding:1.5rem;color:#6b7294;font-size:.82rem;text-align:center}
  .spinner{width:22px;height:22px;border:2px solid #1a1e36;border-top-color:#6c63ff;border-radius:50%;animation:spin .7s linear infinite}
  @keyframes spin{to{transform:rotate(360deg)}}
  .timeline{flex:1;overflow-y:auto;padding:.4rem 0;display:flex;flex-direction:column;gap:.45rem}
  .tl-item{display:flex;gap:.8rem;position:relative}
  .tl-dot{width:9px;height:9px;border-radius:50%;background:#6c63ff;flex-shrink:0;margin-top:.8rem;box-shadow:0 0 0 3px rgba(108,99,255,.15)}
  .tl-card{flex:1;background:#141626;border:1px solid #1a1e36;border-radius:9px;padding:.65rem .95rem;cursor:pointer;transition:border-color .15s,background .15s}
  .tl-card:hover{border-color:#6c63ff44;background:#1a1e36}.tl-item.expanded .tl-card{border-color:#6c63ff55}
  .tl-header{display:flex;align-items:center;gap:.45rem;margin-bottom:.3rem;flex-wrap:wrap}
  .tl-time{font-size:.7rem;color:#6b7294;font-family:monospace}.tl-rel{font-size:.68rem;color:#4a4e6a}.tl-chevron{font-size:.68rem;color:#4a4e6a;margin-left:auto}
  .tl-preview{font-size:.81rem;color:#9da3c0;line-height:1.5;white-space:pre-wrap;word-break:break-word}
  .tl-meta{display:flex;gap:.9rem;margin-top:.55rem;padding-top:.45rem;border-top:1px solid #1a1e36}
  .meta-item{font-size:.7rem;color:#6b7294}.tag{font-size:.63rem;padding:.1rem .4rem;border-radius:999px;background:rgba(108,99,255,.15);color:#9b95ff}
  .list-footer{font-size:.73rem;color:#6b7294;padding:.45rem 0;flex-shrink:0}
  .proc-pane{flex:1;display:flex;gap:.7rem;overflow:hidden}.proc-pane.split .proc-editor-wrap{flex:1}
  .proc-editor-wrap{flex:1;position:relative;display:flex;flex-direction:column}
  .proc-editor{flex:1;width:100%;background:#0e1020;border:1px solid #1a1e36;border-radius:9px;color:#c5c9e8;font-family:'Menlo','Monaco','Fira Code',monospace;font-size:.81rem;line-height:1.6;padding:.9rem;resize:none;tab-size:2}
  .proc-editor:focus{border-color:#6c63ff55;outline:none}
  .proc-preview{flex:1;background:#141626;border:1px solid #1a1e36;border-radius:9px;padding:.9rem 1.1rem;overflow-y:auto;font-size:.82rem;line-height:1.62;color:#c5c9e8}
  .dirty-badge{position:absolute;bottom:.65rem;right:.65rem;font-size:.65rem;padding:.12rem .45rem;border-radius:999px;background:rgba(240,160,96,.15);color:#f0a060;border:1px solid rgba(240,160,96,.3)}
  .proc-hint{display:flex;gap:.65rem;align-items:flex-start;padding:.9rem 1.1rem;background:rgba(108,99,255,.05);border:1px dashed #6c63ff33;border-radius:9px;flex-shrink:0}
  .hint-icon{font-size:1.1rem;flex-shrink:0}.proc-hint p{font-size:.8rem;color:#6b7294;margin:0;line-height:1.5}
  .preview-pane{flex:1;overflow-y:auto;display:flex;flex-direction:column;gap:.9rem}
  .preview-intro{font-size:.81rem;color:#6b7294;line-height:1.55;margin:0}
  .preview-form{display:flex;flex-direction:column;gap:.55rem}
  .preview-input{background:#0e1020;border:1px solid #1a1e36;border-radius:8px;color:#c5c9e8;font-size:.82rem;padding:.7rem .95rem;resize:vertical;font-family:inherit}
  .preview-input:focus{border-color:#6c63ff55;outline:none}
  .preview-meta{display:flex;gap:.65rem;flex-wrap:wrap}
  .meta-chip{background:#141626;border:1px solid #1a1e36;border-radius:8px;padding:.45rem .85rem;display:flex;flex-direction:column;align-items:center;gap:.08rem}
  .mc-num{font-size:1.15rem;font-weight:700;color:#c5c9e8}.mc-num.green{color:#4caf82}.mc-num.dim{color:#6b7294}
  .mc-lbl{font-size:.66rem;color:#6b7294;text-transform:uppercase;letter-spacing:.04em}
  .context-block-label{font-size:.76rem;color:#6b7294;font-weight:600;text-transform:uppercase;letter-spacing:.05em}
  .context-block{background:#0e1020;border:1px solid #1a1e36;border-radius:9px;padding:.9rem;font-size:.78rem;line-height:1.6;color:#9da3c0;white-space:pre-wrap;word-break:break-word;font-family:monospace;overflow-y:auto;max-height:380px;margin:0}
  .modal-bg{position:fixed;inset:0;background:rgba(0,0,0,.6);z-index:1000;display:flex;align-items:center;justify-content:center;padding:1rem}
  .modal{background:#141626;border:1px solid #1a1e36;border-radius:13px;width:100%;max-width:520px;max-height:88vh;display:flex;flex-direction:column}
  .modal .modal-body{overflow-y:auto}
  .modal.modal-sm{max-width:390px}
  .modal-header{display:flex;align-items:center;justify-content:space-between;padding:1rem 1.2rem .7rem;border-bottom:1px solid #1a1e36}
  .modal-header h2{font-size:.95rem;font-weight:600;margin:0}
  .modal-close{background:none;border:none;color:#6b7294;cursor:pointer;font-size:.95rem;padding:.2rem .4rem}
  .modal-body{padding:.9rem 1.2rem;display:flex;flex-direction:column;gap:.45rem}
  .modal-body p{font-size:.83rem;color:#9da3c0;margin:0}
  .modal-label{font-size:.75rem;font-weight:600;color:#6b7294;text-transform:uppercase;letter-spacing:.05em}
  .modal-textarea,.modal-input{width:100%;background:#0e1020;border:1px solid #1a1e36;border-radius:7px;color:#c5c9e8;font-size:.82rem;padding:.6rem .82rem;font-family:inherit}
  .modal-textarea{resize:vertical}.modal-textarea:focus,.modal-input:focus{border-color:#6c63ff55;outline:none}
  .modal-footer{display:flex;gap:.55rem;justify-content:flex-end;padding:.7rem 1.2rem 1rem;border-top:1px solid #1a1e36}
  .optional{font-weight:400;color:#4a4e6a;font-size:.85em}
  .proc-preview :global(h1){font-size:1.05rem;font-weight:700;margin:.45rem 0 .25rem;color:#c5c9e8}
  .proc-preview :global(h2){font-size:.9rem;font-weight:600;margin:.65rem 0 .25rem;color:#c5c9e8}
  .proc-preview :global(h3){font-size:.82rem;font-weight:600;margin:.55rem 0 .2rem;color:#9da3c0}
  .proc-preview :global(strong){color:#c5c9e8}
  .proc-preview :global(li){margin-left:1.1rem;list-style:disc;color:#9da3c0;line-height:1.5}
  .proc-preview :global(code){font-size:.78em}
  .proc-preview :global(p){margin:.25rem 0;color:#9da3c0}
  .locked-hint { border-color: #f0a060; }
  .btn-secondary.locked { color: #f0a060; border-color: #f0a060; }
  .rb-history { margin-top: 14px; padding: 12px 14px; background: #10121f; border: 1px solid #1a1e36; border-radius: 10px; }
  .rb-history h3 { margin: 0 0 .6rem; font-size: .9rem; }
  .rb-row { display: flex; align-items: center; gap: .6rem; padding: .35rem 0; border-bottom: 1px solid #1a1e36; }
  .rb-ver { font-family: monospace; color: #8b85ff; min-width: 2.6rem; }
  .rb-badge { font-size: .65rem; padding: .08rem .45rem; border-radius: 999px; text-transform: uppercase; }
  .rb-badge.auto { background: rgba(91,192,222,.15); color: #5bc0de; }
  .rb-badge.manual { background: rgba(139,133,255,.15); color: #8b85ff; }
  .rb-badge.roll { background: rgba(240,160,96,.15); color: #f0a060; }
  .rb-meta { font-size: .72rem; color: #6b7294; }
  .rb-btn { font-size: .72rem; padding: .2rem .55rem; }
  .rb-diff { margin-top: .8rem; }
  .rb-diff-head { display: flex; align-items: center; gap: .6rem; margin-bottom: .4rem; }
  .rb-diff-body { background: #0b0d18; border: 1px solid #1a1e36; border-radius: 8px; padding: .6rem .8rem; font-size: .75rem; max-height: 320px; overflow: auto; white-space: pre-wrap; }
  .dl.add { color: #5fce9a; }
  .dl.del { color: #f08080; }
  .dl.same { color: #9aa0c3; }
  .proposal-list{flex:1;overflow-y:auto;display:flex;flex-direction:column;gap:.7rem;padding:.2rem 0}
  .learning-health{display:grid;grid-template-columns:repeat(auto-fit,minmax(130px,1fr));gap:.65rem;flex-shrink:0}
  .lh-card{background:#141626;border:1px solid #1a1e36;border-radius:9px;padding:.65rem .8rem;min-height:70px;display:flex;flex-direction:column;justify-content:center}
  .lh-card.urgent{border-color:rgba(240,160,96,.45);background:rgba(240,160,96,.05)}
  .lh-card.wide{gap:.42rem;align-items:stretch;grid-column:span 2}
  .lh-val{font-size:1.25rem;font-weight:750;color:#c5c9e8;line-height:1}
  .lh-val.green{color:#5fce9a}
  .lh-label{font-size:.66rem;color:#6b7294;text-transform:uppercase;letter-spacing:.05em;margin-top:.35rem}
  .lh-sub{font-size:.68rem;color:#8a91b8;margin-top:.25rem}
  .lh-provenance{display:flex;align-items:center;gap:.35rem;flex-wrap:wrap;font-size:.7rem;color:#6b7294}
  .lh-provenance span{font-weight:700;text-transform:uppercase;letter-spacing:.05em}
  .learning-evidence{background:#12142250;border:1px solid #1a1e36;border-radius:10px;padding:.8rem .9rem;margin-top:.65rem;display:flex;flex-direction:column;gap:.7rem;flex-shrink:0}
  .learning-evidence.fleet{border-color:rgba(95,206,154,.26);background:linear-gradient(180deg,rgba(95,206,154,.055),rgba(18,20,34,.32))}
  .le-head{display:flex;flex-direction:column;gap:.15rem}
  .le-title{font-size:.82rem;font-weight:750;color:#c5c9e8}
  .le-sub{font-size:.7rem;color:#6b7294}
  .le-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:.6rem}
  .le-metric{background:#141626;border:1px solid #1a1e36;border-radius:9px;padding:.55rem .7rem}
  .le-val{font-size:1.2rem;font-weight:750;color:#c5c9e8;line-height:1}
  .le-val.green{color:#5fce9a}
  .le-of{font-size:.8rem;color:#6b7294;font-weight:600}
  .le-label{font-size:.64rem;color:#6b7294;text-transform:uppercase;letter-spacing:.05em;margin-top:.3rem}
  .le-sub2{font-size:.66rem;color:#8a91b8;margin-top:.2rem}
  .le-list{display:flex;flex-direction:column;gap:.3rem}
  .le-list-title{font-size:.64rem;font-weight:700;text-transform:uppercase;letter-spacing:.05em;color:#6b7294}
  .le-row{display:flex;align-items:center;justify-content:space-between;gap:.6rem;font-size:.72rem}
  .le-row code{color:#c5c9e8;background:#141626;padding:.12rem .4rem;border-radius:5px;max-width:60%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .le-row code.le-err{color:#e6b17a}
  .le-row-meta{color:#8a91b8;white-space:nowrap}
  .le-row-meta.green{color:#5fce9a}
  .trend-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(78px,1fr));gap:.45rem;align-items:end}
  .trend-week{min-height:78px;background:#141626;border:1px solid #1a1e36;border-radius:8px;padding:.45rem .45rem .35rem;display:flex;flex-direction:column;align-items:center;justify-content:flex-end;gap:.25rem}
  .trend-week > span{font-size:.62rem;color:#6b7294;white-space:nowrap}
  .trend-bars{height:50px;display:flex;align-items:flex-end;gap:3px}
  .bar{display:block;width:8px;min-height:3px;border-radius:3px 3px 0 0;opacity:.9}
  .bar.runs,.trend-legend i.runs{background:#8b85ff}
  .bar.reuse,.trend-legend i.reuse{background:#5fce9a}
  .bar.errors,.trend-legend i.errors{background:#ff8b9c}
  .trend-legend{display:flex;gap:.8rem;flex-wrap:wrap;font-size:.66rem;color:#8a91b8}
  .trend-legend span{display:flex;align-items:center;gap:.25rem}
  .trend-legend i{display:inline-block;width:8px;height:8px;border-radius:2px}
  .agent-evidence-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:.5rem}
  .agent-evidence-card{background:#141626;border:1px solid #1a1e36;border-radius:9px;padding:.55rem .65rem;display:flex;flex-direction:column;gap:.45rem}
  .agent-evidence-card.active{border-color:rgba(139,133,255,.45);background:rgba(139,133,255,.06)}
  .agent-evidence-head{display:flex;align-items:center;justify-content:space-between;gap:.55rem}
  .agent-evidence-head strong{font-size:.77rem;color:#c5c9e8;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .agent-evidence-head span{font-size:.62rem;color:#6b7294;white-space:nowrap}
  .agent-evidence-metrics{display:grid;grid-template-columns:repeat(3,1fr);gap:.35rem}
  .agent-evidence-metrics span{font-size:.85rem;font-weight:750;color:#c5c9e8;display:flex;flex-direction:column;gap:.1rem;min-width:0}
  .agent-evidence-metrics span.green{color:#5fce9a}
  .agent-evidence-metrics em{font-style:normal;font-size:.56rem;font-weight:650;color:#6b7294;text-transform:uppercase;letter-spacing:.04em;white-space:nowrap}
  .proposal-card{background:#141626;border:1px solid #1a1e36;border-radius:9px;padding:.8rem .95rem;display:flex;flex-direction:column;gap:.55rem}
  .proposal-head{display:flex;align-items:center;gap:.55rem;flex-wrap:wrap}
  .proposal-head strong{font-size:.86rem;color:#c5c9e8}
  .proposal-kind{font-size:.66rem;text-transform:uppercase;letter-spacing:.05em;padding:.1rem .42rem;border-radius:999px;background:rgba(76,175,130,.14);color:#5fce9a}
  .proposal-kind.skill{background:rgba(108,99,255,.18);color:#9b95ff}
  .skill-install-meta{display:flex;align-items:center;gap:.45rem;flex-wrap:wrap;font-size:.72rem;color:#6b7294;background:rgba(108,99,255,.06);border:1px solid rgba(108,99,255,.18);border-radius:7px;padding:.45rem .6rem}
  .proposal-title-input,.proposal-skill-input{background:#0e1020;border:1px solid #252a45;border-radius:6px;color:#c5c9e8;font-size:.8rem;padding:.42rem .55rem}
  .proposal-title-input{min-width:min(420px,100%);font-weight:650}
  .proposal-skill-input{width:190px;font-family:monospace}
  .proposal-content{margin:0;background:#0e1020;border:1px solid #1a1e36;border-radius:7px;padding:.65rem .75rem;color:#9da3c0;font-size:.76rem;line-height:1.5;white-space:pre-wrap;word-break:break-word;max-height:240px;overflow:auto}
  .proposal-editor{width:100%;min-height:190px;margin:0;background:#0e1020;border:1px solid #252a45;border-radius:7px;padding:.65rem .75rem;color:#c5c9e8;font-family:'Menlo','Monaco','Fira Code',monospace;font-size:.76rem;line-height:1.5;resize:vertical}
  .proposal-actions{display:flex;align-items:center;gap:.5rem;flex-wrap:wrap}
  .proposal-why{font-size:.74rem;color:#a7d3ff;background:rgba(80,140,255,.08);border:1px solid rgba(80,140,255,.2);border-radius:7px;padding:.45rem .6rem;margin:.35rem 0}
  .disabled-tag{font-size:.63rem;padding:.1rem .4rem;border-radius:999px;background:rgba(255,107,129,.16);color:#ff8b9c}
  .proposal-status{font-size:.72rem;color:#6b7294;text-transform:uppercase;letter-spacing:.05em}
</style>
