package agent

import "github.com/soulacy/soulacy/sdk/reasoning"

// WorkflowSpec is declared under the `workflow:` key in SOUL.yaml.
//
// Two shapes are supported:
//   - steps: the original linear DAG (E5) — executed step by step.
//   - nodes/edges: the cyclic graph form (E25) — conditional edges,
//     bounded cycles, compiled onto the same checkpointing executor.
//
// When nodes are declared they take precedence over steps.
type WorkflowSpec struct {
	Steps []StepSpec `yaml:"steps,omitempty" json:"steps,omitempty"`

	// Graph form (Story E25). Types are the canonical sdk/reasoning flow
	// contract so the "flow" strategy and the executor share one shape.
	Nodes []reasoning.FlowNode `yaml:"nodes,omitempty" json:"nodes,omitempty"`
	Edges []reasoning.FlowEdge `yaml:"edges,omitempty" json:"edges,omitempty"`
	Entry string               `yaml:"entry,omitempty" json:"entry,omitempty"`
	// Output is the node id whose result is the flow's final output (default:
	// last executed node).
	Output string `yaml:"output,omitempty" json:"output,omitempty"`
	// MaxNodeExecutions is the global safety budget (default 100).
	MaxNodeExecutions int `yaml:"max_node_executions,omitempty" json:"max_node_executions,omitempty"`
}

// FlowSpec returns the graph form as the sdk contract, or nil when the
// workflow has no nodes (linear steps mode).
func (w *WorkflowSpec) FlowSpec() *reasoning.FlowSpec {
	if w == nil || len(w.Nodes) == 0 {
		return nil
	}
	return &reasoning.FlowSpec{
		Nodes:             w.Nodes,
		Edges:             w.Edges,
		Entry:             w.Entry,
		Output:            w.Output,
		MaxNodeExecutions: w.MaxNodeExecutions,
	}
}

// StepSpec describes one step in a workflow DAG.
type StepSpec struct {
	ID      string `yaml:"id"       json:"id"`       // unique within workflow; used as checkpoint key
	Tool    string `yaml:"tool"     json:"tool"`     // tool name to invoke
	Prompt  string `yaml:"prompt"   json:"prompt"`   // optional LLM prompt for this step
	If      string `yaml:"if"       json:"if"`       // Go template condition; skip step if evaluates to falsy
	OnError string `yaml:"on_error" json:"on_error"` // "retry" | "skip" | "abort" (default: "abort")
	Input   string `yaml:"input"    json:"input"`    // Go template producing JSON input for the tool
	Output  string `yaml:"output"   json:"output"`   // variable name to store this step's JSON output
}
