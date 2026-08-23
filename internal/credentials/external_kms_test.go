package credentials

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVaultTransitUsesWorkloadIdentityAndWorkspaceContext(t *testing.T) {
	jwtPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(jwtPath, []byte("signed-service-account-jwt"), 0o600); err != nil {
		t.Fatal(err)
	}
	var loginCalls int
	var audited []KMSAuditEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/v1/auth/kubernetes/login":
			loginCalls++
			if body["role"] != "soulacy-worker" || body["jwt"] != "signed-service-account-jwt" {
				t.Errorf("unexpected workload identity request: %#v", body)
			}
			_, _ = w.Write([]byte(`{"auth":{"client_token":"short-lived-token"}}`))
		case "/v1/transit/encrypt/soulacy":
			if r.Header.Get("X-Vault-Token") != "short-lived-token" {
				t.Error("transit request did not use workload identity token")
			}
			if body["context"] != base64.StdEncoding.EncodeToString([]byte("ws-a")) {
				t.Error("workspace was not cryptographically bound as Vault context")
			}
			_, _ = w.Write([]byte(`{"data":{"ciphertext":"vault:v1:cipher"}}`))
		case "/v1/transit/decrypt/soulacy":
			_, _ = w.Write([]byte(`{"data":{"plaintext":"c2VjcmV0"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	wrapper, err := NewVaultTransit(VaultTransitConfig{Address: server.URL, Key: "soulacy", KubernetesRole: "soulacy-worker", JWTPath: jwtPath, Auditor: func(e KMSAuditEvent) { audited = append(audited, e) }})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := wrapper.WrapKey(context.Background(), "ws-a", []byte("secret"))
	if err != nil || string(wrapped) != "vault:v1:cipher" {
		t.Fatalf("wrap=%q err=%v", wrapped, err)
	}
	plain, err := wrapper.UnwrapKey(context.Background(), "ws-a", wrapped)
	if err != nil || string(plain) != "secret" {
		t.Fatalf("unwrap=%q err=%v", plain, err)
	}
	if loginCalls != 1 {
		t.Fatalf("workload identity login calls=%d, want cached token", loginCalls)
	}
	if len(audited) != 2 || !audited[0].Succeeded || audited[0].WorkspaceID != "ws-a" {
		t.Fatalf("metadata audit=%#v", audited)
	}
}

type mismatchedWrapper struct{}

func (mismatchedWrapper) WrapKey(context.Context, string, []byte) ([]byte, error) {
	return []byte("wrapped"), nil
}
func (mismatchedWrapper) UnwrapKey(context.Context, string, []byte) ([]byte, error) {
	return []byte("wrong"), nil
}

func TestVerifyKeyWrapperFailsClosedOnMismatch(t *testing.T) {
	if err := VerifyKeyWrapper(context.Background(), mismatchedWrapper{}); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("startup probe error=%v", err)
	}
}
