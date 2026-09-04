package cfgmap

import "testing"

func TestStr(t *testing.T) {
	m := map[string]any{"a": "x", "b": 7, "empty": ""}
	if got := Str(m, "a", "d"); got != "x" {
		t.Fatalf("Str a = %q", got)
	}
	if got := Str(m, "missing", "d"); got != "d" {
		t.Fatalf("Str missing = %q, want default", got)
	}
	if got := Str(m, "empty", "d"); got != "d" {
		t.Fatalf("Str empty string should fall back to default, got %q", got)
	}
	// non-string values do not stringify implicitly
	if got := Str(m, "b", "d"); got != "d" {
		t.Fatalf("Str non-string = %q, want default", got)
	}
	if got := Str(nil, "a", "d"); got != "d" {
		t.Fatalf("Str nil map = %q, want default", got)
	}
}

func TestInt(t *testing.T) {
	m := map[string]any{
		"i": 5, "i64": int64(6), "f": float64(7), "s": "8", "bad": "x",
	}
	cases := []struct {
		key  string
		want int
	}{
		{"i", 5}, {"i64", 6}, {"f", 7}, {"s", 8}, {"bad", -1}, {"missing", -1},
	}
	for _, c := range cases {
		if got := Int(m, c.key, -1); got != c.want {
			t.Errorf("Int(%q) = %d, want %d", c.key, got, c.want)
		}
	}
}

func TestBool(t *testing.T) {
	m := map[string]any{"t": true, "f": false, "st": "yes", "sf": "off", "junk": "maybe"}
	if !Bool(m, "t", false) {
		t.Fatal("Bool t")
	}
	if Bool(m, "f", true) {
		t.Fatal("Bool f")
	}
	if !Bool(m, "st", false) {
		t.Fatal("Bool yes-string")
	}
	if Bool(m, "sf", true) {
		t.Fatal("Bool off-string")
	}
	if !Bool(m, "junk", true) {
		t.Fatal("Bool junk should fall back to default")
	}
	if !Bool(m, "missing", true) {
		t.Fatal("Bool missing should fall back to default")
	}
}

func TestMap(t *testing.T) {
	inner := map[string]any{"k": "v"}
	m := map[string]any{"m": inner, "notmap": 3}
	if got := Map(m, "m"); got == nil || got["k"] != "v" {
		t.Fatalf("Map = %v", got)
	}
	if got := Map(m, "notmap"); got != nil {
		t.Fatalf("Map non-map = %v, want nil", got)
	}
	if got := Map(m, "missing"); got != nil {
		t.Fatalf("Map missing = %v, want nil", got)
	}
}

func TestBoolPtr(t *testing.T) {
	tv, fv := true, false
	m := map[string]any{"b": true, "p": &fv, "junk": "x"}
	if got := BoolPtr(m, "b"); got == nil || *got != true {
		t.Fatalf("BoolPtr bool = %v", got)
	}
	if got := BoolPtr(m, "p"); got == nil || *got != false {
		t.Fatalf("BoolPtr *bool = %v", got)
	}
	if got := BoolPtr(m, "missing"); got != nil {
		t.Fatalf("BoolPtr missing = %v, want nil", got)
	}
	if got := BoolPtr(m, "junk"); got != nil {
		t.Fatalf("BoolPtr junk = %v, want nil", got)
	}
	_ = tv
}
