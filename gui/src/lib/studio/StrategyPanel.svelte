<script>
  // StrategyPanel — the Build step's strategy contract (ST-02).
  //
  // The mode picker used to be four buttons that set a string. Everything that
  // actually governed the run — when to stop, what counts as done, how many bad
  // steps to tolerate, whether a side effect needs approval — was a hidden
  // default or prose inside the system prompt. So two agents on the same
  // strategy could behave completely differently with nothing on screen
  // explaining why, and "informed override" was impossible: the user could pick
  // ReAct but not see that their model could not sustain it.
  //
  // This panel shows the contract for the SELECTED mode only. Rendering ReAct's
  // budgets under Plan-Execute would imply they were in force when they are not.

  export let mode = 'auto'              // workflow | auto | react | plan_execute
  export let advice = null              // { warning, confidence, capabilities, mode, reason }
  export let recommendation = null      // { mode, rationale }
  export let policy = null              // effective AgentPolicy from the draft
  export let tools = []                 // the agent's allowlist
  export let model = ''                 // e.g. "anthropic / claude-sonnet-4.5"
  export let busy = false
  export let warnings = []              // ValidatePolicy output
  export let unreliable = []            // strategies below the local fit threshold

  export let onSwitchMode = () => {}
  export let onUpdate = () => {}        // (patch) => void, merged into draft.policy

  const MODES = [
    { id: 'workflow',     label: 'Workflow', experimental: true },
    { id: 'auto',         label: 'Auto' },
    { id: 'react',        label: 'ReAct (advanced)' },
    { id: 'plan_execute', label: 'Plan-Execute' },
  ]

  $: contract = (policy && policy.contract) || {}
  $: react = (policy && policy.react) || {}
  $: plan = (policy && policy.plan) || {}
  $: caps = (advice && advice.capabilities) || null
  $: isRecommended = recommendation && recommendation.mode === mode
  // ReAct is never auto-selected, so reaching it is always a deliberate act.
  // The banner says so rather than congratulating the user on a recommendation
  // they did not receive.
  $: isAdvanced = mode === 'react' && !isRecommended

  // The capability badge next to the model. Derived from the advisor's profile
  // so it states a fact about THIS model rather than a generic reassurance.
  $: capBadge = !caps ? '' :
      mode === 'plan_execute' ? (caps.plan_reliability >= 0.7 ? 'Planning capable' : 'Planning unproven') :
      mode === 'react'        ? (caps.native_tools ? 'Reasoning supported' : 'Weak tool calling') :
                                (caps.native_tools ? 'Native tool calling' : 'No native tool calling')
  $: capBadgeOk = !caps ? false :
      mode === 'plan_execute' ? caps.plan_reliability >= 0.7 : !!caps.native_tools

  function patchContract(field, value) { onUpdate({ contract: { ...contract, [field]: value } }) }
  function patchReact(field, value)    { onUpdate({ react: { ...react, [field]: value } }) }
  function patchPlan(field, value)     { onUpdate({ plan: { ...plan, [field]: value } }) }

  function num(e) { const n = Number(e.target.value); return Number.isFinite(n) ? n : 0 }
  function historicallyUnreliable(id) { return (unreliable || []).includes(id) }

  // ── Plan steps ────────────────────────────────────────────────────────────
  $: steps = Array.isArray(plan.steps) ? plan.steps : []
  let selectedStep = 0
  let showCapabilities = false
  let capabilityQuery = ''
  $: filteredTools = (tools || []).filter((tool) =>
    !capabilityQuery.trim() || String(tool).toLowerCase().includes(capabilityQuery.trim().toLowerCase()))
  // Clamp in the DERIVATION rather than assigning selectedStep from a reactive
  // statement. A reactive block that both reads and writes the same variable is
  // a self-referential dependency, which Svelte can reject at compile time.
  $: stepIdx = steps.length ? Math.min(Math.max(selectedStep, 0), steps.length - 1) : 0
  $: current = steps[stepIdx] || null

  function addStep() {
    patchPlan('steps', [...steps, { title: '', status: 'pending' }])
    selectedStep = steps.length
  }
  function patchStep(i, field, value) {
    patchPlan('steps', steps.map((s, idx) => (idx === i ? { ...s, [field]: value } : s)))
  }
  function removeStep(i) {
    patchPlan('steps', steps.filter((_, idx) => idx !== i))
  }
  // Steps with no declared dependency on each other are what the parallel
  // toggle is allowed to overlap; showing that grouping makes the toggle's
  // effect visible instead of implied.
  $: parallelisable = steps
    .map((s, i) => ({ s, i }))
    .filter(({ s }) => !(s.depends_on && s.depends_on.length))
</script>

<div class="sp">
  <!-- Mode tabs -->
  <div class="sp-modes" role="tablist" aria-label="Execution strategy">
    {#each MODES as m}
      <button
        class="sp-mode" class:active={mode === m.id}
        role="tab" aria-selected={mode === m.id}
        type="button" disabled={busy || historicallyUnreliable(m.id)}
        title={historicallyUnreliable(m.id) ? 'Disabled: this model is below the local strategy reliability threshold.' : ''}
        on:click={() => onSwitchMode(m.id)}
      >
        <span>{m.label}</span>
        {#if m.experimental}<span class="sp-exp">Experimental</span>{/if}
        {#if historicallyUnreliable(m.id)}<span class="sp-risk" aria-label="Historically unreliable">⚠</span>{/if}
      </button>
    {/each}
  </div>

  <!-- Verdict banner + model capability -->
  <div class="sp-head">
    <div class="sp-banner" class:ok={isRecommended} class:warn={isAdvanced}>
      {#if isRecommended}
        <strong>Recommended: {MODES.find((m) => m.id === mode)?.label}</strong>
        <span>{recommendation.rationale}</span>
      {:else if isAdvanced}
        <strong>Advanced</strong>
        <span>Use only when iterative observation changes the next action.</span>
      {:else if recommendation && recommendation.mode}
        <strong>Overriding the recommendation</strong>
        <span>Studio suggested {MODES.find((m) => m.id === recommendation.mode)?.label}. {recommendation.rationale}</span>
      {/if}
    </div>
    {#if model}
      <div class="sp-model">
        <span class="sp-model-id">{model}</span>
        {#if capBadge}
          <span class="sp-cap" class:ok={capBadgeOk} class:bad={!capBadgeOk}>{capBadge}</span>
        {/if}
      </div>
    {/if}
  </div>

  <!-- The advisor's warning about THIS model in THIS mode. Placed above the
       editable contract because it changes whether you should be here at all. -->
  {#if advice && advice.warning}
    <div class="sp-warn" role="status">{advice.warning}</div>
  {/if}

  {#if mode === 'workflow'}
    <p class="sp-note">
      <strong>Experimental:</strong> Studio generates a fixed graph and runs the same
      bounded sequence every time. Generated graphs can choose the wrong tools, lose
      inputs between steps, or require manual wiring. Review and test every step before
      deployment.
    </p>
  {:else}
    <!-- Shared agent contract -->
    <div class="sp-cols">
      <section class="sp-card">
        <h4>Agent contract</h4>

        <label class="sp-field">
          <span>Goal</span>
          <textarea rows="2" disabled={busy} value={contract.goal || ''}
            placeholder="What a successful run achieves"
            on:change={(e) => patchContract('goal', e.target.value)}></textarea>
        </label>

        <label class="sp-field">
          <span>Instructions</span>
          <textarea rows="4" disabled={busy} value={contract.instructions || ''}
            placeholder="How the agent should behave"
            on:change={(e) => patchContract('instructions', e.target.value)}></textarea>
        </label>

        <div class="sp-field">
          <span>Available capabilities</span>
          <div class="sp-cap-summary">
            <span>{tools.length ? `${tools.length} tool${tools.length === 1 ? '' : 's'} available` : 'None yet'}</span>
            {#if tools.length}
              <button type="button" class="sp-disclose" aria-expanded={showCapabilities}
                on:click={() => (showCapabilities = !showCapabilities)}>
                {showCapabilities ? 'Hide tools' : 'View tools'}
              </button>
            {/if}
          </div>
          {#if showCapabilities && tools.length}
            <div class="sp-cap-browser">
              {#if tools.length > 8}
                <input class="sp-cap-search" type="search" bind:value={capabilityQuery}
                  placeholder="Search available tools…" aria-label="Search available tools" />
              {/if}
              <div class="sp-chips">
                {#each filteredTools as t}<span class="sp-chip">{t}</span>{/each}
                {#if !filteredTools.length}<span class="sp-empty">No matching tools</span>{/if}
              </div>
            </div>
          {/if}
        </div>

        <label class="sp-field">
          <span>Completion criteria</span>
          <textarea rows="2" disabled={busy} value={contract.completion_criteria || ''}
            placeholder="How the agent knows it is done"
            on:change={(e) => patchContract('completion_criteria', e.target.value)}></textarea>
        </label>
      </section>

      <section class="sp-card">
        <h4>{mode === 'react' ? 'ReAct policy' : mode === 'plan_execute' ? 'Plan-Execute policy' : 'Runtime policy'}</h4>

        {#if mode === 'auto'}
          <label class="sp-field inline"><span>Tool choice</span>
            <select disabled={busy} value={contract.tool_choice || 'auto'}
              on:change={(e) => patchContract('tool_choice', e.target.value)}>
              <option value="auto">Auto</option>
              <option value="required">Required</option>
              <option value="none">None</option>
            </select>
          </label>
          <label class="sp-field inline"><span>Recovery retries</span>
            <input type="number" min="0" disabled={busy} value={contract.recovery_retries ?? 2}
              on:change={(e) => patchContract('recovery_retries', num(e))} />
          </label>
        {/if}

        {#if mode === 'react'}
          <label class="sp-field inline"><span>Invalid-step budget</span>
            <input type="number" min="0" disabled={busy} value={react.invalid_step_budget ?? 2}
              on:change={(e) => patchReact('invalid_step_budget', num(e))} />
          </label>
          <label class="sp-field inline"><span>Repeated-tool limit</span>
            <input type="number" min="0" disabled={busy} value={react.repeated_tool_limit ?? 2}
              on:change={(e) => patchReact('repeated_tool_limit', num(e))} />
          </label>
          <label class="sp-field inline"><span>Confidence threshold</span>
            <input type="number" min="0" max="1" step="0.01" disabled={busy}
              value={react.confidence_threshold ?? 0.72}
              on:change={(e) => patchReact('confidence_threshold', num(e))} />
          </label>
          <label class="sp-toggle">
            <input type="checkbox" disabled={busy} checked={react.preserve_best_result ?? true}
              on:change={(e) => patchReact('preserve_best_result', e.target.checked)} />
            <span>Preserve best result</span>
          </label>
          <label class="sp-toggle">
            <input type="checkbox" disabled={busy} checked={react.fallback_to_auto ?? true}
              on:change={(e) => patchReact('fallback_to_auto', e.target.checked)} />
            <span>Fall back to Auto after repeated invalid steps</span>
          </label>
        {/if}

        {#if mode === 'plan_execute'}
          <label class="sp-field inline"><span>Plan timeout</span>
            <input type="text" disabled={busy} value={plan.plan_timeout || '2m'}
              on:change={(e) => patchPlan('plan_timeout', e.target.value)} />
          </label>
          <label class="sp-toggle">
            <input type="checkbox" disabled={busy} checked={plan.replan_after_failure ?? true}
              on:change={(e) => patchPlan('replan_after_failure', e.target.checked)} />
            <span>Replan after failure</span>
          </label>
          <label class="sp-toggle">
            <input type="checkbox" disabled={busy} checked={plan.parallel_independent_steps ?? true}
              on:change={(e) => patchPlan('parallel_independent_steps', e.target.checked)} />
            <span>Run independent steps in parallel</span>
          </label>
          <label class="sp-toggle">
            <input type="checkbox" disabled={busy} checked={plan.approval_before_side_effects ?? true}
              on:change={(e) => patchPlan('approval_before_side_effects', e.target.checked)} />
            <span>Require approval before side effects</span>
          </label>
        {/if}
      </section>
    </div>

    {#if mode === 'react'}
      <!-- The loop, drawn. A ReAct agent's behaviour is a cycle; a list of
           numbers does not convey that it can revisit the same tool. -->
      <section class="sp-card">
        <h4>Reasoning loop</h4>
        <div class="sp-loop">
          <span class="sp-loop-node goal">User goal</span>
          <span class="sp-loop-arrow">↓</span>
          <span class="sp-loop-node">Think</span>
          <span class="sp-loop-arrow">↓</span>
          <span class="sp-loop-node">Tool</span>
          <span class="sp-loop-arrow">↓</span>
          <span class="sp-loop-node">Observe</span>
          <span class="sp-loop-arrow">↓</span>
          <span class="sp-loop-node">Decide</span>
          <span class="sp-loop-back">↺ repeats until a stop condition is met</span>
        </div>
        <label class="sp-field">
          <span>Stop conditions</span>
          <textarea rows="2" disabled={busy} value={react.stop_conditions || ''}
            placeholder="Stop when the objective is satisfied, confidence is met, or max steps is reached"
            on:change={(e) => patchReact('stop_conditions', e.target.value)}></textarea>
        </label>
        <label class="sp-field">
          <span>Recovery behavior</span>
          <textarea rows="2" disabled={busy} value={react.recovery_behavior || ''}
            placeholder="On invalid or repeated action, revise the plan or fall back to Auto"
            on:change={(e) => patchReact('recovery_behavior', e.target.value)}></textarea>
        </label>
      </section>
    {/if}

    {#if mode === 'plan_execute'}
      <div class="sp-cols">
        <section class="sp-card">
          <h4>Planner contract</h4>
          {#if !steps.length}
            <p class="sp-empty">No steps yet. The planner will produce them at run time; add them here to review the plan first.</p>
          {/if}
          <ol class="sp-steps">
            {#each steps as s, i}
              <!-- The title input is a SIBLING of the select control, not nested
                   inside a button. An <input> inside a <button> is invalid HTML
                   and browsers make it effectively untypeable — the button
                   swallows the click before the field can take focus. Selecting
                   the step happens on focus instead, which is what a user editing
                   a title means anyway. -->
              <li class="sp-step" class:sel={i === stepIdx}>
                <input class="sp-step-title" type="text" disabled={busy}
                  value={s.title || ''} placeholder={`Step ${i + 1}`}
                  aria-label={`Step ${i + 1} title`}
                  on:focus={() => (selectedStep = i)}
                  on:change={(e) => patchStep(i, 'title', e.target.value)} />
                <span class="sp-step-status">{s.status || 'pending'}</span>
                <button class="sp-step-del" type="button" disabled={busy}
                  on:click={() => removeStep(i)} aria-label={`Remove step ${i + 1}`}>×</button>
              </li>
            {/each}
          </ol>
          <button class="sp-add" type="button" disabled={busy} on:click={addStep}>+ Add step</button>
        </section>

        <section class="sp-card">
          <h4>Executor contract {#if current}<span class="sp-sub">(step {stepIdx + 1})</span>{/if}</h4>
          {#if !current}
            <p class="sp-empty">Select a step to narrow what it may do.</p>
          {:else}
            <div class="sp-field">
              <span>Allowed tools</span>
              <div class="sp-chips">
                {#each (current.allowed_tools || []) as t}<span class="sp-chip">{t}</span>{/each}
                {#if !(current.allowed_tools || []).length}
                  <span class="sp-empty">All of the agent's tools</span>
                {/if}
              </div>
            </div>
            <label class="sp-field">
              <span>Expected output</span>
              <textarea rows="2" disabled={busy} value={current.expected_output || ''}
                on:change={(e) => patchStep(stepIdx,'expected_output', e.target.value)}></textarea>
            </label>
            <label class="sp-field">
              <span>Verification</span>
              <textarea rows="2" disabled={busy} value={current.verification || ''}
                placeholder="How this step's output is checked"
                on:change={(e) => patchStep(stepIdx,'verification', e.target.value)}></textarea>
            </label>
          {/if}
        </section>
      </div>

      {#if plan.parallel_independent_steps && parallelisable.length > 1}
        <div class="sp-parallel">
          <span class="sp-parallel-label">Parallelisable</span>
          {#each parallelisable as p}
            <span class="sp-chip">{p.i + 1}. {p.s.title || `Step ${p.i + 1}`}</span>
          {/each}
        </div>
      {/if}
    {/if}

    {#if mode === 'auto'}
      <p class="sp-note">
        Studio keeps generated workflows off by default. Use Plan-Execute for
        multi-step work; choose Experimental Workflow only when you need a fixed
        graph and will review and test it.
      </p>
    {/if}

    {#if warnings && warnings.length}
      <ul class="sp-warnlist">
        {#each warnings as w}<li>{w}</li>{/each}
      </ul>
    {/if}
  {/if}
</div>

<style>
  .sp { display: flex; flex-direction: column; gap: 10px; }

  .sp-modes { display: flex; gap: 4px; flex-wrap: wrap; }
  .sp-mode {
    padding: 6px 14px; border-radius: 6px; font-size: .85rem; cursor: pointer;
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
    background: transparent; color: var(--text, inherit);
    display: inline-flex; align-items: center; gap: 6px;
  }
  .sp-mode.active {
    background: var(--accent, #6d5efc); color: #fff;
    border-color: var(--accent, #6d5efc);
  }
  .sp-mode:disabled { opacity: .55; cursor: default; }
  .sp-exp {
    padding: 1px 5px; border-radius: 999px; font-size: .58rem;
    line-height: 1.3; text-transform: uppercase; letter-spacing: .04em;
    color: var(--warn, #f0ad4e);
    border: 1px solid color-mix(in srgb, var(--warn, #f0ad4e) 55%, transparent);
    background: color-mix(in srgb, var(--warn, #f0ad4e) 12%, transparent);
  }
  .sp-mode.active .sp-exp { color: #fff; border-color: rgba(255, 255, 255, .65); }
  .sp-risk { color: var(--warn, #f0ad4e); font-weight: 700; }
  .sp-mode:disabled .sp-risk { opacity: 1; }

  .sp-head { display: flex; gap: 10px; align-items: stretch; flex-wrap: wrap; }
  .sp-banner {
    flex: 1 1 320px; display: flex; flex-direction: column; gap: 2px;
    padding: 10px 12px; border-radius: 8px; font-size: .85rem;
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
  }
  .sp-banner.ok {
    background: color-mix(in srgb, var(--ok, #2ea043) 12%, transparent);
    border-color: color-mix(in srgb, var(--ok, #2ea043) 35%, transparent);
  }
  .sp-banner.warn {
    background: color-mix(in srgb, var(--warn, #f0ad4e) 12%, transparent);
    border-color: color-mix(in srgb, var(--warn, #f0ad4e) 38%, transparent);
  }
  .sp-model {
    display: flex; align-items: center; gap: 8px; padding: 10px 12px;
    border-radius: 8px; font-size: .82rem;
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
  }
  .sp-model-id { font-family: var(--mono, monospace); }
  .sp-cap { padding: 2px 8px; border-radius: 999px; font-size: .72rem; }
  .sp-cap.ok  { background: color-mix(in srgb, var(--ok, #2ea043) 22%, transparent); }
  .sp-cap.bad { background: color-mix(in srgb, var(--warn, #f0ad4e) 26%, transparent); }

  .sp-warn {
    padding: 8px 10px; border-radius: 6px; font-size: .82rem;
    background: color-mix(in srgb, var(--warn, #f0ad4e) 12%, transparent);
    border: 1px solid color-mix(in srgb, var(--warn, #f0ad4e) 35%, transparent);
  }

  .sp-cols { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  @media (max-width: 900px) { .sp-cols { grid-template-columns: 1fr; } }

  .sp-card {
    padding: 12px; border-radius: 8px;
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
    display: flex; flex-direction: column; gap: 10px;
  }
  .sp-card h4 { margin: 0; font-size: .85rem; }
  .sp-sub { font-weight: 400; color: var(--text-dim, #6b7294); }

  .sp-field { display: flex; flex-direction: column; gap: 4px; font-size: .82rem; }
  .sp-field > span { color: var(--text-dim, #6b7294); }
  .sp-field.inline { flex-direction: row; align-items: center; justify-content: space-between; gap: 10px; }
  .sp-field.inline input, .sp-field.inline select { width: 120px; }
  .sp-field textarea { width: 100%; box-sizing: border-box; font: inherit; resize: vertical; }

  .sp-toggle { display: flex; align-items: center; gap: 8px; font-size: .82rem; }

  .sp-chips { display: flex; flex-wrap: wrap; gap: 4px; }
  .sp-cap-summary {
    display: flex; align-items: center; justify-content: space-between; gap: 8px;
    min-height: 32px; padding: 5px 8px; border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
    border-radius: 6px; color: var(--text-dim, #6b7294);
  }
  .sp-disclose {
    border: 0; background: transparent; color: var(--accent, #6d5efc);
    font: inherit; font-weight: 700; cursor: pointer; white-space: nowrap;
  }
  .sp-cap-browser {
    display: flex; flex-direction: column; gap: 6px; max-height: 180px;
    overflow: auto; padding: 7px; border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
    border-radius: 6px; background: color-mix(in srgb, var(--border) 10%, transparent);
  }
  .sp-cap-search {
    position: sticky; top: 0; z-index: 1; width: 100%; box-sizing: border-box;
    padding: 6px 8px; border-radius: 5px; border: 1px solid var(--border);
    background: var(--bg, #0e1020); color: var(--text, inherit); font: inherit;
  }
  .sp-chip {
    padding: 2px 8px; border-radius: 999px; font-size: .75rem;
    font-family: var(--mono, monospace);
    background: color-mix(in srgb, var(--accent, #6d5efc) 14%, transparent);
  }
  .sp-empty { font-size: .8rem; color: var(--text-dim, #6b7294); }
  .sp-note { margin: 0; font-size: .82rem; color: var(--text-dim, #6b7294); }

  /* The loop, drawn vertically so it reads on a narrow panel. */
  .sp-loop { display: flex; flex-direction: column; align-items: center; gap: 2px; }
  .sp-loop-node {
    padding: 5px 16px; border-radius: 6px; font-size: .8rem;
    background: color-mix(in srgb, var(--accent, #6d5efc) 16%, transparent);
  }
  .sp-loop-node.goal { background: color-mix(in srgb, var(--text-dim, #6b7294) 18%, transparent); }
  .sp-loop-arrow { font-size: .8rem; color: var(--text-dim, #6b7294); }
  .sp-loop-back { margin-top: 4px; font-size: .75rem; color: var(--text-dim, #6b7294); }

  .sp-steps { margin: 0; padding-left: 18px; display: flex; flex-direction: column; gap: 4px; }
  .sp-step {
    display: flex; align-items: center; gap: 6px;
    padding: 3px 6px; border: 1px solid transparent; border-radius: 6px;
  }
  .sp-step.sel { border-color: var(--accent, #6d5efc); }
  .sp-step-title { flex: 1; min-width: 0; font: inherit; font-size: .82rem; }
  .sp-step-status { font-size: .72rem; color: var(--text-dim, #6b7294); }
  .sp-step-del { background: transparent; border: 0; cursor: pointer; color: var(--text-dim, #6b7294); font-size: 1rem; }
  .sp-add {
    align-self: flex-start; background: transparent; cursor: pointer;
    border: 1px dashed color-mix(in srgb, var(--border) 80%, transparent);
    border-radius: 6px; padding: 4px 10px; font-size: .8rem; color: inherit;
  }

  .sp-parallel {
    display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
    padding: 8px 10px; border-radius: 8px;
    border: 1px dashed color-mix(in srgb, var(--accent, #6d5efc) 45%, transparent);
  }
  .sp-parallel-label { font-size: .75rem; color: var(--text-dim, #6b7294); }

  .sp-warnlist {
    margin: 0; padding: 8px 10px 8px 26px; border-radius: 6px; font-size: .8rem;
    background: color-mix(in srgb, var(--warn, #f0ad4e) 10%, transparent);
    border: 1px solid color-mix(in srgb, var(--warn, #f0ad4e) 30%, transparent);
  }
</style>
