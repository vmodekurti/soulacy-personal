package app

import (
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

func TestEmbedderForProviderRegistersKnownEmbeddingProviders(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		cfg         config.ProviderConfig
		wantID      string
		wantPresent bool
	}{
		{
			name:        "ollama without key",
			id:          "ollama",
			cfg:         config.ProviderConfig{},
			wantID:      "ollama",
			wantPresent: true,
		},
		{
			name:        "google with key",
			id:          "google",
			cfg:         config.ProviderConfig{APIKey: "key"},
			wantID:      "google",
			wantPresent: true,
		},
		{
			name:        "gemini alias keeps configured id",
			id:          "gemini",
			cfg:         config.ProviderConfig{APIKey: "key"},
			wantID:      "gemini",
			wantPresent: true,
		},
		{
			name:        "openroute compatible with key",
			id:          "openroute",
			cfg:         config.ProviderConfig{APIKey: "key", BaseURL: "http://router.test/v1"},
			wantID:      "openroute",
			wantPresent: true,
		},
		{
			name:        "hosted provider without key skipped",
			id:          "google",
			cfg:         config.ProviderConfig{},
			wantPresent: false,
		},
		{
			name:        "custom v1 compatible with key",
			id:          "custom_embed",
			cfg:         config.ProviderConfig{APIKey: "key", BaseURL: "http://custom.test/v1"},
			wantID:      "custom_embed",
			wantPresent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			emb := embedderForProvider(tt.id, tt.cfg, "http://ollama.test")
			if !tt.wantPresent {
				if emb != nil {
					t.Fatalf("embedderForProvider returned %T, want nil", emb)
				}
				return
			}
			if emb == nil {
				t.Fatal("embedderForProvider returned nil")
			}
			if emb.ID() != tt.wantID {
				t.Fatalf("ID = %q, want %q", emb.ID(), tt.wantID)
			}
		})
	}
}
