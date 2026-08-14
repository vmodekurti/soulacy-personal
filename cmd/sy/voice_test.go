package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestPatchVoiceSidecar(t *testing.T) {
	path := writeTemp(t, baseConfig)
	if err := patchVoiceSidecar(path, "http://127.0.0.1:8081/", "af_heart", "45s", false); err != nil {
		t.Fatal(err)
	}
	voice := parseConfig(t, path)["voice"].(map[string]any)
	if voice["provider"] != "sidecar" || voice["sidecar_url"] != "http://127.0.0.1:8081" || voice["voice"] != "af_heart" {
		t.Fatalf("voice = %#v", voice)
	}
	if got, ok := voice["allow_remote"].(bool); !ok || got {
		t.Fatalf("allow_remote = %#v, want boolean false", voice["allow_remote"])
	}
	if err := patchVoiceDisabled(path); err != nil {
		t.Fatal(err)
	}
	voice = parseConfig(t, path)["voice"].(map[string]any)
	if voice["provider"] != "" {
		t.Fatalf("provider = %#v, want disabled", voice["provider"])
	}
}

func TestVoiceAutoRecipeMatchesPlatform(t *testing.T) {
	want := voiceRecipeWhisperKokoro
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		want = voiceRecipeMLXKokoro
	}
	if got := resolvedVoiceRecipe(voiceRecipeAuto); got != want {
		t.Fatalf("auto recipe = %q, want %q", got, want)
	}
}

func TestBundledVoiceSidecarImplementsHTTPContract(t *testing.T) {
	source := string(bundledVoiceSidecar)
	for _, route := range []string{"/health", "/capabilities", "/transcribe", "/synthesize"} {
		if !strings.Contains(source, `"`+route+`"`) {
			t.Errorf("bundled adapter does not declare %s", route)
		}
	}
	if !strings.Contains(source, "mlx_whisper.transcribe") || !strings.Contains(source, "whisper-cli") {
		t.Fatal("bundled adapter must support both MLX Whisper and whisper.cpp")
	}
	for _, acceleration := range []string{"torch.cuda.is_available()", "torch.backends.mps.is_available()", "SOULACY_VOICE_ACCELERATOR"} {
		if !strings.Contains(source, acceleration) {
			t.Fatalf("bundled adapter does not auto-detect acceleration via %s", acceleration)
		}
	}
}

func TestVoiceStartExposesAcceleratorSelection(t *testing.T) {
	cmd := buildVoiceStartCmd()
	flag := cmd.Flags().Lookup("accelerator")
	if flag == nil || flag.DefValue != "auto" {
		t.Fatalf("accelerator flag = %#v, want auto default", flag)
	}
}

func TestVoiceCommandExposesManagedEnableFlow(t *testing.T) {
	cmd := buildVoiceCmd()
	found := false
	for _, child := range cmd.Commands() {
		if child.Name() == "enable" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("voice command does not expose enable")
	}
}
