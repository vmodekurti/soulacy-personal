package studio

import "strings"

// devicespec.go — teaching Studio that a phone exists.
//
// Studio could already name the phone tools, because its catalogue comes from
// the live engine. What it could not do was recognise a request for them:
// "brief me when I get to the office" produced a manually-triggered agent
// with no device tools and no region, which fails the moment it runs. The
// vocabulary here is the difference between naming a capability and
// understanding one.

// PhoneSignal is one thing only a phone knows, matched from plain language.
type PhoneSignal struct {
	// Command is the device command an agent invokes through mobile.invoke.
	Command string
	// Label is how the spec panel names it, so a person can confirm Studio
	// understood them.
	Label string
	// Capability is the switch the person must have enabled on the phone,
	// under Settings → Device access.
	Capability string
}

// phoneVocabulary maps the words people actually use to the command that
// serves them. Ordered longest-phrase-first at match time so "how I slept"
// wins over a bare "sleep".
var phoneVocabulary = map[string]PhoneSignal{
	"how i slept":      {Command: "health.summary", Label: "last night's sleep", Capability: "Health summary"},
	"how much i slept": {Command: "health.summary", Label: "last night's sleep", Capability: "Health summary"},
	"my sleep":         {Command: "health.summary", Label: "last night's sleep", Capability: "Health summary"},
	"sleep":            {Command: "health.summary", Label: "last night's sleep", Capability: "Health summary"},
	"steps":            {Command: "health.summary", Label: "yesterday's steps", Capability: "Health summary"},
	"my health":        {Command: "health.summary", Label: "a health summary", Capability: "Health summary"},
	"workout":          {Command: "health.summary", Label: "workouts", Capability: "Health summary"},

	"my calendar":      {Command: "calendar.events", Label: "today's calendar", Capability: "Calendar events"},
	"today's calendar": {Command: "calendar.events", Label: "today's calendar", Capability: "Calendar events"},
	"todays calendar":  {Command: "calendar.events", Label: "today's calendar", Capability: "Calendar events"},
	"on my calendar":   {Command: "calendar.events", Label: "today's calendar", Capability: "Calendar events"},
	"my meetings":      {Command: "calendar.events", Label: "today's meetings", Capability: "Calendar events"},
	"my schedule":      {Command: "calendar.events", Label: "today's calendar", Capability: "Calendar events"},
	"my agenda":        {Command: "calendar.events", Label: "today's calendar", Capability: "Calendar events"},

	"my reminders": {Command: "reminders.list", Label: "open reminders", Capability: "Reminders"},
	"my to-do":     {Command: "reminders.list", Label: "open reminders", Capability: "Reminders"},
	"my todo":      {Command: "reminders.list", Label: "open reminders", Capability: "Reminders"},

	"where i am":  {Command: "location.current", Label: "where you are", Capability: "Location"},
	"my location": {Command: "location.current", Label: "where you are", Capability: "Location"},

	"focus mode":     {Command: "focus.status", Label: "whether a Focus is on", Capability: "Focus status"},
	"do not disturb": {Command: "focus.status", Label: "whether a Focus is on", Capability: "Focus status"},

	"my contacts": {Command: "contacts.search", Label: "your contacts", Capability: "Contacts"},

	"show me a card": {Command: "canvas.present", Label: "a card on the phone", Capability: "Soulacy canvas"},
	"on my phone":    {Command: "canvas.present", Label: "a card on the phone", Capability: "Soulacy canvas"},
}

// PhoneSignals returns the signals an intent asks for, de-duplicated by
// command and in a stable order.
func PhoneSignals(intent string) []PhoneSignal {
	low := strings.ToLower(intent)
	seen := map[string]bool{}
	var out []PhoneSignal
	// Longest phrases first: "how i slept" must not be beaten by "sleep".
	for _, phrase := range phrasesByLength() {
		if !strings.Contains(low, phrase) {
			continue
		}
		signal := phoneVocabulary[phrase]
		if seen[signal.Command] {
			continue
		}
		seen[signal.Command] = true
		out = append(out, signal)
	}
	return out
}

func phrasesByLength() []string {
	phrases := make([]string, 0, len(phoneVocabulary))
	for phrase := range phoneVocabulary {
		phrases = append(phrases, phrase)
	}
	// Insertion sort by descending length, then alphabetical, so the result is
	// deterministic across runs (Go map order is not).
	for i := 1; i < len(phrases); i++ {
		for j := i; j > 0 && lessPhrase(phrases[j], phrases[j-1]); j-- {
			phrases[j], phrases[j-1] = phrases[j-1], phrases[j]
		}
	}
	return phrases
}

func lessPhrase(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a < b
}

// PhoneTools are the builtins an agent needs to reach any device command.
//
// These are opt-in: the engine refuses a tool the agent has not listed in
// `builtins:`. Studio's save path writes the draft's tools into that field,
// so naming them here is what makes a generated agent actually run.
var PhoneTools = []string{"mobile.list_nodes", "mobile.invoke", "mobile.command_status"}

// PersonTools read and add to what Soulacy understands about the person.
var PersonTools = []string{"person.model", "person.observe"}

// DeviceTrigger is a trigger only a phone or the person model can raise.
type DeviceTrigger struct {
	// Kind is "location" or "person"; empty when the intent asks for neither.
	Kind string
	// When, for a person trigger, is the condition to watch.
	When string
	// On, for a location trigger, is "enter" or "exit".
	On string
	// Text explains the trigger in the spec panel, in the person's terms.
	Text string
	// Place is the region named in the intent, when one was named. Never
	// guessed: coordinates cannot be invented, so an unnamed place becomes a
	// blocking question instead.
	Place string
}

// arrivalPhrases and departurePhrases are the ways people describe crossing a
// boundary. Deliberately narrow: a false location trigger asks for a geofence
// nobody wanted.
var arrivalPhrases = []string{
	"when i arrive", "when i get to", "when i reach", "when i get home",
	"when i arrive at", "as i arrive", "when i'm at", "when i am at",
	"arriving at", "when i pull into",
}

var departurePhrases = []string{
	"when i leave", "when i depart", "as i leave", "before i leave",
	"when i head out", "leaving the", "leaving home", "leaving work",
}

// DeviceTriggerFor reads an intent for a phone or person trigger.
func DeviceTriggerFor(intent string) DeviceTrigger {
	low := strings.ToLower(intent)

	for _, phrase := range arrivalPhrases {
		if strings.Contains(low, phrase) {
			place := placeAfter(low, phrase)
			return DeviceTrigger{Kind: "location", On: "enter", Place: place,
				Text: "when your phone arrives at " + placeOrThere(place)}
		}
	}
	for _, phrase := range departurePhrases {
		if strings.Contains(low, phrase) {
			place := placeAfter(low, phrase)
			return DeviceTrigger{Kind: "location", On: "exit", Place: place,
				Text: "when your phone leaves " + placeOrThere(place)}
		}
	}

	// Person triggers: something about the person changed, rather than a
	// place. Checked after location so "when I leave, remind me what's due"
	// stays a location trigger, which is what the person meant.
	switch {
	case containsAny(low, "deadline", "due soon", "coming due", "about to be due", "something is due"):
		return DeviceTrigger{Kind: "person", When: "commitment.due",
			Text: "when something you owe comes due"}
	case containsAny(low, "start driving", "when i'm driving", "when i am driving", "when i start my commute", "when a focus", "when i turn on focus"):
		return DeviceTrigger{Kind: "person", When: "state.changed",
			Text: "when how you are right now changes"}
	case containsAny(low, "unusual day", "off my routine", "different from usual", "not my usual", "departs from my routine"):
		return DeviceTrigger{Kind: "person", When: "routine.deviation",
			Text: "when today departs from your usual shape"}
	}
	return DeviceTrigger{}
}

// placeAfter returns the words naming a place after a trigger phrase, or "".
//
// Deliberately strict. "When I arrive, tell me what is waiting" names no
// place, and an earlier version happily returned "tell me what" — which would
// have produced a geofence around a phrase instead of asking the one question
// that cannot be guessed. A place is only accepted when a preposition
// introduces it, or when the trigger phrase already ends in one ("get home").
func placeAfter(low, phrase string) string {
	if place := placeInsidePhrase(phrase); place != "" {
		return place
	}
	idx := strings.Index(low, phrase)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(low[idx+len(phrase):])
	// Punctuation straight after the trigger means the sentence moved on.
	if rest == "" || strings.HasPrefix(rest, ",") || strings.HasPrefix(rest, ".") ||
		strings.HasPrefix(rest, ";") || strings.HasPrefix(rest, "then ") {
		return ""
	}
	introduced := false
	for _, preposition := range []string{"at ", "in ", "to ", "the "} {
		if strings.HasPrefix(rest, preposition) {
			rest = strings.TrimSpace(strings.TrimPrefix(rest, preposition))
			introduced = true
			break
		}
	}
	if !introduced {
		return ""
	}
	rest = strings.TrimPrefix(rest, "the ")
	rest = strings.TrimPrefix(rest, "my ")
	clause := strings.FieldsFunc(rest, func(r rune) bool {
		return r == ',' || r == '.' || r == ';' || r == ':'
	})
	if len(clause) == 0 {
		return ""
	}
	words := strings.Fields(clause[0])
	if len(words) > 3 {
		words = words[:3]
	}
	return strings.TrimSpace(strings.Join(words, " "))
}

// placeInsidePhrase handles "when I get home", where the place is part of the
// trigger wording rather than something that follows it.
func placeInsidePhrase(phrase string) string {
	for _, place := range []string{"home", "work"} {
		if strings.HasSuffix(strings.TrimSpace(phrase), " "+place) {
			return place
		}
	}
	return ""
}

func placeOrThere(place string) string {
	if place == "" {
		return "a place you name"
	}
	return place
}

// RequiredDeviceTools names the builtins a generated agent must list for an
// intent that asks for phone signals or a person-model trigger.
//
// Returned as a requirement rather than a suggestion because these are
// exactly the tools a model substitutes away: asked for "how I slept" it will
// reach for a web search unless told the real one exists and is mandatory.
func RequiredDeviceTools(intent string) []string {
	var out []string
	if len(PhoneSignals(intent)) > 0 {
		out = append(out, PhoneTools...)
	}
	switch DeviceTriggerFor(intent).Kind {
	case "person":
		out = append(out, PersonTools...)
	case "location":
		// A location trigger needs no device command of its own: the phone
		// reports the crossing and the gateway runs the agent. Adding the
		// mobile tools here would grant access nobody asked for.
	}
	return out
}
