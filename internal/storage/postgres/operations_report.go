package postgres

import (
	"context"
	"time"

	"github.com/soulacy/soulacy/internal/actionlog"
)

func (a *ActionLog) OperationsActivity(ctx context.Context, start, end time.Time) (actionlog.OperationsActivity, error) {
	rows, err := a.pool.Query(ctx, actionlog.OperationsActivitySQL, start.UTC(), end.UTC())
	if err != nil {
		return actionlog.OperationsActivity{}, err
	}
	defer rows.Close()
	return actionlog.ReadOperationsActivity(rows)
}
