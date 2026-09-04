package gateway

import (
	"net/http"
	"testing"
)

func TestExecutorsEndpointReturnsBackendReadiness(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/executors", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("executors status = %d body=%v", status, body)
	}
	if got, _ := body["default_backend"].(string); got == "" {
		t.Fatalf("missing default_backend: %#v", body)
	}
	backends, ok := body["backends"].([]any)
	if !ok || len(backends) < 5 {
		t.Fatalf("backends = %#v", body["backends"])
	}
	foundProcess := false
	for _, raw := range backends {
		b, _ := raw.(map[string]any)
		if b["key"] == "process" {
			foundProcess = true
			if b["kind"] != "local" {
				t.Fatalf("process kind = %#v", b["kind"])
			}
		}
	}
	if !foundProcess {
		t.Fatalf("process backend missing: %#v", backends)
	}
}

func TestReadinessIncludesExecutorParity(t *testing.T) {
	s, _ := newTestGatewayWithLLM(t, "secret")

	status, body := gatewayJSON(t, s, http.MethodGet, "/api/v1/readiness", "secret", "")
	if status != http.StatusOK {
		t.Fatalf("readiness status = %d body=%v", status, body)
	}
	if _, ok := body["executors"].(map[string]any); !ok {
		t.Fatalf("missing executors readiness: %#v", body)
	}
	parity := body["parity"].(map[string]any)
	areas := parity["areas"].([]any)
	for _, raw := range areas {
		area := raw.(map[string]any)
		if area["key"] == "remote_execution" {
			if got, _ := area["score"].(float64); got <= 0 {
				t.Fatalf("remote execution score = %v", area["score"])
			}
			return
		}
	}
	t.Fatalf("remote_execution parity area missing: %#v", areas)
}

func TestParityRemoteExecutionScoresRemoteBackend(t *testing.T) {
	area := parityRemoteExecution(executorReadiness{Backends: []executorBackendStatus{
		{Key: "process", Kind: "local", Status: "ok", Configured: true},
		{Key: "docker", Kind: "container", Status: "ok", Configured: true},
	}})
	if area.Status != "ok" || area.Score < 80 {
		t.Fatalf("remote parity = %#v, want ok >= 80", area)
	}

	area = parityRemoteExecution(executorReadiness{Backends: []executorBackendStatus{
		{Key: "process", Kind: "local", Status: "fail", Configured: true},
	}})
	if area.Status != "fail" {
		t.Fatalf("remote parity = %#v, want fail", area)
	}
}
