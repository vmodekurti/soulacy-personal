package gateway

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/internal/connectors"
)

func TestGenieCreatesUserConnectorFromSelectedSites(t *testing.T) {
	s := newTestGateway(t, "secret")
	store, err := connectors.OpenStore(filepath.Join(t.TempDir(), "connectors.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetConnectorStore(store)

	created, err := s.CreateConnectorForGenie(context.Background(), "Compare products before I buy", "My stores", []string{"eBay", "Etsy"})
	if err != nil {
		t.Fatal(err)
	}
	connector := created["connector"].(connectors.Connector)
	if connector.Name != "My stores" || len(connector.Sites) != 2 {
		t.Fatalf("connector=%+v", connector)
	}

	listed, err := s.ListConnectorsForGenie(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if listed["count"] != 1 {
		t.Fatalf("listed=%v", listed)
	}
}

func TestConnectorComposerLifecycleAndWebsiteAccessLink(t *testing.T) {
	s := newTestGateway(t, "secret")
	store, err := connectors.OpenStore(filepath.Join(t.TempDir(), "connectors.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetConnectorStore(store)
	connections, err := authconnections.Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connections.Close() })
	s.SetAuthenticatedConnectionStore(connections)
	s.SetCredentialVault(newMemVault())

	status, planned := gatewayJSON(t, s, http.MethodPost, "/api/v1/connectors/plan", "secret", `{
		"intent":"Compare products across eBay and Etsy"
	}`)
	if status != http.StatusOK {
		t.Fatalf("plan status=%d body=%v", status, planned)
	}
	plan := planned["plan"].(map[string]any)
	if plan["category"] != "shopping" {
		t.Fatalf("plan category=%v", plan["category"])
	}

	status, created := gatewayJSON(t, s, http.MethodPost, "/api/v1/connectors", "secret", `{
		"name":"My shopping sites","intent":"Compare products across eBay and Etsy","category":"shopping",
		"sites":[
			{"id":"ebay","name":"eBay","base_url":"https://www.ebay.com"},
			{"id":"etsy","name":"Etsy","base_url":"https://www.etsy.com"}
		]
	}`)
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%v", status, created)
	}
	connector := created["connector"].(map[string]any)
	id := connector["id"].(string)
	sites := connector["sites"].([]any)
	siteID := sites[0].(map[string]any)["id"].(string)
	if connector["skill_name"] == "" {
		t.Fatal("generated skill name is missing")
	}

	status, access := gatewayJSON(t, s, http.MethodPost, "/api/v1/connectors/"+id+"/sites/"+siteID+"/website-access", "secret", `{}`)
	if status != http.StatusCreated {
		t.Fatalf("website access status=%d body=%v", status, access)
	}
	connection := access["connection"].(map[string]any)
	if connection["status"] != authconnections.StatusPending {
		t.Fatalf("connection status=%v", connection["status"])
	}
	domains := connection["allowed_domains"].([]any)
	if len(domains) != 1 || domains[0] != "www.ebay.com" {
		t.Fatalf("connection domains=%v", domains)
	}

	status, raw := gatewayRaw(t, s, http.MethodGet, "/api/v1/connectors?q=etsy", "secret", "")
	if status != http.StatusOK || !strings.Contains(raw, id) || !strings.Contains(raw, "auth_connection_id") {
		t.Fatalf("list status=%d body=%s", status, raw)
	}
	for _, forbidden := range []string{"cookie", "password", "secret_value", "access_token_value"} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Fatalf("connector response contains forbidden value marker %q: %s", forbidden, raw)
		}
	}

	status, deleted := gatewayJSON(t, s, http.MethodDelete, "/api/v1/connectors/"+id, "secret", "")
	if status != http.StatusOK || deleted["ok"] != true {
		t.Fatalf("delete status=%d body=%v", status, deleted)
	}
}

func TestConnectorSkillNeverEmbedsAuthenticationMaterial(t *testing.T) {
	item := connectors.Connector{
		ID: "connector_1", Name: "Research", Intent: "Read subscribed research", Category: "research", SkillName: "connector-research",
		Sites: []connectors.Site{{Name: "Example", BaseURL: "https://example.com", Domain: "example.com", AuthConnectionID: "conn_private"}},
	}
	markdown := connectorSkillMarkdown(item)
	if strings.Contains(markdown, "conn_private") || strings.Contains(markdown, "browser_storage_state") {
		t.Fatalf("generated skill exposed authentication material: %s", markdown)
	}
	if !strings.Contains(markdown, "Website Access") || !strings.Contains(markdown, "Public access does not require credentials") {
		t.Fatalf("generated skill lacks access guidance: %s", markdown)
	}
}
