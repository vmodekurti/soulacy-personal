package gateway

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"

	"github.com/hashicorp/mdns"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/discovery"
)

func (s *Server) startDiscovery(listener net.Listener, certificate *tls.Certificate) (*mdns.Server, error) {
	cfg := s.cfg.Server.Discovery
	if !cfg.Enabled {
		return nil, nil
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return nil, fmt.Errorf("LAN discovery requires a TCP gateway listener")
	}
	authenticated := s.authEngine != nil && s.authEngine.Effective()
	if s.authEngine == nil {
		authenticated = s.cfg.Server.APIKey != ""
	}
	prepared, err := discovery.Prepare(discovery.Options{
		Interface: cfg.Interface, Name: cfg.Name, Hostname: cfg.Hostname,
		BindHost: s.cfg.Server.Host, Port: addr.Port, TLS: certificate != nil, Authenticated: authenticated,
	})
	if err != nil {
		return nil, err
	}
	if certificate != nil {
		host := strings.TrimSuffix(strings.ToLower(cfg.Hostname), ".")
		if err := validateDiscoveryTLS(certificate, host); err != nil {
			return nil, err
		}
	}
	server, err := mdns.NewServer(prepared)
	if err != nil {
		return nil, fmt.Errorf("LAN discovery startup: %w", err)
	}
	s.log.Info("LAN discovery enabled; records are unauthenticated hints, not pairing credentials",
		zap.String("interface", cfg.Interface), zap.String("service", discovery.ServiceType), zap.Int("port", addr.Port))
	return server, nil
}

func validateDiscoveryTLS(certificate *tls.Certificate, host string) error {
	if len(certificate.Certificate) == 0 {
		return fmt.Errorf("LAN discovery TLS certificate is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return fmt.Errorf("LAN discovery TLS certificate: %w", err)
	}
	if err := leaf.VerifyHostname(host); err != nil {
		return fmt.Errorf("LAN discovery hostname must match the TLS certificate: %w", err)
	}
	// This is only a configuration consistency check. The phone still performs
	// normal certificate-chain validation; no trust exception is installed.
	return nil
}
