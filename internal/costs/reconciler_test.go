package costs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenAICostImporterPaginatesAndSumsUSD(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "Bearer secret-admin" {
			t.Errorf("authorization = %q", got)
		}
		if r.URL.Query().Get("start_time") == "" || r.URL.Query().Get("end_time") == "" {
			t.Error("missing bounded billing period")
		}
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"data":[{"results":[{"amount":{"value":0.06,"currency":"usd"}}]}],"has_more":true,"next_page":"next"}`))
			return
		}
		if r.URL.Query().Get("page") != "next" {
			t.Errorf("page = %q", r.URL.Query().Get("page"))
		}
		_, _ = w.Write([]byte(`{"data":[{"results":[{"amount":{"value":1.25,"currency":"usd"}}]}],"has_more":false}`))
	}))
	defer server.Close()

	importer, err := NewOpenAICostImporter("openai", server.URL, "secret-admin", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	total, err := importer.ImportCostMicros(context.Background(), time.Unix(1, 0), time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	if total != 1_310_000 || calls != 2 {
		t.Fatalf("total=%d calls=%d", total, calls)
	}
}

type fixedBillingImporter struct {
	provider string
	micros   int64
}

func (f fixedBillingImporter) Provider() string { return f.provider }
func (f fixedBillingImporter) ImportCostMicros(context.Context, time.Time, time.Time) (int64, error) {
	return f.micros, nil
}

func TestReconcilerImportsPreviousCompletedUTCDayIdempotently(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "costs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 12, 15, 30, 0, 0, time.UTC)
	if err := store.Record(context.Background(), UsageRecord{Provider: "openai", CostMicros: 800_000, CostUSD: 0.8, CreatedAt: time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	r := NewReconciler(store, []BillingImporter{fixedBillingImporter{"openai", 1_000_000}}, ReconcilerConfig{Now: func() time.Time { return now }}, nil)
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListReconciliations(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	item := items[0]
	if item.EstimatedMicros != 800_000 || item.ActualMicros != 1_000_000 || item.VarianceMicros != 200_000 {
		t.Fatalf("reconciliation=%+v", item)
	}
	if item.PeriodStart != time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC) || item.PeriodEnd != time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("period=%s..%s", item.PeriodStart, item.PeriodEnd)
	}
}
