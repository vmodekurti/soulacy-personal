package gateway

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// #169 — the gateway's own certificate: created once, reloaded unchanged, and
// its fingerprint is the SHA-256 of the uncompressed EC point — the exact
// bytes iOS hashes (SecKeyCopyExternalRepresentation), not the SPKI.
func TestAutoCert_CreateReloadFingerprint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	cert, created, err := loadOrCreateAutoCert(dir, []string{"mac.local"}, []net.IP{net.IPv4(192, 168, 1, 5)})
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	fp1, err := publicKeyFingerprint(cert)
	if err != nil || len(fp1) != 64 {
		t.Fatalf("fingerprint: %q err=%v", fp1, err)
	}
	again, created, err := loadOrCreateAutoCert(dir, nil, nil)
	if err != nil || created {
		t.Fatalf("reload: created=%v err=%v (must reuse, phones are pinned to it)", created, err)
	}
	fp2, _ := publicKeyFingerprint(again)
	if fp1 != fp2 {
		t.Fatalf("fingerprint changed across reload: %s vs %s", fp1, fp2)
	}

	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	pub := leaf.PublicKey.(*ecdsa.PublicKey)
	ecdhKey, _ := pub.ECDH()
	want := sha256.Sum256(ecdhKey.Bytes()) // 0x04 || X || Y, what SecKey exports
	if fp1 != hex.EncodeToString(want[:]) {
		t.Fatalf("fingerprint is not SHA-256 of the uncompressed point")
	}
	if len(ecdhKey.Bytes()) != 65 || ecdhKey.Bytes()[0] != 0x04 {
		t.Fatalf("expected a 65-byte uncompressed P-256 point")
	}
	for _, name := range []string{"localhost", "mac.local"} {
		if err := leaf.VerifyHostname(name); err != nil {
			t.Errorf("SAN %s missing: %v", name, err)
		}
	}
	if err := leaf.VerifyHostname("192.168.1.5"); err != nil {
		t.Errorf("IP SAN missing: %v", err)
	}
}

// The same port answers plain HTTP and TLS; a TLS client that checks the
// pinned fingerprint (as the phone does) is served, and plain HTTP still works.
func TestMuxListener_ServesBothOnOnePort(t *testing.T) {
	cert, _, err := loadOrCreateAutoCert(filepath.Join(t.TempDir(), "tls"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := publicKeyFingerprint(cert)
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newMuxListener(inner, autoTLSConfig(cert))
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			_, _ = io.WriteString(w, "tls")
		} else {
			_, _ = io.WriteString(w, "plain")
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	addr := inner.Addr().String()

	get := func(client *http.Client, url string) string {
		t.Helper()
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if got := get(&http.Client{Timeout: 5 * time.Second}, "http://"+addr+"/"); got != "plain" {
		t.Fatalf("plain http = %q", got)
	}
	pinned := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, // trust comes from the pin, exactly like the phone
		VerifyConnection: func(cs tls.ConnectionState) error {
			got, err := publicKeyFingerprint(tls.Certificate{Certificate: [][]byte{cs.PeerCertificates[0].Raw}})
			if err != nil || got != fp {
				t.Fatalf("pin mismatch: %s vs %s (%v)", got, fp, err)
			}
			return nil
		},
	}}}
	if got := get(pinned, "https://"+addr+"/"); got != "tls" {
		t.Fatalf("tls = %q", got)
	}
	// And plain again after a TLS connection — the sniff is per connection.
	if got := get(&http.Client{Timeout: 5 * time.Second}, "http://"+addr+"/"); got != "plain" {
		t.Fatalf("plain http after tls = %q", got)
	}
}

// The QR upgrades a direct http base to https and carries the fingerprint;
// an operator-chosen public URL is left alone; no fingerprint, no change.
func TestPinnedPairURL(t *testing.T) {
	s := newTestGateway(t, "secret")
	base, url, fp := s.pinnedPairURL("http://100.95.206.60:18789", "ABCD", "")
	if fp != "" || base != "http://100.95.206.60:18789" || url != "http://100.95.206.60:18789/mobile?pair=ABCD" {
		t.Fatalf("no auto TLS: %s %s %q", base, url, fp)
	}
	s.tlsFingerprint = "deadbeef"
	base, url, fp = s.pinnedPairURL("http://100.95.206.60:18789", "ABCD", "")
	if base != "https://100.95.206.60:18789" || url != "https://100.95.206.60:18789/mobile?pair=ABCD&fp=deadbeef" || fp != "deadbeef" {
		t.Fatalf("direct base: %s %s %q", base, url, fp)
	}
	// Typed on the Mobile page for the phone: also direct.
	base, _, fp = s.pinnedPairURL("http://mac.tailnet.ts.net:18789", "ABCD", "http://mac.tailnet.ts.net:18789")
	if base != "https://mac.tailnet.ts.net:18789" || fp == "" {
		t.Fatalf("typed override: %s %q", base, fp)
	}
	// Operator's public_url (likely a proxy): untouched, no pin.
	s.cfg.Server.PublicURL = "https://soul.example.com"
	base, url, fp = s.pinnedPairURL("https://soul.example.com", "ABCD", "")
	if base != "https://soul.example.com" || url != "https://soul.example.com/mobile?pair=ABCD" || fp != "" {
		t.Fatalf("public_url: %s %s %q", base, url, fp)
	}
}

// Endpoint contract: with auto TLS active, /pairing/tokens hands the phone an
// https base and the fingerprint.
func TestPairingTokenCarriesFingerprint(t *testing.T) {
	srv := newTestGateway(t, "secret")
	srv.tlsFingerprint = "cafebabe"
	status, body := gatewayJSON(t, srv, http.MethodPost, "/api/v1/pairing/tokens", "secret", `{"base_url":"http://mac.tailnet.ts.net:18789"}`)
	if status != http.StatusOK {
		t.Fatalf("status %d: %v", status, body)
	}
	if body["base_url"] != "https://mac.tailnet.ts.net:18789" || body["fingerprint"] != "cafebabe" {
		t.Fatalf("body = %v", body)
	}
	if u, _ := body["pair_url"].(string); u != "https://mac.tailnet.ts.net:18789/mobile?pair="+body["code"].(string)+"&fp=cafebabe" {
		t.Fatalf("pair_url = %q", u)
	}
}
