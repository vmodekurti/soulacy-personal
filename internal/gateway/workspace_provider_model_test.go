package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/soulacy/soulacy/sdk/llm"
)

type modelCatalogProvider struct {
	models []string
	err    error
}

func (p modelCatalogProvider) ID() string { return "catalog" }
func (p modelCatalogProvider) Complete(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, errors.New("not implemented")
}
func (p modelCatalogProvider) Models(context.Context) ([]string, error) { return p.models, p.err }

func TestValidateWorkspaceProviderModelRejectsMissingCatalogEntry(t *testing.T) {
	p := modelCatalogProvider{models: []string{"nvidia/nemotron-3-nano-30b-a3b"}}
	err := validateWorkspaceProviderModel(context.Background(), p, "nvidia/nemotron-nano-3-30b-a3b")
	if err == nil {
		t.Fatal("expected an unavailable model to be rejected")
	}
}

func TestValidateWorkspaceProviderModelAcceptsCatalogEntry(t *testing.T) {
	p := modelCatalogProvider{models: []string{"nvidia/nemotron-3-nano-30b-a3b"}}
	if err := validateWorkspaceProviderModel(context.Background(), p, "nvidia/nemotron-3-nano-30b-a3b"); err != nil {
		t.Fatalf("valid model rejected: %v", err)
	}
}

func TestValidateWorkspaceProviderModelAllowsUnavailableDiscovery(t *testing.T) {
	p := modelCatalogProvider{err: errors.New("catalog unavailable")}
	if err := validateWorkspaceProviderModel(context.Background(), p, "compatible/custom-model"); err != nil {
		t.Fatalf("providers without model discovery must remain configurable: %v", err)
	}
}
