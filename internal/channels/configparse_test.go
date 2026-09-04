package channels

import (
	"reflect"
	"testing"
)

func TestParseInt64List(t *testing.T) {
	m := map[string]any{"allowed_user_ids": []any{int64(1), float64(2), 3, "junk"}}
	got := ParseInt64List(m, "allowed_user_ids")
	want := []int64{1, 2, 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseInt64List = %v, want %v", got, want)
	}
	if got := ParseInt64List(map[string]any{}, "allowed_user_ids"); got != nil {
		t.Fatalf("missing key = %v, want nil", got)
	}
}

func TestParseStringList(t *testing.T) {
	if got := ParseStringList([]string{"a", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("[]string = %v", got)
	}
	if got := ParseStringList([]any{"a", 2, " "}); !reflect.DeepEqual(got, []string{"a", "2"}) {
		t.Fatalf("[]any = %v", got)
	}
	if got := ParseStringList("a b  c"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("string = %v", got)
	}
	if got := ParseStringList(42); got != nil {
		t.Fatalf("other = %v, want nil", got)
	}
}

func TestParseDelimitedList(t *testing.T) {
	if got := ParseDelimitedList("a, b\nc\td , "); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("delimited = %v", got)
	}
	if got := ParseDelimitedList([]any{"x", "y"}); !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Fatalf("list = %v", got)
	}
	if got := ParseDelimitedList(nil); got != nil {
		t.Fatalf("nil = %v", got)
	}
}

func TestActivationFromConfig(t *testing.T) {
	m := map[string]any{
		"trigger_phrase":   "!hey",
		"ignore_groups":    "false",
		"allowed_chat_ids": "10, 20",
		"allowed_user_ids": []any{int64(7), "8"},
	}
	p := ActivationFromConfig(m, true)
	if p.TriggerPhrase != "!hey" {
		t.Fatalf("trigger = %q", p.TriggerPhrase)
	}
	if p.IgnoreGroups {
		t.Fatal("ignore_groups should parse false")
	}
	if !reflect.DeepEqual(p.AllowedThreadIDs, []string{"10", "20"}) {
		t.Fatalf("threads = %v", p.AllowedThreadIDs)
	}
	// string entries come via ParseDelimitedList, int64 entries via ParseInt64List
	if len(p.AllowedUserIDs) != 2 {
		t.Fatalf("users = %v", p.AllowedUserIDs)
	}
}

func TestActivationFromConfigDefaults(t *testing.T) {
	p := ActivationFromConfig(map[string]any{}, true)
	if p.TriggerPhrase != "!soulacy" {
		t.Fatalf("default trigger = %q", p.TriggerPhrase)
	}
	if !p.IgnoreGroups {
		t.Fatal("default ignore_groups should honour the fallback")
	}
}
