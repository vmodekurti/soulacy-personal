package skillstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSkillStoreCreateAndListRequests(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "skill_requests.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	req := InstallRequest{
		ID:          "sk_req_123",
		WorkspaceID: "ws_test",
		SourceURL:   "https://github.com/acme/browser-skill",
		Reason:      "Automate browser workflow",
		Status:      RequestPending,
		RequestedBy: "usr_dev",
		RequestedAt: time.Now().UTC(),
	}

	if err := store.CreateInstallRequest(ctx, req); err != nil {
		t.Fatalf("CreateInstallRequest failed: %v", err)
	}

	requests, err := store.ListInstallRequests(ctx, "ws_test", "")
	if err != nil {
		t.Fatalf("ListInstallRequests failed: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].ID != "sk_req_123" {
		t.Fatalf("expected ID sk_req_123, got %s", requests[0].ID)
	}

	// Test decision
	if err := store.DecideInstallRequest(ctx, "ws_test", "sk_req_123", RequestInstalled, "usr_admin", "Approved after review", "browser-skill"); err != nil {
		t.Fatalf("DecideInstallRequest failed: %v", err)
	}

	requests, err = store.ListInstallRequests(ctx, "ws_test", "")
	if err != nil {
		t.Fatalf("ListInstallRequests failed: %v", err)
	}
	if requests[0].Status != RequestInstalled {
		t.Fatalf("expected status %s, got %s", RequestInstalled, requests[0].Status)
	}
}
