package webpush

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Notification is the JSON payload delivered to the service worker.
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"`
}

// Service ties together the VAPID sender, a persisted subscription list, and a
// simple fan-out Notify. It is safe for concurrent use.
type Service struct {
	mu        sync.Mutex
	sender    *Sender
	subsPath  string
	subs      map[string]Subscription // keyed by endpoint
	publicKey string
}

// LoadOrCreateKeys returns the VAPID keypair stored at path, generating and
// persisting a fresh pair on first use. The file holds "public\nprivate".
func LoadOrCreateKeys(path string) (publicB64, privateB64 string, err error) {
	if data, rerr := os.ReadFile(path); rerr == nil {
		parts := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), nil
		}
	}
	pub, priv, gerr := GenerateVAPIDKeys()
	if gerr != nil {
		return "", "", gerr
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return "", "", mkErr
	}
	if werr := os.WriteFile(path, []byte(pub+"\n"+priv+"\n"), 0o600); werr != nil {
		return "", "", werr
	}
	return pub, priv, nil
}

// NewService builds a push service from a VAPID keypair, a contact subject, and
// a path where subscriptions are persisted as JSONL.
func NewService(publicB64, privateB64, subject, subsPath string) (*Service, error) {
	sender, err := NewSender(publicB64, privateB64, subject)
	if err != nil {
		return nil, err
	}
	s := &Service{
		sender:    sender,
		subsPath:  subsPath,
		subs:      map[string]Subscription{},
		publicKey: publicB64,
	}
	s.load()
	return s, nil
}

// PublicKey returns the base64url applicationServerKey for the browser.
func (s *Service) PublicKey() string { return s.publicKey }

// Subscribe stores (or refreshes) a browser subscription.
func (s *Service) Subscribe(sub Subscription) error {
	if strings.TrimSpace(sub.Endpoint) == "" || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		return errors.New("webpush: subscription missing endpoint or keys")
	}
	s.mu.Lock()
	s.subs[sub.Endpoint] = sub
	err := s.persistLocked()
	s.mu.Unlock()
	return err
}

// Unsubscribe removes a subscription by endpoint.
func (s *Service) Unsubscribe(endpoint string) error {
	s.mu.Lock()
	delete(s.subs, endpoint)
	err := s.persistLocked()
	s.mu.Unlock()
	return err
}

// Count returns the number of stored subscriptions.
func (s *Service) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// NotifyResult reports the outcome of a fan-out.
type NotifyResult struct {
	Sent int
	Gone int
}

// Notify sends n to every subscription, pruning any the push service reports as
// gone. Returns the number of successful deliveries.
func (s *Service) Notify(n Notification) int {
	return s.NotifyDetailed(n).Sent
}

// NotifyDetailed is Notify with a full result breakdown (for metrics).
func (s *Service) NotifyDetailed(n Notification) NotifyResult {
	payload, _ := json.Marshal(n)

	s.mu.Lock()
	targets := make([]Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		targets = append(targets, sub)
	}
	s.mu.Unlock()

	sent := 0
	var gone []string
	for _, sub := range targets {
		err := s.sender.Send(sub, payload, 0)
		switch {
		case err == nil:
			sent++
		case errors.Is(err, ErrGone):
			gone = append(gone, sub.Endpoint)
		}
	}
	if len(gone) > 0 {
		s.mu.Lock()
		for _, ep := range gone {
			delete(s.subs, ep)
		}
		_ = s.persistLocked()
		s.mu.Unlock()
	}
	return NotifyResult{Sent: sent, Gone: len(gone)}
}

func (s *Service) load() {
	f, err := os.Open(s.subsPath)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var sub Subscription
		if json.Unmarshal(sc.Bytes(), &sub) == nil && sub.Endpoint != "" {
			s.subs[sub.Endpoint] = sub
		}
	}
}

func (s *Service) persistLocked() error {
	if s.subsPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.subsPath), 0o755); err != nil {
		return err
	}
	tmp := s.subsPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, sub := range s.subs {
		if err := enc.Encode(sub); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.subsPath)
}
