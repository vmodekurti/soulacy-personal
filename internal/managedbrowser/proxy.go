package managedbrowser

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/soulacy/soulacy/internal/netguard"
)

// browserProxy forces every Chromium request through Soulacy's network guard.
// HTTPS remains end-to-end encrypted; CONNECT is validated and DNS-pinned
// before the tunnel is opened. This prevents a provider page, redirect, or
// injected script from probing localhost, the home LAN, or cloud metadata.
type browserProxy struct {
	listener net.Listener
	server   *http.Server
	once     sync.Once
}

func startBrowserProxy() (*browserProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &browserProxy{listener: listener}
	p.server = &http.Server{
		Handler:           http.HandlerFunc(p.serveHTTP),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *browserProxy) URL() string { return "http://" + p.listener.Addr().String() }

func (p *browserProxy) Close() error {
	var err error
	p.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = p.server.Shutdown(ctx)
		_ = p.listener.Close()
	})
	return err
}

func (p *browserProxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL == nil || (r.URL.Scheme != "http" && r.URL.Scheme != "https") {
		http.Error(w, "browser proxy requires an absolute http or https URL", http.StatusBadRequest)
		return
	}
	if err := netguard.CheckPublicContext(r.Context(), r.URL.String()); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req := r.Clone(r.Context())
	req.RequestURI = ""
	removeHopHeaders(req.Header)
	req.Header.Del("Proxy-Authorization")
	transport := &netguard.GuardedTransport{BlockPrivate: true}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	removeHopHeaders(resp.Header)
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *browserProxy) connect(w http.ResponseWriter, r *http.Request) {
	address := strings.TrimSpace(r.Host)
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "443")
	}
	upstream, err := netguard.DialPublicContext(r.Context(), "tcp", address)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "browser proxy tunneling is unavailable", http.StatusInternalServerError)
		return
	}
	client, rw, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	go proxyTunnel(client, upstream)
}

func proxyTunnel(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() { _ = a.Close(); _ = b.Close() }
	copyOne := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		once.Do(closeBoth)
	}
	go copyOne(a, b)
	copyOne(b, a)
}

func removeHopHeaders(header http.Header) {
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(key)
	}
}
