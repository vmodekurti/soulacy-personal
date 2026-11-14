export function rate(value) {
  return typeof value === "number" && Number.isFinite(value)
    ? `${Math.round(value * 100)}%`
    : "Unmeasured";
}
export function money(value) {
  return typeof value === "number" && Number.isFinite(value)
    ? `$${value.toFixed(4)}`
    : "Unmeasured";
}
export function canAcceptProposal(proposal) {
  return (
    proposal?.status === "pending" &&
    proposal?.baseline?.status === "fail" &&
    proposal?.candidate?.status === "pass"
  );
}
export function candidateProofs(proofs, proposal) {
  return proofs.filter(
    (p) =>
      p.agent_id === proposal.agent_id &&
      p.outcome === "succeeded" &&
      !p.simulation &&
      p.id !== proposal.source_proof_id,
  );
}
export function parseGoalTasks(raw) {
  return validateGoalTasks(JSON.parse(raw));
}

export function orderedGoalTasks(steps, budget) {
  return validateGoalTasks(steps.map((step, index) => ({
    id: step.id,
    title: step.title.trim(),
    prompt: step.prompt.trim(),
    agent_id: step.agent_id,
    depends_on: index ? [steps[index - 1].id] : [],
    budget,
  })));
}

function validateGoalTasks(tasks) {
  if (!Array.isArray(tasks) || !tasks.length)
    throw new Error("Add at least one task.");
  const ids = new Set();
  for (const task of tasks) {
    if (!task?.id || ids.has(task.id))
      throw new Error("Every task needs a unique ID.");
    ids.add(task.id);
    if (!task.agent_id || !task.prompt || !task.title)
      throw new Error("Every task needs an agent, title, and prompt.");
    const { max_cost_usd: cost, max_duration_ms: duration } = task.budget || {};
    if (!Number.isFinite(cost) || cost < 0 || !Number.isInteger(duration) || duration <= 0)
      throw new Error("Every task needs a non-negative cost cap and a positive duration.");
  }
  for (const task of tasks) {
    if (!Array.isArray(task.depends_on ?? []))
      throw new Error("depends_on must be an array of task IDs.");
    if ((task.depends_on || []).some((id) => !ids.has(id) || id === task.id))
      throw new Error("A dependency must name another task.");
  }
  const visited = new Set(),
    visiting = new Set(),
    byID = new Map(tasks.map((t) => [t.id, t]));
  function visit(id) {
    if (visiting.has(id)) throw new Error("Task dependencies contain a cycle.");
    if (visited.has(id)) return;
    visiting.add(id);
    for (const dependency of byID.get(id).depends_on || []) visit(dependency);
    visiting.delete(id);
    visited.add(id);
  }
  for (const id of ids) visit(id);
  return tasks;
}

// Plain-language progress for a goal card. Blocked steps are never retried by
// the server, so the owner is told to review them instead of waiting.
export function goalProgress(goal) {
  const tasks = Array.isArray(goal?.tasks) ? goal.tasks : [];
  const verified = tasks.filter((task) => task.status === "succeeded").length;
  const running = tasks.find((task) => task.status === "running");
  const blocked = tasks.find((task) => task.status === "blocked");
  const failed = tasks.find((task) => task.status === "failed");
  const next = tasks.find(
    (task) => task.status === "pending" &&
      (task.depends_on || []).every((id) =>
        tasks.some((dep) => dep.id === id && dep.status === "succeeded")),
  );
  let next_action;
  if (goal?.status === "draft") next_action = "Review the plan, then start the team.";
  else if (goal?.status === "running" && running) next_action = `Working on “${running.title}”.`;
  else if (goal?.status === "running" && next) next_action = `Next: “${next.title}”.`;
  else if (goal?.status === "running") next_action = "Preparing the next step.";
  else if (goal?.status === "succeeded") next_action = "All steps finished with verified results.";
  else if (goal?.status === "cancelled") next_action = "Cancelled. Completed steps keep their results.";
  else next_action = "Stopped. Review the failed step’s error before planning again.";
  let attention = "";
  if (blocked) attention = `“${blocked.title}” may already have changed something outside Soulacy, so it is paused for review and will not be retried automatically.`;
  else if (failed && goal?.status !== "running") attention = `“${failed.title}” did not finish.`;
  return { verified, total: tasks.length, running: running?.title || "", next: next?.title || "", next_action, attention };
}
