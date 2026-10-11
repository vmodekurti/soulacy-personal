package runtime

import (
	"strings"
	"unicode"

	"github.com/soulacy/soulacy/pkg/agent"
)

// shouldUseFastGenieChat identifies turns that can be answered from the
// conversation and the model's own knowledge. It is deliberately conservative:
// anything involving the user's workspace, current information, or an action
// keeps Genie's complete tool surface.
func shouldUseFastGenieChat(agentID, goal string) bool {
	if agentID != GenieAgentID {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(goal))
	if text == "" {
		return false
	}

	for _, marker := range []string{
		" today", " current", " currently", " latest", " recent", " right now", " live ",
		"weather", "news", "score", "price", "stock", "market",
		"my ", "our ", "soulacy", "agent", "mission", "website", "connection", "connector",
		"file", "document", "inbox", "email", "calendar", "notebook", "podcast",
		"search", "find", "check", "inspect", "open", "read", "fetch", "browse",
		"create", "build", "make", "run", "execute", "fix", "update", "delete",
		"deploy", "install", "upload", "download", "send", "schedule", "automate",
		"book", "reserve", "buy", "order", "login", "log in", "sign in", "connect",
		"research", "analyze", "analyse", "compare", "plan",
	} {
		if strings.Contains(" "+text+" ", marker) {
			return false
		}
	}

	for _, exact := range []string{
		"hi", "hello", "hey", "thanks", "thank you", "ok", "okay", "got it",
		"how are you", "who are you", "what can you do",
	} {
		if trimSentencePunctuation(text) == exact {
			return true
		}
	}

	for _, prefix := range []string{
		"what is ", "what are ", "who is ", "who are ", "why ", "how does ",
		"how do ", "explain ", "define ", "tell me about ",
	} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func trimSentencePunctuation(s string) string {
	return strings.TrimRightFunc(strings.TrimSpace(s), func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})
}

// buildFastGenieSystemPrefix keeps Genie's identity and safety contract while
// leaving live catalogs out of a turn that cannot use them. This reduces prompt
// size without caching credentials or stale capability state.
func buildFastGenieSystemPrefix(def *agent.Definition) string {
	return renderPersonaPrefix(def) + def.SystemPrompt + "\n\n" +
		"This is a conversational turn. Answer directly and concisely. No external action or current workspace inspection is required." +
		"\n\n" + externalContentGuide
}
