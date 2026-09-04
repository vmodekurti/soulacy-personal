package gateway

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

// memVault is an in-memory credentials.Vault for the secrets handler tests.
type memVault struct {
	data map[string]map[string][]byte // agentID -> key -> value
}

func newMemVault() *memVault { return &memVault{data: map[string]map[string][]byte{}} }

func (m *memVault) Set(_ context.Context, agentID, key string, value []byte) error {
	if m.data[agentID] == nil {
		m.data[agentID] = map[string][]byte{}
	}
	m.data[agentID][key] = append([]byte(nil), value...)
	return nil
}
func (m *memVault) Get(_ context.Context, agentID, key string) ([]byte, error) {
	if v, ok := m.data[agentID][key]; ok {
		return v, nil
	}
	return nil, os.ErrNotExist
}
func (m *memVault) Delete(_ context.Context, agentID, key string) error {
	delete(m.data[agentID], key)
	return nil
}
func (m *memVault) List(_ context.Context, agentID string) ([]string, error) {
	var keys []string
	for k := range m.data[agentID] {
		keys = append(keys, k)
	}
	return keys, nil
}
func (m *memVault) WriteBlob(ctx context.Context, a, k string, d []byte) error {
	return m.Set(ctx, a, k, d)
}
func (m *memVault) ReadBlob(ctx context.Context, a, k string) ([]byte, error) {
	return m.Get(ctx, a, k)
}
func (m *memVault) Close() error { return nil }

// TestGatewaySecretsCatalog asserts GET /secrets returns 200 with a secrets
// array and never leaks values.
func TestGatewaySecretsCatalog(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.SetCredentialVault(newMemVault())

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/secrets", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("list secrets status = %d body=%v", status, body)
	}
	if _, ok := body["secrets"].([]any); !ok {
		t.Fatalf("expected secrets array, body=%v", body)
	}
}

// TestGatewaySecretsSetGetDelete exercises the full PUT/GET/DELETE lifecycle and
// asserts that the stored value never appears in any response body.
func TestGatewaySecretsSetGetDelete(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.SetCredentialVault(newMemVault())

	const name = "llm.providers.anthropic.api_key"
	const secretValue = "sk-super-secret-value-123"

	// PUT stores the secret → 204.
	status, raw := gatewayRaw(t, s, http.MethodPut, "/api/v1/secrets/"+name, "secret", `{"value":"`+secretValue+`"}`)
	if status != http.StatusNoContent {
		t.Fatalf("put secret status = %d body=%s", status, raw)
	}
	if strings.Contains(raw, secretValue) {
		t.Fatalf("PUT response leaked secret value: %s", raw)
	}

	// GET catalog shows the slot as set:true, without the value.
	gStatus, gRaw := gatewayRaw(t, s, http.MethodGet, "/api/v1/secrets", "secret", "")
	if gStatus != http.StatusOK {
		t.Fatalf("get secrets status = %d body=%s", gStatus, gRaw)
	}
	if strings.Contains(gRaw, secretValue) {
		t.Fatalf("GET catalog leaked secret value: %s", gRaw)
	}
	if !secretSlotSet(t, s, name) {
		t.Fatalf("expected %s set:true after PUT, body=%s", name, gRaw)
	}

	// DELETE removes it → 204.
	dStatus, dRaw := gatewayRaw(t, s, http.MethodDelete, "/api/v1/secrets/"+name, "secret", "")
	if dStatus != http.StatusNoContent {
		t.Fatalf("delete secret status = %d body=%s", dStatus, dRaw)
	}
	if strings.Contains(dRaw, secretValue) {
		t.Fatalf("DELETE response leaked secret value: %s", dRaw)
	}

	// GET catalog now shows set:false.
	if secretSlotSet(t, s, name) {
		t.Fatalf("expected %s set:false after DELETE", name)
	}
}

// TestGatewaySecretsNilVault asserts PUT/DELETE return 503 when no vault is set.
func TestGatewaySecretsNilVault(t *testing.T) {
	s := newTestGateway(t, "secret")
	// No vault wired.

	// GET still works (catalog from config, all set:false).
	if status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/secrets", "secret", ""); status != http.StatusOK {
		t.Fatalf("list secrets (nil vault) status = %d body=%v", status, body)
	}

	status, body := gatewayJSON(t, s, http.MethodPut, "/api/v1/secrets/server.api_key", "secret", `{"value":"x"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("put secret (nil vault) status = %d body=%v", status, body)
	}
}

// TestGatewaySecretsEmptyValueRejected asserts an empty value yields 400.
func TestGatewaySecretsEmptyValueRejected(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.SetCredentialVault(newMemVault())

	status, body := gatewayJSON(t, s, http.MethodPut, "/api/v1/secrets/server.api_key", "secret", `{"value":""}`)
	if status != http.StatusBadRequest {
		t.Fatalf("put empty value status = %d body=%v", status, body)
	}
}

// secretSlotSet fetches the catalog and reports whether the named slot is set.
func secretSlotSet(t *testing.T, s *Server, name string) bool {
	t.Helper()
	_, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/secrets", "secret", "")
	arr, ok := body["secrets"].([]any)
	if !ok {
		t.Fatalf("secrets not an array: %v", body)
	}
	for _, item := range arr {
		d, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if d["name"] == name {
			set, _ := d["set"].(bool)
			return set
		}
	}
	return false
}
