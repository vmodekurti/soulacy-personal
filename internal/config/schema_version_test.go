// schema_version_test.go — Cohort G: pin the config schema-version resolver.
package config

import "testing"

func TestCheckSchemaVersion_Nil(t *testing.T) {
	st := CheckSchemaVersion(nil)
	if !(st.Have == "" && st.Want == CurrentSchemaVersion) {
		t.Fatalf("nil cfg: have=%q want=%q message=%q", st.Have, st.Want, st.Message)
	}
	if st.OutOfDate {
		t.Fatalf("nil cfg should not report OutOfDate")
	}
}

func TestCheckSchemaVersion_UnstampedFresh(t *testing.T) {
	st := CheckSchemaVersion(&Config{})
	if !st.UnstampedFresh {
		t.Fatalf("empty schema_version should be treated as unstamped fresh, got %+v", st)
	}
	if st.OutOfDate {
		t.Fatalf("unstamped fresh must not report OutOfDate: %+v", st)
	}
	if st.Want != CurrentSchemaVersion {
		t.Fatalf("want=%q, expected %q", st.Want, CurrentSchemaVersion)
	}
}

func TestCheckSchemaVersion_Current(t *testing.T) {
	st := CheckSchemaVersion(&Config{SchemaVersion: CurrentSchemaVersion})
	if st.OutOfDate {
		t.Fatalf("current version should not be OutOfDate: %+v", st)
	}
	if st.UnstampedFresh {
		t.Fatalf("current version should not be UnstampedFresh: %+v", st)
	}
	if st.Have != CurrentSchemaVersion {
		t.Fatalf("Have=%q, want %q", st.Have, CurrentSchemaVersion)
	}
}

func TestCheckSchemaVersion_OutOfDateStampedOlder(t *testing.T) {
	st := CheckSchemaVersion(&Config{SchemaVersion: "v0"})
	if !st.OutOfDate {
		t.Fatalf("older stamp should be OutOfDate: %+v", st)
	}
	if st.UnstampedFresh {
		t.Fatalf("stamped file should not report UnstampedFresh: %+v", st)
	}
	if st.Have != "v0" {
		t.Fatalf("Have=%q, want v0", st.Have)
	}
	// The Message must name both versions so the operator knows what to
	// update — this is user-visible copy in the boot banner and doctor.
	if !containsAll(st.Message, "v0", CurrentSchemaVersion) {
		t.Fatalf("Message should reference both versions: %q", st.Message)
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
