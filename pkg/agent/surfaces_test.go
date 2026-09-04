package agent

import "testing"

func TestEffectiveSurfaces_DerivedFromTrigger(t *testing.T) {
	cron := &Definition{Trigger: TriggerCron}
	if cron.AppearsOnChat() {
		t.Error("cron agent should NOT appear on chat by default")
	}
	if !cron.AppearsOn(SurfaceSchedule) {
		t.Error("cron agent should appear on schedule")
	}

	manual := &Definition{Trigger: TriggerInternal}
	if !manual.AppearsOnChat() {
		t.Error("manual/internal agent should appear on chat")
	}

	chan_ := &Definition{Trigger: TriggerChannel, Channels: []string{"telegram"}}
	if !chan_.AppearsOn("telegram") || !chan_.AppearsOnChat() {
		t.Errorf("channel agent should appear on its channel + chat: %+v", chan_.EffectiveSurfaces())
	}
}

func TestEffectiveSurfaces_ExplicitOverrides(t *testing.T) {
	// A cron agent explicitly opted into chat.
	d := &Definition{Trigger: TriggerCron, Surfaces: []string{"schedule", "chat"}}
	if !d.AppearsOnChat() {
		t.Error("explicit chat surface should make a cron agent chat-eligible")
	}
}
