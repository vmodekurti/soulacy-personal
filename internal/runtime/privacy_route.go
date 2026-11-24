package runtime

import (
	"context"
	"strings"

	"github.com/soulacy/soulacy/internal/llm"
	"github.com/soulacy/soulacy/internal/privacy"
	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/message"
)

// keepRunLocal decides whether this run is private and, if so, points the
// per-run definition at the local model and labels the run. It reports whether
// the run was moved or confirmed local.
//
// A conversation is private when the run was labelled so, when an earlier turn
// made it so, or when the new message is. Only the person's own message is
// judged, and only here, at the start: a run never changes models partway (#401). It does nothing when the router has
// no local route to offer, so installs without a local model are unchanged.
func (e *Engine) keepRunLocal(def *agent.Definition, msg message.Message, metadata *llm.CallMetadata) bool {
	if e == nil || e.llmRouter == nil || def == nil {
		return false
	}
	provider, model, ok := e.llmRouter.PrivateRoute(strings.TrimSpace(def.LLM.Provider))
	if !ok {
		return false
	}
	sess := e.getOrCreateSession(msg.SessionID, msg.AgentID)
	sess.mu.Lock()
	private := sess.Private
	sess.mu.Unlock()

	if !private {
		private = strings.EqualFold(strings.TrimSpace(metadata.DataClassification), privacy.ClassPrivate) ||
			strings.EqualFold(strings.TrimSpace(def.LLM.DataClassification), privacy.ClassPrivate)
	}
	// A delegated run's "message" was written by another agent and can quote
	// web content; it inherits its parent's label above and is not judged here.
	if !private && msg.Channel != "internal" {
		private, _ = privacy.DetectText(flattenParts(msg.Parts))
	}
	if !private {
		return false
	}
	sess.mu.Lock()
	sess.Private = true
	sess.mu.Unlock()
	if provider != strings.TrimSpace(def.LLM.Provider) {
		def.LLM.Provider, def.LLM.Model = provider, model
	}
	metadata.DataClassification = privacy.ClassPrivate
	return true
}

// routePrivateRun applies keepRunLocal at the start of a run. Deciding before
// the run is prepared means the working method is chosen for the model that
// will actually answer; the router enforces the same rule on every call, so
// anything missed here still cannot leave the machine. The returned function
// marks a successful reply so the person can see where it was answered.
func (e *Engine) routePrivateRun(ctx context.Context, def *agent.Definition, msg message.Message, metadata *llm.CallMetadata) (context.Context, func(*message.Message, error)) {
	if !e.keepRunLocal(def, msg, metadata) {
		return ctx, func(*message.Message, error) {}
	}
	return llm.WithCallMetadata(ctx, *metadata), func(reply *message.Message, err error) {
		if err != nil || reply == nil {
			return
		}
		if reply.Metadata == nil {
			reply.Metadata = map[string]string{}
		}
		reply.Metadata["answered_locally"] = "true"
	}
}
