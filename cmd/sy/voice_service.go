package main

import (
	"fmt"
	"html"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const voiceServiceLabel = "io.soulacy.voice"

func buildVoiceEnableCmd() *cobra.Command {
	var recipe, voiceName, accelerator string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Install, configure, and start local voice",
		Long:  "Install the platform-appropriate adapter, configure Soulacy, and register a per-user background service.",
		RunE: func(*cobra.Command, []string) error {
			configPath := voiceConfigPath()
			if configPath == "" {
				return fmt.Errorf("no config.yaml found; run `sy setup` first")
			}
			recipe = resolvedVoiceRecipe(recipe)
			accelerator = strings.ToLower(strings.TrimSpace(accelerator))
			if accelerator != "auto" && accelerator != "cpu" && accelerator != "mps" && accelerator != "cuda" {
				return fmt.Errorf("unknown accelerator %q (use auto, cpu, mps, or cuda)", accelerator)
			}
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			install := exec.Command(executable, "voice", "install", "--recipe", recipe)
			install.Stdin, install.Stdout, install.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := install.Run(); err != nil {
				return err
			}
			if err := patchVoiceSidecar(configPath, "http://127.0.0.1:8081", voiceName, "60s", false); err != nil {
				return err
			}
			if err := installVoiceService(executable, recipe, voiceName, accelerator); err != nil {
				return err
			}
			fmt.Printf("Local voice is configured and starting in the background (%s).\n", recipe)
			if !noWait {
				if err := waitForVoiceSidecar(10 * time.Minute); err != nil {
					return err
				}
				fmt.Println("Voice sidecar is ready.")
			}
			fmt.Println("Restart Soulacy, then run `sy voice test` or refresh Chat.")
			return nil
		},
	}
	cmd.Flags().StringVar(&recipe, "recipe", voiceRecipeAuto, "Recipe: auto, mlx-kokoro, or whisper-kokoro")
	cmd.Flags().StringVar(&voiceName, "voice", "af_heart", "Default synthesized voice")
	cmd.Flags().StringVar(&accelerator, "accelerator", "auto", "Acceleration: auto, cpu, mps, or cuda")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "Return while models continue warming")
	return cmd
}

func waitForVoiceSidecar(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	lastNotice := time.Time{}
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://127.0.0.1:8081/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
		if lastNotice.IsZero() || time.Since(lastNotice) >= 10*time.Second {
			fmt.Println("Waiting for local voice models to warm…")
			lastNotice = time.Now()
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("voice sidecar did not become ready within %s; inspect %s", timeout, voiceServiceLogPath())
}

func voiceServiceLogPath() string {
	dir, err := voiceSidecarDir()
	if err != nil {
		return "voice-sidecar.log"
	}
	return filepath.Join(dir, "voice-sidecar.log")
}

func installVoiceService(executable, recipe, voiceName, accelerator string) error {
	switch runtime.GOOS {
	case "darwin":
		return installVoiceLaunchAgent(executable, recipe, voiceName, accelerator)
	case "linux":
		return installSystemdUserService(executable, recipe, voiceName, accelerator)
	default:
		return fmt.Errorf("automatic background voice is not supported on %s; run `sy voice start --recipe %s`", runtime.GOOS, recipe)
	}
}

func installVoiceLaunchAgent(executable, recipe, voiceName, accelerator string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	plistPath := filepath.Join(dir, voiceServiceLabel+".plist")
	logPath := voiceServiceLogPath()
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>voice</string><string>start</string><string>--recipe</string><string>%s</string><string>--voice</string><string>%s</string><string>--accelerator</string><string>%s</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, voiceServiceLabel, html.EscapeString(executable), html.EscapeString(recipe), html.EscapeString(voiceName), html.EscapeString(accelerator), html.EscapeString(logPath), html.EscapeString(logPath))
	if err := os.WriteFile(plistPath, []byte(plist), 0o600); err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain+"/"+voiceServiceLabel).Run()
	if output, runErr := exec.Command("launchctl", "bootstrap", domain, plistPath).CombinedOutput(); runErr != nil {
		return fmt.Errorf("start voice launch agent: %w: %s", runErr, strings.TrimSpace(string(output)))
	}
	return nil
}

func installSystemdUserService(executable, recipe, voiceName, accelerator string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unitPath := filepath.Join(dir, "soulacy-voice.service")
	quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"` }
	unit := "[Unit]\nDescription=Soulacy local voice sidecar\nAfter=network.target\n\n[Service]\n" +
		"ExecStart=" + quote(executable) + " voice start --recipe " + quote(recipe) + " --voice " + quote(voiceName) + " --accelerator " + quote(accelerator) + "\n" +
		"Restart=on-failure\nRestartSec=3\n\n[Install]\nWantedBy=default.target\n"
	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		return err
	}
	if output, runErr := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); runErr != nil {
		return fmt.Errorf("reload user services: %w: %s", runErr, strings.TrimSpace(string(output)))
	}
	if output, runErr := exec.Command("systemctl", "--user", "enable", "--now", "soulacy-voice.service").CombinedOutput(); runErr != nil {
		return fmt.Errorf("start voice user service: %w: %s", runErr, strings.TrimSpace(string(output)))
	}
	return nil
}

func stopVoiceService() {
	switch runtime.GOOS {
	case "darwin":
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain+"/"+voiceServiceLabel).Run()
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", "--now", "soulacy-voice.service").Run()
	}
}
