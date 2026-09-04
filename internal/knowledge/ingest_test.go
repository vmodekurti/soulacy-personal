// ingest_test.go — regression tests for the chunker and text-extraction
// pipeline. These are pure functions with no external dependencies, so they
// run fast and CI-friendly.
package knowledge

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

func TestChunkText_BasicWindowing(t *testing.T) {
	text := strings.Repeat("abcdefghij", 300) // 3000 chars
	chunks := ChunkText(text, 1000, 200)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	// With size 1000 and step 800 (1000-200), 3000 chars → starts at 0, 800,
	// 1600, 2400 → 4 chunks (the last starts inside the doc and runs to 3000).
	if got, want := len(chunks), 4; got != want {
		t.Errorf("chunk count: got %d, want %d", got, want)
	}
	// First chunk should be 1000 chars (trimmed of any leading/trailing ws —
	// no ws here, so exactly 1000).
	if len(chunks[0]) != 1000 {
		t.Errorf("first chunk size: got %d, want 1000", len(chunks[0]))
	}
}

func TestChunkText_EmptyInputProducesNoChunks(t *testing.T) {
	if got := ChunkText("", 1000, 200); len(got) != 0 {
		t.Errorf("empty input: got %d chunks, want 0", len(got))
	}
	if got := ChunkText("   \n\t  ", 1000, 200); len(got) != 0 {
		t.Errorf("whitespace-only input: got %d chunks, want 0", len(got))
	}
}

func TestChunkText_BadSizeDefaultsApply(t *testing.T) {
	// size <= 0 should default to 1000.
	chunks := ChunkText("hello world", 0, 0)
	if len(chunks) != 1 || chunks[0] != "hello world" {
		t.Errorf("zero size: expected single chunk 'hello world', got %v", chunks)
	}
	// overlap >= size should clamp to size/5.
	chunks = ChunkText(strings.Repeat("x", 500), 100, 100)
	if len(chunks) < 5 {
		t.Errorf("overlap clamping: expected ≥5 chunks for clamped overlap, got %d", len(chunks))
	}
}

func TestChunkText_MultiByteRunesNotSplit(t *testing.T) {
	// Each emoji is one rune but multiple bytes. The chunker should split on
	// runes, not bytes, so an emoji is never fractured.
	text := strings.Repeat("🦀", 500) // 500 runes (~2000 bytes)
	chunks := ChunkText(text, 100, 20)
	for i, c := range chunks {
		// Decoding the chunk back to runes should match its rune count cleanly
		// (no replacement characters).
		if strings.ContainsRune(c, '�') {
			t.Errorf("chunk %d contained replacement char — multi-byte split", i)
		}
	}
}

func TestExtractText_Passthrough(t *testing.T) {
	cases := []struct {
		mime, body string
	}{
		{"text/plain", "hello world"},
		{"text/markdown", "# heading\n\nbody"},
		{"", "no mime"}, // unknown mime → passthrough
		{"text/plain; charset=utf-8", "with params"},
	}
	for _, c := range cases {
		got, err := ExtractText(c.mime, []byte(c.body))
		if err != nil {
			t.Errorf("ExtractText(%q): unexpected error %v", c.mime, err)
			continue
		}
		if got != c.body {
			t.Errorf("ExtractText(%q): got %q, want %q", c.mime, got, c.body)
		}
	}
}

func TestMimeFromFilename(t *testing.T) {
	cases := []struct{ name, want string }{
		{"foo.pdf", "application/pdf"},
		{"FOO.PDF", "application/pdf"}, // case-insensitive
		{"report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"notes.md", "text/markdown"},
		{"notes.markdown", "text/markdown"},
		{"log.txt", "text/plain"},
		{"unknown.xyz", ""},
		{"no-extension", ""},
	}
	for _, c := range cases {
		if got := MimeFromFilename(c.name); got != c.want {
			t.Errorf("MimeFromFilename(%q): got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestCleanPDFText covers the fix for ledongthuc/pdf's one-word-per-line
// fragmentation. The cleaner must collapse single-word lines back into
// flowing text while preserving page-level paragraph breaks.
func TestCleanPDFText(t *testing.T) {
	input := "Claude\n\nMythos\n\nPreview" // typical fragmented output
	got := cleanPDFText(input)
	want := "Claude Mythos Preview"
	if got != want {
		t.Errorf("cleanPDFText fragmented input:\n  got:  %q\n  want: %q", got, want)
	}

	// Triple-newline page markers should produce one double-newline paragraph break.
	pageInput := "page one text\n\n\npage two text"
	got = cleanPDFText(pageInput)
	if !strings.Contains(got, "\n\n") {
		t.Errorf("cleanPDFText should preserve page boundary as \\n\\n, got %q", got)
	}
	if strings.Count(got, "\n") > 2 {
		t.Errorf("cleanPDFText should collapse all other whitespace; got %q", got)
	}
}

// extractDOCX is exercised via a synthetic minimal .docx in memory. We avoid
// shipping a binary fixture by building the zip + word/document.xml inline.
func TestExtractDOCX_RoundTrip(t *testing.T) {
	body := buildMinimalDOCX(t, []string{
		"First paragraph.",
		"Second paragraph with bold word.",
		"Third paragraph.",
	})
	text, err := ExtractText("application/vnd.openxmlformats-officedocument.wordprocessingml.document", body)
	if err != nil {
		t.Fatalf("ExtractText docx: %v", err)
	}
	for _, want := range []string{"First paragraph.", "Second paragraph", "Third paragraph."} {
		if !strings.Contains(text, want) {
			t.Errorf("expected DOCX output to contain %q, got %q", want, text)
		}
	}
}

// ─── normalizeMime ────────────────────────────────────────────────────────────

func TestNormalizeMime(t *testing.T) {
	cases := []struct{ input, want string }{
		{"text/plain", "text/plain"},
		{"TEXT/PLAIN", "text/plain"},
		{"text/plain; charset=utf-8", "text/plain"},
		{"  application/pdf  ", "application/pdf"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeMime(c.input); got != c.want {
			t.Errorf("normalizeMime(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// ─── splitSentences ──────────────────────────────────────────────────────────

func TestSplitSentences_Basic(t *testing.T) {
	text := "Hello world. This is a test! Another sentence? Yes it is."
	got := splitSentences(text)
	if len(got) < 3 {
		t.Errorf("expected >= 3 sentences, got %d: %v", len(got), got)
	}
	if got[0] != "Hello world." {
		t.Errorf("first sentence: got %q", got[0])
	}
}

func TestSplitSentences_Empty(t *testing.T) {
	if got := splitSentences(""); len(got) != 0 {
		t.Errorf("empty input: expected nil, got %v", got)
	}
}

func TestSplitSentences_ParagraphBoundary(t *testing.T) {
	text := "First paragraph sentence.\n\nSecond paragraph sentence."
	got := splitSentences(text)
	if len(got) < 2 {
		t.Errorf("expected 2 sentences from 2 paragraphs, got %d: %v", len(got), got)
	}
}

func TestSplitSentences_NoTerminalPunctuation(t *testing.T) {
	text := "This has no ending punctuation"
	got := splitSentences(text)
	if len(got) != 1 || got[0] != text {
		t.Errorf("no punctuation: got %v", got)
	}
}

func TestSplitSentences_ClosingQuoteAfterPunctuation(t *testing.T) {
	text := `He said "goodbye." She left.`
	got := splitSentences(text)
	// Should produce at least one sentence without crashing.
	if len(got) == 0 {
		t.Error("expected at least one sentence")
	}
}

func TestSplitSentences_WindowsLineEndings(t *testing.T) {
	text := "Sentence one.\r\n\r\nSentence two."
	got := splitSentences(text)
	if len(got) < 2 {
		t.Errorf("expected 2 sentences with CRLF line endings, got %d: %v", len(got), got)
	}
}

// ─── roughTokens ─────────────────────────────────────────────────────────────

func TestRoughTokens(t *testing.T) {
	// Empty string → 1 (minimum).
	if got := roughTokens(""); got != 1 {
		t.Errorf("roughTokens('') = %d, want 1", got)
	}
	// 40 chars → 10 tokens.
	s := strings.Repeat("a", 40)
	if got := roughTokens(s); got != 10 {
		t.Errorf("roughTokens(40 chars) = %d, want 10", got)
	}
	// Single rune (< 4 chars) → 1.
	if got := roughTokens("x"); got != 1 {
		t.Errorf("roughTokens('x') = %d, want 1", got)
	}
	// Unicode multibyte runes counted as runes, not bytes.
	emoji := strings.Repeat("🦀", 8) // 8 runes, ~32 bytes
	if got := roughTokens(emoji); got != 2 {
		t.Errorf("roughTokens(8 emojis) = %d, want 2", got)
	}
}

// ─── hardSplit ───────────────────────────────────────────────────────────────

func TestHardSplit_Basic(t *testing.T) {
	text := strings.Repeat("abcde", 100) // 500 chars
	parts := hardSplit(text, 100, 20)
	if len(parts) == 0 {
		t.Fatal("hardSplit produced no parts")
	}
	// Each part must be at most size chars (some may be trimmed).
	for i, p := range parts {
		if len([]rune(p)) > 100 {
			t.Errorf("part %d too long: %d runes", i, len([]rune(p)))
		}
	}
}

func TestHardSplit_Empty(t *testing.T) {
	if got := hardSplit("", 100, 20); len(got) != 0 {
		t.Errorf("hardSplit empty: got %v", got)
	}
}

func TestHardSplit_SingleChunk(t *testing.T) {
	text := "short"
	parts := hardSplit(text, 100, 10)
	if len(parts) != 1 || parts[0] != "short" {
		t.Errorf("hardSplit short text: got %v", parts)
	}
}

func TestHardSplit_ZeroStepFallback(t *testing.T) {
	// When overlap >= size, step <= 0 should fall back to step=size (no infinite loop).
	text := strings.Repeat("a", 200)
	parts := hardSplit(text, 100, 100) // step = size - overlap = 0
	if len(parts) == 0 {
		t.Fatal("hardSplit zero step: produced no parts")
	}
}

// ─── ChunkText advanced cases ─────────────────────────────────────────────────

func TestChunkText_OverlapCarryover(t *testing.T) {
	// Build text with clear sentence boundaries so overlap rollback fires.
	text := "Sentence one. Sentence two. Sentence three. Sentence four. Sentence five."
	chunks := ChunkText(text, 60, 20) // small enough that multiple sentences per chunk
	if len(chunks) == 0 {
		t.Fatal("expected chunks, got none")
	}
	// Chunks are non-empty strings.
	for i, c := range chunks {
		if c == "" {
			t.Errorf("chunk %d is empty", i)
		}
	}
}

func TestChunkText_NegativeOverlapClamped(t *testing.T) {
	// Negative overlap must be clamped to size/5.
	chunks := ChunkText("hello world foo bar baz", 100, -10)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
}

func TestChunkText_HardSplitTriggered(t *testing.T) {
	// A single long "sentence" (no terminal punctuation) that far exceeds the
	// token budget will trigger hardSplit inside ChunkText.
	// tokenBudget = size/4 = 25; 2× = 50 tokens ≈ 200 chars.
	// Build a run-on of > 200 chars with no sentence boundary.
	text := strings.Repeat("word ", 100) // 500 chars, no punctuation
	chunks := ChunkText(text, 100, 20)
	if len(chunks) < 2 {
		t.Errorf("expected hardSplit to produce multiple chunks for run-on text, got %d", len(chunks))
	}
}

// ─── extractDOCX error paths ──────────────────────────────────────────────────

func TestExtractDOCX_NotAZip(t *testing.T) {
	_, err := ExtractText(
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		[]byte("not a zip file"),
	)
	if err == nil {
		t.Error("expected error for non-zip DOCX input, got nil")
	}
}

func TestExtractDOCX_MissingDocumentXML(t *testing.T) {
	// Build a valid zip but with no word/document.xml entry.
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("other/file.txt")
	_, _ = f.Write([]byte("hello"))
	_ = w.Close()

	_, err := ExtractText(
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		buf.Bytes(),
	)
	if err == nil {
		t.Error("expected error when word/document.xml is absent, got nil")
	}
}

// ─── ExtractText unknown mime passthrough ─────────────────────────────────────

func TestExtractText_UnknownMime(t *testing.T) {
	data := []byte("custom format data")
	got, err := ExtractText("application/x-custom", data)
	if err != nil {
		t.Fatalf("ExtractText unknown mime: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("unknown mime: got %q, want %q", got, string(data))
	}
}

// buildMinimalDOCX writes a zip containing a one-file word/document.xml whose
// body has the given paragraphs. Mirrors the minimum structure Office produces.
func buildMinimalDOCX(t *testing.T, paragraphs []string) []byte {
	t.Helper()
	type run struct {
		XMLName xml.Name `xml:"r"`
		Text    string   `xml:"t"`
	}
	type para struct {
		XMLName xml.Name `xml:"p"`
		Runs    []run    `xml:"r"`
	}
	type body struct {
		XMLName xml.Name `xml:"body"`
		Paras   []para
	}
	type doc struct {
		XMLName xml.Name `xml:"document"`
		Body    body
	}

	d := doc{}
	for _, p := range paragraphs {
		d.Body.Paras = append(d.Body.Paras, para{Runs: []run{{Text: p}}})
	}
	xmlBytes, err := xml.Marshal(d)
	if err != nil {
		t.Fatalf("marshal xml: %v", err)
	}

	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := f.Write(xmlBytes); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}
