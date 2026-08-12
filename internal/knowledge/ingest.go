// ingest.go — text extraction + chunking pipeline for the knowledge store.
//
// We deliberately use a character-based chunker (no tokenizer) so the same
// pipeline works regardless of which embedding model is in play. Defaults:
//
//	chunk_size:    1000 characters (~250 tokens)
//	chunk_overlap: 200  characters
//
// Both are stored on the KB row so they can be tuned per-corpus later.
package knowledge

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

// ExtractText returns the raw text content of a file given its mime type and
// raw bytes. Supported types: text/plain, text/markdown, application/pdf,
// application/vnd.openxmlformats-officedocument.wordprocessingml.document.
//
// Unknown mime types fall through to treating the bytes as utf-8 text so
// pasted content always works.
func ExtractText(mimeType string, data []byte) (string, error) {
	switch normalizeMime(mimeType) {
	case "text/plain", "text/markdown", "":
		return string(data), nil
	case "application/pdf":
		return extractPDF(data)
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return extractDOCX(data)
	default:
		// Unknown — best-effort treat as text.
		return string(data), nil
	}
}

// MimeFromFilename returns a best-effort mime type for the supplied filename.
// Lower-cases the extension; unknown extensions return "" so the caller can
// decide whether to default to text or refuse.
func MimeFromFilename(name string) string {
	name = strings.ToLower(name)
	switch {
	case strings.HasSuffix(name, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(name, ".docx"):
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case strings.HasSuffix(name, ".md"), strings.HasSuffix(name, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(name, ".txt"):
		return "text/plain"
	}
	return ""
}

func normalizeMime(m string) string {
	m = strings.TrimSpace(strings.ToLower(m))
	if idx := strings.Index(m, ";"); idx >= 0 {
		m = strings.TrimSpace(m[:idx])
	}
	return m
}

// ChunkText splits text into semantically coherent chunks using
// sentence-boundary awareness. The algorithm:
//
//  1. Split the text into individual sentences (on ". ", "! ", "? ",
//     ".\n", double-newlines, etc.).
//  2. Greedily merge consecutive sentences into a chunk until the rough
//     token budget (`size`/4 tokens, since 1 token ≈ 4 chars) is exceeded.
//  3. The last `overlap`/4 tokens of each chunk are carried into the next
//     chunk as context overlap — but always on a sentence boundary, never
//     mid-sentence.
//  4. Sentences that are individually longer than the token budget (e.g.
//     a table row that is one giant run-on) are hard-split at the character
//     level as a fallback so no chunk exceeds 2× the budget.
//
// This avoids the mid-sentence cuts that degrade embedding quality in the
// previous character-window approach.
//
// Named with the "Text" suffix to avoid colliding with the Chunk struct
// declared in store.go.
func ChunkText(text string, size, overlap int) []string {
	if size <= 0 {
		size = 1000
	}
	if overlap < 0 || overlap >= size {
		overlap = size / 5
	}

	// Rough token budget: 1 token ≈ 4 characters (works for English prose
	// with any embedding model; no tokenizer dependency needed).
	tokenBudget := size / 4
	if tokenBudget < 50 {
		tokenBudget = 50
	}
	overlapTokens := overlap / 4
	if overlapTokens < 0 {
		overlapTokens = 0
	}

	sentences := splitSentences(text)
	if len(sentences) == 0 {
		return nil
	}

	var chunks []string
	i := 0
	for i < len(sentences) {
		// Build a chunk by merging sentences until we hit the budget.
		var buf []string
		tokens := 0
		for i < len(sentences) {
			s := sentences[i]
			st := roughTokens(s)
			if len(buf) > 0 && tokens+st > tokenBudget {
				break
			}
			// Single sentence exceeds budget: hard-split it.
			if len(buf) == 0 && st > tokenBudget*2 {
				sub := hardSplit(s, size, overlap)
				chunks = append(chunks, sub...)
				i++
				goto next
			}
			buf = append(buf, s)
			tokens += st
			i++
		}
		if len(buf) > 0 {
			chunks = append(chunks, strings.TrimSpace(strings.Join(buf, " ")))
		}

		// Roll back by overlapTokens worth of sentences for context continuity.
		if overlapTokens > 0 && i < len(sentences) {
			carried := 0
			rollback := 0
			for j := len(buf) - 1; j >= 0 && carried < overlapTokens; j-- {
				carried += roughTokens(buf[j])
				rollback++
			}
			i -= rollback
		}
	next:
	}
	return chunks
}

// splitSentences breaks text into individual sentences using a two-level
// punctuation state machine rather than a regex, to avoid adding a regexp
// dependency and to keep the logic easy to audit.
//
// State machine rules:
//
//  1. Paragraph boundaries (double newlines) are always split points —
//     they represent the strongest natural text boundary.
//  2. Within each paragraph, advance a rune cursor. When the cursor lands on
//     '.', '!', or '?', check what follows:
//     a. Skip any closing quote or paren characters (", ', )) that
//     conventionally follow the punctuation.
//     b. If the character after those is a space, newline, or end-of-string,
//     treat the punctuation as a sentence boundary and emit the sentence.
//     c. Otherwise (e.g. "3.14" or "Mr. Smith") advance without splitting.
//  3. Any remaining text in a paragraph with no terminal punctuation is
//     emitted as its own sentence fragment.
//
// The state machine deliberately does not handle abbreviations (U.S.A.) or
// decimal numbers with 100% accuracy — for chunking purposes, the occasional
// spurious split is benign; mid-sentence cuts are the costly failure mode.
func splitSentences(text string) []string {
	// Normalise line endings and paragraph breaks.
	text = strings.ReplaceAll(text, "\r\n", "\n")

	// Split on paragraph breaks first (these are strong boundaries).
	paragraphs := strings.Split(text, "\n\n")

	var sentences []string
	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		// Within each paragraph, split on sentence-ending punctuation
		// followed by a space and an uppercase letter, or end of string.
		// We use a simple state machine instead of regex to avoid a dep.
		runes := []rune(para)
		start := 0
		for k := 0; k < len(runes); k++ {
			r := runes[k]
			if r == '.' || r == '!' || r == '?' {
				// Check what follows: space/newline + uppercase, or end.
				next := k + 1
				// Skip closing quotes/parens after punctuation.
				for next < len(runes) && (runes[next] == '"' || runes[next] == '\'' || runes[next] == ')') {
					next++
				}
				if next >= len(runes) || (next < len(runes) && (runes[next] == ' ' || runes[next] == '\n')) {
					s := strings.TrimSpace(string(runes[start : k+1]))
					if s != "" {
						sentences = append(sentences, s)
					}
					start = next + 1
					k = next
				}
			}
		}
		// Remainder of paragraph (no terminal punctuation).
		if start < len(runes) {
			s := strings.TrimSpace(string(runes[start:]))
			if s != "" {
				sentences = append(sentences, s)
			}
		}
	}
	return sentences
}

// roughTokens estimates the token count of s using the chars/4 heuristic.
// The approximation holds for typical English prose with any byte-pair or
// word-piece tokenizer (GPT-series, LLaMA, nomic-embed-text, etc.) and avoids
// a tokenizer dependency entirely. It intentionally counts Unicode runes rather
// than bytes so CJK or emoji-heavy text isn't under-estimated.
func roughTokens(s string) int {
	n := len([]rune(s)) / 4
	if n < 1 {
		return 1
	}
	return n
}

// hardSplit is the character-window fallback invoked when a single sentence is
// longer than 2× the token budget (e.g. a Markdown table rendered as one run-
// on line, or a code block without newlines). It slices at fixed rune positions
// with overlap rather than trying to find a sentence boundary — there simply
// isn't one. Using rune indexing rather than byte indexing prevents splitting
// in the middle of a multi-byte Unicode character.
func hardSplit(text string, size, overlap int) []string {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}
	step := size - overlap
	if step <= 0 {
		step = size
	}
	var out []string
	for start := 0; start < len(runes); start += step {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		piece := strings.TrimSpace(string(runes[start:end]))
		if piece != "" {
			out = append(out, piece)
		}
		if end == len(runes) {
			break
		}
	}
	return out
}

// --- PDF -------------------------------------------------------------------

func extractPDF(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("pdf: open: %w", err)
	}
	var sb strings.Builder
	totalPages := r.NumPage()
	for i := 1; i <= totalPages; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		// PlainText pulls flowing text out of the page; not perfect with
		// columns but adequate for most prose PDFs.
		t, err := page.GetPlainText(nil)
		if err != nil {
			// Some pages may be images or have unsupported font encodings.
			// Skip rather than fail the whole document.
			continue
		}
		sb.WriteString(t)
		// Page boundary marker — survives whitespace collapsing as a single
		// paragraph break.
		sb.WriteString("\n\n\n")
	}
	out := cleanPDFText(sb.String())
	if out == "" {
		return "", errors.New("pdf: no extractable text (scanned image? add OCR upstream)")
	}
	return out, nil
}

// cleanPDFText fixes the one-word-per-line fragmentation that ledongthuc/pdf
// produces: it emits each text-positioning operator on its own line, so a
// flowing paragraph comes out as "Claude\n\nMythos\n\nPreview\n\n…". For RAG
// quality we collapse all whitespace runs to a single space and preserve
// page-level paragraph breaks (we wrote a triple-newline marker between
// pages in extractPDF).
func cleanPDFText(s string) string {
	// 1. Normalise our page-boundary marker so it survives the collapse below.
	const pageBreak = "\x1fPAGEBREAK\x1f"
	s = strings.ReplaceAll(s, "\n\n\n", pageBreak)

	// 2. Collapse every other whitespace run (newlines, tabs, multiple spaces)
	//    into a single space. This rejoins the fragmented one-word-per-line
	//    output into normal flowing text.
	var sb strings.Builder
	sb.Grow(len(s))
	inWS := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inWS {
				sb.WriteByte(' ')
				inWS = true
			}
			continue
		}
		sb.WriteRune(r)
		inWS = false
	}

	// 3. Restore page breaks as double newlines so chunking still has natural
	//    boundaries to fall on.
	out := strings.ReplaceAll(sb.String(), pageBreak, "\n\n")
	return strings.TrimSpace(out)
}

// --- DOCX ------------------------------------------------------------------
//
// .docx files are zip archives containing word/document.xml. We unzip in-
// memory and walk the XML extracting <w:t> text nodes, inserting newlines
// at <w:p> boundaries.

type docxBody struct {
	XMLName xml.Name `xml:"document"`
	Body    struct {
		Paragraphs []struct {
			Runs []struct {
				Text string `xml:"t"`
			} `xml:"r"`
		} `xml:"p"`
	} `xml:"body"`
}

// maxDocXMLBytes caps the DECOMPRESSED size of a .docx's document.xml.
//
// A .docx is a zip, and every guard on this path measured the COMPRESSED input:
// fiber's BodyLimit and knowledge.max_document_bytes both apply to the uploaded
// file. DEFLATE reaches roughly 1000:1 on repetitive input, so a 1 MB upload
// expanded to ~1 GB inside a single io.ReadAll — from POST /chat/attachments,
// which needs only the chat permission. The sibling plugin-archive extractor has
// capped this since it was written (see plugininstall/archive.go); this path was
// simply missed.
//
// 32 MB of XML is a document of several thousand pages. Anything past that is
// not a document anyone is trying to read.
const maxDocXMLBytes = 32 << 20

func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("docx: open zip: %w", err)
	}
	var docXML []byte
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			// The declared size is checked first so an obvious bomb costs nothing
			// to reject; the LimitReader below is what actually holds, because the
			// header is attacker-controlled and can lie.
			if f.UncompressedSize64 > maxDocXMLBytes {
				return "", fmt.Errorf("docx: document.xml declares %d bytes, over the %d byte limit", f.UncompressedSize64, maxDocXMLBytes)
			}
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("docx: open document.xml: %w", err)
			}
			docXML, err = io.ReadAll(io.LimitReader(rc, maxDocXMLBytes+1))
			rc.Close()
			if err != nil {
				return "", err
			}
			if len(docXML) > maxDocXMLBytes {
				return "", fmt.Errorf("docx: document.xml expands past the %d byte limit", maxDocXMLBytes)
			}
			break
		}
	}
	if docXML == nil {
		return "", errors.New("docx: word/document.xml not found")
	}

	// Stream-decode so we don't depend on namespace prefixes (encoding/xml
	// ignores them when struct tags drop the namespace).
	var body docxBody
	dec := xml.NewDecoder(bytes.NewReader(docXML))
	dec.Strict = false
	if err := dec.Decode(&body); err != nil {
		return "", fmt.Errorf("docx: decode: %w", err)
	}

	var sb strings.Builder
	for _, p := range body.Body.Paragraphs {
		for _, r := range p.Runs {
			sb.WriteString(r.Text)
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String()), nil
}
