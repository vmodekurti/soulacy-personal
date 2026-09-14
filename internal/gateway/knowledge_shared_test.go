package gateway

import (
	"strings"
	"testing"
)

func TestSharedDocumentTextUsesPhoneExtractionForImagesOnly(t *testing.T) {
	text, replaced := sharedDocumentText("whiteboard from the planning meeting", "Q3 goals: ship v2", "image/jpeg")
	if !replaced || !strings.HasPrefix(text, "Note from the person who shared this: whiteboard") || !strings.HasSuffix(text, "Q3 goals: ship v2") {
		t.Fatalf("image with OCR text should be ingested as text: %q %v", text, replaced)
	}
	if _, replaced := sharedDocumentText("caption", "ocr", "application/pdf"); replaced {
		t.Fatal("a PDF must keep the gateway's own extraction")
	}
	if _, replaced := sharedDocumentText("caption", "", "image/png"); replaced {
		t.Fatal("no extracted text means nothing to replace")
	}
	if text, replaced := sharedDocumentText("", "receipt total 42.10", "image/heic"); !replaced || text != "receipt total 42.10" {
		t.Fatalf("no caption means bare text: %q", text)
	}
	if captionPrefix("") != "" || !isTextMime("text/markdown; charset=utf-8") || isTextMime("image/jpeg") {
		t.Fatal("helper edge cases")
	}
}
