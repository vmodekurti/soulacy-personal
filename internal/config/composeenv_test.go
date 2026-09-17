package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// The container must not choose the user's provider or model.
//
// Compose used to set SOULACY_LLM_DEFAULT_PROVIDER to a literal "ollama" and
// the Ollama model to "llama3". Because an environment variable beats the
// config file, those were not fallbacks for an unconfigured install — they
// were overrides that re-applied on every `docker compose up`, silently
// reverting a working ollama-cloud setup to a local Ollama that was not
// running. The symptom reached the user before the cause did.
//
// The fix is an empty default, which viper ignores. These tests pin both
// halves: that the compose files carry no literal, and that an empty value
// really does leave the file's choice alone.
func TestComposeDoesNotPinTheProviderOrModel(t *testing.T) {
	// key: value on a line, capturing the ${VAR:-default} default.
	defaultOf := regexp.MustCompile(`\$\{[A-Z_]+:-([^}]*)\}`)

	for _, name := range []string{"docker-compose.yml", "docker-compose.lite.yml"} {
		path := filepath.Join("..", "..", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			key, _, ok := strings.Cut(trimmed, ":")
			if !ok {
				continue
			}
			switch strings.TrimSpace(key) {
			case "SOULACY_LLM_DEFAULT_PROVIDER", "SOULACY_LLM_PROVIDERS_OLLAMA_MODEL":
			default:
				continue
			}
			m := defaultOf.FindStringSubmatch(trimmed)
			if m == nil {
				t.Errorf("%s: %s must come from the environment, not a literal: %s", name, key, trimmed)
				continue
			}
			if strings.TrimSpace(m[1]) != "" {
				t.Errorf("%s: %s defaults to %q, which overrides the user's config on every recreation; leave it empty",
					name, strings.TrimSpace(key), m[1])
			}
		}
	}
}

// The empty default above only works because viper treats an environment
// variable that is set but empty as unset. If that ever changes, the compose
// files start blanking the provider instead of deferring to the file, so the
// assumption is asserted rather than trusted.
func TestAnEmptyEnvVarDoesNotOverrideTheConfigFile(t *testing.T) {
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader("llm:\n  default_provider: ollama-cloud\n")); err != nil {
		t.Fatalf("reading probe config: %v", err)
	}
	v.SetEnvPrefix("SOULACY")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	t.Setenv("SOULACY_LLM_DEFAULT_PROVIDER", "")
	if got := v.GetString("llm.default_provider"); got != "ollama-cloud" {
		t.Errorf("an empty env var should leave the file alone; got %q", got)
	}

	// And an operator who does set one still wins, which is the escape hatch
	// the empty default preserves.
	t.Setenv("SOULACY_LLM_DEFAULT_PROVIDER", "anthropic")
	if got := v.GetString("llm.default_provider"); got != "anthropic" {
		t.Errorf("a set env var should still override; got %q", got)
	}
}
