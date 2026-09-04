package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/soulacy/soulacy/internal/channels"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// buildChannelSendBuiltin sends a canonical outbound message through any
// registered channel adapter: Telegram, Slack, Discord, Email, Teams,
// Google Chat, WhatsApp, webhooks, sidecars, etc.
func (e *Engine) buildChannelSendBuiltin() BuiltinTool {
	return BuiltinTool{
		Name:        "channel.send",
		Gate:        "",
		Description: "Send text to a configured outbound channel adapter. Use for final delivery to Telegram, Slack, Discord, Email, Teams, Google Chat, WhatsApp, webhooks, or a sidecar channel. In interactive runs, channel and to default to the inbound channel/chat. Scheduled jobs usually use the configured default output, so pass only text unless you are overriding the route. Prefer text for the message body; message/body/content are accepted as compatibility aliases.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel": map[string]any{
					"type":        "string",
					"description": "Registered channel adapter id, such as telegram, slack, discord, email, teams, google_chat, webhook, whatsapp, or an agent-specific mapping id. Optional in interactive channel runs; defaults to the inbound adapter id.",
				},
				"adapter": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for channel. Prefer channel.",
				},
				"to": map[string]any{
					"type":        "string",
					"description": "Destination id for the platform: Telegram chat id, Slack/Discord channel id, email address, phone number, or sidecar thread id. Optional when the inbound route or default_output_to is configured. Webhook/Teams/Google Chat usually do not need to.",
				},
				"destination": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for to.",
				},
				"target": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for to.",
				},
				"recipient": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for to.",
				},
				"chat_id": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for to, useful for Telegram chat ids.",
				},
				"channel_id": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for to, useful for Slack/Discord channel ids.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "Message body to send",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for text",
				},
				"msg": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for text",
				},
				"body": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for text",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for text",
				},
				"metadata": map[string]any{
					"type":        "object",
					"description": "Optional string metadata passed through to the channel adapter",
				},
			},
			"required": []string{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			channelID := argStringFirst(args, "channel", "adapter", "adapter_id", "platform")
			to := argStringFirst(args, "to", "destination", "target", "recipient", "chat_id", "channel_id", "thread_id", "user_id", "conversation", "room")
			text := argStringFirst(args, "text", "message", "msg", "body", "content")
			routeSource := "explicit"
			if channelID == "" || to == "" {
				routeSource = "partial-explicit"
			}
			if inbound, ok := ctx.Value(inboundMsgKey{}).(message.Message); ok {
				if channelID == "" {
					channelID = strings.TrimSpace(inbound.Channel)
					routeSource = "inbound"
				}
				if to == "" {
					to = strings.TrimSpace(inbound.ThreadID)
					routeSource = "inbound"
				}
			}
			defaultOut, hasDefault := e.channelDefaultOutput(channelID)
			if channelID == "" {
				defaultOut, hasDefault = e.onlyChannelDefaultOutput()
				if hasDefault {
					channelID = strings.TrimSpace(defaultOut.Channel)
					routeSource = "default"
				}
			}
			if hasDefault {
				if defaultOut.Channel != "" {
					channelID = strings.TrimSpace(defaultOut.Channel)
					if routeSource == "partial-explicit" {
						routeSource = "default"
					}
				}
				if to == "" {
					to = strings.TrimSpace(defaultOut.To)
					routeSource = "default"
				}
			}
			if channelID == "" {
				return "", fmt.Errorf("channel.send: channel is required when there is no inbound channel context and no single default outbound channel is configured")
			}
			if to == "" && channelSendRequiresDestination(channelID) {
				return "", fmt.Errorf("channel.send: to is required when there is no inbound chat/thread context and no default_output_to is configured for channel %q", channelID)
			}
			if text == "" {
				return "", fmt.Errorf("channel.send: text is required")
			}
			if e.channelRegistry == nil {
				return "", fmt.Errorf("channel.send: channel registry is unavailable")
			}
			if _, ok := e.channelRegistry.Statuses()[channelID]; !ok {
				return "", fmt.Errorf("channel.send: channel %q is not registered", channelID)
			}

			meta := map[string]string{
				"tool": "channel.send",
			}
			if rawMeta, ok := args["metadata"].(map[string]any); ok {
				for k, v := range rawMeta {
					ks := strings.TrimSpace(k)
					if ks == "" {
						continue
					}
					meta[ks] = strings.TrimSpace(fmt.Sprint(v))
				}
			}

			out := message.Message{
				ID:        uuid.New().String(),
				SessionID: "channel-send-" + uuid.New().String(),
				AgentID:   "channel.send",
				Channel:   channelID,
				ThreadID:  to,
				UserID:    "agent",
				Username:  "agent",
				Role:      message.RoleAssistant,
				Parts:     message.Text(text),
				Metadata:  meta,
				CreatedAt: time.Now().UTC(),
			}
			if err := e.channelRegistry.Send(ctx, out); err != nil {
				status := e.channelRegistry.Statuses()[channelID]
				diag := channels.DiagnoseDelivery(channelID, to, true, status.Connected, err)
				return "", fmt.Errorf(
					"channel.send: send failed through channel %q to %q: %w; diagnosis category=%s reason=%s fix=%s",
					channelID,
					to,
					err,
					diag.Category,
					diag.Reason,
					diag.Fix,
				)
			}
			b, _ := json.Marshal(map[string]any{
				"ok":           true,
				"delivered":    true,
				"channel":      channelID,
				"to":           to,
				"route_source": routeSource,
				"text_preview": previewText(text, 160),
			})
			return string(b), nil
		},
	}
}

// buildChannelStatusBuiltin lets agents inspect the exact outbound route that
// channel.send would use before spending steps on trial-and-error sends.
func (e *Engine) buildChannelStatusBuiltin() BuiltinTool {
	return BuiltinTool{
		Name:        "channel.status",
		Gate:        "",
		Description: "Diagnose whether a configured outbound channel route is ready. Use before retrying channel.send failures or when you need to know the resolved adapter, destination, defaults, and connection state. Does not send a message.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel": map[string]any{
					"type":        "string",
					"description": "Channel adapter id such as telegram, slack, discord, email, teams, google_chat, webhook, or an agent-specific mapping id. Optional when an inbound channel or single default outbound channel is available.",
				},
				"adapter": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for channel.",
				},
				"to": map[string]any{
					"type":        "string",
					"description": "Destination id such as a Telegram chat id, Slack/Discord channel id, email address, or thread id. Optional when an inbound route or configured default output exists. Webhook/Teams/Google Chat usually do not need to.",
				},
				"destination": map[string]any{
					"type":        "string",
					"description": "Compatibility alias for to.",
				},
				"include_channels": map[string]any{
					"type":        "boolean",
					"description": "When true, include registered channel ids and configured defaults in the response.",
				},
			},
			"required": []string{},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			channelID, to, routeSource := e.resolveChannelRoute(ctx, args)
			requiresTo := channelSendRequiresDestination(channelID)
			statuses := map[string]channels.AdapterStatus{}
			if e.channelRegistry != nil {
				statuses = e.channelRegistry.Statuses()
			}
			status, registered := statuses[channelID]
			connected := registered && status.Connected
			diag := channels.DiagnoseDelivery(channelID, to, registered, connected, nil)
			if channelID == "" {
				diag = channels.Diagnosis{
					Category: channels.DeliveryAdapterDown,
					Reason:   "No outbound channel could be resolved from the request, inbound context, or configured defaults.",
					Fix:      "Pass channel explicitly, run from an interactive channel, or configure exactly one default outbound channel.",
				}
			} else if !requiresTo && to == "" && registered {
				diag = channels.Diagnosis{OK: connected, Category: channels.DeliveryOK, Reason: "The channel route is configured and does not require a destination."}
				if !connected {
					diag = channels.DiagnoseDelivery(channelID, "webhook", registered, connected, nil)
				}
			}
			response := map[string]any{
				"ok":                   diag.OK,
				"channel":              channelID,
				"to":                   to,
				"route_source":         routeSource,
				"registered":           registered,
				"connected":            connected,
				"requires_destination": requiresTo,
				"diagnosis":            diag,
			}
			if status.Detail != "" {
				response["detail"] = status.Detail
			}
			if argBool(args, "include_channels") || !diag.OK {
				response["registered_channels"] = sortedChannelIDs(statuses)
				response["default_outputs"] = e.channelDefaultOutputSnapshot()
			}
			b, _ := json.Marshal(response)
			return string(b), nil
		},
	}
}

func (e *Engine) resolveChannelRoute(ctx context.Context, args map[string]any) (string, string, string) {
	channelID := argStringFirst(args, "channel", "adapter", "adapter_id", "platform")
	to := argStringFirst(args, "to", "destination", "target", "recipient", "chat_id", "channel_id", "thread_id", "user_id", "conversation", "room")
	routeSource := "explicit"
	if channelID == "" || to == "" {
		routeSource = "partial-explicit"
	}
	if inbound, ok := ctx.Value(inboundMsgKey{}).(message.Message); ok {
		if channelID == "" {
			channelID = strings.TrimSpace(inbound.Channel)
			routeSource = "inbound"
		}
		if to == "" {
			to = strings.TrimSpace(inbound.ThreadID)
			routeSource = "inbound"
		}
	}
	defaultOut, hasDefault := e.channelDefaultOutput(channelID)
	if channelID == "" {
		defaultOut, hasDefault = e.onlyChannelDefaultOutput()
		if hasDefault {
			channelID = strings.TrimSpace(defaultOut.Channel)
			routeSource = "default"
		}
	}
	if hasDefault {
		if defaultOut.Channel != "" {
			channelID = strings.TrimSpace(defaultOut.Channel)
			if routeSource == "partial-explicit" {
				routeSource = "default"
			}
		}
		if to == "" {
			to = strings.TrimSpace(defaultOut.To)
			routeSource = "default"
		}
	}
	return channelID, to, routeSource
}

func sortedChannelIDs(statuses map[string]channels.AdapterStatus) []string {
	out := make([]string, 0, len(statuses))
	for id := range statuses {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (e *Engine) channelDefaultOutputSnapshot() map[string]map[string]string {
	e.channelDefaultMu.RLock()
	defer e.channelDefaultMu.RUnlock()
	out := make(map[string]map[string]string, len(e.channelDefaults))
	for id, def := range e.channelDefaults {
		row := map[string]string{}
		if def.Channel != "" {
			row["channel"] = def.Channel
		}
		if def.To != "" {
			row["to"] = def.To
		}
		if def.BotName != "" {
			row["bot_name"] = def.BotName
		}
		out[id] = row
	}
	return out
}

func argStringFirst(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := strings.TrimSpace(argString(args, key)); s != "" {
			return s
		}
	}
	return ""
}

func channelSendRequiresDestination(channelID string) bool {
	switch strings.TrimSpace(channelID) {
	case "webhook":
		return false
	default:
		return true
	}
}

func previewText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	prefix := s[:max-1]
	for !utf8.ValidString(prefix) && len(prefix) > 0 {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + "…"
}

func (e *Engine) channelDefaultOutput(channelID string) (agent.ScheduleOutput, bool) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return agent.ScheduleOutput{}, false
	}
	e.channelDefaultMu.RLock()
	defer e.channelDefaultMu.RUnlock()
	out, ok := e.channelDefaults[channelID]
	if !ok {
		return agent.ScheduleOutput{}, false
	}
	if out.Channel == "" {
		out.Channel = channelID
	}
	return out, true
}

func (e *Engine) onlyChannelDefaultOutput() (agent.ScheduleOutput, bool) {
	e.channelDefaultMu.RLock()
	defer e.channelDefaultMu.RUnlock()
	var out agent.ScheduleOutput
	count := 0
	for channelID, candidate := range e.channelDefaults {
		if strings.TrimSpace(candidate.To) == "" {
			continue
		}
		if candidate.Channel == "" {
			candidate.Channel = channelID
		}
		out = candidate
		count++
		if count > 1 {
			return agent.ScheduleOutput{}, false
		}
	}
	return out, count == 1
}
