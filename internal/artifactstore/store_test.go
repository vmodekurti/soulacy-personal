package artifactstore

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
)

func TestSharedFileStoreRoundTripAndWorkspacePurge(t *testing.T) {
	raw := (&url.URL{Scheme: "file", Path: t.TempDir()}).String()
	store, err := OpenStore(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, key := range []string{
		WorkspacePrefix("ws_a") + "runs/run-1/report.txt",
		WorkspacePrefix("ws_b") + "runs/run-1/report.txt",
	} {
		if err = store.Put(context.Background(), key, strings.NewReader(key), int64(len(key)), "text/plain"); err != nil {
			t.Fatal(err)
		}
	}
	keyA := WorkspacePrefix("ws_a") + "runs/run-1/report.txt"
	object, err := store.Open(context.Background(), keyA)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(object.Body)
	_ = object.Body.Close()
	if string(data) != keyA {
		t.Fatalf("object = %q", data)
	}
	deleted, err := store.DeletePrefix(context.Background(), WorkspacePrefix("ws_a"))
	if err != nil || deleted != 1 {
		t.Fatalf("purge: deleted=%d err=%v", deleted, err)
	}
	if _, err = store.Open(context.Background(), keyA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purged object error = %v", err)
	}
	if _, err = store.Open(context.Background(), WorkspacePrefix("ws_b")+"runs/run-1/report.txt"); err != nil {
		t.Fatalf("neighbour object was purged: %v", err)
	}
}

func TestObjectKeysCannotEscapeRoot(t *testing.T) {
	store, err := OpenStore(context.Background(), (&url.URL{Scheme: "file", Path: t.TempDir()}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, key := range []string{"../escape", "/absolute", "a/../../escape", `a\b`} {
		if err = store.Put(context.Background(), key, strings.NewReader("x"), 1, "text/plain"); err == nil {
			t.Errorf("unsafe key %q was accepted", key)
		}
	}
}
