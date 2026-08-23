package agentmemory

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
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

func TestEpisodic_ReadRecentReadsBackwardFromEOF(t *testing.T) {
	dir := t.TempDir()
	s := NewEpisodicStore(dir)
	path := s.episodicPath("a")
	if err := os.MkdirAll(dir+"/a", 0o700); err != nil {
		t.Fatal(err)
	}

	old, err := json.Marshal(Record{AgentID: "a", Content: "old", Timestamp: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	var contents strings.Builder
	oldLine := string(old) + "\n"
	for contents.Len() < 4<<20 {
		contents.WriteString(oldLine)
	}
	for i, content := range []string{"recent-one", "recent-two", "recent-three"} {
		line, marshalErr := json.Marshal(Record{
			AgentID: "a", Content: content, Timestamp: time.Unix(int64(10+i), 0),
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		contents.Write(line)
		contents.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(contents.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	records, bytesRead, err := readRecentTail(f, info.Size(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Content != "recent-three" || records[1].Content != "recent-two" {
		t.Fatalf("records = %#v; want the final two records newest-first", records)
	}
	if bytesRead > tailReadBlockBytes {
		t.Fatalf("read %d bytes for a two-record tail of a %d-byte file; want at most one %d-byte block", bytesRead, info.Size(), tailReadBlockBytes)
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
