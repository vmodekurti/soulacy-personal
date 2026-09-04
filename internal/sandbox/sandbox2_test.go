package sandbox

import (
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// parseSandboxArgs — single-flag parse paths
// ---------------------------------------------------------------------------

// TestParseSandboxArgs_OnlyNofile verifies the --nofile= flag is parsed in
// isolation: only OpenFiles should be non-zero; all other limit fields stay at
// their zero values.
func TestParseSandboxArgs_OnlyNofile(t *testing.T) {
	argv := []string{"soulacy", "__exec-sandbox", "--nofile=128", "--", "cat"}
	l, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		t.Fatalf("parseSandboxArgs: %v", err)
	}
	if l.OpenFiles != 128 {
		t.Errorf("OpenFiles = %d, want 128", l.OpenFiles)
	}
	if l.CPUSeconds != 0 || l.MemoryMB != 0 || l.FileSizeMB != 0 {
		t.Errorf("unexpected non-zero fields in limits: %+v", l)
	}
	if !reflect.DeepEqual(cmd, []string{"cat"}) {
		t.Errorf("cmd = %v, want [cat]", cmd)
	}
}

// TestParseSandboxArgs_OnlyFsize verifies the --fsize= flag is parsed in
// isolation: only FileSizeMB should be non-zero.
func TestParseSandboxArgs_OnlyFsize(t *testing.T) {
	argv := []string{"soulacy", "__exec-sandbox", "--fsize=32", "--", "ls"}
	l, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		t.Fatalf("parseSandboxArgs: %v", err)
	}
	if l.FileSizeMB != 32 {
		t.Errorf("FileSizeMB = %d, want 32", l.FileSizeMB)
	}
	if l.CPUSeconds != 0 || l.MemoryMB != 0 || l.OpenFiles != 0 {
		t.Errorf("unexpected non-zero fields in limits: %+v", l)
	}
	if !reflect.DeepEqual(cmd, []string{"ls"}) {
		t.Errorf("cmd = %v, want [ls]", cmd)
	}
}

// TestParseSandboxArgs_OnlyCPU verifies the --cpu= flag is parsed in isolation.
func TestParseSandboxArgs_OnlyCPU(t *testing.T) {
	argv := []string{"soulacy", "__exec-sandbox", "--cpu=60", "--", "sleep", "1"}
	l, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		t.Fatalf("parseSandboxArgs: %v", err)
	}
	if l.CPUSeconds != 60 {
		t.Errorf("CPUSeconds = %d, want 60", l.CPUSeconds)
	}
	if l.MemoryMB != 0 || l.OpenFiles != 0 || l.FileSizeMB != 0 {
		t.Errorf("unexpected non-zero fields in limits: %+v", l)
	}
	if !reflect.DeepEqual(cmd, []string{"sleep", "1"}) {
		t.Errorf("cmd = %v, want [sleep 1]", cmd)
	}
}

// TestParseSandboxArgs_OnlyMem verifies the --mem= flag is parsed in isolation.
func TestParseSandboxArgs_OnlyMem(t *testing.T) {
	argv := []string{"soulacy", "__exec-sandbox", "--mem=256", "--", "python3"}
	l, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		t.Fatalf("parseSandboxArgs: %v", err)
	}
	if l.MemoryMB != 256 {
		t.Errorf("MemoryMB = %d, want 256", l.MemoryMB)
	}
	if l.CPUSeconds != 0 || l.OpenFiles != 0 || l.FileSizeMB != 0 {
		t.Errorf("unexpected non-zero fields in limits: %+v", l)
	}
	if !reflect.DeepEqual(cmd, []string{"python3"}) {
		t.Errorf("cmd = %v, want [python3]", cmd)
	}
}

// ---------------------------------------------------------------------------
// Wrap — partial limit combinations not yet covered
// ---------------------------------------------------------------------------

// TestWrap_OnlyCPUAndFsize verifies that when only CPUSeconds and FileSizeMB
// are set, Wrap emits exactly --cpu= and --fsize=, omitting --mem= and --nofile=.
func TestWrap_OnlyCPUAndFsize(t *testing.T) {
	in := []string{"python3", "run.py"}
	out := Wrap("/soulacy", Limits{Enabled: true, CPUSeconds: 10, FileSizeMB: 16}, in)

	wantFlags := map[string]bool{"--cpu=10": false, "--fsize=16": false}
	for _, a := range out {
		if _, ok := wantFlags[a]; ok {
			wantFlags[a] = true
		}
		if hasPrefix(a, "--mem=") || hasPrefix(a, "--nofile=") {
			t.Errorf("unexpected flag %q in output %v", a, out)
		}
	}
	for flag, found := range wantFlags {
		if !found {
			t.Errorf("expected flag %q not found in %v", flag, out)
		}
	}
}

// TestWrap_OnlyNofileAndFsize verifies the combination of only OpenFiles and
// FileSizeMB: --nofile= and --fsize= are emitted; --cpu= and --mem= are not.
func TestWrap_OnlyNofileAndFsize(t *testing.T) {
	in := []string{"bash", "script.sh"}
	out := Wrap("/soulacy", Limits{Enabled: true, OpenFiles: 64, FileSizeMB: 8}, in)

	wantFlags := map[string]bool{"--nofile=64": false, "--fsize=8": false}
	for _, a := range out {
		if _, ok := wantFlags[a]; ok {
			wantFlags[a] = true
		}
		if hasPrefix(a, "--cpu=") || hasPrefix(a, "--mem=") {
			t.Errorf("unexpected flag %q in output %v", a, out)
		}
	}
	for flag, found := range wantFlags {
		if !found {
			t.Errorf("expected flag %q not found in %v", flag, out)
		}
	}
}

// TestWrap_NegativeLimitsOmitted verifies that negative limit values are
// treated as zero and not emitted as flags. The source guards are `> 0`, so
// negative values must be silently skipped.
func TestWrap_NegativeLimitsOmitted(t *testing.T) {
	in := []string{"echo", "hi"}
	out := Wrap("/soulacy", Limits{Enabled: true, CPUSeconds: -1, MemoryMB: -100, OpenFiles: -5, FileSizeMB: -10}, in)

	for _, a := range out {
		for _, prefix := range []string{"--cpu=", "--mem=", "--nofile=", "--fsize="} {
			if hasPrefix(a, prefix) {
				t.Errorf("negative limit produced flag %q in %v", a, out)
			}
		}
	}
	// Must still be a valid sandbox invocation (self + sentinel + -- + cmd).
	if len(out) < 4 {
		t.Fatalf("output too short for valid sandbox invocation: %v", out)
	}
	if out[0] != "/soulacy" || out[1] != sentinel {
		t.Errorf("self/sentinel missing: %v", out)
	}
}

// TestWrap_LargeValues verifies that very large limit values are formatted
// correctly (no overflow or truncation in the Itoa conversion path).
func TestWrap_LargeValues(t *testing.T) {
	in := []string{"prog"}
	l := Limits{Enabled: true, CPUSeconds: 3600, MemoryMB: 65536, OpenFiles: 1048576, FileSizeMB: 32768}
	out := Wrap("/soulacy", l, in)

	want := map[string]bool{
		"--cpu=3600":     false,
		"--mem=65536":    false,
		"--nofile=1048576": false,
		"--fsize=32768":  false,
	}
	for _, a := range out {
		if _, ok := want[a]; ok {
			want[a] = true
		}
	}
	for flag, found := range want {
		if !found {
			t.Errorf("expected flag %q not found in %v", flag, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Limits struct — field independence and zero-value semantics
// ---------------------------------------------------------------------------

// TestLimits_AllFieldsIndependent verifies that setting each field of a Limits
// struct individually does not affect the others.
func TestLimits_AllFieldsIndependent(t *testing.T) {
	l := Limits{}
	l.CPUSeconds = 5
	if l.MemoryMB != 0 || l.OpenFiles != 0 || l.FileSizeMB != 0 || l.Enabled {
		t.Errorf("setting CPUSeconds affected other fields: %+v", l)
	}
	l.MemoryMB = 10
	if l.CPUSeconds != 5 {
		t.Errorf("setting MemoryMB changed CPUSeconds: %+v", l)
	}
	l.OpenFiles = 20
	l.FileSizeMB = 30
	l.Enabled = true
	if l.CPUSeconds != 5 || l.MemoryMB != 10 || l.OpenFiles != 20 || l.FileSizeMB != 30 {
		t.Errorf("unexpected mutation of fields: %+v", l)
	}
}

// TestLimits_ZeroValueAllNumericFieldsAreZero verifies the complete zero-value
// state of the Limits struct — all numeric fields start at 0, Enabled starts
// false. This prevents a regression where a future field gets a non-zero default
// via a struct tag or init mechanism.
func TestLimits_ZeroValueAllNumericFieldsAreZero(t *testing.T) {
	var l Limits
	if l.CPUSeconds != 0 {
		t.Errorf("zero Limits.CPUSeconds = %d, want 0", l.CPUSeconds)
	}
	if l.MemoryMB != 0 {
		t.Errorf("zero Limits.MemoryMB = %d, want 0", l.MemoryMB)
	}
	if l.OpenFiles != 0 {
		t.Errorf("zero Limits.OpenFiles = %d, want 0", l.OpenFiles)
	}
	if l.FileSizeMB != 0 {
		t.Errorf("zero Limits.FileSizeMB = %d, want 0", l.FileSizeMB)
	}
	if l.Enabled {
		t.Errorf("zero Limits.Enabled = true, want false")
	}
}

// ---------------------------------------------------------------------------
// hasPrefix — additional edge cases
// ---------------------------------------------------------------------------

// TestHasPrefix_PrefixLongerThanString verifies that hasPrefix correctly returns
// false when the prefix string is longer than the candidate string. This is the
// `len(s) >= len(p)` guard branch.
func TestHasPrefix_PrefixLongerThanString(t *testing.T) {
	cases := []struct {
		s, p string
	}{
		{"", "--cpu="},
		{"--", "--cpu="},
		{"--c", "--cpu="},
		{"--cpu", "--cpu="},
	}
	for _, tc := range cases {
		if hasPrefix(tc.s, tc.p) {
			t.Errorf("hasPrefix(%q, %q) = true, want false (prefix longer than string)", tc.s, tc.p)
		}
	}
}

// TestHasPrefix_ExactMatchPrefixAndString verifies that hasPrefix returns true
// when the string and prefix are identical — i.e. len(s) == len(p) and all
// bytes match.
func TestHasPrefix_ExactMatchPrefixAndString(t *testing.T) {
	cases := []string{"--cpu=", "--mem=", "--nofile=", "--fsize=", "--"}
	for _, s := range cases {
		if !hasPrefix(s, s) {
			t.Errorf("hasPrefix(%q, %q) = false, want true (string == prefix)", s, s)
		}
	}
}

// TestHasPrefix_EmptyPrefixAlwaysMatches verifies that an empty prefix is a
// valid prefix of any string (including the empty string), consistent with
// standard prefix-match semantics.
func TestHasPrefix_EmptyPrefixAlwaysMatches(t *testing.T) {
	cases := []string{"", "a", "--cpu=30", "anything"}
	for _, s := range cases {
		if !hasPrefix(s, "") {
			t.Errorf("hasPrefix(%q, \"\") = false, want true (empty prefix matches all)", s)
		}
	}
}

// ---------------------------------------------------------------------------
// IsSandboxInvocation — additional edge cases
// ---------------------------------------------------------------------------

// TestIsSandboxInvocation_NilArgv verifies that a nil slice (distinct from an
// empty slice) does not panic and returns false.
func TestIsSandboxInvocation_NilArgv(t *testing.T) {
	if IsSandboxInvocation(nil) {
		t.Error("IsSandboxInvocation(nil) should return false")
	}
}

// TestIsSandboxInvocation_SentinelAtIndexZero verifies that the sentinel in
// position 0 (not position 1) is NOT treated as a sandbox invocation — the
// check is specifically argv[1] == sentinel.
func TestIsSandboxInvocation_SentinelAtIndexZero(t *testing.T) {
	// argv[0] IS the sentinel; argv[1] is something else.
	if IsSandboxInvocation([]string{"__exec-sandbox", "serve"}) {
		t.Error("sentinel at argv[0] should not trigger IsSandboxInvocation")
	}
}

// ---------------------------------------------------------------------------
// syscallEnviron — deeper format validation
// ---------------------------------------------------------------------------

// TestSyscallEnviron_KeyValueFormat verifies that every entry returned by
// syscallEnviron has a non-empty key (the portion before the first '=') and
// that the format KEY=VALUE is preserved for entries with values that contain
// additional '=' characters (common in base64-encoded values, for example).
func TestSyscallEnviron_KeyValueFormat(t *testing.T) {
	env := syscallEnviron()
	for _, e := range env {
		idx := strings.IndexByte(e, '=')
		if idx < 0 {
			t.Errorf("syscallEnviron: entry %q has no '='", e)
			continue
		}
		key := e[:idx]
		if key == "" {
			t.Errorf("syscallEnviron: entry %q has empty key", e)
		}
	}
}

// ---------------------------------------------------------------------------
// Wrap + parseSandboxArgs — structural invariants
// ---------------------------------------------------------------------------

// TestWrap_DashDashAlwaysPresentWhenEnabled verifies that every call to Wrap
// with Enabled=true and a non-empty command always includes the "--" separator
// regardless of which limits are set, so the wrapped command is always findable.
func TestWrap_DashDashAlwaysPresentWhenEnabled(t *testing.T) {
	cases := []Limits{
		{Enabled: true},
		{Enabled: true, CPUSeconds: 1},
		{Enabled: true, MemoryMB: 64},
		{Enabled: true, OpenFiles: 32},
		{Enabled: true, FileSizeMB: 8},
		{Enabled: true, CPUSeconds: 5, MemoryMB: 128, OpenFiles: 64, FileSizeMB: 16},
	}
	in := []string{"prog", "arg"}
	for _, l := range cases {
		out := Wrap("/soulacy", l, in)
		found := false
		for _, a := range out {
			if a == "--" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("'--' separator missing from Wrap output for limits %+v: %v", l, out)
		}
	}
}

// TestParseSandboxArgs_CommandWithSpacesInArgs verifies that command arguments
// containing spaces are preserved verbatim — the argument is already a Go string
// at this point, so no shell-splitting should occur.
func TestParseSandboxArgs_CommandWithSpacesInArgs(t *testing.T) {
	argv := []string{
		"soulacy", "__exec-sandbox", "--cpu=5", "--",
		"python3", "-c", "print('hello world')",
	}
	_, cmd, err := parseSandboxArgs(argv)
	if err != nil {
		t.Fatalf("parseSandboxArgs: %v", err)
	}
	want := []string{"python3", "-c", "print('hello world')"}
	if !reflect.DeepEqual(cmd, want) {
		t.Errorf("cmd = %v, want %v", cmd, want)
	}
}

// TestParseSandboxArgs_EnabledAlwaysTrueOnSuccess verifies that every successful
// parse sets Limits.Enabled to true — the parseSandboxArgs function hardcodes
// this to indicate the sandbox wrapper is active.
func TestParseSandboxArgs_EnabledAlwaysTrueOnSuccess(t *testing.T) {
	cases := [][]string{
		{"soulacy", "__exec-sandbox", "--", "cmd"},
		{"soulacy", "__exec-sandbox", "--cpu=1", "--", "cmd"},
		{"soulacy", "__exec-sandbox", "--mem=64", "--nofile=32", "--", "cmd"},
	}
	for _, argv := range cases {
		l, _, err := parseSandboxArgs(argv)
		if err != nil {
			t.Fatalf("parseSandboxArgs(%v): %v", argv, err)
		}
		if !l.Enabled {
			t.Errorf("parseSandboxArgs(%v): Enabled = false, want true", argv)
		}
	}
}

// TestWrap_OutputLengthWithNoLimits verifies that when all numeric limits are
// zero (only Enabled=true), the output is exactly [self, sentinel, "--", cmd...]
// — 3 fixed elements plus the length of the input command.
func TestWrap_OutputLengthWithNoLimits(t *testing.T) {
	in := []string{"python3", "-c", "pass"}
	out := Wrap("/soulacy", Limits{Enabled: true}, in)
	// Expected: /soulacy __exec-sandbox -- python3 -c pass = 6 elements
	want := 3 + len(in)
	if len(out) != want {
		t.Errorf("Wrap no-limits: len(out) = %d, want %d: %v", len(out), want, out)
	}
}

// TestWrap_SelfIsBinaryAtIndexZero verifies that the self argument is always
// placed at index 0 of the returned slice when sandboxing is active, regardless
// of the self string's content.
func TestWrap_SelfIsBinaryAtIndexZero(t *testing.T) {
	cases := []string{
		"/usr/local/bin/soulacy",
		"/soulacy",
		"./soulacy",
		"/very/long/path/to/the/soulacy/binary",
	}
	in := []string{"echo"}
	for _, self := range cases {
		out := Wrap(self, Limits{Enabled: true}, in)
		if len(out) == 0 || out[0] != self {
			t.Errorf("Wrap(%q): out[0] = %q, want %q", self, out[0], self)
		}
	}
}
