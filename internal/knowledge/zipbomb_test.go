package knowledge

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// Every size guard on the .docx path measured the COMPRESSED upload — fiber's
// BodyLimit and knowledge.max_document_bytes both do. extractDOCX then did a
// bare io.ReadAll on the DECOMPRESSED entry, and DEFLATE reaches ~1000:1 on
// repetitive input. A ~1 MB upload therefore allocated ~1 GB in one go, from
// POST /api/v1/chat/attachments, which needs only the chat permission.
func TestExtractDOCX_RefusesADecompressionBomb(t *testing.T) {
	// ~64 MB of one repeated byte compresses to a few kilobytes.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	chunk := bytes.Repeat([]byte("A"), 1<<20)
	for i := 0; i < 64; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if buf.Len() > 1<<20 {
		t.Fatalf("precondition: the bomb did not compress (%d bytes), so this test proves nothing", buf.Len())
	}

	_, err = extractDOCX(buf.Bytes())
	if err == nil {
		t.Fatalf("a %d-byte upload was expanded to 64 MB without complaint", buf.Len())
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("extraction failed, but not because of the size cap — that may be luck: %v", err)
	}
}

// The counterpart: an ordinary small document still extracts. A cap that also
// rejects real documents is not a fix.
func TestExtractDOCX_OrdinaryDocumentStillExtracts(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(`<?xml version="1.0"?><w:document xmlns:w="x"><w:body><w:p><w:r><w:t>hello world</w:t></w:r></w:p></w:body></w:document>`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := extractDOCX(buf.Bytes())
	if err != nil {
		t.Fatalf("a normal docx was rejected: %v", err)
	}
	if !strings.Contains(got, "hello world") {
		t.Fatalf("extracted text = %q", got)
	}
}
