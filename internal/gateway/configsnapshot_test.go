package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

// The whole point of the snapshot is that a reload and a request can run at
// the same time. Under -race this fails on any build that goes back to a plain
// field.
func TestAReloadAndAReadCanRunAtTheSameTime(t *testing.T) {
	s := &Server{}
	s.setConfig(&config.Config{Server: config.ServerConfig{APIKey: "a"}})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			next := &config.Config{}
			next.Server.APIKey = "key"
			s.setConfig(next)
		}
	}()
	for i := 0; i < 20000; i++ {
		if key := s.config().Server.APIKey; key != "a" && key != "key" {
			t.Fatalf("torn read: %q", key)
		}
	}
	close(stop)
	wg.Wait()
}

// A handler holding a snapshot must keep seeing the configuration it started
// with. This is the property a mutex would NOT have given: under a lock, two
// reads inside one request can straddle a reload.
func TestASnapshotDoesNotChangeUnderTheHandlerHoldingIt(t *testing.T) {
	s := &Server{}
	s.setConfig(&config.Config{Server: config.ServerConfig{APIKey: "before"}})
	held := s.config()
	s.setConfig(&config.Config{Server: config.ServerConfig{APIKey: "after"}})
	if held.Server.APIKey != "before" {
		t.Fatalf("the snapshot a request was holding changed under it: %q", held.Server.APIKey)
	}
	if s.config().Server.APIKey != "after" {
		t.Fatal("the new snapshot was not published")
	}
}

// mutateConfig must not write through the map an older snapshot still points
// at — that is the race, reintroduced one entry at a time.
func TestPublishingOneProviderDoesNotEditAnOlderSnapshot(t *testing.T) {
	s := &Server{}
	s.setConfig(&config.Config{LLM: config.LLMConfig{Providers: map[string]config.ProviderConfig{
		"openai": {Model: "gpt-4o-mini"},
	}}})
	held := s.config()

	s.setProviderConfig("anthropic", config.ProviderConfig{Model: "claude"})

	if _, leaked := held.LLM.Providers["anthropic"]; leaked {
		t.Error("a provider written after the snapshot was taken appeared inside it — the map is " +
			"shared with every in-flight request")
	}
	if got := s.config().LLM.Providers["anthropic"].Model; got != "claude" {
		t.Errorf("new snapshot provider model = %q, want claude", got)
	}
	if got := s.config().LLM.Providers["openai"].Model; got != "gpt-4o-mini" {
		t.Errorf("existing provider lost on publish: %q", got)
	}
}

func TestPublishingOneChannelDoesNotEditAnOlderSnapshot(t *testing.T) {
	s := &Server{}
	s.setConfig(&config.Config{Channels: map[string]map[string]any{"slack": {"enabled": true}}})
	held := s.config()

	s.setChannelConfig("discord", map[string]any{"enabled": true})

	if _, leaked := held.Channels["discord"]; leaked {
		t.Error("channel written into a snapshot that was already handed out")
	}
	if s.config().Channels["slack"] == nil {
		t.Error("existing channel lost on publish")
	}
}

// The behaviour tests above all pass on a build where ONE handler still writes
// through s.config(). That build has the original race, and it would have no
// failing test, so the source is checked directly.
//
// Test files are deliberately not scanned: a test mutates the config before it
// serves anything, from the single goroutine that built the Server, so there is
// no concurrent reader to race with. Production handlers have no such
// guarantee — that is the entire distinction.
func TestNoProductionCodeWritesThroughTheConfigSnapshot(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, lhs := range assign.Lhs {
				if writesThroughConfigCall(lhs) {
					t.Errorf("%s:%d writes through s.config(); the returned snapshot is shared with "+
						"every in-flight request, so this is the data race the snapshot removed. "+
						"Use mutateConfig, which clones first",
						name, fset.Position(assign.Pos()).Line)
				}
			}
			return true
		})
	}
}

// writesThroughConfigCall reports whether an assignment target is rooted at a
// config() call — s.config().X.Y, s.config().M[k], and so on.
func writesThroughConfigCall(expr ast.Expr) bool {
	for {
		switch e := expr.(type) {
		case *ast.SelectorExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.StarExpr:
			expr = e.X
		case *ast.CallExpr:
			sel, ok := e.Fun.(*ast.SelectorExpr)
			return ok && sel.Sel.Name == "config"
		default:
			return false
		}
	}
}
