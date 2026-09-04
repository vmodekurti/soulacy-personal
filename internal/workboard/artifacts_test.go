package workboard

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func artifactStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "wb.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func startedRun(t *testing.T, s *Store) (Task, Run) {
	t.Helper()
	task, err := s.Create(context.Background(), Task{Title: "build report", AgentID: "agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.StartRun(context.Background(), task.ID, "agent-1", "wb-sess-1", "")
	if err != nil {
		t.Fatal(err)
	}
	return task, run
}

func TestAddArtifacts_AndListByTask(t *testing.T) {
	s := artifactStore(t)
	task, run := startedRun(t, s)

	arts := []Artifact{
		{Path: "/tmp/report.pdf", SizeBytes: 1234, Tool: "write_file"},
		{Path: "/tmp/data.csv", SizeBytes: 99, Tool: "write_file"},
	}
	if err := s.AddArtifacts(context.Background(), task.ID, run.ID, arts); err != nil {
		t.Fatalf("AddArtifacts: %v", err)
	}

	got, err := s.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("artifacts = %d, want 2: %+v", len(got), got)
	}
	for _, a := range got {
		if a.ID == 0 || a.TaskID != task.ID || a.RunID != run.ID {
			t.Fatalf("artifact ids not populated: %+v", a)
		}
		if a.CreatedAt.IsZero() {
			t.Fatalf("artifact CreatedAt zero: %+v", a)
		}
	}
}

func TestAddArtifacts_DuplicatePathSameRun_Upserts(t *testing.T) {
	s := artifactStore(t)
	task, run := startedRun(t, s)

	if err := s.AddArtifacts(context.Background(), task.ID, run.ID,
		[]Artifact{{Path: "/tmp/x.txt", SizeBytes: 10, Tool: "write_file"}}); err != nil {
		t.Fatal(err)
	}
	// Same path written again later in the run (append) — keep ONE row with
	// the latest size.
	if err := s.AddArtifacts(context.Background(), task.ID, run.ID,
		[]Artifact{{Path: "/tmp/x.txt", SizeBytes: 25, Tool: "write_file"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("artifacts = %d, want 1 (deduped): %+v", len(got), got)
	}
	if got[0].SizeBytes != 25 {
		t.Fatalf("SizeBytes = %d, want latest 25", got[0].SizeBytes)
	}
}

func TestGetArtifact(t *testing.T) {
	s := artifactStore(t)
	task, run := startedRun(t, s)
	if err := s.AddArtifacts(context.Background(), task.ID, run.ID,
		[]Artifact{{Path: "/tmp/a.txt", SizeBytes: 5, Tool: "write_file"}}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListArtifacts(context.Background(), task.ID)
	got, err := s.GetArtifact(context.Background(), list[0].ID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got.Path != "/tmp/a.txt" {
		t.Fatalf("artifact = %+v", got)
	}
	if _, err := s.GetArtifact(context.Background(), 99999); err != ErrNotFound {
		t.Fatalf("missing artifact err = %v, want ErrNotFound", err)
	}
}

func TestListRunArtifacts(t *testing.T) {
	s := artifactStore(t)
	task, run1 := startedRun(t, s)
	if _, err := s.FinishRun(context.Background(), run1.ID, RunStatusDone, "ok", ""); err != nil {
		t.Fatal(err)
	}
	run2, err := s.StartRun(context.Background(), task.ID, "agent-1", "wb-sess-2", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.AddArtifacts(context.Background(), task.ID, run1.ID, []Artifact{{Path: "/tmp/r1.txt", Tool: "write_file"}})
	_ = s.AddArtifacts(context.Background(), task.ID, run2.ID, []Artifact{{Path: "/tmp/r2.txt", Tool: "write_file"}})

	got, err := s.ListRunArtifacts(context.Background(), run2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != "/tmp/r2.txt" {
		t.Fatalf("run2 artifacts = %+v", got)
	}
}

func TestDeleteTask_CascadesArtifacts(t *testing.T) {
	s := artifactStore(t)
	task, run := startedRun(t, s)
	_ = s.AddArtifacts(context.Background(), task.ID, run.ID, []Artifact{{Path: "/tmp/z.txt", Tool: "write_file"}})
	if _, err := s.FinishRun(context.Background(), run.ID, RunStatusDone, "ok", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("artifacts survived task delete: %+v", got)
	}
}

func TestAddArtifacts_EmptyListNoop(t *testing.T) {
	s := artifactStore(t)
	task, run := startedRun(t, s)
	if err := s.AddArtifacts(context.Background(), task.ID, run.ID, nil); err != nil {
		t.Fatalf("empty AddArtifacts should be a no-op, got %v", err)
	}
}

func TestArtifactTimesSecondPrecision(t *testing.T) {
	// House style: times truncated to seconds so returned structs match DB
	// round-trips (see store.go).
	s := artifactStore(t)
	task, run := startedRun(t, s)
	_ = s.AddArtifacts(context.Background(), task.ID, run.ID,
		[]Artifact{{Path: "/tmp/t.txt", Tool: "write_file"}})
	list, _ := s.ListArtifacts(context.Background(), task.ID)
	if !list[0].CreatedAt.Equal(list[0].CreatedAt.Truncate(time.Second)) {
		t.Fatalf("CreatedAt not second-truncated: %v", list[0].CreatedAt)
	}
}
