package costs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// BillingImporter retrieves prompt-free provider billing totals. Implementors
// return integer micro-dollars to avoid floating-point drift in the ledger.
type BillingImporter interface {
	Provider() string
	ImportCostMicros(context.Context, time.Time, time.Time) (int64, error)
}

type ReconcilerConfig struct {
	Interval               time.Duration
	VarianceAlertThreshold float64
	Now                    func() time.Time
}

// Reconciler imports the previous completed UTC day. Re-imports are safe:
// Store.ReconcileProvider upserts the provider/period tuple.
type Reconciler struct {
	store     *Store
	importers []BillingImporter
	cfg       ReconcilerConfig
	log       *zap.Logger
	cancel    context.CancelFunc
	done      chan struct{}
	once      sync.Once
}

func NewReconciler(store *Store, importers []BillingImporter, cfg ReconcilerConfig, log *zap.Logger) *Reconciler {
	if cfg.Interval <= 0 {
		cfg.Interval = 24 * time.Hour
	}
	if cfg.VarianceAlertThreshold <= 0 {
		cfg.VarianceAlertThreshold = 0.1
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Reconciler{store: store, importers: append([]BillingImporter(nil), importers...), cfg: cfg, log: log, done: make(chan struct{})}
}

func (r *Reconciler) Start(parent context.Context) {
	if r == nil || r.store == nil || len(r.importers) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	go func() {
		defer close(r.done)
		_ = r.RunOnce(ctx)
		ticker := time.NewTicker(r.cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = r.RunOnce(ctx)
			}
		}
	}()
}

func (r *Reconciler) Close() error {
	if r == nil || r.cancel == nil {
		return nil
	}
	r.once.Do(r.cancel)
	<-r.done
	return nil
}

func (r *Reconciler) RunOnce(ctx context.Context) error {
	if r == nil || r.store == nil {
		return errors.New("cost reconciler requires a store")
	}
	end := r.cfg.Now().UTC().Truncate(24 * time.Hour)
	start := end.Add(-24 * time.Hour)
	var joined error
	for _, importer := range r.importers {
		actual, err := importer.ImportCostMicros(ctx, start, end)
		if err != nil {
			joined = errors.Join(joined, fmt.Errorf("%s billing import: %w", importer.Provider(), err))
			r.log.Error("provider cost reconciliation failed", zap.String("provider", importer.Provider()), zap.Error(err))
			continue
		}
		item, err := r.store.ReconcileProvider(ctx, importer.Provider(), start, end, actual, "scheduled-provider-billing-api")
		if err != nil {
			joined = errors.Join(joined, fmt.Errorf("%s reconciliation: %w", importer.Provider(), err))
			continue
		}
		variance := item.VarianceRatio()
		fields := []zap.Field{zap.String("provider", item.Provider), zap.Int64("estimated_micros", item.EstimatedMicros), zap.Int64("actual_micros", item.ActualMicros), zap.Float64("variance_ratio", variance)}
		if variance > r.cfg.VarianceAlertThreshold {
			r.log.Warn("provider cost reconciliation variance exceeded alert threshold", fields...)
		} else {
			r.log.Info("provider cost reconciliation complete", fields...)
		}
	}
	return joined
}

// OpenAICostImporter reads the official organization Costs endpoint. It needs
// an OpenAI admin key, supplied at runtime; the key never enters a report.
type OpenAICostImporter struct {
	provider     string
	baseURL      string
	adminKey     string
	organization string
	client       *http.Client
}

func NewOpenAICostImporter(provider, baseURL, adminKey, organization string, client *http.Client) (*OpenAICostImporter, error) {
	if strings.TrimSpace(provider) == "" {
		provider = "openai"
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.openai.com"
	}
	if strings.TrimSpace(adminKey) == "" {
		return nil, errors.New("admin API key is empty")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &OpenAICostImporter{provider: provider, baseURL: strings.TrimRight(baseURL, "/"), adminKey: adminKey, organization: organization, client: client}, nil
}

func (i *OpenAICostImporter) Provider() string { return i.provider }

func (i *OpenAICostImporter) ImportCostMicros(ctx context.Context, start, end time.Time) (int64, error) {
	if !end.After(start) {
		return 0, errors.New("billing period end must be after start")
	}
	var total int64
	page := ""
	for pages := 0; pages < 100; pages++ {
		endpoint, err := url.Parse(i.baseURL + "/v1/organization/costs")
		if err != nil {
			return 0, err
		}
		query := endpoint.Query()
		query.Set("start_time", strconv.FormatInt(start.UTC().Unix(), 10))
		query.Set("end_time", strconv.FormatInt(end.UTC().Unix(), 10))
		query.Set("bucket_width", "1d")
		query.Set("limit", "180")
		if page != "" {
			query.Set("page", page)
		}
		endpoint.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+i.adminKey)
		req.Header.Set("Accept", "application/json")
		if i.organization != "" {
			req.Header.Set("OpenAI-Organization", i.organization)
		}
		resp, err := i.client.Do(req)
		if err != nil {
			return 0, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return 0, readErr
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return 0, fmt.Errorf("costs API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
		}
		var payload struct {
			Data []struct {
				Results []struct {
					Amount struct {
						Value    float64 `json:"value"`
						Currency string  `json:"currency"`
					} `json:"amount"`
				} `json:"results"`
			} `json:"data"`
			HasMore  bool   `json:"has_more"`
			NextPage string `json:"next_page"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return 0, fmt.Errorf("decode costs API: %w", err)
		}
		for _, bucket := range payload.Data {
			for _, result := range bucket.Results {
				if !strings.EqualFold(result.Amount.Currency, "usd") {
					return 0, fmt.Errorf("unsupported billing currency %q", result.Amount.Currency)
				}
				total += int64(result.Amount.Value*1_000_000 + 0.5)
			}
		}
		if !payload.HasMore {
			return total, nil
		}
		if payload.NextPage == "" || payload.NextPage == page {
			return 0, errors.New("costs API pagination did not advance")
		}
		page = payload.NextPage
	}
	return 0, errors.New("costs API pagination exceeded 100 pages")
}
