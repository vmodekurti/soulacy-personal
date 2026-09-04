---
name: go-code-review
description: Review Go source code for correctness, idiomatic style, error handling, and performance. Use when the user asks you to review, audit, check, or improve Go code.
license: Apache-2.0
metadata:
  author: soulacy
  version: "1.0"
---

# Go Code Review Skill

## When to use this skill

Use this skill when the user asks you to review, audit, check, or improve Go source code.

## Review checklist

Work through these categories in order. Report findings grouped by severity: **Critical**, **Warning**, **Suggestion**.

### 1. Correctness
- [ ] Are errors checked and handled (not silently dropped with `_`)?
- [ ] Are goroutines properly synchronized (no data races)?
- [ ] Are channels closed by the sender, never the receiver?
- [ ] Is `context` propagated correctly through call chains?
- [ ] Are defer calls in the right place (not inside loops unless intentional)?

### 2. Idiomatic Go
- [ ] Are receivers consistent (all pointer or all value, not mixed)?
- [ ] Are exported names documented with a godoc comment?
- [ ] Does the code avoid `init()` unless truly necessary?
- [ ] Are named return values used only when they add clarity?
- [ ] Are interfaces defined at the point of use (consumer-side)?

### 3. Error handling
- [ ] Are errors wrapped with `%w` (not `%v`) for proper `errors.Is`/`errors.As` support?
- [ ] Are sentinel errors (`var ErrFoo = errors.New(...)`) used for errors callers check?
- [ ] Does every error path return early rather than continuing on failure?

### 4. Performance
- [ ] Is `strings.Builder` used instead of `+` concatenation in loops?
- [ ] Are slices pre-allocated with `make([]T, 0, n)` when size is known?
- [ ] Are maps pre-allocated with `make(map[K]V, n)` when size is known?
- [ ] Is the `sync.Pool` or object reuse considered for high-allocation hot paths?

### 5. Security
- [ ] Are SQL queries parameterized (no string interpolation into queries)?
- [ ] Are file paths sanitized before use in OS calls?
- [ ] Is cryptographic randomness from `crypto/rand`, not `math/rand`?
- [ ] Are HTTP timeouts set on all `http.Client` instances?

## Output format

For each finding, write:

```
[SEVERITY] <brief title>
File: <file>:<line> (if known)
Issue: <what the problem is>
Fix: <what to change>
```

End with a **Summary** section: total issue count by severity, and an overall verdict (Approve / Needs changes / Reject).

## Example

**[WARNING] Error silently dropped**
File: internal/handler.go:42
Issue: `json.Unmarshal` error assigned to `_`, masking parse failures.
Fix: Return or log the error: `if err := json.Unmarshal(data, &v); err != nil { return err }`
