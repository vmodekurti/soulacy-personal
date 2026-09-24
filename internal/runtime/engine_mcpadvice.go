package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/mcpinstall"
)

// Making the inspection verdict binding.
//
// `mcp_install_inspect` already answers the question that matters: can this
// server run inside the gateway, or does it need its own service? For a server
// with its own runtime or durable state the answer is "companion service", and
// `Recommendation.CanInstallHere` says so in a field, not just in prose.
//
// Nothing enforced it. A model could read "Deploy it as a companion service"
// and call `package_install` on the same URL anyway, which cloned the
// repository and rediscovered the same conclusion the expensive way — and in
// the run that prompted this, left seventeen further turns of the model trying
// to hand-install it through tools it was never going to be allowed (#231).
//
// So the installer asks the same question first and refuses when the answer is
// no. The verdict is cached from the inspection the model has usually just
// done, so in the normal path this costs nothing; when it was skipped, one
// shallow clone is still far cheaper than the loop it prevents.

// mcpAdviceTTL is how long an inspection verdict stays usable. A repository
// can change, but not within the minutes an install decision takes, and a
// stale "cannot install here" is the safe direction to be wrong in.
const mcpAdviceTTL = 30 * time.Minute

type mcpAdviceEntry struct {
	rec mcpinstall.Recommendation
	at  time.Time
}

var (
	mcpAdviceMu    sync.Mutex
	mcpAdviceCache = map[string]mcpAdviceEntry{}
)

func mcpAdviceKey(source string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(source), "/"))
}

// rememberMCPAdvice caches what an inspection concluded about a source.
func rememberMCPAdvice(source string, rec mcpinstall.Recommendation) {
	key := mcpAdviceKey(source)
	if key == "" {
		return
	}
	mcpAdviceMu.Lock()
	defer mcpAdviceMu.Unlock()
	mcpAdviceCache[key] = mcpAdviceEntry{rec: rec, at: time.Now()}
	// The cache exists to skip one clone, not to grow without bound.
	if len(mcpAdviceCache) > 64 {
		for k, v := range mcpAdviceCache {
			if time.Since(v.at) > mcpAdviceTTL {
				delete(mcpAdviceCache, k)
			}
		}
	}
}

// recallMCPAdvice returns a fresh cached verdict for a source.
func recallMCPAdvice(source string) (mcpinstall.Recommendation, bool) {
	key := mcpAdviceKey(source)
	mcpAdviceMu.Lock()
	defer mcpAdviceMu.Unlock()
	entry, ok := mcpAdviceCache[key]
	if !ok || time.Since(entry.at) > mcpAdviceTTL {
		return mcpinstall.Recommendation{}, false
	}
	return entry.rec, true
}

// mcpInstallRefusal returns the message to refuse an in-gateway install with,
// or "" when the source may be installed here. `force` is the operator's
// explicit override, which the recommendation itself documents.
func mcpInstallRefusal(ctx context.Context, source string, force bool) string {
	if force {
		return ""
	}
	rec, ok := recallMCPAdvice(source)
	if !ok {
		analysed, err := mcpinstall.AnalyzeRepository(ctx, source)
		if err != nil {
			// Inspection is advice. If it cannot be obtained, do not invent a
			// refusal — the installer's own safety introspection still runs.
			return ""
		}
		rememberMCPAdvice(source, analysed)
		rec = analysed
	}
	if rec.CanInstallHere {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "this server cannot be installed inside the gateway. %s: %s", rec.Title, rec.Summary)
	if len(rec.Reasons) > 0 {
		b.WriteString("\n\nWhy:")
		for _, r := range rec.Reasons {
			fmt.Fprintf(&b, "\n- %s", r)
		}
	}
	if len(rec.Steps) > 0 {
		b.WriteString("\n\nWhat to do instead:")
		for i, s := range rec.Steps {
			fmt.Fprintf(&b, "\n%d. %s", i+1, s)
		}
	}
	if rec.Command != "" {
		fmt.Fprintf(&b, "\n\nRegister it once it is running:\n%s", rec.Command)
	}
	if rec.Alternative != "" {
		fmt.Fprintf(&b, "\n\n%s", rec.Alternative)
	}
	b.WriteString("\n\nDo not try to install it another way: the tools for that are not available to this agent. " +
		"Give the person these directions instead.")
	return b.String()
}
