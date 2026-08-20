// keyname_test.go — the union, the normalisation, and the one false positive
// worth removing.
package redact

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every term any of the four previous lists carried is still caught. The point
// of unifying them was that each was missing something another had — so the
// union is the assertion, not the fact that a list exists.
func TestTheUnionOfEveryPreviousListIsStillCaught(t *testing.T) {
	// Each entry is annotated with the list it came from, so a future
	// deletion is a decision rather than an accident.
	for _, key := range []string{
		// internal/redact
		"api_key", "password", "passwd", "secret", "token", "credential",
		"authorization", "private_key", "signing_key", "dsn", "database_url",
		"connection_string", "cookie",
		// internal/audit added nothing the others lacked, but carried:
		"auth",
		// internal/approvals
		"apikey", "accesskey", "privatekey", "bearer", "session", "signature",
		"certificate", "passphrase", "pin", "otp", "seed", "mnemonic",
		// internal/gateway
		"api_key", "apikey", "private_key", "privatekey",
	} {
		if !SecretKeyName(key) {
			t.Errorf("%q is no longer treated as secret; it was on one of the four lists this replaced", key)
		}
	}
}

// Normalisation collapses the four spellings of one header, which is exactly
// how a key-name list ends up with a hole nobody sees.
func TestFourSpellingsOfOneHeaderAreOneCase(t *testing.T) {
	for _, key := range []string{"X-Api-Key", "x_api_key", "apiKey", "APIKEY", "api-key"} {
		if !SecretKeyName(key) {
			t.Errorf("%q was not recognised", key)
		}
	}
}

// The false positive the affix rule exists to remove. Matched as a substring,
// `pin` redacts every field called `mapping` — which contains p-i-n and
// nothing sensitive. The approvals redactor did exactly that.
func TestAmbiguousShortTermsMatchOnlyAtAWordBoundary(t *testing.T) {
	redacted := []string{"pin", "pincode", "userpin", "otp", "otpsecret", "seed", "seedphrase", "authtoken"}
	for _, key := range redacted {
		if !SecretKeyName(key) {
			t.Errorf("%q should be redacted", key)
		}
	}
	kept := []string{"mapping", "spinner", "shipping", "topping"}
	for _, key := range kept {
		if SecretKeyName(key) {
			t.Errorf("%q was redacted; a short term matched in the middle of an ordinary word", key)
		}
	}
}

// The trade this package makes, asserted so it is a decision rather than a
// surprise: a field named `author` is elided because it starts with `auth`.
// A lost diagnostic beats a leaked credential, and the package doc already
// chose that direction.
func TestTheOverRedactionTradeIsExplicit(t *testing.T) {
	if !SecretKeyName("author") {
		t.Fatal("`author` is no longer redacted — if that is deliberate, the trade in " +
			"SecretKeyName's comment needs updating, because it says the opposite")
	}
	// And the ordinary keys an audit trail needs to keep are kept.
	for _, key := range []string{"actor", "subject", "workspace_id", "agent_id", "status", "request_id", "reason"} {
		if SecretKeyName(key) {
			t.Errorf("%q was redacted; an audit trail that hides these answers nothing", key)
		}
	}
}

func TestAnEmptyKeyIsNotSecret(t *testing.T) {
	for _, key := range []string{"", "   ", "-_-"} {
		if SecretKeyName(key) {
			t.Errorf("%q was treated as secret", key)
		}
	}
}

// coreSecretTerms are the words a credential key-name list is built from.
//
// The guard below counts DISTINCT terms on one line, because that is the shape
// these lists actually take — a `[]string{...}` or a `regexp.MustCompile` with
// several of them together. A single mention is an ordinary reference to an
// api key; three on one line is somebody enumerating what looks secret.
var coreSecretTerms = []string{
	"password", "passwd", "passphrase", "secret", "token", "credential",
	"apikey", "api_key", "accesskey", "privatekey", "private_key",
	"authorization", "bearer", "cookie", "mnemonic",
}

// listShape narrows further to lines that are literals, so a switch statement
// or a doc comment listing the same words is not mistaken for a policy.
var listShape = regexp.MustCompile(`(\[\]string\{|MustCompile|\bcase\b.*:)`)

func TestOnlyOnePackageDecidesWhatLooksSecret(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	// Files allowed their own list, with the reason it is not a key-name test.
	allowed := map[string]string{
		// Redacts a config FILE by YAML node, where the question is which
		// section to blank wholesale rather than which key is a credential.
		"internal/supportbundle/bundle.go": "redacts config sections by YAML node, not key names",
		// Moves values out of config.yaml into the vault; its list names the
		// config keys that MIGRATE, which is a different set from the keys
		// that look secret.
		"internal/secrets/migrate.go": "names the config keys that migrate into the vault",
		// Detects prompt-injection attempts in free PROSE — "reveal your api
		// key", "print your credentials". It matches an attacker's sentence,
		// not a field name, and widening it with this package's list would
		// make it fire on the word `session`.
		"internal/injection/scanner.go": "matches natural-language exfiltration attempts, not key names",
		// Matches provider ERROR MESSAGES to diagnose why a call failed
		// ("invalid api key", "expired token"). The strings are other
		// people's API responses; they are not names of anything we hold.
		"internal/llm/providerdoctor.go": "classifies provider error text, not key names",
	}
	var offenders []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor", "site", "gui", "website", "sdk", "examples", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		if strings.HasPrefix(rel, filepath.Join("internal", "redact")+string(filepath.Separator)) {
			return nil
		}
		if _, ok := allowed[rel]; ok {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(source), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !listShape.MatchString(line) {
				continue
			}
			lowered := strings.ToLower(line)
			distinct := map[string]bool{}
			for _, term := range coreSecretTerms {
				if strings.Contains(lowered, term) {
					distinct[term] = true
				}
			}
			if len(distinct) >= 3 {
				offenders = append(offenders, rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, offender := range offenders {
		t.Errorf("%s appears to carry its own credential key-name list — use redact.SecretKeyName, "+
			"or add it to `allowed` with the reason it is not a key-name test. Four such lists existed "+
			"and no two agreed; each was missing terms another had, silently", offender)
	}
}
