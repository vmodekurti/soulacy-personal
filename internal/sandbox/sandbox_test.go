package sandbox

import (
	"reflect"
	"testing"
)

// TestWrap_DisabledPassthrough — when limits are off, the wrapper must
// hand the engine back the exact command the engine asked to run. No
// extra args. No env mutation. The whole point of the Enabled switch is
// to be a true no-op so an operator can flip sandboxing off without
// touching the engine path.
func TestWrap_DisabledPassthrough(t *testing.T) {
	in := []string{"python3", "-c", "print('hi')"}
	out := Wrap("/usr/local/bin/soulacy", Limits{Enabled: false}, in)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("disabled wrap should be identity; got %v", out)
	}
}

// TestWrap_NoSelfPathPassthrough — discovering os.Executable() can fail
// in obscure setups. Rather than refuse to run the tool, the engine
// degrades to direct exec when self is empty. Verifying that here so a
// later refactor doesn't accidentally make the engine crash on missing
// self.
func TestWrap_NoSelfPathPassthrough(t *testing.T) {
	in := []string{"python3", "-c", "print('hi')"}
	out := Wrap("", DefaultLimits(), in)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("missing self should be identity; got %v", out)
	}
}

// TestWrap_BuildsCorrectArgv — golden case. When enabled, the wrapper
// emits [self, sentinel, --cpu=X, --mem=Y, --nofile=Z, --fsize=W, --,
// cmd…]. Order of the limit flags doesn't matter to the parser, but the
// `--` separator MUST precede the wrapped command or argv parsing will
// eat the wrong tokens.
func TestWrap_BuildsCorrectArgv(t *testing.T) {
	in := []string{"python3", "-c", "print('hi')"}
	out := Wrap("/usr/local/bin/soulacy", Limits{
		Enabled: true, CPUSeconds: 30, MemoryMB: 512, OpenFiles: 256, FileSizeMB: 64,
	}, in)
	want := []string{
		"/usr/local/bin/soulacy", "__exec-sandbox",
		"--cpu=30", "--mem=512", "--nofile=256", "--fsize=64",
		"--", "python3", "-c", "print('hi')",
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("argv mismatch\n got  %v\n want %v", out, want)
	}
}

// TestParseSandboxArgs_Roundtrip — feeding the output of Wrap back into
// parseSandboxArgs should produce the same Limits + the same trailing
// command. Without this, a typo in either side (flag name change, wrong
// `--` placement) silently breaks the sandbox at runtime.
func TestParseSandboxArgs_Roundtrip(t *testing.T) {
	in := []string{"python3", "-c", "x=1"}
	limitsIn := Limits{Enabled: true, CPUSeconds: 10, MemoryMB: 64, OpenFiles: 32, FileSizeMB: 4}
	argv := Wrap("/soulacy", limitsIn, in)

	limitsOut, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if limitsOut.CPUSeconds != limitsIn.CPUSeconds ||
		limitsOut.MemoryMB != limitsIn.MemoryMB ||
		limitsOut.OpenFiles != limitsIn.OpenFiles ||
		limitsOut.FileSizeMB != limitsIn.FileSizeMB {
		t.Errorf("limits roundtrip: got %+v want %+v", limitsOut, limitsIn)
	}
	if !reflect.DeepEqual(cmd, in) {
		t.Errorf("cmd roundtrip: got %v want %v", cmd, in)
	}
}

// TestParseSandboxArgs_MissingCommand — defensively: forgetting the
// `--` separator OR forgetting the trailing command should be reported,
// not silently treated as an empty command (which would execve to "").
func TestParseSandboxArgs_MissingCommand(t *testing.T) {
	cases := [][]string{
		{"soulacy", "__exec-sandbox", "--cpu=10"},          // no -- at all
		{"soulacy", "__exec-sandbox", "--cpu=10", "--"},    // -- but nothing after
	}
	for _, argv := range cases {
		_, _, err := parseSandboxArgs(argv)
		if err == nil {
			t.Errorf("expected error for argv=%v", argv)
		}
	}
}

// TestIsSandboxInvocation guards the entry-point check in main(). If we
// rename the sentinel, the check moves with it (it reads `sentinel` from
// this package), but a typo in `os.Args[1] == "…"` somewhere else would
// only surface as "sandbox limits never apply" — silent and bad. This
// test plus the Wrap test together ensure the round-trip works.
func TestIsSandboxInvocation(t *testing.T) {
	if !IsSandboxInvocation([]string{"soulacy", "__exec-sandbox", "--cpu=1", "--", "python"}) {
		t.Errorf("should detect sentinel")
	}
	if IsSandboxInvocation([]string{"soulacy", "serve"}) {
		t.Errorf("should not detect on normal invocation")
	}
	if IsSandboxInvocation([]string{"soulacy"}) {
		t.Errorf("should not detect on bare argv")
	}
}

// ---------------------------------------------------------------------------
// Additional coverage tests
// ---------------------------------------------------------------------------

// TestDefaultLimits verifies that DefaultLimits() returns the documented
// conservative values. A regression here would silently loosen resource
// caps on every production sandbox invocation.
func TestDefaultLimits(t *testing.T) {
	l := DefaultLimits()
	if !l.Enabled {
		t.Error("default Enabled should be true")
	}
	if l.CPUSeconds != 30 {
		t.Errorf("CPUSeconds = %d, want 30", l.CPUSeconds)
	}
	if l.MemoryMB != 512 {
		t.Errorf("MemoryMB = %d, want 512", l.MemoryMB)
	}
	if l.OpenFiles != 256 {
		t.Errorf("OpenFiles = %d, want 256", l.OpenFiles)
	}
	if l.FileSizeMB != 64 {
		t.Errorf("FileSizeMB = %d, want 64", l.FileSizeMB)
	}
}

// TestWrap_EmptyCommandPassthrough — empty cmd must not produce a
// sandbox wrapper; returning an empty slice would cause exec.Command("")
// to fail in mysterious ways.
func TestWrap_EmptyCommandPassthrough(t *testing.T) {
	out := Wrap("/soulacy", DefaultLimits(), []string{})
	if len(out) != 0 {
		t.Errorf("empty cmd should pass through as empty, got %v", out)
	}
}

// TestWrap_ZeroLimitsOmitted verifies that Wrap omits individual flags
// for zero-valued limit fields. Zero means "no limit applied for that
// knob" — emitting `--cpu=0` would confuse the parser (and potentially
// set the rlimit to zero, killing the process immediately).
func TestWrap_ZeroLimitsOmitted(t *testing.T) {
	in := []string{"python3", "script.py"}
	// Only CPUSeconds set; the rest are zero.
	out := Wrap("/soulacy", Limits{Enabled: true, CPUSeconds: 5}, in)

	// Verify --cpu= is present.
	found := false
	for _, a := range out {
		if a == "--cpu=5" {
			found = true
		}
		if a == "--mem=0" || a == "--nofile=0" || a == "--fsize=0" {
			t.Errorf("zero limit should be omitted, got %q in %v", a, out)
		}
	}
	if !found {
		t.Errorf("expected --cpu=5 in %v", out)
	}
}

// TestWrap_OnlyMemorySet verifies the partial-limits case where only
// MemoryMB is configured — only --mem= should appear.
func TestWrap_OnlyMemorySet(t *testing.T) {
	in := []string{"python3", "script.py"}
	out := Wrap("/soulacy", Limits{Enabled: true, MemoryMB: 128}, in)

	for _, a := range out {
		if a == "--cpu=0" || a == "--nofile=0" || a == "--fsize=0" {
			t.Errorf("zero limit should be omitted, got %q in %v", a, out)
		}
	}

	found := false
	for _, a := range out {
		if a == "--mem=128" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --mem=128 in wrapped command %v", out)
	}
}

// TestHasPrefix covers all branches of the hasPrefix helper to ensure
// the string comparison doesn't have an off-by-one error.
func TestHasPrefix(t *testing.T) {
	cases := []struct {
		s, p string
		want bool
	}{
		{"--cpu=30", "--cpu=", true},
		{"--cpu=", "--cpu=", true},
		{"--cpu", "--cpu=", false},  // shorter than prefix
		{"", "--cpu=", false},
		{"--mem=512", "--cpu=", false},
		{"--fsize=64", "--fsize=", true},
	}
	for _, tc := range cases {
		got := hasPrefix(tc.s, tc.p)
		if got != tc.want {
			t.Errorf("hasPrefix(%q, %q) = %v, want %v", tc.s, tc.p, got, tc.want)
		}
	}
}

// TestParseSandboxArgs_BadFlags verifies that each individual unknown or
// malformed flag returns a descriptive error rather than silently being
// ignored or causing a panic.
func TestParseSandboxArgs_BadFlags(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{
			"bad cpu value",
			[]string{"soulacy", "__exec-sandbox", "--cpu=notanumber", "--", "python"},
		},
		{
			"bad mem value",
			[]string{"soulacy", "__exec-sandbox", "--mem=notanumber", "--", "python"},
		},
		{
			"bad nofile value",
			[]string{"soulacy", "__exec-sandbox", "--nofile=x", "--", "python"},
		},
		{
			"bad fsize value",
			[]string{"soulacy", "__exec-sandbox", "--fsize=x", "--", "python"},
		},
		{
			"unknown flag",
			[]string{"soulacy", "__exec-sandbox", "--unknown=1", "--", "python"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseSandboxArgs(tc.argv)
			if err == nil {
				t.Errorf("expected error for %v, got nil", tc.argv)
			}
		})
	}
}

// TestParseSandboxArgs_AllLimits verifies that all four limit flags are
// parsed independently and that their values are stored in the correct fields.
func TestParseSandboxArgs_AllLimits(t *testing.T) {
	argv := []string{
		"soulacy", "__exec-sandbox",
		"--cpu=99", "--mem=200", "--nofile=50", "--fsize=10",
		"--", "bash", "-c", "echo hi",
	}
	l, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		t.Fatalf("parseSandboxArgs: %v", err)
	}
	if l.CPUSeconds != 99 {
		t.Errorf("CPUSeconds = %d, want 99", l.CPUSeconds)
	}
	if l.MemoryMB != 200 {
		t.Errorf("MemoryMB = %d, want 200", l.MemoryMB)
	}
	if l.OpenFiles != 50 {
		t.Errorf("OpenFiles = %d, want 50", l.OpenFiles)
	}
	if l.FileSizeMB != 10 {
		t.Errorf("FileSizeMB = %d, want 10", l.FileSizeMB)
	}
	if !reflect.DeepEqual(cmd, []string{"bash", "-c", "echo hi"}) {
		t.Errorf("cmd = %v, want [bash -c echo hi]", cmd)
	}
}

// TestParseSandboxArgs_NoFlags verifies that a bare `--` with no limit
// flags still produces a valid Limits{Enabled:true} and the wrapped command.
func TestParseSandboxArgs_NoFlags(t *testing.T) {
	argv := []string{"soulacy", "__exec-sandbox", "--", "echo", "hello"}
	l, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		t.Fatalf("parseSandboxArgs: %v", err)
	}
	if !l.Enabled {
		t.Error("Enabled should be true even with no flags")
	}
	if !reflect.DeepEqual(cmd, []string{"echo", "hello"}) {
		t.Errorf("cmd = %v, want [echo hello]", cmd)
	}
}

// ---------------------------------------------------------------------------
// Additional coverage: uncovered paths
// ---------------------------------------------------------------------------

// TestIsSandboxInvocation_ExactlyTwoArgs verifies the edge case where argv
// has exactly two entries: the binary and the sentinel. This is still a valid
// sandbox invocation (RunSandboxedAndExit would then fail with "missing
// command" — tested separately), but IsSandboxInvocation must return true.
func TestIsSandboxInvocation_ExactlyTwoArgs(t *testing.T) {
	if !IsSandboxInvocation([]string{"soulacy", "__exec-sandbox"}) {
		t.Error("IsSandboxInvocation should return true for exactly [binary, sentinel]")
	}
}

// TestIsSandboxInvocation_EmptyArgv verifies that an empty argv slice (edge
// case on some OS stubs) does not panic and returns false.
func TestIsSandboxInvocation_EmptyArgv(t *testing.T) {
	if IsSandboxInvocation([]string{}) {
		t.Error("IsSandboxInvocation on empty argv should return false")
	}
}

// TestIsSandboxInvocation_SingleArg verifies that a bare binary path (argv
// with only one element) returns false — need at least two entries.
func TestIsSandboxInvocation_SingleArg(t *testing.T) {
	if IsSandboxInvocation([]string{"soulacy"}) {
		t.Error("IsSandboxInvocation on single-element argv should return false")
	}
}

// TestIsSandboxInvocation_WrongSentinel verifies that a second argument that
// is NOT the sentinel string is not treated as a sandbox invocation, even if
// the binary name matches.
func TestIsSandboxInvocation_WrongSentinel(t *testing.T) {
	cases := [][]string{
		{"soulacy", "serve"},
		{"soulacy", "_exec-sandbox"},  // one underscore
		{"soulacy", "__exec_sandbox"}, // hyphen → underscore
		{"soulacy", ""},
	}
	for _, argv := range cases {
		if IsSandboxInvocation(argv) {
			t.Errorf("IsSandboxInvocation(%v) = true, want false", argv)
		}
	}
}

// TestWrap_LargeMultiArgCommand verifies that Wrap correctly preserves a
// multi-argument command (five elements) after the "--" separator.
func TestWrap_LargeMultiArgCommand(t *testing.T) {
	in := []string{"python3", "-W", "ignore", "-c", "import sys; print(sys.version)"}
	out := Wrap("/soulacy", Limits{Enabled: true, CPUSeconds: 5}, in)

	// Locate "--" and verify everything after it matches `in`.
	sep := -1
	for i, a := range out {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep == -1 {
		t.Fatalf("no '--' found in %v", out)
	}
	tail := out[sep+1:]
	if !reflect.DeepEqual(tail, in) {
		t.Errorf("tail = %v, want %v", tail, in)
	}
}

// TestWrap_EnabledWithAllZeroLimits verifies that when Enabled is true but
// every numeric limit is zero, the output is still a valid sandbox invocation
// (binary + sentinel + "--" + cmd) with no limit flags.
func TestWrap_EnabledWithAllZeroLimits(t *testing.T) {
	in := []string{"bash", "-c", "echo ok"}
	out := Wrap("/soulacy", Limits{Enabled: true}, in)

	// Must start with self and sentinel.
	if len(out) < 2 || out[0] != "/soulacy" || out[1] != sentinel {
		t.Fatalf("output missing self/sentinel: %v", out)
	}
	// Must contain "--".
	hasSep := false
	for _, a := range out {
		if a == "--" {
			hasSep = true
		}
		// None of the limit flags should be present.
		for _, flag := range []string{"--cpu=", "--mem=", "--nofile=", "--fsize="} {
			if hasPrefix(a, flag) {
				t.Errorf("unexpected flag %q in output %v", a, out)
			}
		}
	}
	if !hasSep {
		t.Errorf("'--' separator missing from output: %v", out)
	}
	// Tail must equal `in`.
	sep := -1
	for i, a := range out {
		if a == "--" {
			sep = i
		}
	}
	if !reflect.DeepEqual(out[sep+1:], in) {
		t.Errorf("wrapped command tail = %v, want %v", out[sep+1:], in)
	}
}

// TestParseSandboxArgs_OnlyDashDash verifies the "-- but nothing after"
// error path explicitly: argv ends exactly at the "--" separator itself
// so i ends up equal to len(argv).
func TestParseSandboxArgs_OnlyDashDash(t *testing.T) {
	argv := []string{"soulacy", "__exec-sandbox", "--"}
	_, _, err := parseSandboxArgs(argv)
	if err == nil {
		t.Fatal("expected error when '--' has no trailing command")
	}
}

// TestParseSandboxArgs_NoDashDashAtAll verifies that omitting the separator
// entirely (all tokens look like flags) also produces an error because the
// loop exhausts argv without finding "--".
func TestParseSandboxArgs_NoDashDashAtAll(t *testing.T) {
	argv := []string{"soulacy", "__exec-sandbox", "--cpu=10", "--mem=64"}
	_, _, err := parseSandboxArgs(argv)
	if err == nil {
		t.Fatal("expected error when '--' is missing entirely")
	}
}

// TestParseSandboxArgs_ShortArgv verifies that an argv shorter than 3
// elements (i.e. only the binary + sentinel, no flags, no "--") also
// produces an error for the missing command path.
func TestParseSandboxArgs_ShortArgv(t *testing.T) {
	// parseSandboxArgs starts at i=2. If len(argv) == 2 the loop doesn't
	// execute and we fall through to the "i >= len(argv)" check.
	argv := []string{"soulacy", "__exec-sandbox"}
	_, _, err := parseSandboxArgs(argv)
	if err == nil {
		t.Fatal("expected error for argv with only binary+sentinel")
	}
}

// TestHasPrefix_EqualLengthMismatch verifies that a string equal in length to
// the prefix but with different content correctly returns false.
func TestHasPrefix_EqualLengthMismatch(t *testing.T) {
	if hasPrefix("--mem=", "--cpu=") {
		t.Error("hasPrefix('--mem=', '--cpu=') should be false (same length, different bytes)")
	}
}

// TestDefaultLimits_Struct verifies that the returned Limits struct is
// fully populated — no field is accidentally left at its zero value. This
// catches silent regressions where a field rename causes DefaultLimits to
// return a partial struct.
func TestDefaultLimits_Struct(t *testing.T) {
	l := DefaultLimits()
	fields := map[string]int{
		"CPUSeconds": l.CPUSeconds,
		"MemoryMB":   l.MemoryMB,
		"OpenFiles":  l.OpenFiles,
		"FileSizeMB": l.FileSizeMB,
	}
	for name, v := range fields {
		if v <= 0 {
			t.Errorf("DefaultLimits().%s = %d, want > 0", name, v)
		}
	}
}

// TestWrap_SentinelConstantValue guards against accidental rename of the
// "__exec-sandbox" sentinel. If the sentinel changes, both Wrap and
// IsSandboxInvocation must change together — this test makes the coupling
// explicit.
func TestWrap_SentinelConstantValue(t *testing.T) {
	if sentinel != "__exec-sandbox" {
		t.Errorf("sentinel = %q, want __exec-sandbox", sentinel)
	}
	in := []string{"echo", "hello"}
	out := Wrap("/soulacy", Limits{Enabled: true}, in)
	if len(out) < 2 || out[1] != sentinel {
		t.Errorf("Wrap did not embed sentinel at index 1: %v", out)
	}
}

// ---------------------------------------------------------------------------
// syscallEnviron (wrap_unix.go / wrap_other.go)
// ---------------------------------------------------------------------------

// TestSyscallEnvironReturnsNonEmptyEnv verifies that syscallEnviron() returns
// at least one entry. On any supported platform (Linux, Darwin, Windows) the
// process environment will have at least PATH or HOME set by the test runner.
// This exercises the otherwise-uncovered one-liner in both wrap_unix.go and
// wrap_other.go.
func TestSyscallEnvironReturnsNonEmptyEnv(t *testing.T) {
	env := syscallEnviron()
	if len(env) == 0 {
		t.Error("syscallEnviron: expected non-empty environment, got empty slice")
	}
	// Each entry must contain "=" (KEY=VALUE format).
	for _, e := range env {
		hasEq := false
		for _, r := range e {
			if r == '=' {
				hasEq = true
				break
			}
		}
		if !hasEq {
			t.Errorf("syscallEnviron: entry %q does not contain '='", e)
		}
	}
}

// ---------------------------------------------------------------------------
// Limits struct — field semantics
// ---------------------------------------------------------------------------

// TestLimitsZeroValueIsDisabled verifies that a zero-value Limits struct has
// Enabled == false, which is the correct safe default (no accidental sandboxing
// of commands when Limits is declared but not initialised).
func TestLimitsZeroValueIsDisabled(t *testing.T) {
	var l Limits
	if l.Enabled {
		t.Error("zero-value Limits should have Enabled=false")
	}
}

// TestLimitsEnabledFalseWithNonZeroFields verifies that when Enabled is false
// Wrap still returns the original command unchanged regardless of the other
// field values. This guards the operator scenario where limits were configured
// but sandboxing was switched off via a flag.
func TestLimitsEnabledFalseWithNonZeroFields(t *testing.T) {
	in := []string{"python3", "script.py"}
	l := Limits{
		Enabled:    false,
		CPUSeconds: 30,
		MemoryMB:   512,
		OpenFiles:  256,
		FileSizeMB: 64,
	}
	out := Wrap("/soulacy", l, in)
	if !reflect.DeepEqual(out, in) {
		t.Errorf("Enabled=false: Wrap should be identity; got %v", out)
	}
}

// ---------------------------------------------------------------------------
// Wrap — edge cases not covered by existing tests
// ---------------------------------------------------------------------------

// TestWrap_OnlyOpenFilesSet verifies the partial-limits case where only
// OpenFiles is configured — only --nofile= should appear.
func TestWrap_OnlyOpenFilesSet(t *testing.T) {
	in := []string{"bash", "-c", "ls"}
	out := Wrap("/soulacy", Limits{Enabled: true, OpenFiles: 100}, in)

	found := false
	for _, a := range out {
		if a == "--nofile=100" {
			found = true
		}
		if hasPrefix(a, "--cpu=") || hasPrefix(a, "--mem=") || hasPrefix(a, "--fsize=") {
			t.Errorf("unexpected flag %q for OpenFiles-only limits: %v", a, out)
		}
	}
	if !found {
		t.Errorf("expected --nofile=100 in %v", out)
	}
}

// TestWrap_OnlyFileSizeSet verifies that only --fsize= appears when only
// FileSizeMB is configured.
func TestWrap_OnlyFileSizeSet(t *testing.T) {
	in := []string{"bash", "-c", "echo"}
	out := Wrap("/soulacy", Limits{Enabled: true, FileSizeMB: 32}, in)

	found := false
	for _, a := range out {
		if a == "--fsize=32" {
			found = true
		}
		if hasPrefix(a, "--cpu=") || hasPrefix(a, "--mem=") || hasPrefix(a, "--nofile=") {
			t.Errorf("unexpected flag %q for FileSizeMB-only limits: %v", a, out)
		}
	}
	if !found {
		t.Errorf("expected --fsize=32 in %v", out)
	}
}

// TestParseSandboxArgs_MultipleCommandArgs verifies that all arguments after
// "--" are preserved in the cmd slice — including flags that look like sandbox
// flags (they belong to the wrapped program, not the sandbox).
func TestParseSandboxArgs_MultipleCommandArgs(t *testing.T) {
	argv := []string{
		"soulacy", "__exec-sandbox", "--cpu=5", "--",
		"python3", "--version", "--no-site",
	}
	_, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		t.Fatalf("parseSandboxArgs: %v", err)
	}
	want := []string{"python3", "--version", "--no-site"}
	if !reflect.DeepEqual(cmd, want) {
		t.Errorf("cmd = %v, want %v", cmd, want)
	}
}

// TestIsSandboxInvocation_NonStringArgv confirms that the function works
// correctly when argv[1] contains characters that look like flags but aren't
// the sentinel.
func TestIsSandboxInvocation_FlagLookingArgv(t *testing.T) {
	// "--exec-sandbox" is close to the sentinel but not equal (missing extra _).
	if IsSandboxInvocation([]string{"soulacy", "--exec-sandbox"}) {
		t.Error("'--exec-sandbox' is not the sentinel; should return false")
	}
}

// TestDefaultLimits_EnabledIsTrue is a focused guard that Enabled is true in
// DefaultLimits — a single-field regression test so any refactor is caught
// before it reaches the per-flag checks in TestDefaultLimits.
func TestDefaultLimits_EnabledIsTrue(t *testing.T) {
	if !DefaultLimits().Enabled {
		t.Error("DefaultLimits().Enabled must be true")
	}
}

// TestWrap_CapacityPreallocated is a white-box test that confirms Wrap does not
// return more elements than expected: len(cmd) + len(self) + len(sentinel) +
// up to 4 flags + 1 separator. This prevents capacity miscalculation regressions
// that could cause silent out-of-bounds or extra nil args.
func TestWrap_CapacityPreallocated(t *testing.T) {
	in := []string{"echo", "hi"}
	l := Limits{
		Enabled:    true,
		CPUSeconds: 1,
		MemoryMB:   2,
		OpenFiles:  3,
		FileSizeMB: 4,
	}
	out := Wrap("/soulacy", l, in)
	// Expected: /soulacy __exec-sandbox --cpu=1 --mem=2 --nofile=3 --fsize=4 -- echo hi = 9 elements
	if len(out) != 9 {
		t.Errorf("Wrap all-limits: expected 9 elements, got %d: %v", len(out), out)
	}
}
