package gateway

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/actionlog"
	"github.com/soulacy/soulacy/internal/costs"
)

type operationsActivityReader interface {
	OperationsActivity(context.Context, time.Time, time.Time) (actionlog.OperationsActivity, error)
}

type reportSource struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type operationsReport struct {
	SchemaVersion int                           `json:"schema_version"`
	GeneratedAt   time.Time                     `json:"generated_at"`
	Window        string                        `json:"window"`
	Start         time.Time                     `json:"start"`
	End           time.Time                     `json:"end"`
	Status        string                        `json:"status"`
	Sources       map[string]reportSource       `json:"sources"`
	Activity      *actionlog.OperationsActivity `json:"activity"`
	Usage         *costs.OperationsUsage        `json:"usage"`
	Notes         []string                      `json:"notes"`
}

// handleOperationsReport is a read-only, metrics-authorized snapshot. No model
// is called, no report is sent, and no logging configuration is changed.
func (s *Server) handleOperationsReport(c *fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	authenticated := s.cfg.Server.APIKey != ""
	if s.authEngine != nil {
		authenticated = s.authEngine.Effective()
	}
	if !authenticated {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "operations reports require gateway authentication")
	}
	window := c.Query("window", "24h")
	durations := map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}
	duration, ok := durations[window]
	if !ok {
		return s.errMsg(c, fiber.StatusBadRequest, "window must be 24h, 7d, or 30d")
	}
	// Match the usage ledger's second resolution. Both stores share these exact
	// boundaries; writes still waiting in an async event queue are not invented.
	end := time.Now().UTC().Truncate(time.Second)
	// Respect the configured HTTP budget, with a tighter read-only reporting
	// ceiling so a large aggregation cannot monopolize the gateway database.
	queryTimeout := min(s.httpRequestTimeout(), 10*time.Second)
	ctx, cancel := context.WithTimeout(c.UserContext(), queryTimeout)
	defer cancel()
	report := operationsReport{
		SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Window: window,
		Start: end.Add(-duration), End: end, Status: "unavailable",
		Sources: map[string]reportSource{
			"activity": {Status: "unavailable", Detail: "Durable activity reporting is not configured or supported by this backend."},
			"usage":    {Status: "unavailable", Detail: "The usage ledger is not configured."},
		},
		Notes: []string{
			"UTC rolling window: start inclusive, end exclusive. Only retained, persisted records are included; recent buffered events may appear after refresh.",
			"Sessions group agent + conversation ID with an incoming request in the window. Several requests can share one session; model requests and retries are separate counts.",
			"Reply-complete means replies cover incoming requests and no error/dead-letter event was recorded in that window. It is not proof of answer quality or delivery. Errors can be recovered; unresolved sessions are not automatically failures or currently running.",
			"Reply latency samples only sessions with exactly one incoming request, one reply, and no error in the window. Multi-request sessions are excluded. Boundary-crossing sessions may be partial.",
			"Costs are recorded estimates, not provider invoices; unknown pricing is not free usage. Infrastructure and non-model tool charges are excluded. Rejected requests are counted separately from failed attempts.",
			"The report contains identifiers and operational totals, but no chat bodies, prompts, tool arguments, raw errors, or user identities. Keep exports private.",
		},
	}
	if reader, ok := s.actions.(operationsActivityReader); ok {
		activity, err := reader.OperationsActivity(ctx, report.Start, report.End)
		if err != nil {
			s.log.Warn("operations report: activity aggregation failed")
			report.Sources["activity"] = reportSource{Status: "error", Detail: "Activity could not be read within the report limits. Retry a shorter window or check gateway logs."}
		} else {
			report.Activity = &activity
			report.Sources["activity"] = reportSource{Status: "ready", Detail: "Durable activity records; absence of older retained data cannot be reconstructed."}
		}
	}
	if s.costStore != nil {
		usage, err := s.costStore.OperationsUsage(ctx, report.Start, report.End)
		if err != nil {
			s.log.Warn("operations report: usage aggregation failed")
			report.Sources["usage"] = reportSource{Status: "error", Detail: "Usage could not be read within the report limits. Retry a shorter window or check gateway logs."}
		} else {
			report.Usage = &usage
			report.Sources["usage"] = reportSource{Status: "ready", Detail: "Recorded model usage and estimated spend; not a provider billing statement."}
		}
	}
	switch {
	case report.Activity != nil && report.Usage != nil:
		report.Status = "ready"
	case report.Activity != nil || report.Usage != nil:
		report.Status = "partial"
	}
	return c.JSON(report)
}
