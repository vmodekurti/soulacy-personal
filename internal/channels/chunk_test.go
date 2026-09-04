package channels

import (
	"strings"
	"testing"
)

func TestSplitForLimit_ShortStaysWhole(t *testing.T) {
	if got := SplitForLimit("hello", 100); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("short text should be one chunk, got %v", got)
	}
}

func TestSplitForLimit_RespectsLimit(t *testing.T) {
	text := strings.Repeat("a", 5000)
	chunks := SplitForLimit(text, 2000)
	if len(chunks) < 3 {
		t.Fatalf("expected >=3 chunks for 5000/2000, got %d", len(chunks))
	}
	for i, ch := range chunks {
		if len([]rune(ch)) > 2000 {
			t.Fatalf("chunk %d exceeds limit: %d runes", i, len([]rune(ch)))
		}
	}
	if joined := strings.Join(chunks, ""); joined != text {
		t.Fatalf("rejoined chunks must equal original (len %d vs %d)", len(joined), len(text))
	}
}

func TestSplitForLimit_PrefersLineBreak(t *testing.T) {
	// Two ~1500-char paragraphs; with a 2000 limit it should break at the
	// blank line rather than mid-paragraph.
	p1 := strings.Repeat("x", 1500)
	p2 := strings.Repeat("y", 1500)
	chunks := SplitForLimit(p1+"\n\n"+p2, 2000)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks split on the paragraph break, got %d", len(chunks))
	}
	if strings.Contains(chunks[0], "y") {
		t.Fatalf("first chunk should be the first paragraph only")
	}
}

func TestSplitForLimit_MultibyteSafe(t *testing.T) {
	text := strings.Repeat("é", 3000) // 2-byte runes
	for _, ch := range SplitForLimit(text, 1000) {
		if !strings.HasPrefix(ch, "é") {
			t.Fatalf("multibyte rune was split: chunk starts with %q", ch[:2])
		}
	}
}
