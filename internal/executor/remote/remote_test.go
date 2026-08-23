package remote

import (
	"context"
	"github.com/soulacy/soulacy/internal/executor/process"
	queuememory "github.com/soulacy/soulacy/internal/queue/memory"
	"testing"
	"time"
)

func TestGatewayDispatchesExecutionToWorker(t *testing.T) {
	q := queuememory.New()
	defer q.Close()
	w := NewWorker(q, process.New("python3"), "test-workers", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	out, err := New(q).Run(ctx, "", "run", "def run(args): return {'ok': args['value']}", []byte(`{"value":42}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"ok": 42}` {
		t.Fatalf("output=%q", out)
	}
}
