package voice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeTransport records the request and returns a canned response
// (house rule: no httptest.NewServer).
type fakeTransport struct {
	status  int
	body    string
	gotReq  *http.Request
	gotBody []byte
	err     error
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.gotReq = req
	if req.Body != nil {
		f.gotBody, _ = io.ReadAll(req.Body)
	}
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func minterWith(t *testing.T, ft *fakeTransport) *OpenAIMinter {
	t.Helper()
	m := NewOpenAIMinter("sk-test", "gpt-realtime-mini", "")
	m.SetClient(&http.Client{Transport: ft})
	return m
}

func TestOpenAIMinter_Ready(t *testing.T) {
	m := NewOpenAIMinter("sk-test", "", "")
	ready, _ := m.Ready()
	if !ready {
		t.Fatal("minter with key should be ready")
	}
	m2 := NewOpenAIMinter("", "", "")
	ready, detail := m2.Ready()
	if ready {
		t.Fatal("minter without key must not be ready")
	}
	if detail == "" {
		t.Fatal("not-ready must explain itself")
	}
}

func TestOpenAIMinter_Defaults(t *testing.T) {
	m := NewOpenAIMinter("sk-test", "", "")
	if m.model != "gpt-realtime-mini" {
		t.Fatalf("default model = %q", m.model)
	}
	if m.baseURL != "https://api.openai.com" {
		t.Fatalf("default baseURL = %q", m.baseURL)
	}
	if m.Provider() != "openai" {
		t.Fatalf("Provider = %q", m.Provider())
	}
}

func TestOpenAIMinter_Mint_ClientSecretsShape(t *testing.T) {
	exp := time.Now().Add(time.Minute).Unix()
	ft := &fakeTransport{status: 200,
		body: `{"value":"ek_abc","expires_at":` + jsonInt(exp) + `}`}
	m := minterWith(t, ft)

	key, err := m.Mint(context.Background())
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if key.Key != "ek_abc" {
		t.Fatalf("Key = %q", key.Key)
	}
	if key.ExpiresAt.Unix() != exp {
		t.Fatalf("ExpiresAt = %v want unix %d", key.ExpiresAt, exp)
	}
	if key.Model != "gpt-realtime-mini" || key.Provider != "openai" {
		t.Fatalf("key = %+v", key)
	}

	// Request shape: POST <base>/v1/realtime/client_secrets, bearer auth,
	// session.model in the JSON body.
	if ft.gotReq.Method != http.MethodPost ||
		ft.gotReq.URL.String() != "https://api.openai.com/v1/realtime/client_secrets" {
		t.Fatalf("request = %s %s", ft.gotReq.Method, ft.gotReq.URL)
	}
	if got := ft.gotReq.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("auth header = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(ft.gotBody, &body); err != nil {
		t.Fatal(err)
	}
	sess, _ := body["session"].(map[string]any)
	if sess == nil || sess["model"] != "gpt-realtime-mini" {
		t.Fatalf("request body = %s", ft.gotBody)
	}
}

func TestOpenAIMinter_Mint_LegacySessionShape(t *testing.T) {
	exp := time.Now().Add(time.Minute).Unix()
	ft := &fakeTransport{status: 200,
		body: `{"client_secret":{"value":"ek_legacy","expires_at":` + jsonInt(exp) + `}}`}
	m := minterWith(t, ft)
	key, err := m.Mint(context.Background())
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if key.Key != "ek_legacy" || key.ExpiresAt.Unix() != exp {
		t.Fatalf("key = %+v", key)
	}
}

func TestOpenAIMinter_Mint_HTTPError(t *testing.T) {
	ft := &fakeTransport{status: 401, body: `{"error":{"message":"bad key"}}`}
	m := minterWith(t, ft)
	_, err := m.Mint(context.Background())
	if err == nil {
		t.Fatal("Mint with 401 = nil error")
	}
	if strings.Contains(err.Error(), "sk-test") {
		t.Fatal("error must not echo the API key")
	}
}

func TestOpenAIMinter_Mint_NoKey(t *testing.T) {
	m := NewOpenAIMinter("", "", "")
	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("Mint without key = nil error")
	}
}

func TestOpenAIMinter_Mint_EmptyValue(t *testing.T) {
	ft := &fakeTransport{status: 200, body: `{}`}
	m := minterWith(t, ft)
	if _, err := m.Mint(context.Background()); err == nil {
		t.Fatal("Mint with empty payload = nil error")
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
