package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/reasoning"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"go.uber.org/zap"
)

// WorkflowExecutor runs a WorkflowSpec step-by-step, checkpointing after each step.
type WorkflowExecutor struct {
	spec   agent.WorkflowSpec
	engine *Engine
	store  *CheckpointStore
	log    *zap.Logger
}

// NewWorkflowExecutor creates a WorkflowExecutor.
func NewWorkflowExecutor(spec agent.WorkflowSpec, engine *Engine, store *CheckpointStore, log *zap.Logger) *WorkflowExecutor {
	return &WorkflowExecutor{
		spec:   spec,
		engine: engine,
		store:  store,
		log:    log,
	}
}

// Run executes the workflow for the given trigger message.
// If resumeRunID is non-empty, execution starts from the first non-completed step.
// Returns the output of the final step.
//
// When the spec declares nodes (Story E25 graph form), Run compiles and
// walks the cyclic graph instead of the linear steps — same checkpoint
// store, same resume semantics (visit-indexed checkpoint keys).
func (w *WorkflowExecutor) Run(ctx context.Context, msg message.Message, resumeRunID string) (json.RawMessage, error) {
	// 1. Determine run ID.
	runID := resumeRunID
	if runID == "" {
		runID = uuid.New().String()
	}

	release, err := w.bindRun(ctx, msg, runID)
	if err != nil {
		return nil, err
	}
	defer release()

	if len(w.spec.Nodes) > 0 {
		return w.runFlow(ctx, msg, runID)
	}

	agentID := msg.AgentID

	// 2. Build initial vars map.
	vars := map[string]interface{}{
		"trigger": flattenParts(msg.Parts),
	}

	var lastResult json.RawMessage
	var outcomeTrace []reasoning.FlowNodeRun

	// 3. Iterate steps.
	for _, step := range w.spec.Steps {
		// 3a. Load checkpoint; if completed, load state into vars and skip.
		if w.store != nil {
			existing, err := w.store.Get(ctx, agentID, runID, step.ID)
			if err != nil && err != sql.ErrNoRows {
				return nil, fmt.Errorf("read workflow checkpoint: %w", err)
			}
			if err == nil && existing.Status == CheckpointCompleted {
				outcomeTrace = append(outcomeTrace, reasoning.FlowNodeRun{NodeID: step.ID, Output: existing.State})
				w.log.Debug("workflow step already completed, skipping",
					zap.String("run_id", runID),
					zap.String("step_id", step.ID),
				)
				if existing.State != nil {
					lastResult = existing.State
					if step.Output != "" {
						var v interface{}
						if jsonErr := json.Unmarshal(existing.State, &v); jsonErr == nil {
							vars[step.Output] = v
						}
					}
				}
				continue
			}
		}

		// 3b. Upsert checkpoint with status = in_progress.
		if w.store != nil {
			if err := w.store.Upsert(ctx, Checkpoint{
				AgentID:   agentID,
				RunID:     runID,
				StepID:    step.ID,
				Status:    CheckpointInProgress,
				UpdatedAt: time.Now().UTC(),
			}); err != nil {
				return nil, fmt.Errorf("persist workflow step before execution: %w", err)
			}
		}

		// 3c. Evaluate step.If condition.
		if step.If != "" {
			condResult, err := renderTemplate(step.If, vars)
			if err != nil {
				w.log.Warn("workflow step If template error",
					zap.String("step_id", step.ID), zap.Error(err))
			}
			condResult = strings.TrimSpace(condResult)
			if condResult == "" || condResult == "false" || condResult == "0" {
				w.log.Debug("workflow step skipped (If condition)",
					zap.String("step_id", step.ID), zap.String("condition_result", condResult))
				if w.store != nil {
					_ = w.store.Upsert(ctx, Checkpoint{
						AgentID:   agentID,
						RunID:     runID,
						StepID:    step.ID,
						Status:    CheckpointCompleted,
						State:     nil,
						UpdatedAt: time.Now().UTC(),
					})
				}
				continue
			}
		}

		// 3d. Render step.Input as a Go template.
		renderedInput := ""
		if step.Input != "" {
			var err error
			renderedInput, err = renderTemplate(step.Input, vars)
			if err != nil {
				return w.handleStepError(ctx, agentID, runID, step, nil, fmt.Errorf("step %q: render input template: %w", step.ID, err))
			}
		}

		// 3e. Call RunTool.
		result, toolErr := w.runStepTool(ctx, msg, step.Tool, renderedInput)

		if toolErr != nil {
			var retryErr error
			switch step.OnError {
			case "skip":
				w.log.Warn("workflow step error, skipping",
					zap.String("step_id", step.ID), zap.Error(toolErr))
				if w.store != nil {
					_ = w.store.Upsert(ctx, Checkpoint{
						AgentID:   agentID,
						RunID:     runID,
						StepID:    step.ID,
						Status:    CheckpointCompleted,
						State:     nil,
						UpdatedAt: time.Now().UTC(),
					})
				}
				continue
			case "retry":
				// Retry once.
				result, retryErr = w.runStepTool(ctx, msg, step.Tool, renderedInput)
				if retryErr != nil {
					return w.handleStepError(ctx, agentID, runID, step, nil, fmt.Errorf("step %q: retry failed: %w", step.ID, retryErr))
				}
			default: // "abort" or empty
				return w.handleStepError(ctx, agentID, runID, step, nil, fmt.Errorf("step %q: tool error: %w", step.ID, toolErr))
			}
		}

		// 3f. Store result in vars if Output is set.
		if step.Output != "" && result != nil {
			var v interface{}
			if jsonErr := json.Unmarshal(result, &v); jsonErr == nil {
				vars[step.Output] = v
			} else {
				// Store as raw string if not valid JSON.
				vars[step.Output] = string(result)
			}
		}
		lastResult = result
		outcomeTrace = append(outcomeTrace, reasoning.FlowNodeRun{NodeID: step.ID, Output: result})

		// 3g. Upsert checkpoint with status = completed.
		if w.store != nil {
			if err := w.store.Upsert(ctx, Checkpoint{
				AgentID:   agentID,
				RunID:     runID,
				StepID:    step.ID,
				Status:    CheckpointCompleted,
				State:     result,
				UpdatedAt: time.Now().UTC(),
			}); err != nil {
				return nil, fmt.Errorf("workflow step executed but its checkpoint could not be saved; verify before retrying: %w", err)
			}
		}
	}

	if err := w.evaluateOutcome(ctx, msg, runID, lastResult, outcomeTrace); err != nil {
		return nil, err
	}
	// 4. Return the output of the last completed step.
	return lastResult, nil
}

// handleStepError marks the checkpoint as failed and returns the error.
func (w *WorkflowExecutor) handleStepError(ctx context.Context, agentID, runID string, step agent.StepSpec, state json.RawMessage, err error) (json.RawMessage, error) {
	w.log.Error("workflow step failed",
		zap.String("step_id", step.ID),
		zap.String("run_id", runID),
		zap.Error(err),
	)
	if w.store != nil {
		_ = w.store.Upsert(ctx, Checkpoint{
			AgentID:   agentID,
			RunID:     runID,
			StepID:    step.ID,
			Status:    CheckpointFailed,
			State:     state,
			UpdatedAt: time.Now().UTC(),
		})
	}
	return nil, err
}

// renderTemplate executes a Go text/template string with vars as data.
func renderTemplate(tmplStr string, vars map[string]interface{}) (string, error) {
	tmpl, err := template.New("").Option("missingkey=zero").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// Linear workflows must use the same permissions, confirmations, timeout and
// observation path as graph workflows and chat. Bare embedded executors retain
// the trusted RunTool API when no agent loader has been supplied.
func (w *WorkflowExecutor) runStepTool(ctx context.Context, msg message.Message, name, input string) (json.RawMessage, error) {
	if w.engine.loader == nil {
		return w.engine.RunTool(ctx, name, input)
	}
	def := w.engine.definitionForContext(ctx, msg.AgentID)
	if def == nil {
		return nil, fmt.Errorf("workflow agent %q is unavailable", msg.AgentID)
	}
	args := map[string]any{}
	if strings.TrimSpace(input) != "" {
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return nil, fmt.Errorf("decode workflow tool arguments: %w", err)
		}
	}
	out, err := w.engine.runTool(ctx, def, msg.SessionID, message.ToolCall{ID: uuid.NewString(), Name: name, Arguments: args})
	if err != nil {
		return nil, err
	}
	if json.Valid([]byte(out)) {
		return json.RawMessage(out), nil
	}
	return json.Marshal(out)
}
