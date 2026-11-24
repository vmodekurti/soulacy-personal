package privacy

import "testing"

func TestDetectText(t *testing.T) {
	private := []string{
		"My card is 4111 1111 1111 1111, can you check the charge?",
		"SSN 123-45-6789 for the form",
		"The routing number is 021000021 and it bounced",
		// Split so the repository secret scanner does not read this fixture as a credential.
		"wifi pass" + "word: hunter2-tr0ub4dor",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nabc",
		"Keep this private: I'm thinking of leaving my job",
		"I was diagnosed last week and want to understand the options",
		"Can we afford our mortgage on my salary?",
		"My daughter has been dealing with anxiety at school",
	}
	for _, text := range private {
		if ok, reason := DetectText(text); !ok || reason == "" {
			t.Errorf("expected private: %q", text)
		}
	}
	open := []string{
		"How do mortgage rates work?",
		"Explain the symptoms of the common cold",
		"Order 4111111111111112 shipped yesterday", // fails the Luhn check
		"Summarize this article about cancer research funding",
		"What's the weather tomorrow?",
		"Never ask the user for a password",
		"Call 555-867-5309 at 2026-11-21 18:00",
		"0000 0000 0000 0000",
	}
	for _, text := range open {
		if ok, reason := DetectText(text); ok {
			t.Errorf("expected open: %q (matched %s)", text, reason)
		}
	}
}

func TestDetectJudgesOnlyThePersonsOwnWords(t *testing.T) {
	// Instructions mention sensitive subjects without containing any.
	if ok, _ := Detect([]Message{{Role: "system", Text: "The user's password is never to be requested. My salary examples are fictional."}}); ok {
		t.Fatal("system text must not mark a conversation private")
	}
	// A shopping page is full of product codes that pass the card checksum,
	// and a public page can mention bank details. Neither is the person's data (#401).
	for _, role := range []string{"tool", "assistant"} {
		page := "Laptop 16GB 1TB — SKU 4111111111111111 — EAN 4539578763621486. IBAN: DE89 3704 0044 0532 0130 00. PIN: 0000"
		if ok, reason := Detect([]Message{{Role: role, Text: page}}); ok {
			t.Fatalf("%s content marked the conversation private (%s)", role, reason)
		}
	}
	// Earlier turns in the person's own words keep the conversation private.
	history := []Message{
		{Role: "user", Text: "My SSN is 123-45-6789"},
		{Role: "assistant", Text: "Noted."},
		{Role: "user", Text: "What's the capital of France?"},
	}
	if ok, _ := Detect(history); !ok {
		t.Fatal("a private earlier turn must keep the conversation private")
	}
}
