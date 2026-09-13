import { describe, it, expect } from "vitest";
import {
  rate,
  money,
  canAcceptProposal,
  candidateProofs,
  parseGoalTasks,
} from "./autopilot.js";
describe("Autopilot evidence boundary", () => {
  it("does not represent unknown measurements as zero", () => {
    expect(rate(null)).toBe("Unmeasured");
    expect(money(undefined)).toBe("Unmeasured");
    expect(rate(0)).toBe("0%");
    expect(money(0)).toBe("$0.0000");
  });
  it("requires independently recorded baseline failure and candidate success", () => {
    expect(
      canAcceptProposal({
        status: "pending",
        baseline: { status: "unknown" },
        candidate: { status: "pass" },
      }),
    ).toBe(false);
    expect(
      canAcceptProposal({
        status: "pending",
        baseline: { status: "fail" },
        candidate: { status: "pass" },
      }),
    ).toBe(true);
    expect(
      candidateProofs(
        [
          { id: "sim", agent_id: "a", outcome: "succeeded", simulation: true },
          { id: "other", agent_id: "b", outcome: "succeeded" },
        ],
        { agent_id: "a" },
      ),
    ).toEqual([]);
  });
  it("rejects unsafe goal graphs before submission", () => {
    const a = {
      id: "a",
      title: "A",
      agent_id: "agent",
      prompt: "work",
      budget: { max_cost_usd: 1, max_duration_ms: 1000 },
    };
    expect(parseGoalTasks(JSON.stringify([a]))).toHaveLength(1);
    expect(() => parseGoalTasks(JSON.stringify([a, a]))).toThrow("unique");
    expect(() =>
      parseGoalTasks(
        JSON.stringify([
          { ...a, depends_on: ["b"] },
          { ...a, id: "b", depends_on: ["a"] },
        ]),
      ),
    ).toThrow("cycle");
  });
});
