package buildtool

import (
	"strings"
	"testing"
)

func TestParseWith(t *testing.T) {
	m, v, err := parseWith("github.com/acme/soulacy-matrix@v1.2.3")
	if err != nil || m != "github.com/acme/soulacy-matrix" || v != "v1.2.3" {
		t.Fatalf("got %q %q %v", m, v, err)
	}
	m, v, err = parseWith("github.com/acme/soulacy-matrix")
	if err != nil || m != "github.com/acme/soulacy-matrix" || v != "latest" {
		t.Fatalf("no-version: got %q %q %v", m, v, err)
	}
	if _, _, err := parseWith(""); err == nil {
		t.Fatal("empty must error")
	}
	if _, _, err := parseWith("@v1"); err == nil {
		t.Fatal("missing module path must error")
	}
	if _, _, err := parseWith("bad path with spaces@v1"); err == nil {
		t.Fatal("spaces must error")
	}
}

func TestGenerateExtraImports(t *testing.T) {
	src := generateExtraImports([]string{
		"github.com/acme/soulacy-matrix",
		"github.com/acme/soulacy-iris",
	})
	for _, want := range []string{
		"package main",
		`_ "github.com/acme/soulacy-matrix"`,
		`_ "github.com/acme/soulacy-iris"`,
		"DO NOT EDIT",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("generated file missing %q:\n%s", want, src)
		}
	}
}

func TestGenerateExtraImportsEmpty(t *testing.T) {
	if got := generateExtraImports(nil); got != nil {
		t.Fatalf("no modules → no file, got %q", got)
	}
}
