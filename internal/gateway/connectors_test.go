package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestHandleListConnectorsFiltersAndNeverReturnsSecretValues(t *testing.T) {
	app := fiber.New()
	s := &Server{}
	app.Get("/connectors", s.handleListConnectors)
	req := httptest.NewRequest("GET", "/connectors?category=events&q=ticketmaster", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Count      int              `json:"count"`
		Connectors []map[string]any `json:"connectors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || body.Connectors[0]["id"] != "ticketmaster-public" {
		t.Fatalf("unexpected response: %+v", body)
	}
	raw, _ := json.Marshal(body)
	for _, forbidden := range []string{"secret_value", "api_key_value", "access_token_value"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("response contains forbidden field %q", forbidden)
		}
	}
}
