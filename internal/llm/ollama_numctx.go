package llm

// A meaningful serving window depends on the model, not a constant. The old
// default was a flat num_ctx of 16384 for every model, which was too small for
// Genie — its orchestrator prompt, tool schemas and an 8k output reserve
// overflowed it, so it failed preflight with "prompt exceeds the evidenced
// input budget" on a model that could hold far more.
//
// So when the operator has not pinned num_ctx, it is chosen from the model's
// own trained context_length, capped at a memory-reasonable target. The choice
// is made in ProfileModel (which already reads the model's metadata), cached on
// the provider, and read back on the request path — so the window a request is
// served is exactly the window preflight budgeted against.
//
// An operator num_ctx always wins, and an explicit num_ctx of 0 is respected as
// "let Ollama choose its own window" — the gateway does not infer one.

const (
	ollamaNumCtxFloor  = 16384 // conservative window when the model's max is unknown
	ollamaNumCtxTarget = 32768 // enough for Genie plus a large tool result; the cap for big models
)

// chooseNumCtx picks a serving window from the model's trained maximum.
// modelMax <= 0 means unknown. The result is deterministic — it depends only on
// the model, so cached profiles do not vary by machine. An operator on unusual
// hardware can still pin num_ctx explicitly.
func chooseNumCtx(modelMax int) int {
	if modelMax <= 0 {
		return ollamaNumCtxFloor // unknown model: conservative, never truncating a normal agent
	}
	if modelMax < ollamaNumCtxTarget {
		return modelMax // a small model gets its own maximum, not one it cannot serve
	}
	return ollamaNumCtxTarget // a large model is capped for memory, not run at its architectural max
}

// rememberNumCtx caches the resolved window for a model, set by ProfileModel
// once the model's trained maximum is known.
func (o *OllamaProvider) rememberNumCtx(model string, n int) {
	if n <= 0 {
		return
	}
	o.numCtxMu.Lock()
	defer o.numCtxMu.Unlock()
	if o.numCtxCache == nil {
		o.numCtxCache = make(map[string]int)
	}
	o.numCtxCache[model] = n
}

// resolveNumCtx returns the window to send for a model when the request did not
// already carry num_ctx: an explicit operator value, the value ProfileModel
// cached, or the floor until the profile is known. It never touches the
// network — the profiler owns that.
func (o *OllamaProvider) resolveNumCtx(model string) int {
	if n := o.contextLimit(); n > 0 {
		return n
	}
	o.numCtxMu.Lock()
	defer o.numCtxMu.Unlock()
	if n, ok := o.numCtxCache[model]; ok {
		return n
	}
	return ollamaNumCtxFloor
}
