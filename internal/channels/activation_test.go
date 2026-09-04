package channels

import "testing"

func TestActivationPolicyTriggerPhraseGroupOnly(t *testing.T) {
	// IgnoreGroups false so groups reach the trigger gate; DirectActivatesWithoutPhrase
	// mirrors a real chat platform (DMs skip the phrase).
	p := ActivationPolicy{TriggerPhrase: "!soulacy", DirectActivatesWithoutPhrase: true}

	// In a GROUP, the phrase is required and stripped.
	if _, ok := p.Apply("hello", "thread", "user", true); ok {
		t.Fatal("group message without trigger phrase should be ignored")
	}
	text, ok := p.Apply("!soulacy hello", "thread", "user", true)
	if !ok || text != "hello" {
		t.Fatalf("group message with trigger should be accepted+stripped, got %q ok=%v", text, ok)
	}

	// In a DIRECT message, no phrase is needed — the bot always responds.
	text, ok = p.Apply("hello", "thread", "user", false)
	if !ok || text != "hello" {
		t.Fatalf("DM should activate without a trigger phrase, got %q ok=%v", text, ok)
	}
	// A DM that DID include the phrase still has it stripped.
	if text, ok := p.Apply("!soulacy hello", "thread", "user", false); !ok || text != "hello" {
		t.Fatalf("DM with phrase should strip it, got %q ok=%v", text, ok)
	}
}

func TestActivationPolicyIgnoresGroups(t *testing.T) {
	p := ActivationPolicy{TriggerPhrase: "!soulacy", IgnoreGroups: true}

	if _, ok := p.Apply("!soulacy hello", "thread", "user", true); ok {
		t.Fatal("group message should be ignored when IgnoreGroups is enabled")
	}
}

func TestActivationPolicyAllowLists(t *testing.T) {
	p := ActivationPolicy{
		TriggerPhrase:    "!soulacy",
		AllowedThreadIDs: []string{"allowed-thread"},
		AllowedUserIDs:   []string{"allowed-user"},
	}

	if _, ok := p.Apply("!soulacy hello", "other-thread", "allowed-user", false); ok {
		t.Fatal("message from disallowed thread should be ignored")
	}
	if _, ok := p.Apply("!soulacy hello", "allowed-thread", "other-user", false); ok {
		t.Fatal("message from disallowed user should be ignored")
	}
	if _, ok := p.Apply("!soulacy hello", "allowed-thread", "allowed-user", false); !ok {
		t.Fatal("message matching thread and user allowlists should be accepted")
	}
}
