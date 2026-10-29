package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/llm"
	sdkhooks "github.com/soulacy/soulacy/sdk/hooks"
)

// lifecycleStreamProvider streams one token, then holds the stream open
// until release is closed, so the test controls when the provider finishes.
type lifecycleStreamProvider struct {
	release chan struct{}
}

func (lifecycleStreamProvider) ID() string { return "lifecycle-stream" }

func (lifecycleStreamProvider) Models(context.Context) ([]string, error) {
	return []string{"test"}, nil
}

func (p lifecycleStreamProvider) Complete(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	stream := make(chan string)
	resp := &llm.CompletionResponse{Stream: stream}
	go func() {
		stream <- "complete"
		<-p.release
		resp.OutputTokens = 7
		resp.TotalTokens = 11
		close(stream)
	}()
	return resp, nil
}

func TestLLMAfterLifecycleHookWaitsForStreamingUsage(t *testing.T) {
	router := llm.NewRouter("lifecycle-stream")
	release := make(chan struct{})
	router.Register(lifecycleStreamProvider{release: release})
	engine := &Engine{llmRouter: router}

	afterEvents := make(chan sdkhooks.Event, 1)
	registry := &sdkhooks.Registry{}
	if err := registry.Register("capture-after", sdkhooks.HandlerFunc(func(_ context.Context, event sdkhooks.Event) (sdkhooks.Decision, error) {
		afterEvents <- event
		return sdkhooks.Decision{}, nil
	}), sdkhooks.LLMAfter); err != nil {
		t.Fatal(err)
	}
	engine.SetLifecycleHooks(registry)

	resp, err := engine.completeLLM(context.Background(), "lifecycle-stream", llm.CompletionRequest{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	// The first token has been relayed but the provider is still holding
	// its stream open, so the after hook must not have run. (The relay is
	// buffered: without the gate the provider could finish, and the hook
	// legitimately fire, before the consumer reads anything.)
	if token := <-resp.Stream; token != "complete" {
		t.Fatalf("token = %q", token)
	}
	select {
	case <-afterEvents:
		t.Fatal("after hook ran before the stream completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range resp.Stream {
	}

	select {
	case event := <-afterEvents:
		if event.Data["output_tokens"] != 7 || event.Data["outcome"] != "success" {
			t.Fatalf("after hook did not observe final usage: %+v", event.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("after hook did not run after stream completion")
	}
}
