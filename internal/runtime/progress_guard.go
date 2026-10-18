package runtime

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

type progressGuardKey struct{}

// A bounded evidence window detects repeated outcomes, including alternating
// tools and argument churn. Distinct results reset the consecutive stall count.
// This is a liveness backstop, never a claim that the user's goal is complete.
type progressGuard struct {
	mu              sync.Mutex
	limit, stagnant int
	recent          [][32]byte
	err             error
}

func withProgressGuard(ctx context.Context, def *agent.Definition) context.Context {
	limit := 20
	if def != nil && def.NoProgressLimit != nil {
		limit = max(0, *def.NoProgressLimit)
	}
	return context.WithValue(ctx, progressGuardKey{}, &progressGuard{limit: limit})
}

func progressGuardFrom(ctx context.Context) *progressGuard {
	g, _ := ctx.Value(progressGuardKey{}).(*progressGuard)
	return g
}

func (g *progressGuard) failure() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

func (g *progressGuard) observe(call message.ToolCall, output string, failed bool) {
	if g == nil || g.limit == 0 {
		return
	}
	signature := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%t\x00%s", call.Name, failed, output)))
	g.mu.Lock()
	defer g.mu.Unlock()
	repeated := false
	for _, prior := range g.recent {
		if prior == signature {
			repeated = true
			break
		}
	}
	if repeated {
		g.stagnant++
	} else {
		g.stagnant = 0
	}
	g.recent = append(g.recent, signature)
	if len(g.recent) > 64 {
		g.recent = g.recent[len(g.recent)-64:]
	}
	if g.stagnant >= g.limit {
		g.err = fmt.Errorf("run incomplete: %d consecutive tool results added no new evidence; change the approach or adjust no_progress_limit (0 disables this detector)", g.stagnant)
	}
}
