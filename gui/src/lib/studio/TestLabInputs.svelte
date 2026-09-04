<script>
  // TestLabInputs — the Test step's four input tabs (ST-10).
  //
  // Input / Mock Data / Variables / Environment. These were previously either
  // absent (variables, environment, start node) or scattered down a scrolling
  // bench (mocks), which made "set up the exact conditions that reproduce the
  // bug" a hunt rather than a task.
  //
  // Everything here writes through to `workflow.outcome`, so a test setup
  // travels with the workflow instead of dying with the component.

  import KeyValueEditor from '../KeyValueEditor.svelte'

  export let sampleInput = ''
  export let mockText = {}       // { [nodeId]: raw text the user typed }
  export let mockErrors = {}     // { [nodeId]: parse error }
  export let variables = {}
  export let environment = {}
  export let startNode = ''
  export let nodes = []          // draft nodes, for mock + start-node targets
  export let disabled = false

  export let onSampleInput = () => {}
  export let onSetMock = () => {}
  export let onVariables = () => {}
  export let onEnvironment = () => {}
  export let onStartNode = () => {}

  let tab = 'input'

  // Counts on the tabs, so the user can see there ARE eight mocks configured
  // without opening the tab. A hidden setting that silently changes a run's
  // result is the thing that makes a test bench untrustworthy.
  $: mockCount = Object.values(mockText || {}).filter((t) => (t || '').trim()).length
  $: varCount = Object.keys(variables || {}).length
  $: envCount = Object.keys(environment || {}).length
  $: errCount = Object.keys(mockErrors || {}).length

  const TABS = [
    { id: 'input', label: 'Input' },
    { id: 'mocks', label: 'Mock Data' },
    { id: 'vars', label: 'Variables' },
    { id: 'env', label: 'Environment' },
  ]
  // Must be a reactive VALUE, not a function call in the template: Svelte only
  // re-evaluates `{#if}` conditions whose dependencies it can see, and a plain
  // function call over a const `each` item has none — the badge froze at mount.
  $: badges = { input: '', mocks: mockCount || '', vars: varCount || '', env: envCount || '' }
</script>

<div class="tl">
  <div class="tl-tabs" role="tablist" aria-label="Test inputs">
    {#each TABS as t}
      <button
        class="tl-tab" class:active={tab === t.id}
        role="tab" aria-selected={tab === t.id}
        type="button" on:click={() => (tab = t.id)}
      >
        {t.label}
        {#if badges[t.id]}<span class="tl-badge">{badges[t.id]}</span>{/if}
        {#if t.id === 'mocks' && errCount}<span class="tl-badge bad" title="{errCount} mock(s) have invalid JSON">!</span>{/if}
      </button>
    {/each}
  </div>

  {#if tab === 'input'}
    <label class="tl-field">
      <span>Trigger input</span>
      <textarea rows="4" {disabled} value={sampleInput}
        placeholder="The payload the run starts from"
        on:change={(e) => onSampleInput(e.target.value)}></textarea>
    </label>

    <label class="tl-field">
      <span>Start from</span>
      <select {disabled} value={startNode} on:change={(e) => onStartNode(e.target.value)}>
        <!-- Default is the flow's own entry. Being able to start mid-pipeline is
             what makes iterating on step 7 of 9 bearable. -->
        <option value="">The workflow's entry point</option>
        {#each nodes as n}
          <option value={n.id}>{n.id}{n.description ? ` — ${n.description}` : ''}</option>
        {/each}
      </select>
    </label>
    {#if startNode}
      <p class="tl-note">
        Steps before “{startNode}” are skipped, so anything they would have produced
        must be supplied as a mock or a variable.
      </p>
    {/if}

  {:else if tab === 'mocks'}
    {#if !nodes.length}
      <p class="tl-note">No steps to mock yet.</p>
    {:else}
      <p class="tl-note">Canned output for a step, so a run can be reproduced without calling the real tool. JSON.</p>
      {#each nodes as n (n.id)}
        <label class="tl-field">
          <span>{n.id}{n.tool ? ` · ${n.tool}` : ''}</span>
          <textarea rows="3" class="tl-mono" {disabled}
            value={mockText[n.id] || ''}
            placeholder="{'{ }'}  — leave empty to call the real step"
            on:input={(e) => onSetMock(n.id, e.target.value)}></textarea>
          {#if mockErrors[n.id]}
            <span class="tl-err">{mockErrors[n.id]}</span>
          {/if}
        </label>
      {/each}
    {/if}

  {:else if tab === 'vars'}
    <p class="tl-note">Named values the run can read beyond the trigger payload.</p>
    <KeyValueEditor
      value={variables}
      keyLabel="Variable"
      valueLabel="Value"
      keyPlaceholder="topic"
      valuePlaceholder="AI in education"
      {disabled}
      on:change={(e) => onVariables(e.detail)}
    />

  {:else}
    <p class="tl-note">
      Environment values for this test run only. They are never written to the
      saved agent, so a sandbox setting cannot leak into production.
    </p>
    <KeyValueEditor
      value={environment}
      keyLabel="Name"
      valueLabel="Value"
      keyPlaceholder="STAGE"
      valuePlaceholder="sandbox"
      {disabled}
      on:change={(e) => onEnvironment(e.detail)}
    />
  {/if}
</div>

<style>
  .tl { display: flex; flex-direction: column; gap: 8px; }

  .tl-tabs { display: flex; gap: 2px; flex-wrap: wrap; }
  .tl-tab {
    display: inline-flex; align-items: center; gap: 5px;
    padding: 4px 10px; border-radius: 6px 6px 0 0;
    background: transparent; border: 1px solid transparent;
    border-bottom: 2px solid transparent;
    font-size: .82rem; color: var(--text-dim, #6b7294); cursor: pointer;
  }
  .tl-tab.active {
    color: var(--text, inherit);
    border-bottom-color: var(--accent, #6d5efc);
  }
  .tl-badge {
    padding: 0 6px; border-radius: 999px; font-size: .68rem;
    background: color-mix(in srgb, var(--accent, #6d5efc) 20%, transparent);
  }
  .tl-badge.bad {
    background: color-mix(in srgb, var(--danger, #e5484d) 24%, transparent);
    color: var(--danger, #e5484d);
  }

  .tl-field { display: flex; flex-direction: column; gap: 4px; font-size: .82rem; }
  .tl-field > span { color: var(--text-dim, #6b7294); }
  .tl-field textarea, .tl-field select {
    width: 100%; box-sizing: border-box; font: inherit; resize: vertical;
  }
  .tl-mono { font-family: var(--mono, monospace); font-size: .8rem; }
  .tl-note { margin: 0; font-size: .8rem; color: var(--text-dim, #6b7294); }
  .tl-err { font-size: .78rem; color: var(--danger, #e5484d); }
</style>
