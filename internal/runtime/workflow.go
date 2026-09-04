package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"

	"github.com/google/uuid"
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

	if len(w.spec.Nodes) > 0 {
		return w.runFlow(ctx, msg, runID)
	}

	agentID := msg.AgentID

	// 2. Build initial vars map.
	vars := map[string]interface{}{
		"trigger": flattenParts(msg.Parts),
	}

	var lastResult json.RawMessage

	// 3. Iterate steps.
	for _, step := range w.spec.Steps {
		// 3a. Load checkpoint; if completed, load state into vars and skip.
		if w.store != nil {
			existing, err := w.store.Get(ctx, agentID, runID, step.ID)
			if err == nil && existing.Status == CheckpointCompleted {
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
				w.log.Warn("workflow checkpoint upsert failed",
					zap.String("step_id", step.ID), zap.Error(err))
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
		result, toolErr := w.engine.RunTool(ctx, step.Tool, renderedInput)

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
				result, retryErr = w.engine.RunTool(ctx, step.Tool, renderedInput)
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

		// 3g. Upsert checkpoint with status = completed.
		if w.store != nil {
			_ = w.store.Upsert(ctx, Checkpoint{
				AgentID:   agentID,
				RunID:     runID,
				StepID:    step.ID,
				Status:    CheckpointCompleted,
				State:     result,
				UpdatedAt: time.Now().UTC(),
			})
		}
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
