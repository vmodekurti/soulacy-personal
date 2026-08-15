package main

import (
	"strings"
	"testing"
	"time"
)

func TestRootExposesScopedCredentialCommands(t *testing.T) {
	root := buildRoot()
	cmd, _, err := root.Find([]string{"credential"})
	if err != nil || cmd == nil || cmd.Name() != "credential" {
		t.Fatalf("missing sy credential command: %v", err)
	}
	for _, name := range []string{"create", "list", "rotate", "revoke", "status"} {
		sub, _, subErr := root.Find([]string{"credential", name})
		if subErr != nil || sub == nil || sub.Name() != name {
			t.Fatalf("missing sy credential %s: %v", name, subErr)
		}
	}
	create, _, _ := root.Find([]string{"credential", "create"})
	for _, flag := range []string{"kind", "subject", "workspace", "role", "scope", "expires-in", "organization"} {
		if create.Flag(flag) == nil {
			t.Fatalf("sy credential create is missing --%s", flag)
		}
	}
}

func TestCredentialKindNormalization(t *testing.T) {
	for _, input := range []string{"", "personal", "pat", "PERSONAL_ACCESS_TOKEN"} {
		kind, err := normalizeCredentialKind(input)
		if err != nil || kind != credentialKindPersonal {
			t.Fatalf("kind %q resolved to %q (%v)", input, kind, err)
		}
	}
	for _, input := range []string{"service", "Service-Account", "service_account"} {
		kind, err := normalizeCredentialKind(input)
		if err != nil || kind != credentialKindService {
			t.Fatalf("kind %q resolved to %q (%v)", input, kind, err)
		}
	}
	if _, err := normalizeCredentialKind("root"); err == nil {
		t.Fatal("expected an unknown credential kind to be rejected")
	}
}

// A credential's displayed principal is the audit identity. A service-account
// credential must never be presented as an anonymous or generic API user.
func TestCredentialPrincipalNamesTheServiceAccount(t *testing.T) {
	service := credentialPrincipal(credential{Kind: credentialKindService, SubjectID: "svc_ci"})
	if !strings.Contains(service, "service-account") || !strings.Contains(service, "svc_ci") {
		t.Fatalf("service-account principal not identified: %q", service)
	}
	personal := credentialPrincipal(credential{Kind: credentialKindPersonal, SubjectID: "usr_alice"})
	if !strings.Contains(personal, "user") || !strings.Contains(personal, "usr_alice") {
		t.Fatalf("personal principal not identified: %q", personal)
	}
	if got := credentialPrincipal(credential{Kind: credentialKindService}); !strings.Contains(got, "unbound") {
		t.Fatalf("expected an unbound subject to be visible, got %q", got)
	}
}

func TestParseCredentialExpiryAcceptsDaysAndRejectsNonPositive(t *testing.T) {
	at, err := parseCredentialExpiry("30d")
	if err != nil {
		t.Fatal(err)
	}
	if delta := time.Until(at); delta < 29*24*time.Hour || delta > 31*24*time.Hour {
		t.Fatalf("30d resolved to %s", delta)
	}
	if _, err := parseCredentialExpiry("12h"); err != nil {
		t.Fatalf("12h rejected: %v", err)
	}
	for _, bad := range []string{"", "0d", "-5d", "soon", "30x"} {
		if _, err := parseCredentialExpiry(bad); err == nil {
			t.Fatalf("expiry %q was accepted", bad)
		}
	}
}

func TestSplitCommaValuesFlattensAndDeduplicates(t *testing.T) {
	got := splitCommaValues([]string{"agents:read, agents:run", "agents:read", " ", "secrets:list"})
	want := []string{"agents:read", "agents:run", "secrets:list"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFormatCredentialExpiryMarksElapsedCredentials(t *testing.T) {
	if got := formatCredentialExpiry(nil); got != "never" {
		t.Fatalf("nil expiry rendered %q", got)
	}
	past := time.Now().UTC().Add(-time.Hour)
	if got := formatCredentialExpiry(&past); !strings.Contains(got, "expired") {
		t.Fatalf("elapsed expiry rendered %q", got)
	}
	future := time.Now().UTC().Add(time.Hour)
	if got := formatCredentialExpiry(&future); strings.Contains(got, "expired") {
		t.Fatalf("live expiry rendered %q", got)
	}
}
