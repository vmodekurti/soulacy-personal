package channelsidecarstarter_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/soulacy/soulacy/sdk/extchannel/sidecartest"
)

func TestEchoSidecarConforms(t *testing.T) {
	path := filepath.Join(".", "echo_sidecar.py")
	if err := sidecartest.RunConformance(context.Background(), "python3", path); err != nil {
		t.Fatal(err)
	}
}

