package extstorage

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/cfgmap"
	"github.com/soulacy/soulacy/sdk/queue"
	"github.com/soulacy/soulacy/sdk/registry"
	"github.com/soulacy/soulacy/sdk/vector"
)

// init self-registers the "external" vector and queue factories (Story
// E10 pattern): config selects a sidecar-backed store with
//
//	vector:
//	  backend: external
//	  command: /usr/local/bin/my-vector-sidecar
//	  args: ["--flag"]
//
// Host-internal cfg keys: "scratch_root" (workspace data dir) and "logger"
// (*zap.Logger), both injected by internal/app wiring.
func init() {
	registry.MustRegisterVector("external", func(cfg map[string]any) (vector.Backend, error) {
		cc, err := clientConfigFrom(cfg, "vector-external")
		if err != nil {
			return nil, err
		}
		return NewVectorBackend(context.Background(), cc)
	})
	registry.MustRegisterQueue("external", func(cfg map[string]any) (queue.Backend, error) {
		cc, err := clientConfigFrom(cfg, "queue-external")
		if err != nil {
			return nil, err
		}
		return NewQueueBackend(context.Background(), cc)
	})
}

func clientConfigFrom(cfg map[string]any, name string) (ClientConfig, error) {
	command := cfgmap.Str(cfg, "command", "")
	if command == "" {
		return ClientConfig{}, fmt.Errorf("extstorage: %s requires a non-empty command", name)
	}
	cc := ClientConfig{
		Name:        cfgmap.Str(cfg, "id", name),
		Command:     command,
		Args:        stringList(cfg["args"]),
		ScratchRoot: cfgmap.Str(cfg, "scratch_root", ""),
	}
	if lg, ok := cfg["logger"].(*zap.Logger); ok {
		cc.Log = lg
	}
	return cc, nil
}

// stringList coerces YAML []any / []string args defensively.
func stringList(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
