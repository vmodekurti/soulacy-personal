package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/internal/runtime"
)

// buildBuilderEnvironment combines the executable tool catalog with visible,
// secret-free Website Access metadata. Credential values never enter the
// builder prompt or understanding.
func (s *Server) buildBuilderEnvironment(ctx context.Context, workspaceID, subject string) runtime.BuilderEnvironment {
	environment := runtime.BuilderEnvironment{Prompt: s.toolCatalog().BuilderPrompt()}
	if s.authConnections == nil {
		return environment
	}
	connections, err := s.authConnections.ListVisible(ctx, workspaceID, subject)
	if err != nil {
		s.log.Warn("builder could not list Website Access connections")
		return environment
	}
	now := time.Now()
	var section strings.Builder
	section.WriteString("\n## Website Access connections\n")
	section.WriteString("These are encrypted browser sessions, not MCP tools. When the user names a matching site, select its connection ID in `connections[]` automatically. If the user explicitly asks for Website Access, do not substitute generic web or MCP tools for those sites.\n")
	for _, connection := range connections {
		if connection.Kind != authconnections.KindBrowser {
			continue
		}
		ready := connection.Status == authconnections.StatusReady && connection.HasSecret &&
			(connection.ExpiresAt == nil || connection.ExpiresAt.After(now))
		environment.WebsiteConnections = append(environment.WebsiteConnections, runtime.BuilderConnection{
			ID: connection.ID, Name: connection.Name,
			Domains: append([]string(nil), connection.AllowedDomains...), Ready: ready,
		})
		status := "needs sign-in"
		if ready {
			status = "ready"
		}
		fmt.Fprintf(&section, "- **%s**: connection_id `%s`; status %s; domains %s\n",
			connection.Name, connection.ID, status, strings.Join(connection.AllowedDomains, ", "))
	}
	if len(environment.WebsiteConnections) > 0 {
		environment.Prompt += section.String()
	}
	return environment
}

func (s *Server) prepareBuilderConnectionSelection(ctx context.Context, agentID string, selectedIDs []string, opts builderDeployOptions) (authenticatedConnectionSelection, error) {
	selection := authenticatedConnectionSelection{selectedIDs: uniqueConnectionIDs(selectedIDs)}
	workspaceID := strings.TrimSpace(opts.WorkspaceID)
	if workspaceID == "" {
		workspaceID = runtime.PersonalWorkspaceID
	}
	selection.workspaceID = workspaceID
	if len(selection.selectedIDs) == 0 && s.authConnections == nil {
		return selection, nil
	}
	if s.authConnections == nil {
		return selection, fmt.Errorf("website access connections are not configured")
	}
	subject := strings.TrimSpace(opts.Subject)
	if subject == "" {
		subject = "admin"
	}
	visible, err := s.authConnections.ListVisible(ctx, workspaceID, subject)
	if err != nil {
		return selection, err
	}
	byID := make(map[string]authconnections.Connection, len(visible))
	for _, connection := range visible {
		byID[connection.ID] = connection
		if connection.Scope == authconnections.ScopeUser || isWorkspaceAdministrator(opts.Role) {
			selection.manageableIDs = append(selection.manageableIDs, connection.ID)
		}
	}
	now := time.Now()
	for _, id := range selection.selectedIDs {
		connection, ok := byID[id]
		if !ok {
			return selection, fmt.Errorf("website access connection is unavailable: %s", id)
		}
		if connection.Kind != authconnections.KindBrowser || connection.Status != authconnections.StatusReady ||
			!connection.HasSecret || (connection.ExpiresAt != nil && !connection.ExpiresAt.After(now)) {
			return selection, fmt.Errorf("website access connection %q needs sign-in or refresh", connection.Name)
		}
		if connection.Scope == authconnections.ScopeWorkspace && !isWorkspaceAdministrator(opts.Role) && !containsConnectionAgent(connection.AgentIDs, agentID) {
			return selection, fmt.Errorf("a workspace owner or admin must grant connection %q to this agent", connection.Name)
		}
	}
	return selection, nil
}
