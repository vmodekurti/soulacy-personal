// engine_tools_http.go — HTTP/network built-ins.
//
// ARCH-2: mechanically extracted from engine.go (no behaviour change).
// SAFE: fetch_url, http_request. SYSTEM (privileged): download_file (writes
// arbitrary bytes to disk). SSRF protection applies to all three.
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// buildHTTPTools returns the http-domain OS-level built-in tools. Extracted
// from buildSystemTools (ARCH-2) — identical definitions, no behaviour change.
// maxDownloadBytes caps what download_file will write to disk in one call.
// 512 MB is far past any legitimate artefact an agent fetches and far short of
// filling a host volume.
//
// A var rather than a const purely so a test can exercise the limit without
// moving half a gigabyte. Nothing in the running system writes it.
var maxDownloadBytes int64 = 512 << 20

func (e *Engine) buildHTTPTools() []BuiltinTool {
	return []BuiltinTool{
		{
			Name:        "fetch_url",
			Gate:        "",
			Description: "Fetch the content of a URL and return it as text. Useful for reading documentation, GitHub READMEs, APIs, and web pages before acting on them. HTML is returned as-is; use this to understand setup instructions before running commands.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The URL to fetch (http or https)",
					},
					"uri": map[string]any{
						"type":        "string",
						"description": "Compatibility alias for url.",
					},
					"link": map[string]any{
						"type":        "string",
						"description": "Compatibility alias for url.",
					},
					"href": map[string]any{
						"type":        "string",
						"description": "Compatibility alias for url.",
					},
					"max_bytes": map[string]any{
						"type":        "integer",
						"description": "Maximum response bytes to return (default 256 KB, max 1 MB)",
					},
				},
				"required": []string{},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				rawURL := strings.TrimSpace(argStringFirst(args, "url", "uri", "link", "href"))
				if rawURL == "" {
					return "", fmt.Errorf("fetch_url: url is required")
				}
				if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
					return "", fmt.Errorf("fetch_url: only http/https URLs are supported")
				}
				if err := checkSSRF(rawURL, e.ssrfProtection, e.allowPrivateHosts); err != nil {
					return "", err
				}

				maxBytes := argInt(args, "max_bytes", 256*1024)
				if maxBytes <= 0 {
					maxBytes = 256 * 1024
				}
				if maxBytes > 1024*1024 {
					maxBytes = 1024 * 1024
				}

				// For GitHub repo URLs, redirect to the raw README for cleaner text.
				// e.g. https://github.com/user/repo → https://raw.githubusercontent.com/user/repo/main/README.md
				fetchURL := rawURL
				if strings.HasPrefix(rawURL, "https://github.com/") {
					parts := strings.Split(strings.TrimPrefix(rawURL, "https://github.com/"), "/")
					if len(parts) == 2 { // bare repo URL
						fetchURL = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/README.md", parts[0], parts[1])
					}
				}

				httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
				if err != nil {
					return "", fmt.Errorf("fetch_url: build request: %w", err)
				}
				httpReq.Header.Set("User-Agent", "Soulacy/1.0")
				httpReq.Header.Set("Accept", "text/plain, text/html, */*")

				client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: e.ssrfRedirectHook()}
				resp, err := client.Do(httpReq)
				if err != nil {
					return "", fmt.Errorf("fetch_url: request failed: %w", err)
				}
				defer resp.Body.Close()

				if resp.StatusCode >= 400 {
					return "", fmt.Errorf("fetch_url: HTTP %d from %s", resp.StatusCode, fetchURL)
				}

				body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)))
				if err != nil {
					return "", fmt.Errorf("fetch_url: read body: %w", err)
				}

				// When the response is JSON, return the RAW JSON body so downstream
				// python (json.loads) and templates ({{ ... }}) can parse it directly.
				// Previously a "URL:/Status:/Content-Type:" header block was prepended
				// to EVERY response, which made json.loads fail at char 0 on API
				// responses. The header context is still useful for text/HTML pages,
				// so it's kept only for non-JSON bodies.
				trimmed := strings.TrimSpace(string(body))
				ctype := strings.ToLower(resp.Header.Get("Content-Type"))
				isJSON := strings.Contains(ctype, "json") ||
					((strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed)))
				if isJSON && len(body) < maxBytes {
					// Full (untruncated) JSON body → hand it back clean.
					return trimmed, nil
				}
				result := fmt.Sprintf("URL: %s\nStatus: %d\nContent-Type: %s\n\n%s",
					fetchURL, resp.StatusCode,
					resp.Header.Get("Content-Type"),
					string(body),
				)
				if len(body) == maxBytes {
					result += "\n\n[truncated — response exceeded max_bytes limit]"
				}
				return result, nil
			},
		},
		{
			Name:        "http_request",
			Gate:        "",
			Description: "Send an HTTP request with any method (GET, POST, PUT, PATCH, DELETE), custom headers, and a request body. Use for REST API calls, webhooks, and any interaction that needs more than a plain GET.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"method": map[string]any{
						"type":        "string",
						"description": "HTTP method: GET, POST, PUT, PATCH, DELETE",
					},
					"url": map[string]any{
						"type":        "string",
						"description": "The full URL to request (http or https)",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Optional request body (plain string or JSON string)",
					},
					"content_type": map[string]any{
						"type":        "string",
						"description": "Content-Type header value (default: application/json when body is provided)",
					},
					"headers": map[string]any{
						"type":        "object",
						"description": "Optional extra headers as key-value pairs",
					},
				},
				"required": []string{"method", "url"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				method := strings.ToUpper(strings.TrimSpace(argString(args, "method")))
				rawURL := strings.TrimSpace(argString(args, "url"))
				if rawURL == "" {
					return "", fmt.Errorf("http_request: url is required")
				}
				if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
					return "", fmt.Errorf("http_request: only http/https URLs are supported")
				}
				if err := checkSSRF(rawURL, e.ssrfProtection, e.allowPrivateHosts); err != nil {
					return "", err
				}
				valid := map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true}
				if !valid[method] {
					return "", fmt.Errorf("http_request: unsupported method %q", method)
				}

				bodyStr := argString(args, "body")
				var bodyReader io.Reader
				if bodyStr != "" {
					bodyReader = strings.NewReader(bodyStr)
				}

				req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
				if err != nil {
					return "", fmt.Errorf("http_request: build request: %w", err)
				}
				req.Header.Set("User-Agent", "Soulacy/1.0")
				if bodyStr != "" {
					ct := argStringDefault(args, "content_type", "application/json")
					req.Header.Set("Content-Type", ct)
				}
				if hdrs, ok := args["headers"].(map[string]any); ok {
					for k, v := range hdrs {
						if vs, ok := v.(string); ok {
							req.Header.Set(k, vs)
						}
					}
				}

				client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: e.ssrfRedirectHook()}
				resp, err := client.Do(req)
				if err != nil {
					return "", fmt.Errorf("http_request: request failed: %w", err)
				}
				defer resp.Body.Close()

				respBody, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
				if err != nil {
					return "", fmt.Errorf("http_request: read response: %w", err)
				}
				// Return the raw JSON body directly for JSON responses (the common
				// REST-API case) so json.loads / templates parse it without stripping
				// a header preamble; keep the Status/Content-Type context for non-JSON.
				trimmed := strings.TrimSpace(string(respBody))
				ctype := strings.ToLower(resp.Header.Get("Content-Type"))
				isJSON := strings.Contains(ctype, "json") ||
					((strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && json.Valid([]byte(trimmed)))
				if isJSON {
					return trimmed, nil
				}
				return fmt.Sprintf("Status: %d %s\nContent-Type: %s\n\n%s",
					resp.StatusCode, resp.Status,
					resp.Header.Get("Content-Type"),
					string(respBody),
				), nil
			},
		},
		{
			Name:        "download_file",
			Gate:        "",
			Description: "Download a file from a URL and save it to a local path. Unlike fetch_url (which returns text), this writes the raw bytes to disk — ideal for archives (.zip, .tar.gz), binaries, images, and any non-text content.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The URL to download (http or https)",
					},
					"dest_path": map[string]any{
						"type":        "string",
						"description": "Local file path to write the download to (supports ~ and $VAR expansion). Parent directories are created as needed.",
					},
				},
				"required": []string{"url", "dest_path"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				rawURL := strings.TrimSpace(argString(args, "url"))
				if rawURL == "" {
					return "", fmt.Errorf("download_file: url is required")
				}
				if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
					return "", fmt.Errorf("download_file: only http/https URLs are supported")
				}
				if err := checkSSRF(rawURL, e.ssrfProtection, e.allowPrivateHosts); err != nil {
					return "", err
				}

				destPath := os.ExpandEnv(argString(args, "dest_path"))
				if strings.HasPrefix(destPath, "~/") {
					if home, err := os.UserHomeDir(); err == nil {
						destPath = filepath.Join(home, destPath[2:])
					}
				}
				if destPath == "" {
					return "", fmt.Errorf("download_file: dest_path is required")
				}
				if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
					return "", fmt.Errorf("download_file: create dirs: %w", err)
				}

				req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
				if err != nil {
					return "", fmt.Errorf("download_file: build request: %w", err)
				}
				req.Header.Set("User-Agent", "Soulacy/1.0")

				client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: e.ssrfRedirectHook()} // longer timeout for large files
				resp, err := client.Do(req)
				if err != nil {
					return "", fmt.Errorf("download_file: request failed: %w", err)
				}
				defer resp.Body.Close()

				if resp.StatusCode >= 400 {
					return "", fmt.Errorf("download_file: HTTP %d from %s", resp.StatusCode, rawURL)
				}

				f, err := os.Create(destPath)
				if err != nil {
					return "", fmt.Errorf("download_file: create file: %w", err)
				}
				defer f.Close()

				// Bounded, unlike before: the only limit used to be the client's
				// 5-minute timeout, which on a fast link is tens of gigabytes onto
				// the host disk. The URL comes from model output, so a prompt-injected
				// page could pick the target. fetch_url and http_request above have
				// both always capped their reads; this one did not.
				n, err := io.Copy(f, io.LimitReader(resp.Body, maxDownloadBytes+1))
				if err != nil {
					return "", fmt.Errorf("download_file: write: %w", err)
				}
				if n > maxDownloadBytes {
					_ = f.Close()
					_ = os.Remove(destPath)
					return "", fmt.Errorf("download_file: response exceeds the %d byte limit", maxDownloadBytes)
				}
				return fmt.Sprintf("Downloaded %d bytes → %s", n, destPath), nil
			},
		},
	}
}
