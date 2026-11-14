// @vitest-environment jsdom
import { tick } from "svelte";
import { expect, it, vi } from "vitest";
import Autopilot from "./Autopilot.svelte";

const { apiFetch } = vi.hoisted(() => ({ apiFetch: vi.fn(async () => ({})) }));
vi.mock("../lib/api.js", () => ({
  apiFetch,
  api: { agents: { list: async () => ({ agents: [{ id: "research" }, { id: "review" }] }) } },
}));
vi.mock("../lib/TourButton.svelte", () => ({ default: () => {} }));
vi.mock("../lib/SafeUndo.svelte", () => ({ default: () => {} }));

it("saves a guided goal as an ordered draft without starting a run", async () => {
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
  const target = document.createElement("div");
  document.body.appendChild(target);
  const page = new Autopilot({ target });
  const button = (text) => [...target.querySelectorAll("button")].find(
    (element) => element.textContent.trim() === text,
  );
  const change = (element, value, event = "input") => {
    element.value = value;
    element.dispatchEvent(new Event(event, { bubbles: true }));
  };

  try {
    await vi.waitFor(() => expect(apiFetch).toHaveBeenCalledWith("/autopilot/goals"));
    button("Goals").click();
    await tick();
    expect(button("Save goal draft").disabled).toBe(true);
    const panel = button("Save goal draft").closest("section");
    change(panel.querySelectorAll("input")[0], "Trip plan");
    change(panel.querySelectorAll("input")[1], "Plan the trip");
    button("Add step").click();
    await tick();
    const [first, second] = panel.querySelectorAll("article");
    change(first.querySelector("input"), "Research");
    change(first.querySelector("select"), "research", "change");
    change(first.querySelector("textarea"), "Collect options");
    change(second.querySelector("input"), "Review");
    change(second.querySelector("select"), "review", "change");
    change(second.querySelector("textarea"), "Check the options");
    await tick();
    expect(button("Save goal draft").disabled).toBe(false);
    button("Save goal draft").click();

    await vi.waitFor(() => {
      const writes = apiFetch.mock.calls.filter(([, options]) => options?.method === "POST");
      expect(writes).toHaveLength(1);
      const [[path, options]] = writes;
      expect(path).toBe("/autopilot/goals");
      const budget = { max_cost_usd: 1, max_duration_ms: 600000 };
      expect(JSON.parse(options.body)).toEqual({
        title: "Trip plan",
        objective: "Plan the trip",
        budget,
        tasks: [
          { id: "step-1", title: "Research", prompt: "Collect options", agent_id: "research", depends_on: [], budget },
          { id: "step-2", title: "Review", prompt: "Check the options", agent_id: "review", depends_on: ["step-1"], budget },
        ],
      });
    });
  } finally {
    page.$destroy();
    target.remove();
    confirm.mockRestore();
  }
});

it("fills the editor from a described goal without saving or starting it", async () => {
  apiFetch.mockClear();
  apiFetch.mockImplementation(async (path) => path === "/autopilot/goals/draft"
    ? { saved: false, draft: { title: "Lisbon", objective: "Compare trips", steps: [
        { id: "step-1", title: "Research", prompt: "Find options", agent_id: "research" },
        { id: "step-2", title: "Review", prompt: "Check options", agent_id: "review" },
      ] } }
    : {});
  const target = document.createElement("div");
  document.body.appendChild(target);
  const page = new Autopilot({ target });
  const button = (text) => [...target.querySelectorAll("button")].find((element) => element.textContent.trim() === text);
  try {
    await vi.waitFor(() => expect(apiFetch).toHaveBeenCalledWith("/autopilot/goals"));
    button("Goals").click();
    await tick();
    const area = [...target.querySelectorAll("textarea")].find((element) => element.placeholder.startsWith("For example"));
    area.value = "Compare Lisbon trips with a review";
    area.dispatchEvent(new Event("input", { bubbles: true }));
    await tick();
    button("Draft plan from description").click();
    await vi.waitFor(() => expect(target.textContent).toContain("Draft ready"));
    expect(apiFetch).toHaveBeenCalledWith("/autopilot/goals/draft", expect.objectContaining({ method: "POST" }));
    expect(target.querySelectorAll("article.task")).toHaveLength(2);
    expect(button("Save goal draft").disabled).toBe(false);
    const writes = apiFetch.mock.calls.filter(([path, options]) => options?.method === "POST" && path !== "/autopilot/goals/draft");
    expect(writes).toHaveLength(0);
  } finally {
    page.$destroy();
    target.remove();
    apiFetch.mockImplementation(async () => ({}));
  }
});

it("does not record an outcome for a blocked step without evidence and measurements", async () => {
  apiFetch.mockClear();
  apiFetch.mockImplementation(async (path) => path === "/autopilot/goals"
    ? { goals: [{ id: "g1", title: "Restarted", objective: "o", status: "running", cost_usd: 0, duration_ms: 0,
        budget: { max_cost_usd: 1, max_duration_ms: 600000 },
        tasks: [{ id: "first", title: "First", agent_id: "research", status: "blocked", depends_on: [], error: "unknown" }] }] }
    : {});
  const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
  const target = document.createElement("div");
  document.body.appendChild(target);
  const page = new Autopilot({ target });
  const button = (text) => [...target.querySelectorAll("button")].find((element) => element.textContent.trim() === text);
  try {
    await vi.waitFor(() => expect(button("Goals")).toBeDefined());
    button("Goals").click();
    await vi.waitFor(() => expect(target.textContent).toContain("Restarted"));
    expect(button("Resume")).toBeUndefined();
    button("Record outcome").click();
    await vi.waitFor(() => expect(target.textContent).toContain("Enter the evidence"));
    const writes = apiFetch.mock.calls.filter(([path, options]) => options?.method === "POST");
    expect(writes).toHaveLength(0);
  } finally {
    page.$destroy();
    target.remove();
    confirm.mockRestore();
    apiFetch.mockImplementation(async () => ({}));
  }
});
