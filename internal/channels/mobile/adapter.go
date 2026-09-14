package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	store         *Store
	relayURL      string
	relayToken    string
	apns          *apnsClient
	pushConfigErr error
	client        *http.Client
	log           *zap.Logger
	mu            sync.RWMutex
	started       bool
}

func New(store *Store, log *zap.Logger) *Adapter {
	if log == nil {
		log = zap.NewNop()
	}
	apns, apnsErr := newAPNSClientFromEnvironment()
	a := &Adapter{
		store: store, relayURL: strings.TrimRight(strings.TrimSpace(os.Getenv("SOULACY_MOBILE_PUSH_RELAY_URL")), "/"),
		relayToken: strings.TrimSpace(os.Getenv("SOULACY_MOBILE_PUSH_RELAY_TOKEN")),
		apns:       apns, pushConfigErr: apnsErr,
		client: &http.Client{Timeout: 8 * time.Second}, log: log.Named("mobile-channel"),
	}
	SetDefaultAdapter(a)
	return a
}

var (
	defaultAdapterMu sync.RWMutex
	defaultAdapter   *Adapter
)

// SetDefaultAdapter records the process-wide adapter so gateway code that
// does not hold the channel registry (approval broker hooks, triggers) can
// still push to paired phones.
func SetDefaultAdapter(a *Adapter) {
	defaultAdapterMu.Lock()
	defaultAdapter = a
	defaultAdapterMu.Unlock()
}

// DefaultAdapter returns the process-wide adapter, or nil.
func DefaultAdapter() *Adapter {
	defaultAdapterMu.RLock()
	defer defaultAdapterMu.RUnlock()
	return defaultAdapter
}

// Notification categories the iOS app registers actions for.
const (
	CategoryApproval = "SOULACY_APPROVAL" // Approve / Deny actions
	CategoryDelivery = "SOULACY_DELIVERY" // open the delivery
	CategoryTrigger  = "SOULACY_TRIGGER"  // a location run completed
)

// Notification is a push a gateway feature wants a phone to show. Category
// selects the actions the phone attaches; Data rides along for the action
// handler (call id, agent id, session id). TimeSensitive raises the
// interruption level so Focus modes still show it.
type Notification struct {
	Title         string
	Body          string
	Category      string
	ThreadID      string
	DeepLink      string
	Data          map[string]string
	TimeSensitive bool
}

// CanPush reports whether any push transport is configured.
func (a *Adapter) CanPush() bool { return a != nil && (a.apns != nil || a.relayURL != "") }

// Notify pushes n to every notification-enabled device matching destination
// ("" = every device in the workspace, "user:<id>", or "device:<id>").
// It never blocks a caller on a failed transport: the first error is
// returned for logging after every device has been attempted.
func (a *Adapter) Notify(ctx context.Context, workspaceID, destination string, n Notification) error {
	if a == nil || a.store == nil {
		return errors.New("mobile delivery store is unavailable")
	}
	devices, err := a.store.TargetDevices(ctx, workspaceID, destination)
	if err != nil {
		return err
	}
	var firstErr error
	for _, device := range devices {
		rn := relayNotification{DeviceToken: device.PushToken, Environment: device.PushEnvironment,
			BundleID: device.BundleID, Title: n.Title, Body: n.Body, DeepLink: n.DeepLink,
			Category: n.Category, ThreadID: n.ThreadID, Data: n.Data, TimeSensitive: n.TimeSensitive}
		if err := a.deliver(ctx, rn); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// deliver sends one relayNotification over APNs when configured, otherwise
// through the push relay.
func (a *Adapter) deliver(ctx context.Context, n relayNotification) error {
	if a.apns != nil {
		return a.apns.push(ctx, n)
	}
	if a.relayURL == "" {
		return errors.New("no push transport configured")
	}
	payload, _ := json.Marshal(n)
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
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("push relay returned %d", resp.StatusCode)
	}
	return nil
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
	if a.pushConfigErr != nil {
		detail += "; native push misconfigured"
	} else if a.apns != nil {
		detail += "; direct APNs ready"
	} else if a.relayURL == "" {
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
	if a.apns != nil || a.relayURL != "" {
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
	DeviceToken   string            `json:"device_token"`
	Environment   string            `json:"environment"`
	BundleID      string            `json:"bundle_id"`
	DeliveryID    string            `json:"delivery_id,omitempty"`
	Title         string            `json:"title"`
	Body          string            `json:"body"`
	DeepLink      string            `json:"deep_link,omitempty"`
	Category      string            `json:"category,omitempty"`
	ThreadID      string            `json:"thread_id,omitempty"`
	Data          map[string]string `json:"data,omitempty"`
	TimeSensitive bool              `json:"time_sensitive,omitempty"`
}

func (a *Adapter) notify(ctx context.Context, workspaceID string, delivery Delivery) error {
	devices, err := a.store.TargetDevices(ctx, workspaceID, delivery.Destination)
	if err != nil {
		return err
	}
	var firstErr error
	for _, device := range devices {
		notification := relayNotification{DeviceToken: device.PushToken, Environment: device.PushEnvironment,
			BundleID: device.BundleID, DeliveryID: delivery.ID, Title: delivery.Title,
			Body: "An agent result is ready to review.", DeepLink: "soulacy://delivery/" + delivery.ID,
			Category: CategoryDelivery, ThreadID: "delivery-" + delivery.AgentID}
		if a.apns != nil {
			if err := a.apns.push(ctx, notification); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		payload, _ := json.Marshal(notification)
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
