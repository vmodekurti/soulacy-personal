package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

// Filter before pagination so a recent unrelated run cannot hide a matching
// result. Time bounds are explicit RFC3339 instants, including their timezone.
func filterRunLedger(c *fiber.Ctx, rows []runLedgerRow) ([]runLedgerRow, error) {
	bounds := map[string]time.Time{}
	for _, name := range []string{"after", "before"} {
		if raw := c.Query(name); raw != "" {
			v, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return nil, fmt.Errorf("%s must be RFC3339 with timezone", name)
			}
			bounds[name] = v
		}
	}
	if !bounds["after"].IsZero() && !bounds["before"].IsZero() && bounds["after"].After(bounds["before"]) {
		return nil, fmt.Errorf("after must precede before")
	}
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	q := strings.ToLower(strings.TrimSpace(c.Query("q")))
	runID := strings.TrimSpace(c.Query("run_id"))
	out := make([]runLedgerRow, 0, len(rows))
	for _, row := range rows {
		if runID != "" && row.RunID != runID && row.ID != runID {
			continue
		}
		if status != "" && strings.ToLower(row.Status) != status {
			continue
		}
		if after := bounds["after"]; !after.IsZero() && row.StartedAt.Before(after) {
			continue
		}
		if before := bounds["before"]; !before.IsZero() && !row.StartedAt.Before(before) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(row.AgentID+" "+row.AgentName+" "+row.Output+" "+row.Error+" "+row.RunID), q) {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}
