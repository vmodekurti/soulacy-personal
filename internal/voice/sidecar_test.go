package voice

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewSidecarRejectsRemoteByDefault(t *testing.T) {
	if _, err := NewSidecar("https://voice.example.com", "", "", false); err == nil {
		t.Fatal("expected remote URL to require explicit consent")
	}
	if _, err := NewSidecar("https://voice.example.com", "", "", true); err != nil {
		t.Fatalf("explicit remote URL rejected: %v", err)
	}
}

func TestSidecarContract(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"stt":true,"tts":true,"voices":["test"]}`)
	})
	mux.HandleFunc("/transcribe", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		f, _, err := r.FormFile("audio")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		if string(data) != "audio" {
			t.Errorf("audio = %q", data)
		}
		_, _ = io.WriteString(w, `{"text":"hello locally"}`)
	})
	mux.HandleFunc("/synthesize", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(data), `"voice":"test"`) {
			t.Errorf("payload = %s", data)
		}
		w.Header().Set("Content-Type", "audio/test")
		_, _ = w.Write([]byte("speech"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	sidecar, err := NewSidecar(srv.URL, "test", "5s", true)
	if err != nil {
		t.Fatal(err)
	}
	if ok, detail := sidecar.Ready(); !ok {
		t.Fatalf("not ready: %s", detail)
	}
	caps, err := sidecar.Capabilities(context.Background())
	if err != nil || !caps.STT || !caps.TTS || len(caps.Voices) != 1 {
		t.Fatalf("capabilities=%+v err=%v", caps, err)
	}
	text, err := sidecar.Transcribe(context.Background(), "audio/webm", bytes.NewBufferString("audio"))
	if err != nil || text != "hello locally" {
		t.Fatalf("transcript=%q err=%v", text, err)
	}
	audio, contentType, err := sidecar.Synthesize(context.Background(), "hello", "")
	if err != nil || string(audio) != "speech" || contentType != "audio/test" {
		t.Fatalf("audio=%q type=%q err=%v", audio, contentType, err)
	}
}

func TestCapabilities404UsesMinimalContract(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	sidecar, _ := NewSidecar(srv.URL, "", "", true)
	caps, err := sidecar.Capabilities(context.Background())
	if err != nil || !caps.STT || !caps.TTS {
		t.Fatalf("caps=%+v err=%v", caps, err)
	}
}

func TestSidecarBlocksRemoteRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://voice.example.com/health")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	sidecar, err := NewSidecar(srv.URL, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, detail := sidecar.Ready(); ok || !strings.Contains(detail, "blocked") {
		t.Fatalf("ready=%v detail=%q", ok, detail)
	}
}
