package llm

import (
	"context"
	"testing"
)

type governanceFakeEmbedder struct{ calls int }

func (e *governanceFakeEmbedder) ID() string { return "embed" }
func (e *governanceFakeEmbedder) Embed(context.Context, string, []string) ([][]float32, error) {
	e.calls++
	return [][]float32{{1, 2, 3}}, nil
}
func (e *governanceFakeEmbedder) Dim(context.Context, string) (int, error) { return 3, nil }

type embeddingController struct {
	before int
	after  int
	req    CompletionRequest
	resp   *CompletionResponse
}

func (c *embeddingController) Before(ctx context.Context, _ string, req *CompletionRequest) (context.Context, Reservation, error) {
	c.before++
	c.req = *req
	return ctx, Reservation{ID: "embed-call"}, nil
}
func (c *embeddingController) After(_ context.Context, _ Reservation, _ string, _ CompletionRequest, resp *CompletionResponse, _ error) {
	c.after++
	c.resp = resp
}

func TestGovernedEmbedderUsesRouterController(t *testing.T) {
	router := NewRouter("")
	controller := &embeddingController{}
	router.SetController(controller)
	inner := &governanceFakeEmbedder{}
	embedder := NewGovernedEmbedder(inner, router)
	if _, err := embedder.Embed(context.Background(), "embedding-model", []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if controller.before != 1 || controller.after != 1 || controller.req.Operation != "embedding" {
		t.Fatalf("controller before/after/operation = %d/%d/%q", controller.before, controller.after, controller.req.Operation)
	}
	if controller.resp == nil || controller.resp.InputTokens <= 0 {
		t.Fatal("embedding usage was not estimated")
	}
}
