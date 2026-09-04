// deliverydoctor.go — plain-language diagnosis of channel delivery failures.
//
// When a message doesn't reach Telegram/Slack/Discord, the underlying adapter
// returns a raw provider error ("not_in_channel", "Bad Request: chat not
// found", "401 Unauthorized", "Unknown Channel (code 10003)", ...). Those are
// accurate but opaque to a non-expert operator. DiagnoseDelivery turns the
// precondition state plus the raw error into a stable category, a plain-English
// reason, and a concrete fix — the "delivery doctor" behind the Diagnose button
// on each channel mapping.
//
// This file is deliberately pure (no I/O, no gateway types) so it is trivially
// unit-tested and reused by both the API handler and the CLI doctor.

package channels

import "strings"

// DeliveryCategory is a stable, machine-readable classification of a delivery
// attempt. UIs can branch on it; humans read Reason/Fix.
type DeliveryCategory string

const (
	DeliveryOK                DeliveryCategory = "ok"
	DeliveryMissingTo         DeliveryCategory = "missing_destination"
	DeliveryAdapterDown       DeliveryCategory = "adapter_unavailable"
	DeliveryAdapterDisconnect DeliveryCategory = "adapter_disconnected"
	DeliveryBadToken          DeliveryCategory = "bad_token"
	DeliveryMissingScope      DeliveryCategory = "missing_scope"
	DeliveryBotNotInvited     DeliveryCategory = "bot_not_invited"
	DeliveryInvalidDest       DeliveryCategory = "invalid_destination"
	DeliveryForbidden         DeliveryCategory = "forbidden"
	DeliveryNetwork           DeliveryCategory = "network"
	DeliveryRateLimited       DeliveryCategory = "rate_limited"
	DeliveryUnknown           DeliveryCategory = "unknown"
)

// Diagnosis is the result of a delivery check.
type Diagnosis struct {
	OK       bool             `json:"ok"`
	Category DeliveryCategory `json:"category"`
	Reason   string           `json:"reason"`           // plain-language: what happened
	Fix      string           `json:"fix,omitempty"`    // plain-language: what to do about it
	Detail   string           `json:"detail,omitempty"` // raw provider error, for the curious
}

// DiagnoseDelivery composes the precondition state and any send error into a
// single Diagnosis. Callers pass:
//   - adapterID: the resolved adapter/mapping id (e.g. "telegram-support")
//   - to:        the destination (chat/channel id); "" means none set
//   - registered: whether the adapter is registered/running
//   - connected:  whether the adapter reports itself connected (Status)
//   - sendErr:    the error from attempting Send (nil if the send succeeded, or
//     nil if the caller only wanted the precondition checks)
//
// Precondition problems (no destination, adapter down/disconnected) are reported
// without needing an actual send error, so a mapping can be diagnosed even when
// a live send isn't attempted.
func DiagnoseDelivery(adapterID, to string, registered, connected bool, sendErr error) Diagnosis {
	if strings.TrimSpace(to) == "" && sendErr == nil {
		return Diagnosis{
			Category: DeliveryMissingTo,
			Reason:   "No destination is set for this mapping, so there is nowhere to deliver the message.",
			Fix:      "Set a destination (chat/channel ID) on the mapping, or configure a default outbound destination for scheduled sends.",
		}
	}
	if !registered {
		return Diagnosis{
			Category: DeliveryAdapterDown,
			Reason:   "The adapter for this channel is not running. It may be disabled, or the config changed since the gateway last started.",
			Fix:      "Enable the channel and restart the gateway (`sy daemon stop && sy daemon start`), then diagnose again.",
		}
	}
	if sendErr == nil {
		if !connected {
			return Diagnosis{
				Category: DeliveryAdapterDisconnect,
				Reason:   "The adapter is loaded but not currently connected to the provider.",
				Fix:      "Check the bot token and network access; the adapter should reconnect automatically once reachable.",
			}
		}
		return Diagnosis{OK: true, Category: DeliveryOK, Reason: "Delivery succeeded."}
	}

	d := ClassifyDeliveryErrorForAdapter(adapterID, sendErr.Error())
	d.Detail = sendErr.Error()
	return d
}

// ClassifyDeliveryError maps a raw adapter error string to a plain-language
// category/reason/fix. It matches on the stable substrings the Telegram, Slack,
// and Discord adapters preserve from provider responses. Ordering matters:
// more specific signals (missing scope, not-invited) are checked before the
// broad auth/not-found buckets.
func ClassifyDeliveryError(raw string) Diagnosis {
	return ClassifyDeliveryErrorForAdapter("", raw)
}

// ClassifyDeliveryErrorForAdapter is the adapter-aware form used by live sends.
// It keeps the generic classifier stable for tests while allowing richer fixes
// for platform-specific errors that otherwise look identical (HTTP 400/403/404).
func ClassifyDeliveryErrorForAdapter(adapterID, raw string) Diagnosis {
	s := strings.ToLower(raw)
	adapter := strings.ToLower(strings.TrimSpace(adapterID))

	contains := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(s, sub) {
				return true
			}
		}
		return false
	}
	isEmail := func() bool { return strings.Contains(adapter, "email") || strings.Contains(adapter, "smtp") }
	isWebhookLike := func() bool {
		// Story 4 sweep: email is intentionally NOT in the webhook family. It
		// speaks SMTP, so a "regenerate the webhook URL" fix would be wrong.
		// Email keeps its own classification path below.
		return strings.Contains(adapter, "webhook") ||
			strings.Contains(adapter, "teams") ||
			strings.Contains(adapter, "google_chat") ||
			strings.Contains(adapter, "google-chat")
	}

	// Email/SMTP-specific classification — SMTP status codes don't overlap
	// cleanly with the HTTP-status buckets below, so handle them first. See
	// RFC 5321 §4.2 for status ranges: 5xx = permanent, 4xx = transient.
	//
	// Ordering rule: the substring matcher is first-match-wins, so more
	// specific patterns must come BEFORE more generic ones. STARTTLS (530)
	// and the enhanced-status codes overlap with the generic auth-failure
	// heuristic (5.7.0 is a general "security policy" marker that servers
	// emit for BOTH auth AND STARTTLS-required responses), so the specific
	// 530-prefixed check has to run first. Do not reorder alphabetically.
	if isEmail() {
		switch {
		case contains("530 5.7.0", "must issue a starttls", "must be tls", "starttls required"):
			return Diagnosis{
				Category: DeliveryBadToken,
				Reason:   "The SMTP server requires TLS but the connection is unencrypted.",
				Fix:      "Set TLS to `starttls` (port 587) or `implicit` (port 465) in the email channel settings.",
			}
		// Auth failure — 535 is the RFC-canonical auth code; 5.7.8 is the
		// enhanced-status marker specific to authentication (RFC 3463 §3.7).
		// The bare 5.7.0 marker was intentionally REMOVED from this branch —
		// it also matches the STARTTLS-required 530 5.7.0 case above, which
		// is why the reorder alone isn't sufficient. Text tokens
		// ("authentication", "credentials") still catch every real auth path.
		case contains("535 ", "535-", "5.7.8", "authentication failed", "auth failed", "authentication credentials", "invalid credentials"):
			return Diagnosis{
				Category: DeliveryBadToken,
				Reason:   "The SMTP server rejected the mailbox credentials.",
				Fix:      "Update the SMTP username and password (for Gmail use an app password, not the account password), save, and restart the gateway.",
			}
		case contains("550 5.7.1", "spf fail", "dmarc", "dkim=fail", "not authenticated", "not permitted to send", "relay access denied", "relay not permitted", "relaying denied"):
			return Diagnosis{
				Category: DeliveryForbidden,
				Reason:   "The SMTP server refused to relay this message — the From address, sender IP, or SPF/DKIM/DMARC alignment failed policy.",
				Fix:      "Send From an address the SMTP server is authorized to relay for, and verify SPF/DKIM/DMARC records for your sending domain.",
			}
		case contains("550 5.1.1", "550 5.1.2", "550 5.1.3", "user unknown", "recipient rejected", "no such user", "553 5.1.3", "mailbox unavailable", "does not exist"):
			return Diagnosis{
				Category: DeliveryInvalidDest,
				Reason:   "The SMTP server rejected the recipient address — the mailbox does not exist or is not accepting mail.",
				Fix:      "Verify the recipient (msg.ThreadID / msg.Metadata[\"to\"] / default_output_to) is a real, deliverable address.",
			}
		case contains("421 ", "421-", "452 4.5.3", "552 5.3.4", "quota exceeded", "over quota", "too many recipients", "too much mail"):
			return Diagnosis{
				Category: DeliveryRateLimited,
				Reason:   "The SMTP server throttled or refused this send (rate limit, mailbox quota, or too many recipients).",
				Fix:      "Slow the send cadence, split large batches, or route through a transactional provider (Postmark/SES/SendGrid) with a higher quota.",
			}
		case contains("554 5.7.1", "message rejected", "550 5.7.26", "554 "):
			return Diagnosis{
				Category: DeliveryForbidden,
				Reason:   "The SMTP server rejected the message content, likely for policy or reputation reasons.",
				Fix:      "Check whether the sending domain is on a deny list, the body contains blocked terms, or the From address is unauthenticated; try a different sending domain if the issue persists.",
			}
		case contains("tls handshake", "certificate", "x509", "tls: bad record", "tls handshake failed"):
			return Diagnosis{
				Category: DeliveryNetwork,
				Reason:   "The TLS handshake with the SMTP server failed.",
				Fix:      "Verify the host/port/tls combination (587=starttls, 465=implicit), and that outbound TLS to the provider is not blocked.",
			}
		}
	}

	switch {
	// Missing OAuth scope (Slack) — check before generic auth.
	case contains("missing_scope", "missing scope", "not_allowed_token_type"):
		return Diagnosis{
			Category: DeliveryMissingScope,
			Reason:   "The bot token is valid but is missing a permission (scope) needed to post here.",
			Fix:      "Add the `chat:write` scope (and `chat:write.public` for channels the bot isn't in) to the Slack app, then reinstall the app to your workspace.",
		}

	// Bot not a member of the channel/group.
	case contains("not_in_channel", "not in channel", "not a member", "is not a member", "user_not_in_channel"):
		return Diagnosis{
			Category: DeliveryBotNotInvited,
			Reason:   "The bot has not been added to this channel, so it cannot post there.",
			Fix:      "Invite the app/bot to the destination (Slack: `/invite @yourbot`; Telegram/Discord: add the bot to the group/channel), then try again.",
		}

	// Bad / rotated / missing token.
	case contains("unauthorized", "invalid_auth", "invalid auth", "invalid token", "token_revoked", "account_inactive", "401"):
		if isWebhookLike() {
			return Diagnosis{
				Category: DeliveryBadToken,
				Reason:   "The webhook endpoint rejected the request. The URL may be expired, revoked, or from a different workspace.",
				Fix:      "Regenerate the incoming webhook URL in the provider, paste it into the channel settings, save, restart the gateway, and run the delivery test again.",
			}
		}
		return Diagnosis{
			Category: DeliveryBadToken,
			Reason:   "The provider rejected the bot token — it is missing, wrong, or has been revoked.",
			Fix:      "Update the bot token in the vault with a current, valid token, then restart the gateway.",
		}

	// Destination doesn't exist (or bot can't see it). Telegram "chat not found",
	// Slack "channel_not_found", Discord "Unknown Channel".
	case contains("chat not found", "channel_not_found", "unknown channel", "unknown_channel", "chat_not_found", "peer_id_invalid", "user not found", "not found", "404"):
		switch {
		case strings.Contains(adapter, "telegram"):
			return Diagnosis{
				Category: DeliveryInvalidDest,
				Reason:   "Telegram could not find this chat, or the bot is not allowed to see it.",
				Fix:      "For DMs, open the bot and send `/start`. For groups/channels, add the bot, grant post access, then use the numeric chat id (channels and supergroups usually start with `-100`).",
			}
		case strings.Contains(adapter, "slack"):
			return Diagnosis{
				Category: DeliveryInvalidDest,
				Reason:   "Slack could not find this channel, or the app cannot see it.",
				Fix:      "Use the Slack channel ID (usually `C...` or `G...`), invite the app to the channel, and confirm the app was installed in the same workspace.",
			}
		case strings.Contains(adapter, "discord"):
			return Diagnosis{
				Category: DeliveryInvalidDest,
				Reason:   "Discord could not find this channel, or the bot cannot access it.",
				Fix:      "Use the numeric Discord channel ID, invite the bot to the server, and grant it View Channel and Send Messages permissions.",
			}
		case isWebhookLike():
			return Diagnosis{
				Category: DeliveryInvalidDest,
				Reason:   "The configured webhook URL no longer points to a valid destination.",
				Fix:      "Create a fresh incoming webhook for this destination, update the channel settings, save, restart the gateway, and test delivery.",
			}
		}
		return Diagnosis{
			Category: DeliveryInvalidDest,
			Reason:   "The destination ID is invalid, or the bot cannot see that chat/channel.",
			Fix:      "Double-check the destination ID and confirm the bot/app has access to that destination.",
		}

	// Forbidden / blocked / lacks permission (but token itself is valid).
	case contains("forbidden", "403", "missing permissions", "missing access", "bot was blocked", "cannot initiate conversation", "not enough rights"):
		switch {
		case strings.Contains(adapter, "telegram"):
			return Diagnosis{
				Category: DeliveryForbidden,
				Reason:   "Telegram accepted the bot token but refused delivery to this chat.",
				Fix:      "Unblock the bot for DMs, or add it back to the group/channel and grant permission to post.",
			}
		case strings.Contains(adapter, "slack"):
			return Diagnosis{
				Category: DeliveryForbidden,
				Reason:   "Slack accepted the token but the app is not permitted to post here.",
				Fix:      "Invite the app to the channel and verify it has `chat:write` plus any workspace-required posting permissions.",
			}
		case strings.Contains(adapter, "discord"):
			return Diagnosis{
				Category: DeliveryForbidden,
				Reason:   "Discord accepted the bot token but denied access to this channel.",
				Fix:      "Grant the bot View Channel and Send Messages permissions in that channel, then test again.",
			}
		case isWebhookLike():
			return Diagnosis{
				Category: DeliveryForbidden,
				Reason:   "The webhook exists but refused the post.",
				Fix:      "Check whether the webhook was disabled, deleted, or restricted by workspace policy; regenerate it if needed.",
			}
		}
		return Diagnosis{
			Category: DeliveryForbidden,
			Reason:   "The bot is authenticated but is not permitted to post to this destination (blocked, or lacking the right permissions).",
			Fix:      "Grant the bot permission to post in this channel, or ensure the user hasn't blocked the bot.",
		}

	// Provider throttling.
	case contains("too many requests", "rate_limited", "rate limited", "retry_after", "429"):
		return Diagnosis{
			Category: DeliveryRateLimited,
			Reason:   "The provider is throttling outbound messages for this bot or webhook.",
			Fix:      "Wait for the provider retry window, reduce burst sends, or route scheduled reports through a less busy channel mapping.",
		}

	// Generic bad request — usually a malformed chat/channel id on Telegram.
	case contains("bad request", "400"):
		if strings.Contains(adapter, "telegram") {
			return Diagnosis{
				Category: DeliveryInvalidDest,
				Reason:   "Telegram rejected the delivery request, usually because the chat id is malformed or the bot cannot address it yet.",
				Fix:      "Use the numeric chat id, not the bot username. For channels/groups, add the bot first and use the `-100...` id; for DMs, send `/start` to the bot.",
			}
		}
		return Diagnosis{
			Category: DeliveryInvalidDest,
			Reason:   "The provider rejected the request — most often a malformed or wrong destination ID.",
			Fix:      "Verify the destination ID format for this channel and try again.",
		}

	// Transport-level failure.
	case contains("timeout", "deadline exceeded", "connection refused", "no such host", "dial tcp", "eof", "network is unreachable"):
		return Diagnosis{
			Category: DeliveryNetwork,
			Reason:   "The message could not be sent because the provider was unreachable (a network or connectivity problem).",
			Fix:      "Check outbound network access to the provider and retry; if it persists, verify the provider isn't down.",
		}

	default:
		return Diagnosis{
			Category: DeliveryUnknown,
			Reason:   "Delivery failed for a reason the doctor didn't recognize. The raw provider error is included below.",
			Fix:      "Check the raw error detail and the channel setup guide for this provider.",
		}
	}
}
