package extstorage

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	sdkext "github.com/soulacy/soulacy/sdk/extstorage"
	"github.com/soulacy/soulacy/sdk/queue"
)

// QueueBackend adapts a negotiated storage sidecar to queue.Backend.
// Deliveries arrive as queue.message notifications and are dispatched to
// the subscription's handler on a dedicated goroutine; Ack round-trips
// queue.ack when the sidecar tracks deliveries.
type QueueBackend struct {
	c *Client

	mu   sync.Mutex
	subs map[string]func(*queue.Message) // subscription_id → handler
}

// NewQueueBackend spawns + negotiates a sidecar and verifies it
// advertises the "queue" capability.
func NewQueueBackend(ctx context.Context, cfg ClientConfig) (*QueueBackend, error) {
	c := NewClient(cfg)
	b := &QueueBackend{c: c, subs: map[string]func(*queue.Message){}}
	c.OnNotification(b.onNotify)
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	if !hasCapability(c.Negotiated().Capabilities, "queue") {
		_ = c.Close()
		return nil, fmt.Errorf("extstorage: %s does not advertise the queue capability (got %v)",
			cfg.Name, c.Negotiated().Capabilities)
	}
	return b, nil
}

// Client exposes the underlying session to the host.
func (b *QueueBackend) Client() *Client { return b.c }

func (b *QueueBackend) onNotify(method string, params json.RawMessage) {
	if method != sdkext.NotifyQueueMessage {
		return // unknown notifications are skipped (forward compat)
	}
	var p sdkext.QueueMessageParams
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	b.mu.Lock()
	handler := b.subs[p.SubscriptionID]
	b.mu.Unlock()
	if handler == nil {
		return
	}
	ack := func() error { return nil }
	if p.DeliveryID != "" {
		deliveryID := p.DeliveryID
		ack = func() error {
			return b.c.Call(context.Background(), sdkext.MethodQueueAck,
				sdkext.QueueAckParams{DeliveryID: deliveryID}, nil)
		}
	}
	go handler(queue.NewMessage(p.Subject, p.Data, ack))
}

// Publish implements queue.Backend.
func (b *QueueBackend) Publish(ctx context.Context, subject string, data []byte) error {
	var res sdkext.QueuePublishResult
	return b.c.Call(ctx, sdkext.MethodQueuePublish,
		sdkext.QueuePublishParams{Subject: subject, Data: data}, &res)
}

// Subscribe implements queue.Backend.
func (b *QueueBackend) Subscribe(ctx context.Context, subject, group string, handler func(*queue.Message)) (queue.Subscription, error) {
	var res sdkext.QueueSubscribeResult
	err := b.c.Call(ctx, sdkext.MethodQueueSubscribe,
		sdkext.QueueSubscribeParams{Subject: subject, Group: group}, &res)
	if err != nil {
		return nil, err
	}
	if res.SubscriptionID == "" {
		return nil, fmt.Errorf("extstorage: %s: queue.subscribe returned no subscription id", b.c.cfg.Name)
	}
	b.mu.Lock()
	b.subs[res.SubscriptionID] = handler
	b.mu.Unlock()
	return &extSubscription{b: b, id: res.SubscriptionID}, nil
}

// Close implements queue.Backend.
func (b *QueueBackend) Close() error {
	b.mu.Lock()
	b.subs = map[string]func(*queue.Message){}
	b.mu.Unlock()
	return b.c.Close()
}

type extSubscription struct {
	b    *QueueBackend
	id   string
	once sync.Once
}

// Unsubscribe implements queue.Subscription. Idempotent.
func (s *extSubscription) Unsubscribe() error {
	var err error
	s.once.Do(func() {
		s.b.mu.Lock()
		delete(s.b.subs, s.id)
		s.b.mu.Unlock()
		err = s.b.c.Call(context.Background(), sdkext.MethodQueueUnsubscribe,
			sdkext.QueueUnsubscribeParams{SubscriptionID: s.id}, nil)
	})
	return err
}
