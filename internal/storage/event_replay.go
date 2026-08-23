package storage

import (
	"context"

	"github.com/soulacy/soulacy/pkg/message"
)

type CursorEvent struct {
	ID    uint64
	Event message.Event
}

type EventReplayWindow struct {
	Events  []CursorEvent
	Oldest  uint64
	Latest  uint64
	HasMore bool
}

// DurableEventReplay is implemented by the shared action log. Cursor IDs are
// database positions, not replica-local counters.
type DurableEventReplay interface {
	ReplayEvents(context.Context, string, uint64, int) (EventReplayWindow, error)
	LatestEventCursor(context.Context, string) (uint64, error)
}
