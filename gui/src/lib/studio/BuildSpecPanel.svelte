<script>
  // BuildSpecPanel — "Studio understood" (ST-01).
  //
  // The Describe step's right pane: the structured specification Studio derived
  // from the intent, shown BEFORE anything is generated so the user can verify
  // it rather than discover the misunderstanding on the canvas.
  //
  // Two behaviours are deliberate:
  //   • Empty sections are rendered as "not specified" instead of being hidden.
  //     A missing delivery destination is precisely what someone needs to catch
  //     here, and dropping the row hides it until the run delivers nothing.
  //   • Blockers gate Generate. A blocker means we already know the build would
  //     be incomplete, so offering to build anyway wastes the user's time and
  //     teaches them the check is decorative.

  import {
    specRows, specBlockers, specQuestions, changeSummary,
    deliveryPrompt, isDeliveryQuestion, knownDestinations, unresolvedBlockers,
  } from './buildspecview.js'
  import GenerationTrigger from './GenerationTrigger.svelte'

  export let spec = null            // /studio/build-spec payload
  export let recommendation = null  // { mode, rationale }
  export let loading = false
  export let error = ''
  export let answers = {}           // { [questionId]: string }
  export let channels = []          // GET /channels payload, for destination options
  export let generationTrigger = { type: 'auto', cron: '', channel: '' }
  export let generationTriggerError = ''

  export let onAnswer = () => {}    // (id, value) => void
  export let onRefine = () => {}
  export let onGenerate = () => {}
  export let onGenerationTrigger = () => {}
  // Both actions are builder-model calls that can take many seconds. The panel
  // used to know only `loading` (the spec READ), so pressing either button left
  // it enabled and unchanged for the whole call — the click looked ignored.
  export let refining = false       // a refine-prompt pass is in flight
  export let generating = false     // a generate/compile pass is in flight
  // The Studio home puts the primary Refine and Generate actions directly on
  // their prompt cards. Modal/legacy callers can keep the panel actions, while
  // the home screen avoids presenting the same primary action twice.
  export let showActions = true

  $: rows = specRows(spec, recommendation)
  $: blockers = specBlockers(spec)
  $: questions = specQuestions(spec)
  $: change = changeSummary(spec)
  // The channel-specific destination ask, derived from whatever the spec said
  // delivery was.
  $: delivery = deliveryPrompt(spec && spec.delivery)
  // Destinations this workspace is actually configured for. Empty means nothing
  // is set up, and the UI falls back to asking for a raw id rather than showing
  // an empty dropdown.
  $: destinations = knownDestinations(channels, delivery.channel)

  // A blocker is only satisfied once the user has actually supplied the value —
  // `ready` reflects the spec as the SERVER saw it, before these answers were
  // typed, so the live gate is the unresolved list rather than that flag.
  // "none" is a real answer ("don't deliver anywhere"), so it must read as
  // resolved — the generic blocker check only looks for a non-empty string,
  // which it satisfies, but name it here so the intent is not accidental.
  const NO_DELIVERY = 'none'
  function channelLabel(id) {
    if (id === NO_DELIVERY) return 'Nowhere — just return the result'
    const list = Array.isArray(channels) ? channels : (channels && channels.channels) || []
    const ch = list.find((c) => c && String(c.id).toLowerCase() === String(id).toLowerCase())
    return (ch && (ch.name || ch.id)) || id
  }

  $: unresolved = unresolvedBlockers(spec, answers)
  $: busy = loading || refining || generating
  $: canGenerate = !busy && !!spec && unresolved.length === 0 && !generationTriggerError
</script>

<div class="bs">
  <div class="bs-head">
    <h3>Studio understood</h3>
    <p class="bs-sub">Here's the plan Soulacy will build.</p>
  </div>

  {#if error}
    <div class="bs-error" role="alert">{error}</div>
  {/if}

  <!-- This is an operator decision, not another inferred row. Keep it above
       the interpretation it controls so changing it visibly updates every
       dependent field below. -->
  <GenerationTrigger
    selection={generationTrigger}
    {channels}
    onChange={onGenerationTrigger}
  />

  {#if loading}
    <p class="bs-muted">Reading your description…</p>
  {:else if !spec}
    <p class="bs-muted">Describe what you want on the left — Studio will show what it understood here before building anything.</p>
  {:else}
    <!-- What refining actually changed. Shown above the spec because it tells
         the user whether re-reading the whole thing is worth their time. -->
    {#if change}
      <div class="bs-change" class:material={change.material}>
        <strong>{change.summary}</strong>
        {#if change.changes.length}
          <ul>
            {#each change.changes as c}
              <li>
                <span class="bs-cfield">{c.field}</span>
                {#if c.kind === 'added'}<span class="bs-add">added “{c.after}”</span>
                {:else if c.kind === 'removed'}<span class="bs-rm">removed “{c.before}”</span>
                {:else}<span class="bs-rm">{c.before}</span> → <span class="bs-add">{c.after}</span>{/if}
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    {/if}

    <ul class="bs-rows">
      {#each rows as r (r.key)}
        <li class="bs-row" class:empty={r.empty}>
          <span class="bs-icon" aria-hidden="true">{r.icon}</span>
          <div class="bs-body">
            <span class="bs-label">{r.label}</span>
            {#if r.empty}
              <span class="bs-none">not specified</span>
            {:else if r.lines && r.lines.length}
              <ol class="bs-stages">
                {#each r.lines as l}<li>{l}</li>{/each}
              </ol>
            {:else}
              <span class="bs-value">{r.value}</span>
            {/if}
            {#if r.detail}<span class="bs-detail">{r.detail}</span>{/if}
          </div>
        </li>
      {/each}
    </ul>

    <!-- Blockers: each one is an input, because "Telegram destination required"
         with nowhere to type it is a dead end.
         For the delivery destination we ask in the CHANNEL'S OWN terms — a
         Telegram chat id, a Slack #channel, an E.164 number — because "which
         chat, channel, or address?" expects an identifier in a format only the
         adapter knows, and someone who has not seen it will type "my group
         chat" and be told it is invalid. -->
    {#each blockers as b (b.id)}
      {@const dq = isDeliveryQuestion(b) ? delivery : null}
      <div class="bs-blocker">
        <span class="bs-blocker-q">⚠ {dq && dq.question ? dq.question : (b.question || b.message || b.id)}</span>
        {#if dq}
          <span class="bs-why">{dq.help}</span>
        {:else if b.why}
          <span class="bs-why">{b.why}</span>
        {/if}

        {#if (b.options || []).length}
          <!-- A closed set of valid answers: the workspace's own channels. A
               free-text box here is answered with a typo, and a channel that
               isn't installed is indistinguishable from one that is. -->
          <select value={answers[b.id] || ''} on:change={(e) => onAnswer(b.id, e.target.value)}>
            <option value="">Choose…</option>
            {#each b.options as opt}
              <option value={opt}>{channelLabel(opt)}</option>
            {/each}
          </select>
        {:else if dq && destinations.length}
          <!-- Soulacy already knows these destinations from the channel's own
               configuration, so offer them rather than making the user go and
               look up an ID it could have filled in. "Enter another…" keeps the
               manual path for a destination that is not configured yet. -->
          <select
            value={destinations.some((d) => d.value === answers[b.id]) ? answers[b.id] : ''}
            on:change={(e) => onAnswer(b.id, e.target.value)}
          >
            <option value="">Choose a configured destination…</option>
            {#each destinations as d}
              <option value={d.value}>{d.label}</option>
            {/each}
          </select>
          <details class="bs-manual">
            <summary>Enter another destination</summary>
            <input
              type="text"
              value={answers[b.id] || ''}
              placeholder={dq.placeholder}
              on:change={(e) => onAnswer(b.id, e.target.value)}
            />
          </details>
        {:else}
          <input
            type="text"
            value={answers[b.id] || ''}
            placeholder={dq && dq.placeholder ? dq.placeholder : 'Required to continue'}
            on:change={(e) => onAnswer(b.id, e.target.value)}
          />
        {/if}
      </div>
    {/each}

    {#if questions.length}
      <details class="bs-questions">
        <summary>{questions.length} optional clarification{questions.length === 1 ? '' : 's'}</summary>
        {#each questions as q (q.id)}
          <div class="bs-question">
            <span>{q.question}</span>
            {#if (q.options || []).length}
              <select value={answers[q.id] || ''} on:change={(e) => onAnswer(q.id, e.target.value)}>
                <option value="">Choose…</option>
                {#each q.options as opt}
                  <option value={opt}>{channelLabel(opt)}</option>
                {/each}
              </select>
            {:else}
              <input
                type="text"
                value={answers[q.id] || ''}
                placeholder="Optional"
                on:change={(e) => onAnswer(q.id, e.target.value)}
              />
            {/if}
          </div>
        {/each}
      </details>
    {/if}

    {#if showActions}
      <div class="bs-actions">
        <button class="btn" type="button" disabled={busy} on:click={onRefine}>
          {#if refining}<span class="bs-spin" aria-hidden="true"></span>Refining…{:else}Refine prompt{/if}
        </button>
        <button class="btn primary" type="button" disabled={!canGenerate} on:click={onGenerate}
          data-tooltip={unresolved.length ? 'Answer the required questions first' : generationTriggerError}>
          {#if generating}<span class="bs-spin" aria-hidden="true"></span>Generating…{:else}Generate workflow{/if}
        </button>
      </div>
    {/if}
    {#if refining || generating}
      <p class="bs-working" role="status" aria-live="polite">
        {refining
          ? 'Reading your prompt and writing a build-ready spec — this can take a few seconds.'
          : 'Asking the builder model to write the workflow — this can take a minute.'}
      </p>
    {/if}
    {#if unresolved.length}
      <p class="bs-gate">
        {unresolved.length} required detail{unresolved.length === 1 ? '' : 's'} still missing — generating now would
        build something known to be incomplete.
      </p>
    {:else if generationTriggerError}
      <p class="bs-gate">{generationTriggerError}</p>
    {/if}
  {/if}
</div>

<style>
  .bs-working {
    margin: 8px 0 0; font-size: 12px; color: var(--text-muted, #8b93ab);
  }
  .bs-spin {
    display: inline-block; width: 11px; height: 11px; margin-right: 7px;
    vertical-align: -1px;
    border: 2px solid currentColor; border-right-color: transparent;
    border-radius: 50%; animation: bs-spin 0.7s linear infinite;
  }
  @keyframes bs-spin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { .bs-spin { animation: none; } }
  .bs { display: flex; flex-direction: column; gap: 10px; }
  .bs-head h3 { margin: 0; font-size: .95rem; }
  .bs-sub { margin: 2px 0 0; font-size: .8rem; color: var(--text-dim, #6b7294); }
  .bs-muted { font-size: .84rem; color: var(--text-dim, #6b7294); }
  .bs-error {
    padding: 8px 10px; border-radius: 6px; font-size: .82rem;
    color: var(--danger, #e5484d);
    background: color-mix(in srgb, var(--danger, #e5484d) 10%, transparent);
    border: 1px solid color-mix(in srgb, var(--danger, #e5484d) 32%, transparent);
  }

  .bs-change {
    padding: 8px 10px; border-radius: 6px; font-size: .8rem;
    border: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
  }
  .bs-change.material {
    background: color-mix(in srgb, var(--accent, #6d5efc) 10%, transparent);
    border-color: color-mix(in srgb, var(--accent, #6d5efc) 32%, transparent);
  }
  .bs-change ul { margin: 6px 0 0; padding-left: 18px; }
  .bs-cfield { font-family: var(--mono, monospace); margin-right: 6px; }
  .bs-add { color: var(--ok, #2ea043); }
  .bs-rm { color: var(--danger, #e5484d); text-decoration: line-through; }

  .bs-rows { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .bs-row { display: flex; gap: 10px; align-items: flex-start; }
  .bs-icon {
    flex: none; width: 24px; height: 24px; border-radius: 6px;
    display: grid; place-items: center; font-size: .8rem;
    background: color-mix(in srgb, var(--accent, #6d5efc) 14%, transparent);
  }
  .bs-body { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
  .bs-label { font-size: .72rem; text-transform: uppercase; letter-spacing: .04em; color: var(--text-dim, #6b7294); }
  .bs-value { font-size: .85rem; overflow-wrap: anywhere; }
  .bs-detail { font-size: .78rem; color: var(--text-dim, #6b7294); }
  /* "not specified" is deliberately visible rather than hidden. */
  .bs-none { font-size: .82rem; font-style: italic; color: var(--text-dim, #6b7294); }
  .bs-row.empty .bs-icon { background: color-mix(in srgb, var(--text-dim, #6b7294) 12%, transparent); }
  .bs-stages { margin: 2px 0 0; padding-left: 16px; font-size: .84rem; }
  .bs-stages li { margin: 1px 0; }

  .bs-blocker {
    display: flex; flex-direction: column; gap: 4px;
    padding: 8px 10px; border-radius: 6px; font-size: .82rem;
    background: color-mix(in srgb, var(--warn, #f0ad4e) 12%, transparent);
    border: 1px solid color-mix(in srgb, var(--warn, #f0ad4e) 35%, transparent);
  }
  .bs-blocker input, .bs-blocker select { width: 100%; box-sizing: border-box; }
  .bs-why { font-size: .78rem; color: var(--text-dim, #6b7294); }
  .bs-manual { font-size: .78rem; }
  .bs-manual summary { cursor: pointer; color: var(--text-dim, #6b7294); margin-bottom: 4px; }

  .bs-questions { font-size: .82rem; }
  .bs-questions summary { cursor: pointer; color: var(--text-dim, #6b7294); }
  .bs-question { display: flex; flex-direction: column; gap: 3px; margin: 6px 0; }
  .bs-question input { width: 100%; box-sizing: border-box; }

  .bs-actions { display: flex; gap: 8px; justify-content: flex-end; }
  .bs-gate { margin: 0; font-size: .78rem; color: var(--text-dim, #6b7294); text-align: right; }
</style>
