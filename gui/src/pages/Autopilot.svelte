<script>
  import { onMount, onDestroy } from "svelte";
  import { api, apiFetch } from "../lib/api.js";
  import TourButton from "../lib/TourButton.svelte";
  import SafeUndo from "../lib/SafeUndo.svelte";
  import {
    rate,
    money,
    canAcceptProposal,
    candidateProofs,
    parseGoalTasks,
  } from "../lib/autopilot.js";

  let summary = {
    proofs: [],
    reliability: [],
    proposals: [],
    unfinished_runs: [],
  };
  let deployments = [],
    states = [],
    goals = [],
    agents = [];
  let section = "proofs",
    error = "",
    notice = "",
    busy = false,
    loading = true,
    refreshed = null,
    timer;
  let selectedProof = null,
    proposalProofs = {},
    activeAgent = "",
    version = "",
    goal = "",
    criterion = "",
    maxCost = "0.25",
    duration = "2m",
    allowedTools = "",
    restrictTools = false,
    testInput = "";
  let goalTitle = "",
    objective = "",
    goalCost = "1",
    goalMinutes = "10";
  let tasksJSON =
    '[\n  {"id":"research","title":"Research","agent_id":"your-agent","prompt":"Investigate the objective and cite evidence","depends_on":[],"budget":{"max_cost_usd":0.5,"max_duration_ms":240000}},\n  {"id":"review","title":"Review","agent_id":"reviewer-agent","prompt":"Check the dependency results","depends_on":["research"],"budget":{"max_cost_usd":0.5,"max_duration_ms":240000}}\n]';
  const tabs = [
    ["proofs", "Run proofs"],
    ["releases", "Releases"],
    ["learning", "Regression checks"],
    ["goals", "Goal teams"],
    ["undo", "Safe Undo"],
  ];
  const segment = encodeURIComponent;
  const post = (path, body = {}) =>
    apiFetch("/autopilot" + path, {
      method: "POST",
      body: JSON.stringify(body),
    });
  $: pending = summary.proposals.filter((p) => p.status === "pending");
  $: successes = summary.reliability.reduce(
    (n, r) => n + (r.verified_count || 0),
    0,
  );
  $: samples = summary.reliability.reduce(
    (n, r) => n + (r.sample_count || 0),
    0,
  );

  async function refresh() {
    if (busy) return;
    try {
      const results = await Promise.allSettled([
        apiFetch("/autopilot/summary"),
        apiFetch("/autopilot/deployments"),
        apiFetch("/autopilot/goals"),
        api.agents.list(),
      ]);
      const [s, d, g, a] = results;
      if (s.status === "fulfilled") {
        const array = (value) => Array.isArray(value) ? value : [];
        summary = { ...s.value, proofs: array(s.value?.proofs), reliability: array(s.value?.reliability), proposals: array(s.value?.proposals), unfinished_runs: array(s.value?.unfinished_runs) };
      }
      if (d.status === "fulfilled") {
        deployments = Array.isArray(d.value?.deployments) ? d.value.deployments : [];
        states = Array.isArray(d.value?.states) ? d.value.states : [];
      }
      if (g.status === "fulfilled") goals = Array.isArray(g.value?.goals) ? g.value.goals : [];
      if (a.status === "fulfilled")
        agents = (Array.isArray(a.value?.agents) ? a.value.agents : []).filter(
          (a) => a.id !== "system" && a.id !== "genie",
        );
      error = results
        .filter((r) => r.status === "rejected")
        .map((r) => r.reason.message)
        .join(" · ");
      if (s.status === "fulfilled") refreshed = new Date();
      if (!activeAgent && agents.length) activeAgent = agents[0].id;
    } finally {
      loading = false;
    }
  }
  async function action(label, operation, explanation = label) {
    if (busy || !window.confirm(explanation)) return;
    busy = true;
    error = "";
    notice = "";
    try {
      const result = await operation();
      notice = label + " completed.";
      return result;
    } catch (e) {
      error =
        e.message +
        (e.body?.gates?.reasons?.length
          ? ": " + e.body.gates.reasons.join("; ")
          : "");
    } finally {
      busy = false;
      await refresh();
    }
  }
  async function createVersion() {
    await action(
      "Create immutable draft",
      async () => {
        if (!criterion.trim() || !goal.trim())
          throw new Error("Enter a mission goal and an explicit output check.");
        const dollars = Number(maxCost);
        if (!Number.isFinite(dollars) || dollars < 0)
          throw new Error("The inference budget must be zero or greater.");
        const fetched = await api.agents.get(activeAgent);
        const definition = structuredClone(
          fetched.agent || fetched.definition || fetched,
        );
        definition.mission = {
          id: `mission-${crypto.randomUUID()}`,
          goal,
          acceptance: [
            {
              id: "required-output",
              type: "output_contains",
              description: "Output includes the required evidence marker",
              value: criterion,
            },
          ],
          limits: { max_cost_usd: dollars, max_duration: duration },
        };
        if (restrictTools)
          definition.mission.limits.allowed_tools = allowedTools
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean);
        await post("/deployments", {
          agent_id: activeAgent,
          version: version || undefined,
          definition,
        });
      },
      "Save this mission as a new immutable draft? It will not receive live traffic until promoted.",
    );
  }
  async function testVersion(v, simulation) {
    const result = await action(
      simulation ? "Simulation" : "Candidate run",
      () =>
        post(
          `/deployments/${segment(v.id)}/${simulation ? "simulate" : "run"}`,
          { input: testInput },
        ),
      simulation
        ? "Simulate this version? Tool execution is disabled, but inference may incur charges."
        : "Run this candidate against real tools? Normal approval and mission limits still apply.",
    );
    if (result?.proof) {
      selectedProof = result.proof;
      if (result.run_error) error = result.run_error;
    }
  }
  async function createGoal() {
    await action(
      "Create goal team",
      async () => {
        await post("/goals", {
          title: goalTitle,
          objective,
          budget: {
            max_cost_usd: Number(goalCost),
            max_duration_ms: Number(goalMinutes) * 60000,
          },
          tasks: parseGoalTasks(tasksJSON),
        });
      },
      "Save this bounded task graph as a draft? Starting it is a separate action.",
    );
  }
  onMount(() => {
    refresh();
    timer = setInterval(() => {
      if (!document.hidden) refresh();
    }, 10000);
  });
  onDestroy(() => clearInterval(timer));
</script>

<div class="autopilot">
  <header>
    <div>
      <p class="eyebrow">AUTONOMY, WITH EVIDENCE</p>
      <h1>Verified Autopilot</h1>
      <p class="lede">
        Give agents an outcome. Keep the proof, the limits, and the final say.
      </p>
    </div>
    <div class="header-actions">
      <TourButton page="autopilot" /><button
        class="btn"
        on:click={refresh}
        disabled={busy}>Refresh</button
      >
    </div>
  </header>
  {#if error}<div class="alert" role="alert">{error}</div>{/if}
  {#if notice}<div class="notice" role="status">{notice}</div>{/if}
  <div class="metrics">
    <article>
      <span>Verified runs</span><strong
        >{successes}<small> / {samples}</small></strong
      >
      <p>Real runs only · explicit checks</p>
    </article>
    <article>
      <span>Awaiting review</span><strong>{pending.length}</strong>
      <p>Failures turned into regression proposals</p>
    </article>
    <article>
      <span>Uncertain runs</span><strong
        >{summary.unfinished_runs?.length || 0}</strong
      >
      <p>Never automatically replayed</p>
    </article>
  </div>
  <nav aria-label="Autopilot sections">
    {#each tabs as [id, label]}<button
        class:active={section === id}
        on:click={() => (section = id)}>{label}</button
      >{/each}
  </nav>
  <p class="freshness">
    {refreshed
      ? `Last reconciled ${refreshed.toLocaleTimeString()}`
      : "Waiting for gateway data"} · Costs are inference estimates, not provider
    invoices. “Verified” means the recorded checks passed.
  </p>

  {#if section === "proofs"}
    {#if summary.unfinished_runs?.length}<section class="panel warning">
        <h2>Interrupted work needs review</h2>
        {#each summary.unfinished_runs as run}<p>
            <code>{run.run_id}</code> · {run.agent_id} · {run.status}
          </p>{/each}
        <p>
          Inspect external systems before starting another run. A missing
          receipt is not proof that nothing happened.
        </p>
      </section>{/if}
    <div class="split">
      <section class="panel">
        <h2>Run proofs</h2>
        {#if !summary.proofs.length}<p class="empty">
            {loading
              ? "Loading proofs…"
              : "Your first run will create a proof here. No score is invented before evidence arrives."}
          </p>{/if}{#each summary.proofs as proof}<button
            class="proof-row"
            class:selected={selectedProof?.id === proof.id}
            on:click={() => (selectedProof = proof)}
            ><div>
              <strong>{proof.agent_id}</strong><span
                >{new Date(proof.completed_at).toLocaleString()}</span
              >
            </div>
            <div>
              <span class="pill" class:good={proof.verification === "pass"}
                >{proof.simulation ? "Simulation" : proof.verification}</span
              ><span>{proof.outcome} · {money(proof.cost_usd)}</span>
            </div></button
          >{/each}
      </section>
      <section class="panel detail">
        <h2>{selectedProof ? "Evidence, not a promise" : "Inspect a run"}</h2>
        {#if selectedProof}<p><code>{selectedProof.id}</code></p>
          <p>
            Revision <code>{selectedProof.revision_hash || "Unrecorded"}</code>
          </p>
          <h3>Acceptance checks</h3>
          {#each selectedProof.checks as check}<article class="check">
              <span class="pill" class:good={check.status === "pass"}
                >{check.status}</span
              ><strong>{check.description || check.type}</strong>
              <p>Expected: {check.expected || "Not specified"}</p>
              <p>Observed: {check.actual || check.detail || "Unavailable"}</p>
            </article>{/each}{#if !selectedProof.checks.length}<p>
              No acceptance contract was configured. The outcome is unverified.
            </p>{/if}
          <h3>Observed tool calls</h3>
          {#each selectedProof.tools as tool}<p>
              <code>{tool.name}</code> · {tool.status}
            </p>{/each}
          <h3>Evidence</h3>
          {#each selectedProof.evidence as item}<p>
              {item.title}{item.summary ? `: ${item.summary}` : ""}
            </p>{/each}
          <details>
            <summary>Immutable proof payload</summary>
            <pre>{JSON.stringify(selectedProof, null, 2)}</pre>
          </details>
          <p class="muted">
            The local SHA-256 checksum detects changes. It is not an external
            audit signature or proof of an unobserved business outcome.
          </p>{:else}<p class="empty">
            Choose a proof to inspect its checks, tool receipts, cost, duration,
            and revision.
          </p>{/if}
      </section>
    </div>
    <section class="panel">
      <h2>Reliability by agent</h2>
      <div class="table-scroll">
        <table>
          <thead
            ><tr
              ><th>Agent</th><th>Samples</th><th>Success</th><th>Verified</th
              ><th>Score</th></tr
            ></thead
          ><tbody
            >{#each summary.reliability as r}<tr
                ><td>{r.agent_id}</td><td>{r.sample_count}</td><td
                  >{rate(r.success_rate)}</td
                ><td>{rate(r.verification_rate)}</td><td
                  >{r.score == null
                    ? "Unmeasured"
                    : `${r.score.toFixed(0)} / 100`}</td
                ></tr
              >{/each}</tbody
          >
        </table>
      </div>
      <p class="muted">
        Small samples are preliminary. A simulated tool result never raises the
        production score.
      </p>
    </section>
  {:else if section === "releases"}
    <section class="panel">
      <h2>Design a mission</h2>
      <p>
        Save a contract and agent snapshot. Existing live behavior stays
        unchanged.
      </p>
      <div class="form-grid">
        <label
          >Agent<select bind:value={activeAgent}
            >{#each agents as a}<option value={a.id}>{a.name || a.id}</option
              >{/each}</select
          ></label
        ><label
          >Version label<input
            bind:value={version}
            placeholder="e.g. research-v2"
          /></label
        ><label
          >Mission outcome<input
            bind:value={goal}
            placeholder="Produce a cited research brief"
          /></label
        ><label
          >Required output text<input
            bind:value={criterion}
            placeholder="e.g. Sources:"
          /></label
        ><label
          >Inference budget (USD)<input
            type="number"
            min="0"
            step="0.01"
            bind:value={maxCost}
          /></label
        ><label
          >Maximum duration<input
            bind:value={duration}
            placeholder="2m"
          /></label
        >
      </div>
      <label class="inline"
        ><input type="checkbox" bind:checked={restrictTools} /> Restrict tools to
        an exact allowlist</label
      >{#if restrictTools}<label
          >Tool names, comma-separated (empty blocks every tool)<input
            bind:value={allowedTools}
          /></label
        >{/if}<button
        class="btn primary"
        disabled={busy || !activeAgent}
        on:click={createVersion}>Save immutable draft</button
      >
      <p class="muted">
        An output marker is a deterministic formatting check, not factual
        verification. Advanced mission checks can be declared in the agent
        definition.
      </p>
    </section>
    <section class="panel">
      <h2>Release channel</h2>
      <p class="flow">
        Draft <span>→</span> Simulation <span>→</span> Canary <span>→</span> Stable
      </p>
      <label
        >Test input<textarea
          bind:value={testInput}
          rows="2"
          placeholder="Use a representative scenario"
        ></textarea></label
      >{#each deployments as v}<article class="release">
          <div>
            <h3>{v.agent_id} <span class="muted">/ {v.version}</span></h3>
            <code>{v.revision_hash}</code>
          </div>
          <div class="actions">
            <button
              class="btn"
              disabled={busy || !testInput}
              on:click={() => testVersion(v, true)}>Simulate</button
            ><button
              class="btn"
              disabled={busy || !testInput}
              on:click={() => testVersion(v, false)}>Run candidate</button
            ><button
              class="btn"
              disabled={busy}
              on:click={() =>
                action(
                  "Canary promotion",
                  () =>
                    post(`/deployments/${segment(v.id)}/canary`, {
                      traffic_percent: 10,
                    }),
                  "Route 10% of this owner’s runs to this version? Requires simulation and at least one successful, verified real run.",
                )}>Canary 10%</button
            ><button
              class="btn"
              disabled={busy}
              on:click={() =>
                action(
                  "Stable promotion",
                  () => post(`/deployments/${segment(v.id)}/promote`),
                  "Promote to stable? Requires the canary stage and five successful, verified real samples.",
                )}>Promote stable</button
            >
          </div>
        </article>{/each}{#if !deployments.length}<p class="empty">
          No releases yet.
        </p>{/if}
    </section>
    <section class="panel">
      <h2>Live controls</h2>
      {#each agents as a}{@const state = states.find(
          (s) => s.agent_id === a.id,
        )}
        <div class="release">
          <div>
            <strong>{a.name || a.id}</strong>
            <p>
              {state?.frozen ? "Frozen" : "Accepting runs"} · {state?.channels
                ?.map((c) => c.channel)
                .join(", ") || "Workspace definition"}
            </p>
          </div>
          <div class="actions">
            <button
              class="btn"
              disabled={busy}
              on:click={() =>
                action(
                  state?.frozen ? "Unfreeze" : "Freeze",
                  () =>
                    post(`/agents/${segment(a.id)}/freeze`, {
                      frozen: !state?.frozen,
                      reason: "Operator action from Autopilot",
                    }),
                  state?.frozen
                    ? "Allow new runs for this owner again?"
                    : "Block new runs and cancel this owner’s active runs? Completed external actions cannot be undone.",
                )}>{state?.frozen ? "Unfreeze" : "Freeze now"}</button
            >{#each (state?.channels || []).filter((c) => ["canary", "stable"].includes(c.channel) && c.previous_version_id) as ch}<button
                class="btn"
                disabled={busy}
                on:click={() =>
                  action("Rollback", () =>
                    post(
                      `/deployments/${segment(ch.current_version_id)}/rollback`,
                      { channel: ch.channel, reason: "Operator rollback" },
                    ),
                  )}>Roll back {ch.channel}</button
              >{/each}
          </div>
        </div>{/each}
    </section>
  {:else if section === "learning"}
    <section class="panel">
      <h2>Every failure can become a test</h2>
      <p>
        Run a corrected candidate, compare recorded assertions, then approve the
        regression check. Agent instructions are never rewritten automatically.
      </p>
      {#each summary.proposals as p}<article class="proposal">
          <div class="release">
            <h3>{p.agent_id}</h3>
            <span class="pill">{p.status}</span>
          </div>
          <p>{p.failure_summary}</p>
          <p>
            Baseline: <strong>{p.baseline?.status || "unknown"}</strong> ·
            Candidate: <strong>{p.candidate?.status || "unknown"}</strong>
          </p>
          <details>
            <summary>Proposed check</summary>
            <pre>{JSON.stringify(p.candidate_check, null, 2)}</pre>
          </details>
          {#if p.status === "pending"}<label
              >Candidate proof<select bind:value={proposalProofs[p.id]}
                ><option value="">Choose a successful real run</option
                >{#each candidateProofs(summary.proofs, p) as proof}<option
                    value={proof.id}>{proof.agent_id} · {proof.id}</option
                  >{/each}</select
              ></label
            >
            <div class="actions">
              <button
                class="btn"
                disabled={busy || !proposalProofs[p.id]}
                on:click={() =>
                  action("Verify candidate", () =>
                    post(`/proposals/${segment(p.id)}/verify`, {
                      candidate_proof_id: proposalProofs[p.id],
                    }),
                  )}>Compare proofs</button
              ><button
                class="btn primary"
                disabled={busy || !canAcceptProposal(p)}
                on:click={() =>
                  action("Accept regression check", () =>
                    post(`/proposals/${segment(p.id)}/accept`, {
                      reason: "Reviewed recorded baseline and candidate",
                    }),
                  )}>Accept check</button
              ><button
                class="btn"
                disabled={busy}
                on:click={() =>
                  action("Reject proposal", () =>
                    post(`/proposals/${segment(p.id)}/reject`, {
                      reason: "Operator rejected",
                    }),
                  )}>Reject</button
              >
            </div>{/if}
        </article>{/each}{#if !summary.proposals.length}<p class="empty">
          No failed mission assertions have produced proposals yet.
        </p>{/if}
    </section>
  {:else if section === "goals"}
    <section class="panel">
      <h2>One goal. A bounded team.</h2>
      <p>
        Dependency-ready tasks execute in order. Each requires a verified
        real-run proof. Missing measurements or interrupted work stop progress.
      </p>
      <div class="form-grid">
        <label>Title<input bind:value={goalTitle} /></label><label
          >Objective<input bind:value={objective} /></label
        ><label
          >Total inference budget (USD)<input
            type="number"
            min="0.01"
            step="0.01"
            bind:value={goalCost}
          /></label
        ><label
          >Maximum minutes<input
            type="number"
            min="1"
            bind:value={goalMinutes}
          /></label
        >
      </div>
      <label
        >Task graph (JSON)<textarea
          class="code"
          rows="9"
          bind:value={tasksJSON}
        ></textarea></label
      ><button
        class="btn primary"
        disabled={busy || !goalTitle || !objective}
        on:click={createGoal}>Save goal draft</button
      >
    </section>
    {#each goals as g}<section class="panel">
        <div class="release">
          <div>
            <h2>{g.title}</h2>
            <p>{g.objective}</p>
          </div>
          <span class="pill">{g.status}</span>
        </div>
        <p>
          {money(g.cost_usd)} of {money(g.budget.max_cost_usd)} · {(
            g.duration_ms / 1000
          ).toFixed(1)} seconds
        </p>
        {#each g.tasks as task}<article class="task">
            <strong>{task.title}</strong><span
              >{task.agent_id} · {task.status}</span
            >
            <p>Depends on: {task.depends_on?.join(", ") || "Nothing"}</p>
            {#if task.error}<p class="danger">
                {task.error}
              </p>{/if}{#if task.result}<details>
                <summary>Result</summary>
                <pre>{task.result}</pre>
              </details>{/if}
          </article>{/each}
        <div class="actions">
          {#if g.status === "draft"}<button
              class="btn primary"
              disabled={busy}
              on:click={() =>
                action(
                  "Start goal team",
                  () => post(`/goals/${segment(g.id)}/run`),
                  "Start these agents against real tools within the stated budgets?",
                )}>Start team</button
            >{/if}{#if ["draft", "running"].includes(g.status)}<button
              class="btn"
              disabled={busy}
              on:click={() =>
                action(
                  "Cancel goal",
                  () => post(`/goals/${segment(g.id)}/cancel`),
                  "Cancel remaining tasks? Existing external effects are not undone.",
                )}>Cancel</button
            >{/if}
        </div>
      </section>{/each}
  {:else if section === "undo"}
    <section class="panel">
      <label>Agent<select bind:value={activeAgent}>{#each agents as a}<option value={a.id}>{a.name || a.id}</option>{/each}</select></label>
      {#if activeAgent}{#key activeAgent}<SafeUndo agentID={activeAgent}/>{/key}{:else}<p>Select an agent to review its supported changes.</p>{/if}
    </section>
  {/if}
</div>

<style>
  .autopilot {
    max-width: 1320px;
    margin: 0 auto;
    padding: 32px 30px 64px;
    color: var(--text);
  }
  header,
  .release,
  .actions,
  .header-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }
  header {
    align-items: flex-start;
    margin-bottom: 26px;
  }
  .eyebrow {
    font-size: 10px;
    font-weight: 700;
    letter-spacing: 0.2em;
    color: #6cbba9;
    margin: 0 0 12px;
  }
  h1 {
    font-size: 32px;
    letter-spacing: -1px;
    margin: 0 0 9px;
  }
  h2 {
    font-size: 17px;
    letter-spacing: -0.3px;
    margin: 0 0 12px;
  }
  h3 {
    font-size: 14px;
    margin: 14px 0 8px;
  }
  .lede,
  p {
    line-height: 1.55;
  }
  .lede,
  .muted,
  .freshness,
  .empty {
    color: var(--text-muted, #8e97a6);
  }
  .lede {
    margin: 0;
    font-size: 14px;
  }
  .metrics {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 14px;
    margin-bottom: 22px;
  }
  .metrics article,
  .panel {
    border: 1px solid var(--border);
    border-radius: 16px;
    background: var(--bg-elev-1, var(--bg-elev-2));
    padding: 23px;
  }
  .metrics span {
    font-size: 12px;
  }
  .metrics strong {
    display: block;
    font-size: 34px;
    font-weight: 600;
    letter-spacing: -1.3px;
    margin: 14px 0 3px;
  }
  .metrics small {
    font-size: 19px;
    color: var(--text-muted, #8e97a6);
  }
  .metrics p {
    font-size: 11px;
    color: var(--text-muted, #8e97a6);
    margin: 0;
  }
  nav {
    display: flex;
    gap: 5px;
    border-bottom: 1px solid var(--border);
    overflow: auto;
  }
  nav button {
    padding: 13px 17px;
    border: 0;
    background: none;
    color: var(--text-muted, #8e97a6);
    white-space: nowrap;
    cursor: pointer;
  }
  nav button.active {
    color: var(--text);
    border-bottom: 2px solid #6cbba9;
  }
  .freshness {
    font-size: 11px;
    margin: 13px 0 24px;
  }
  .panel {
    margin-bottom: 17px;
  }
  .split {
    display: grid;
    grid-template-columns: 1fr 1.05fr;
    gap: 17px;
  }
  .proof-row {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    text-align: left;
    width: 100%;
    background: none;
    border: 1px solid transparent;
    border-bottom-color: var(--border);
    padding: 17px 12px;
    color: var(--text);
    cursor: pointer;
  }
  .proof-row:hover,
  .proof-row.selected {
    background: #6cbba910;
    border-color: #6cbba94a;
    border-radius: 10px;
  }
  .proof-row span {
    display: block;
    font-size: 11px;
    margin-top: 6px;
  }
  .proof-row strong {
    font-size: 13px;
  }
  .proof-row > div:last-child {
    text-align: right;
  }
  .pill {
    display: inline-block !important;
    font-size: 10px !important;
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 4px 8px;
    color: #dfb56f;
  }
  .pill.good {
    color: #6cbba9;
    border-color: #6cbba94a;
  }
  .detail code {
    font-size: 10px;
    overflow-wrap: anywhere;
  }
  pre {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 500px;
    overflow: auto;
    background: #00000020;
    border: 1px solid var(--border);
    padding: 16px;
    border-radius: 9px;
    font-size: 11px;
  }
  .check,
  .proposal,
  .task {
    border-top: 1px solid var(--border);
    padding: 16px 0;
  }
  .check strong {
    margin-left: 8px;
    font-size: 12px;
  }
  .check p,
  .task p,
  .proposal p {
    font-size: 12px;
    margin: 7px 0;
  }
  .task span {
    display: block;
    font-size: 11px;
    color: var(--text-muted, #8e97a6);
    margin-top: 6px;
  }
  .form-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0 18px;
  }
  label {
    display: block;
    font-size: 12px;
    margin: 12px 0 16px;
  }
  input:not([type="checkbox"]),
  select,
  textarea {
    display: block;
    width: 100%;
    box-sizing: border-box;
    margin-top: 8px;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 11px 12px;
    background: var(--bg-elev-2);
    color: var(--text);
    font: inherit;
  }
  textarea.code,
  code {
    font-family: ui-monospace, monospace;
  }
  .inline {
    display: flex;
    gap: 9px;
    align-items: center;
  }
  .actions {
    justify-content: flex-start;
    flex-wrap: wrap;
  }
  .release {
    padding: 16px 0;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .release code {
    font-size: 10px;
    word-break: break-all;
  }
  .release p {
    font-size: 12px;
    margin: 7px 0;
  }
  .flow {
    font-size: 13px;
    color: #6cbba9;
  }
  .flow span {
    margin: 0 15px;
    color: var(--text-muted, #8e97a6);
  }
  .alert,
  .notice {
    padding: 15px 18px;
    border-radius: 10px;
    font-size: 13px;
    line-height: 1.5;
    margin-bottom: 18px;
  }
  .alert,
  .warning {
    background: #deaa5710;
    border: 1px solid #deaa5740;
  }
  .notice {
    background: #6cbba918;
    color: #6cbba9;
  }
  .danger {
    color: #eb927c;
  }
  .empty {
    padding: 30px 5px;
    font-size: 13px;
  }
  .muted {
    font-size: 11px;
  }
  .table-scroll {
    overflow: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
    text-align: left;
  }
  th {
    font-weight: 500;
    color: var(--text-muted, #8e97a6);
  }
  th,
  td {
    padding: 13px 8px;
    border-bottom: 1px solid var(--border);
  }
  details {
    margin: 15px 0;
  }
  summary {
    font-size: 12px;
    cursor: pointer;
  }
  button:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  @media (max-width: 800px) {
    .autopilot {
      padding: 20px 16px;
    }
    .split,
    .form-grid {
      grid-template-columns: 1fr;
    }
    .metrics {
      gap: 8px;
    }
    .metrics article {
      padding: 15px;
    }
    .metrics strong {
      font-size: 27px;
    }
    .metrics p {
      display: none;
    }
    h1 {
      font-size: 26px;
    }
    .header-actions {
      flex-direction: column;
    }
    .release {
      align-items: flex-start;
    }
  }
</style>
