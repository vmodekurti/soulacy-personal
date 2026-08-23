package main

import "testing"

func TestMemoryReindexCommandIsRegisteredWithSafetyFlags(t *testing.T) {
	cmd, _, err := buildRoot().Find([]string{"memory", "reindex"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"confirm-offline", "dry-run", "restart", "dims", "model", "provider"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("memory reindex is missing --%s", name)
		}
	}
}
