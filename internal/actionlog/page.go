package actionlog

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// page.go — keyset pagination for durable event reads (MU-031 criterion 4:
// "search and export are workspace-scoped, PAGINATED, and themselves audited").
//
// KEYSET, NOT OFFSET. An offset cursor skips or repeats rows whenever a write
// lands between two pages, and this table is append-only under load — so the
// page boundary moves under every reader, always. An audit trail that repeats
// records is one an investigator cannot count, and one that skips them is
// worse. The durable query already orders by (created_at DESC, id DESC), so
// that pair is the cursor.
//
// THE CURSOR IS OPAQUE, and that is a decision rather than decoration. A
// client that can construct one can ask for rows either side of a boundary it
// chose, which turns a pagination parameter into a query language nobody
// designed. Encoding it means the only cursors in circulation are ones this
// package emitted.
//
// It deliberately does NOT reach message.Event. Carrying a row id on the event
// type would put a storage detail into the SDK's wire contract and through
// TestEveryEventFieldIsClassified, for the benefit of one read path. The page
// returns its own next cursor instead: the store knows where it stopped, and
// the caller only needs to be able to say "continue".

// ErrInvalidCursor reports a cursor this package did not produce.
//
// Distinct from "no more results" on purpose. A malformed cursor is a client
// bug or a tampering attempt, and answering it with an empty page would make
// both look like the end of the data — an investigator would conclude the
// trail stopped there.
var ErrInvalidCursor = errors.New("actionlog: invalid page cursor")

// pageCursor is the position of the last row a page returned.
type pageCursor struct {
	createdAt time.Time
	id        int64
}

// cursorVersion prefixes the encoding so a future change to the key can be
// told from corruption rather than silently mis-parsed.
const cursorVersion = "v1"

func encodeCursor(c pageCursor) string {
	raw := fmt.Sprintf("%s|%d|%d", cursorVersion, c.createdAt.UTC().UnixNano(), c.id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(encoded string) (pageCursor, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return pageCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return pageCursor{}, ErrInvalidCursor
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 || parts[0] != cursorVersion {
		return pageCursor{}, ErrInvalidCursor
	}
	nanos, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return pageCursor{}, ErrInvalidCursor
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return pageCursor{}, ErrInvalidCursor
	}
	return pageCursor{createdAt: time.Unix(0, nanos).UTC(), id: id}, nil
}

// maxPageSize bounds one page. The unpaginated read clamped at 1000 and
// callers asked for whatever they liked; a page has to be small enough that
// asking for the next one is normal rather than exceptional.
const maxPageSize = 500

// QueryEventsPageInWorkspace returns one page of durable events, NEWEST FIRST,
// with the cursor for the next page.
//
// Newest-first is the opposite of QueryEventsInWorkspace, which returns its
// window oldest-first. That is not an inconsistency to tidy away: the
// unpaginated call answers "show me the recent window, in the order it
// happened", and this one answers "walk backwards through the trail". A
// paginated read that returned each page oldest-first would have the pages
// themselves in descending order, which reads as corrupted data.
//
// An empty next cursor means the end of the trail — the caller has seen
// everything matching the filter, not merely everything in this page.
func (l *Logger) QueryEventsPageInWorkspace(workspaceID, agentID, sessionID string, limit int, allowed map[string]bool, cursor string) ([]message.Event, string, error) {
	position, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	agentID = strings.TrimSpace(agentID)
	sessionID = strings.TrimSpace(sessionID)
	if limit <= 0 {
		limit = 100
	} else if limit > maxPageSize {
		limit = maxPageSize
	}

	args := []any{workspaceOrPersonal(workspaceID)}
	where := "workspace_id = ?"
	if agentID != "" {
		where += " AND agent_id = ?"
		args = append(args, agentID)
	}
	if sessionID != "" {
		where += " AND session_id = ?"
		args = append(args, sessionID)
	}
	if len(allowed) > 0 {
		placeholders := make([]string, 0, len(allowed))
		for typ := range allowed {
			typ = strings.TrimSpace(typ)
			if typ == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, typ)
		}
		if len(placeholders) > 0 {
			where += " AND type IN (" + strings.Join(placeholders, ",") + ")"
		}
	}
	if !position.createdAt.IsZero() || position.id != 0 {
		// The row-id tiebreaker is what makes this exact. Two audit records
		// written in the same millisecond are ordinary — a single request can
		// produce several — and a cursor on the timestamp alone either drops
		// the second one or returns it twice, depending on which side of the
		// comparison it falls.
		where += " AND (created_at < ? OR (created_at = ? AND id < ?))"
		// Passed as a time.Time so the driver formats both sides of the
		// comparison the same way it formatted the stored value. Formatting it
		// here by hand is how a cursor stops matching rows it should match,
		// and the symptom is a page boundary that silently drops records.
		args = append(args, position.createdAt, position.createdAt, position.id)
	}
	// One more than asked for, so "is there another page" is answered by the
	// data rather than by guessing from a full page — a page that happens to
	// end exactly on the last row would otherwise hand out a cursor that
	// returns nothing, and a client that trusts a non-empty cursor loops.
	args = append(args, limit+1)

	rows, err := l.db.Query(`
		SELECT workspace_id, agent_id, session_id, type, COALESCE(payload, ''), created_at, id
		FROM agent_events
		WHERE `+where+`
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	events := make([]message.Event, 0, limit)
	var last pageCursor
	more := false
	for rows.Next() {
		if len(events) == limit {
			more = true
			break
		}
		var ev message.Event
		var payload string
		var atRaw any
		var id int64
		if err := rows.Scan(&ev.WorkspaceID, &ev.AgentID, &ev.SessionID, &ev.Type, &payload, &atRaw, &id); err != nil {
			return nil, "", err
		}
		ev.Timestamp = parseSQLiteTime(atRaw)
		if payload != "" {
			var decoded any
			if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
				ev.Payload = decoded
			} else {
				ev.Payload = payload
			}
		}
		events = append(events, ev)
		last = pageCursor{createdAt: ev.Timestamp, id: id}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if !more {
		return events, "", nil
	}
	return events, encodeCursor(last), nil
}
