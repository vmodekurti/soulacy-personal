package gateway

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
	"go.uber.org/zap/zaptest"
)

// TestEventHubDisconnectMidBroadcast pins the lock ordering used by Handler:
// remove a client while holding the hub lock, then close its queue. Under
// `go test -race` this catches both map races and send-on-closed-channel bugs.
func TestEventHubDisconnectMidBroadcast(t *testing.T) {
	hub := NewEventHub(zaptest.NewLogger(t), nil)
	const clients = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < clients; i++ {
		client := &wsClient{send: make(chan []byte, 2), principal: eventPrincipal{Principal: fmt.Sprintf("user-%d", i)}}
		hub.mu.Lock()
		hub.clients[client] = struct{}{}
		hub.mu.Unlock()
		wg.Add(1)
		go func(c *wsClient) {
			defer wg.Done()
			<-start
			hub.mu.Lock()
			delete(hub.clients, c)
			hub.mu.Unlock()
			close(c.send)
		}(client)
	}
	close(start)
	for i := 0; i < 500; i++ {
		hub.Emit(message.Event{Type: "progress", SessionID: "race", Timestamp: time.Now().UTC(), Payload: i})
	}
	wg.Wait()
	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("clients after disconnect = %d", got)
	}
}
