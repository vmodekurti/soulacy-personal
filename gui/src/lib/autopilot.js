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
  const tasks = JSON.parse(raw);
  if (!Array.isArray(tasks) || !tasks.length)
    throw new Error("Add at least one task.");
  const ids = new Set();
  for (const task of tasks) {
    if (!task.id || ids.has(task.id))
      throw new Error("Every task needs a unique ID.");
    ids.add(task.id);
    if (!task.agent_id || !task.prompt || !task.title)
      throw new Error("Every task needs an agent, title, and prompt.");
    if (!(task.budget?.max_cost_usd > 0) || !(task.budget?.max_duration_ms > 0))
      throw new Error("Every task needs positive cost and duration limits.");
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
