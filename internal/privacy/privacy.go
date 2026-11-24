// Package privacy decides, on this machine and without any model call, whether
// text is private enough that it must not leave the machine.
//
// It is deliberately deterministic. The judgment has to be local — sending text
// to a hosted model to ask whether it is safe to send would defeat the point —
// and it has to be explainable, so every positive result names the rule that
// fired. It errs toward "private": a false positive costs answer quality (the
// local model answers instead of a stronger one), a false negative costs the
// person their privacy.
//
// It will miss things. A sentence can be sensitive with no pattern in it. This
// is the floor, not the whole of the protection.
package privacy

import (
	"regexp"
	"strings"
)

// ClassPrivate is the data classification for content that stays on this machine.
const ClassPrivate = "private"

// Message is the minimum the detector needs to know about one piece of a
// conversation: who said it and what it says.
type Message struct {
	Role string
	Text string
}

// maxScan bounds the work per call. Private details are overwhelmingly in the
// recent part of a conversation, and the scan runs on every model call.
const maxScan = 200_000

var (
	cardRe = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)
	// Identifiers that are private whatever the sentence around them says.
	hardRules = []struct {
		reason string
		re     *regexp.Regexp
	}{
		{"a government ID number", regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)},
		{"bank account details", regexp.MustCompile(`(?i)\b(account number|routing number|iban|sort code|swift code)\b[^.\n]{0,24}\d{4,}`)},
		{"a password or code", regexp.MustCompile(`(?i)\b(password|passcode|passphrase|pin|security code|cvv)\b\s*(is|=|:)\s*\S+`)},
		{"a private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	}
	// The person asking for privacy in their own words.
	askRe = regexp.MustCompile(`(?i)\b(keep (this|it|that) (private|confidential|between us)|this is (private|confidential)|off the record)\b`)
	// A sensitive subject only counts when the person is talking about
	// themselves or their family. "How do mortgage rates work?" is general
	// knowledge; "can we afford our mortgage on my salary?" is not.
	topicRe = regexp.MustCompile(`(?i)\b(diagnos(is|ed)|prescri(bed|ption)|medications?|symptoms?|therap(y|ist)|depress(ion|ed)|anxiety|pregnan(t|cy)|cancer|hiv|blood test|lab results?|medical (records?|history|condition)|surgery|salary|paycheck|tax return|bank statement|net worth|debts?|credit score|mortgage|divorce|custody|lawsuit|immigration status|passport number|social security)\b`)
	selfRe  = regexp.MustCompile(`(?i)\b(my|our|i|i'm|i've|me|mine|myself|wife|husband|partner|son|daughter|mother|father|mom|dad)\b`)
	splitRe = regexp.MustCompile(`[.!?\n]+`)
)

// DetectText judges one piece of text written by the person.
func DetectText(text string) (bool, string) {
	return Detect([]Message{{Role: "user", Text: text}})
}

// Detect reports whether the person's own words are private, and which rule
// said so. Only user-authored messages are judged.
//
// Text from tools and the assistant is deliberately not judged here. Nothing
// yet records where a tool's data came from, and public web pages are full of
// long digit runs — product codes, barcodes — that pass the same checksum a
// card number does. Judging them marked ordinary shopping research private
// (#401). Private data that tools bring back needs source tagging, not
// pattern matching.
func Detect(messages []Message) (bool, string) {
	budget := maxScan
	for i := len(messages) - 1; i >= 0 && budget > 0; i-- {
		m := messages[i]
		if !strings.EqualFold(strings.TrimSpace(m.Role), "user") || m.Text == "" {
			continue
		}
		text := m.Text
		if len(text) > budget {
			text = text[len(text)-budget:]
		}
		budget -= len(text)
		if hasCard(text) {
			return true, "a payment card number"
		}
		for _, rule := range hardRules {
			if rule.re.MatchString(text) {
				return true, rule.reason
			}
		}
		if askRe.MatchString(text) {
			return true, "you asked to keep it private"
		}
		for _, sentence := range splitRe.Split(text, -1) {
			if topicRe.MatchString(sentence) && selfRe.MatchString(sentence) {
				return true, "a personal health, money or legal matter"
			}
		}
	}
	return false, ""
}

// hasCard finds a 13–19 digit run that passes the Luhn check, which is what
// separates a card number from an order number or a timestamp.
func hasCard(text string) bool {
	for _, candidate := range cardRe.FindAllString(text, 8) {
		digits := make([]int, 0, 19)
		for _, r := range candidate {
			if r >= '0' && r <= '9' {
				digits = append(digits, int(r-'0'))
			}
		}
		if len(digits) < 13 || len(digits) > 19 {
			continue
		}
		sum, double := 0, false
		allSame := true
		for i := len(digits) - 1; i >= 0; i-- {
			d := digits[i]
			if d != digits[0] {
				allSame = false
			}
			if double {
				d *= 2
				if d > 9 {
					d -= 9
				}
			}
			sum += d
			double = !double
		}
		if !allSame && sum%10 == 0 {
			return true
		}
	}
	return false
}
