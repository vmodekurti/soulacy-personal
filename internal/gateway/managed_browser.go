package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/internal/managedbrowser"
	"github.com/soulacy/soulacy/internal/runtime"
)

func (s *Server) StartWebsiteActionForGenie(ctx context.Context, rawURL, connectionID string) (map[string]any, error) {
	if s.managedBrowser == nil {
		return nil, errors.New("managed website actions are unavailable on this deployment")
	}
	result, err := s.managedBrowser.Start(ctx, managedbrowser.StartRequest{
		WorkspaceID:  runtime.PersonalWorkspaceID,
		Subject:      "admin",
		AgentID:      runtime.GenieAgentID,
		URL:          strings.TrimSpace(rawURL),
		ConnectionID: strings.TrimSpace(connectionID),
	})
	return managedBrowserResult("open the provider website", rawURL, result, err)
}

func (s *Server) InspectWebsiteActionForGenie(ctx context.Context, sessionID string) (map[string]any, error) {
	if s.managedBrowser == nil {
		return nil, errors.New("managed website actions are unavailable on this deployment")
	}
	result, err := s.managedBrowser.Observe(ctx, strings.TrimSpace(sessionID), runtime.GenieAgentID, "admin")
	return managedBrowserResult("inspect the provider website", "", result, err)
}

func (s *Server) ActOnWebsiteForGenie(ctx context.Context, sessionID, action, ref, value string) (map[string]any, error) {
	if s.managedBrowser == nil {
		return nil, errors.New("managed website actions are unavailable on this deployment")
	}
	result, err := s.managedBrowser.Act(ctx, strings.TrimSpace(sessionID), runtime.GenieAgentID, "admin", managedbrowser.Action{
		Kind: strings.TrimSpace(action), Ref: strings.TrimSpace(ref), Value: value,
	})
	return managedBrowserResult("prepare the provider action", "", result, err)
}

func (s *Server) CommitWebsiteActionForGenie(ctx context.Context, sessionID, ref, provider, action, item, schedule, terms, total string) (map[string]any, error) {
	if s.managedBrowser == nil {
		return nil, errors.New("managed website actions are unavailable on this deployment")
	}
	result, err := s.managedBrowser.Commit(ctx, strings.TrimSpace(sessionID), runtime.GenieAgentID, "admin", strings.TrimSpace(ref), managedbrowser.CommitReview{
		Provider: provider, Action: action, Item: item, Schedule: schedule, Terms: terms, Total: total,
	})
	return managedBrowserResult("submit the approved provider action", "", result, err)
}

func (s *Server) CloseWebsiteActionForGenie(_ context.Context, sessionID string) (map[string]any, error) {
	if s.managedBrowser == nil {
		return nil, errors.New("managed website actions are unavailable on this deployment")
	}
	if err := s.managedBrowser.CloseSession(strings.TrimSpace(sessionID), runtime.GenieAgentID, "admin"); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "status": "closed", "message": "Managed browser session closed and its temporary profile was removed."}, nil
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
