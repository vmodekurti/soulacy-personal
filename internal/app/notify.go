// notify.go — runtime.FailureNotifier implementation backed by the
// channel registry. Wired into the engine in main.go after chanReg is
// up so a run error in the engine results in a real outbound message on
// the operator's preferred channel, not just a line in an action log
// that nobody is watching.
//
// PRODUCTION_AUDIT — F5 follow-up (2026-05-28): until this lands, cron
// agents that errored produced exactly one trace ("✖ ERROR") that no
// human ever saw unless they actively opened the Activity feed. After:
//   • def.NotifyOnFailure → engine sends a templated heads-up to the
//     declared channel + recipient (the typical "send me a Telegram
//     ping when the daily podcast fails" case).
//   • inbound channel != http/internal AND no explicit NotifyOnFailure
//     → engine replies on the same channel so the user who messaged the
//     agent sees the error instead of silence.
//   • Otherwise → still nothing on the wire, but we log a warn so the
//     operator at least sees it in /tmp/soulacy.log.

package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/internal/webpush"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

type failureNotifier struct {
	chanReg *channels.Registry
	log     *zap.Logger
}

const defaultFailureTemplate = "🚨 Soulacy agent {agent_id} failed at {timestamp}: {error}"

// truncateForPush keeps push bodies short (notifications are clipped by the OS
// anyway) and single-line.
func truncateForPush(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 140 {
		return s[:137] + "…"
	}
	return s
}

// NotifyFailure satisfies runtime.FailureNotifier. Logs every call (so the
// audit trail in /tmp/soulacy.log is consistent regardless of channel
// configuration) and either fans out to the declared notify_on_failure
// channel, or falls back to replying on the inbound channel.
func (f *failureNotifier) NotifyFailure(ctx context.Context, def *agent.Definition, inbound message.Message, errMsg string) {
	// Always log — operators reading /tmp/soulacy.log shouldn't have to
	// hunt for failures in the per-agent JSONL too.
	f.log.Warn("agent run failed",
		zap.String("agent", def.ID),
		zap.String("session", inbound.SessionID),
		zap.String("trigger_channel", inbound.Channel),
		zap.String("error", errMsg),
	)

	// Fire a push to any paired mobile devices (Epic 8). Best-effort and a no-op
	// when push isn't configured; independent of the channel-delivery path below.
	pushAgentName := def.Name
	if pushAgentName == "" {
		pushAgentName = def.ID
	}
	webpush.NotifyDefault(webpush.Notification{
		Title: "Agent run failed",
		Body:  fmt.Sprintf("%s: %s", pushAgentName, truncateForPush(errMsg)),
		URL:   "/#mobile",
		Tag:   "fail-" + def.ID,
	})

	channelID, to, body, ok := f.resolveTarget(def, inbound, errMsg)
	if !ok {
		f.log.Warn("agent failed; no notify_on_failure configured and inbound channel is non-routable — failure recorded in actionlog only",
			zap.String("agent", def.ID),
			zap.String("inbound_channel", inbound.Channel),
			zap.String("hint", "add notify_on_failure: {channel: telegram, to: \"...\"} to the agent's SOUL.yaml"),
		)
		return
	}

	out := message.Message{
		ID:        uuid.New().String(),
		SessionID: fmt.Sprintf("failure-%s", inbound.SessionID),
		AgentID:   def.ID,
		Channel:   channelID,
		ThreadID:  to,
		UserID:    to,
		Username:  to,
		Role:      message.RoleAssistant,
		Parts:     message.Text(body),
		CreatedAt: time.Now().UTC(),
	}
	if err := f.chanReg.Send(ctx, out); err != nil {
		f.log.Warn("failure notifier: chanReg.Send failed",
			zap.String("agent", def.ID),
			zap.String("channel", channelID),
			zap.String("to", to),
			zap.Error(err),
		)
	}
}

// resolveTarget encapsulates the routing decision so it's unit-testable
// in isolation if we ever add a notify_test.go. Returns ok=false when
// there's no actionable destination (cron / manual-http with no
// notify_on_failure block); the caller logs and stops.
func (f *failureNotifier) resolveTarget(def *agent.Definition, inbound message.Message, errMsg string) (channelID, to, body string, ok bool) {
	// 1. Explicit notify_on_failure declaration wins.
	if def.NotifyOnFailure != nil && def.NotifyOnFailure.Channel != "" && def.NotifyOnFailure.To != "" {
		tmpl := def.NotifyOnFailure.Template
		if tmpl == "" {
			tmpl = defaultFailureTemplate
		}
		body = renderFailureTemplate(tmpl, def, errMsg)
		// IncludeError defaults to true semantically — if the template
		// doesn't reference {error}, append it so the operator never gets
		// a "something broke" alert without the actual breakage text.
		if def.NotifyOnFailure.IncludeError && !strings.Contains(tmpl, "{error}") {
			body = body + "\n\n" + errMsg
		}
		return def.NotifyOnFailure.Channel, def.NotifyOnFailure.To, body, true
	}

	// 2. For inbound channel triggers, reply on the same channel so the
	// originating user sees the error instead of silence. The HTTP and
	// internal channels have no human recipient on the other side — HTTP
	// surfaces the error directly in the API response; internal/cron has
	// nobody waiting on a reply.
	if inbound.Channel != "" && inbound.Channel != "http" && inbound.Channel != "internal" && inbound.UserID != "" {
		body = "Sorry — something went wrong handling that: " + errMsg
		return inbound.Channel, inbound.UserID, body, true
	}

	return "", "", "", false
}

func renderFailureTemplate(tmpl string, def *agent.Definition, errMsg string) string {
	repl := strings.NewReplacer(
		"{agent_id}", def.ID,
		"{agent_name}", def.Name,
		"{timestamp}", time.Now().UTC().Format(time.RFC3339),
		"{error}", errMsg,
	)
	return repl.Replace(tmpl)
}
