import { describe, expect, it } from "vitest";
import { goalProgress } from "./autopilot.js";

const task = (id, status, depends_on = []) => ({ id, title: id.toUpperCase(), status, depends_on });

describe("goalProgress", () => {
  it("reports the running step and the verified count", () => {
    const p = goalProgress({ status: "running", tasks: [task("a", "succeeded"), task("b", "running", ["a"]), task("c", "pending", ["b"])] });
    expect(p).toMatchObject({ verified: 1, total: 3, running: "B", next_action: "Working on “B”." });
  });

  it("names the next ready step while between steps", () => {
    const p = goalProgress({ status: "running", tasks: [task("a", "succeeded"), task("b", "pending", ["a"])] });
    expect(p.next).toBe("B");
    expect(p.next_action).toBe("Next: “B”.");
  });

  it("tells the owner to review a blocked step instead of waiting", () => {
    const p = goalProgress({ status: "failed", tasks: [task("a", "blocked")] });
    expect(p.attention).toContain("will not be retried automatically");
  });

  it("handles a draft with no steps yet", () => {
    expect(goalProgress({ status: "draft", tasks: [] })).toMatchObject({ verified: 0, total: 0, next_action: "Review the plan, then start the team." });
  });
});
