package studio

import (
	"strings"
	"testing"
)

func TestStudioRecognisesAPhoneRequest(t *testing.T) {
	spec := ExtractBuildSpec("Every morning brief me using how I slept and today's calendar")

	if len(spec.PhoneSignals) != 2 {
		t.Fatalf("both signals should be recognised: %v", spec.PhoneSignals)
	}
	joined := strings.Join(spec.PhoneSignals, " | ")
	if !strings.Contains(joined, "sleep") || !strings.Contains(joined, "calendar") {
		t.Fatalf("signals should read back in the person's terms: %q", joined)
	}
	// The panel exists to prove Studio understood; access must be stated.
	security := strings.Join(spec.Security, " ")
	if !strings.Contains(security, "asks your iPhone") {
		t.Fatalf("phone access must be stated plainly: %q", security)
	}
	if !strings.Contains(security, "Device access") {
		t.Fatalf("the person should be told which switches they will need: %q", security)
	}
	// And the tools must be required, or the model substitutes a web search.
	required := RequiredDeviceTools("how I slept")
	for _, want := range PhoneTools {
		if !contains(required, want) {
			t.Fatalf("%s must be required for a phone intent: %v", want, required)
		}
	}
}

func TestLongerPhrasesWinOverShorterOnes(t *testing.T) {
	// "how i slept" and "sleep" both map to health.summary, so this is really
	// a test that the same command is never reported twice.
	signals := PhoneSignals("tell me how I slept and how my sleep has been")
	if len(signals) != 1 {
		t.Fatalf("one command, one signal: %+v", signals)
	}
	if signals[0].Command != "health.summary" {
		t.Fatalf("command: %q", signals[0].Command)
	}
}

func TestAnIntentWithNoPhoneWordsAsksForNothing(t *testing.T) {
	spec := ExtractBuildSpec("Every weekday at 7am send a news digest to telegram")
	if len(spec.PhoneSignals) != 0 {
		t.Fatalf("an ordinary intent must not request phone access: %v", spec.PhoneSignals)
	}
	if spec.DeviceTriggerKind != "" {
		t.Fatalf("nor a device trigger: %q", spec.DeviceTriggerKind)
	}
	if len(RequiredDeviceTools(spec.Intent)) != 0 {
		t.Fatal("and no device tools should be forced on it")
	}
	if spec.Trigger != "schedule" {
		t.Fatalf("the schedule must survive: %q", spec.Trigger)
	}
}

func TestArrivalBecomesALocationTriggerNotASchedule(t *testing.T) {
	spec := ExtractBuildSpec("Every morning when I arrive at the office, remind me what is due")

	if spec.Trigger != "location" {
		t.Fatalf("a place outranks the clock: %q (%q)", spec.Trigger, spec.ScheduleText)
	}
	if spec.Schedule != "" {
		t.Fatalf("a location trigger has no cron: %q", spec.Schedule)
	}
	if !strings.Contains(spec.ScheduleText, "arrives at") {
		t.Fatalf("the panel should explain it: %q", spec.ScheduleText)
	}
	security := strings.Join(spec.Security, " ")
	if !strings.Contains(security, "watch one region") {
		t.Fatalf("background region monitoring must be disclosed: %q", security)
	}
	// A named place is echoed back rather than invented.
	if place := DeviceTriggerFor(spec.Intent).Place; place != "office" {
		t.Fatalf("place: %q", place)
	}
}

func TestDepartureIsRecognisedSeparatelyFromArrival(t *testing.T) {
	spec := ExtractBuildSpec("When I leave home, check the traffic")
	if spec.Trigger != "location" {
		t.Fatalf("trigger: %q", spec.Trigger)
	}
	if on := DeviceTriggerFor(spec.Intent).On; on != "exit" {
		t.Fatalf("leaving is an exit, not an enter: %q", on)
	}
	if !strings.Contains(spec.ScheduleText, "leaves") {
		t.Fatalf("panel text: %q", spec.ScheduleText)
	}
}

func TestAGeofenceWithNoPlaceBlocksRatherThanGuessing(t *testing.T) {
	spec := ExtractBuildSpec("When I arrive, tell me what is waiting")

	var blocker *SpecQuestion
	for i := range spec.Questions {
		if spec.Questions[i].ID == "location.place" {
			blocker = &spec.Questions[i]
		}
	}
	if blocker == nil {
		t.Fatalf("an unnamed place must be asked about: %+v", spec.Questions)
	}
	if !blocker.Blocker {
		t.Fatal("coordinates cannot be guessed, so this has to block")
	}
	if !strings.Contains(blocker.Why, "cannot be guessed") {
		t.Fatalf("the reason should say why: %q", blocker.Why)
	}

	// A named place asks nothing.
	named := ExtractBuildSpec("When I arrive at the office, tell me what is waiting")
	for _, q := range named.Questions {
		if q.ID == "location.place" {
			t.Fatal("a named place should not be asked about again")
		}
	}
}

func TestADeadlineBecomesAPersonTrigger(t *testing.T) {
	spec := ExtractBuildSpec("Tell me when something I owe is about to be due")

	if spec.Trigger != "person" {
		t.Fatalf("trigger: %q", spec.Trigger)
	}
	if spec.DeviceTriggerKind != "person" {
		t.Fatalf("kind: %q", spec.DeviceTriggerKind)
	}
	if when := DeviceTriggerFor(spec.Intent).When; when != "commitment.due" {
		t.Fatalf("condition: %q", when)
	}
	security := strings.Join(spec.Security, " ")
	if !strings.Contains(security, "About You") {
		t.Fatalf("the person should be told a sense is needed: %q", security)
	}
	required := RequiredDeviceTools(spec.Intent)
	if !contains(required, "person.model") {
		t.Fatalf("a person-triggered agent needs to read the model: %v", required)
	}
	// It must NOT be handed the phone tools it never asked for.
	if contains(required, "mobile.invoke") {
		t.Fatalf("a person trigger is not a reason to grant device access: %v", required)
	}
}

func TestALocationTriggerIsNotGrantedDeviceAccessItDidNotAskFor(t *testing.T) {
	required := RequiredDeviceTools("When I arrive at the office, remind me what is due")
	if len(required) != 0 {
		t.Fatalf("the phone reports the crossing; the agent needs no device command: %v", required)
	}
}
