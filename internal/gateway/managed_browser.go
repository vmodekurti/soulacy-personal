package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/authconnections"
	"github.com/soulacy/soulacy/internal/managedbrowser"
	"github.com/soulacy/soulacy/internal/runtime"
)

func (s *Server) StartWebsiteActionForGenie(ctx context.Context, rawURL, connectionID string) (map[string]any, error) {
	if s.managedBrowser == nil {
		return nil, errors.New("managed website actions are unavailable on this deployment")
	}
	agentID := managedBrowserAgentID(ctx)
	connectionID = strings.TrimSpace(connectionID)
	var selected *authconnections.Connection
	if connectionID == "" && s.authConnections != nil {
		parsed, err := url.Parse(strings.TrimSpace(rawURL))
		if err == nil {
			connections, listErr := s.authConnections.ListVisible(ctx, runtime.PersonalWorkspaceID, runtime.SubjectFromContext(ctx))
			if listErr != nil {
				return nil, listErr
			}
			if connection, ok := matchingReadyWebsiteConnection(connections, agentID, parsed.Hostname(), time.Now()); ok {
				connectionID = connection.ID
				selected = &connection
			}
		}
	}
	result, err := s.managedBrowser.Start(ctx, managedbrowser.StartRequest{
		WorkspaceID:  runtime.PersonalWorkspaceID,
		Subject:      runtime.SubjectFromContext(ctx),
		AgentID:      agentID,
		URL:          strings.TrimSpace(rawURL),
		ConnectionID: connectionID,
	})
	out, err := managedBrowserResult("open the provider website", rawURL, result, err)
	if err == nil && selected != nil {
		out["route"] = "website_access"
		out["connection_id"] = selected.ID
		out["connection_name"] = selected.Name
		out["selection_reason"] = "matched a ready Website Access connection for this domain"
	}
	return out, err
}

func matchingReadyWebsiteConnection(connections []authconnections.Connection, agentID, host string, now time.Time) (authconnections.Connection, bool) {
	host = normalizeDomain(host)
	bestScore := -1
	var best authconnections.Connection
	for _, connection := range connections {
		if connection.Kind != authconnections.KindBrowser || connection.Status != authconnections.StatusReady || !connection.HasSecret ||
			(connection.ExpiresAt != nil && !connection.ExpiresAt.After(now)) || !missionContainsString(connection.AgentIDs, agentID) {
			continue
		}
		for _, boundary := range connection.AllowedDomains {
			boundary = normalizeDomain(boundary)
			if !hostWithinBoundary(host, boundary) {
				continue
			}
			score := len(boundary)
			if host == boundary {
				score += 10000
			}
			if score > bestScore {
				bestScore = score
				best = connection
			}
		}
	}
	return best, bestScore >= 0
}

func (s *Server) InspectWebsiteActionForGenie(ctx context.Context, sessionID string) (map[string]any, error) {
	if s.managedBrowser == nil {
		return nil, errors.New("managed website actions are unavailable on this deployment")
	}
	result, err := s.managedBrowser.Observe(ctx, strings.TrimSpace(sessionID), managedBrowserAgentID(ctx), runtime.SubjectFromContext(ctx))
	return managedBrowserResult("inspect the provider website", "", result, err)
}

func (s *Server) ActOnWebsiteForGenie(ctx context.Context, sessionID, action, ref, value string) (map[string]any, error) {
	if s.managedBrowser == nil {
		return nil, errors.New("managed website actions are unavailable on this deployment")
	}
	result, err := s.managedBrowser.Act(ctx, strings.TrimSpace(sessionID), managedBrowserAgentID(ctx), runtime.SubjectFromContext(ctx), managedbrowser.Action{
		Kind: strings.TrimSpace(action), Ref: strings.TrimSpace(ref), Value: value,
	})
	return managedBrowserResult("prepare the provider action", "", result, err)
}

func (s *Server) CommitWebsiteActionForGenie(ctx context.Context, sessionID, ref, provider, action, item, schedule, terms, total string) (map[string]any, error) {
	if s.managedBrowser == nil {
		return nil, errors.New("managed website actions are unavailable on this deployment")
	}
	result, err := s.managedBrowser.Commit(ctx, strings.TrimSpace(sessionID), managedBrowserAgentID(ctx), runtime.SubjectFromContext(ctx), strings.TrimSpace(ref), managedbrowser.CommitReview{
		Provider: provider, Action: action, Item: item, Schedule: schedule, Terms: terms, Total: total,
	})
	return managedBrowserResult("submit the approved provider action", "", result, err)
}

func (s *Server) CloseWebsiteActionForGenie(ctx context.Context, sessionID string) (map[string]any, error) {
	if s.managedBrowser == nil {
		return nil, errors.New("managed website actions are unavailable on this deployment")
	}
	if err := s.managedBrowser.CloseSession(strings.TrimSpace(sessionID), managedBrowserAgentID(ctx), runtime.SubjectFromContext(ctx)); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "status": "closed", "message": "Managed browser session closed and its temporary profile was removed."}, nil
}

func managedBrowserAgentID(ctx context.Context) string {
	if agentID := runtime.ActiveAgentFromContext(ctx); agentID != "" {
		return agentID
	}
	// Direct gateway calls predate active-agent context and belong to Genie.
	return runtime.GenieAgentID
}

func managedBrowserResult(operation, attempted string, result managedbrowser.Result, err error) (map[string]any, error) {
	if err != nil {
		fallback := strings.TrimSpace(result.Fallback)
		if fallback == "" {
			fallback = "Try the provider's official app, phone number, or another official provider route."
		}
		if attempted = strings.TrimSpace(attempted); attempted != "" {
			return nil, fmt.Errorf("could not %s at %s: %w. Official fallback: %s", operation, attempted, err, fallback)
		}
		return nil, fmt.Errorf("could not %s: %w. Official fallback: %s", operation, err, fallback)
	}
	b, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return nil, marshalErr
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
