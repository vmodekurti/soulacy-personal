package autopilot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func goalBudget(cost float64, duration int64) ExecutionBudget {
	return ExecutionBudget{MaxCostUSD: cost, MaxDurationMS: duration}
}

func TestConcurrentGoalTaskStartsCannotOversubscribeCost(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	draft := twoTaskGoal()
	draft.Budget.MaxCostUSD = .5
	for i := range draft.Tasks {
		draft.Tasks[i].DependsOn = nil
		draft.Tasks[i].Budget.MaxCostUSD = .4
	}
	goal, err := store.CreateGoal(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoal(ctx, "alice", goal.ID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, taskID := range []string{"research", "publish"} {
		wg.Add(1)
		go func(taskID, runID string) {
			defer wg.Done()
			<-start
			_, err := store.StartGoalTask(ctx, "alice", goal.ID, taskID, runID)
			errs <- err
		}(taskID, fmtID(i))
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded, conflicted := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected start error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("starts: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	goal, err = store.GetGoal(ctx, "alice", goal.ID)
	if err != nil || goal.ReservedCostUSD != .4 {
		t.Fatalf("reserved cost = %v, goal=%+v err=%v", goal.ReservedCostUSD, goal, err)
	}
}

func twoTaskGoal() GoalDraft {
	return GoalDraft{
		Subject: "alice", Title: "Launch", Objective: "research then publish",
		Budget: goalBudget(1, 10_000),
		Tasks: []GoalTaskDraft{
			{ID: "research", Title: "Research", Prompt: "Find facts", AgentID: "researcher", Budget: goalBudget(.4, 2_000)},
			{ID: "publish", Title: "Publish", Prompt: "Write result", AgentID: "writer", DependsOn: []string{"research"}, Budget: goalBudget(.4, 2_000)},
		},
	}
}

func TestGoalDAGTransitionsAndDependencyResults(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	goal, err := store.CreateGoal(ctx, twoTaskGoal())
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != GoalStatusDraft || len(goal.Tasks) != 2 {
		t.Fatalf("created goal = %+v", goal)
	}
	goal, err = store.StartGoal(ctx, "alice", goal.ID)
	if err != nil || goal.Status != GoalRunning {
		t.Fatalf("start = %+v, err=%v", goal, err)
	}
	ready, err := store.ReadyGoalTasks(ctx, "alice", goal.ID)
	if err != nil || len(ready) != 1 || ready[0].ID != "research" {
		t.Fatalf("initial ready = %+v, err=%v", ready, err)
	}
	if _, err := store.StartGoalTask(ctx, "alice", goal.ID, "publish", "too-early"); !errors.Is(err, ErrConflict) {
		t.Fatalf("started dependent task early: %v", err)
	}
	if _, err := store.StartGoalTask(ctx, "alice", goal.ID, "research", "run-research"); err != nil {
		t.Fatal(err)
	}
	cost, duration := .2, int64(100)
	goal, err = store.FinishGoalTask(ctx, FinishGoalTaskRequest{
		Subject: "alice", GoalID: goal.ID, TaskID: "research", Status: GoalTaskSucceeded,
		ProofID: "proof-research", Result: "three sourced facts", CostUSD: &cost, DurationMS: &duration,
	})
	if err != nil || goal.Status != GoalRunning || goal.Tasks[0].Result != "three sourced facts" {
		t.Fatalf("finish research = %+v, err=%v", goal, err)
	}
	ready, err = store.ReadyGoalTasks(ctx, "alice", goal.ID)
	if err != nil || len(ready) != 1 || ready[0].ID != "publish" {
		t.Fatalf("publish ready = %+v, err=%v", ready, err)
	}
	if _, err := store.StartGoalTask(ctx, "alice", goal.ID, "publish", "run-publish"); err != nil {
		t.Fatal(err)
	}
	goal, err = store.FinishGoalTask(ctx, FinishGoalTaskRequest{
		Subject: "alice", GoalID: goal.ID, TaskID: "publish", Status: GoalTaskSucceeded,
		ProofID: "proof-publish", Result: "published", CostUSD: &cost, DurationMS: &duration,
	})
	if err != nil || goal.Status != GoalSucceeded || goal.CostUSD != .4 || goal.FinishedAt == nil {
		t.Fatalf("completed goal = %+v, err=%v", goal, err)
	}
}

func TestGoalRejectsCyclesMissingDependenciesAndUnboundedBudgets(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	draft := twoTaskGoal()
	draft.Tasks[0].DependsOn = []string{"publish"}
	if _, err := store.CreateGoal(ctx, draft); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle error = %v", err)
	}
	draft = twoTaskGoal()
	draft.Tasks[1].DependsOn = []string{"missing"}
	if _, err := store.CreateGoal(ctx, draft); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing dependency error = %v", err)
	}
	draft = twoTaskGoal()
	draft.Budget.MaxDurationMS = 0
	if _, err := store.CreateGoal(ctx, draft); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded duration error = %v", err)
	}
	draft = twoTaskGoal()
	draft.Budget.MaxCostUSD = 0
	for i := range draft.Tasks {
		draft.Tasks[i].Budget.MaxCostUSD = 0
	}
	if _, err := store.CreateGoal(ctx, draft); err != nil {
		t.Fatalf("zero-cost bound should be valid: %v", err)
	}
}

func TestGoalBudgetReservationAndFailClosedMeasurement(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	draft := twoTaskGoal()
	draft.Budget.MaxCostUSD = .5
	draft.Tasks[0].DependsOn = nil
	draft.Tasks[1].DependsOn = nil
	draft.Tasks[0].Budget.MaxCostUSD = .4
	draft.Tasks[1].Budget.MaxCostUSD = .4
	goal, err := store.CreateGoal(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoal(ctx, "alice", goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoalTask(ctx, "alice", goal.ID, "research", "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoalTask(ctx, "alice", goal.ID, "publish", "r2"); !errors.Is(err, ErrConflict) {
		t.Fatalf("oversubscribed goal budget: %v", err)
	}
	actual, duration := .1, int64(20)
	if _, err := store.FinishGoalTask(ctx, FinishGoalTaskRequest{
		Subject: "alice", GoalID: goal.ID, TaskID: "research", Status: GoalTaskSucceeded,
		CostUSD: &actual, DurationMS: &duration,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoalTask(ctx, "alice", goal.ID, "publish", "r2"); err != nil {
		t.Fatalf("released reservation did not permit task: %v", err)
	}
	goal, err = store.FinishGoalTask(ctx, FinishGoalTaskRequest{
		Subject: "alice", GoalID: goal.ID, TaskID: "publish", Status: GoalTaskSucceeded,
		// nil measurements must fail closed rather than becoming observed zero.
	})
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != GoalFailed || goal.Tasks[1].Status != GoalTaskFailed || !strings.Contains(goal.Tasks[1].Error, "measurement") {
		t.Fatalf("missing measurements did not fail closed: %+v", goal)
	}
}

func TestFailGoalPreservesInFlightTaskAndBlocksPendingWork(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	draft := twoTaskGoal()
	draft.Tasks[0].DependsOn = nil
	draft.Tasks[1].DependsOn = nil
	goal, err := store.CreateGoal(ctx, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoal(ctx, "alice", goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoalTask(ctx, "alice", goal.ID, "research", "in-flight"); err != nil {
		t.Fatal(err)
	}
	goal, err = store.FailGoal(ctx, "alice", goal.ID, "remaining duration budget cannot admit publish")
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != GoalFailed || !strings.Contains(goal.Error, "duration budget") || goal.FinishedAt == nil {
		t.Fatalf("failed goal = %+v", goal)
	}
	if goal.ReservedCostUSD != .4 || goal.Tasks[0].Status != GoalTaskRunning || goal.Tasks[1].Status != GoalTaskBlocked {
		t.Fatalf("failure changed in-flight work or left pending work schedulable: %+v", goal)
	}
	if !strings.Contains(goal.Tasks[1].Error, "duration budget") {
		t.Fatalf("pending task did not preserve the admission reason: %+v", goal.Tasks[1])
	}
	finishedAt := *goal.FinishedAt
	cost, duration := .2, int64(100)
	goal, err = store.FinishGoalTask(ctx, FinishGoalTaskRequest{
		Subject: "alice", GoalID: goal.ID, TaskID: "research", Status: GoalTaskSucceeded,
		Result: "late confirmed result", CostUSD: &cost, DurationMS: &duration,
	})
	if err != nil {
		t.Fatal(err)
	}
	if goal.Status != GoalFailed || goal.FinishedAt == nil || !goal.FinishedAt.Equal(finishedAt) || goal.ReservedCostUSD != 0 {
		t.Fatalf("late completion incorrectly revived failed goal: %+v", goal)
	}
}

func TestGoalCrashQuarantineRequiresExplicitResolution(t *testing.T) {
	store, path := openTestStore(t)
	ctx := context.Background()
	goal, err := store.CreateGoal(ctx, twoTaskGoal())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoal(ctx, "alice", goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoalTask(ctx, "alice", goal.ID, "research", "crashed-run"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	goal, err = reopened.GetGoal(ctx, "alice", goal.ID)
	if err != nil || goal.Tasks[0].Status != GoalTaskBlocked || !strings.Contains(goal.Tasks[0].Error, "unknown") {
		t.Fatalf("recovered goal = %+v, err=%v", goal, err)
	}
	ready, err := reopened.ReadyGoalTasks(ctx, "alice", goal.ID)
	if err != nil || len(ready) != 0 {
		t.Fatalf("crash-uncertain task allowed scheduling: %+v, err=%v", ready, err)
	}
	cost, duration := .2, int64(100)
	goal, err = reopened.ResolveBlockedGoalTask(ctx, FinishGoalTaskRequest{
		Subject: "alice", GoalID: goal.ID, TaskID: "research", Status: GoalTaskSucceeded,
		ProofID: "manual-evidence", Result: "verified externally", CostUSD: &cost, DurationMS: &duration,
	})
	if err != nil {
		t.Fatal(err)
	}
	ready, err = reopened.ReadyGoalTasks(ctx, "alice", goal.ID)
	if err != nil || len(ready) != 1 || ready[0].ID != "publish" {
		t.Fatalf("manual resolution did not resume DAG: %+v, err=%v", ready, err)
	}
}

func TestGoalResultBoundAndCancellation(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	goal, err := store.CreateGoal(ctx, twoTaskGoal())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoal(ctx, "alice", goal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartGoalTask(ctx, "alice", goal.ID, "research", "r"); err != nil {
		t.Fatal(err)
	}
	cost, duration := .1, int64(1)
	if _, err := store.FinishGoalTask(ctx, FinishGoalTaskRequest{
		Subject: "alice", GoalID: goal.ID, TaskID: "research", Status: GoalTaskSucceeded,
		Result: strings.Repeat("x", maxTaskResultBytes+1), CostUSD: &cost, DurationMS: &duration,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized result = %v", err)
	}
	goal, err = store.CancelGoal(ctx, "alice", goal.ID)
	if err != nil || goal.Status != GoalCancelled || goal.Tasks[0].Status != GoalTaskCancelled || goal.Tasks[1].Status != GoalTaskCancelled {
		t.Fatalf("cancelled goal = %+v, err=%v", goal, err)
	}
}
