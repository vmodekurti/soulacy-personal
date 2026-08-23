package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/llm"
	mem "github.com/soulacy/soulacy/internal/memory"
)

type reindexEmbedder struct {
	inner llm.Embedder
	model string
}

func (e reindexEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vectors, err := e.inner.Embed(ctx, e.model, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, fmt.Errorf("embedding provider returned %d vectors, want 1", len(vectors))
	}
	return vectors[0], nil
}

func (e reindexEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return e.inner.Embed(ctx, e.model, texts)
}

func buildMemoryReindexCmd() *cobra.Command {
	var dbPath, providerID, model, baseURL string
	var targetDims, batchSize int
	var restart, dryRun, confirmOffline bool
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Rebuild sqlite-vec memory after changing embedding models",
		Long: `Re-embed every semantic-memory row into a new sqlite-vec dimension.

The operation is resumable and its final cutover is atomic. Stop the gateway
before running it so new memory cannot arrive between the final checkpoint and
cutover. Provider credentials are read from the normal Soulacy configuration;
they are never accepted as command-line flags.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, _, err := config.Load(os.Getenv("SOULACY_CONFIG_PATH"))
			if err != nil {
				return err
			}
			if dbPath == "" {
				dbPath = cfg.Memory.SQLitePath
			}
			if providerID == "" {
				providerID = cfg.Knowledge.EmbeddingProvider
			}
			if model == "" {
				model = cfg.Knowledge.EmbeddingModel
			}
			providerID, model = strings.TrimSpace(providerID), strings.TrimSpace(model)
			if dbPath == "" || providerID == "" || model == "" {
				return fmt.Errorf("database, embedding provider, and embedding model are required")
			}
			providerCfg := cfg.LLM.Providers[providerID]
			if baseURL != "" {
				providerCfg.BaseURL = baseURL
			}
			embedder, err := reindexProvider(providerID, providerCfg, cfg.LLM.Providers["ollama"].BaseURL)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if targetDims <= 0 {
				targetDims, err = embedder.Dim(ctx, model)
				if err != nil {
					return fmt.Errorf("probe target dimensions: %w", err)
				}
			}

			archive, err := mem.NewSQLiteArchive(dbPath)
			if err != nil {
				return err
			}
			defer archive.Close()
			currentDims, err := mem.VectorDimensions(ctx, archive.DB())
			if err != nil {
				return err
			}
			var rows int64
			if err := archive.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_vector_meta`).Scan(&rows); err != nil {
				return fmt.Errorf("read vector metadata: %w", err)
			}
			if dryRun {
				return printReindexValue(map[string]any{"database": dbPath, "provider": providerID, "model": model,
					"source_dims": currentDims, "target_dims": targetDims, "rows": rows, "noop": currentDims == targetDims})
			}
			if !confirmOffline {
				return fmt.Errorf("refusing online reindex: stop the gateway and pass --confirm-offline")
			}

			report, err := mem.ReindexVectorStore(ctx, archive.DB(), reindexEmbedder{inner: embedder, model: model}, mem.VectorReindexOptions{
				TargetDims: targetDims,
				ModelID:    providerID + "/" + model + "@" + strings.TrimSpace(providerCfg.BaseURL),
				BatchSize:  batchSize,
				Restart:    restart,
				Progress: func(progress mem.VectorReindexProgress) {
					if !outputJSON {
						fmt.Fprintf(os.Stderr, "reindexing: %d/%d rows (%d → %d dimensions)\n", progress.Completed, progress.Total, progress.SourceDims, progress.TargetDims)
					}
				},
			})
			if err != nil {
				return err
			}
			return printReindexValue(report)
		},
	}
	cmd.Flags().StringVar(&dbPath, "database", "", "SQLite memory archive (default: memory.sqlite_path)")
	cmd.Flags().StringVar(&providerID, "provider", "", "Embedding provider (default: knowledge.embedding_provider)")
	cmd.Flags().StringVar(&model, "model", "", "Embedding model (default: knowledge.embedding_model)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Override the configured embedding provider base URL")
	cmd.Flags().IntVar(&targetDims, "dims", 0, "Target dimensions (default: probe the model)")
	cmd.Flags().IntVar(&batchSize, "batch-size", 64, "Checkpoint batch size (1-1000)")
	cmd.Flags().BoolVar(&restart, "restart", false, "Discard an incompatible prior checkpoint")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Inspect the planned migration without changing data")
	cmd.Flags().BoolVar(&confirmOffline, "confirm-offline", false, "Confirm the gateway is stopped")
	return cmd
}

func reindexProvider(id string, pc config.ProviderConfig, ollamaBaseURL string) (llm.Embedder, error) {
	baseURL := strings.TrimSpace(pc.BaseURL)
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "ollama":
		if baseURL == "" {
			baseURL = strings.TrimSpace(ollamaBaseURL)
		}
		return llm.NewOllamaEmbedder(baseURL), nil
	case "google", "gemini":
		if pc.APIKey == "" {
			return nil, fmt.Errorf("embedding provider %q has no configured API key", id)
		}
		return llm.NewGoogleCompatibleEmbedder(id, baseURL, pc.APIKey), nil
	case "openai":
		if pc.APIKey == "" {
			return nil, fmt.Errorf("embedding provider %q has no configured API key", id)
		}
		return llm.NewOpenAIEmbedder(baseURL, pc.APIKey), nil
	default:
		if pc.APIKey == "" || !strings.Contains(baseURL, "/v1") {
			return nil, fmt.Errorf("embedding provider %q requires an API key and OpenAI-compatible /v1 base URL", id)
		}
		return llm.NewOpenAICompatibleEmbedder(id, baseURL, pc.APIKey), nil
	}
}

func printReindexValue(value any) error {
	if outputJSON {
		return json.NewEncoder(os.Stdout).Encode(value)
	}
	raw, _ := json.MarshalIndent(value, "", "  ")
	fmt.Println(string(raw))
	return nil
}
