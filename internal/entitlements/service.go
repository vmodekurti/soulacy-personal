package entitlements

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	StatusActive    = "active"
	StatusPastDue   = "past_due"
	StatusSuspended = "suspended"
	CapabilityRuns  = "runs"
)

type Entitlement struct {
	WorkspaceID, Status, Plan, CustomerID, SubscriptionID string
	UpdatedAt                                             time.Time
}
type Store interface {
	Get(context.Context, string) (Entitlement, error)
	ApplyEvent(context.Context, string, Entitlement, string) (bool, error)
}

var ErrNotFound = errors.New("entitlement not found")

type MissingPolicy string

const (
	MissingAllowed MissingPolicy = "allow"
	MissingDenied  MissingPolicy = "deny"
)

type ServiceOptions struct{ Missing MissingPolicy }

type Service struct {
	store   Store
	missing MissingPolicy
}

func New(store Store) *Service { return NewWithOptions(store, ServiceOptions{}) }

func NewWithOptions(store Store, opts ServiceOptions) *Service {
	if opts.Missing == "" {
		opts.Missing = MissingAllowed
	}
	return &Service{store: store, missing: opts.Missing}
}

func (s *Service) Allowed(ctx context.Context, workspaceID, capability string) (bool, string, error) {
	e, err := s.store.Get(ctx, workspaceID)
	if errors.Is(err, ErrNotFound) {
		if s.missing == MissingDenied {
			return false, "subscription required", nil
		}
		return true, "unmetered", nil
	}
	if err != nil {
		return false, "entitlement service unavailable", err
	}
	if e.Status == StatusActive {
		return true, e.Plan, nil
	}
	return false, "subscription " + e.Status, nil
}

type PostgresStore struct{ pool *pgxpool.Pool }

func OpenPostgres(ctx context.Context, pool *pgxpool.Pool) (*PostgresStore, error) {
	_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS workspace_entitlements (
workspace_id TEXT PRIMARY KEY, status TEXT NOT NULL, plan TEXT NOT NULL DEFAULT '', customer_id TEXT NOT NULL DEFAULT '', subscription_id TEXT NOT NULL DEFAULT '', updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS billing_webhook_events (event_id TEXT PRIMARY KEY, source TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL DEFAULT '', applied_status TEXT NOT NULL DEFAULT '', received_at TIMESTAMPTZ NOT NULL DEFAULT now());
ALTER TABLE billing_webhook_events ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT '';
ALTER TABLE billing_webhook_events ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE billing_webhook_events ADD COLUMN IF NOT EXISTS applied_status TEXT NOT NULL DEFAULT ''`)
	if err != nil {
		return nil, err
	}
	return &PostgresStore{pool: pool}, nil
}
func (s *PostgresStore) Get(ctx context.Context, id string) (Entitlement, error) {
	var e Entitlement
	e.WorkspaceID = id
	err := s.pool.QueryRow(ctx, `SELECT status,plan,customer_id,subscription_id,updated_at FROM workspace_entitlements WHERE workspace_id=$1`, id).Scan(&e.Status, &e.Plan, &e.CustomerID, &e.SubscriptionID, &e.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return e, ErrNotFound
		}
		return e, err
	}
	return e, nil
}
func (s *PostgresStore) ApplyEvent(ctx context.Context, id string, e Entitlement, source string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit
	tag, err := tx.Exec(ctx, `INSERT INTO billing_webhook_events(event_id,source,workspace_id,applied_status) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, id, source, e.WorkspaceID, e.Status)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `INSERT INTO workspace_entitlements(workspace_id,status,plan,customer_id,subscription_id,updated_at) VALUES($1,$2,$3,$4,$5,now()) ON CONFLICT(workspace_id) DO UPDATE SET status=excluded.status,plan=excluded.plan,customer_id=excluded.customer_id,subscription_id=excluded.subscription_id,updated_at=now()`, e.WorkspaceID, e.Status, e.Plan, e.CustomerID, e.SubscriptionID)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

type StripeWebhook struct {
	Secret    string
	Tolerance time.Duration
	Store     Store
}
type stripeEvent struct {
	ID, Type string
	Data     struct {
		Object struct {
			ID, Customer, Status string
			Subscription         string            `json:"subscription"`
			Metadata             map[string]string `json:"metadata"`
			Items                struct {
				Data []struct{ Price struct{ ID string } }
			}
		} `json:"object"`
	}
}

func (h StripeWebhook) Handle(ctx context.Context, body []byte, signature string, now time.Time) error {
	if h.Secret == "" {
		return fmt.Errorf("stripe webhook secret is not configured")
	}
	timestamp, signatures := parseStripeSignature(signature)
	if timestamp == 0 || len(signatures) == 0 {
		return fmt.Errorf("invalid Stripe-Signature")
	}
	tolerance := h.Tolerance
	if tolerance == 0 {
		tolerance = 5 * time.Minute
	}
	eventTime := time.Unix(timestamp, 0)
	if now.Sub(eventTime) > tolerance || eventTime.Sub(now) > tolerance {
		return fmt.Errorf("stale Stripe webhook")
	}
	mac := hmac.New(sha256.New, []byte(h.Secret))
	fmt.Fprintf(mac, "%d.%s", timestamp, body)
	expected := mac.Sum(nil)
	valid := false
	for _, sig := range signatures {
		decoded, err := hex.DecodeString(sig)
		if err == nil && hmac.Equal(decoded, expected) {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("invalid Stripe webhook signature")
	}
	var event stripeEvent
	if err := json.Unmarshal(body, &event); err != nil {
		return err
	}
	if event.ID == "" {
		return fmt.Errorf("stripe event id is required")
	}
	obj := event.Data.Object
	workspace := strings.TrimSpace(obj.Metadata["workspace_id"])
	if workspace == "" {
		return nil
	}
	status := ""
	switch event.Type {
	case "invoice.payment_failed":
		status = StatusPastDue
	case "customer.subscription.deleted":
		status = StatusSuspended
	case "invoice.paid":
		status = StatusActive
	case "customer.subscription.updated", "customer.subscription.created":
		status = StatusActive
		if obj.Status != "active" && obj.Status != "trialing" {
			status = StatusPastDue
		}
	default:
		return nil
	}
	plan := ""
	if len(obj.Items.Data) > 0 {
		plan = obj.Items.Data[0].Price.ID
	}
	subscriptionID := obj.Subscription
	if strings.HasPrefix(event.Type, "customer.subscription.") {
		subscriptionID = obj.ID
	}
	_, err := h.Store.ApplyEvent(ctx, event.ID, Entitlement{WorkspaceID: workspace, Status: status, Plan: plan, CustomerID: obj.Customer, SubscriptionID: subscriptionID}, "stripe:"+event.Type)
	return err
}
func parseStripeSignature(raw string) (int64, []string) {
	var ts int64
	var sigs []string
	for _, p := range strings.Split(raw, ",") {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) != 2 {
			continue
		}
		if kv[0] == "t" {
			ts, _ = strconv.ParseInt(kv[1], 10, 64)
		}
		if kv[0] == "v1" {
			sigs = append(sigs, kv[1])
		}
	}
	return ts, sigs
}
