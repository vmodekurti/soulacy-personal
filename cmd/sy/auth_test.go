package main

import (
	"strings"
	"testing"
)

func TestRootExposesInteractiveSessionCommands(t *testing.T) {
	root := buildRoot()
	for _, name := range []string{"login", "logout"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == nil || cmd.Name() != name {
			t.Fatalf("missing sy %s command: %v", name, err)
		}
	}
	login, _, _ := root.Find([]string{"login"})
	if login.Flag("device") == nil || login.Flag("no-browser") == nil {
		t.Fatal("headless device-flow flags are missing")
	}
}

func TestCredentialServiceIsGatewayScopedAndDoesNotContainURL(t *testing.T) {
	one := credentialService("https://one.example")
	two := credentialService("https://two.example")
	if one == two || strings.Contains(one, "example") || !strings.HasPrefix(one, "soulacy-cli-") {
		t.Fatalf("unsafe credential service names: %q %q", one, two)
	}
}
