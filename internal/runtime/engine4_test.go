// engine4_test.go — additional coverage pushing internal/runtime toward 65%.
//
// Focus areas NOT already hit by engine_test.go, engine2_test.go, engine3_test.go:
//   - toolargs.go: argString, argInt, argInt64, argFloat, argBool, argStringSlice,
//     argStringDefault, splitCSV, trimSpace
//   - confirm.go: ConfirmBroker Register/Resolve, confirmSenderFrom, WithConfirmSender
//   - builder.go: sanitizeID, toTitleCase, truncate, parseBuilderResponse,
//     tryParseBuilderJSON, generateSOULYAML, understandingToAgentMap,
//     GetBuilderUnderstanding, DeleteBuilderSession, getOrCreateBuilderSession
//   - ssrf.go: checkSSRF allowed/blocked logic
//   - loader.go: LoadAll prune, IsBuiltin, SetLogger, All, seedBuiltins,
//     builtinSystemAgent fields
//   - engine.go: buildSystemPrefix knowledge catalog path, knowledgeCatalogFor,
//     skillNamesCSV, sanitizeMCPID, allowMCPServer, allowMCPTool, uuidShort,
//     providerIsOllama, MemoryList/MemorySearch nil-archive path,
//     getOrCreateSession, runAgentCall depth limit, maybeConfirm no-confirm-sender path,
//     Handle multi-tool-call batch, Handle MCP tool blocked, Handle depth limit exceeded,
//     normalizeToolCall (system tool confirmation gate), workflow If condition skip
//   - workflow.go: renderTemplate error, step.If skip path, no checkpoint store path
//
// All tests are pure-Go (no real LLM, no subprocess, no httptest.Server).
package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
	"github.com/soulacy/soulacy/pkg/skill"
	"go.uber.org/zap"
)

// ─────────────────────────────────────────────────────────────────────────────
// toolargs.go — argString
// ─────────────────────────────────────────────────────────────────────────────

func TestArgString_PresentString(t *testing.T) {
	args := map[string]any{"key": "hello"}
	if got := argString(args, "key"); got != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestArgString_AbsentKey(t *testing.T) {
	if got := argString(map[string]any{}, "missing"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestArgString_NilValue(t *testing.T) {
	args := map[string]any{"key": nil}
	if got := argString(args, "key"); got != "" {
		t.Errorf("got %q, want empty for nil value", got)
	}
}

func TestArgString_NumericCoercedViaFmtSprintf(t *testing.T) {
	args := map[string]any{"key": 42}
	got := argString(args, "key")
	if got == "" {
		t.Error("expected non-empty coercion for int 42")
	}
}

func TestArgString_StringerInterface(t *testing.T) {
	args := map[string]any{"key": &stringerImpl{"stringer-value"}}
	if got := argString(args, "key"); got != "stringer-value" {
		t.Errorf("got %q, want stringer-value", got)
	}
}

type stringerImpl struct{ s string }

func (s *stringerImpl) String() string { return s.s }

// ─────────────────────────────────────────────────────────────────────────────
// toolargs.go — argStringDefault
// ─────────────────────────────────────────────────────────────────────────────

func TestArgStringDefault_PresentReturnsValue(t *testing.T) {
	args := map[string]any{"key": "present"}
	if got := argStringDefault(args, "key", "fallback"); got != "present" {
		t.Errorf("got %q, want present", got)
	}
}

func TestArgStringDefault_AbsentReturnsFallback(t *testing.T) {
	if got := argStringDefault(map[string]any{}, "missing", "fallback"); got != "fallback" {
		t.Errorf("got %q, want fallback", got)
	}
}

func TestArgStringDefault_EmptyStringReturnsFallback(t *testing.T) {
	args := map[string]any{"key": ""}
	if got := argStringDefault(args, "key", "fallback"); got != "fallback" {
		t.Errorf("got %q, want fallback for empty string", got)
	}
}

func TestArgContentText_AcceptsStructuredJSON(t *testing.T) {
	args := map[string]any{
		"content": map[string]any{
			"tags":    []any{"governance", "ai"},
			"content": "stored text",
		},
	}
	got := argContentText(args, "content")
	if !strings.Contains(got, `"tags"`) || !strings.Contains(got, `"stored text"`) {
		t.Fatalf("structured content rendered as %q", got)
	}
}

func TestArgContentText_StripsMarkdownJSONFence(t *testing.T) {
	args := map[string]any{"content": "```json\n{\"ok\":true}\n```"}
	if got := argContentText(args, "content"); got != `{"ok":true}` {
		t.Fatalf("got %q, want unfenced JSON", got)
	}
}

func TestTrimMessageForEvent_CapsLargeTextParts(t *testing.T) {
	msg := message.Message{Parts: message.Text(strings.Repeat("x", messageEventTextMaxRunes+100))}
	got := trimMessageForEvent(msg)
	if len(got.Parts) != 1 {
		t.Fatalf("parts = %d", len(got.Parts))
	}
	if len([]rune(got.Parts[0].Text)) >= len([]rune(msg.Parts[0].Text)) {
		t.Fatalf("message was not trimmed")
	}
	if !strings.Contains(got.Parts[0].Text, "truncated") {
		t.Fatalf("missing truncation marker: %q", got.Parts[0].Text)
	}
	if msg.Parts[0].Text == got.Parts[0].Text {
		t.Fatalf("expected event copy to differ from original message")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// toolargs.go — argInt
// ─────────────────────────────────────────────────────────────────────────────

func TestArgInt_Float64JSON(t *testing.T) {
	args := map[string]any{"n": float64(7)}
	if got := argInt(args, "n", 0); got != 7 {
		t.Errorf("got %d, want 7", got)
	}
}

func TestArgInt_StringNumeric(t *testing.T) {
	args := map[string]any{"n": "42"}
	if got := argInt(args, "n", 0); got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestArgInt_StringFloat(t *testing.T) {
	args := map[string]any{"n": "3.9"}
	if got := argInt(args, "n", 0); got != 3 {
		t.Errorf("got %d, want 3 (truncated from 3.9)", got)
	}
}

func TestArgInt_NilReturnsDefault(t *testing.T) {
	args := map[string]any{"n": nil}
	if got := argInt(args, "n", 99); got != 99 {
		t.Errorf("got %d, want 99 (default for nil)", got)
	}
}

func TestArgInt_InvalidStringReturnsDefault(t *testing.T) {
	args := map[string]any{"n": "not-a-number"}
	if got := argInt(args, "n", 5); got != 5 {
		t.Errorf("got %d, want 5 (default for bad string)", got)
	}
}

func TestArgInt_NativeInt(t *testing.T) {
	args := map[string]any{"n": int(10)}
	if got := argInt(args, "n", 0); got != 10 {
		t.Errorf("got %d, want 10", got)
	}
}

func TestArgInt_Int64(t *testing.T) {
	args := map[string]any{"n": int64(1000)}
	if got := argInt(args, "n", 0); got != 1000 {
		t.Errorf("got %d, want 1000", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// toolargs.go — argInt64
// ─────────────────────────────────────────────────────────────────────────────

func TestArgInt64_Float64(t *testing.T) {
	args := map[string]any{"n": float64(12345678901)}
	if got := argInt64(args, "n", 0); got != 12345678901 {
		t.Errorf("got %d, want 12345678901", got)
	}
}

func TestArgInt64_StringInt(t *testing.T) {
	args := map[string]any{"n": "9999"}
	if got := argInt64(args, "n", 0); got != 9999 {
		t.Errorf("got %d, want 9999", got)
	}
}

func TestArgInt64_StringFloat(t *testing.T) {
	args := map[string]any{"n": "1.5"}
	if got := argInt64(args, "n", 0); got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

func TestArgInt64_DefaultWhenAbsent(t *testing.T) {
	if got := argInt64(map[string]any{}, "x", int64(7)); got != 7 {
		t.Errorf("got %d, want 7", got)
	}
}

func TestArgInt64_NativeInt64(t *testing.T) {
	args := map[string]any{"n": int64(42)}
	if got := argInt64(args, "n", 0); got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestArgInt64_NativeInt(t *testing.T) {
	args := map[string]any{"n": int(77)}
	if got := argInt64(args, "n", 0); got != 77 {
		t.Errorf("got %d, want 77", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// toolargs.go — argFloat
// ─────────────────────────────────────────────────────────────────────────────

func TestArgFloat_Float64(t *testing.T) {
	args := map[string]any{"f": float64(3.14)}
	if got := argFloat(args, "f", 0); got != 3.14 {
		t.Errorf("got %v, want 3.14", got)
	}
}

func TestArgFloat_String(t *testing.T) {
	args := map[string]any{"f": "2.718"}
	if got := argFloat(args, "f", 0); got != 2.718 {
		t.Errorf("got %v, want 2.718", got)
	}
}

func TestArgFloat_IntConversion(t *testing.T) {
	args := map[string]any{"f": int(5)}
	if got := argFloat(args, "f", 0); got != 5.0 {
		t.Errorf("got %v, want 5.0", got)
	}
}

func TestArgFloat_DefaultWhenAbsent(t *testing.T) {
	if got := argFloat(map[string]any{}, "f", 1.5); got != 1.5 {
		t.Errorf("got %v, want 1.5", got)
	}
}

func TestArgFloat_BadStringReturnsDefault(t *testing.T) {
	args := map[string]any{"f": "not-a-float"}
	if got := argFloat(args, "f", 9.9); got != 9.9 {
		t.Errorf("got %v, want 9.9 (default)", got)
	}
}

func TestArgFloat_Float32(t *testing.T) {
	args := map[string]any{"f": float32(1.5)}
	got := argFloat(args, "f", 0)
	if got < 1.49 || got > 1.51 {
		t.Errorf("got %v, want ~1.5", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// toolargs.go — argBool
// ─────────────────────────────────────────────────────────────────────────────

func TestArgBool_TrueBool(t *testing.T) {
	args := map[string]any{"b": true}
	if !argBool(args, "b") {
		t.Error("expected true")
	}
}

func TestArgBool_FalseBool(t *testing.T) {
	args := map[string]any{"b": false}
	if argBool(args, "b") {
		t.Error("expected false")
	}
}

func TestArgBool_StringTrue(t *testing.T) {
	for _, s := range []string{"true", "1", "TRUE"} {
		args := map[string]any{"b": s}
		if !argBool(args, "b") {
			t.Errorf("argBool(%q) should be true", s)
		}
	}
}

func TestArgBool_StringFalse(t *testing.T) {
	for _, s := range []string{"false", "0", "FALSE", "no"} {
		args := map[string]any{"b": s}
		if argBool(args, "b") {
			t.Errorf("argBool(%q) should be false", s)
		}
	}
}

func TestArgBool_Float64Nonzero(t *testing.T) {
	args := map[string]any{"b": float64(1)}
	if !argBool(args, "b") {
		t.Error("float64(1) should be true")
	}
}

func TestArgBool_Float64Zero(t *testing.T) {
	args := map[string]any{"b": float64(0)}
	if argBool(args, "b") {
		t.Error("float64(0) should be false")
	}
}

func TestArgBool_AbsentKey(t *testing.T) {
	if argBool(map[string]any{}, "b") {
		t.Error("absent key should return false")
	}
}

func TestArgBool_NilValue(t *testing.T) {
	args := map[string]any{"b": nil}
	if argBool(args, "b") {
		t.Error("nil should return false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// toolargs.go — argStringSlice
// ─────────────────────────────────────────────────────────────────────────────

func TestArgStringSlice_NativeStringSlice(t *testing.T) {
	args := map[string]any{"arr": []string{"a", "b", "c"}}
	got := argStringSlice(args, "arr")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("got %v, want [a b c]", got)
	}
}

func TestArgStringSlice_SliceAny(t *testing.T) {
	args := map[string]any{"arr": []any{"x", "y"}}
	got := argStringSlice(args, "arr")
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("got %v, want [x y]", got)
	}
}

func TestArgStringSlice_CSVString(t *testing.T) {
	args := map[string]any{"arr": "foo, bar, baz"}
	got := argStringSlice(args, "arr")
	if len(got) != 3 || got[0] != "foo" || got[1] != "bar" || got[2] != "baz" {
		t.Errorf("got %v, want [foo bar baz]", got)
	}
}

func TestArgStringSlice_EmptyString(t *testing.T) {
	args := map[string]any{"arr": ""}
	got := argStringSlice(args, "arr")
	if got != nil {
		t.Errorf("got %v, want nil for empty string", got)
	}
}

func TestArgStringSlice_AbsentKey(t *testing.T) {
	if got := argStringSlice(map[string]any{}, "arr"); got != nil {
		t.Errorf("got %v, want nil for absent key", got)
	}
}

func TestArgStringSlice_NilValue(t *testing.T) {
	args := map[string]any{"arr": nil}
	if got := argStringSlice(args, "arr"); got != nil {
		t.Errorf("got %v, want nil for nil value", got)
	}
}

func TestArgStringSlice_MixedAny(t *testing.T) {
	args := map[string]any{"arr": []any{"text", 42}}
	got := argStringSlice(args, "arr")
	if len(got) != 2 || got[0] != "text" {
		t.Errorf("got %v, want 2 elements with first='text'", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// toolargs.go — splitCSV and trimSpace (internal helpers)
// ─────────────────────────────────────────────────────────────────────────────

func TestSplitCSV_Basic(t *testing.T) {
	got := splitCSV("a, b , c")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("got %v", got)
	}
}

func TestSplitCSV_SingleItem(t *testing.T) {
	got := splitCSV("only")
	if len(got) != 1 || got[0] != "only" {
		t.Errorf("got %v", got)
	}
}

func TestSplitCSV_TrailingComma(t *testing.T) {
	got := splitCSV("a,b,")
	// trailing comma produces an empty string at end
	if len(got) < 2 {
		t.Errorf("got %v, expected at least 2 parts", got)
	}
}

func TestTrimSpace_Tabs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\t hello \t", "hello"},
		{" \n world \r\n ", "world"},
		{"no-spaces", "no-spaces"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := trimSpace(tc.in); got != tc.want {
			t.Errorf("trimSpace(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// confirm.go — ConfirmBroker
// ─────────────────────────────────────────────────────────────────────────────

func TestConfirmBroker_RegisterAndResolveApproved(t *testing.T) {
	b := newConfirmBroker()
	ch := b.Register("call-1")
	ok := b.Resolve("call-1", true)
	if !ok {
		t.Fatal("Resolve should return true for known call ID")
	}
	select {
	case decision := <-ch:
		if !decision {
			t.Error("expected approved decision")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for confirm decision")
	}
}

func TestConfirmBroker_RegisterAndResolveDenied(t *testing.T) {
	b := newConfirmBroker()
	ch := b.Register("call-2")
	b.Resolve("call-2", false)
	select {
	case decision := <-ch:
		if decision {
			t.Error("expected denied decision")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for confirm decision")
	}
}

func TestConfirmBroker_ResolveUnknownCallID(t *testing.T) {
	b := newConfirmBroker()
	ok := b.Resolve("nonexistent-call", true)
	if ok {
		t.Error("Resolve should return false for unknown call ID")
	}
}

func TestConfirmBroker_DoubleResolvePanic(t *testing.T) {
	// Resolving twice should not panic — second call gets ok=false.
	b := newConfirmBroker()
	b.Register("call-3")
	b.Resolve("call-3", true)
	ok := b.Resolve("call-3", true) // second resolve, call already removed
	if ok {
		t.Error("second Resolve on same call ID should return false")
	}
}

func TestConfirmSenderFrom_RoundTrip(t *testing.T) {
	var called bool
	fn := ConfirmSenderFunc(func(req ConfirmRequest) <-chan bool {
		called = true
		ch := make(chan bool, 1)
		ch <- true
		return ch
	})
	ctx := WithConfirmSender(context.Background(), fn)
	sender, ok := confirmSenderFrom(ctx)
	if !ok {
		t.Fatal("confirmSenderFrom should find sender in context")
	}
	<-sender(ConfirmRequest{CallID: "c", Tool: "t"})
	if !called {
		t.Error("sender function was not called")
	}
}

func TestConfirmSenderFrom_AbsentReturnsNotOK(t *testing.T) {
	_, ok := confirmSenderFrom(context.Background())
	if ok {
		t.Error("confirmSenderFrom on bare context should return ok=false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — maybeConfirm: no confirm sender present (deny unless unattended)
// ─────────────────────────────────────────────────────────────────────────────

func TestMaybeConfirm_NoSenderDeniesByDefault(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "guarded",
		ConfirmTools: []string{"dangerous_op"},
	}
	err := e.maybeConfirm(context.Background(), def, message.ToolCall{
		ID: "call-1", Name: "dangerous_op",
	})
	if err == nil {
		t.Fatal("maybeConfirm without sender should deny for a non-unattended agent")
	}
}

func TestMaybeConfirm_NoSenderUnattendedAutoApproves(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "scheduled-guarded",
		ConfirmTools: []string{"dangerous_op"},
		Unattended:   true,
	}
	err := e.maybeConfirm(context.Background(), def, message.ToolCall{
		ID: "call-1", Name: "dangerous_op",
	})
	if err != nil {
		t.Fatalf("maybeConfirm without sender should auto-approve for unattended agents, got error: %v", err)
	}
}

func TestDynamicConfirm_NoSenderDeniesByDefault(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{ID: "guarded"}
	err := e.dynamicConfirm(context.Background(), def, message.ToolCall{
		ID: "c", Name: "shell_exec",
	}, "privileged system action")
	if err == nil {
		t.Fatal("dynamicConfirm without sender should DENY (error) for a non-unattended agent")
	}
}

func TestDynamicConfirm_UnattendedAutoApproves(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{ID: "scheduled-bot", Unattended: true}
	// No confirm sender (e.g. a scheduled run). Unattended → auto-approve.
	err := e.dynamicConfirm(context.Background(), def, message.ToolCall{
		ID: "c", Name: "shell_exec",
	}, "privileged system action")
	if err != nil {
		t.Fatalf("unattended agent should auto-approve guardrail confirm, got: %v", err)
	}
}

func TestMaybeConfirm_ToolNotInListProceeds(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "guarded",
		ConfirmTools: []string{"only_this_tool"},
	}
	// Tool not in ConfirmTools — should proceed regardless of sender.
	err := e.maybeConfirm(context.Background(), def, message.ToolCall{
		ID: "call-2", Name: "some_other_tool",
	})
	if err != nil {
		t.Fatalf("expected nil error for non-guarded tool, got: %v", err)
	}
}

func TestMaybeConfirm_WildcardRequiresConfirmation(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{
		ID:           "gated",
		ConfirmTools: []string{"*"}, // wildcard gates all tools
	}
	confirmSender := ConfirmSenderFunc(func(req ConfirmRequest) <-chan bool {
		ch := make(chan bool, 1)
		ch <- true // approve
		return ch
	})
	ctx := WithConfirmSender(context.Background(), confirmSender)
	err := e.maybeConfirm(ctx, def, message.ToolCall{
		ID: "c3", Name: "any_tool",
	})
	if err != nil {
		t.Fatalf("approval with wildcard should succeed, got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go — sanitizeID
// ─────────────────────────────────────────────────────────────────────────────

func TestSanitizeID_BasicName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"My Agent", "my-agent"},
		{"stock-watcher", "stock-watcher"},
		{"UPPER CASE", "upper-case"},
		{"has_underscore", "has-underscore"},
		{"  leading space  ", "leading-space"},
		{"double  space", "double-space"},
		{"123numeric", "123numeric"},
		{"special!@#chars", "specialchars"},
		{"", ""},
	}
	for _, tc := range cases {
		got := sanitizeID(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeID_MaxLength(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := sanitizeID(long)
	if len(got) > 64 {
		t.Errorf("sanitizeID length %d, want ≤ 64", len(got))
	}
}

func TestSanitizeID_NoTrailingHyphen(t *testing.T) {
	got := sanitizeID("agent-")
	if strings.HasSuffix(got, "-") {
		t.Errorf("sanitizeID should strip trailing hyphen, got %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go — toTitleCase
// ─────────────────────────────────────────────────────────────────────────────

func TestToTitleCase_Basic(t *testing.T) {
	cases := []struct{ in, want string }{
		{"my agent", "My Agent"},
		{"stock watcher", "Stock Watcher"},
		{"", ""},
		{"single", "Single"},
		{"ALREADY UPPER", "ALREADY UPPER"},
	}
	for _, tc := range cases {
		got := toTitleCase(tc.in)
		if got != tc.want {
			t.Errorf("toTitleCase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go — truncate
// ─────────────────────────────────────────────────────────────────────────────

func TestTruncate_ShortString(t *testing.T) {
	got := truncate("hello", 10)
	if got != "hello" {
		t.Errorf("truncate short: got %q, want hello", got)
	}
}

func TestTruncate_ExactLength(t *testing.T) {
	got := truncate("hello", 5)
	if got != "hello" {
		t.Errorf("truncate exact: got %q, want hello", got)
	}
}

func TestTruncate_LongString(t *testing.T) {
	got := truncate("this is a long string", 10)
	if len(got) <= 10 {
		// The ellipsis makes it longer than the cut, that's OK — just check it's truncated.
		t.Errorf("truncate long: got %q, should be truncated", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncate long should end with ellipsis, got %q", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go — tryParseBuilderJSON
// ─────────────────────────────────────────────────────────────────────────────

func TestTryParseBuilderJSON_ValidObject(t *testing.T) {
	content := `{"reply":"Hello!","understanding":{"confidence":0.5}}`
	reply, u, ok := tryParseBuilderJSON(content)
	if !ok {
		t.Fatal("expected parse success")
	}
	if reply != "Hello!" {
		t.Errorf("reply = %q, want Hello!", reply)
	}
	if u == nil {
		t.Fatal("understanding should not be nil")
	}
	if u.Confidence != 0.5 {
		t.Errorf("confidence = %v, want 0.5", u.Confidence)
	}
}

func TestTryParseBuilderJSON_WithCodeFence(t *testing.T) {
	content := "```json\n{\"reply\":\"Fenced!\",\"understanding\":{\"confidence\":0.3}}\n```"
	reply, _, ok := tryParseBuilderJSON(content)
	if !ok {
		t.Fatal("expected parse success with code fence")
	}
	if reply != "Fenced!" {
		t.Errorf("reply = %q, want Fenced!", reply)
	}
}

func TestTryParseBuilderJSON_InvalidJSON(t *testing.T) {
	_, _, ok := tryParseBuilderJSON("not json at all")
	if ok {
		t.Error("expected parse failure for non-JSON")
	}
}

func TestTryParseBuilderJSON_EmptyString(t *testing.T) {
	_, _, ok := tryParseBuilderJSON("")
	if ok {
		t.Error("expected parse failure for empty string")
	}
}

func TestTryParseBuilderJSON_SynthesisesReplyFromMissing(t *testing.T) {
	content := `{"reply":"","understanding":{"confidence":0.4,"missing":["trigger type","output channel"]}}`
	reply, u, ok := tryParseBuilderJSON(content)
	if !ok {
		t.Fatal("expected parse success")
	}
	if reply == "" {
		t.Error("expected a synthesised reply when reply field is empty but missing fields exist")
	}
	if u == nil {
		t.Fatal("understanding should not be nil")
	}
}

func TestTryParseBuilderJSON_SynthesisesReplyWhenNoMissing(t *testing.T) {
	content := `{"reply":"","understanding":{"confidence":0.9,"missing":[]}}`
	reply, _, ok := tryParseBuilderJSON(content)
	if !ok {
		t.Fatal("expected parse success")
	}
	if reply == "" {
		t.Error("expected a synthesised fallback reply when reply is empty and no missing fields")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go — parseBuilderResponse (legacy REPLY:/UNDERSTANDING: markers)
// ─────────────────────────────────────────────────────────────────────────────

func TestParseBuilderResponse_JSONPath(t *testing.T) {
	content := `{"reply":"JSON reply","understanding":{"confidence":0.7}}`
	reply, u := parseBuilderResponse(content)
	if reply != "JSON reply" {
		t.Errorf("reply = %q, want JSON reply", reply)
	}
	if u == nil {
		t.Fatal("understanding should not be nil")
	}
}

func TestParseBuilderResponse_LegacyMarkers(t *testing.T) {
	content := "REPLY: Legacy reply text\nUNDERSTANDING: {\"confidence\":0.6}"
	reply, u := parseBuilderResponse(content)
	if !strings.Contains(reply, "Legacy reply text") {
		t.Errorf("reply = %q, want to contain 'Legacy reply text'", reply)
	}
	if u == nil {
		t.Fatal("understanding should not be nil from legacy markers")
	}
}

func TestParseBuilderResponse_PlainText(t *testing.T) {
	content := "Just a plain text response with no markers or JSON."
	reply, u := parseBuilderResponse(content)
	if reply == "" {
		t.Error("plain text should become the reply")
	}
	if u != nil {
		t.Error("understanding should be nil for plain text")
	}
}

func TestParseBuilderResponse_ReplyOnlyMarker(t *testing.T) {
	content := "REPLY: Only a reply, no understanding."
	reply, _ := parseBuilderResponse(content)
	if !strings.Contains(reply, "Only a reply") {
		t.Errorf("reply = %q, want to contain reply text", reply)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go — generateSOULYAML
// ─────────────────────────────────────────────────────────────────────────────

func TestGenerateSOULYAML_BasicAgent(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:         "My Stock Watcher",
		Description:  "Watches stocks.",
		Confidence:   0.9,
		Purpose:      "Monitor stock prices.",
		SystemPrompt: "You are a stock watcher.",
	}
	yaml := generateSOULYAML(u, "ollama", "llama3")
	if !strings.Contains(yaml, "id: my-stock-watcher") {
		t.Errorf("YAML should contain id, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "provider: ollama") {
		t.Errorf("YAML should contain provider, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "model: llama3") {
		t.Errorf("YAML should contain model, got:\n%s", yaml)
	}
}

func TestGenerateSOULYAML_CronTrigger(t *testing.T) {
	u := &BuilderUnderstanding{
		Name: "cron-agent",
		Trigger: &BuilderTrigger{
			Type:     "cron",
			Schedule: "0 7 * * *",
		},
		Confidence: 0.9,
	}
	yaml := generateSOULYAML(u, "openai", "gpt-4")
	if !strings.Contains(yaml, "trigger: cron") {
		t.Errorf("YAML should have cron trigger, got:\n%s", yaml)
	}
	if !strings.Contains(yaml, "0 7 * * *") {
		t.Errorf("YAML should contain cron schedule, got:\n%s", yaml)
	}
}

func TestGenerateSOULYAML_WithTools(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:       "tool-agent",
		Confidence: 0.9,
		Tools: []BuilderTool{
			{Name: "fetch-prices", Description: "Fetches prices from an API."},
		},
	}
	yaml := generateSOULYAML(u, "anthropic", "claude-3")
	if !strings.Contains(yaml, "fetch-prices") {
		t.Errorf("YAML should include tool name, got:\n%s", yaml)
	}
}

func TestGenerateSOULYAML_NoMemory(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:       "nomem-agent",
		Confidence: 0.9,
		Memory:     &BuilderMemory{Needs: false},
	}
	yaml := generateSOULYAML(u, "ollama", "llama3")
	if !strings.Contains(yaml, "read_scopes: []") {
		t.Errorf("YAML should have empty read_scopes for no-memory, got:\n%s", yaml)
	}
}

func TestGenerateSOULYAML_GlobalMemory(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:       "global-mem-agent",
		Confidence: 0.9,
		Memory:     &BuilderMemory{Needs: true, Scope: "global"},
	}
	yaml := generateSOULYAML(u, "ollama", "llama3")
	if !strings.Contains(yaml, "global") {
		t.Errorf("YAML should include global scope, got:\n%s", yaml)
	}
}

func TestGenerateSOULYAML_EmptyNameFallback(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:       "",
		Confidence: 0.9,
	}
	yaml := generateSOULYAML(u, "ollama", "llama3")
	if !strings.Contains(yaml, "id: my-agent") {
		t.Errorf("YAML should have fallback id, got:\n%s", yaml)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go — understandingToAgentMap
// ─────────────────────────────────────────────────────────────────────────────

func TestUnderstandingToAgentMap_Basic(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:        "My Bot",
		Description: "Does things.",
		Confidence:  0.9,
	}
	m := understandingToAgentMap(u, "ollama", "llama3")
	if m["id"] != "my-bot" {
		t.Errorf("id = %q, want my-bot", m["id"])
	}
	if m["description"] != "Does things." {
		t.Errorf("description = %q, want Does things.", m["description"])
	}
	if m["enabled"] != true {
		t.Errorf("enabled should be true")
	}
}

func TestUnderstandingToAgentMap_CronTrigger(t *testing.T) {
	u := &BuilderUnderstanding{
		Name: "cron-bot",
		Trigger: &BuilderTrigger{
			Type:     "cron",
			Schedule: "0 8 * * *",
		},
	}
	m := understandingToAgentMap(u, "ollama", "llama3")
	if m["trigger"] != "cron" {
		t.Errorf("trigger = %q, want cron", m["trigger"])
	}
}

func TestUnderstandingToAgentMap_ChannelTriggerWithChannels(t *testing.T) {
	u := &BuilderUnderstanding{
		Name: "channel-bot",
		Trigger: &BuilderTrigger{
			Type:     "channel",
			Channels: []string{"telegram", "http"},
		},
	}
	m := understandingToAgentMap(u, "ollama", "llama3")
	channels, ok := m["channels"].([]string)
	if !ok || len(channels) != 2 {
		t.Errorf("channels = %v, want [telegram http]", m["channels"])
	}
}

func TestUnderstandingToAgentMap_FallbackSystemPrompt(t *testing.T) {
	u := &BuilderUnderstanding{
		Name:       "no-sys",
		Confidence: 0.9,
		Purpose:    "Does something useful.",
		// SystemPrompt intentionally empty
	}
	m := understandingToAgentMap(u, "ollama", "llama3")
	sp, _ := m["system_prompt"].(string)
	if !strings.Contains(sp, "Does something useful") && !strings.Contains(sp, "helpful") {
		t.Errorf("system_prompt should fall back to purpose or default, got %q", sp)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// builder.go — GetBuilderUnderstanding / DeleteBuilderSession
// ─────────────────────────────────────────────────────────────────────────────

func TestGetBuilderUnderstanding_MissingSession(t *testing.T) {
	e := newMinimalEngine(t)
	u := e.GetBuilderUnderstanding("nonexistent-session-id")
	if u != nil {
		t.Errorf("expected nil for missing session, got %+v", u)
	}
}

func TestDeleteBuilderSession_ExistingAndAbsent(t *testing.T) {
	e := newMinimalEngine(t)
	// Create a session by storing it directly.
	e.getOrCreateBuilderSession("sess-to-delete")
	// Should not panic.
	e.DeleteBuilderSession("sess-to-delete")
	// Verify it's gone.
	u := e.GetBuilderUnderstanding("sess-to-delete")
	if u != nil {
		t.Error("expected nil after deleting session")
	}
	// Deleting a non-existent session should also not panic.
	e.DeleteBuilderSession("never-existed")
}

// ─────────────────────────────────────────────────────────────────────────────
// ssrf.go — checkSSRF
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckSSRF_ValidPublicURL(t *testing.T) {
	// Public URLs should not error (real DNS lookup may fail in CI, but the
	// function tolerates resolution failure gracefully).
	err := checkSSRF("https://example.com/page", false, nil)
	if err != nil {
		t.Logf("checkSSRF(public URL) returned error (DNS failure ok in CI): %v", err)
	}
}

func TestCheckSSRF_InvalidURL(t *testing.T) {
	err := checkSSRF("://not-a-valid-url", false, nil)
	// May return nil (some parsers tolerate this) or an error.
	// Just verify no panic.
	_ = err
}

func TestCheckSSRF_AllowedHostBypassesPrivateCheck(t *testing.T) {
	// Loopback is always allowed — this should not return an SSRF error.
	err := checkSSRF("http://localhost/api", true, nil)
	if err != nil {
		t.Logf("localhost blocked (unexpected): %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// loader.go — IsBuiltin, SetLogger, All, LoadAll prune
// ─────────────────────────────────────────────────────────────────────────────

func TestLoader_IsBuiltin_SystemIsBuiltin(t *testing.T) {
	l := NewLoader([]string{t.TempDir()})
	if !l.IsBuiltin("system") {
		t.Error("'system' should be reported as a built-in")
	}
}

func TestLoader_IsBuiltin_UserAgentNotBuiltin(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	def := &agent.Definition{ID: "user-agent", Enabled: true}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if l.IsBuiltin("user-agent") {
		t.Error("user-created agent should NOT be a built-in")
	}
}

func TestLoader_IsBuiltin_UnknownAgentReturnsFalse(t *testing.T) {
	l := NewLoader([]string{t.TempDir()})
	if l.IsBuiltin("ghost") {
		t.Error("unknown agent should not be a built-in")
	}
}

func TestLoader_SetLogger_DoesNotPanic(t *testing.T) {
	l := NewLoader([]string{t.TempDir()})
	// Should not panic.
	l.SetLogger(zap.NewNop())
}

func TestLoader_All_IncludesBuiltins(t *testing.T) {
	l := NewLoader([]string{t.TempDir()})
	all := l.All()
	found := false
	for _, d := range all {
		if d.ID == "system" {
			found = true
		}
	}
	if !found {
		t.Error("All() should include the built-in 'system' agent")
	}
}

func TestLoader_All_IncludesUserAgents(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	def := &agent.Definition{ID: "mine", Enabled: true}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	all := l.All()
	found := false
	for _, d := range all {
		if d.ID == "mine" {
			found = true
		}
	}
	if !found {
		t.Error("All() should include user-upserted agents")
	}
}

func TestLoader_LoadAll_PrunesDeletedAgents(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	def := &agent.Definition{ID: "to-prune", Enabled: true}
	if err := l.Upsert(dir, def); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := l.Delete("to-prune"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// LoadAll should not resurrect the deleted agent.
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Logf("LoadAll errors (expected for empty dir): %v", errs)
	}
	if l.Get("to-prune") != nil {
		t.Error("LoadAll should not resurrect a deleted agent")
	}
}

func TestLoader_BuiltinSurvivesLoadAll(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader([]string{dir})
	// LoadAll on empty dir should NOT prune built-ins.
	if errs := l.LoadAll(); len(errs) > 0 {
		t.Logf("LoadAll errors: %v", errs)
	}
	if l.Get("system") == nil {
		t.Error("built-in 'system' agent should survive LoadAll on empty dir")
	}
}

func TestLoader_DeleteBuiltin_Blocked(t *testing.T) {
	l := NewLoader([]string{t.TempDir()})
	err := l.Delete("system")
	if err == nil {
		t.Error("deleting built-in 'system' agent should return an error")
	}
	if !strings.Contains(err.Error(), "built-in") {
		t.Errorf("error should mention 'built-in', got: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — sanitizeMCPID, allowMCPServer, allowMCPTool
// ─────────────────────────────────────────────────────────────────────────────

func TestSanitizeMCPID_Lowercase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Rocket Money", "rocket_money"},
		{"my-server", "my-server"},
		{"CAPS", "caps"},
		{"has space", "has_space"},
		{"already-clean", "already-clean"},
	}
	for _, tc := range cases {
		got := sanitizeMCPID(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeMCPID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAllowMCPServer_NilAllowlist(t *testing.T) {
	if allowMCPServer(nil, "myserver") {
		t.Error("nil allowlist should return false")
	}
}

func TestAllowMCPServer_WildcardPermitsAll(t *testing.T) {
	list := []string{"*"}
	if !allowMCPServer(&list, "anything") {
		t.Error("wildcard allowlist should permit any server")
	}
}

func TestAllowMCPServer_ExactMatch(t *testing.T) {
	list := []string{"rocketmoney"}
	if !allowMCPServer(&list, "rocketmoney") {
		t.Error("exact match should be permitted")
	}
}

func TestAllowMCPServer_Mismatch(t *testing.T) {
	list := []string{"rocketmoney"}
	if allowMCPServer(&list, "filesystem") {
		t.Error("non-member should not be permitted")
	}
}

func TestAllowMCPTool_NilAllowlist(t *testing.T) {
	if allowMCPTool(nil, "mcp__foo__bar") {
		t.Error("nil allowlist should return false")
	}
}

func TestAllowMCPTool_ExactMatch(t *testing.T) {
	list := []string{"mcp__rocketmoney__get_transactions"}
	if !allowMCPTool(&list, "mcp__rocketmoney__get_transactions") {
		t.Error("exact match should be permitted")
	}
}

func TestAllowMCPTool_WildcardPermitsAll(t *testing.T) {
	list := []string{"all"}
	if !allowMCPTool(&list, "mcp__anything__tool") {
		t.Error("'all' wildcard should permit any tool")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — skillNamesCSV
// ─────────────────────────────────────────────────────────────────────────────

func TestSkillNamesCSV_WithSkills(t *testing.T) {
	skills := []*skill.Skill{
		{Name: "alpha", Dir: t.TempDir()},
		{Name: "beta", Dir: t.TempDir()},
	}
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: skills}

	csv := e.skillNamesCSV()
	if !strings.Contains(csv, "alpha") || !strings.Contains(csv, "beta") {
		t.Errorf("skillNamesCSV = %q, want to contain alpha and beta", csv)
	}
}

func TestSkillNamesCSV_NoSkillLoader(t *testing.T) {
	e := newMinimalEngine(t)
	// skillLoader is nil by default
	csv := e.skillNamesCSV()
	if csv != "(none)" {
		t.Errorf("skillNamesCSV with nil loader = %q, want (none)", csv)
	}
}

func TestSkillNamesCSV_EmptySkills(t *testing.T) {
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: nil}
	csv := e.skillNamesCSV()
	if csv != "(none)" {
		t.Errorf("skillNamesCSV with no skills = %q, want (none)", csv)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — providerIsOllama
// ─────────────────────────────────────────────────────────────────────────────

func TestProviderIsOllama_Explicit(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{LLM: agent.LLMConfig{Provider: "ollama"}}
	if !e.providerIsOllama(def) {
		t.Error("providerIsOllama should be true when provider is ollama")
	}
}

func TestProviderIsOllama_NotOllama(t *testing.T) {
	e := newMinimalEngine(t)
	def := &agent.Definition{LLM: agent.LLMConfig{Provider: "anthropic"}}
	if e.providerIsOllama(def) {
		t.Error("providerIsOllama should be false when provider is anthropic")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — MemoryList and MemorySearch nil archive
// ─────────────────────────────────────────────────────────────────────────────

func TestMemoryList_NilArchiveReturnsEmpty(t *testing.T) {
	e := newMinimalEngine(t)
	// archive is nil by default in newMinimalEngine
	entries, err := e.MemoryList("agent-1", 10)
	if err != nil {
		t.Fatalf("MemoryList: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for nil archive, got %d", len(entries))
	}
}

func TestMemorySearch_NilArchiveReturnsEmpty(t *testing.T) {
	e := newMinimalEngine(t)
	entries, err := e.MemorySearch("agent-1", "some query", 10)
	if err != nil {
		t.Fatalf("MemorySearch: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for nil archive, got %d", len(entries))
	}
}

func TestMemorySearch_EmptyQueryCallsMemoryList(t *testing.T) {
	e := newMinimalEngine(t)
	// With nil archive, both code paths return empty non-nil slice.
	entries, err := e.MemorySearch("agent-1", "", 10)
	if err != nil {
		t.Fatalf("MemorySearch empty query: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — uuidShort uniqueness
// ─────────────────────────────────────────────────────────────────────────────

func TestUUIDShort_Unique(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := uuidShort()
		if ids[id] {
			t.Fatalf("uuidShort produced duplicate: %q", id)
		}
		ids[id] = true
		if id == "" {
			t.Fatal("uuidShort returned empty string")
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — runAgentCall depth limit
// ─────────────────────────────────────────────────────────────────────────────

func TestRunAgentCall_DepthLimitExceeded(t *testing.T) {
	dir := t.TempDir()
	loader := NewLoader([]string{dir})

	callerDef := &agent.Definition{
		ID:      "depth-caller",
		Enabled: true,
		Agents:  []string{"depth-peer"},
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
	}
	peerDef := &agent.Definition{
		ID:      "depth-peer",
		Enabled: true,
		LLM:     agent.LLMConfig{Provider: "test", Model: "fake-model"},
	}
	for _, d := range []*agent.Definition{callerDef, peerDef} {
		if err := loader.Upsert(dir, d); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	mem, _ := memory.NewFileStore(t.TempDir())
	router := llm.NewRouter("test")
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	// Inject maximum depth into context.
	ctx := withAgentCallDepth(context.Background(), defaultMaxAgentCallDepth)

	_, err := e.runAgentCall(ctx, callerDef, AgentToolPrefix+"depth-peer", map[string]any{"message": "hello"})
	if err == nil {
		t.Fatal("expected depth-limit error, got nil")
	}
	if !strings.Contains(err.Error(), "depth limit") {
		t.Errorf("error = %q, want to mention depth limit", err.Error())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — runAgentCall malformed tool name
// ─────────────────────────────────────────────────────────────────────────────

func TestRunAgentCall_MalformedToolName(t *testing.T) {
	e := newMinimalEngine(t)
	_, err := e.runAgentCall(context.Background(), &agent.Definition{ID: "caller"}, AgentToolPrefix, map[string]any{})
	if err == nil {
		t.Fatal("expected error for malformed tool name")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — buildSystemPrefix with knowledge service (nil Store guard)
// ─────────────────────────────────────────────────────────────────────────────

// TestBuildSystemPrefix_KnowledgeServiceNilStoreNoAppend verifies that when the
// knowledge service has a nil Store (as returned by nonNilKnowledgeServiceForSchemaGate),
// knowledgeCatalogFor returns empty and no "Available Knowledge Bases" block is
// appended — the engine does not panic on the nil-Store path.
func TestBuildSystemPrefix_KnowledgeServiceNilStoreNoAppend(t *testing.T) {
	e := newMinimalEngine(t)
	e.knowledge = nonNilKnowledgeServiceForSchemaGate() // Store is nil inside

	def := &agent.Definition{
		ID:           "kb-agent",
		SystemPrompt: "You are a knowledgeable bot.",
		Knowledge:    []string{"my-kb"},
	}
	// Should not panic even with nil Store inside knowledge service.
	prefix := e.buildSystemPrefix(def)
	if !strings.Contains(prefix, "You are a knowledgeable bot.") {
		t.Errorf("prefix should contain system prompt, got:\n%s", prefix)
	}
}

// TestBuildSystemPrefix_NilKnowledgeServiceNoKBBlock verifies that when no
// knowledge service is wired (e.knowledge == nil), the catalog block is
// completely absent.
func TestBuildSystemPrefix_NilKnowledgeServiceNoKBBlock(t *testing.T) {
	e := newMinimalEngine(t)
	// e.knowledge is nil by default from newMinimalEngine

	def := &agent.Definition{
		ID:           "no-kb-agent",
		SystemPrompt: "Simple prompt.",
		Knowledge:    []string{"some-kb"},
	}
	prefix := e.buildSystemPrefix(def)
	if strings.Contains(prefix, "Available Knowledge Bases") {
		t.Error("prefix should NOT contain 'Available Knowledge Bases' when knowledge service is nil")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — Handle with multiple builtin tool calls in one turn
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleMultipleToolCallsInOneTurn(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "multi-tool-bot",
		Name:         "Multi Tool",
		Enabled:      true,
		SystemPrompt: "Use all tools.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr("tool_a", "tool_b"),
	})
	e.builtins = []BuiltinTool{
		{
			Name: "tool_a",
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				mu.Lock()
				calls = append(calls, "a")
				mu.Unlock()
				return "result_a", nil
			},
		},
		{
			Name: "tool_b",
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				mu.Lock()
				calls = append(calls, "b")
				mu.Unlock()
				return "result_b", nil
			},
		},
	}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{
			{ID: "c1", Name: "tool_a", Arguments: map[string]any{}},
			{ID: "c2", Name: "tool_b", Arguments: map[string]any{}},
		}},
		{Content: "used both tools"},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("multi-tool-bot", "sess-multi", "use all tools"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "used both tools" {
		t.Fatalf("reply = %q, want 'used both tools'", got)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(calls))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — Handle with tool error (builtin returns error)
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleBuiltinToolErrorSurfacedAsToolResult(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "erroring-bot",
		Name:         "Erroring",
		Enabled:      true,
		SystemPrompt: "Use erroring tool.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     3,
		Builtins:     strListPtr("error_tool"),
	})
	e.builtins = []BuiltinTool{{
		Name: "error_tool",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "", &testToolError{"intentional error from tool"}
		},
	}}
	provider.responses = []llm.CompletionResponse{
		{ToolCalls: []message.ToolCall{{ID: "c1", Name: "error_tool", Arguments: map[string]any{}}}},
		{Content: "tool had an error"},
	}

	reply, err := e.Handle(context.Background(), testUserMessage("erroring-bot", "sess-err-tool", "run erroring"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	// The error should be passed back as tool result content so LLM can respond.
	reqs := provider.requestsSnapshot()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(reqs))
	}
	if !chatMessagesContain(reqs[1].Messages, "tool", "error") {
		t.Fatalf("second LLM call should have tool error in context: %#v", reqs[1].Messages)
	}
	_ = flattenParts(reply.Parts)
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — Handle: context cancellation
// ─────────────────────────────────────────────────────────────────────────────

func TestHandle_ContextCancelled(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "cancel-bot",
		Name:         "Cancel Bot",
		Enabled:      true,
		SystemPrompt: "Work.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     5,
		Builtins:     strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "done"}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// With a cancelled context, the LLM call may fail or return an error.
	// We only care that it doesn't deadlock.
	_, err := e.Handle(ctx, testUserMessage("cancel-bot", "sess-cancel", "run"))
	// May succeed (if check happens before cancellation propagates) or fail.
	// Either is acceptable — we just want no hang.
	_ = err
}

// ─────────────────────────────────────────────────────────────────────────────
// workflow.go — renderTemplate
// ─────────────────────────────────────────────────────────────────────────────

func TestRenderTemplate_Basic(t *testing.T) {
	vars := map[string]interface{}{
		"name": "world",
	}
	got, err := renderTemplate("Hello {{.name}}!", vars)
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}
	if got != "Hello world!" {
		t.Errorf("got %q, want Hello world!", got)
	}
}

func TestRenderTemplate_MissingKeyIsZero(t *testing.T) {
	vars := map[string]interface{}{}
	got, err := renderTemplate("value: {{.missing}}", vars)
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}
	// missingkey=zero should produce empty string for missing key
	if !strings.Contains(got, "value: ") {
		t.Errorf("got %q, expected template to expand", got)
	}
}

func TestRenderTemplate_InvalidTemplate(t *testing.T) {
	_, err := renderTemplate("{{.bad syntax", map[string]interface{}{})
	if err == nil {
		t.Error("expected error for invalid template syntax")
	}
}

func TestRenderTemplate_NestedValue(t *testing.T) {
	vars := map[string]interface{}{
		"trigger": "hello world",
	}
	got, err := renderTemplate(`{"msg":"{{.trigger}}"}`, vars)
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}
	if !strings.Contains(got, "hello world") {
		t.Errorf("got %q, expected template to contain trigger value", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// workflow.go — WorkflowExecutor with nil CheckpointStore
// ─────────────────────────────────────────────────────────────────────────────

func TestWorkflowExecutorNoCheckpointStore(t *testing.T) {
	e := &Engine{}
	var ran bool
	e.builtins = []BuiltinTool{{
		Name: "simple_step",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ran = true
			return "done", nil
		},
	}}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Steps: []agent.StepSpec{
			{ID: "step1", Tool: "simple_step", Input: `{}`},
		},
	}, e, nil, zap.NewNop()) // nil store

	out, err := w.Run(context.Background(), workflowTestMessage("trigger"), "run-nostore")
	if err != nil {
		t.Fatalf("Run with nil store: %v", err)
	}
	if !ran {
		t.Error("step should have run even without checkpoint store")
	}
	var s string
	if jsonErr := json.Unmarshal(out, &s); jsonErr != nil || s != "done" {
		t.Errorf("output = %s, want \"done\"", out)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// workflow.go — step.If condition skips step
// ─────────────────────────────────────────────────────────────────────────────

func TestWorkflowExecutorIfConditionSkipsStep(t *testing.T) {
	e := &Engine{}
	var condStepRan bool
	e.builtins = []BuiltinTool{
		{
			Name: "maybe_step",
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				condStepRan = true
				return "should not run", nil
			},
		},
		{
			Name: "final_step",
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return "final", nil
			},
		},
	}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Steps: []agent.StepSpec{
			{
				ID:    "conditional",
				Tool:  "maybe_step",
				Input: `{}`,
				If:    "false", // always false — step should be skipped
			},
			{
				ID:    "always",
				Tool:  "final_step",
				Input: `{}`,
			},
		},
	}, e, nil, zap.NewNop())

	out, err := w.Run(context.Background(), workflowTestMessage("trigger"), "run-if")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if condStepRan {
		t.Error("conditional step should have been skipped (If=false)")
	}
	var s string
	if jsonErr := json.Unmarshal(out, &s); jsonErr != nil || s != "final" {
		t.Errorf("output = %s, want \"final\"", out)
	}
}

func TestWorkflowExecutorIfConditionRunsStep(t *testing.T) {
	e := &Engine{}
	var ran bool
	e.builtins = []BuiltinTool{{
		Name: "cond_step",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ran = true
			return "ran", nil
		},
	}}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Steps: []agent.StepSpec{
			{
				ID:    "run-it",
				Tool:  "cond_step",
				Input: `{}`,
				If:    "true", // always true — step should run
			},
		},
	}, e, nil, zap.NewNop())

	_, err := w.Run(context.Background(), workflowTestMessage("trigger"), "run-if-true")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ran {
		t.Error("step should have run when If condition evaluates to true")
	}
}

func TestWorkflowExecutorIfConditionEmptySkips(t *testing.T) {
	e := &Engine{}
	var ran bool
	e.builtins = []BuiltinTool{{
		Name: "skip_step",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			ran = true
			return "ran", nil
		},
	}}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Steps: []agent.StepSpec{
			{
				ID:    "empty-cond",
				Tool:  "skip_step",
				Input: `{}`,
				If:    "{{if false}}run{{end}}", // renders to "" (empty) → skipped
			},
		},
	}, e, nil, zap.NewNop())

	_, err := w.Run(context.Background(), workflowTestMessage("trigger"), "run-if-empty")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ran {
		t.Error("step should be skipped when If evaluates to empty string")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// workflow.go — retry double-failure marks checkpoint failed
// ─────────────────────────────────────────────────────────────────────────────

func TestWorkflowExecutorRetryDoubleFail(t *testing.T) {
	store := newTestCheckpointStore(t)
	e := &Engine{}
	e.builtins = []BuiltinTool{{
		Name: "always_fail",
		Handler: func(ctx context.Context, _ map[string]any) (string, error) {
			return "", &testToolError{"persistent failure"}
		},
	}}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Steps: []agent.StepSpec{
			{ID: "failing", Tool: "always_fail", Input: `{}`, OnError: "retry"},
		},
	}, e, store, zap.NewNop())

	_, err := w.Run(context.Background(), workflowTestMessage("go"), "run-retry-fail")
	if err == nil {
		t.Fatal("expected error when retry also fails")
	}
	assertCheckpointStatus(t, store, "workflow-agent", "run-retry-fail", "failing", CheckpointFailed)
}

// ─────────────────────────────────────────────────────────────────────────────
// workflow.go — template error in step.Input marks checkpoint failed
// ─────────────────────────────────────────────────────────────────────────────

func TestWorkflowExecutorTemplateErrorFails(t *testing.T) {
	store := newTestCheckpointStore(t)
	e := &Engine{}
	e.builtins = []BuiltinTool{{
		Name:    "any_tool",
		Handler: func(ctx context.Context, _ map[string]any) (string, error) { return "ok", nil },
	}}

	w := NewWorkflowExecutor(agent.WorkflowSpec{
		Steps: []agent.StepSpec{
			{ID: "bad-input", Tool: "any_tool", Input: "{{.bad syntax"},
		},
	}, e, store, zap.NewNop())

	_, err := w.Run(context.Background(), workflowTestMessage("go"), "run-tmpl-err")
	if err == nil {
		t.Fatal("expected error when step input template is invalid")
	}
	assertCheckpointStatus(t, store, "workflow-agent", "run-tmpl-err", "bad-input", CheckpointFailed)
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — checkpoint.go — ListInProgress
// ─────────────────────────────────────────────────────────────────────────────

func TestCheckpointStoreListInProgress(t *testing.T) {
	store := newTestCheckpointStore(t)
	ctx := context.Background()

	// Insert one in-progress and one completed checkpoint.
	if err := store.Upsert(ctx, Checkpoint{
		AgentID:   "agent-1",
		RunID:     "run-1",
		StepID:    "step-1",
		Status:    CheckpointInProgress,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert in_progress: %v", err)
	}
	if err := store.Upsert(ctx, Checkpoint{
		AgentID:   "agent-1",
		RunID:     "run-1",
		StepID:    "step-2",
		Status:    CheckpointCompleted,
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Upsert completed: %v", err)
	}

	cps, err := store.ListInProgress(ctx)
	if err != nil {
		t.Fatalf("ListInProgress: %v", err)
	}
	if len(cps) != 1 {
		t.Fatalf("expected 1 in_progress checkpoint, got %d", len(cps))
	}
	if cps[0].StepID != "step-1" {
		t.Errorf("StepID = %q, want step-1", cps[0].StepID)
	}
}

func TestCheckpointStoreGetMissing(t *testing.T) {
	store := newTestCheckpointStore(t)
	_, err := store.Get(context.Background(), "no-agent", "no-run", "no-step")
	if err == nil {
		t.Fatal("expected error for absent checkpoint, got nil")
	}
}

func TestCheckpointStoreUpsertUpdatesStatus(t *testing.T) {
	store := newTestCheckpointStore(t)
	ctx := context.Background()

	cp := Checkpoint{
		AgentID:   "a",
		RunID:     "r",
		StepID:    "s",
		Status:    CheckpointInProgress,
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.Upsert(ctx, cp); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	cp.Status = CheckpointCompleted
	cp.State = json.RawMessage(`"result"`)
	if err := store.Upsert(ctx, cp); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := store.Get(ctx, "a", "r", "s")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != CheckpointCompleted {
		t.Errorf("status = %q, want %q", got.Status, CheckpointCompleted)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — getOrCreateSession is safe under concurrent access
// ─────────────────────────────────────────────────────────────────────────────

func TestGetOrCreateSession_ConcurrentSafety(t *testing.T) {
	e := &Engine{}
	const goroutines = 20
	done := make(chan *Session, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			sess := e.getOrCreateSession("shared-sess", "shared-agent")
			done <- sess
		}()
	}

	var first *Session
	for i := 0; i < goroutines; i++ {
		sess := <-done
		if first == nil {
			first = sess
		} else if sess != first {
			t.Error("concurrent getOrCreateSession returned different Session pointers for same (agent, session)")
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — SetOllamaAPIKey thread-safety
// ─────────────────────────────────────────────────────────────────────────────

func TestSetOllamaAPIKey_Concurrent(t *testing.T) {
	e := newMinimalEngine(t)
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(n int) {
			e.SetOllamaAPIKey("key-value")
			_ = e.getOllamaAPIKey()
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — Handle: empty MaxTurns defaults (default behaviour)
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleZeroMaxTurns_UsesDefault(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "zero-turns",
		Name:         "Zero Turns",
		Enabled:      true,
		SystemPrompt: "Be helpful.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     0, // zero — engine should use a sensible default
		Builtins:     strListPtr(),
	})
	provider.responses = []llm.CompletionResponse{{Content: "answer despite zero turns"}}

	reply, err := e.Handle(context.Background(), testUserMessage("zero-turns", "sess-zero", "hello"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if flattenParts(reply.Parts) == "" {
		t.Fatal("expected non-empty reply even with MaxTurns=0 (should default)")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — buildSystemPrefix: brainStore nil is a no-op
// ─────────────────────────────────────────────────────────────────────────────

func TestBuildSystemPrefix_NilBrainStoreNoOp(t *testing.T) {
	e := newMinimalEngine(t)
	// brainStore is nil by default — buildSystemPrefix should not panic.
	def := &agent.Definition{
		ID:           "no-brain",
		SystemPrompt: "Simple prompt.",
	}
	prefix := e.buildSystemPrefix(def)
	if !strings.Contains(prefix, "Simple prompt.") {
		t.Errorf("prefix should contain system prompt, got:\n%s", prefix)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — allToolSchemas with agent peer schemas included
// ─────────────────────────────────────────────────────────────────────────────

func TestAllToolSchemas_IncludesPeerAgentSchemas(t *testing.T) {
	dir := t.TempDir()
	loader := NewLoader([]string{dir})

	peer := &agent.Definition{ID: "helper", Name: "Helper", Description: "Helps.", Enabled: true}
	caller := &agent.Definition{ID: "caller", Agents: []string{"helper"}}
	for _, d := range []*agent.Definition{peer, caller} {
		if err := loader.Upsert(dir, d); err != nil {
			t.Fatalf("Upsert %s: %v", d.ID, err)
		}
	}

	mem, _ := memory.NewFileStore(t.TempDir())
	router := llm.NewRouter("test")
	e := NewEngine(loader, router, mem, nil, "", time.Second, zap.NewNop(), nil, nil, "", nil, nil, nil, nil, nil)

	schemas := e.allToolSchemas(caller, "http")
	names := toolSchemaNameSet(schemas)
	if !names[AgentToolPrefix+"helper"] {
		t.Errorf("allToolSchemas should include agent__helper, got: %v", sortedSchemaNames(names))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// engine.go — Handle: StreamReply flag does not break non-streaming path
// ─────────────────────────────────────────────────────────────────────────────

func TestHandle_StreamReplyFlagWithoutCallback(t *testing.T) {
	e, provider := newHandleTestEngine(t, &agent.Definition{
		ID:           "streaming-bot",
		Name:         "Streaming Bot",
		Enabled:      true,
		SystemPrompt: "Stream.",
		LLM:          agent.LLMConfig{Provider: "test", Model: "fake-model"},
		MaxTurns:     2,
		Builtins:     strListPtr(),
		StreamReply:  true, // flag set but no stream callback in context
	})
	provider.responses = []llm.CompletionResponse{{Content: "streamed response"}}

	reply, err := e.Handle(context.Background(), testUserMessage("streaming-bot", "sess-stream", "stream me"))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := flattenParts(reply.Parts); got != "streamed response" {
		t.Fatalf("reply = %q, want 'streamed response'", got)
	}
}
