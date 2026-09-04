package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestKnowledgeKBParamDecodesEscapedName(t *testing.T) {
	app := fiber.New()
	app.Get("/api/v1/knowledge/:kb/documents", func(c *fiber.Ctx) error {
		return c.SendString(knowledgeKBParam(c))
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge/AI%20Documents/documents", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "AI Documents" {
		t.Fatalf("decoded KB name = %q, want %q", got, "AI Documents")
	}
}
