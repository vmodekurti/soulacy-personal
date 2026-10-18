package gateway

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/internal/runtime"
)

func TestBuilderEnvironmentExposesReadyWebsiteSessionsWithoutSecrets(t *testing.T) {
	s := newTestGateway(t, "secret")
	store, err := authconnections.Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetAuthenticatedConnectionStore(store)
	connection, err := store.Create(t.Context(), authconnections.CreateInput{
		ID: "notebook", WorkspaceID: runtime.PersonalWorkspaceID, OwnerSubject: "admin",
		Scope: authconnections.ScopeUser, Kind: authconnections.KindBrowser,
		Name: "NotebookLM", BaseURL: "https://notebook.google.com", AllowedDomains: []string{"notebook.google.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSecret(t.Context(), runtime.PersonalWorkspaceID, connection.ID, nil); err != nil {
		t.Fatal(err)
	}

	environment := s.buildBuilderEnvironment(t.Context(), runtime.PersonalWorkspaceID, "admin")
	if len(environment.WebsiteConnections) != 1 || !environment.WebsiteConnections[0].Ready {
		t.Fatalf("connections = %#v", environment.WebsiteConnections)
	}
	if !strings.Contains(environment.Prompt, "NotebookLM") || !strings.Contains(environment.Prompt, "notebook.google.com") {
		t.Fatalf("prompt omitted Website Access metadata: %s", environment.Prompt)
	}
	if strings.Contains(environment.Prompt, "has_secret") || strings.Contains(environment.Prompt, "expires_at") {
		t.Fatalf("prompt exposed storage metadata: %s", environment.Prompt)
	}
}

func TestBuilderDeployPersistsAndGrantsWebsiteSession(t *testing.T) {
	s := newTestGateway(t, "secret")
	store, err := authconnections.Open(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s.SetAuthenticatedConnectionStore(store)
	connection, err := store.Create(t.Context(), authconnections.CreateInput{
		ID: "notebook", WorkspaceID: runtime.PersonalWorkspaceID, OwnerSubject: "admin",
		Scope: authconnections.ScopeUser, Kind: authconnections.KindBrowser,
		Name: "NotebookLM", BaseURL: "https://notebook.google.com", AllowedDomains: []string{"notebook.google.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSecret(t.Context(), runtime.PersonalWorkspaceID, connection.ID, nil); err != nil {
		t.Fatal(err)
	}

	u := &runtime.BuilderUnderstanding{
		Name: "daily-podcast", Description: "Create the daily podcast.",
		Purpose:      "Create a NotebookLM podcast and return the audio link.",
		SystemPrompt: "Use NotebookLM Website Access to create a notebook and generate a podcast.",
		Trigger:      &runtime.BuilderTrigger{Type: "manual"},
		Connections: []runtime.BuilderConnection{{
			ID: connection.ID, Name: connection.Name, Domains: connection.AllowedDomains, Ready: true,
		}},
	}
	built, err := s.deployFromUnderstanding(t.Context(), u, builderDeployOptions{
		Provider: "test", Model: "fake-model", WorkspaceID: runtime.PersonalWorkspaceID,
		Subject: "admin", Role: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !built.Decision.Allowed {
		t.Fatalf("deploy denied: %#v", built.Decision)
	}
	if len(built.Def.Connections) != 1 || built.Def.Connections[0] != connection.ID {
		t.Fatalf("definition connections = %v", built.Def.Connections)
	}
	stored, err := store.Get(t.Context(), runtime.PersonalWorkspaceID, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.AgentIDs) != 1 || stored.AgentIDs[0] != built.Def.ID {
		t.Fatalf("connection grants = %v", stored.AgentIDs)
	}
}
