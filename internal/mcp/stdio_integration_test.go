package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestMCPStdioHelperProcess is a real child process used by the transport
// integration tests. It intentionally speaks newline-delimited JSON-RPC over
// its actual stdin/stdout rather than substituting an in-memory transport.
func TestMCPStdioHelperProcess(t *testing.T) {
	if os.Getenv("SOULACY_MCP_HELPER") != "1" {
		return
	}
	mode := os.Getenv("SOULACY_MCP_HELPER_MODE")
	if expected := os.Getenv("SOULACY_MCP_HELPER_EXPECT_SECRET"); expected != "" && os.Getenv("TEST_TOKEN") != expected {
		os.Exit(24)
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			os.Exit(0)
		}
		var req map[string]any
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		method, _ := req["method"].(string)
		id, hasID := req["id"]
		if !hasID { // initialized notification
			continue
		}
		switch method {
		case "initialize":
			helperReply(id, map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}})
		case "tools/list":
			helperReply(id, map[string]any{"tools": []any{map[string]any{
				"name": "echo", "description": "echo text",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			}}})
			if mode == "deadlock" {
				// Read just enough of the next large request to ensure the client
				// is blocked filling stdin, then fill stdout. A transport that holds
				// its pending-map mutex during Write deadlocks here because readLoop
				// needs that mutex to drain this notification.
				prefix := make([]byte, 4096)
				n, readErr := reader.Read(prefix)
				if readErr != nil {
					os.Exit(0)
				}
				blob, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "progress", "params": strings.Repeat("z", 4<<20)})
				fmt.Println(string(blob))
				rest, _ := reader.ReadString('\n')
				var largeReq map[string]any
				_ = json.Unmarshal(append(prefix[:n], []byte(rest)...), &largeReq)
				helperReply(largeReq["id"], toolResult("deadlock-free"))
				os.Exit(0)
			}
		case "tools/call":
			switch mode {
			case "exit":
				os.Exit(23)
			case "malformed":
				fmt.Println(`{definitely-not-json`)
				helperReply(id, toolResult("after-malformed"))
			case "noresponse":
				for {
					time.Sleep(time.Hour)
				}
			default:
				helperReply(id, toolResult("echo-ok"))
			}
		}
	}
}

func TestStdioResolvesEnvironmentSecrets(t *testing.T) {
	c := New(Config{
		ResolveSecret: func(_ context.Context, name string) (string, error) {
			if name != "travel-api-key" {
				return "", fmt.Errorf("unexpected secret %q", name)
			}
			return "vault-value", nil
		},
		Servers: map[string]ServerConfig{"helper": {
			Command: os.Args[0],
			Args:    []string{"-test.run=^TestMCPStdioHelperProcess$"},
			Env: map[string]string{
				"SOULACY_MCP_HELPER":               "1",
				"SOULACY_MCP_HELPER_EXPECT_SECRET": "vault-value",
			},
			EnvSecretRefs: map[string]string{"TEST_TOKEN": "travel-api-key"},
		}},
	}, zaptest.NewLogger(t))
	t.Cleanup(func() { _ = c.Close() })
	statuses := c.ServersSnapshot()
	if len(statuses) != 1 || !statuses[0].Connected {
		t.Fatalf("vault-backed stdio server did not connect: %+v", statuses)
	}
}

func helperReply(id any, result any) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(payload))
}

func toolResult(text string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
}

func newHelperClient(t *testing.T, mode string) *Client {
	t.Helper()
	c := New(Config{Servers: map[string]ServerConfig{"helper": {
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPStdioHelperProcess$"},
		Env: map[string]string{
			"SOULACY_MCP_HELPER":      "1",
			"SOULACY_MCP_HELPER_MODE": mode,
		},
	}}}, zaptest.NewLogger(t))
	t.Cleanup(func() { _ = c.Close() })
	statuses := c.ServersSnapshot()
	if len(statuses) != 1 || !statuses[0].Connected || len(statuses[0].Tools) != 1 {
		t.Fatalf("stdio handshake/tools list failed: %+v", statuses)
	}
	return c
}

func TestStdioSubprocessHandshakeListAndCall(t *testing.T) {
	c := newHelperClient(t, "normal")
	got, err := c.Call(context.Background(), "mcp__helper__echo", map[string]any{"text": "hello"})
	if err != nil || got != "echo-ok" {
		t.Fatalf("Call = %q, %v", got, err)
	}
}

func TestStdioSubprocessMidCallExit(t *testing.T) {
	c := newHelperClient(t, "exit")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Call(ctx, "mcp__helper__echo", nil); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("expected transport-closed error, got %v", err)
	}
}

func TestStdioSubprocessMalformedJSONIsSkipped(t *testing.T) {
	c := newHelperClient(t, "malformed")
	got, err := c.Call(context.Background(), "mcp__helper__echo", nil)
	if err != nil || got != "after-malformed" {
		t.Fatalf("Call after malformed frame = %q, %v", got, err)
	}
}

func TestStdioSubprocessNoResponseHonorsContext(t *testing.T) {
	c := newHelperClient(t, "noresponse")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Call(ctx, "mcp__helper__echo", nil); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected named context deadline, got %v", err)
	}
}

func TestStdioLargeBidirectionalTransferDoesNotDeadlock(t *testing.T) {
	c := newHelperClient(t, "deadlock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := c.Call(ctx, "mcp__helper__echo", map[string]any{"text": strings.Repeat("x", 4<<20)})
	if err != nil || got != "deadlock-free" {
		t.Fatalf("large bidirectional call = %q, %v", got, err)
	}
}
