package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/learning"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/pkg/message"
)

// Opt-in: contacts only the operator-selected local Ollama model. All notes,
// agents and databases are disposable synthetic fixtures, never user data.
func TestLearningLiveModel(t *testing.T) {
	model := os.Getenv("SOULACY_LEARNING_LIVE_MODEL")
	if model == "" {
		t.Skip("set SOULACY_LEARNING_LIVE_MODEL to run real local-model learning evaluation")
	}
	e, _, def, n, ctx := learningEngine(t)
	e.llmRouter.Register(&learningTraceProvider{Provider: llm.NewOllamaProvider("http://127.0.0.1:11434", model, "5m", nil), t: t})
	def.LLM.Provider = "ollama"
	def.LLM.Model = model
	def.Builtins = strListPtr("learning.search", "learning.read", "learning.propose")
	e.loader.Register(def)
	e.SetTimeoutHierarchy(20*time.Second, 120*time.Second, 120*time.Second, 240*time.Second)
	scope := learning.Scope{Owner: "alice", AgentID: def.ID}
	t.Run("explicit preference", func(t *testing.T) {
		l, created, err := e.TeachLearning(ctx, scope, "For measurement answers, I prefer metric units and concise bullet points. This is my ongoing communication preference.")
		if err != nil || l == nil || !created || l.Kind != "preference" || l.Status != "pending" {
			t.Fatal(l, created, err)
		}
		if !strings.Contains(strings.ToLower(l.Content), "metric") {
			t.Fatal(l.Content)
		}
		if _, err := n.Review(ctx, scope, l.ID, "approve"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("corrects existing lesson", func(t *testing.T) {
		l, created, err := e.TeachLearning(ctx, scope, "Correction to my measurement answers preference: use imperial units instead of metric units for recipes. Keep concise bullet points for all answers.")
		if err != nil || l == nil || !created || l.BaseID == "" || !strings.Contains(strings.ToLower(l.Content), "imperial") {
			t.Fatal(l, created, err)
		}
	})
	t.Run("concrete reusable workflow", func(t *testing.T) {
		l, _, err := e.TeachLearning(ctx, scope, "Our staging release verification procedure is: back up the staging database, apply the pending migration to a restored staging copy, run the smoke tests against that copy, and compare row counts before promoting. Stop on any mismatch. Verify the migration log has no errors and all smoke tests pass. Never run this procedure on production without separate approval.")
		if err != nil || l == nil || l.Kind != "skill" || !strings.Contains(strings.ToLower(l.Content), "staging") {
			t.Fatal(l, err)
		}
	})
	t.Run("nothing useful creates nothing", func(t *testing.T) {
		l, created, err := e.TeachLearning(ctx, scope, "Hello! How are you today?")
		if err != nil || l != nil || created {
			t.Fatal(l, created, err)
		}
	})
	t.Run("real agent proposes within normal loop", func(t *testing.T) {
		ctx := WithToolObserver(ctx, func(call message.ToolCall, output string, failed bool) {
			t.Logf("tool=%s failed=%v arguments=%v result=%s", call.Name, failed, call.Arguments, learning.Clip(output, 1800))
		})
		m := testUserMessage(def.ID, "live-task", "Please remember this preference: I want dates written in ISO 8601 YYYY-MM-DD format in reports. Use learning.propose to draft it for my review, then tell me it is pending.")
		m.ID = "live-task-run"
		if reply, err := e.Handle(ctx, m); err != nil {
			t.Fatal(err)
		} else {
			t.Logf("reply=%s", flattenParts(reply.Parts))
		}
		pending, err := n.List(context.Background(), scope, "pending")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, l := range pending {
			if l.RunID == m.ID && strings.Contains(l.Content, "YYYY-MM-DD") {
				found = true
			}
		}
		if !found {
			t.Logf("pending=%+v", pending)
			t.Fatal("real model did not produce the source-backed date preference")
		}
	})
}

// Only the opt-in synthetic evaluation logs model drafts. Runtime production
// logs never record private teaching notes or reflection responses.
type learningTraceProvider struct {
	llm.Provider
	t *testing.T
}

func (p *learningTraceProvider) Complete(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	resp, err := p.Provider.Complete(ctx, req)
	if resp != nil && llm.CallMetadataFromContext(ctx).Source == "learning.teach" {
		p.t.Logf("synthetic reflection: %s", resp.Content)
	}
	return resp, err
}
