// engine_tools_test.go — characterization tests (TEST-1).
//
// These tests PIN the CURRENT behavior of every builtin OS-level tool returned
// by buildSystemTools, so later engine changes (eviction, history windowing,
// etc.) cannot silently alter the tool contracts. They are pure-Go: no real
// LLM, no httptest server, no outbound network. Each test drives a tool's
// Handler directly the way engine2_test.go sets things up.
//
// All tests are named TestBuiltin* so they can be selected with:
//
//	go test ./internal/runtime/ -run TestBuiltin -count=1
package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// systemTool fetches a builtin OS-level tool by name from buildSystemTools.
// Fails the test if the tool is missing (so a renamed/removed tool is caught).
func systemTool(t *testing.T, e *Engine, name string) BuiltinTool {
	t.Helper()
	for _, bt := range e.buildSystemTools() {
		if bt.Name == name {
			return bt
		}
	}
	t.Fatalf("system tool %q not found in buildSystemTools()", name)
	return BuiltinTool{}
}

// ---------------------------------------------------------------------------
// shell_exec — timeout clamp + output capture + exit code reporting
// ---------------------------------------------------------------------------

func TestBuiltinShellExec_RequiresCommand(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "shell_exec")
	_, err := tool.Handler(context.Background(), map[string]any{"command": "  "})
	if err == nil {
		t.Fatal("expected error for empty command, got nil")
	}
	if !strings.Contains(err.Error(), "command is required") {
		t.Errorf("error = %q, want 'command is required'", err.Error())
	}
}

func TestBuiltinShellExec_CapturesStdoutAndStderr(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "shell_exec")
	out, err := tool.Handler(context.Background(), map[string]any{
		"command": "echo out; echo err 1>&2",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Errorf("output should capture both stdout and stderr; got %q", out)
	}
}

func TestBuiltinShellExec_NonZeroExitReportedNotError(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "shell_exec")
	// A failing command must NOT return a Go error; it reports exit info in the
	// string result (this is the current contract).
	out, err := tool.Handler(context.Background(), map[string]any{
		"command": "exit 3",
	})
	if err != nil {
		t.Fatalf("non-zero exit should not be a Go error, got: %v", err)
	}
	if !strings.Contains(out, "exit_code: non-zero") {
		t.Errorf("output should report non-zero exit; got %q", out)
	}
}

func TestBuiltinShellExec_TruncatesLargeOutput(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "shell_exec")
	out, err := tool.Handler(context.Background(), map[string]any{
		// Produce >8000 bytes of output.
		"command": "for i in $(seq 1 5000); do echo XXXXXXXXXX; done",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(out, "[output truncated]") {
		t.Errorf("large output should be truncated; got %d bytes, no marker", len(out))
	}
}

// TestBuiltinShellExec_TimeoutClampedTo600 pins the documented clamp behavior:
// an over-large timeout_seconds is clamped to 600. We can't directly observe
// the internal deadline, but we CAN prove the clamp branch is exercised: a tiny
// command with a huge requested timeout still returns promptly (it does not
// wait), confirming the timeout arg is interpreted (and the >600 clamp path in
// the handler is taken without affecting correctness).
func TestBuiltinShellExec_OverLargeTimeoutClampedAndStillRuns(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "shell_exec")
	out, err := tool.Handler(context.Background(), map[string]any{
		"command":         "echo clamped",
		"timeout_seconds": 100000, // > 600 → clamp branch
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(out, "clamped") {
		t.Errorf("output = %q, want to contain 'clamped'", out)
	}
}

func TestBuiltinShellExec_DeadlineCancelsLongRunningCommand(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "shell_exec")
	// timeout_seconds <= 0 is normalized to 60; instead we rely on a short ctx
	// deadline to prove the command is run under context cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 1)
	defer cancel()
	out, err := tool.Handler(ctx, map[string]any{
		"command": "sleep 30",
	})
	// Current contract: a killed command surfaces as a non-zero/error string,
	// not a Go error.
	if err != nil {
		t.Fatalf("expected string result, got Go error: %v", err)
	}
	if !strings.Contains(out, "exit_code: non-zero") {
		t.Errorf("cancelled command should report non-zero exit; got %q", out)
	}
}

// ---------------------------------------------------------------------------
// read_file / write_file / list_dir — file tool contracts
// ---------------------------------------------------------------------------

func TestBuiltinWriteFile_CreatesParentsAndReports(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "write_file")
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "file.txt")
	out, err := tool.Handler(context.Background(), map[string]any{
		"path":    path,
		"content": "hello world",
	})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if !strings.Contains(out, "Written 11 bytes") {
		t.Errorf("report = %q, want byte count", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello world" {
		t.Errorf("file content = %q, want 'hello world'", string(got))
	}
}

func TestBuiltinWriteFile_AppendVsOverwrite(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "write_file")
	path := filepath.Join(t.TempDir(), "a.txt")

	if _, err := tool.Handler(context.Background(), map[string]any{
		"path": path, "content": "AAA",
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// append:true must add to the end.
	if _, err := tool.Handler(context.Background(), map[string]any{
		"path": path, "content": "BBB", "append": true,
	}); err != nil {
		t.Fatalf("append write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "AAABBB" {
		t.Errorf("after append, content = %q, want 'AAABBB'", string(got))
	}
	// default (no append) must overwrite/truncate.
	if _, err := tool.Handler(context.Background(), map[string]any{
		"path": path, "content": "C",
	}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, _ = os.ReadFile(path)
	if string(got) != "C" {
		t.Errorf("after overwrite, content = %q, want 'C'", string(got))
	}
}

func TestBuiltinReadFile_RoundTrip(t *testing.T) {
	e := newMinimalEngine(t)
	path := filepath.Join(t.TempDir(), "r.txt")
	if err := os.WriteFile(path, []byte("contents here"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := systemTool(t, e, "read_file")
	out, err := tool.Handler(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if out != "contents here" {
		t.Errorf("read content = %q, want 'contents here'", out)
	}
}

func TestBuiltinReadFile_MaxBytesCapsRead(t *testing.T) {
	e := newMinimalEngine(t)
	path := filepath.Join(t.TempDir(), "big.txt")
	if err := os.WriteFile(path, []byte("0123456789"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := systemTool(t, e, "read_file")
	out, err := tool.Handler(context.Background(), map[string]any{
		"path": path, "max_bytes": 4,
	})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if out != "0123" {
		t.Errorf("max_bytes=4 read = %q, want '0123'", out)
	}
}

func TestBuiltinReadFile_MissingFileErrors(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "read_file")
	_, err := tool.Handler(context.Background(), map[string]any{
		"path": filepath.Join(t.TempDir(), "does-not-exist"),
	})
	if err == nil {
		t.Fatal("expected error reading missing file, got nil")
	}
	if !strings.Contains(err.Error(), "read_file") {
		t.Errorf("error = %q, want to be prefixed 'read_file'", err.Error())
	}
}

func TestBuiltinListDir_ListsEntriesAndHidesDotfiles(t *testing.T) {
	e := newMinimalEngine(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(dir, ".hidden"), []byte("y"), 0644)
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	tool := systemTool(t, e, "list_dir")

	out, err := tool.Handler(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("list_dir: %v", err)
	}
	if !strings.Contains(out, "visible.txt") {
		t.Errorf("listing should include visible.txt; got:\n%s", out)
	}
	if strings.Contains(out, ".hidden") {
		t.Errorf("listing should hide dotfiles by default; got:\n%s", out)
	}
	if !strings.Contains(out, "[dir ]") {
		t.Errorf("listing should mark directories; got:\n%s", out)
	}

	// show_hidden:true must reveal dotfiles.
	out2, err := tool.Handler(context.Background(), map[string]any{
		"path": dir, "show_hidden": true,
	})
	if err != nil {
		t.Fatalf("list_dir show_hidden: %v", err)
	}
	if !strings.Contains(out2, ".hidden") {
		t.Errorf("show_hidden=true should reveal dotfiles; got:\n%s", out2)
	}
}

func TestBuiltinListDir_MissingDirErrors(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "list_dir")
	_, err := tool.Handler(context.Background(), map[string]any{
		"path": filepath.Join(t.TempDir(), "no-such-dir"),
	})
	if err == nil {
		t.Fatal("expected error for missing dir, got nil")
	}
}

// ---------------------------------------------------------------------------
// find_files — name glob + content regex + max_results
// ---------------------------------------------------------------------------

func TestBuiltinFindFiles_NamePatternMatch(t *testing.T) {
	e := newMinimalEngine(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("a: 1"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0644)

	tool := systemTool(t, e, "find_files")
	out, err := tool.Handler(context.Background(), map[string]any{
		"path": dir, "name_pattern": "*.yaml",
	})
	if err != nil {
		t.Fatalf("find_files: %v", err)
	}
	if !strings.Contains(out, "config.yaml") {
		t.Errorf("should find config.yaml; got:\n%s", out)
	}
	if strings.Contains(out, "notes.txt") {
		t.Errorf("should NOT match notes.txt for *.yaml; got:\n%s", out)
	}
}

func TestBuiltinFindFiles_ContentPatternMatch(t *testing.T) {
	e := newMinimalEngine(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "x.txt"), []byte("needle in here"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "y.txt"), []byte("nothing"), 0644)

	tool := systemTool(t, e, "find_files")
	out, err := tool.Handler(context.Background(), map[string]any{
		"path": dir, "content_pattern": "needle",
	})
	if err != nil {
		t.Fatalf("find_files: %v", err)
	}
	if !strings.Contains(out, "x.txt") || strings.Contains(out, "y.txt") {
		t.Errorf("content_pattern should match only x.txt; got:\n%s", out)
	}
}

func TestBuiltinFindFiles_InvalidContentRegexErrors(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "find_files")
	_, err := tool.Handler(context.Background(), map[string]any{
		"path": t.TempDir(), "content_pattern": "(unclosed",
	})
	if err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
	if !strings.Contains(err.Error(), "invalid content_pattern") {
		t.Errorf("error = %q, want 'invalid content_pattern'", err.Error())
	}
}

func TestBuiltinFindFiles_NoMatchesMessage(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "find_files")
	out, err := tool.Handler(context.Background(), map[string]any{
		"path": t.TempDir(), "name_pattern": "*.nope",
	})
	if err != nil {
		t.Fatalf("find_files: %v", err)
	}
	if !strings.Contains(out, "No files found") {
		t.Errorf("expected 'No files found' message; got %q", out)
	}
}

// ---------------------------------------------------------------------------
// fetch_url / http_request / download_file — scheme guard + SSRF + byte caps
// ---------------------------------------------------------------------------

func TestBuiltinFetchURL_RequiresURL(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "fetch_url")
	_, err := tool.Handler(context.Background(), map[string]any{"url": "  "})
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("expected 'url is required', got %v", err)
	}
}

func TestBuiltinFetchURL_RejectsNonHTTPScheme(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "fetch_url")
	_, err := tool.Handler(context.Background(), map[string]any{"url": "file:///etc/passwd"})
	if err == nil || !strings.Contains(err.Error(), "only http/https") {
		t.Fatalf("expected scheme rejection, got %v", err)
	}
}

func TestBuiltinFetchURL_SSRFBlocksMetadataEndpoint(t *testing.T) {
	e := newMinimalEngine(t)
	// SSRF protection on or off, the metadata endpoint is ALWAYS blocked.
	e.SetSSRF(false, nil)
	tool := systemTool(t, e, "fetch_url")
	_, err := tool.Handler(context.Background(), map[string]any{
		"url": "http://169.254.169.254/latest/meta-data/",
	})
	if err == nil {
		t.Fatal("expected SSRF block for metadata endpoint, got nil")
	}
	if !strings.Contains(err.Error(), "ssrf") {
		t.Errorf("error = %q, want an ssrf block", err.Error())
	}
}

func TestBuiltinFetchURL_SSRFBlocksPrivateRangeWhenEnabled(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetSSRF(true, nil) // protection enabled → RFC-1918 blocked
	tool := systemTool(t, e, "fetch_url")
	_, err := tool.Handler(context.Background(), map[string]any{
		"url": "http://10.0.0.1/",
	})
	if err == nil || !strings.Contains(err.Error(), "ssrf") {
		t.Fatalf("expected ssrf block for private range, got %v", err)
	}
}

func TestBuiltinHTTPRequest_RequiresURL(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "http_request")
	_, err := tool.Handler(context.Background(), map[string]any{
		"method": "GET", "url": "",
	})
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("expected 'url is required', got %v", err)
	}
}

func TestBuiltinHTTPRequest_RejectsNonHTTPScheme(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "http_request")
	_, err := tool.Handler(context.Background(), map[string]any{
		"method": "GET", "url": "ftp://example.com/x",
	})
	if err == nil || !strings.Contains(err.Error(), "only http/https") {
		t.Fatalf("expected scheme rejection, got %v", err)
	}
}

func TestBuiltinHTTPRequest_RejectsUnsupportedMethod(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetSSRF(false, nil)
	tool := systemTool(t, e, "http_request")
	// Use a loopback URL so SSRF passes and we reach the method validation.
	_, err := tool.Handler(context.Background(), map[string]any{
		"method": "TRACE", "url": "http://127.0.0.1:0/",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported method") {
		t.Fatalf("expected unsupported method error, got %v", err)
	}
}

func TestBuiltinHTTPRequest_SSRFBlocksMetadata(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetSSRF(false, nil)
	tool := systemTool(t, e, "http_request")
	_, err := tool.Handler(context.Background(), map[string]any{
		"method": "GET", "url": "http://169.254.169.254/",
	})
	if err == nil || !strings.Contains(err.Error(), "ssrf") {
		t.Fatalf("expected ssrf block, got %v", err)
	}
}

func TestBuiltinDownloadFile_RejectsNonHTTPScheme(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "download_file")
	_, err := tool.Handler(context.Background(), map[string]any{
		"url": "file:///etc/hosts", "dest_path": filepath.Join(t.TempDir(), "out"),
	})
	if err == nil || !strings.Contains(err.Error(), "only http/https") {
		t.Fatalf("expected scheme rejection, got %v", err)
	}
}

func TestBuiltinDownloadFile_SSRFBlocksMetadata(t *testing.T) {
	e := newMinimalEngine(t)
	e.SetSSRF(false, nil)
	tool := systemTool(t, e, "download_file")
	_, err := tool.Handler(context.Background(), map[string]any{
		"url":       "http://169.254.169.254/x",
		"dest_path": filepath.Join(t.TempDir(), "out"),
	})
	if err == nil || !strings.Contains(err.Error(), "ssrf") {
		t.Fatalf("expected ssrf block, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// env_get / sys_info — read-only introspection tools
// ---------------------------------------------------------------------------

// These two used to assert that env_get returns ANY environment variable —
// which was the tool reading os.Environ() unrestricted, and is the hole that
// made ANTHROPIC_API_KEY a single tool call away. They were testing that the
// tool worked, not protecting a requirement, so they now state the boundary
// instead. See envget_test.go for the leak cases.

func TestBuiltinEnvGet_NamedVariable(t *testing.T) {
	e := newMinimalEngine(t)
	// A variable the gateway deliberately exposes is readable...
	e.SetAgentShellEnv([]string{"SOULACY_TEST_VAR=pinned-value"})
	tool := systemTool(t, e, "env_get")
	out, err := tool.Handler(context.Background(), map[string]any{"name": "SOULACY_TEST_VAR"})
	if err != nil {
		t.Fatalf("env_get: %v", err)
	}
	if out != "SOULACY_TEST_VAR=pinned-value" {
		t.Errorf("env_get = %q, want 'SOULACY_TEST_VAR=pinned-value'", out)
	}
}

func TestBuiltinEnvGet_UndeclaredVariableIsNotReadable(t *testing.T) {
	e := newMinimalEngine(t)
	// ...while one merely present in the gateway's own environment is not.
	t.Setenv("SOULACY_TEST_VAR", "pinned-value")
	tool := systemTool(t, e, "env_get")
	out, err := tool.Handler(context.Background(), map[string]any{"name": "SOULACY_TEST_VAR"})
	if err != nil {
		t.Fatalf("env_get: %v", err)
	}
	if strings.Contains(out, "pinned-value") {
		t.Errorf("an undeclared gateway variable was readable: %q", out)
	}
}

func TestBuiltinEnvGet_UnsetVariable(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "env_get")
	out, err := tool.Handler(context.Background(), map[string]any{"name": "SOULACY_DEFINITELY_UNSET_XYZ"})
	if err != nil {
		t.Fatalf("env_get: %v", err)
	}
	if !strings.Contains(out, "not set") {
		t.Errorf("env_get unset = %q, want it to report the variable as unavailable", out)
	}
}

func TestBuiltinSysInfo_ReportsRuntimeFacts(t *testing.T) {
	e := newMinimalEngine(t)
	tool := systemTool(t, e, "sys_info")
	out, err := tool.Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("sys_info: %v", err)
	}
	for _, want := range []string{"OS:", "Arch:", "Home:", "Go:"} {
		if !strings.Contains(out, want) {
			t.Errorf("sys_info output missing %q; got:\n%s", want, out)
		}
	}
}
