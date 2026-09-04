package gateway

import (
	"context"
	"fmt"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/session"
)

type chatExperienceCheck struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type chatExperienceReadiness struct {
	Status        string                `json:"status"`
	Score         int                   `json:"score"`
	Ready         int                   `json:"ready"`
	Total         int                   `json:"total"`
	ChatAgents    int                   `json:"chat_agents"`
	Providers     int                   `json:"providers"`
	HistorySearch bool                  `json:"history_search"`
	Branching     bool                  `json:"branching"`
	Attachments   bool                  `json:"attachments"`
	Artifacts     bool                  `json:"artifacts"`
	Cancellation  bool                  `json:"cancellation"`
	Voice         bool                  `json:"voice"`
	Checks        []chatExperienceCheck `json:"checks"`
	NextActions   []string              `json:"next_actions"`
}

func (s *Server) handleChatStatus(c *fiber.Ctx) error {
	return c.JSON(s.chatExperienceReadiness(c))
}

func (s *Server) chatExperienceReadiness(c *fiber.Ctx) chatExperienceReadiness {
	_, _, chatAgents, _, _ := s.agentReadinessCounts()
	providersReady := countDoctorProviders(s.providerDoctorChecks(c), "ok", "warn")
	historySearch := false
	if s != nil && s.historyStore != nil {
		_, historySearch = s.historyStore.(interface {
			Search(ctx context.Context, agentID, query string, limit int) ([]session.SearchHit, error)
		})
	}
	branching := false
	if s != nil && s.historyStore != nil {
		_, branching = s.historyStore.(*session.SQLiteHistoryStore)
	}
	attachments := s != nil && s.resourceStore != nil
	artifacts := s != nil && s.actions != nil
	cancellation := s != nil && s.runReg != nil
	voiceReady := false
	voiceDetail := "Realtime voice is not configured."
	if s != nil {
		if minter := s.voiceMinterRef(); minter != nil {
			voiceReady, voiceDetail = minter.Ready()
		}
	}

	checks := []chatExperienceCheck{
		{
			Key:    "agents",
			Label:  "Chat-ready Agents",
			Status: statusIf(chatAgents > 0, "ok", "fail"),
			Detail: countDetail(chatAgents, "chat-ready agent", "No enabled agent is exposed to chat yet."),
		},
		{
			Key:    "providers",
			Label:  "Model Routing",
			Status: statusIf(providersReady > 0, "ok", "fail"),
			Detail: countDetail(providersReady, "usable model provider", "No usable LLM provider is ready for chat."),
		},
		{
			Key:    "history_search",
			Label:  "Conversation Search",
			Status: statusIf(historySearch, "ok", "warn"),
			Detail: statusDetail(historySearch, "Persisted chat history can be searched.", "History search is unavailable; wire a searchable history store."),
		},
		{
			Key:    "branching",
			Label:  "Branching",
			Status: statusIf(branching, "ok", "warn"),
			Detail: statusDetail(branching, "SQLite history enables conversation fork/branch workflows.", "Conversation branching needs the SQLite history store."),
		},
		{
			Key:    "attachments",
			Label:  "Attachments",
			Status: statusIf(attachments, "ok", "warn"),
			Detail: statusDetail(attachments, "Chat attachments are backed by the resource store.", "Attachment upload/download needs a resource store."),
		},
		{
			Key:    "artifacts",
			Label:  "Artifacts",
			Status: statusIf(artifacts, "ok", "warn"),
			Detail: statusDetail(artifacts, "Produced files can be surfaced back into Chat.", "Artifact discovery needs an action log backend."),
		},
		{
			Key:    "cancellation",
			Label:  "Stop/Retry Control",
			Status: statusIf(cancellation, "ok", "warn"),
			Detail: statusDetail(cancellation, "In-flight chat runs can be cancelled and retried.", "Run cancellation needs the run registry."),
		},
		{
			Key:    "voice",
			Label:  "Voice",
			Status: statusIf(voiceReady, "ok", "warn"),
			Detail: voiceDetail,
		},
	}

	ready := 0
	points := 0
	next := make([]string, 0, 4)
	for _, check := range checks {
		switch check.Status {
		case "ok":
			ready++
			points += 100
		case "warn":
			points += 60
			next = append(next, chatExperienceNextAction(check.Key))
		default:
			next = append(next, chatExperienceNextAction(check.Key))
		}
	}
	score := 0
	if len(checks) > 0 {
		score = points / len(checks)
	}
	if len(next) > 4 {
		next = next[:4]
	}

	return chatExperienceReadiness{
		Status:        statusFromScore(score),
		Score:         score,
		Ready:         ready,
		Total:         len(checks),
		ChatAgents:    chatAgents,
		Providers:     providersReady,
		HistorySearch: historySearch,
		Branching:     branching,
		Attachments:   attachments,
		Artifacts:     artifacts,
		Cancellation:  cancellation,
		Voice:         voiceReady,
		Checks:        checks,
		NextActions:   compactStrings(next),
	}
}

func parityChat(chat chatExperienceReadiness) parityArea {
	detail := fmt.Sprintf("%d/%d chat capabilities ready across agents, routing, history, artifacts, attachments, cancellation, and voice.", chat.Ready, chat.Total)
	next := "Keep polishing message ergonomics, citations, file previews, keyboard flows, and project context."
	if len(chat.NextActions) > 0 {
		next = chat.NextActions[0]
	}
	return parityArea{
		Key:       "chat",
		Label:     "Chat Experience",
		Status:    chat.Status,
		Score:     chat.Score,
		Detail:    detail,
		Next:      next,
		Benchmark: "ChatGPT/Claude",
		Href:      "#chat",
	}
}

func parityVoice(voice voiceReadiness) parityArea {
	if voice.Ready {
		model := voice.Model
		if model == "" {
			model = "configured realtime model"
		}
		return parityArea{
			Key:       "voice",
			Label:     "Realtime Voice",
			Status:    "ok",
			Score:     maxInt(voice.Score, 82),
			Detail:    fmt.Sprintf("Voice is configured for %s (%s); Chat can mint ephemeral sessions without exposing the API key.", voice.Provider, model),
			Next:      "Run a credential-backed mic session before launch if voice is in the product promise.",
			Benchmark: "OpenClaw",
			Href:      "#chat",
		}
	}
	if voice.Enabled {
		return parityArea{
			Key:       "voice",
			Label:     "Realtime Voice",
			Status:    voice.Status,
			Score:     voice.Score,
			Detail:    voice.Detail,
			Next:      voice.Next,
			Benchmark: "OpenClaw",
			Href:      "#chat",
		}
	}
	return parityArea{
		Key:       "voice",
		Label:     "Realtime Voice",
		Status:    "warn",
		Score:     55,
		Detail:    "Voice is an optional OpenClaw-style differentiator; text chat remains fully usable without it.",
		Next:      "Decide whether voice is launch scope. If yes, configure voice.provider: openai and run a real mic session.",
		Benchmark: "OpenClaw",
		Href:      "#chat",
	}
}

func chatExperienceNextAction(key string) string {
	switch key {
	case "agents":
		return "Enable at least one chat-facing agent."
	case "providers":
		return "Fix provider credentials or select a working default model."
	case "history_search":
		return "Use the SQLite history store so conversations are searchable."
	case "branching":
		return "Use the SQLite history store so conversations can be forked."
	case "attachments":
		return "Enable the resource store for chat uploads and previews."
	case "artifacts":
		return "Enable the action log backend so generated files appear in Chat."
	case "cancellation":
		return "Enable the run registry so slow runs can be stopped and retried."
	case "voice":
		return "Configure realtime voice credentials if voice parity is required."
	default:
		return "Review the chat experience setup."
	}
}
