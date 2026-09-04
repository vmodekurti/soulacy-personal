package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	sdkr "github.com/soulacy/soulacy/sdk/reasoning"

	"github.com/soulacy/soulacy/internal/studio"
)

// pyCheckScript PARSES (never executes) a Python node's source and reports
// syntax validity plus whether it defines the required top-level run(inputs)
// function. ast.parse compiles to an AST without running the code, so this is
// safe to run on LLM-authored source at build time.
const pyCheckScript = `import ast,sys,json
src=sys.stdin.read()
try:
    tree=ast.parse(src)
except SyntaxError as e:
    print(json.dumps({"syntax_error":(e.msg or "invalid syntax"),"line":(e.lineno or 0)}));sys.exit(0)
except Exception as e:
    print(json.dumps({"syntax_error":str(e),"line":0}));sys.exit(0)
has_run=any(isinstance(n,ast.FunctionDef) and n.name=="run" for n in tree.body)
print(json.dumps({"has_run":has_run}))`

// validatePythonNodes syntax-checks every inline Python node in the draft using
// the configured interpreter and returns hard errors (syntax errors, or a
// missing run(inputs) entrypoint the runtime requires). Degrades to no findings
// when the interpreter is unavailable, so validation never breaks on a missing
// python3.
func (s *Server) validatePythonNodes(draft studio.Draft) []studio.ValidateError {
	pythonBin := strings.TrimSpace(s.cfg.Runtime.PythonBin)
	if pythonBin == "" {
		pythonBin = "python3"
	}

	var errs []studio.ValidateError
	for _, n := range draft.Flow.Nodes {
		if !strings.EqualFold(strings.TrimSpace(n.Kind), sdkr.FlowNodePython) {
			continue
		}
		code := n.Code
		if strings.TrimSpace(code) == "" {
			continue // a python node that references a deployed tool — nothing to parse
		}
		verdict, ok := runPyCheck(pythonBin, code)
		if !ok {
			return nil // interpreter unavailable — skip the whole check silently
		}
		if verdict.SyntaxError != "" {
			msg := "Python syntax error: " + verdict.SyntaxError
			if verdict.Line > 0 {
				msg += " (line " + itoa(verdict.Line) + ")"
			}
			errs = append(errs, studio.ValidateError{NodeID: n.ID, Message: msg})
			continue
		}
		if !verdict.HasRun {
			errs = append(errs, studio.ValidateError{
				NodeID:  n.ID,
				Message: "Python node must define a top-level `def run(inputs):` — the runtime calls run(inputs) with the node's JSON input and uses its return value as the node output.",
			})
		}
	}
	return errs
}

// pythonBuildProblems adapts the python validity check into the plain problem
// lines the "Build until it works" loop feeds to the repair model, so broken
// generated python is fixed automatically like any other build problem.
func (s *Server) pythonBuildProblems(d studio.Draft) []string {
	var out []string
	for _, e := range s.validatePythonNodes(d) {
		prefix := ""
		if e.NodeID != "" {
			prefix = "node " + e.NodeID + ": "
		}
		out = append(out, prefix+e.Message)
	}
	return out
}

type pyVerdict struct {
	SyntaxError string `json:"syntax_error"`
	Line        int    `json:"line"`
	HasRun      bool   `json:"has_run"`
}

func runPyCheck(pythonBin, code string) (pyVerdict, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pythonBin, "-c", pyCheckScript)
	cmd.Stdin = strings.NewReader(code)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		// A non-zero exit with valid JSON on stdout is fine (we sys.exit(0) on
		// syntax error), so only treat a truly failed spawn / empty output as
		// "interpreter unavailable".
		if out.Len() == 0 {
			return pyVerdict{}, false
		}
	}
	var v pyVerdict
	if jerr := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &v); jerr != nil {
		return pyVerdict{}, false
	}
	return v, true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
