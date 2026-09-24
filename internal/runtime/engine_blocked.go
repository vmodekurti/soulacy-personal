package runtime

import (
	"fmt"
	"strings"

	"github.com/soulacy/soulacy/pkg/message"
)

// Diagnosing a run that went nowhere.
//
// A run that asks for something this deployment cannot do fails twice over.
// First the model tries the obvious tool and is refused by a guardrail. Then,
// because nothing tells it the refusal is permanent, it tries a different
// route to the same place and is refused again — and each attempt re-sends the
// whole transcript, so the run pays full prompt price per turn to rediscover
// the same wall. Eventually some ceiling fires and the person sees only the
// ceiling.
//
// That is what happened installing an MCP server that needs its own runtime:
// twenty model calls and ten minutes, of which the last seventeen were the
// model working around a missing shell it was never going to get, ending in
// "agent token budget cannot fit the next prompt" — a message about the
// backstop, not about the cause (#229, #230).
//
// Two ideas fix it:
//
//   - A refusal for a *missing capability or policy* is durable. The same call
//     will be refused for the rest of the run, so say so once, plainly, and
//     stop the run when enough different doors have been found locked.
//   - Remember the first real failure. Whatever ends the run, the person
//     should be told what actually broke first.
//
// A refusal that is merely transient — a timeout, a 404, a bad argument — is
// deliberately NOT durable: retrying those is how an agent makes progress.

// blockedToolStopAt is how many DIFFERENT tools may be durably refused before
// the run is abandoned. Three is a deliberate compromise: a healthy run almost
// never meets three separate permission walls, while the failing install above
// met four. Counting distinct tools rather than consecutive failures matters —
// the model interleaves a working call with each blocked one, so a consecutive
// counter never fires.
const blockedToolStopAt = 3

// durableBlocks are the refusals that will not change within a run. Each entry
// matches the lowercased tool error and explains the wall in the operator's
// terms, because the person reading it usually cannot see the agent's config.
var durableBlocks = []struct {
	match  string
	reason string
}{
	{"capability in the agent's", "the agent has not been granted that capability"},
	{"requires the 'system' capability", "the agent has not been granted the system capability"},
	{"outside configured workspace roots", "that path is outside the workspace this agent may read"},
	{"filesystem access denied", "that path is outside the workspace this agent may read"},
	{"ssrf:", "the network policy refuses that address"},
	{"learning requires an authenticated", "learning is not enabled for this run"},
	{"insufficient permissions", "this agent's role does not allow it"},
	{"access denied", "this deployment does not permit it"},
	{"is not available on", "that tool is not available in this deployment"},
	{"not permitted", "this deployment does not permit it"},
}

// remoteFetchTools reach the internet, where a 403 or "access denied" belongs
// to somebody else's server and says nothing about this deployment. Trying a
// different URL is legitimate progress, so only OUR OWN refusals (the SSRF
// guard, an approval declined) count as durable for these.
var remoteFetchTools = map[string]bool{
	"fetch_url": true, "http_request": true, "web_search": true, "browser_open": true,
}

// blockedReason classifies a tool error as durable and explains it, or returns
// "" when the failure is the ordinary kind an agent should retry around.
func blockedReason(name, content string) string {
	c := strings.ToLower(content)
	if !strings.HasPrefix(c, "error:") {
		return ""
	}
	// A refused approval is the person saying no. It is durable for this run,
	// but it is not a misconfiguration and must not read like one. Match the
	// engine's own wording rather than a loose "denied by", which also appears
	// in a remote server's 403 ("access denied by the origin server").
	if strings.Contains(c, "was denied by the user") {
		return "someone declined the approval"
	}
	if remoteFetchTools[normalizeToolCallName(name)] {
		if strings.Contains(c, "ssrf:") {
			return "the network policy refuses that address"
		}
		// Anything else is the far end's answer, not our wall.
		return ""
	}
	for _, b := range durableBlocks {
		if strings.Contains(c, b.match) {
			return b.reason
		}
	}
	return ""
}

// runFailures remembers what went wrong while a run was under way.
type runFailures struct {
	// first substantive tool failure of the run, already formatted.
	first string
	// blocked keeps one entry per tool name, in the order first seen.
	blocked      []blockedTool
	blockedIndex map[string]bool
}

type blockedTool struct{ name, reason string }

// observe records this turn's tool results and returns any steer to hand the
// model on the next turn. The steer is sent once per tool: repeating it would
// itself cost a prompt each turn, which is the problem being solved.
func (f *runFailures) observe(results []message.ToolResult) []string {
	var nudges []string
	for _, tr := range results {
		if !tr.IsError {
			continue
		}
		name := normalizeToolCallName(tr.Name)
		if f.first == "" {
			f.first = fmt.Sprintf("%s: %s", name, firstLine(strings.TrimPrefix(tr.Content, "error: ")))
		}
		reason := blockedReason(tr.Name, tr.Content)
		if reason == "" {
			continue
		}
		if f.blockedIndex == nil {
			f.blockedIndex = map[string]bool{}
		}
		if f.blockedIndex[name] {
			continue
		}
		f.blockedIndex[name] = true
		f.blocked = append(f.blocked, blockedTool{name: name, reason: reason})
		nudges = append(nudges, fmt.Sprintf(
			"%q is not available for this run: %s. This will not change while the run lasts, so do NOT call %q again "+
				"and do not look for another way around it. Either finish using the tools that do work, or say plainly "+
				"what cannot be done here and what the person would have to change.",
			name, reason, name))
	}
	return nudges
}

// shouldStop reports a run that has met enough locked doors to conclude the
// environment cannot do what was asked.
func (f *runFailures) shouldStop() bool { return len(f.blocked) >= blockedToolStopAt }

// stopMessage is the answer the person gets instead of a budget error. It
// names every wall, because fixing one of them rarely unblocks the task.
func (f *runFailures) stopMessage() string {
	var b strings.Builder
	b.WriteString("I stopped because this deployment refused the tools needed to finish.\n\n")
	for _, blk := range f.blocked {
		fmt.Fprintf(&b, "- `%s` — %s\n", blk.name, blk.reason)
	}
	b.WriteString("\nEach of these is a permission or policy decision, not a temporary error, ")
	b.WriteString("so trying again as-is will fail the same way. ")
	if f.first != "" {
		fmt.Fprintf(&b, "The first thing that failed was %s. ", f.first)
	}
	b.WriteString("Grant what is missing and ask again, or ask me for something that fits what this deployment allows.")
	return b.String()
}

// annotate names the first real failure alongside whatever ended the run, so a
// ceiling (token budget, call budget, timeout) never reads as the cause (#229).
func (f *runFailures) annotate(err error) error {
	if err == nil || f == nil || f.first == "" {
		return err
	}
	return fmt.Errorf("%w (the first failure in this run was %s)", err, f.first)
}

// firstLine keeps an error readable in a one-line summary. Tool errors often
// carry a whole stdout transcript behind their first sentence.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const max = 160
	if len(s) > max {
		s = strings.TrimSpace(s[:max]) + "…"
	}
	return s
}

// blockedNames lists the refused tools for the error event, so an operator
// grepping logs sees the same set the person was told about.
func (f *runFailures) blockedNames() []string {
	out := make([]string, 0, len(f.blocked))
	for _, b := range f.blocked {
		out = append(out, b.name)
	}
	return out
}
