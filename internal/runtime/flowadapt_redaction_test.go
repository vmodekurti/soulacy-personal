// flowadapt_redaction_test.go — the flow-repair snapshot leaves the machine.
//
// WHY THIS FILE EXISTS AT ALL: the redaction on this path had no test. Every
// other redactor in the repo (audit, approvals, admin_audit, gateway config)
// has one, and this is the only one whose output is sent to a third-party LLM
// rather than written to a local file. The least-covered path was the most
// exposed one.
//
// The tests assert through flowVarsSnapshot, the function whose result is
// concatenated into the prompt, rather than through isSensitiveFlowRepairKey.
// Testing the predicate directly would pass even if a future edit stopped
// calling it — which is the failure that matters here, since the value still
// looks fine in the vars map and only the outbound prompt changes.
package runtime

import (
	"strings"
	"testing"
)

func TestTheFlowRepairPromptCarriesNoCredentialValues(t *testing.T) {
	// Each name below was NOT caught by the list this path used to carry:
	// it knew password/passwd/secret/token/api_key/apikey/authorization/
	// cookie/credential/private_key and nothing else.
	vars := map[string]any{
		"bearer_header":    "Bearer eyJhbGciOi.SHOULD_NOT_APPEAR",
		"vault_passphrase": "correct horse SHOULD_NOT_APPEAR",
		"aws_accesskey":    "AKIA_SHOULD_NOT_APPEAR",
		"signing_key":      "MIIEv_SHOULD_NOT_APPEAR",
		"session":          "sid_SHOULD_NOT_APPEAR",
		"db_dsn":           "postgres://u:SHOULD_NOT_APPEAR@h/db",
		"jwt":              "eyJ.SHOULD_NOT_APPEAR",
		// And the values a repair genuinely needs must survive, or the
		// snapshot is useless and someone will delete the redaction to get
		// their diagnostics back.
		"order_id": "ord-4417",
		"mapping":  map[string]any{"from": "csv", "to": "json"},
	}

	snapshot := flowVarsSnapshot(vars)

	if strings.Contains(snapshot, "SHOULD_NOT_APPEAR") {
		t.Errorf("a credential value reached the outbound repair prompt:\n%s", snapshot)
	}
	for _, want := range []string{"ord-4417", "mapping", "csv"} {
		if !strings.Contains(snapshot, want) {
			t.Errorf("snapshot dropped the non-secret value %q, leaving the model nothing to repair from:\n%s", want, snapshot)
		}
	}
}

func TestACredentialNestedInsideAPlainObjectIsStillRedacted(t *testing.T) {
	// The recursion passes the CHILD key down, so a secret one level below a
	// harmless container is judged on its own name. A flow's vars are almost
	// always shaped this way — a step's whole output under one key — so
	// top-level-only redaction would cover nearly nothing in practice.
	vars := map[string]any{
		"http_request": map[string]any{
			"url": "https://api.example.com/v1/orders",
			"headers": map[string]any{
				"Authorization": "Bearer SHOULD_NOT_APPEAR",
				"Accept":        "application/json",
			},
		},
	}

	snapshot := flowVarsSnapshot(vars)

	if strings.Contains(snapshot, "SHOULD_NOT_APPEAR") {
		t.Errorf("a nested credential reached the outbound repair prompt:\n%s", snapshot)
	}
	if !strings.Contains(snapshot, "api.example.com") {
		t.Errorf("the surrounding request shape was lost, so the redaction took the diagnostic with it:\n%s", snapshot)
	}
}
