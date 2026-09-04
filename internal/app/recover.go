// recover.go — startup crash-recovery for in-flight agent runs.
//
// PRODUCTION_AUDIT — F2 (2026-05-27): the channel inbox is in-memory.
// If the host crashes, reboots, or `kill -9`s the gateway between
// "message.in" being logged and "message.out" being logged, the inbound
// message is lost — Telegram/Slack/Discord have already POSTed it; the
// adapter has already acked it; the engine never produced a reply.
//
// This routine reopens the actionlog at boot, asks it for every
// message.in within the recovery window that has no outcome event after
// it, and re-enqueues those messages onto the live inbox so the worker
// pool resumes them. The actionlog stores the full original message
// payload (engine emits it as Event.Payload), so the recovery only has
// to unmarshal + push.
//
// Idempotency: a re-enqueued message produces a NEW message.in event when
// the engine handles it this time around. The next recovery pass will see
// THAT outcome and not re-enqueue again. So a single recovery cycle is
// safe to run unconditionally on every boot.
//
// Bounded: only messages whose channel is NOT "http" are recovered.
// HTTP messages are synchronous request/response — the original client
// has long since timed out, so retrying would only send replies into
// /dev/null. Scheduler/cron triggers re-fire naturally and don't need
// recovery.

package app

import (
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/storage"
	"github.com/soulacy/soulacy/pkg/message"
)

// recoveryWindow is how far back the boot scan looks for unfinished runs.
// Anything older is considered stale (the originating user has moved on)
// and skipped. One hour is generous for typical "host restarted" scenarios
// without re-litigating ancient messages.
const recoveryWindow = 1 * time.Hour

// maxReplayAttempts is the poison-pill threshold. If a message has been
// started (message.in event) this many times without ever producing a
// message.out or error outcome, we assume it is crashing the engine on
// every attempt. We mark it dead-lettered and skip it permanently so it
// doesn't loop forever across gateway restarts.
const maxReplayAttempts = 3

// replayIncompleteRuns is called once at boot, after the channel registry
// has been started (so chanReg.Enqueue's inbox has consumers) and before
// the worker pool drains the inbox. Returns the number of messages
// successfully re-enqueued + the number dropped because of a full buffer.
func replayIncompleteRuns(actions storage.ActionLogBackend, chanReg *channels.Registry, log *zap.Logger) (replayed, dropped int) {
	if actions == nil || chanReg == nil {
		return 0, 0
	}
	since := time.Now().Add(-recoveryWindow)
	payloads, err := actions.IncompleteMessageIns(since)
	if err != nil {
		log.Warn("crash recovery: query failed; skipping replay", zap.Error(err))
		return 0, 0
	}
	var poisoned int
	for _, raw := range payloads {
		var msg message.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Warn("crash recovery: bad message.in payload, skipping",
				zap.Error(err))
			continue
		}
		// Skip HTTP — synchronous channel, no original requester left to
		// receive the reply. Skip scheduler-generated runs — they re-fire
		// on cadence naturally. (See comment at the top of this file.)
		if msg.Channel == "" || msg.Channel == "http" || msg.Channel == "cron" || msg.Channel == "internal" {
			continue
		}

		// Poison-pill guard: count how many times this (agent, session) has
		// already been attempted within the recovery window. If it has hit
		// the max, it's crashing the engine on every boot — quarantine it.
		attempts, cerr := actions.CountMessageInAttempts(msg.AgentID, msg.SessionID, since)
		if cerr != nil {
			log.Warn("crash recovery: attempt count query failed; allowing replay",
				zap.String("session", msg.SessionID), zap.Error(cerr))
		} else if attempts >= maxReplayAttempts {
			reason := fmt.Sprintf("reached %d crash-recovery attempts without completing", attempts)
			if dlErr := actions.MarkDeadLetter(msg.AgentID, msg.SessionID, reason); dlErr != nil {
				log.Error("crash recovery: failed to mark dead letter",
					zap.String("session", msg.SessionID), zap.Error(dlErr))
			} else {
				log.Warn("crash recovery: session quarantined (poison-pill)",
					zap.String("agent", msg.AgentID),
					zap.String("session", msg.SessionID),
					zap.Int("attempts", attempts),
					zap.String("msg_id", msg.ID),
				)
			}
			poisoned++
			continue
		}

		if chanReg.Enqueue(msg) {
			replayed++
		} else {
			dropped++
		}
	}
	if replayed > 0 || dropped > 0 || poisoned > 0 {
		log.Info("crash recovery: replayed in-flight runs",
			zap.Int("replayed", replayed),
			zap.Int("dropped_inbox_full", dropped),
			zap.Int("quarantined_poison_pill", poisoned),
			zap.Duration("window", recoveryWindow),
		)
	}
	return replayed, dropped
}
