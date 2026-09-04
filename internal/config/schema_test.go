package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSchemaRejectsUnknownSecurityCriticalKeys(t *testing.T) {
	for _, body := range []string{
		"server:\n  api_keey: value\n",
		"runtime:\n  ssrf_protecton: true\n",
		"auth:\n  jwt_secert: value\n",
		"security:\n  intent_gaet: deny\n",
	} {
		if err := ValidateSchema([]byte(body)); err == nil {
			t.Fatalf("accepted unknown key in:\n%s", body)
		}
	}
}

func TestSchemaExplicitlyRejectsMisleadingAllowSystemTools(t *testing.T) {
	err := ValidateSchema([]byte("runtime:\n  allow_system_tools: false\n"))
	if err == nil || !strings.Contains(err.Error(), "allow_system_agents") {
		t.Fatalf("error = %v", err)
	}
}

func TestSchemaAllowsDeclaredExtensionMaps(t *testing.T) {
	body := "channels:\n  custom:\n    future_option: yes\nplugins_config:\n  x:\n    arbitrary: value\nmcp:\n  servers:\n    x:\n      env:\n        CASE_SENSITIVE: value\n"
	if err := ValidateSchema([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func TestShippedExampleConfigsMatchSchema(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "configs", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no example configs found")
	}
	for _, path := range matches {
		if err := ValidateSchemaFile(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestSecurityDocumentationNamesRealSchemaKeys(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration", "security.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, key := range []string{"runtime.allow_system_agents", "runtime.sandbox", "runtime.ssrf_protection", "server.allow_unauthenticated", "security.intent_gate"} {
		parts := strings.Split(key, ".")
		if _, ok := schemaFieldsForPath(parts); !ok {
			t.Fatalf("documented key %q is not in real schema", key)
		}
		if !strings.Contains(doc, parts[len(parts)-1]) {
			t.Errorf("security docs omit real key %q", key)
		}
	}
}

func schemaFieldsForPath(parts []string) (any, bool) {
	typ := reflect.TypeOf(Config{})
	for _, part := range parts {
		field, ok := schemaFields(typ)[part]
		if !ok {
			return nil, false
		}
		for field.Kind() == reflect.Pointer {
			field = field.Elem()
		}
		typ = field
	}
	return typ, true
}
