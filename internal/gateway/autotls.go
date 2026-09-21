package gateway

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Automatic TLS: real encryption to every phone with zero user steps.
//
// A home gateway has no public name, so it can't get a certificate a phone
// would trust on its own — and asking people to "trust this certificate" is
// how they learn to tap through warnings. Instead the gateway mints one
// certificate for itself, keeps it in the workspace, and hands the phone the
// fingerprint of its public key inside the pairing QR. The QR is read off the
// operator's own screen, so it is as trustworthy as the pairing code it
// carries; the phone pins that fingerprint and from then on every request —
// REST, WebSocket, token refresh — is TLS to exactly this key. No CA, no
// Tailscale admin toggle, no http/https choice for anyone.
//
// The same port keeps answering plain HTTP too (muxListener sniffs the first
// byte), so the browser on localhost and every existing install carry on
// unchanged.

const (
	autoTLSDirName  = "tls"
	autoTLSCertFile = "gateway.crt"
	autoTLSKeyFile  = "gateway.key"
	autoTLSValidity = 10 * 365 * 24 * time.Hour
)

// loadOrCreateAutoCert returns the gateway's own certificate from dir,
// generating an ECDSA P-256 self-signed one on first use. hostnames and ips
// go into the SANs for browsers; the phone pins the key and ignores them.
func loadOrCreateAutoCert(dir string, hostnames []string, ips []net.IP) (tls.Certificate, bool, error) {
	certPath, keyPath := filepath.Join(dir, autoTLSCertFile), filepath.Join(dir, autoTLSKeyFile)
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		// Something is there but unusable. Refuse to overwrite it silently —
		// a phone may be pinned to it — and let the operator decide.
		return tls.Certificate{}, false, fmt.Errorf("existing auto TLS material in %s is unusable (%v); delete it to regenerate", dir, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, false, err
	}
	dns := append([]string{"localhost"}, hostnames...)
	addrs := append([]net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}, ips...)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Soulacy gateway", Organization: []string{"Soulacy"}},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(autoTLSValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dns,
		IPAddresses:           addrs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, false, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, false, err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, false, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	return cert, true, err
}

// publicKeyFingerprint is the lowercase hex SHA-256 of the leaf's public key
// in the exact byte form the iOS app hashes (SecKeyCopyExternalRepresentation,
// see PinningDelegate.swift): the uncompressed X9.63 point for EC keys and
// PKCS#1 DER for RSA. Hashing the SubjectPublicKeyInfo instead would never
// match, so this is deliberately not the usual SPKI pin.
func publicKeyFingerprint(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", errors.New("certificate has no leaf")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return "", err
	}
	var raw []byte
	switch pub := leaf.PublicKey.(type) {
	case *ecdsa.PublicKey:
		ecdhKey, err := pub.ECDH()
		if err != nil {
			return "", err
		}
		raw = ecdhKey.Bytes()
	case *rsa.PublicKey:
		raw = x509.MarshalPKCS1PublicKey(pub)
	default:
		return "", fmt.Errorf("unsupported public key type %T for pinning", leaf.PublicKey)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// autoTLSConfig is the server-side TLS config for the auto certificate. No
// h2: fasthttp speaks HTTP/1.1, and offering h2 would make URLSession pick it.
func autoTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}
}

// muxPeekTimeout bounds how long a fresh connection may stay silent before we
// give up deciding whether it is TLS. Browsers and the app send immediately.
const muxPeekTimeout = 10 * time.Second

// muxListener serves TLS and plain HTTP on one port. A TLS ClientHello always
// starts with the handshake record byte 0x16; anything else is treated as
// plain HTTP. The peeked byte is replayed to whichever server takes the
// connection.
type muxListener struct {
	net.Listener
	tlsCfg *tls.Config
}

func newMuxListener(inner net.Listener, tlsCfg *tls.Config) net.Listener {
	return &muxListener{Listener: inner, tlsCfg: tlsCfg}
}

func (m *muxListener) Accept() (net.Conn, error) {
	for {
		conn, err := m.Listener.Accept()
		if err != nil {
			return nil, err
		}
		_ = conn.SetReadDeadline(time.Now().Add(muxPeekTimeout))
		br := bufio.NewReader(conn)
		first, err := br.Peek(1)
		if err != nil {
			// Silent or broken client: drop it and keep serving. Returning
			// the error would stop the whole accept loop.
			_ = conn.Close()
			continue
		}
		_ = conn.SetReadDeadline(time.Time{})
		pc := &peekedConn{Conn: conn, r: br}
		if first[0] == 0x16 {
			return tls.Server(pc, m.tlsCfg), nil
		}
		return pc, nil
	}
}

// peekedConn reads through the bufio.Reader that already holds the sniffed
// bytes, so nothing is lost.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// autoTLSHostnames are the DNS SANs worth putting on the auto certificate:
// the machine's own name in a couple of common spellings. Browsers use them;
// the phone pins the key and does not.
func autoTLSHostnames() []string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return nil
	}
	return []string{h, h + ".local"}
}

// autoTLSDir is where the gateway keeps its own certificate: next to
// config.yaml, i.e. inside the workspace, so it survives upgrades and moves
// with the data.
func autoTLSDir(cfgPath string) string {
	if cfgPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cfgPath), autoTLSDirName)
}

// pairTLSProbe reports whether https://<base>/ping answers 200 with a
// certificate whose key fingerprint is fp — i.e. whether the phone, arriving
// at that address with that pin, would actually reach THIS gateway's TLS.
// It is false when a proxy (tailscale serve --http, nginx) owns the address.
// Injectable so tests never open sockets.
var pairTLSProbe = httpsPinProbe

func httpsPinProbe(ctx context.Context, base, fp string) bool {
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: pairTLSConfig(fp, false),
		DialContext:     pairDialContext,
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upgradeToHTTPS(base)+"/ping", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// pairTLSConfig trusts a server whose key fingerprint is fp — the gateway's
// own auto certificate — and, when systemToo is set, any certificate the OS
// trusts (a proxy's Let's Encrypt cert). The reachability probe wants both;
// the pin probe wants only the fingerprint, because its whole question is
// "is this OUR certificate?". Hostname checks still apply to the system path.
func pairTLSConfig(fp string, systemToo bool) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // verification happens in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no certificate")
			}
			if fp != "" {
				got, err := publicKeyFingerprint(tls.Certificate{Certificate: [][]byte{cs.PeerCertificates[0].Raw}})
				if err == nil && got == fp {
					return nil
				}
			}
			if !systemToo {
				return errors.New("certificate is not this gateway's")
			}
			opts := x509.VerifyOptions{DNSName: cs.ServerName, Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := cs.PeerCertificates[0].Verify(opts)
			return err
		},
	}
}

// pairDialContext dials normally, with one exception: a host that does not
// resolve here but is this machine's own tailnet (MagicDNS) name is dialed
// at our own Tailscale address instead. macOS often cannot resolve its own
// MagicDNS name even though every phone on the tailnet can; without this the
// operator's typed name could never be probed. TLS still sees the real name.
func pairDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, addr)
	if err == nil {
		return conn, nil
	}
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		return nil, err
	}
	host, port, splitErr := net.SplitHostPort(addr)
	if splitErr != nil || !isOwnTailnetName(host) {
		return nil, err
	}
	for _, ip := range localAddresses() {
		if isTailscaleIP(ip) {
			return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
	}
	return nil, err
}

// isOwnTailnetName reports whether host looks like THIS machine's MagicDNS
// name: <hostname-slug>.<tailnet>.ts.net, where the slug is the lowercase
// hostname with anything but letters and digits turned into '-'.
func isOwnTailnetName(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.HasSuffix(host, ".ts.net") {
		return false
	}
	first, _, ok := strings.Cut(host, ".")
	if !ok {
		return false
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return false
	}
	return first == tailnetSlug(h)
}

func tailnetSlug(hostname string) string {
	hostname = strings.ToLower(strings.TrimSuffix(hostname, ".local"))
	var b strings.Builder
	for _, r := range hostname {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// upgradeToHTTPS rewrites an http:// base to https:// (host and port kept).
func upgradeToHTTPS(base string) string {
	if len(base) > 7 && base[:7] == "http://" {
		return "https://" + base[7:]
	}
	return base
}
