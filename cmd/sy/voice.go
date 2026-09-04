package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	voicebridge "github.com/soulacy/soulacy/internal/voice"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

//go:embed voice_sidecar.py
var bundledVoiceSidecar []byte

const (
	voiceRecipeAuto          = "auto"
	voiceRecipeMLXKokoro     = "mlx-kokoro"
	voiceRecipeWhisperKokoro = "whisper-kokoro"
	voiceInstallSchema       = "2"
)

func buildVoiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "voice",
		Short: "Configure provider-neutral speech for Chat",
		Long:  "Connect a local STT/TTS sidecar to Soulacy. Voice remains independent of the agent's LLM provider.",
	}
	cmd.AddCommand(
		&cobra.Command{Use: "providers", Short: "Show recommended sidecar combinations", Run: func(*cobra.Command, []string) {
			fmt.Println("RECIPE                  SPEECH-TO-TEXT   TEXT-TO-SPEECH   NOTES")
			if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
				fmt.Println("mlx-kokoro (recommended) MLX Whisper      Kokoro           Apple Silicon native (Metal)")
			}
			fmt.Println("whisper-kokoro           whisper.cpp      Kokoro           portable; requires whisper-cli + ggml model")
			fmt.Println("custom                    your endpoint    your endpoint    implements the Soulacy voice sidecar HTTP contract")
			fmt.Println("\nSidecar contract: GET /health, GET /capabilities, POST /transcribe, POST /synthesize")
			fmt.Printf("Detected platform: %s/%s; auto recipe: %s\n", runtime.GOOS, runtime.GOARCH, resolvedVoiceRecipe(voiceRecipeAuto))
		}},
		&cobra.Command{Use: "status", Short: "Show current voice readiness", RunE: func(*cobra.Command, []string) error {
			return apiGet("/voice/status", "")
		}},
		&cobra.Command{Use: "test", Short: "Test the sidecar and list its capabilities", RunE: func(*cobra.Command, []string) error {
			data, err := apiCall("GET", "/voice/status", nil)
			if err != nil {
				return err
			}
			var status struct {
				Available bool   `json:"available"`
				Detail    string `json:"detail"`
				Provider  string `json:"provider"`
			}
			if err := json.Unmarshal(data, &status); err != nil {
				return err
			}
			if !status.Available {
				return fmt.Errorf("voice is not ready: %s", status.Detail)
			}
			fmt.Printf("Voice ready (%s).\n", status.Provider)
			if status.Provider == "sidecar" {
				return apiGet("/voice/capabilities", "")
			}
			return nil
		}},
	)
	cmd.AddCommand(buildVoiceInstallCmd(), buildVoiceStartCmd(), buildVoiceEnableCmd())

	var sidecarURL, voiceName, timeout string
	var allowRemote bool
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Connect an HTTP voice sidecar",
		RunE: func(*cobra.Command, []string) error {
			configPath := voiceConfigPath()
			if configPath == "" {
				return fmt.Errorf("no config.yaml found; run `sy setup` first")
			}
			if _, err := voicebridge.NewSidecar(sidecarURL, voiceName, timeout, allowRemote); err != nil {
				return err
			}
			if err := patchVoiceSidecar(configPath, sidecarURL, voiceName, timeout, allowRemote); err != nil {
				return err
			}
			fmt.Printf("Voice sidecar saved to %s. Restart the gateway, then run `sy voice test`.\n", configPath)
			return nil
		},
	}
	configure.Flags().StringVar(&sidecarURL, "sidecar-url", "http://127.0.0.1:8081", "Sidecar base URL")
	configure.Flags().StringVar(&voiceName, "voice", "", "Default synthesized voice")
	configure.Flags().StringVar(&timeout, "timeout", "60s", "STT/TTS request timeout")
	configure.Flags().BoolVar(&allowRemote, "allow-remote", false, "Allow a non-loopback sidecar URL")
	cmd.AddCommand(configure)

	disable := &cobra.Command{Use: "disable", Short: "Disable voice in config.yaml", RunE: func(*cobra.Command, []string) error {
		configPath := voiceConfigPath()
		if configPath == "" {
			return fmt.Errorf("no config.yaml found")
		}
		if err := patchVoiceDisabled(configPath); err != nil {
			return err
		}
		stopVoiceService()
		fmt.Println("Voice disabled. Restart the gateway to apply the change.")
		return nil
	}}
	cmd.AddCommand(disable)
	return cmd
}

func resolvedVoiceRecipe(recipe string) string {
	if recipe == "" || recipe == voiceRecipeAuto {
		if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
			return voiceRecipeMLXKokoro
		}
		return voiceRecipeWhisperKokoro
	}
	return recipe
}

func voiceSidecarDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".soulacy", "voice-sidecar"), nil
}

func findVoicePython() (string, error) {
	// Kokoro 0.9.4's published wheel currently excludes Python 3.13 even
	// though the repository metadata on main is more permissive. Prefer 3.12
	// and keep this aligned with what pip can actually install.
	for _, name := range []string{"python3.12", "python3.11", "python3.10", "python3"} {
		python, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		check := exec.Command(python, "-c", "import sys; raise SystemExit(0 if (3, 10) <= sys.version_info[:2] < (3, 13) else 1)")
		if check.Run() == nil {
			return python, nil
		}
	}
	return "", fmt.Errorf("python 3.10–3.12 is required by Kokoro; install it first (for example `brew install python@3.12`)")
}

func voicePythonCompatible(python string) bool {
	if strings.TrimSpace(python) == "" {
		return false
	}
	check := exec.Command(python, "-c", "import sys; raise SystemExit(0 if (3, 10) <= sys.version_info[:2] < (3, 13) else 1)")
	return check.Run() == nil
}

func voiceEnvironmentReady(python, recipe string) bool {
	imports := "import fastapi, uvicorn, multipart, numpy, kokoro, misaki"
	if recipe == voiceRecipeMLXKokoro {
		imports += ", mlx_whisper"
	}
	return exec.Command(python, "-c", imports).Run() == nil
}

func buildVoiceInstallCmd() *cobra.Command {
	var recipe string
	var dryRun, force bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Soulacy's local voice adapter",
		Long:  "Install the bundled HTTP adapter and its isolated Python environment. Apple Silicon uses MLX Whisper automatically.",
		RunE: func(*cobra.Command, []string) error {
			recipe = resolvedVoiceRecipe(recipe)
			if recipe != voiceRecipeMLXKokoro && recipe != voiceRecipeWhisperKokoro {
				return fmt.Errorf("unknown recipe %q (use auto, mlx-kokoro, or whisper-kokoro)", recipe)
			}
			if recipe == voiceRecipeMLXKokoro && (runtime.GOOS != "darwin" || runtime.GOARCH != "arm64") {
				return fmt.Errorf("mlx-kokoro requires an Apple Silicon Mac; use whisper-kokoro")
			}
			dir, err := voiceSidecarDir()
			if err != nil {
				return err
			}
			packages := []string{"fastapi", "uvicorn", "python-multipart", "numpy", "kokoro==0.9.4", "misaki[en]>=0.9.4"}
			if recipe == voiceRecipeMLXKokoro {
				packages = append(packages, "mlx-whisper")
			}
			fmt.Printf("Recipe: %s\nInstall directory: %s\n", recipe, dir)
			if dryRun {
				fmt.Printf("Would create an isolated venv and install: %s\n", strings.Join(packages, " "))
				return nil
			}
			if _, err := exec.LookPath("ffmpeg"); err != nil {
				if runtime.GOOS == "darwin" {
					return fmt.Errorf("ffmpeg is required to decode browser audio; install it with `brew install ffmpeg`")
				}
				return fmt.Errorf("ffmpeg is required to decode browser audio; install it with your OS package manager")
			}
			python, err := findVoicePython()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			script := filepath.Join(dir, "sidecar.py")
			if err := os.WriteFile(script, bundledVoiceSidecar, 0o700); err != nil {
				return err
			}
			venv := filepath.Join(dir, "venv")
			venvPython := filepath.Join(venv, "bin", "python")
			markerPath := filepath.Join(dir, "installed")
			markerWant := voiceInstallSchema + ":" + recipe + "\n"
			if _, statErr := os.Stat(venvPython); statErr == nil && !voicePythonCompatible(venvPython) {
				fmt.Println("Replacing incompatible voice environment with Python 3.10–3.12.")
				if removeErr := os.RemoveAll(venv); removeErr != nil {
					return fmt.Errorf("replace incompatible voice venv: %w", removeErr)
				}
			}
			if _, err := os.Stat(venvPython); os.IsNotExist(err) {
				if output, runErr := exec.Command(python, "-m", "venv", venv).CombinedOutput(); runErr != nil {
					return fmt.Errorf("create voice venv: %w: %s", runErr, strings.TrimSpace(string(output)))
				}
			}
			marker, _ := os.ReadFile(markerPath)
			installed := string(marker) == markerWant || voiceEnvironmentReady(venvPython, recipe)
			if !force && installed {
				fmt.Println("Voice dependencies are already installed; reusing the managed environment.")
				if string(marker) != markerWant {
					if err := os.WriteFile(markerPath, []byte(markerWant), 0o600); err != nil {
						return err
					}
				}
			} else {
				pipArgs := append([]string{"-m", "pip", "install", "--upgrade"}, packages...)
				if output, runErr := exec.Command(venvPython, pipArgs...).CombinedOutput(); runErr != nil {
					return fmt.Errorf("install voice dependencies: %w: %s", runErr, strings.TrimSpace(string(output)))
				}
				if err := os.WriteFile(markerPath, []byte(markerWant), 0o600); err != nil {
					return err
				}
			}
			if recipe == voiceRecipeWhisperKokoro {
				fmt.Println("Adapter installed. Before starting, install whisper-cli and download a ggml model.")
				fmt.Println("Then set SOULACY_VOICE_WHISPER_BIN and SOULACY_VOICE_WHISPER_MODEL.")
			} else {
				fmt.Println("Apple Silicon voice adapter installed.")
			}
			fmt.Println("Start it with `sy voice start`, configure it with `sy voice configure`, then restart Soulacy.")
			return nil
		},
	}
	cmd.Flags().StringVar(&recipe, "recipe", voiceRecipeAuto, "Recipe: auto, mlx-kokoro, or whisper-kokoro")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be installed")
	cmd.Flags().BoolVar(&force, "force", false, "Reinstall dependencies even when the managed environment is current")
	return cmd
}

func buildVoiceStartCmd() *cobra.Command {
	var recipe, host, whisperBin, whisperModel, voiceName, accelerator string
	var port int
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Run the installed local voice adapter",
		RunE: func(*cobra.Command, []string) error {
			recipe = resolvedVoiceRecipe(recipe)
			accelerator = strings.ToLower(strings.TrimSpace(accelerator))
			if accelerator != "auto" && accelerator != "cpu" && accelerator != "mps" && accelerator != "cuda" {
				return fmt.Errorf("unknown accelerator %q (use auto, cpu, mps, or cuda)", accelerator)
			}
			dir, err := voiceSidecarDir()
			if err != nil {
				return err
			}
			python := filepath.Join(dir, "venv", "bin", "python")
			script := filepath.Join(dir, "sidecar.py")
			if _, err := os.Stat(python); err != nil {
				return fmt.Errorf("voice adapter is not installed; run `sy voice install --recipe %s`", recipe)
			}
			command := exec.Command(python, script, "--host", host, "--port", fmt.Sprint(port))
			command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
			backend := "whisper_cpp"
			if recipe == voiceRecipeMLXKokoro {
				backend = "mlx"
			}
			environment := append(os.Environ(),
				"SOULACY_VOICE_STT="+backend,
				"SOULACY_VOICE_VOICE="+voiceName,
				"SOULACY_VOICE_WHISPER_BIN="+whisperBin,
				"SOULACY_VOICE_WHISPER_MODEL="+whisperModel,
				"SOULACY_VOICE_ACCELERATOR="+accelerator,
				"PYTORCH_ENABLE_MPS_FALLBACK=1",
			)
			command.Env = environment
			check := exec.Command(python, script, "--check")
			check.Env = environment
			if output, checkErr := check.CombinedOutput(); checkErr != nil {
				return fmt.Errorf("voice adapter is not ready: %s", strings.TrimSpace(string(output)))
			}
			fmt.Printf("Starting %s voice sidecar at http://%s:%d (accelerator: %s)\n", recipe, host, port, accelerator)
			return command.Run()
		},
	}
	cmd.Flags().StringVar(&recipe, "recipe", voiceRecipeAuto, "Recipe: auto, mlx-kokoro, or whisper-kokoro")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "Listen address")
	cmd.Flags().IntVar(&port, "port", 8081, "Listen port")
	cmd.Flags().StringVar(&voiceName, "voice", "af_heart", "Kokoro voice")
	cmd.Flags().StringVar(&whisperBin, "whisper-bin", "whisper-cli", "whisper.cpp CLI path")
	cmd.Flags().StringVar(&whisperModel, "whisper-model", "", "whisper.cpp ggml model path")
	cmd.Flags().StringVar(&accelerator, "accelerator", "auto", "Acceleration: auto, cpu, mps, or cuda")
	return cmd
}

func voiceConfigPath() string {
	if explicit := strings.TrimSpace(os.Getenv("SOULACY_CONFIG_PATH")); explicit != "" {
		return explicit
	}
	return viper.ConfigFileUsed()
}

func patchVoiceSidecar(configPath, sidecarURL, voiceName, timeout string, allowRemote bool) error {
	doc, root, err := loadConfigDoc(configPath)
	if err != nil {
		return err
	}
	voice := ensureMapping(root, "voice")
	setScalar(voice, "provider", "sidecar", 0)
	setScalar(voice, "sidecar_url", strings.TrimRight(strings.TrimSpace(sidecarURL), "/"), yaml.DoubleQuotedStyle)
	setScalar(voice, "voice", strings.TrimSpace(voiceName), yaml.DoubleQuotedStyle)
	setScalar(voice, "timeout", strings.TrimSpace(timeout), 0)
	setBoolScalar(voice, "allow_remote", allowRemote)
	return saveConfigDoc(configPath, doc)
}

func setBoolScalar(m *yaml.Node, key string, value bool) {
	text := fmt.Sprint(value)
	if v := yamlMapValue(m, key); v != nil {
		v.Kind, v.Tag, v.Value, v.Style = yaml.ScalarNode, "!!bool", text, 0
		return
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: text},
	)
}

func patchVoiceDisabled(configPath string) error {
	doc, root, err := loadConfigDoc(configPath)
	if err != nil {
		return err
	}
	setScalar(ensureMapping(root, "voice"), "provider", "", yaml.DoubleQuotedStyle)
	return saveConfigDoc(configPath, doc)
}
