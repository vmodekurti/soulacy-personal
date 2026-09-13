package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/config"
)

func TestDiscoveryDisabledByDefault(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	advertiser, err := s.startDiscovery(nil, nil)
	if err != nil || advertiser != nil {
		t.Fatalf("default discovery must not inspect listener or open sockets: %v", err)
	}
}

func TestDiscoveryUnauthenticatedOverrideStillFailsClosed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	s := &Server{cfg: &config.Config{Server: config.ServerConfig{AllowUnauthenticated: true, Discovery: config.DiscoveryConfig{Enabled: true, Interface: "test"}}}}
	if _, err := s.startDiscovery(listener, nil); err == nil || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("unauthenticated discovery allowed: %v", err)
	}
}

func TestDiscoveryTLSMustMatchHostname(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"test-gateway.local"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &tls.Certificate{Certificate: [][]byte{der}}
	if err := validateDiscoveryTLS(certificate, "test-gateway.local"); err != nil {
		t.Fatal(err)
	}
	if err := validateDiscoveryTLS(certificate, "spoofed.local"); err == nil {
		t.Fatal("mismatched certificate accepted")
	}
	if err := validateDiscoveryTLS(&tls.Certificate{}, "test-gateway.local"); err == nil {
		t.Fatal("empty certificate accepted")
	}
}

func TestListenCancelledContextDoesNotBind(t *testing.T) {
	s := &Server{cfg: &config.Config{Server: config.ServerConfig{Host: "127.0.0.1"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Listen(ctx); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}

func TestListenCancellationDuringStartup(t *testing.T) {
	s := newTestGateway(t, "test-only")
	t.Cleanup(func() { _ = s.Close() })
	s.cfg.Server.Host = "127.0.0.1"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.app.Hooks().OnListen(func(fiber.ListenData) error { cancel(); return nil })
	done := make(chan error, 1)
	go func() { done <- s.Listen(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("normal cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gateway kept serving after cancellation during startup")
	}
}

func TestListenHTTPAndTLSKeepAuthenticationAndShutdown(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		t.Run(fmt.Sprint("tls=", useTLS), func(t *testing.T) {
			s := newTestGateway(t, "test-only")
			t.Cleanup(func() { _ = s.Close() })
			s.cfg.Server.Host = "127.0.0.1"
			transport := &http.Transport{Proxy: nil}
			defer transport.CloseIdleConnections()
			scheme := "http"
			if useTLS {
				scheme = "https"
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				template := &x509.Certificate{SerialNumber: big.NewInt(2), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign}
				der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
				if err != nil {
					t.Fatal(err)
				}
				keyDER, err := x509.MarshalECPrivateKey(key)
				if err != nil {
					t.Fatal(err)
				}
				certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
				dir := t.TempDir()
				s.cfg.Server.TLSCert, s.cfg.Server.TLSKey = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
				if err := os.WriteFile(s.cfg.Server.TLSCert, certPEM, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(s.cfg.Server.TLSKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
					t.Fatal(err)
				}
				pool := x509.NewCertPool()
				pool.AppendCertsFromPEM(certPEM)
				transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready, done := make(chan string, 1), make(chan error, 1)
			s.app.Hooks().OnListen(func(data fiber.ListenData) error { ready <- data.Port; return nil })
			go func() { done <- s.Listen(ctx) }()
			var port string
			select {
			case port = <-ready:
			case err := <-done:
				t.Fatalf("startup failed: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("startup timed out")
			}
			client := &http.Client{Transport: transport, Timeout: time.Second}
			for _, token := range []string{"", "test-only"} {
				req, _ := http.NewRequest(http.MethodGet, scheme+"://127.0.0.1:"+port+"/api/v1/agents", nil)
				req.Header.Set("Authorization", "Bearer "+token)
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				want := http.StatusOK
				if token == "" {
					want = http.StatusUnauthorized
				}
				if response.StatusCode != want {
					t.Fatalf("auth changed: got %d want %d", response.StatusCode, want)
				}
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("shutdown: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown timed out")
			}
		})
	}
}
