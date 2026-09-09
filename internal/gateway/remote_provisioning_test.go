package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPutOwnedMCPServerUpsertsPersonalConfig(t *testing.T) {
	s := newTestGateway(t, "secret")
	root := t.TempDir()
	s.cfgPath = filepath.Join(root, "config.yaml")
	if err := os.WriteFile(s.cfgPath, []byte("mcp:\n  servers: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "mcp-servers", "weather", "weather-mcp")
	if err := os.MkdirAll(filepath.Dir(command), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"transport":"stdio","command":%q,"args":["--json"],"env":{"REGION":"us"}}`, command)
	status, response := gatewayJSON(t, s, http.MethodPut, "/api/v1/mcp/own/weather", "secret", body)
	if status != http.StatusCreated || response["created"] != true {
		t.Fatalf("first upsert status/body = %d %#v", status, response)
	}
	status, response = gatewayJSON(t, s, http.MethodPut, "/api/v1/mcp/own/weather", "secret", body)
	if status != http.StatusOK || response["created"] != false {
		t.Fatalf("second upsert status/body = %d %#v", status, response)
	}
	raw, err := os.ReadFile(s.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"weather-mcp", "REGION", "--json", "managed_only: true"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config missing %q: %s", want, raw)
		}
	}
}

func TestPutOwnedMCPServerEnforcesNetworkAndManagedPathBoundaries(t *testing.T) {
	s := newTestGateway(t, "secret")
	root := t.TempDir()
	s.cfgPath = filepath.Join(root, "config.yaml")
	_ = os.WriteFile(s.cfgPath, []byte("{}\n"), 0o600)

	status, response := gatewayJSON(t, s, http.MethodPut, "/api/v1/mcp/own/private", "secret", `{"transport":"http","url":"https://127.0.0.1/mcp"}`)
	if status != http.StatusForbidden || !strings.Contains(response["error"].(string), "public-network") {
		t.Fatalf("private endpoint status/body = %d %#v", status, response)
	}
	status, response = gatewayJSON(t, s, http.MethodPut, "/api/v1/mcp/own/outside", "secret", `{"transport":"stdio","command":"/bin/sh"}`)
	if status != http.StatusForbidden || !strings.Contains(response["error"].(string), "mcp-servers") {
		t.Fatalf("outside command status/body = %d %#v", status, response)
	}
}

func TestPutOwnedMCPServerAppliesEditionPolicy(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	_ = os.WriteFile(s.cfgPath, []byte("{}\n"), 0o600)
	s.SetMCPRegistrationPolicy(func(transport, endpoint, command string) error {
		if transport == "stdio" || !strings.HasPrefix(endpoint, "https://") {
			return errors.New("Team/Scale remote MCP definitions must use HTTPS; stdio host processes are not allowed")
		}
		return nil
	})
	status, response := gatewayJSON(t, s, http.MethodPut, "/api/v1/mcp/own/local", "secret", `{"transport":"stdio","command":"local-mcp"}`)
	if status != http.StatusForbidden || !strings.Contains(response["error"].(string), "must use HTTPS") {
		t.Fatalf("status/body = %d %#v", status, response)
	}
}

func TestRemotePackageInstallJobProgressAndPolicy(t *testing.T) {
	s := newTestGateway(t, "secret")
	s.packageInstallRunner = func(_ context.Context, req PackageInstallRequest, progress func(string)) error {
		if req.Source != "https://1.1.1.1/acme/weather-mcp" {
			t.Fatalf("request = %#v", req)
		}
		progress("Creating venv")
		progress("Installing dependencies")
		return nil
	}
	payload := `{"source":"https://1.1.1.1/acme/weather-mcp","kind":"mcp","allow_unverified":true,"allow_host_build":true}`
	status, response := gatewayJSON(t, s, http.MethodPost, "/api/v1/packages/install", "secret", payload)
	if status != http.StatusAccepted {
		t.Fatalf("start status/body = %d %#v", status, response)
	}
	jobID, _ := response["job_id"].(string)
	deadline := time.Now().Add(time.Second)
	for {
		status, response = gatewayJSON(t, s, http.MethodGet, "/api/v1/packages/install/"+jobID, "secret", "")
		if response["status"] == "succeeded" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status != http.StatusOK || response["status"] != "succeeded" {
		t.Fatalf("job status/body = %d %#v", status, response)
	}
	messages := response["messages"].([]any)
	if len(messages) < 4 {
		t.Fatalf("progress messages = %#v", messages)
	}

	s.SetPackageInstallPolicy(func(PackageInstallRequest) error {
		return errors.New("Team/Scale permits source builds only for approved catalog packages")
	})
	status, response = gatewayJSON(t, s, http.MethodPost, "/api/v1/packages/install", "secret", payload)
	if status != http.StatusForbidden || !strings.Contains(response["error"].(string), "approved catalog") {
		t.Fatalf("policy status/body = %d %#v", status, response)
	}
}

func TestRemotePackageInstallValidation(t *testing.T) {
	s := newTestGateway(t, "secret")
	status, body := gatewayJSON(t, s, http.MethodPost, "/api/v1/packages/install", "secret", `{"source":"https://1.1.1.1/acme/mcp","kind":"mcp","allow_unverified":true}`)
	if status != http.StatusBadRequest || !strings.Contains(body["error"].(string), "allow_host_build") {
		t.Fatalf("status/body = %d %#v", status, body)
	}
}
