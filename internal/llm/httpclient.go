// httpclient.go — shared HTTP transport for every LLM provider.
//
// PRODUCTION_AUDIT → HIGH/Performance: each provider was building its own
// http.Client with the default Transport, which caps idle conns per host at
// 2. With concurrent agents hitting the same Ollama/Anthropic/OpenAI host
// the pool churned and every nth request paid a full TLS handshake. We now
// share one well-tuned Transport across all providers and embedders, so the
// pool warms up once and stays warm.
//
// Per-provider timeout overrides still apply: each provider builds its own
// *http.Client around this shared Transport with its own Timeout.

package llm

import (
	"net/http"
	"sync"
	"time"
)

var (
	sharedTransportOnce sync.Once
	sharedTransport     *http.Transport
	localTransportOnce  sync.Once
	localTransport      *http.Transport
)

// SharedTransport returns the process-wide *http.Transport used by every
// LLM provider client. Tuned for high-concurrency same-host workloads:
//   - MaxIdleConnsPerHost: 64  (vs default 2)
//   - MaxConnsPerHost:     0   (unlimited; flow control is upstream)
//   - IdleConnTimeout:    90s  (Go default)
//   - ResponseHeaderTimeout: 120s — protects against a stuck provider that
//     accepts the connection but never returns headers. 120s gives Ollama
//     enough runway to load a model cold before the first token streams.
func SharedTransport() *http.Transport {
	sharedTransportOnce.Do(func() {
		// Start from a clone of DefaultTransport so we inherit sane defaults
		// for DialContext, ForceAttemptHTTP2, TLSHandshakeTimeout, etc.
		base, ok := http.DefaultTransport.(*http.Transport)
		var t *http.Transport
		if ok {
			t = base.Clone()
		} else {
			t = &http.Transport{}
		}
		t.MaxIdleConnsPerHost = 64
		t.MaxConnsPerHost = 0
		t.IdleConnTimeout = 90 * time.Second
		t.ResponseHeaderTimeout = 120 * time.Second
		sharedTransport = t
	})
	return sharedTransport
}

// SharedHTTPClient returns an *http.Client wrapping SharedTransport with the
// caller's chosen overall timeout. Pass 0 to disable the client-level timeout
// (Ollama uses this — its context governs the upper bound because cold-loading
// large models can exceed any reasonable client deadline).
func SharedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: SharedTransport(),
		Timeout:   timeout,
	}
}

// LocalTransport is like SharedTransport but with NO ResponseHeaderTimeout.
// A LOCAL model server (Ollama, llama.cpp, LM Studio) can take far longer than
// 120s to return the first response header on a cold start — it must load tens
// of GB from disk into memory before generating a single token. The 120s cap on
// the shared transport (which protects against a stuck *cloud* provider) would
// abort that load with "timeout awaiting response headers". Local requests are
// instead bounded by the request context / per-agent run_timeout, so a cold
// load is allowed to finish.
func LocalTransport() *http.Transport {
	localTransportOnce.Do(func() {
		t := SharedTransport().Clone()
		t.ResponseHeaderTimeout = 0 // no header cap — context governs the bound
		localTransport = t
	})
	return localTransport
}

// LocalHTTPClient wraps LocalTransport for local model servers. Pass 0 for the
// timeout so the overall bound comes from the caller's context (run_timeout).
func LocalHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: LocalTransport(),
		Timeout:   timeout,
	}
}

// LongGenHTTPClient is for cloud providers whose response can withhold headers
// for a long time — notably a NON-STREAMING completion, where the provider
// sends no response headers until the entire body has been generated. The
// shared transport's 120s ResponseHeaderTimeout would abort such a legitimate
// long generation with "timeout awaiting response headers" (seen with large
// reasoning models like glm/qwen served over an OpenAI-compatible endpoint), so
// this client uses a transport with NO header cap and relies on the overall
// timeout (and the request context) as the single upper bound. Pass a generous
// timeout; pass 0 to let the context be the only bound.
func LongGenHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: LocalTransport(),
		Timeout:   timeout,
	}
}
