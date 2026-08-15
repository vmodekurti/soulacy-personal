package wsroot

import (
	"path/filepath"
	"strings"
	"testing"
)

// Product invariant 7: an existing single-user installation must not notice
// that storage became tenant-aware. Its files stay exactly where they were.
func TestPersonalLayoutIsUnchanged(t *testing.T) {
	base := filepath.Join("/data", "studio")
	for _, id := range []string{"", "   ", PersonalWorkspaceID} {
		if got := Dir(base, id); got != base {
			t.Errorf("Dir(%q) = %q, want the untouched base %q", id, got, base)
		}
	}
	path := filepath.Join(base, "studio-macros.json")
	for _, id := range []string{"", PersonalWorkspaceID} {
		if got := File(path, id); got != path {
			t.Errorf("File(%q) = %q, want the untouched path %q", id, got, path)
		}
	}
}

func TestOtherWorkspacesAreNamespaced(t *testing.T) {
	base := filepath.Join("/data", "studio")
	dir := Dir(base, "ws_a")
	want := filepath.Join(base, NamespaceDir, "ws_a")
	if dir != want {
		t.Fatalf("Dir = %q want %q", dir, want)
	}
	file := File(filepath.Join(base, "studio-macros.json"), "ws_a")
	wantFile := filepath.Join(base, NamespaceDir, "ws_a", "studio-macros.json")
	if file != wantFile {
		t.Fatalf("File = %q want %q", file, wantFile)
	}
	// Two workspaces never share a location, which is the whole point.
	if Dir(base, "ws_a") == Dir(base, "ws_b") || File(filepath.Join(base, "x.json"), "ws_a") == File(filepath.Join(base, "x.json"), "ws_b") {
		t.Fatal("two workspaces resolved to the same location")
	}
}

// An ID that cannot be a path segment must never become one. Resolving to the
// personal root is the safe failure: it can conflate with personal state,
// which callers prevent by validating, but it cannot write outside the root.
func TestUnusableIDsNeverEscapeTheRoot(t *testing.T) {
	base := filepath.Join("/data", "studio")
	for _, bad := range []string{"..", ".", "../escape", "ws/../../etc", "WS_UPPER", strings.Repeat("a", 65), "has space"} {
		if err := Validate(bad); err == nil {
			t.Errorf("Validate(%q) accepted an unusable ID", bad)
		}
		dir := Dir(base, bad)
		if !strings.HasPrefix(filepath.Clean(dir), filepath.Clean(base)) {
			t.Errorf("Dir(%q) = %q escaped the root", bad, dir)
		}
		file := File(filepath.Join(base, "x.json"), bad)
		if !strings.HasPrefix(filepath.Clean(file), filepath.Clean(base)) {
			t.Errorf("File(%q) = %q escaped the root", bad, file)
		}
	}
}

func TestOfDerivesOwnershipFromLocation(t *testing.T) {
	base := "/data/studio"
	cases := map[string]struct {
		path      string
		want      string
		wantValid bool
	}{
		"personal file":    {"/data/studio/studio-macros.json", PersonalWorkspaceID, true},
		"personal nested":  {"/data/studio/drafts/a.json", PersonalWorkspaceID, true},
		"workspace file":   {"/data/studio/.workspaces/ws_a/studio-macros.json", "ws_a", true},
		"workspace nested": {"/data/studio/.workspaces/ws_a/drafts/a.json", "ws_a", true},
		// Owned by no one — must be ignored, not guessed into a workspace.
		"namespace root": {"/data/studio/.workspaces/stray.json", "", false},
		"bad id":         {"/data/studio/.workspaces/UPPER/x.json", "", false},
		"escapes base":   {"/elsewhere/x.json", "", false},
	}
	for name, tc := range cases {
		got, ok := Of(base, tc.path)
		if ok != tc.wantValid || (ok && got != tc.want) {
			t.Errorf("%s: Of(%q) = %q,%v want %q,%v", name, tc.path, got, ok, tc.want, tc.wantValid)
		}
	}
}

// Dir and Of must agree, or a file written for one workspace could be read
// back as another's.
func TestDirAndOfRoundTrip(t *testing.T) {
	base := "/data/studio"
	for _, id := range []string{PersonalWorkspaceID, "ws_a", "ws_prod-1", "ws.b"} {
		path := filepath.Join(Dir(base, id), "state.json")
		got, ok := Of(base, path)
		if !ok || got != id {
			t.Errorf("round trip for %q gave %q,%v", id, got, ok)
		}
	}
}

func TestFileHandlesEmptyPath(t *testing.T) {
	if got := File("", "ws_a"); got != "" {
		t.Fatalf("File(\"\") = %q, want an empty path to stay empty", got)
	}
}

// User-private state stays put for Personal's implicit local identities, so an
// upgrade does not orphan an existing installation's drafts.
func TestUserDirKeepsPersonalIdentitiesInPlace(t *testing.T) {
	base := "/data/studio/drafts"
	for _, subject := range []string{"", "  ", "local-owner", "Local-Owner", "admin", "api-key"} {
		if got := UserDir(base, PersonalWorkspaceID, subject); got != base {
			t.Errorf("UserDir(%q) = %q, want the untouched base", subject, got)
		}
	}
}

func TestUserDirSeparatesRealSubjects(t *testing.T) {
	base := "/data/studio/drafts"
	alice := UserDir(base, "ws_a", "usr_alice")
	bob := UserDir(base, "ws_a", "usr_bob")
	if alice == bob {
		t.Fatal("two users resolved to the same directory")
	}
	// Same user in two workspaces is still two places.
	if UserDir(base, "ws_a", "usr_alice") == UserDir(base, "ws_b", "usr_alice") {
		t.Fatal("one user's state is shared across workspaces")
	}
	// Stable across calls, or a restart would lose everything.
	if alice != UserDir(base, "ws_a", "usr_alice") {
		t.Fatal("UserDir is not stable for the same subject")
	}
}

// The raw subject must not appear on disk: it can be an email address, and a
// directory listing or a backup should not spread it around.
func TestUserSegmentDoesNotLeakTheSubject(t *testing.T) {
	segment := UserSegment("alice@example.com")
	if segment == "" || strings.Contains(segment, "alice") || strings.Contains(segment, "@") || strings.Contains(segment, "example") {
		t.Fatalf("UserSegment leaked the subject: %q", segment)
	}
	if !strings.HasPrefix(segment, "u_") {
		t.Fatalf("UserSegment = %q, want a recognizable prefix", segment)
	}
	if err := Validate(segment); err != nil {
		t.Fatalf("UserSegment %q is not a usable path segment: %v", segment, err)
	}
}
