package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/soulacy/soulacy/internal/person"
	"github.com/soulacy/soulacy/pkg/message"
)

// personStore is set when the gateway has a person model configured.
var personStore atomic.Pointer[personRuntime]

type personRuntime struct{ store person.Store }

// SetPersonModel installs (or replaces) the person model store. Nil disables
// the tools, which is the state on a gateway that has not enabled it.
func (e *Engine) SetPersonModel(store person.Store) {
	if store == nil {
		personStore.Store(nil)
		return
	}
	personStore.Store(&personRuntime{store: store})
}

// PersonModel returns the configured store, or nil.
func (e *Engine) PersonModel() person.Store {
	if rt := personStore.Load(); rt != nil {
		return rt.store
	}
	return nil
}

// buildPersonBuiltins exposes the person model to agents as two tools.
//
// One read tool, deliberately. An agent asking "what kind of day is this
// person having" should not have to call location, calendar, health and
// focus and reason about raw samples: the observers already did that, and
// consistency across agents matters more than freshness measured in seconds.
func (e *Engine) buildPersonBuiltins() []BuiltinTool {
	resolve := func(ctx context.Context) (person.Store, string, error) {
		rt := personStore.Load()
		if rt == nil {
			return nil, "", fmt.Errorf("the person model is not enabled on this gateway")
		}
		owner, err := mobileToolOwner(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("the person model needs to know who is asking: %w", err)
		}
		return rt.store, owner, nil
	}

	return []BuiltinTool{
		{
			Name: "person.model",
			Description: "What Soulacy understands about the person you are working for: who they are, " +
				"their usual day, how they are right now, the people who matter, open commitments and " +
				"preferences. Prefer this over asking their phone for raw signals. Optionally narrow by " +
				"section or search term.",
			Gate: "person",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sections": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string", "enum": sectionNames()},
						"description": "Sections to read. Omit for all of them.",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "Optional words to look for, e.g. \"priya proposal\".",
					},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				store, owner, err := resolve(ctx)
				if err != nil {
					return "", err
				}
				now := time.Now().UTC()
				model, err := person.ModelFor(ctx, store, owner, now)
				if err != nil {
					return "", err
				}
				if query := strings.TrimSpace(stringArg(args, "query")); query != "" {
					hits := person.Search(model, query, 20)
					if len(hits) == 0 {
						return fmt.Sprintf("Nothing in what Soulacy knows about them matches %q.", query), nil
					}
					return person.Render(person.NewModel(owner, hits, now), now), nil
				}
				return person.RenderSections(model, sectionsArg(args), now), nil
			},
		},
		{
			Name: "person.observe",
			Description: "Record something you learned about the person so every future run knows it too. " +
				"Use a stable key so repeated observations update rather than pile up. Anything the person " +
				"stated themselves outranks this and will not be overwritten.",
			Gate: "person",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"section": map[string]any{"type": "string", "enum": sectionNames(), "description": "Which part of the model this belongs to."},
					"key":     map[string]any{"type": "string", "description": "Stable identifier within the section, e.g. \"priya-s\" or \"weekday.leave\"."},
					"summary": map[string]any{"type": "string", "description": "One sentence, in plain language."},
					"confidence": map[string]any{
						"type": "number", "minimum": 0, "maximum": 1,
						"description": "How sure you are. Be honest; low-confidence entries are marked as guesses.",
					},
					"expires_in_hours": map[string]any{
						"type":        "number",
						"description": "For anything that goes stale, such as how they are right now.",
					},
				},
				"required": []string{"section", "key", "summary"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				store, owner, err := resolve(ctx)
				if err != nil {
					return "", err
				}
				// The inbound message already carries the agent; no extra
				// plumbing, and it is the same id the audit log records.
				agentID := "unknown"
				if msg, ok := ctx.Value(inboundMsgKey{}).(message.Message); ok && strings.TrimSpace(msg.AgentID) != "" {
					agentID = strings.TrimSpace(msg.AgentID)
				}
				entry := person.Entry{
					Owner:      owner,
					Section:    person.Section(strings.TrimSpace(stringArg(args, "section"))),
					Key:        stringArg(args, "key"),
					Summary:    stringArg(args, "summary"),
					Source:     person.SourceAgentPrefix + agentID,
					Confidence: float32(floatArg(args, "confidence")),
				}
				if hours := floatArg(args, "expires_in_hours"); hours > 0 {
					expires := time.Now().UTC().Add(time.Duration(hours * float64(time.Hour)))
					entry.ExpiresAt = &expires
				}
				result, err := store.Put(ctx, entry)
				if err != nil {
					return "", err
				}
				if !result.Applied {
					return "Not recorded: " + result.Reason + ". What is stored is: " + result.Entry.Summary, nil
				}
				return "Recorded.", nil
			},
		},
	}
}

func sectionNames() []string {
	names := make([]string, 0, len(person.Sections))
	for _, section := range person.Sections {
		names = append(names, string(section))
	}
	return names
}

func sectionsArg(args map[string]any) []person.Section {
	raw, ok := args["sections"].([]any)
	if !ok {
		return nil
	}
	var sections []person.Section
	for _, item := range raw {
		if name, ok := item.(string); ok {
			if section := person.Section(strings.TrimSpace(name)); section.Valid() {
				sections = append(sections, section)
			}
		}
	}
	return sections
}

func stringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func floatArg(args map[string]any, key string) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 0
}
