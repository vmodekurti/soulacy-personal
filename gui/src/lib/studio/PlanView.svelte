<script>
  // PlanView — the "Simple" plan-first view for the Guided Studio Builder.
  // Renders the workflow as six lanes (Trigger, Gather, Think, Act, Verify,
  // Deliver) with plain-English cards showing readiness and risk, a "Needs
  // attention" group, and a plain-English plan review. Reads the SAME `workflow`
  // model the canvas uses, so switching views never loses edits.
  import { toLanes, planReview } from './planlanes.js'
  import { suggestPythonSteps } from './pyinfer.js'
  import ConfigCard from './ConfigCard.svelte'

  export let workflow = null
  export let onSelectNode = null   // (node) => void — open a block in the Inspector
  export let onSave = null         // () => void — proceed to save
  export let onAddPython = null    // (suggestion) => void — insert a suggested Python step
  export let onAddStep = null      // (instruction) => void — add one step from natural language
  export let addStepBusy = false   // true while a natural-language step is compiling
  export let addStepMsg = ''       // result/error message from the last add-step
  export let onUpdateNode = null   // (updatedNode) => void — patch a block's config
  export let testByNode = {}       // { [nodeId]: {ok,error,durationMs} } — test results
  export let saving = false

  let stepText = ''
  function submitStep() {
    if (!stepText.trim() || !onAddStep) return
    onAddStep(stepText)
    stepText = ''
  }

  let expanded = {}                // per-card technical-detail disclosure
  let editing = {}                 // per-card inline config-card editing
  const toggle = (id) => (expanded = { ...expanded, [id]: !expanded[id] })
  const toggleEdit = (id) => (editing = { ...editing, [id]: !editing[id] })

  $: lanes = workflow ? toLanes(workflow) : { trigger: [], gather: [], think: [], act: [], verify: [], deliver: [] }
  $: review = workflow ? planReview(workflow) : null
  $: attention = review ? review.attention : []
  $: pySuggestions = workflow ? suggestPythonSteps(workflow) : []

  const RISK_LABEL = { low: '', medium: 'medium risk', high: 'high risk' }
  const STATUS_ICON = { ready: '✓', 'needs-attention': '!', risky: '⚠' }

  function pick(card) {
    if (card?.node && onSelectNode) onSelectNode(card.node)
  }
</script>

<div class="plan-view">
  {#if !workflow}
    <div class="plan-empty">Describe an automation above, then press Generate — the plan will appear here.</div>
  {:else}
    <!-- Six lanes: Trigger → Gather → Think → Act → Verify → Deliver -->
    {#each [['trigger','⚡','Trigger','How it starts'], ['gather','📥','Gather','Inputs and context it collects'], ['think','🧠','Think','How it reasons or analyzes'], ['act','🛠️','Act','Actions it takes'], ['verify','✅','Verify','Checks before delivering'], ['deliver','📤','Deliver','Where the result goes']] as [key, icon, title, sub]}
      <section class="lane lane-{key}">
        <header class="lane-head">
          <span class="lane-icon" aria-hidden="true">{icon}</span>
          <span class="lane-title">{title}</span>
          <span class="lane-sub">{sub}</span>
        </header>
        <div class="lane-cards">
          {#if lanes[key].length === 0}
            <div class="card card-empty">Nothing here yet</div>
          {:else}
            {#each lanes[key] as card (card.id)}
              <div class="card status-{card.readiness.status}"
                   class:clickable={!!card.node}>
                <div class="card-top">
                  <span class="card-status" title={card.readiness.status}>{STATUS_ICON[card.readiness.status]}</span>
                  <span class="card-label">{card.label}</span>
                  {#if card.node && testByNode[card.id]}
                    {@const tr = testByNode[card.id]}
                    <span class="card-test {tr.ok ? 'pass' : 'fail'}"
                          title={tr.ok ? `Passed${tr.durationMs != null ? ` · ${tr.durationMs}ms` : ''}` : tr.error}>
                      {tr.ok ? '✓ test' : '✕ test'}
                    </span>
                  {/if}
                  {#if RISK_LABEL[card.risk]}<span class="card-risk risk-{card.risk}">{RISK_LABEL[card.risk]}</span>{/if}
                </div>
                {#if card.node && testByNode[card.id] && !testByNode[card.id].ok}
                  <div class="card-testerr">✕ {testByNode[card.id].error}</div>
                {/if}
                {#if card.readiness.reasons.length}
                  <ul class="card-reasons">
                    {#each card.readiness.reasons as r}<li>{r}</li>{/each}
                  </ul>
                {/if}
                {#if card.node}
                  <div class="card-actions">
                    <button class="card-more" on:click={() => pick(card)}>
                      Open
                    </button>
                    {#if onUpdateNode}
                      <button class="card-more" on:click|stopPropagation={() => toggleEdit(card.id)}>
                        {editing[card.id] ? 'Done ▲' : 'Edit ✎'}
                      </button>
                    {/if}
                    <button class="card-more" on:click|stopPropagation={() => toggle(card.id)}>
                      {expanded[card.id] ? 'Hide details ▲' : 'Details ▼'}
                    </button>
                  </div>
                  {#if editing[card.id] && onUpdateNode}
                    <div role="group">
                      <ConfigCard node={card.node} onUpdate={onUpdateNode} />
                    </div>
                  {/if}
                  {#if expanded[card.id]}
                    <dl class="card-detail">
                      <div><dt>Type</dt><dd>{card.node.kind}{card.node.tool ? ` · ${card.node.tool}` : ''}{card.node.agent ? ` · ${card.node.agent}` : ''}</dd></div>
                      {#if card.node.output}<div><dt>Produces</dt><dd>{card.node.output}</dd></div>{/if}
                      {#if card.node.input}<div><dt>Input</dt><dd class="mono">{card.node.input}</dd></div>{/if}
                    </dl>
                  {/if}
                {/if}
              </div>
            {/each}
          {/if}
        </div>
      </section>
    {/each}

    <!-- Add a step in natural language (Epic 3): describe it; the backend picks
         the right block (tool/python/agent) and appends it to the flow. -->
    {#if onAddStep}
      <section class="add-step">
        <header class="add-step-head">✨ Add a step</header>
        <div class="add-step-row">
          <input class="add-step-input" placeholder="Describe a step to add, e.g. 'summarize the results with an LLM'"
                 bind:value={stepText} on:keydown={(e) => e.key === 'Enter' && submitStep()} disabled={addStepBusy} />
          <button class="btn btn-sm" on:click={submitStep} disabled={addStepBusy || !stepText.trim()}>
            {addStepBusy ? 'Adding…' : 'Add step'}
          </button>
        </div>
        {#if addStepMsg}<div class="add-step-msg">{addStepMsg}</div>{/if}
      </section>
    {/if}

    <!-- Python suggestions (Stories 3 & 4): deterministic hint that a step
         looks like computation that belongs in Python, with the reason. -->
    {#if pySuggestions.length}
      <section class="suggest">
        <header class="suggest-head">🐍 Suggested Python steps</header>
        <ul class="suggest-list">
          {#each pySuggestions as s (s.nodeId)}
            <li class="suggest-item">
              <div class="suggest-body">
                <strong>{s.label}</strong>
                <span class="suggest-why">{s.reason}</span>
              </div>
              {#if onAddPython}
                <button class="btn btn-sm" on:click={() => onAddPython(s)}>Add Python step</button>
              {/if}
            </li>
          {/each}
        </ul>
      </section>
    {/if}

    <!-- Needs attention (slice item 7) -->
    {#if attention.length}
      <section class="attention">
        <header class="attention-head">⚠ Needs attention ({attention.length})</header>
        <ul class="attention-list">
          {#each attention as a}
            <li><strong>{a.label}</strong> — {a.reasons.join('; ')}</li>
          {/each}
        </ul>
      </section>
    {/if}

    <!-- Plain-English plan review before save (slice items 6 & 11) -->
    {#if review}
      <section class="review">
        <header class="review-head">Review before save</header>
        <div class="review-grid">
          <div class="rv"><span class="rv-k">When</span><span class="rv-v">{review.when}</span></div>
          <div class="rv"><span class="rv-k">Does</span><span class="rv-v">{review.does.length ? review.does.join(' → ') : 'No steps yet'}</span></div>
          {#if review.usesTools.length}<div class="rv"><span class="rv-k">Tools</span><span class="rv-v">{review.usesTools.join(', ')}</span></div>{/if}
          {#if review.usesCode.length}<div class="rv"><span class="rv-k">Code</span><span class="rv-v">{review.usesCode.join(', ')} <span class="flag">generated code</span></span></div>{/if}
          <div class="rv"><span class="rv-k">Delivers</span><span class="rv-v">{review.deliversTo.join(', ')}</span></div>
          {#if review.permissions.length}<div class="rv"><span class="rv-k">Permissions</span><span class="rv-v">{review.permissions.join(', ')}</span></div>{/if}
          {#if review.risks.length}<div class="rv rv-risk"><span class="rv-k">⚠ Risky</span><span class="rv-v">{review.risks.join('; ')}</span></div>{/if}
        </div>
        {#if onSave}
          <div class="review-actions">
            {#if !review.ready}
              <span class="review-warn">Resolve the items under “Needs attention” before enabling.</span>
            {/if}
            <!-- Honour the verdict this panel just rendered. The button used to
                 be disabled={saving} alone, so it sat enabled directly beneath
                 its own "resolve these first" warning — the message and the gate
                 contradicting each other on the same row. -->
            <button class="btn primary" on:click={() => onSave && onSave()}
                    disabled={saving || !review.ready}
                    title={!review.ready ? 'Resolve the items under “Needs attention” first' : ''}>
              {saving ? 'Saving…' : 'Save automation'}
            </button>
          </div>
        {/if}
      </section>
    {/if}
  {/if}
</div>

<style>
  .plan-view { padding: 16px; overflow-y: auto; height: 100%; display: flex; flex-direction: column; gap: 14px; }
  /* Never compress a section to fit — each lane/panel keeps its full height and
     the whole view scrolls instead, so no cards get clipped. */
  .plan-view > * { flex: 0 0 auto; }
  .plan-empty { color: var(--text-muted); font-size: 13px; padding: 40px 16px; text-align: center; }
  .lane { border: 1px solid var(--border); border-radius: 12px; background: var(--bg-elev); overflow: hidden; }
  .lane-head { display: flex; align-items: baseline; gap: 8px; padding: 10px 14px; background: var(--bg-elev-2); border-bottom: 1px solid var(--border); }
  .lane-icon { font-size: 15px; }
  .lane-title { font-weight: 600; font-size: 13px; color: var(--text); }
  .lane-sub { font-size: 11px; color: var(--text-muted); }
  .lane-cards { display: flex; flex-wrap: wrap; gap: 10px; padding: 12px 14px; }
  .lane-gather .lane-cards,
  .lane-think .lane-cards,
  .lane-act .lane-cards,
  .lane-verify .lane-cards { flex-direction: column; }
  .card {
    flex: 0 1 auto; min-width: 200px; max-width: 360px;
    border: 1px solid var(--border); border-left: 3px solid var(--border);
    border-radius: 10px; background: var(--bg-elev-2); padding: 10px 12px;
  }
  .card.clickable { cursor: pointer; transition: border-color .12s; }
  .card.clickable:hover { border-color: var(--accent); }
  .card.status-ready { border-left-color: #36d399; }
  .card.status-needs-attention { border-left-color: #f5a524; }
  .card.status-risky { border-left-color: #ff6b81; }
  .card-empty { color: var(--text-muted); font-style: italic; font-size: 12px; border-left-color: var(--border); }
  .card-top { display: flex; align-items: center; gap: 8px; }
  .card-status { font-size: 12px; width: 16px; text-align: center; }
  .status-ready .card-status { color: #36d399; }
  .status-needs-attention .card-status { color: #f5a524; }
  .status-risky .card-status { color: #ff6b81; }
  .card-label { font-size: 13px; font-weight: 600; color: var(--text); }
  .card-test { font-size: 10px; padding: 1px 7px; border-radius: 999px; }
  .card-test.pass { background: rgba(54,211,153,.16); color: #36d399; }
  .card-test.fail { background: rgba(255,107,129,.16); color: #ff6b81; }
  .card-testerr { margin-top: 4px; font-size: 11px; color: #ff6b81; }
  .card-risk { margin-left: auto; font-size: 10px; padding: 1px 7px; border-radius: 999px; }
  .risk-medium { background: rgba(245,165,36,.16); color: #f5a524; }
  .risk-high { background: rgba(255,107,129,.16); color: #ff6b81; }
  .card-reasons { margin: 6px 0 0; padding-left: 18px; font-size: 11px; color: var(--text-muted); }
  .card-actions { display: flex; gap: 12px; margin-top: 6px; }
  .card-more { background: none; border: none; color: var(--accent); font-size: 11px; cursor: pointer; padding: 0; }
  .card-detail { margin: 6px 0 0; font-size: 11px; display: grid; gap: 3px; }
  .card-detail div { display: flex; gap: 6px; }
  .card-detail dt { color: var(--text-muted); min-width: 60px; margin: 0; }
  .card-detail dd { margin: 0; color: var(--text); word-break: break-word; }
  .card-detail .mono { font-family: ui-monospace, Menlo, monospace; }
  .add-step { border: 1px solid var(--border); border-radius: 12px; background: var(--bg-elev); padding: 12px 14px; }
  .add-step-head { font-weight: 600; font-size: 13px; margin-bottom: 8px; }
  .add-step-row { display: flex; gap: 8px; }
  .add-step-input { flex: 1; min-width: 0; padding: 8px 10px; border-radius: 8px; border: 1px solid var(--border); background: var(--bg-elev-2); color: var(--text); font-size: 13px; }
  .add-step-msg { margin-top: 8px; font-size: 12px; color: var(--text-muted); }
  .suggest { border: 1px solid var(--border); border-radius: 12px; background: rgba(108,140,255,.06); }
  .suggest-head { padding: 10px 14px; font-weight: 600; font-size: 13px; color: var(--accent, #6c8cff); }
  .suggest-list { margin: 0; padding: 0 14px 12px; list-style: none; display: flex; flex-direction: column; gap: 8px; }
  .suggest-item { display: flex; align-items: center; gap: 12px; }
  .suggest-body { display: flex; flex-direction: column; gap: 2px; }
  .suggest-why { font-size: 11px; color: var(--text-muted); }
  .suggest-item .btn { margin-left: auto; white-space: nowrap; }
  .attention { border: 1px solid #f5a524; border-radius: 12px; background: rgba(245,165,36,.06); }
  .attention-head { padding: 10px 14px; font-weight: 600; font-size: 13px; color: #f5a524; }
  .attention-list { margin: 0; padding: 0 14px 12px 32px; font-size: 12px; color: var(--text); }
  .attention-list li { margin-bottom: 3px; }
  .review { border: 1px solid var(--border); border-radius: 12px; background: var(--bg-elev); }
  .review-head { padding: 10px 14px; font-weight: 600; font-size: 13px; border-bottom: 1px solid var(--border); }
  .review-grid { padding: 12px 14px; display: grid; gap: 8px; }
  .rv { display: flex; gap: 10px; font-size: 12px; }
  .rv-k { flex: 0 0 80px; color: var(--text-muted); text-transform: uppercase; font-size: 10px; letter-spacing: .5px; padding-top: 2px; }
  .rv-v { color: var(--text); }
  .rv-risk .rv-k, .rv-risk .rv-v { color: #ff6b81; }
  .flag { font-size: 10px; padding: 0 6px; border-radius: 999px; background: rgba(255,107,129,.16); color: #ff6b81; }
  .review-actions { display: flex; align-items: center; justify-content: flex-end; gap: 12px; padding: 0 14px 14px; }
  .review-warn { font-size: 11px; color: #f5a524; margin-right: auto; }
</style>
