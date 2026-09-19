package platform

import (
	"errors"
	"testing"
)

func TestWorkspaceDurable(t *testing.T) {
	root := func(string) (uint64, error) { return 1, nil } // same device as "/"
	vol := func(p string) (uint64, error) {                // workspace on its own device
		if p == "/" {
			return 1, nil
		}
		return 2, nil
	}
	fail := func(string) (uint64, error) { return 0, errors.New("nope") }

	cases := []struct {
		name        string
		info        Info
		dev         func(string) (uint64, error)
		wantDurable bool
	}{
		{"host is always durable", Info{"Mac", Host}, root, true},
		{"container on root fs is ephemeral", Info{"Docker", Container}, root, false},
		{"container with a volume is durable", Info{"Docker", Container}, vol, true},
		{"managed on root fs is ephemeral", Info{"Railway", Managed}, root, false},
		{"managed with a disk is durable", Info{"Render", Managed}, vol, true},
		{"unknowable device does not warn", Info{"Docker", Container}, fail, true},
	}
	for _, c := range cases {
		durable, reason := workspaceDurable(c.info, "/home/soulacy/.soulacy", c.dev)
		if durable != c.wantDurable {
			t.Errorf("%s: durable=%v want %v (reason=%q)", c.name, durable, c.wantDurable, reason)
		}
		if !durable && reason == "" {
			t.Errorf("%s: not durable but no reason given", c.name)
		}
	}
}
