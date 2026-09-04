package command

import (
	"strings"
	"testing"
)

func TestArgv_PrependsRunner(t *testing.T) {
	e := New("runpod", []string{"runpodctl", "exec", "python", "--"}, "python3")
	argv := e.Argv("print(1)")
	joined := strings.Join(argv, " ")
	if !strings.HasPrefix(joined, "runpodctl exec python -- python3 -c print(1)") {
		t.Fatalf("argv wrong: %s", joined)
	}
}

func TestNew_TrimsAndDefaults(t *testing.T) {
	e := New("", []string{" wrap ", ""}, "")
	if e.label != "command" || e.pythonBin != "python3" {
		t.Fatalf("defaults not applied: label=%q python=%q", e.label, e.pythonBin)
	}
	if len(e.runner) != 1 || e.runner[0] != "wrap" {
		t.Fatalf("runner not cleaned: %v", e.runner)
	}
}
