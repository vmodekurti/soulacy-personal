// Package httptestutil supplies protocol-valid requests for Fiber's in-process
// test transport. Real HTTP/1.1 clients always include Host; net/http permits
// relative URLs without it, which newer fasthttp versions correctly reject.
package httptestutil

import "net/http"

// WithHost leaves explicit host selection untouched and never changes the
// request target or its escaping. It does not relax the production parser.
func WithHost(req *http.Request) *http.Request {
	if req == nil || req.Host != "" {
		return req
	}
	clone := req.Clone(req.Context())
	clone.Host = req.URL.Host
	if clone.Host == "" {
		clone.Host = "localhost"
	}
	return clone
}
