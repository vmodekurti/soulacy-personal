package agentmemory

import (
	"os"
	"testing"
)

// episodic.jsonl was append-only with no rotation, and ReadRecent parsed the
// WHOLE file and sorted it before keeping the newest handful — on every agent
// run, because that read builds the system prompt. Both halves are bounded now.
func TestEpisodic_LogIsRotatedOnceItPassesTheCap(t *testing.T) {
	dir := t.TempDir()
	s := NewEpisodicStore(dir)

	big := make([]byte, 30*1024)
	for i := range big {
		big[i] = 'x'
	}
	// 8 MB cap ÷ 30 KB ≈ 280 writes; 400 comfortably crosses it.
	for i := 0; i < 400; i++ {
		if err := s.Write(Record{AgentID: "a", Content: string(big)}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	info, err := os.Stat(s.episodicPath("a"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxEpisodicBytes {
		t.Fatalf("episodic.jsonl is %d bytes, past the %d cap — it never rotated", info.Size(), maxEpisodicBytes)
	}
	if _, err := os.Stat(s.episodicPath("a") + ".1"); err != nil {
		t.Fatalf("no rolled generation — the rotation dropped the old records instead of rolling them: %v", err)
	}
}

// The behaviour that matters is unchanged: newest first, capped at max.
func TestEpisodic_ReadRecentStillReturnsTheNewestFirst(t *testing.T) {
	s := NewEpisodicStore(t.TempDir())
	for _, c := range []string{"first", "second", "third"} {
		if err := s.Write(Record{AgentID: "a", Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ReadRecent("a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	if got[0].Content != "third" || got[1].Content != "second" {
		t.Fatalf("records = %q, %q; want third, second", got[0].Content, got[1].Content)
	}
}
