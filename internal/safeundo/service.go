package safeundo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Authorize is evaluated at preview and immediately before each dispatch,
// including reconciliation. It must check current authority, not a receipt's
// saved role. Nil is denied, not an implicit trusted caller.
type Authorize func() error

func permitted(auth Authorize) error {
	if auth == nil {
		return ErrPermission
	}
	return auth()
}

func (s *Store) Prepare(ctx context.Context, owner, agentID, runID string, input PrepareRequest, auth Authorize) (Job, error) {
	if err := permitted(auth); err != nil {
		return Job{}, err
	}
	if !cleanLabel(owner, 512) || !identifier.MatchString(agentID) || len(runID) > 512 || !cleanLabel(input.Title, 200) || len(input.Changes) == 0 || len(input.Changes) > MaxActions {
		return Job{}, ErrInvalid
	}
	if err := s.enter(ctx); err != nil {
		return Job{}, err
	}
	defer s.leave()
	j := Job{ID: uuid.NewString(), Owner: owner, AgentID: agentID, RunID: runID, Title: input.Title, CreatedAt: s.now(), Actions: []Action{}}
	seen := map[string]bool{}
	for _, input := range input.Changes {
		r, ok := s.resources[agentID+"/"+input.ResourceID]
		if !ok || seen[input.ResourceID] {
			return Job{}, ErrInvalid
		}
		seen[input.ResourceID] = true
		if err := permitted(auth); err != nil {
			return Job{}, err
		}
		snap, err := s.read(ctx, r)
		if err != nil {
			return Job{}, err
		}
		a, err := makeAction(r, input, snap)
		if err != nil {
			return Job{}, err
		}
		j.Actions = append(j.Actions, a)
	}
	if err := s.save(ctx, &j); err != nil {
		return Job{}, err
	}
	return j.Public(), nil
}

func (s *Store) Preview(ctx context.Context, owner, agentID, id, direction string, auth Authorize) (Review, error) {
	if err := permitted(auth); err != nil {
		return Review{}, err
	}
	if direction != "apply" && direction != "undo" && direction != "reconcile" {
		return Review{}, ErrInvalid
	}
	if err := s.enter(ctx); err != nil {
		return Review{}, err
	}
	defer s.leave()
	j, err := s.get(ctx, owner, agentID, id)
	if err != nil {
		return Review{}, err
	}
	if j.Attempts >= 16 || (direction == "apply" && j.UndoStarted) {
		return Review{}, ErrConflict
	}
	plan := &Plan{Token: uuid.NewString(), Direction: direction, ExpiresAt: s.now().Add(5 * time.Minute), Items: []PlanItem{}}
	for i, a := range j.Actions {
		if direction != "reconcile" && (a.Status == "needs_review" || a.Status == "running") {
			return Review{}, ErrUncertain
		}
		wanted := (direction == "apply" && a.Status == "pending") || (direction == "undo" && a.Status == "applied") || (direction == "reconcile" && (a.Status == "needs_review" || a.Status == "running"))
		if !wanted {
			continue
		}
		r, err := s.resource(j, a)
		if err != nil {
			return Review{}, err
		}
		if err := s.competing(ctx, j, a, direction); err != nil {
			return Review{}, err
		}
		if err := permitted(auth); err != nil {
			return Review{}, err
		}
		snap, err := s.read(ctx, r)
		if err != nil {
			return Review{}, err
		}
		item := PlanItem{Index: i, Version: snap.version}
		if direction == "reconcile" {
			switch {
			case matches(snap, a, true):
				item.ObservedStatus = "applied"
			case matches(snap, a, false):
				if a.PriorStatus == "applied" {
					item.ObservedStatus = "undone"
				} else {
					item.ObservedStatus = "pending"
				}
			default:
				return Review{}, ErrConflict
			}
		} else {
			if !matches(snap, a, direction == "undo") {
				return Review{}, ErrConflict
			}
			if !resultFits(snap, a, direction == "apply") {
				return Review{}, ErrLimit
			}
			// Documents are whole-resource reversals, not a text merge. A new
			// version conflicts even if its bytes happen to have changed back.
			if a.Kind == "webdav_text" && snap.version != a.Version {
				return Review{}, ErrConflict
			}
		}
		plan.Items = append(plan.Items, item)
	}
	if len(plan.Items) == 0 {
		return Review{}, ErrConflict
	}
	if direction == "undo" {
		for i, k := 0, len(plan.Items)-1; i < k; i, k = i+1, k-1 {
			plan.Items[i], plan.Items[k] = plan.Items[k], plan.Items[i]
		}
	}
	j.Plan = plan
	if err := s.save(ctx, &j); err != nil {
		return Review{}, err
	}
	warning := "Only these recorded changes are covered. Other tool actions are not undoable. Cross-system changes are not atomic; a partial failure stops the remaining steps."
	if direction == "reconcile" {
		warning = "Inspect the external resources before confirming. This records your acceptance of their current state; it does not prove whether the interrupted request ran. No external writes will be made."
	}
	steps := make([]ReviewStep, 0, len(plan.Items))
	for _, item := range plan.Items {
		result := item.ObservedStatus
		if direction == "apply" {
			result = "applied"
		}
		if direction == "undo" {
			result = "undone"
		}
		steps = append(steps, ReviewStep{Index: item.Index, Result: result})
	}
	return Review{Job: j.Public(), Token: plan.Token, Direction: direction, ExpiresAt: plan.ExpiresAt, Warning: warning, Steps: steps}, nil
}

func (s *Store) Execute(ctx context.Context, owner, agentID, id, token string, auth Authorize) (Job, error) {
	if err := permitted(auth); err != nil {
		return Job{}, err
	}
	if strings.TrimSpace(token) == "" || len(token) > 128 {
		return Job{}, ErrInvalid
	}
	if err := s.enter(ctx); err != nil {
		return Job{}, err
	}
	defer s.leave()
	j, err := s.get(ctx, owner, agentID, id)
	if err != nil {
		return Job{}, err
	}
	// Repeated delivery can read the current receipt, never dispatch it twice.
	if j.LastToken == token {
		return j.Public(), nil
	}
	plan := j.Plan
	if plan == nil || plan.Token != token || !s.now().Before(plan.ExpiresAt) || j.Attempts >= 16 {
		return Job{}, ErrConflict
	}
	// Check every resource before the first write. If any preview is stale,
	// this invocation changes nothing. If-Match closes later per-write races.
	for _, item := range plan.Items {
		if item.Index < 0 || item.Index >= len(j.Actions) {
			return Job{}, ErrUnavailable
		}
		a := j.Actions[item.Index]
		r, err := s.resource(j, a)
		if err != nil {
			return Job{}, err
		}
		if err := s.competing(ctx, j, a, plan.Direction); err != nil {
			return Job{}, err
		}
		if err := permitted(auth); err != nil {
			return Job{}, err
		}
		snap, err := s.read(ctx, r)
		if err != nil {
			return Job{}, err
		}
		if snap.version != item.Version {
			return Job{}, ErrConflict
		}
		if plan.Direction != "reconcile" && !resultFits(snap, a, plan.Direction == "apply") {
			return Job{}, ErrLimit
		}
	}
	j.LastToken, j.Plan, j.Notice = token, nil, ""
	j.Attempts++
	if plan.Direction == "undo" {
		j.UndoStarted = true
	}
	if err := s.persist(&j); err != nil {
		return Job{}, err
	}
	for _, item := range plan.Items {
		a := &j.Actions[item.Index]
		if err := ctx.Err(); err != nil {
			j.Notice = "Stopped before the next action. Review a fresh preview to continue."
			_ = s.persist(&j)
			return j.Public(), nil
		}
		if err := permitted(auth); err != nil {
			j.Notice = ErrPermission.Error()
			_ = s.persist(&j)
			return j.Public(), nil
		}
		r, err := s.resource(j, *a)
		if err != nil {
			j.Notice = ErrPermission.Error()
			_ = s.persist(&j)
			return j.Public(), nil
		}
		if plan.Direction == "reconcile" {
			// The observation must still be current at confirmation. A second
			// GET is deliberate: earlier actions may have consumed time.
			snap, err := s.read(ctx, r)
			if err != nil || snap.version != item.Version {
				j.Notice = ErrConflict.Error()
				_ = s.persist(&j)
				return j.Public(), nil
			}
			a.Status, a.Version, a.Reconciled = item.ObservedStatus, snap.version, true
			if err := s.persist(&j); err != nil {
				return Job{}, err
			}
			continue
		}
		a.PriorStatus, a.Status = a.Status, "running"
		if err := s.persist(&j); err != nil {
			return Job{}, err
		}
		// From this durable intent onward, any crash means needs_review.
		// Persistence may have blocked. Revalidate once more immediately
		// before dispatch instead of relying on pre-commit permission.
		err = permitted(auth)
		if err == nil {
			err = s.mutate(ctx, r, *a, plan.Direction, item.Version)
		}
		if err == nil {
			snap, readErr := s.read(ctx, r)
			if readErr != nil || snap.version == item.Version || !matches(snap, *a, plan.Direction == "apply") {
				err = ErrUncertain
			} else {
				a.Version = snap.version
				if plan.Direction == "apply" {
					a.Status = "applied"
				} else {
					a.Status = "undone"
				}
			}
		}
		if err != nil {
			if errors.Is(err, ErrUncertain) {
				a.Status = "needs_review"
			} else {
				a.Status = a.PriorStatus
			}
			j.Notice = err.Error()
		}
		if saveErr := s.persist(&j); saveErr != nil {
			return Job{}, fmt.Errorf("%w: receipt could not be saved", ErrUncertain)
		}
		if err != nil {
			return j.Public(), nil
		}
	}
	if plan.Direction == "reconcile" {
		j.Notice = "Current resource state was accepted by an operator, not verified as the original request's result."
	}
	if err := s.persist(&j); err != nil {
		return Job{}, err
	}
	return j.Public(), nil
}
