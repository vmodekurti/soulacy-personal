package whatsappweb

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

func TestSendWaitsForSidecarAcknowledgement(t *testing.T) {
	reader, writer := io.Pipe()
	a := &Adapter{stdin: writer, pending: make(map[string]chan error)}

	commandRead := make(chan sidecarCommand, 1)
	go func() {
		line, _ := bufio.NewReader(reader).ReadBytes('\n')
		var cmd sidecarCommand
		_ = json.Unmarshal(line, &cmd)
		commandRead <- cmd
	}()

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- a.Send(ctx, message.Message{ThreadID: "self", Parts: message.Text("test")})
	}()

	cmd := <-commandRead
	if cmd.Type != "send" || cmd.ID == "" || cmd.To != "self" {
		t.Fatalf("unexpected sidecar command: %+v", cmd)
	}
	select {
	case err := <-done:
		t.Fatalf("Send returned before sidecar acknowledgement: %v", err)
	default:
	}
	a.mu.Lock()
	waiter := a.pending[cmd.ID]
	a.mu.Unlock()
	if waiter == nil {
		t.Fatal("send acknowledgement waiter was not registered")
	}
	waiter <- nil
	if err := <-done; err != nil {
		t.Fatalf("Send after acknowledgement: %v", err)
	}
}
