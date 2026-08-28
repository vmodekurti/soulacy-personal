// strategies.go — the built-in reasoning strategies, registered with the
// SDK factory registry (Story E15) exactly like channel/provider drivers
// (E10): init() self-registration, resolved by name at Loop.Run time.
// Custom strategies follow the same pattern from their own packages.
package reasoning

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	sdkreasoning "github.com/soulacy/soulacy/sdk/reasoning"
	"github.com/soulacy/soulacy/sdk/registry"
)

func init() {
	registry.MustRegisterReasoningStrategy(StrategyReAct, func(cfg map[string]any) (sdkreasoning.Strategy, error) {
		return reactStrategy{}, nil
	})
	registry.MustRegisterReasoningStrategy(StrategyPlanExecute, func(cfg map[string]any) (sdkreasoning.Strategy, error) {
		return planExecuteStrategy{}, nil
	})
}

// ─── ReAct ────────────────────────────────────────────────────────────────────

// reactStrategy runs interleaved think/act/observe cycles until the LLM
// reports IsDone, MaxSteps is exhausted, or the context expires — then
// reflects on whatever trace exists.
type reactStrategy struct{}

const (
	maxConsecutiveThinkErrors = 4
	maxTotalThinkErrors       = 8
	// minThinkErrorsBeforeSalvage is how many CONSECUTIVE malformed reasoning
	// steps must occur before the loop abandons the run and reflects on what it
	// has. Every malformed step already appends a targeted format-repair
	// instruction to the trace; salvaging at 1 meant that instruction was never
	// once acted on, and a transient parse slip cost the whole remaining step
	// budget. At 2, the model gets exactly one corrected turn.
	minThinkErrorsBeforeSalvage = 2
)

func (reactStrategy) Run(ctx context.Context, env Env, taskInput string) ([]Step, ReflectResponse) {
	var steps []Step
	consecutiveThinkErrors := 0
	totalThinkErrors := 0
	lastFailedToolKey := ""
	repeatedFailedToolCalls := 0

	for i := 0; i < env.Config.MaxSteps; i++ {
		if ctx.Err() != nil {
			break
		}

		stepCtx, cancel := context.WithTimeout(ctx, env.Config.StepTimeout)
		stepStart := time.Now()

		think, err := env.LLM.Think(stepCtx, ThinkRequest{
			TaskInput:    taskInput,
			StepHistory:  steps,
			SystemPrompt: env.Config.SystemPrompt,
			ToolNames:    env.Config.ToolNames,
		})
		if err != nil {
			consecutiveThinkErrors++
			totalThinkErrors++
			steps = append(steps, Step{
				ID:      fmt.Sprintf("step-%d", i+1),
				Thought: "The model returned an invalid reasoning step.",
				Obs: Observation{
					Content: thinkErrorInstruction(err, consecutiveThinkErrors, totalThinkErrors, env.Config.ToolNames),
					Source:  "controller",
				},
				Duration: time.Since(stepStart),
			})
			cancel()
			// Salvage only after the format-repair instruction just appended has
			// had a turn to work. Bailing to Reflect on the FIRST malformed step
			// threw away the remaining step budget (23 of 30 on the run that
			// motivated this) and shipped a half-finished narration as the answer.
			if consecutiveThinkErrors >= minThinkErrorsBeforeSalvage && lastUsefulObservation(steps) != "" {
				if resp, ok := reflectAfterRepeatedThinkErrors(ctx, env, taskInput, steps); ok {
					return steps, resp
				}
				if consecutiveThinkErrors >= minThinkErrorsBeforeSalvage+1 {
					return steps, ReflectResponse{Output: synthesizeAfterThinkErrors(steps)}
				}
			}
			if consecutiveThinkErrors >= maxConsecutiveThinkErrors || totalThinkErrors >= maxTotalThinkErrors {
				return steps, ReflectResponse{Output: thinkErrorStopMessage(steps)}
			}
			continue
		}
		if think.IsDone {
			consecutiveThinkErrors = 0
			if call, ok := recoverTextualToolCall(think.FinalAnswer, env.Config.ToolNames); ok {
				obs := env.Tools.Execute(stepCtx, call)
				obs = boundObservation(obs)
				steps = append(steps, Step{
					ID:       fmt.Sprintf("step-%d", i+1),
					Thought:  firstNonEmpty(think.Thought, "Recovered plain-text tool call from final_answer."),
					Action:   call,
					Obs:      obs,
					Duration: time.Since(stepStart),
				})
				cancel()
				continue
			}
			if isPrematureFinalAnswer(think.FinalAnswer) && i < env.Config.MaxSteps-1 {
				steps = append(steps, Step{
					ID:      fmt.Sprintf("step-%d", i+1),
					Thought: firstNonEmpty(think.Thought, "The model returned a progress note instead of a final answer."),
					Obs: Observation{
						Content: "controller: that was a progress note, not a completed result. Continue by making the next concrete tool call; do not say you are proceeding unless the work is actually complete.",
						Source:  "controller",
					},
				})
				cancel()
				continue
			}
			cancel()
			resp, _ := env.LLM.Reflect(ctx, ReflectRequest{
				TaskInput:    taskInput,
				Steps:        steps,
				SystemPrompt: env.Config.SystemPrompt,
				OutputFormat: env.Config.OutputFormat,
			})
			if resp.Output == "" && think.FinalAnswer != "" {
				resp.Output = think.FinalAnswer
			}
			if call, ok := recoverTextualToolCall(resp.Output, env.Config.ToolNames); ok {
				recoveredCtx, recoveredCancel := context.WithTimeout(ctx, env.Config.StepTimeout)
				recoveredStart := time.Now()
				obs := env.Tools.Execute(recoveredCtx, call)
				recoveredCancel()
				obs = boundObservation(obs)
				steps = append(steps, Step{
					ID:       fmt.Sprintf("step-%d", i+1),
					Thought:  firstNonEmpty(think.Thought, "Recovered plain-text tool call from reflected output."),
					Action:   call,
					Obs:      obs,
					Duration: time.Since(recoveredStart),
				})
				continue
			}
			if isPrematureFinalAnswer(resp.Output) && i < env.Config.MaxSteps-1 {
				steps = append(steps, Step{
					ID:      fmt.Sprintf("step-%d", i+1),
					Thought: firstNonEmpty(think.Thought, "The model reflected a progress note instead of a final answer."),
					Obs: Observation{
						Content: "controller: reflected output was a progress note, not a completed result. Continue by making the next concrete tool call.",
						Source:  "controller",
					},
				})
				continue
			}
			return steps, resp
		}

		if strings.TrimSpace(think.Action.Tool) == "" {
			consecutiveThinkErrors++
			totalThinkErrors++
			steps = append(steps, Step{
				ID:      fmt.Sprintf("step-%d", i+1),
				Thought: firstNonEmpty(think.Thought, "The model returned an invalid reasoning step."),
				Obs: Observation{
					Content: invalidActionInstruction(consecutiveThinkErrors, totalThinkErrors, env.Config.ToolNames),
					Source:  "controller",
				},
				Duration: time.Since(stepStart),
			})
			cancel()
			// Same rule as the Think-error path above: give the repair
			// instruction a turn before spending the run on a salvage Reflect.
			if consecutiveThinkErrors >= minThinkErrorsBeforeSalvage && lastUsefulObservation(steps) != "" {
				if resp, ok := reflectAfterRepeatedThinkErrors(ctx, env, taskInput, steps); ok {
					return steps, resp
				}
				if consecutiveThinkErrors >= minThinkErrorsBeforeSalvage+1 {
					return steps, ReflectResponse{Output: synthesizeAfterInvalidActions(steps)}
				}
			}
			if consecutiveThinkErrors >= maxConsecutiveThinkErrors || totalThinkErrors >= maxTotalThinkErrors {
				return steps, ReflectResponse{Output: invalidActionStopMessage(steps)}
			}
			continue
		}

		consecutiveThinkErrors = 0

		// Execute the tool — failures are wrapped as observations, not panics.
		obs := env.Tools.Execute(stepCtx, think.Action)
		obs = boundObservation(obs)
		callKey := toolCallKey(think.Action)
		repeatedToolFailure := false
		if isToolFailure(obs) && callKey != "" {
			if callKey == lastFailedToolKey {
				repeatedFailedToolCalls++
			} else {
				lastFailedToolKey = callKey
				repeatedFailedToolCalls = 1
			}
			repeatedToolFailure = repeatedFailedToolCalls >= 2
		} else {
			lastFailedToolKey = ""
			repeatedFailedToolCalls = 0
		}

		steps = append(steps, Step{
			ID:       fmt.Sprintf("step-%d", i+1),
			Thought:  think.Thought,
			Action:   think.Action,
			Obs:      obs,
			Duration: time.Since(stepStart),
		})
		cancel()
		if repeatedToolFailure {
			steps = append(steps, Step{
				ID:      fmt.Sprintf("step-%d-recovery", i+1),
				Thought: "Repeated identical tool failure detected.",
				Obs: Observation{
					Content: repeatedToolFailureInstruction(think.Action, obs, repeatedFailedToolCalls),
					Source:  "controller",
				},
			})
			if repeatedFailedToolCalls >= 3 {
				if resp, ok := reflectAfterRepeatedToolFailures(ctx, env, taskInput, steps); ok {
					return steps, resp
				}
				return steps, ReflectResponse{Output: repeatedToolFailureStopMessage(steps)}
			}
		}
	}

	// MaxSteps exhausted or LLM errored — reflect on what we have.
	resp, _ := env.LLM.Reflect(ctx, ReflectRequest{
		TaskInput:    taskInput,
		Steps:        steps,
		SystemPrompt: env.Config.SystemPrompt,
		OutputFormat: env.Config.OutputFormat,
	})
	if call, ok := recoverTextualToolCall(resp.Output, env.Config.ToolNames); ok {
		stepCtx, cancel := context.WithTimeout(ctx, env.Config.StepTimeout)
		stepStart := time.Now()
		obs := env.Tools.Execute(stepCtx, call)
		cancel()
		obs = boundObservation(obs)
		steps = append(steps, Step{
			ID:       fmt.Sprintf("step-%d", len(steps)+1),
			Thought:  "Recovered plain-text tool call from reflected output.",
			Action:   call,
			Obs:      obs,
			Duration: time.Since(stepStart),
		})
		resp, _ = env.LLM.Reflect(ctx, ReflectRequest{
			TaskInput:    taskInput,
			Steps:        steps,
			SystemPrompt: env.Config.SystemPrompt,
			OutputFormat: env.Config.OutputFormat,
		})
	}

	// Final guard: never end the run on a progress note ("I'll…", "Searching…").
	// If the terminal reflection is still a preamble, force one completion-only
	// synthesis; failing that, fall back to the last substantive observation so
	// the user gets the actual gathered result instead of an intent statement.
	if isPrematureFinalAnswer(resp.Output) {
		forced, _ := env.LLM.Reflect(ctx, ReflectRequest{
			TaskInput: taskInput,
			Steps:     steps,
			SystemPrompt: strings.TrimSpace(env.Config.SystemPrompt +
				"\n\nIMPORTANT: The task is complete. Output the FINISHED result now, using only what the steps already gathered. Do NOT describe what you are about to do or say you are proceeding — produce the completed answer itself."),
			OutputFormat: env.Config.OutputFormat,
		})
		if t := strings.TrimSpace(forced.Output); t != "" && !isPrematureFinalAnswer(t) {
			resp = forced
		} else if obs := lastUsefulObservation(steps); strings.TrimSpace(obs) != "" {
			resp.Output = obs
		}
	}
	return steps, resp
}

func thinkErrorInstruction(err error, consecutive, total int, toolNames []string) string {
	return fmt.Sprintf("controller: Think failed (%s). Return exactly one valid JSON object. %s Keep thought under 25 words. If a tool is needed, use action with concise arguments. Invalid response %d in a row, %d total this run.", err.Error(), reactJSONRepairGuide(toolNames), consecutive, total)
}

func invalidActionInstruction(consecutive, total int, toolNames []string) string {
	return fmt.Sprintf("controller: invalid reasoning step. is_done=false requires action.tool and action.arguments. Choose one available tool, or set is_done=true with final_answer if the work is complete. %s Invalid action %d in a row, %d total this run.", reactJSONRepairGuide(toolNames), consecutive, total)
}

func reactJSONRepairGuide(toolNames []string) string {
	tools := compactToolList(toolNames, 14)
	if tools == "" {
		tools = "the available tool list"
	} else {
		tools = "one of these tools exactly: " + tools
	}
	return `Use ` + tools + `. Valid shapes: {"thought":"short reason","is_done":false,"action":{"tool":"TOOL_NAME","arguments":{}}} or {"thought":"done","is_done":true,"final_answer":"answer"}. Do not include Markdown, code fences, prose before JSON, or progress-only final answers.`
}

func compactToolList(toolNames []string, limit int) string {
	if limit <= 0 || len(toolNames) == 0 {
		return ""
	}
	seen := map[string]bool{}
	out := make([]string, 0, minInt(len(toolNames), limit))
	for _, tool := range toolNames {
		tool = strings.TrimSpace(tool)
		if tool == "" || seen[tool] {
			continue
		}
		seen[tool] = true
		if len(out) < limit {
			out = append(out, tool)
		}
	}
	if len(out) == 0 {
		return ""
	}
	if len(seen) > len(out) {
		return strings.Join(out, ", ") + fmt.Sprintf(", +%d more", len(seen)-len(out))
	}
	return strings.Join(out, ", ")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func toolCallKey(call ToolCall) string {
	if strings.TrimSpace(call.Tool) == "" {
		return ""
	}
	args := call.Arguments
	if len(args) == 0 && len(call.Input) > 0 {
		args = make(map[string]any, len(call.Input))
		for k, v := range call.Input {
			args[k] = v
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return strings.TrimSpace(call.Tool)
	}
	return strings.TrimSpace(call.Tool) + ":" + string(raw)
}

func isToolFailure(obs Observation) bool {
	if obs.Error != nil {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(obs.Content)), "tool error:")
}

func repeatedToolFailureInstruction(call ToolCall, obs Observation, repeated int) string {
	msg := strings.TrimSpace(obs.Content)
	if msg == "" && obs.Error != nil {
		msg = obs.Error.Error()
	}
	if msg == "" {
		msg = "the tool failed without a message"
	}
	return fmt.Sprintf("controller: the exact same %q tool call failed %d times. Do not repeat it unchanged. Change the arguments, choose a different available tool, or finish with a concise explanation of the failure. Last failure: %s", call.Tool, repeated, truncateForPrompt(msg, 360))
}

func reflectAfterRepeatedToolFailures(ctx context.Context, env Env, taskInput string, steps []Step) (ReflectResponse, bool) {
	resp, err := env.LLM.Reflect(ctx, ReflectRequest{
		TaskInput: taskInput,
		Steps:     steps,
		SystemPrompt: strings.TrimSpace(env.Config.SystemPrompt +
			"\n\nIMPORTANT: The same tool call failed repeatedly. Do not propose running that identical call again. Produce the best available answer from the trace, or clearly explain what configuration/input is missing."),
		OutputFormat: env.Config.OutputFormat,
	})
	if err != nil || strings.TrimSpace(resp.Output) == "" {
		return ReflectResponse{}, false
	}
	if isPrematureFinalAnswer(resp.Output) {
		return ReflectResponse{}, false
	}
	return resp, true
}

func repeatedToolFailureStopMessage(steps []Step) string {
	last := lastPresentableObservation(steps)
	if last != "" {
		return "The run stopped because the same tool call failed repeatedly. Last useful observation: " + last
	}
	return "The run stopped because the same tool call failed repeatedly. Change the tool arguments, choose another tool, or verify the required channel/provider/credential configuration."
}

func thinkErrorStopMessage(steps []Step) string {
	last := lastPresentableObservation(steps)
	if last != "" {
		return "The run stopped because the model returned invalid ReAct JSON too many times. The last useful observation was: " + last
	}
	return "The run stopped because the model returned invalid ReAct JSON too many times before producing a usable tool result. Retry with a smaller input, choose a more reliable model, or switch this workflow step to a deterministic tool/flow node."
}

func invalidActionStopMessage(steps []Step) string {
	last := lastPresentableObservation(steps)
	if last != "" {
		return "The run stopped because the model repeatedly returned is_done=false without a usable action.tool. The last useful observation was: " + last
	}
	return "The run stopped because the model repeatedly returned is_done=false without a usable action.tool. Retry with a smaller input, choose a more reliable model, or switch this workflow step to a deterministic tool/flow node."
}

func synthesizeAfterThinkErrors(steps []Step) string {
	observations := recentUsefulObservations(steps, 3)
	if len(observations) == 0 {
		return thinkErrorStopMessage(steps)
	}
	var b strings.Builder
	b.WriteString("The run stopped because the model repeatedly returned invalid ReAct JSON after useful work was already completed. Here is the best available result from the successful steps:\n\n")
	for i, obs := range observations {
		if len(observations) > 1 {
			fmt.Fprintf(&b, "%d. %s\n", i+1, obs)
		} else {
			b.WriteString(obs)
		}
	}
	return b.String()
}

func synthesizeAfterInvalidActions(steps []Step) string {
	observations := recentUsefulObservations(steps, 3)
	if len(observations) == 0 {
		return invalidActionStopMessage(steps)
	}
	var b strings.Builder
	b.WriteString("The run stopped because the model repeatedly returned is_done=false without a usable action.tool after useful work was already completed. Here is the best available result from the successful steps:\n\n")
	for i, obs := range observations {
		if len(observations) > 1 {
			fmt.Fprintf(&b, "%d. %s\n", i+1, obs)
		} else {
			b.WriteString(obs)
		}
	}
	return b.String()
}

// looksPresentable reports whether an observation is human-readable prose worth
// surfacing as a degraded final answer. Raw structured tool output (a JSON
// object/array, e.g. a web_search results blob) and control payloads are NOT —
// dumping them verbatim gives the user `{"query":…,"results":[…]}` instead of an
// answer, which is worse than a clean "couldn't finish" message.
func looksPresentable(content string) bool {
	t := strings.TrimSpace(content)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		return false
	}
	if looksLikeMalformedControlPayload(t) {
		return false
	}
	if looksLikeSensitiveFile(t) {
		return false
	}
	return true
}

// looksLikeSensitiveFile flags raw file contents that must NEVER be surfaced as
// a reply — most importantly credential/cookie files a tool read as part of the
// run. Dumping the last observation as a degraded fallback once leaked a whole
// cookies.txt to the delivery channel; this stops that class of leak.
func looksLikeSensitiveFile(s string) bool {
	low := strings.ToLower(s)
	for _, marker := range []string{
		"netscape http cookie file",
		"cookie_spec.html",
		"-----begin ", // PEM private keys / certs
		"aws_secret_access_key",
		"api_key=", "apikey=", "secret=", "password=", "token=",
	} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	// A cookies.txt is tab-separated lines with a domain and a session value; the
	// leading comment banner above already catches the common generated form.
	return false
}

type observationDisplay struct {
	text   string
	failed bool
}

// displayObservation returns a user-facing observation snippet. Successful
// structured payloads are compacted instead of being discarded, so degraded
// ReAct replies can still show the useful facts gathered by tools.
func displayObservation(s Step) (observationDisplay, bool) {
	if s.Obs.Source == "controller" {
		return observationDisplay{}, false
	}
	if isInstructionalObservation(s) {
		return observationDisplay{}, false
	}
	content := strings.TrimSpace(s.Obs.Content)
	if content == "" && s.Obs.Error != nil {
		content = strings.TrimSpace(s.Obs.Error.Error())
	}
	if content == "" {
		return observationDisplay{}, false
	}
	failed := isToolFailure(s.Obs)
	if looksPresentable(content) {
		return observationDisplay{text: truncateForPrompt(content, 420), failed: failed}, true
	}
	if failed {
		return observationDisplay{}, false
	}
	if summary := compactStructuredObservation(content); summary != "" {
		return observationDisplay{text: truncateForPrompt(summary, 420), failed: false}, true
	}
	return observationDisplay{}, false
}

func isInstructionalObservation(s Step) bool {
	source := strings.ToLower(strings.TrimSpace(s.Obs.Source))
	tool := strings.ToLower(strings.TrimSpace(s.Action.Tool))
	if source == "read_skill" || source == "read_skill_file" || tool == "read_skill" || tool == "read_skill_file" {
		return true
	}
	content := strings.ToLower(strings.TrimSpace(s.Obs.Content))
	return strings.HasPrefix(content, "<skill_content") || strings.Contains(content, "<skill_resources>")
}

// lastPresentableObservation returns the most recent successful readable
// observation. Tool errors are only used when no successful evidence exists.
func lastPresentableObservation(steps []Step) string {
	if obs := lastObservationDisplay(steps, false); obs != "" {
		return obs
	}
	return lastObservationDisplay(steps, true)
}

func lastObservationDisplay(steps []Step, includeFailures bool) string {
	for i := len(steps) - 1; i >= 0; i-- {
		obs, ok := displayObservation(steps[i])
		if !ok {
			continue
		}
		if obs.failed && !includeFailures {
			continue
		}
		return obs.text
	}
	return ""
}

func recentUsefulObservations(steps []Step, max int) []string {
	if max <= 0 {
		return nil
	}
	if out := recentObservationDisplays(steps, max, false); len(out) > 0 {
		return out
	}
	return recentObservationDisplays(steps, max, true)
}

func recentObservationDisplays(steps []Step, max int, includeFailures bool) []string {
	reversed := make([]string, 0, max)
	for i := len(steps) - 1; i >= 0 && len(reversed) < max; i-- {
		obs, ok := displayObservation(steps[i])
		if !ok {
			continue
		}
		if obs.failed && !includeFailures {
			continue
		}
		reversed = append(reversed, obs.text)
	}
	out := make([]string, len(reversed))
	for i := range reversed {
		out[len(reversed)-1-i] = reversed[i]
	}
	return out
}

func compactStructuredObservation(content string) string {
	var decoded any
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &decoded); err != nil {
		return ""
	}
	return summarizeJSONValue(decoded)
}

func summarizeJSONValue(v any) string {
	switch typed := v.(type) {
	case map[string]any:
		return summarizeJSONObject(typed)
	case []any:
		if len(typed) == 0 {
			return "Tool returned an empty list."
		}
		parts := []string{fmt.Sprintf("Tool returned %d item(s).", len(typed))}
		for i, item := range typed {
			if i >= 2 {
				break
			}
			if summary := summarizeJSONValue(item); summary != "" {
				parts = append(parts, summary)
			}
		}
		return strings.Join(parts, " ")
	default:
		if scalar, ok := scalarString(typed); ok {
			return scalar
		}
	}
	return ""
}

func summarizeJSONObject(obj map[string]any) string {
	if output, ok := stringField(obj, "output"); ok && looksPresentable(output) {
		return output
	}
	if results, ok := obj["results"].([]any); ok {
		return summarizeSearchResults(obj, results)
	}
	if summary := summarizeFinanceObject(obj); summary != "" {
		return summary
	}

	keys := []string{
		"title", "name", "symbol", "ticker", "query", "status", "ok", "count",
		"source", "url", "summary", "description", "reason", "message",
	}
	parts := make([]string, 0, 8)
	seen := map[string]bool{}
	for _, key := range keys {
		if value, ok := obj[key]; ok {
			if scalar, ok := scalarString(value); ok {
				parts = append(parts, fmt.Sprintf("%s: %s", key, scalar))
				seen[key] = true
			}
		}
	}
	for key, value := range obj {
		if len(parts) >= 8 {
			break
		}
		if seen[key] || isNoisyJSONKey(key) {
			continue
		}
		if scalar, ok := scalarString(value); ok {
			parts = append(parts, fmt.Sprintf("%s: %s", key, scalar))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ")
}

func summarizeSearchResults(obj map[string]any, results []any) string {
	query, _ := stringField(obj, "query")
	count := len(results)
	if n, ok := scalarString(obj["result_count"]); ok {
		query = strings.TrimSpace(query)
		prefix := fmt.Sprintf("Search returned %s result(s)", n)
		if query != "" {
			prefix += " for " + query
		}
		return prefix + summarizeResultTitles(results)
	}
	prefix := fmt.Sprintf("Search returned %d result(s)", count)
	if strings.TrimSpace(query) != "" {
		prefix += " for " + query
	}
	return prefix + summarizeResultTitles(results)
}

func summarizeResultTitles(results []any) string {
	titles := make([]string, 0, 2)
	for _, item := range results {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		title, ok := firstStringField(obj, "title", "name")
		if !ok {
			continue
		}
		if url, ok := stringField(obj, "url"); ok && url != "" {
			title += " (" + url + ")"
		}
		titles = append(titles, title)
		if len(titles) == 2 {
			break
		}
	}
	if len(titles) == 0 {
		return "."
	}
	return ": " + strings.Join(titles, "; ") + "."
}

func summarizeFinanceObject(obj map[string]any) string {
	symbol, ok := firstStringField(obj, "symbol", "ticker")
	if !ok {
		return ""
	}
	name, _ := firstStringField(obj, "longName", "shortName", "name")
	header := symbol
	if name != "" && name != symbol {
		header += " (" + name + ")"
	}
	fields := []string{"currentPrice", "regularMarketPrice", "marketCap", "sector", "industry", "forwardPE", "trailingPE", "targetMeanPrice", "recommendationKey", "fiftyTwoWeekRange"}
	parts := []string{header}
	for _, key := range fields {
		if value, ok := obj[key]; ok {
			if scalar, ok := scalarString(value); ok {
				parts = append(parts, fmt.Sprintf("%s: %s", key, scalar))
			}
		}
	}
	if len(parts) == 1 {
		return ""
	}
	return strings.Join(parts, "; ")
}

func firstStringField(obj map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := stringField(obj, key); ok {
			return value, true
		}
	}
	return "", false
}

func stringField(obj map[string]any, key string) (string, bool) {
	value, ok := obj[key]
	if !ok {
		return "", false
	}
	return scalarString(value)
}

func scalarString(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "", false
	case string:
		t := strings.TrimSpace(typed)
		if t == "" {
			return "", false
		}
		return truncateForPrompt(t, 140), true
	case float64:
		return fmt.Sprintf("%v", typed), true
	case bool:
		return fmt.Sprintf("%t", typed), true
	default:
		return "", false
	}
}

func isNoisyJSONKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "longbusinesssummary", "companyofficers", "calendar", "earnings",
		"financialdata", "summarydetail", "defaultkeystatistics", "price",
		"recommendationtrend", "upgradedowngradehistory":
		return true
	default:
		return false
	}
}

func reflectAfterRepeatedThinkErrors(ctx context.Context, env Env, taskInput string, steps []Step) (ReflectResponse, bool) {
	resp, err := env.LLM.Reflect(ctx, ReflectRequest{
		TaskInput:    taskInput,
		Steps:        steps,
		SystemPrompt: env.Config.SystemPrompt,
		OutputFormat: env.Config.OutputFormat,
	})
	if err != nil || strings.TrimSpace(resp.Output) == "" {
		return ReflectResponse{}, false
	}
	if isPrematureFinalAnswer(resp.Output) {
		return ReflectResponse{}, false
	}
	return resp, true
}

func lastUsefulObservation(steps []Step) string {
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		if s.Obs.Source == "controller" {
			continue
		}
		if strings.TrimSpace(s.Obs.Content) != "" {
			return truncateForPrompt(strings.TrimSpace(s.Obs.Content), 420)
		}
		if s.Obs.Error != nil {
			return truncateForPrompt(s.Obs.Error.Error(), 420)
		}
	}
	return ""
}

func truncateForPrompt(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return s[:max-1] + "…"
}

func recoverTextualToolCall(text string, toolNames []string) (ToolCall, bool) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	if call, ok := recoverXMLTextualToolCall(s, toolNames); ok {
		return call, true
	}
	if call, ok := recoverJSONToolCall(s, toolNames); ok {
		return call, true
	}
	if call, ok := recoverActionInputToolCall(s, toolNames); ok {
		return call, true
	}
	if idx := strings.LastIndex(strings.ToLower(s), "action:"); idx >= 0 {
		s = strings.TrimSpace(s[idx+len("action:"):])
	}
	open := strings.Index(s, "(")
	close := strings.LastIndex(s, ")")
	if open <= 0 || close != len(s)-1 {
		return ToolCall{}, false
	}
	name := strings.TrimSpace(s[:open])
	canonicalName, ok := canonicalAllowedTool(name, toolNames)
	if !ok {
		return ToolCall{}, false
	}
	name = canonicalName
	rawArgs := strings.TrimSpace(s[open+1 : close])
	if rawArgs == "" {
		rawArgs = "{}"
	}
	var args map[string]any
	if parsed, ok := parseActionArgs(rawArgs); ok {
		args = parsed
	} else if strings.HasPrefix(rawArgs, "map[") && strings.HasSuffix(rawArgs, "]") {
		var ok bool
		args, ok = parseLegacyMapArgs(rawArgs)
		if !ok {
			return ToolCall{}, false
		}
	} else if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return ToolCall{}, false
	}
	input := make(map[string]string, len(args))
	for k, v := range args {
		switch t := v.(type) {
		case string:
			input[k] = t
		case nil:
			input[k] = ""
		case bool, float64:
			input[k] = fmt.Sprint(t)
		default:
			b, err := json.Marshal(t)
			if err != nil {
				return ToolCall{}, false
			}
			input[k] = string(b)
		}
	}
	return ToolCall{Tool: name, Input: input, Arguments: args}, true
}

type textualXMLToolNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr           `xml:",any,attr"`
	Text     string               `xml:",chardata"`
	Children []textualXMLToolNode `xml:",any"`
}

// recoverXMLTextualToolCall accepts the compact function syntax emitted by
// several open-weight reasoning models. The complete response must be exactly
// one call to an offered tool; attributes, prose and nested markup are rejected
// so an example in ordinary model text cannot become an executable action.
func recoverXMLTextualToolCall(s string, toolNames []string) (ToolCall, bool) {
	if !strings.HasPrefix(strings.TrimSpace(s), "<") {
		return ToolCall{}, false
	}
	var root textualXMLToolNode
	if err := xml.Unmarshal([]byte(s), &root); err != nil || len(root.Attrs) != 0 ||
		strings.TrimSpace(root.Text) != "" || len(root.Children) == 0 {
		return ToolCall{}, false
	}
	name, ok := canonicalAllowedTool(root.XMLName.Local, toolNames)
	if !ok {
		return ToolCall{}, false
	}
	args := make(map[string]any, len(root.Children))
	for _, child := range root.Children {
		if child.XMLName.Local == "" || len(child.Attrs) != 0 || len(child.Children) != 0 {
			return ToolCall{}, false
		}
		key, value := child.XMLName.Local, strings.TrimSpace(child.Text)
		if (key == "arguments" || key == "parameters") && strings.HasPrefix(value, "{") {
			var decoded map[string]any
			if json.Unmarshal([]byte(value), &decoded) != nil {
				return ToolCall{}, false
			}
			for k, v := range decoded {
				args[k] = v
			}
			continue
		}
		if _, duplicate := args[key]; duplicate {
			return ToolCall{}, false
		}
		args[key] = value
	}
	call := ToolCall{Tool: name, Arguments: args}
	normalizeToolCallArgs(&call)
	return call, true
}

func recoverJSONToolCall(s string, toolNames []string) (ToolCall, bool) {
	var direct ToolCall
	if err := json.Unmarshal([]byte(s), &direct); err == nil && direct.Tool != "" {
		canonicalName, ok := canonicalAllowedTool(direct.Tool, toolNames)
		if !ok {
			return ToolCall{}, false
		}
		direct.Tool = canonicalName
		normalizeToolCallArgs(&direct)
		return direct, true
	}

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return ToolCall{}, false
	}
	raw := s[start : end+1]

	var wrapped struct {
		Tool        string         `json:"tool"`
		Name        string         `json:"name"`
		Action      any            `json:"action"`
		Input       map[string]any `json:"input"`
		Arguments   map[string]any `json:"arguments"`
		ActionInput map[string]any `json:"action_input"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil {
		return ToolCall{}, false
	}

	name := firstNonEmpty(wrapped.Tool, wrapped.Name)
	args := firstNonNilMap(wrapped.Arguments, wrapped.Input, wrapped.ActionInput)

	switch a := wrapped.Action.(type) {
	case string:
		if name == "" {
			name = a
		}
	case map[string]any:
		if name == "" {
			name = firstString(a["tool"], a["name"])
		}
		if len(args) == 0 {
			args = firstMap(a["arguments"], a["input"], a["action_input"])
		}
	}

	canonicalName, ok := canonicalAllowedTool(name, toolNames)
	if name == "" || !ok {
		return ToolCall{}, false
	}
	call := ToolCall{Tool: canonicalName, Arguments: args}
	normalizeToolCallArgs(&call)
	return call, true
}

func recoverActionInputToolCall(s string, toolNames []string) (ToolCall, bool) {
	lines := strings.Split(s, "\n")
	name := ""
	rawInput := ""
	for _, line := range lines {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "action", "tool":
			if name == "" {
				name = strings.TrimSpace(value)
			}
		case "action input", "input", "arguments":
			if rawInput == "" {
				rawInput = strings.TrimSpace(value)
			}
		}
	}
	canonicalName, ok := canonicalAllowedTool(name, toolNames)
	if name == "" || !ok {
		return ToolCall{}, false
	}
	args := map[string]any{}
	if rawInput != "" {
		parsed, ok := parseActionArgs(rawInput)
		if !ok {
			return ToolCall{}, false
		}
		args = parsed
	}
	call := ToolCall{Tool: canonicalName, Arguments: args}
	normalizeToolCallArgs(&call)
	return call, true
}

func parseActionArgs(raw string) (map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, true
	}
	if strings.HasPrefix(raw, "input=") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "input="))
		var args map[string]any
		if err := json.Unmarshal([]byte(raw), &args); err == nil {
			return args, true
		}
		return nil, false
	}
	if strings.HasPrefix(raw, "map[") && strings.HasSuffix(raw, "]") {
		return parseLegacyMapArgs(raw)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err == nil {
		return args, true
	}
	return nil, false
}

func normalizeToolCallArgs(call *ToolCall) {
	call.Tool = canonicalToolAlias(call.Tool)
	if call.Arguments == nil {
		call.Arguments = map[string]any{}
	}
	if len(call.Arguments) == 0 && len(call.Input) > 0 {
		call.Arguments = make(map[string]any, len(call.Input))
		for k, v := range call.Input {
			call.Arguments[k] = v
		}
	}
	call.Input = make(map[string]string, len(call.Arguments))
	for k, v := range call.Arguments {
		switch t := v.(type) {
		case string:
			call.Input[k] = t
		case nil:
			call.Input[k] = ""
		case bool, float64:
			call.Input[k] = fmt.Sprint(t)
		default:
			b, err := json.Marshal(t)
			if err == nil {
				call.Input[k] = string(b)
			}
		}
	}
}

func firstString(values ...any) string {
	for _, v := range values {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstMap(values ...any) map[string]any {
	for _, v := range values {
		if m, ok := v.(map[string]any); ok && len(m) > 0 {
			return m
		}
	}
	return nil
}

func firstNonNilMap(values ...map[string]any) map[string]any {
	for _, v := range values {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

// truncateAtFabricatedObservation cuts a model completion at the first
// "Observation:" it writes, because an observation is not the model's to write.
//
// The Think prompt renders history as Thought/Action/Observation triplets
// (formatStepHistory), which is the shape the model is being asked to continue.
// Nothing stopped it continuing PAST its own action: with no stop sequence, a
// model will happily author the observation it hopes to receive, then a further
// thought, action and observation, inventing an entire trajectory in one turn.
//
// Seen live on the Portfolio Replacement Strategist. Its narration contained a
// market overview keyed SPX/NDX/DJI/VIX with sector_performance and top_movers
// blocks, and a correlation payload with diversification_score, sector_breakdown
// and prose notes. The real tools return neither shape: the overview is keyed
// "^GSPC"/"^DJI" with name/symbol fields and no envelope, and the correlation
// tool returns a bare "matrix" of full-precision floats — 0.6453931054478129,
// not the tidy 0.72 in the narration. The numbers were invented too: ^GSPC was
// 7753.11 that day, the narration said SPX 6120.46.
//
// This is the worst failure mode available to a financial agent, because the
// invention is indistinguishable from data: fabricated observations enter the
// step history, become the prompt for the next turn, and the conclusions get
// built on them and presented with the same confidence as real figures.
//
// Truncating leaves exactly one proposed action, which the runtime then executes
// for real. The next turn continues from the observation that actually came
// back, which is the whole point of the loop.
func truncateAtFabricatedObservation(raw string) string {
	lower := strings.ToLower(raw)
	idx := strings.Index(lower, "observation:")
	if idx < 0 {
		return raw
	}
	// Only cut at a line-leading marker; "the observation: ..." inside a
	// sentence is prose about an observation, not a forged one.
	lineStart := strings.LastIndexByte(raw[:idx], '\n') + 1
	if strings.TrimSpace(raw[lineStart:idx]) != "" {
		return raw
	}
	return strings.TrimSpace(raw[:lineStart])
}

func recoverThinkResponseFromRaw(raw string, toolNames []string) (ThinkResponse, bool) {
	// Only the runtime may write an Observation. See truncateAtFabricatedObservation.
	raw = truncateAtFabricatedObservation(raw)
	call, ok := recoverTextualToolCall(raw, toolNames)
	if !ok {
		answer := strings.TrimSpace(stripMarkdownFence(raw))
		if answer == "" || looksLikeMalformedControlPayload(answer) || isPrematureFinalAnswer(answer) || !looksLikeSubstantivePlainAnswer(answer) {
			return ThinkResponse{}, false
		}
		return ThinkResponse{
			Thought:     "Recovered plain-text final answer.",
			IsDone:      true,
			FinalAnswer: answer,
		}, true
	}
	thought := strings.TrimSpace(raw)
	if idx := strings.LastIndex(strings.ToLower(thought), "action:"); idx >= 0 {
		thought = strings.TrimSpace(thought[:idx])
	}
	thought = strings.TrimPrefix(thought, "Thought:")
	thought = strings.TrimSpace(thought)
	if thought == "" {
		thought = "Recovered legacy ReAct tool action."
	}
	return ThinkResponse{
		Thought: thought,
		IsDone:  false,
		Action:  call,
	}, true
}

func stripMarkdownFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if j := strings.LastIndex(s, "```"); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s)
}

func looksLikeMalformedControlPayload(s string) bool {
	trimmed := strings.TrimSpace(s)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		return true
	}
	for _, marker := range []string{"\"thought\"", "\"is_done\"", "\"action\"", "tool_name", "action input:", "action:"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeSubstantivePlainAnswer(s string) bool {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) < 24 {
		return false
	}
	if strings.Contains(trimmed, "\n") || strings.Contains(trimmed, "|") {
		return true
	}
	if strings.Contains(trimmed, ".") || strings.Contains(trimmed, "!") || strings.Contains(trimmed, "?") || strings.Contains(trimmed, ":") {
		return true
	}
	return false
}

func parseLegacyMapArgs(raw string) (map[string]any, bool) {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "map["), "]"))
	if body == "" {
		return map[string]any{}, true
	}

	// Common model fallback form for a single free-form argument:
	//   python_eval(map[code:import os
	//   print("hi")])
	// strings.Fields would destroy the code. Preserve everything after code:.
	if strings.HasPrefix(body, "code:") {
		return map[string]any{"code": strings.TrimSpace(strings.TrimPrefix(body, "code:"))}, true
	}

	// Go's fmt prints maps as map[k:v]. Some models copy that shape and values
	// may contain spaces, especially for file writes. Split only on known key
	// labels so values can remain free-form.
	if parsed, ok := parseKnownLegacyMap(body, []string{
		"path", "content", "queue", "item", "ttl", "to", "channel", "text", "message",
		"query", "kb", "title", "summary", "topic_tag", "source_url", "file_path",
		"source_channel", "timestamp", "status",
	}); ok {
		return parsed, true
	}

	args := map[string]any{}
	fields := strings.Fields(body)
	for _, f := range fields {
		k, v, ok := strings.Cut(f, ":")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, false
		}
		args[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return args, true
}

func parseKnownLegacyMap(body string, keys []string) (map[string]any, bool) {
	type hit struct {
		key string
		at  int
	}
	var hits []hit
	for _, key := range keys {
		prefix := key + ":"
		searchFrom := 0
		for {
			idx := strings.Index(body[searchFrom:], prefix)
			if idx < 0 {
				break
			}
			at := searchFrom + idx
			if at == 0 || body[at-1] == ' ' || body[at-1] == '\n' || body[at-1] == '\t' {
				hits = append(hits, hit{key: key, at: at})
			}
			searchFrom = at + len(prefix)
		}
	}
	if len(hits) == 0 {
		return nil, false
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].at < hits[j-1].at; j-- {
			hits[j], hits[j-1] = hits[j-1], hits[j]
		}
	}
	args := map[string]any{}
	for i, h := range hits {
		start := h.at + len(h.key) + 1
		end := len(body)
		if i+1 < len(hits) {
			end = hits[i+1].at
		}
		value := strings.TrimSpace(body[start:end])
		if value == "" {
			continue
		}
		if (strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}")) ||
			(strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]")) {
			var decoded any
			if err := json.Unmarshal([]byte(value), &decoded); err == nil {
				args[h.key] = decoded
				continue
			}
		}
		args[h.key] = value
	}
	return args, len(args) > 0
}

func toolAllowed(name string, toolNames []string) bool {
	_, ok := canonicalAllowedTool(name, toolNames)
	return ok
}

func canonicalAllowedTool(name string, toolNames []string) (string, bool) {
	candidate := canonicalToolAlias(name)
	if len(toolNames) == 0 {
		return candidate, strings.TrimSpace(candidate) != ""
	}
	for _, n := range toolNames {
		canonicalName := canonicalToolAlias(n)
		if canonicalName == candidate || strings.TrimSpace(n) == strings.TrimSpace(name) {
			return canonicalName, true
		}
	}
	return resolveBareMCPTool(candidate, toolNames)
}

// resolveBareMCPTool matches a tool named WITHOUT its mcp__<server>__ prefix
// against the one available tool that carries it.
//
// A model handed forty tools called mcp__maverick-mcp__market_data_get_quote
// will sometimes write the readable half — market_data_get_quote — in a plan
// step. Until now that made the tool "unavailable", and for plan_execute the
// consequence was total: planUnavailableTool rejects the WHOLE plan on one
// unrecognised name, so the run silently downgraded to ReAct and lost the very
// things plan_execute is chosen for — the upfront plan, dependency gating,
// parallel levels. What ran instead was greedy one-tool-at-a-time execution
// that spent its whole step budget fetching and never reached an answer.
//
// Only an UNAMBIGUOUS match counts. If two servers both expose a "get_quote",
// picking one would be guessing which server the author meant, and calling the
// wrong server's tool is worse than reporting the name as unavailable.
func resolveBareMCPTool(candidate string, toolNames []string) (string, bool) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || strings.HasPrefix(candidate, "mcp__") {
		return "", false
	}
	suffix := "__" + candidate
	match := ""
	for _, n := range toolNames {
		n = strings.TrimSpace(n)
		if !strings.HasPrefix(n, "mcp__") || !strings.HasSuffix(n, suffix) {
			continue
		}
		if match != "" && match != n {
			return "", false // two servers expose it; the author must say which
		}
		match = n
	}
	if match == "" {
		return "", false
	}
	return match, true
}

func canonicalToolAlias(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"agent:", "tool:", "function:", "functions."} {
		if strings.HasPrefix(name, prefix) {
			name = strings.TrimSpace(strings.TrimPrefix(name, prefix))
			break
		}
	}
	switch strings.ToLower(name) {
	case "google:search", "google_search", "browser.search", "browser_search", "search", "web.search", "web-search", "search_web", "websearch", "internet_search", "internet.search":
		return "web_search"
	case "send_message", "send.notification", "send_notification", "notify", "notification.send", "channel_send", "channel-send", "send_channel", "send.channel", "telegram_send", "telegram.send", "slack_send", "slack.send", "discord_send", "discord.send":
		return "channel.send"
	case "read_url", "open_url", "get_url", "url_fetch", "fetch-url", "http_get", "http.get":
		return "fetch_url"
	case "search_kb", "kb.search", "knowledge_search", "knowledge.search", "rag_search", "rag.search":
		return "kb_search"
	case "write_kb", "kb.write", "kb_add", "kb.add", "kb_store", "kb.store", "store_kb", "knowledge_write", "knowledge.write", "knowledge_store", "knowledge.store":
		return "kb_write"
	case "queue.add", "queue_add", "queue.push", "queue_push", "enqueue", "queue.enqueue":
		return "queue_put"
	case "queue.read", "queue_read", "queue.items", "queue_items", "queue.peek", "queue_peek", "peek_queue":
		return "queue_list"
	case "dequeue", "queue.pop", "queue_pop":
		return "queue_take"
	case "queue.create", "queue-create":
		return "queue_create"
	case "queue.clear", "queue-clear":
		return "queue_clear"
	default:
		return name
	}
}

// IsProgressPreamble reports whether text is a "I'll start by…" / "let me…"
// progress note rather than a completed answer. Exported so the classic tool
// loop (internal/runtime) can reject such a preamble as a final reply and force
// a real synthesis, the same way the reasoning loop already does.
func IsProgressPreamble(text string) bool { return isPrematureFinalAnswer(text) }

// IsInternalScratchNarration reports whether a purported final answer is the
// model narrating its private work rather than addressing the user. Some
// OpenAI-compatible reasoning models put their scratchpad in the ordinary
// content field (instead of reasoning_content). A long scratchpad used to pass
// the progress-preamble guard and could expose raw tool payloads in Chat.
//
// Keep this deliberately conservative: require both an explicit reference to
// the user's request and first-person planning language. Ordinary reports may
// say "the user" or "we need" independently, but rarely combine both while
// describing what answer still needs to be produced.
func IsInternalScratchNarration(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	if s == "" {
		return false
	}
	// The evidence is normally in the opening workpad. Bounding the scan also
	// prevents a legitimate long report from being rejected because it quotes
	// planning language much later in the answer.
	if len(s) > 1800 {
		s = s[:1800]
	}
	userReference := containsAny(s,
		"the user wants", "the user asked", "the user's request",
		"user wants", "user asked", "user query", "user's query")
	planning := containsAny(s,
		"we need to", "i need to", "need to provide", "need to answer",
		"let's examine", "let us examine", "let's inspect", "let us inspect",
		"now synthesize", "need to synthesize")
	return userReference && planning
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func isPrematureFinalAnswer(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	if s == "" {
		return false
	}
	// A long progress note is still a progress note. The length/paragraph
	// exemption below exists to protect real reports, but a model narrating the
	// RUN ("Looking at the trace, I need to continue from where the pipeline
	// broke — fetch the article, then proceed…") can trivially exceed 800 chars
	// and sail through it, landing meta-commentary in the user's inbox as if it
	// were the deliverable. Judge the OPENING sentence, which a real report and
	// a narration never share, before applying any length exemption.
	if hasMetaNarrationOpener(firstSentence(s)) {
		return true
	}
	// A substantial, multi-paragraph answer is a real deliverable — a progress
	// note ("I'll read the file, then search…") is a short single block. Don't
	// misflag a real report (e.g. a podcast briefing) just because its prose
	// happens to contain "I will" or "let me" somewhere, which would wrongly
	// discard it and degrade the run.
	if strings.Contains(s, "\n\n") || len(s) > 800 {
		return false
	}
	// Explicit progress phrases — flagged anywhere in the text.
	for _, phrase := range []string{
		"proceeding to step",
		"proceeding to list",
		"proceeding to process",
		"proceeding to check",
		"starting daily processing",
		"checking for pending",
		"pending resources queue exists",
		"i need to ",
		"let me ",
		"i will ",
		"next i ",
		"now i ",
		"about to ",
	} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	// Progress/intent OPENERS: a preamble *begins* by announcing what it is
	// about to do ("I'll scout the routes…", "Searching for fares…"). Anchored
	// to the start and gated to a short, single-block note so a real, structured
	// report that merely mentions these words later is never misflagged. This
	// catches contractions and gerund openers the phrase list above misses.
	if len(s) <= 400 && !strings.Contains(s, "\n") {
		for _, opener := range []string{
			// future-intent openers (contractions the phrase list above misses)
			"i'll ", "we'll ", "i'm going to ", "i am going to ", "i'm about to ",
			"i plan to ", "i shall ", "let's ", "let me now ", "first, i", "first i ",
			"next, i", "now i'll", "i'm now ", "going to ",
			// intent gerunds — anchored AND followed by a target preposition so a
			// results statement ("Searching returned 20 results…") is not flagged.
			"searching for ", "looking for ", "scouting for ", "scouting the ",
			"fetching the ", "retrieving the ", "gathering the ", "compiling the ",
		} {
			if strings.HasPrefix(s, opener) {
				return true
			}
		}
	}
	return false
}

// firstSentence returns the lower-cased opening sentence of s, bounded so a
// wall of prose without punctuation can't turn the opener check into a
// whole-document scan. Terminated by sentence punctuation, a newline, or the
// bound — whichever comes first.
func firstSentence(s string) string {
	const maxFirstSentence = 240
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > maxFirstSentence {
		s = s[:maxFirstSentence]
	}
	if i := strings.IndexAny(s, ".!?\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// hasMetaNarrationOpener reports whether an opening sentence is the model
// narrating the run rather than answering the task. Two shapes qualify:
//
//   - Trace/run narration ("Looking at the trace…", "Picking up where…") —
//     always meta, because a finished deliverable never opens by describing
//     the machinery that produced it.
//   - A first-person intent opener ("I need to…", "Let me…") that ALSO
//     announces work still to come ("continue", "then", "fetch"). The second
//     clause is required so a real answer opening "I'll note one caveat…"
//     stays safe.
//
// Deliberately narrow: this runs BEFORE the length exemption, so a false
// positive here discards a genuine report.
func hasMetaNarrationOpener(first string) bool {
	if first == "" {
		return false
	}
	for _, opener := range []string{
		"looking at the trace", "looking at the run", "looking at the log",
		"reviewing the trace", "based on the trace", "from the trace",
		"continuing from", "picking up where", "picking up from", "resuming from",
		"as shown in the trace", "the trace shows",
	} {
		if strings.HasPrefix(first, opener) {
			return true
		}
	}
	intent := false
	for _, opener := range []string{
		"i need to ", "i'll ", "i will ", "let me ", "i'm going to ",
		"i am going to ", "we'll ", "we need to ", "next i ", "now i ",
	} {
		if strings.HasPrefix(first, opener) {
			intent = true
			break
		}
	}
	if !intent {
		return false
	}
	for _, pending := range []string{
		"continue", "proceed", "resume", "next", "then ", "fetch", "search",
		"start", "gather", "retrieve", "pick up", "finish the", "complete the",
	} {
		if strings.Contains(first, pending) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ─── Plan-Execute ─────────────────────────────────────────────────────────────

// planExecuteStrategy decomposes the task with one Plan() call, executes the
// planned steps in order with dependency gating, then reflects. Planning
// failure falls back to ReAct.
type planExecuteStrategy struct{}

var planStepPlaceholderRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.:-]+)\.(?:output|result|content|observation)\s*\}\}`)

func (planExecuteStrategy) Run(ctx context.Context, env Env, taskInput string) ([]Step, ReflectResponse) {
	plan, err := env.LLM.Plan(ctx, planSystemPrompt(env.Config.SystemPrompt, env.Config.ToolNames), taskInput, env.Config.MaxPlanSteps)
	badTool, hasBadTool := planUnavailableTool(plan, env.Config.ToolNames)
	if err != nil || hasBadTool {
		// Planning failed — fall back to ReAct. This downgrade used to be
		// completely silent: the run banner still said "plan_execute" while the
		// loop was actually running ReAct, so every guarantee plan_execute is
		// chosen FOR (an upfront plan, dependency gating, parallel levels) was
		// quietly absent with nothing in the trace to say so. Lead the trace with
		// a step naming the cause instead.
		reason := planDowngradeReason(err, badTool)
		reactSteps, resp := reactStrategy{}.Run(ctx, env, taskInput)
		downgrade := Step{
			ID:      "plan-downgrade",
			Thought: "Plan-Execute downgraded to ReAct before any step ran.",
			Obs: Observation{
				Content: "planner: " + reason + " — this run executed with the ReAct strategy, not plan_execute.",
				// Source is "planner", NOT "controller": this is a visibility
				// record, and controller-sourced steps flip the run's Confident
				// flag. A ReAct fallback still routinely produces a good answer,
				// so it must not on its own mark the whole run degraded.
				Source: "planner",
			},
		}
		return append([]Step{downgrade}, reactSteps...), resp
	}
	plan = normalizePlanToolAliases(plan)

	completedIDs := map[string]bool{}
	var steps []Step

	// Independent steps run concurrently. The plan already carries the
	// dependency DAG (PlannedStep.DependsOn, plus any {{id.output}} placeholder
	// references), and it was previously used ONLY as a skip gate while the
	// walk stayed strictly single-file — so three provably independent searches
	// executed one at a time for no reason. Grouping into dependency levels
	// keeps ordering deterministic (results are appended in plan order) while
	// letting each level's steps overlap.
	for _, level := range planDependencyLevels(plan) {
		if ctx.Err() != nil {
			break
		}
		// Snapshot the steps completed by EARLIER levels. Same-level steps are
		// independent by construction, so none of them can reference another's
		// output — this is the exact input the serial walk would have supplied.
		prior := append([]Step(nil), steps...)

		runnable := make([]PlannedStep, 0, len(level))
		for _, ps := range level {
			// Dependency ordering: skip steps whose dependencies haven't
			// completed. An unmet dependency means an upstream step failed —
			// we skip the dependent step gracefully.
			if dep, ok := firstUnmetDependency(ps, completedIDs); !ok {
				steps = append(steps, Step{
					ID:      ps.ID,
					Thought: ps.Description,
					Obs: Observation{
						Content: fmt.Sprintf("skipped: dependency %v not completed (%q)", ps.DependsOn, dep),
					},
				})
				continue
			}
			runnable = append(runnable, ps)
		}
		if len(runnable) == 0 {
			continue
		}

		results := make([]Step, len(runnable))
		execute := func(i int, ps PlannedStep) {
			stepCtx, cancel := context.WithTimeout(ctx, env.Config.StepTimeout)
			defer cancel()
			stepStart := time.Now()
			call := plannedStepToolCall(ps, prior)
			obs := boundObservation(env.Tools.Execute(stepCtx, call))
			results[i] = Step{
				ID:       ps.ID,
				Thought:  ps.Description,
				Action:   call,
				Obs:      obs,
				Duration: time.Since(stepStart),
			}
		}

		if parallelism := planParallelism(env.Config); len(runnable) == 1 || parallelism <= 1 {
			// Preserve the serial path exactly for a single-step level, and for
			// an operator who pinned max_parallel_steps: 1 because their tool
			// executor is not safe for concurrent calls.
			for i, ps := range runnable {
				execute(i, ps)
			}
		} else {
			var wg sync.WaitGroup
			slots := make(chan struct{}, parallelism)
			for i, ps := range runnable {
				wg.Add(1)
				go func(i int, ps PlannedStep) {
					defer wg.Done()
					slots <- struct{}{}
					defer func() { <-slots }()
					// A panicking tool must not take down the whole run — the
					// serial loop was protected by the caller's recover, and a
					// goroutine is not.
					defer func() {
						if r := recover(); r != nil {
							results[i] = Step{
								ID:      ps.ID,
								Thought: ps.Description,
								Obs: Observation{
									Content: fmt.Sprintf("tool error: planned step panicked: %v", r),
									Error:   fmt.Errorf("planned step %q panicked: %v", ps.ID, r),
								},
							}
						}
					}()
					execute(i, ps)
				}(i, ps)
			}
			wg.Wait()
		}

		// Append in plan order regardless of completion order, so the trace and
		// any downstream placeholder resolution stay deterministic.
		for _, st := range results {
			steps = append(steps, st)
			if !isToolFailure(st.Obs) {
				completedIDs[st.ID] = true
			}
		}
	}

	resp, _ := env.LLM.Reflect(ctx, ReflectRequest{
		TaskInput:    taskInput,
		Steps:        steps,
		SystemPrompt: env.Config.SystemPrompt,
		OutputFormat: env.Config.OutputFormat,
	})
	if isPrematureFinalAnswer(resp.Output) && planExecutionHadIssue(steps) {
		steps = append(steps, planExecuteControllerStep("reflect output was a progress note after a failed or skipped plan step"))
		resp.Output = planExecuteFallbackOutput(steps)
	} else if looksLikePendingAsyncPayload(stripJSONFence(strings.TrimSpace(resp.Output))) {
		steps = append(steps, planExecuteControllerStep("reflect output was an async status payload, not a completed final deliverable"))
		resp.Output = planExecuteAsyncPendingOutput(steps)
	} else if strings.TrimSpace(resp.Output) == "" || isPrematureFinalAnswer(resp.Output) {
		steps = append(steps, planExecuteControllerStep("reflect did not produce a completed user-facing answer"))
		resp.Output = planExecuteFallbackOutput(steps)
	}
	return steps, resp
}

func planExecuteControllerStep(reason string) Step {
	return Step{
		ID:      "controller-finalization",
		Thought: "Plan-Execute could not produce a reliable final deliverable.",
		Obs: Observation{
			Content: "controller: " + reason,
			Source:  "controller",
		},
	}
}

func planExecuteFallbackOutput(steps []Step) string {
	if len(steps) == 0 {
		return "The run could not produce a plan to execute. Retry with a clearer request or choose a ReAct agent for open-ended tool use."
	}
	if planExecutionHasPendingAsyncPayload(steps) {
		return planExecuteAsyncPendingOutput(steps)
	}
	failed := 0
	skipped := 0
	for _, step := range steps {
		if isToolFailure(step.Obs) {
			failed++
			continue
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(step.Obs.Content)), "skipped:") {
			skipped++
		}
	}
	if failed > 0 || skipped > 0 {
		return fmt.Sprintf("The plan could not fully complete: %d step(s) failed and %d dependent step(s) were skipped. Open the run trace to inspect the failed step, then fix the tool input, credential, or workflow dependency.", failed, skipped)
	}
	completed := 0
	for _, step := range steps {
		if step.Obs.Source == "controller" || strings.TrimSpace(step.Action.Tool) == "" {
			continue
		}
		completed++
	}
	if completed > 0 {
		return fmt.Sprintf("The workflow made progress (%d tool step(s) completed), but it did not produce the required final deliverable. I did not publish raw tool output as the final answer. Open the run trace to inspect the completed steps, then rerun after fixing the model/step output.", completed)
	}
	return "The plan executed but the model did not produce a final answer. Open the run trace to inspect the completed steps."
}

func planExecuteAsyncPendingOutput(steps []Step) string {
	return asyncIncompleteFallback(steps)
}

func planExecutionHasPendingAsyncPayload(steps []Step) bool {
	for i := len(steps) - 1; i >= 0; i-- {
		if looksLikePendingAsyncPayload(steps[i].Obs.Content) {
			return true
		}
	}
	return false
}

func planSystemPrompt(systemPrompt string, toolNames []string) string {
	system := strings.TrimSpace(systemPrompt)
	if len(toolNames) == 0 {
		return system
	}
	toolList := "Available tools: " + strings.Join(toolNames, ", ")
	if system == "" {
		return toolList
	}
	return system + "\n\n" + toolList
}

func plannedStepToolCall(ps PlannedStep, prior []Step) ToolCall {
	if len(ps.Arguments) > 0 {
		args := resolvePlanArgumentPlaceholders(ps.Arguments, prior)
		input := stringifyArgs(args)
		return ToolCall{Tool: ps.Tool, Input: input, Arguments: args}
	}
	if len(ps.Input) > 0 {
		args := make(map[string]any, len(ps.Input))
		for k, v := range ps.Input {
			args[k] = v
		}
		args = resolvePlanArgumentPlaceholders(args, prior)
		return ToolCall{Tool: ps.Tool, Input: stringifyArgs(args), Arguments: args}
	}
	args := map[string]any{"task": ps.Description}
	return ToolCall{Tool: ps.Tool, Input: map[string]string{"task": ps.Description}, Arguments: args}
}

func resolvePlanArgumentPlaceholders(args map[string]any, prior []Step) map[string]any {
	if len(args) == 0 {
		return args
	}
	lookup := make(map[string]string, len(prior))
	for _, step := range prior {
		content := strings.TrimSpace(step.Obs.Content)
		if content == "" && step.Obs.Error != nil {
			content = step.Obs.Error.Error()
		}
		if strings.TrimSpace(step.ID) != "" {
			lookup[step.ID] = content
		}
	}
	if len(lookup) == 0 {
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = resolvePlanValue(v, lookup)
	}
	return out
}

func resolvePlanValue(v any, lookup map[string]string) any {
	switch t := v.(type) {
	case string:
		return planStepPlaceholderRe.ReplaceAllStringFunc(t, func(match string) string {
			parts := planStepPlaceholderRe.FindStringSubmatch(match)
			if len(parts) != 2 {
				return match
			}
			if replacement, ok := lookup[parts[1]]; ok {
				return replacement
			}
			return match
		})
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = resolvePlanValue(item, lookup)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			out[k] = resolvePlanValue(item, lookup)
		}
		return out
	default:
		return v
	}
}

func stringifyArgs(args map[string]any) map[string]string {
	input := make(map[string]string, len(args))
	for k, v := range args {
		switch t := v.(type) {
		case string:
			input[k] = t
		case nil:
			input[k] = ""
		case bool, float64, int, int64, uint64:
			input[k] = fmt.Sprint(t)
		default:
			raw, err := json.Marshal(t)
			if err != nil {
				input[k] = fmt.Sprint(t)
			} else {
				input[k] = string(raw)
			}
		}
	}
	return input
}

func planExecutionHadIssue(steps []Step) bool {
	for _, step := range steps {
		if isToolFailure(step.Obs) {
			return true
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(step.Obs.Content)), "skipped:") {
			return true
		}
	}
	return false
}

// planUnavailableTool reports whether the plan is unusable, and NAMES the
// reason. It replaces a bare bool that discarded exactly the detail needed to
// diagnose a downgrade: an operator seeing "fell back to ReAct" had no way to
// learn that the planner had asked for, say, read_file on an agent whose
// builtins allowlist omits it.
func planUnavailableTool(plan Plan, toolNames []string) (string, bool) {
	if len(plan.Steps) == 0 {
		return "", true
	}
	if len(toolNames) == 0 {
		return "", false
	}
	for _, step := range plan.Steps {
		if strings.TrimSpace(step.Tool) == "" {
			return fmt.Sprintf("step %q names no tool", step.ID), true
		}
		if !toolAllowed(step.Tool, toolNames) {
			return fmt.Sprintf("step %q requires tool %q, which this agent cannot call", step.ID, step.Tool), true
		}
	}
	return "", false
}

// planDowngradeReason renders the human-readable cause of a plan_execute →
// ReAct downgrade for the trace.
func planDowngradeReason(planErr error, badTool string) string {
	switch {
	case planErr != nil:
		return "planning failed: " + planErr.Error()
	case badTool != "":
		return "the plan was unusable: " + badTool
	default:
		return "the planner returned no steps"
	}
}

// firstUnmetDependency reports whether every declared dependency of ps has
// completed. When one has not, it returns that dependency's id so the skip
// record can name it. ok=true means all dependencies are satisfied.
func firstUnmetDependency(ps PlannedStep, completed map[string]bool) (string, bool) {
	for _, dep := range ps.DependsOn {
		if !completed[strings.TrimSpace(dep)] {
			return dep, false
		}
	}
	return "", true
}

// DefaultMaxParallelSteps bounds how many planned steps execute concurrently
// within one dependency level when the agent sets no explicit
// reasoning.max_parallel_steps. Kept small on purpose: the point is to stop
// serializing provably independent calls, not to let a six-step plan open six
// simultaneous connections to the same host and trip its rate limiter.
const DefaultMaxParallelSteps = 4

// planParallelism resolves the concurrency bound for one run, clamping to at
// least 1 so a zero-valued Config (a directly-constructed Env in a test or an
// embedding host) stays serial rather than deadlocking on a zero-capacity
// semaphore.
func planParallelism(cfg LoopConfig) int {
	if cfg.MaxParallelSteps <= 0 {
		return DefaultMaxParallelSteps
	}
	return cfg.MaxParallelSteps
}

// planDependencyLevels groups a plan's steps into dependency levels: every
// step in level N depends only on steps in levels < N, so a level's members
// are mutually independent and safe to run concurrently. Plan order is
// preserved within each level.
//
// Dependencies come from two sources, and BOTH matter: the declared
// DependsOn list, and any {{other_step.output}} placeholder a step's arguments
// reference. Honouring only the declared list would let a step that reads an
// undeclared placeholder run alongside its producer and silently resolve to an
// empty value — trading a slow-but-correct walk for a fast wrong one.
//
// Conservative by construction: a dependency that is unknown or appears LATER
// in the plan (a forward reference the plan order does not support) pushes the
// step into its own level after everything before it, reproducing the old
// strictly-serial behaviour for that step rather than guessing.
func planDependencyLevels(plan Plan) [][]PlannedStep {
	n := len(plan.Steps)
	if n == 0 {
		return nil
	}
	index := make(map[string]int, n)
	for i, ps := range plan.Steps {
		if id := strings.TrimSpace(ps.ID); id != "" {
			if _, dup := index[id]; !dup {
				index[id] = i
			}
		}
	}

	levels := make([]int, n)
	maxSoFar := -1
	for i, ps := range plan.Steps {
		level := 0
		serialize := false
		for _, dep := range planStepDependencies(ps) {
			j, known := index[dep]
			if !known || j >= i {
				serialize = true
				break
			}
			if levels[j]+1 > level {
				level = levels[j] + 1
			}
		}
		if serialize {
			level = maxSoFar + 1
		}
		levels[i] = level
		if level > maxSoFar {
			maxSoFar = level
		}
	}

	grouped := make([][]PlannedStep, maxSoFar+1)
	for i, ps := range plan.Steps {
		grouped[levels[i]] = append(grouped[levels[i]], ps)
	}
	out := grouped[:0]
	for _, g := range grouped {
		if len(g) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// planStepDependencies returns every step id this step depends on: the
// declared DependsOn entries plus the ids referenced by {{id.output}}-style
// placeholders in its arguments.
func planStepDependencies(ps PlannedStep) []string {
	seen := map[string]bool{}
	var deps []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		deps = append(deps, id)
	}
	for _, dep := range ps.DependsOn {
		add(dep)
	}
	for _, raw := range planStepArgumentStrings(ps) {
		for _, m := range planStepPlaceholderRe.FindAllStringSubmatch(raw, -1) {
			if len(m) > 1 {
				add(m[1])
			}
		}
	}
	return deps
}

// planStepArgumentStrings renders a planned step's argument values as strings
// so placeholder references can be scanned out of them.
func planStepArgumentStrings(ps PlannedStep) []string {
	var out []string
	for _, v := range ps.Arguments {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		default:
			if b, err := json.Marshal(v); err == nil {
				out = append(out, string(b))
			}
		}
	}
	for _, v := range ps.Input {
		out = append(out, v)
	}
	return out
}

func normalizePlanToolAliases(plan Plan) Plan {
	for i := range plan.Steps {
		plan.Steps[i].Tool = canonicalToolAlias(plan.Steps[i].Tool)
	}
	return plan
}
