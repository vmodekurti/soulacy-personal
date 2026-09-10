package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/message"
	sdkchannel "github.com/soulacy/soulacy/sdk/channel"
)

type Adapter struct {
	store      *Store
	relayURL   string
	relayToken string
	client     *http.Client
	log        *zap.Logger
	mu         sync.RWMutex
	started    bool
}

func New(store *Store, log *zap.Logger) *Adapter {
	if log == nil {
		log = zap.NewNop()
	}
	return &Adapter{
		store: store, relayURL: strings.TrimRight(strings.TrimSpace(os.Getenv("SOULACY_MOBILE_PUSH_RELAY_URL")), "/"),
		relayToken: strings.TrimSpace(os.Getenv("SOULACY_MOBILE_PUSH_RELAY_TOKEN")),
		client:     &http.Client{Timeout: 8 * time.Second}, log: log.Named("mobile-channel"),
	}
}

func (a *Adapter) ID() string   { return "mobile" }
func (a *Adapter) Name() string { return "Soulacy Mobile" }

func (a *Adapter) Start(context.Context, chan<- message.Message) error {
	if a.store == nil {
		return errors.New("mobile delivery store is unavailable")
	}
	a.mu.Lock()
	a.started = true
	a.mu.Unlock()
	return nil
}

func (a *Adapter) Stop() error {
	a.mu.Lock()
	a.started = false
	a.mu.Unlock()
	return nil
}

func (a *Adapter) Status() sdkchannel.AdapterStatus {
	a.mu.RLock()
	started := a.started
	a.mu.RUnlock()
	detail := "durable app inbox"
	if a.relayURL == "" {
		detail += "; push relay not configured"
	} else {
		detail += "; native push ready"
	}
	return sdkchannel.AdapterStatus{Connected: started && a.store != nil, Detail: detail}
}

func (a *Adapter) Send(ctx context.Context, msg message.Message) error {
	if a.store == nil {
		return errors.New("mobile delivery store is unavailable")
	}
	body := messageText(msg)
	title := "Result from " + msg.AgentID
	d := Delivery{ID: msg.ID, Destination: msg.ThreadID, AgentID: msg.AgentID, SessionID: msg.SessionID,
		Title: title, Body: body, Parts: msg.Parts, Metadata: msg.Metadata, CreatedAt: msg.CreatedAt}
	if err := a.store.Add(ctx, "personal", d); err != nil {
		return err
	}
	// APNs is a wake-up signal, not the source of truth. A relay failure must
	// never turn a safely persisted result into an undelivered run.
	if a.relayURL != "" {
		if err := a.notify(ctx, "personal", d); err != nil {
			a.log.Warn("native push failed; result remains in the mobile inbox", zap.String("delivery_id", d.ID), zap.Error(err))
		}
	}
	return nil
}

func messageText(msg message.Message) string {
	var values []string
	for _, part := range msg.Parts {
		if part.Type == message.ContentText && strings.TrimSpace(part.Text) != "" {
			values = append(values, part.Text)
		}
	}
	return strings.Join(values, "\n\n")
}

type relayNotification struct {
	DeviceToken string `json:"device_token"`
	Environment string `json:"environment"`
	BundleID    string `json:"bundle_id"`
	DeliveryID  string `json:"delivery_id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	DeepLink    string `json:"deep_link"`
}

func (a *Adapter) notify(ctx context.Context, workspaceID string, delivery Delivery) error {
	devices, err := a.store.TargetDevices(ctx, workspaceID, delivery.Destination)
	if err != nil {
		return err
	}
	var firstErr error
	for _, device := range devices {
		payload, _ := json.Marshal(relayNotification{DeviceToken: device.PushToken, Environment: device.PushEnvironment,
			BundleID: device.BundleID, DeliveryID: delivery.ID, Title: delivery.Title,
			Body: "An agent result is ready to review.", DeepLink: "soulacy://delivery/" + delivery.ID})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.relayURL+"/v1/push", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if a.relayToken != "" {
			req.Header.Set("Authorization", "Bearer "+a.relayToken)
		}
		resp, err := a.client.Do(req)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if firstErr == nil {
				firstErr = errors.New("push relay returned " + resp.Status)
			}
		}
	}
	return firstErr
}
