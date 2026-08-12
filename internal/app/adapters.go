package app

// Small bridge adapters between subsystem interfaces. These exist so the
// packages on either side don't have to import each other (which would
// create cycles); the composition root is the natural home for glue.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/agentmemory"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/costs"
	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/memory"
	"github.com/soulacy/soulacy/internal/plugins"
	"github.com/soulacy/soulacy/internal/queue/dlq"
	"github.com/soulacy/soulacy/internal/runtime"
	"github.com/soulacy/soulacy/internal/telemetry"
)

// agentMemoryVectorAdapter makes the native sqlite-vec store the semantic
// backend for agentmemory.CompositeStore. The composition root owns this glue
// so neither memory package needs to depend on the other.
type agentMemoryVectorAdapter struct{ store *memory.VectorStore }

func (a *agentMemoryVectorAdapter) Write(r agentmemory.Record) error {
	if a == nil || a.store == nil {
		return fmt.Errorf("agent memory sqlite-vec store is unavailable")
	}
	if r.ID == "" {
		return fmt.Errorf("agent memory semantic record id is required")
	}
	created := r.Timestamp
	if created.IsZero() {
		created = time.Now().UTC()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.store.Write(ctx, memory.Entry{
		ID: r.ID, AgentID: r.AgentID, Scope: memory.ScopeAgent, Key: r.ID,
		Content: r.Content, Metadata: map[string]string{"tags": strings.Join(r.Tags, ",")}, CreatedAt: created,
	})
}

func (a *agentMemoryVectorAdapter) Search(agentID, query string, max int) ([]agentmemory.Record, error) {
	if a == nil || a.store == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hits, err := a.store.SearchFiltered(ctx, query, max, agentID)
	if err != nil {
		return nil, err
	}
	out := make([]agentmemory.Record, 0, len(hits))
	for _, hit := range hits {
		out = append(out, agentmemory.Record{
			ID: hit.Entry.Key, AgentID: hit.Entry.AgentID, Type: agentmemory.MemoryTypeSemantic,
			Timestamp: hit.Entry.CreatedAt, Content: hit.Entry.Content,
			Tags: strings.FieldsFunc(hit.Entry.Metadata["tags"], func(r rune) bool { return r == ',' }),
			Meta: map[string]string{"distance": fmt.Sprintf("%.6f", hit.Distance)},
		})
	}
	return out, nil
}

// llmEmbedAdapter wraps an llm.Embedder so it satisfies memory.Embedder.
// The llm.Embedder interface takes a model name and a slice of texts; memory
// only needs one text at a time and a fixed model baked in at construction.
type llmEmbedAdapter struct {
	inner llm.Embedder
	model string
}

func (a *llmEmbedAdapter) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := a.inner.Embed(ctx, a.model, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedder returned no vectors")
	}
	return vecs[0], nil
}

// pluginToolAdapter bridges *plugins.Loader → runtime.PluginToolProvider.
// Converts plugin.ToolSpec → runtime.PluginTool so the engine package doesn't
// need to import the plugins package (which would create a cycle).
type pluginToolAdapter struct{ loader *plugins.Loader }

func (a *pluginToolAdapter) AllTools() []runtime.PluginTool {
	specs := a.loader.AllTools()
	out := make([]runtime.PluginTool, 0, len(specs))
	for _, s := range specs {
		out = append(out, runtime.PluginTool{
			Name:        s.Name,
			Description: s.Description,
			Parameters:  s.Parameters,
			Handler:     s.Handler,
		})
	}
	return out
}

// Ensure the adapter satisfies the interface at compile time.
var _ runtime.PluginToolProvider = (*pluginToolAdapter)(nil)

// engineTracerAdapter bridges telemetry.Tracer → runtime's local tracer
// interface. Both use the same Start(ctx, name, ...string) signature.
type engineTracerAdapter struct{ t telemetry.Tracer }

func (a *engineTracerAdapter) Start(ctx context.Context, name string, kv ...string) (context.Context, interface{ End() }) {
	newCtx, span := a.t.Start(ctx, name, kv...)
	return newCtx, span
}

// engineDLQAdapter bridges dlq.Store → runtime's dead-letter interface.
type engineDLQAdapter struct{ s dlq.Store }

func (a *engineDLQAdapter) PushFailed(ctx context.Context, queue string, payload []byte, errMsg string) error {
	return a.s.Push(ctx, dlq.DeadLetter{
		ID:       dlq.NewID(),
		Queue:    queue,
		Payload:  payload,
		ErrorMsg: errMsg,
		Attempts: 1,
	})
}

// engineCostStoreAdapter bridges *costs.Store → runtime's cost-recording
// interface (individual fields → UsageRecord struct).
type engineCostStoreAdapter struct {
	s      *costs.Store
	prices costs.PriceTable
}

func (a *engineCostStoreAdapter) Record(ctx context.Context,
	agentID, sessionID, provider, model string,
	promptTokens, compTokens, totalTokens int,
	costUSD float64,
) error {
	if costUSD == 0 {
		costUSD = costs.EstimateUSD(a.prices, provider, model, promptTokens, compTokens)
	}
	return a.s.Record(ctx, costs.UsageRecord{
		AgentID:      agentID,
		SessionID:    sessionID,
		Provider:     provider,
		Model:        model,
		PromptTokens: promptTokens,
		CompTokens:   compTokens,
		TotalTokens:  totalTokens,
		CostUSD:      costUSD,
	})
}

func costPriceTableFromConfig(in map[string]config.CostPricing) costs.PriceTable {
	if len(in) == 0 {
		return nil
	}
	out := make(costs.PriceTable, len(in))
	for key, price := range in {
		normalized := costs.NormalizePriceKey(key)
		if normalized == "" {
			continue
		}
		out[normalized] = costs.Pricing{
			InputPerMTok:       price.InputPerMTok,
			OutputPerMTok:      price.OutputPerMTok,
			CachedInputPerMTok: price.CachedInputPerMTok,
			CacheWritePerMTok:  price.CacheWritePerMTok,
			ReasoningPerMTok:   price.ReasoningPerMTok,
			Source:             price.Source,
			EffectiveDate:      price.EffectiveDate,
			Version:            price.Version,
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
