package channels

import "strings"

// ActivationPolicy controls which inbound channel messages may trigger agents.
// Empty allowlists mean "allow any"; an empty trigger phrase means no phrase
// gate. Callers should pass the deployment default explicitly.
type ActivationPolicy struct {
	TriggerPhrase    string
	IgnoreGroups     bool
	AllowedThreadIDs []string
	AllowedUserIDs   []string
	// DirectActivatesWithoutPhrase makes a 1:1 (non-group) message activate the
	// agent WITHOUT the trigger phrase. True for real chat platforms (Telegram,
	// Slack, Discord, WhatsApp) where a DM is unambiguously for the bot; false
	// for generic/external channels that have no DM concept and use the trigger
	// phrase as a plain filter.
	DirectActivatesWithoutPhrase bool
}

// Apply returns the text to pass to the agent and whether the message is
// allowed. When a trigger phrase is configured, the phrase is stripped before
// dispatch so agents see the user's actual request.
//
// The trigger phrase gates GROUP chats only. Its purpose is to stop the bot from
// replying to every message in a busy channel — which is meaningless in a direct
// (private) message, where every message is unambiguously addressed to the bot.
// So a DM always activates without any prefix; requiring one there was a silent
// trap that dropped normal messages with no reply.
func (p ActivationPolicy) Apply(text, threadID, userID string, isGroup bool) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	if p.IgnoreGroups && isGroup {
		return "", false
	}
	if !containsOrEmpty(p.AllowedThreadIDs, threadID) {
		return "", false
	}
	if !containsOrEmpty(p.AllowedUserIDs, userID) {
		return "", false
	}
	trigger := strings.TrimSpace(p.TriggerPhrase)
	// No phrase configured, or a direct message on a chat platform: always
	// activate. The phrase is still stripped if the user happened to include it.
	if trigger == "" || (!isGroup && p.DirectActivatesWithoutPhrase) {
		return strings.TrimSpace(strings.TrimPrefix(text, trigger)), true
	}
	if !strings.HasPrefix(text, trigger) {
		return "", false
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, trigger))
	if text == "" {
		text = trigger
	}
	return text, true
}

func containsOrEmpty(values []string, target string) bool {
	if len(values) == 0 {
		return true
	}
	target = strings.TrimSpace(target)
	for _, v := range values {
		if strings.TrimSpace(v) == target {
			return true
		}
	}
	return false
}
