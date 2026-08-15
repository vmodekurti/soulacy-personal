package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type cliSession struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}
type oidcStartResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	State            string `json:"state"`
}

func buildLoginCmd() *cobra.Command {
	var noBrowser bool
	var device bool
	cmd := &cobra.Command{Use: "login", Short: "Sign in to a multi-user Soulacy gateway", RunE: func(cmd *cobra.Command, _ []string) error {
		if device {
			return runDeviceLogin(cmd.Context(), noBrowser)
		}
		return runBrowserLogin(cmd.Context(), noBrowser)
	}}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the authorization URL instead of opening it")
	cmd.Flags().BoolVar(&device, "device", false, "Use device authorization for a headless terminal")
	return cmd
}

func runDeviceLogin(ctx context.Context, noBrowser bool) error {
	resp, err := http.Post(strings.TrimRight(gatewayURL, "/")+"/api/v1/auth/oidc/device/start", "application/json", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("start device login: %w", err)
	}
	defer resp.Body.Close()
	var start struct {
		DeviceHandle            string `json:"device_handle"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&start) != nil || start.DeviceHandle == "" {
		return errors.New("device authorization is not supported by this provider")
	}
	visit := start.VerificationURIComplete
	if visit == "" {
		visit = start.VerificationURI
	}
	fmt.Printf("Open %s and enter code %s\n", start.VerificationURI, start.UserCode)
	if !noBrowser {
		_ = openBrowser(visit)
	}
	if start.Interval < 1 {
		start.Interval = 5
	}
	if start.ExpiresIn < 1 {
		start.ExpiresIn = 600
	}
	deadline := time.NewTimer(time.Duration(start.ExpiresIn) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Duration(start.Interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("device login timed out")
		case <-ticker.C:
			body, _ := json.Marshal(map[string]string{"device_handle": start.DeviceHandle})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(gatewayURL, "/")+"/api/v1/auth/oidc/device/poll", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			poll, pollErr := http.DefaultClient.Do(req)
			if pollErr != nil {
				continue
			}
			if poll.StatusCode == http.StatusAccepted || poll.StatusCode == http.StatusTooManyRequests {
				_ = poll.Body.Close()
				continue
			}
			var session cliSession
			decodeErr := json.NewDecoder(poll.Body).Decode(&session)
			_ = poll.Body.Close()
			if poll.StatusCode != http.StatusOK || decodeErr != nil || session.AccessToken == "" {
				return errors.New("device authentication failed")
			}
			if err := saveCLISession(gatewayURL, session); err != nil {
				return fmt.Errorf("save login session: %w", err)
			}
			apiKey = session.AccessToken
			fmt.Println("Signed in. Credentials were saved in your operating system credential store.")
			return nil
		}
	}
}

func runBrowserLogin(ctx context.Context, noBrowser bool) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("open login callback: %w", err)
	}
	defer listener.Close()
	redirectURI := "http://" + listener.Addr().String() + "/callback"
	body, _ := json.Marshal(map[string]string{"client": "cli", "redirect_uri": redirectURI})
	resp, err := http.Post(strings.TrimRight(gatewayURL, "/")+"/api/v1/auth/oidc/start", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("start login: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("interactive login is not available on this gateway")
	}
	var start oidcStartResponse
	if json.NewDecoder(resp.Body).Decode(&start) != nil || start.AuthorizationURL == "" {
		return errors.New("gateway returned an invalid login response")
	}

	result := make(chan cliSession, 1)
	failure := make(chan error, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" || r.URL.Query().Get("state") != start.State {
			http.Error(w, "Authentication failed", http.StatusUnauthorized)
			return
		}
		completeBody, _ := json.Marshal(map[string]string{"state": r.URL.Query().Get("state"), "code": r.URL.Query().Get("code"), "redirect_uri": redirectURI})
		completeReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(gatewayURL, "/")+"/api/v1/auth/oidc/complete", bytes.NewReader(completeBody))
		completeReq.Header.Set("Content-Type", "application/json")
		completeResp, completeErr := http.DefaultClient.Do(completeReq)
		if completeErr != nil {
			failure <- completeErr
			http.Error(w, "Authentication failed", http.StatusBadGateway)
			return
		}
		defer completeResp.Body.Close()
		var session cliSession
		if completeResp.StatusCode != http.StatusOK || json.NewDecoder(completeResp.Body).Decode(&session) != nil || session.AccessToken == "" || session.RefreshToken == "" {
			failure <- errors.New("authentication failed")
			http.Error(w, "Authentication failed", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "Soulacy CLI is signed in. You can close this window.")
		result <- session
	})
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			failure <- serveErr
		}
	}()
	if noBrowser {
		fmt.Println(start.AuthorizationURL)
	} else if err := openBrowser(start.AuthorizationURL); err != nil {
		fmt.Printf("Open this URL to sign in:\n%s\n", start.AuthorizationURL)
	}
	fmt.Println("Waiting for browser sign-in…")
	select {
	case session := <-result:
		_ = server.Shutdown(context.Background())
		if err := saveCLISession(gatewayURL, session); err != nil {
			return fmt.Errorf("save login session: %w", err)
		}
		apiKey = session.AccessToken
		fmt.Println("Signed in. Credentials were saved in your operating system credential store.")
		return nil
	case err := <-failure:
		_ = server.Shutdown(context.Background())
		return err
	case <-time.After(10 * time.Minute):
		_ = server.Shutdown(context.Background())
		return errors.New("login timed out")
	case <-ctx.Done():
		_ = server.Shutdown(context.Background())
		return ctx.Err()
	}
}

func buildLogoutCmd() *cobra.Command {
	return &cobra.Command{Use: "logout", Short: "Revoke the current session", RunE: func(cmd *cobra.Command, _ []string) error {
		session, _ := loadCLISession(gatewayURL)
		body, _ := json.Marshal(map[string]string{"refresh_token": session.RefreshToken})
		req, _ := http.NewRequestWithContext(cmd.Context(), http.MethodPost, strings.TrimRight(gatewayURL, "/")+"/api/v1/auth/logout", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if session.AccessToken != "" {
			req.Header.Set("Authorization", "Bearer "+session.AccessToken)
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
		if err := deleteCLISession(gatewayURL); err != nil {
			return err
		}
		apiKey = ""
		fmt.Println("Signed out.")
		return nil
	}}
}

func openBrowser(rawURL string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{rawURL}
	case "linux":
		name, args = "xdg-open", []string{rawURL}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", rawURL}
	default:
		return errors.New("unsupported browser launcher")
	}
	return exec.Command(name, args...).Start()
}

func credentialService(gateway string) string {
	sum := sha256.Sum256([]byte(strings.TrimRight(gateway, "/")))
	return "soulacy-cli-" + hex.EncodeToString(sum[:8])
}
func credentialAccount() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "soulacy"
}

func saveCLISession(gateway string, session cliSession) error {
	raw, _ := json.Marshal(session)
	return credentialWrite(credentialService(gateway), credentialAccount(), string(raw))
}
func loadCLISession(gateway string) (cliSession, error) {
	raw, err := credentialRead(credentialService(gateway), credentialAccount())
	if err != nil {
		return cliSession{}, err
	}
	var s cliSession
	err = json.Unmarshal([]byte(raw), &s)
	return s, err
}
func deleteCLISession(gateway string) error {
	return credentialDelete(credentialService(gateway), credentialAccount())
}

func credentialWrite(service, account, secret string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("security", "add-generic-password", "-U", "-s", service, "-a", account, "-w", secret).Run()
	case "linux":
		cmd := exec.Command("secret-tool", "store", "--label=Soulacy CLI", "service", service, "account", account)
		cmd.Stdin = strings.NewReader(secret)
		return cmd.Run()
	default:
		return errors.New("no supported OS credential store; use --api-key for this session")
	}
}
func credentialRead(service, account string) (string, error) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w")
	case "linux":
		cmd = exec.Command("secret-tool", "lookup", "service", service, "account", account)
	default:
		return "", errors.New("unsupported credential store")
	}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
func credentialDelete(service, account string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("security", "delete-generic-password", "-s", service, "-a", account)
	case "linux":
		cmd = exec.Command("secret-tool", "clear", "service", service, "account", account)
	default:
		return nil
	}
	err := cmd.Run()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil
		}
	}
	return err
}

func refreshCLISession() bool {
	session, err := loadCLISession(gatewayURL)
	if err != nil || session.RefreshToken == "" {
		return false
	}
	body, _ := json.Marshal(map[string]string{"refresh_token": session.RefreshToken})
	resp, err := http.Post(strings.TrimRight(gatewayURL, "/")+"/api/v1/auth/refresh", "application/json", bytes.NewReader(body))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = deleteCLISession(gatewayURL)
		return false
	}
	if json.NewDecoder(resp.Body).Decode(&session) != nil || session.AccessToken == "" || session.RefreshToken == "" {
		return false
	}
	if saveCLISession(gatewayURL, session) != nil {
		return false
	}
	apiKey = session.AccessToken
	return true
}
