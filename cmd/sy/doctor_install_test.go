package main

import "testing"

func TestConfiguredServiceBinaryDarwin(t *testing.T) {
	plist := `<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.soulacy.soulacy</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/me/.local/bin/soulacy</string>
    <string>serve</string>
  </array>
</dict>
</plist>`
	got := configuredServiceBinary(plist, "darwin")
	if got != "/Users/me/.local/bin/soulacy" {
		t.Fatalf("configuredServiceBinary(darwin) = %q", got)
	}
}

func TestConfiguredServiceBinaryLinux(t *testing.T) {
	unit := `[Service]
ExecStart=/home/me/.local/bin/soulacy serve
Restart=on-failure
`
	got := configuredServiceBinary(unit, "linux")
	if got != "/home/me/.local/bin/soulacy" {
		t.Fatalf("configuredServiceBinary(linux) = %q", got)
	}
}

func TestUniqueStrings(t *testing.T) {
	got := uniqueStrings([]string{" a ", "b", "a", "", "b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("uniqueStrings = %#v", got)
	}
}
