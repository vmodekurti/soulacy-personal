package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

func buildConnectionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "connection", Short: "Manage encrypted authenticated website connections"}
	cmd.AddCommand(buildConnectionListCmd(), buildConnectionCaptureCmd())
	return cmd
}

func buildConnectionListCmd() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List connections visible to the current user", RunE: func(*cobra.Command, []string) error {
		body, err := apiCall(http.MethodGet, "/authenticated-connections", nil)
		if err != nil {
			return err
		}
		if outputJSON {
			fmt.Println(string(body))
			return nil
		}
		var response struct {
			Connections []struct {
				ID, Name, Scope, Kind, Status, BaseURL string
				AgentIDs                               []string `json:"agent_ids"`
			} `json:"connections"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return err
		}
		for _, c := range response.Connections {
			fmt.Printf("%-34s %-12s %-10s %-9s %s\n", c.ID, c.Scope, c.Kind, c.Status, c.Name)
		}
		return nil
	}}
}

func buildConnectionCaptureCmd() *cobra.Command {
	var name, scope, domainsCSV, agentsCSV, chromePath, connectionID string
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "capture <login-url>",
		Short: "Sign in in an isolated local browser and encrypt the resulting session",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			loginURL, domains, err := connectionCaptureBoundary(args[0], domainsCSV)
			if err != nil {
				return err
			}
			if strings.TrimSpace(name) == "" {
				name = domains[0]
			}
			state, err := captureChromeStorageState(loginURL, domains, chromePath, wait)
			if err != nil {
				return err
			}
			createdNew := strings.TrimSpace(connectionID) == ""
			if createdNew {
				createBody, _ := json.Marshal(map[string]any{
					"name": name, "scope": scope, "kind": "browser_session", "base_url": loginURL,
					"allowed_domains": domains, "agent_ids": splitCSV(agentsCSV),
				})
				createdRaw, err := apiCall(http.MethodPost, "/authenticated-connections", createBody)
				if err != nil {
					return fmt.Errorf("create connection: %w", err)
				}
				var created struct {
					Connection struct {
						ID string `json:"id"`
					} `json:"connection"`
				}
				if err := json.Unmarshal(createdRaw, &created); err != nil || created.Connection.ID == "" {
					return errors.New("gateway returned an invalid connection record")
				}
				connectionID = created.Connection.ID
			}
			sessionBody, _ := json.Marshal(map[string]any{"storage_state": state})
			if _, err := apiCall(http.MethodPut, "/authenticated-connections/"+url.PathEscape(connectionID)+"/session", sessionBody); err != nil {
				if createdNew {
					_, _ = apiCall(http.MethodDelete, "/authenticated-connections/"+url.PathEscape(connectionID), nil)
				}
				return fmt.Errorf("store encrypted session: %w", err)
			}
			fmt.Printf("Connection %q is ready (%s). The temporary browser profile was removed.\n", name, connectionID)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Display name (default: login domain)")
	cmd.Flags().StringVar(&scope, "scope", "user", "Connection scope: user or workspace")
	cmd.Flags().StringVar(&domainsCSV, "domains", "", "Comma-separated approved cookie domains (default: login domain)")
	cmd.Flags().StringVar(&agentsCSV, "agents", "", "Comma-separated agent IDs allowed to use this connection")
	cmd.Flags().StringVar(&chromePath, "chrome", "", "Chrome/Chromium executable path")
	cmd.Flags().StringVar(&connectionID, "connection-id", "", "Replace the encrypted session for an existing connection")
	cmd.Flags().DurationVar(&wait, "timeout", 10*time.Minute, "Maximum time allowed to complete sign-in")
	return cmd
}

func connectionCaptureBoundary(rawURL, rawDomains string) (string, []string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", nil, errors.New("login URL must be a public https URL")
	}
	domains := splitCSV(rawDomains)
	if len(domains) == 0 {
		domains = []string{strings.ToLower(parsed.Hostname())}
	}
	for i := range domains {
		domains[i] = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domains[i])), ".")
		if domains[i] == "" || (parsed.Hostname() != domains[i] && !strings.HasSuffix(parsed.Hostname(), "."+domains[i]) && !strings.HasSuffix(domains[i], "."+parsed.Hostname())) {
			return "", nil, fmt.Errorf("approved domain %q does not share the login URL boundary", domains[i])
		}
	}
	return parsed.String(), domains, nil
}

func captureChromeStorageState(loginURL string, domains []string, explicitChrome string, timeout time.Duration) (json.RawMessage, error) {
	chrome, err := findChrome(explicitChrome)
	if err != nil {
		return nil, err
	}
	profile, err := os.MkdirTemp("", "soulacy-auth-capture-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(profile)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	process := exec.CommandContext(ctx, chrome, "--remote-debugging-port="+fmt.Sprint(port), "--remote-debugging-address=127.0.0.1", "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--disable-sync", loginURL)
	if err := process.Start(); err != nil {
		return nil, fmt.Errorf("start isolated Chrome: %w", err)
	}
	defer func() {
		if process.Process != nil {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
	}()
	debugURL, err := waitForChromePage(ctx, port)
	if err != nil {
		return nil, err
	}
	fmt.Printf("\nAn isolated Chrome window opened for %s.\nSign in normally. Soulacy never sees your password.\nPress Enter here after the site shows you as signed in... ", domains[0])
	enter := make(chan struct{})
	go func() { _, _ = bufio.NewReader(os.Stdin).ReadString('\n'); close(enter) }()
	select {
	case <-ctx.Done():
		return nil, errors.New("sign-in capture timed out")
	case <-enter:
	}
	return readChromeStorageState(debugURL, domains)
}

func findChrome(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, nil
		}
		return "", fmt.Errorf("chrome executable not found at %s", explicit)
	}
	candidates := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"}
	if runtime.GOOS == "darwin" {
		candidates = append([]string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"}, candidates...)
	}
	for _, candidate := range candidates {
		if filepath.IsAbs(candidate) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("chrome or Chromium is required for secure session capture; install it or pass --chrome")
}

func waitForChromePage(ctx context.Context, port int) (string, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", errors.New("chrome did not expose its isolated debugging session")
		case <-ticker.C:
			response, err := http.Get(endpoint)
			if err != nil {
				continue
			}
			var pages []struct {
				Type                 string `json:"type"`
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			err = json.NewDecoder(response.Body).Decode(&pages)
			response.Body.Close()
			if err == nil {
				for _, page := range pages {
					if page.Type == "page" && page.WebSocketDebuggerURL != "" {
						return page.WebSocketDebuggerURL, nil
					}
				}
			}
		}
	}
}

func readChromeStorageState(debugURL string, domains []string) (json.RawMessage, error) {
	conn, _, err := websocket.DefaultDialer.Dial(debugURL, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to isolated Chrome: %w", err)
	}
	defer conn.Close()
	type cdpResponse struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	call := func(id int, method string, params any) (json.RawMessage, error) {
		if err := conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		for {
			var response cdpResponse
			if err := conn.ReadJSON(&response); err != nil {
				return nil, err
			}
			if response.ID != id {
				continue
			}
			if response.Error != nil {
				return nil, errors.New(response.Error.Message)
			}
			return response.Result, nil
		}
	}
	cookieRaw, err := call(1, "Network.getAllCookies", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("read session cookies: %w", err)
	}
	var cookieResult struct {
		Cookies []map[string]any `json:"cookies"`
	}
	if err := json.Unmarshal(cookieRaw, &cookieResult); err != nil {
		return nil, err
	}
	cookies := make([]map[string]any, 0)
	for _, cookie := range cookieResult.Cookies {
		domain, _ := cookie["domain"].(string)
		if !captureDomainAllowed(domain, domains) {
			continue
		}
		entry := map[string]any{"name": cookie["name"], "value": cookie["value"], "domain": domain, "path": cookie["path"], "expires": cookie["expires"], "httpOnly": cookie["httpOnly"], "secure": cookie["secure"]}
		if sameSite, ok := cookie["sameSite"].(string); ok && sameSite != "" {
			entry["sameSite"] = sameSite
		}
		cookies = append(cookies, entry)
	}
	localRaw, _ := call(2, "Runtime.evaluate", map[string]any{"expression": `JSON.stringify({origin: location.origin, values: Object.keys(localStorage).map(name => ({name, value: localStorage.getItem(name)}))})`, "returnByValue": true})
	origins := []map[string]any{}
	var eval struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(localRaw, &eval) == nil && eval.Result.Value != "" {
		var local struct {
			Origin string              `json:"origin"`
			Values []map[string]string `json:"values"`
		}
		if json.Unmarshal([]byte(eval.Result.Value), &local) == nil {
			if parsed, _ := url.Parse(local.Origin); parsed != nil && captureDomainAllowed(parsed.Hostname(), domains) {
				origins = append(origins, map[string]any{"origin": local.Origin, "localStorage": local.Values})
			}
		}
	}
	if len(cookies) == 0 && len(origins) == 0 {
		return nil, errors.New("no authenticated state was found for the approved domains; confirm the login completed")
	}
	return json.Marshal(map[string]any{"cookies": cookies, "origins": origins})
}

func captureDomainAllowed(host string, domains []string) bool {
	host = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), ".")
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func splitCSV(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
