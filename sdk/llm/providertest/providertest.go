// Package providertest is the official llm.Provider conformance kit
// (Story E11). Extension authors run it out-of-tree against their provider;
// the host runs it against every built-in in CI so the contract and the
// implementations cannot drift.
//
//	func TestMyProviderConforms(t *testing.T) {
//	    providertest.RunProviderSuite(t, func() llm.Provider {
//	        return myprovider.New("https://api.example.com", "key", "model-x")
//	    })
//	}
//
// The suite is network-free: it verifies identity stability and context
// discipline — a cancelled context must surface as a prompt error, never a
// hang or a panic. Completion quality belongs in the author's own
// integration tests against a live endpoint.
package providertest

import (
	"context"
	"testing"
	"time"

	"github.com/soulacy/soulacy/sdk/llm"
)

// RunProviderSuite runs the conformance checks against fresh providers
// built by newProvider (one per subtest).
func RunProviderSuite(t *testing.T, newProvider func() llm.Provider) {
	t.Helper()

	t.Run("ID_NonEmptyAndStable", func(t *testing.T) {
		p := newProvider()
		id := p.ID()
		if id == "" {
			t.Fatal("conformance: ID() must return a non-empty identifier")
		}
		if p.ID() != id {
			t.Fatalf("conformance: ID() must be stable, got %q then %q", id, p.ID())
		}
	})

	t.Run("Complete_CancelledContext_PromptError", func(t *testing.T) {
		p := newProvider()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		done := make(chan struct{})
		var err error
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("conformance: Complete() with cancelled context panicked: %v", r)
				}
			}()
			_, err = p.Complete(ctx, llm.CompletionRequest{
				Messages: []llm.ChatMessage{{Role: "user", Content: "conformance probe"}},
			})
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("conformance: Complete() ignored the cancelled context (no return within 5s) — requests must carry ctx")
		}
		if err == nil {
			t.Fatal("conformance: Complete() with a cancelled context must return an error")
		}
	})

	t.Run("Models_CancelledContext_NoPanicNoHang", func(t *testing.T) {
		p := newProvider()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("conformance: Models() with cancelled context panicked: %v", r)
				}
			}()
			// Static model lists may succeed without network; remote lookups
			// must error promptly. Both are conforming — hanging is not.
			_, _ = p.Models(ctx)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("conformance: Models() ignored the cancelled context (no return within 5s)")
		}
	})
}
