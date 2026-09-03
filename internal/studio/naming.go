package studio

import (
	"regexp"
	"strconv"
	"strings"
)

// ImproveGeneratedAgentName replaces Studio's broad fallback labels with a
// concise name derived from what this particular agent does. It runs only for
// brand-new generated drafts: an existing agent's name is user-owned, and an
// explicit "agent named ..." request is always authoritative.
func ImproveGeneratedAgentName(draft *Draft, cat Catalog) {
	if draft == nil || strings.TrimSpace(draft.ID) != "" {
		return
	}
	intent := strings.TrimSpace(draft.RawIntent)
	if intent == "" {
		intent = strings.TrimSpace(draft.Intent)
	}
	if explicit := explicitRequestedAgentName(intent); explicit != "" {
		draft.Name = explicit
		return
	}

	current := strings.Join(strings.Fields(strings.TrimSpace(draft.Name)), " ")
	candidate := semanticAgentName(intent, *draft)
	if candidate == "" {
		candidate = current
	}
	if !genericGeneratedName(current) && !agentNameCollision(current, cat.Agents) {
		return
	}
	if candidate == "" {
		candidate = "Purpose-Built Assistant"
	}
	draft.Name = availableGeneratedName(candidate, cat.Agents)
}

func genericGeneratedName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "soulacy agent", "research agent", "knowledge ingestion agent", "knowledge ingestion workflow",
		"stock advisor", "travel advisor", "weather expert", "deal finder", "notebook podcast agent",
		"research digest workflow", "market digest workflow", "deal digest workflow", "parallel specialist council":
		return true
	default:
		return false
	}
}

func semanticAgentName(intent string, draft Draft) string {
	low := strings.ToLower(intent)
	role := "Assistant"
	switch {
	case anyContains(low, "podcast", "notebooklm", "notebook lm", "audio overview"):
		role = "Podcast Producer"
	case anyContains(low, "ingest", "knowledge base", "knowledge", "kb_write", "index document", "store document"):
		role = "Knowledge Curator"
	case anyContains(low, "monitor", "watch", "track", "alert", "notify when"):
		role = "Monitor"
	case anyContains(low, "digest", "briefing", "brief", "summarize", "summarise"):
		role = "Briefing Agent"
	case anyContains(low, "weather", "forecast", "meteorolog"):
		role = "Weather Advisor"
	case anyContains(low, "travel", "flight", "hotel", "itinerary", "trip"):
		role = "Travel Planner"
	case anyContains(low, "stock", "ticker", "portfolio", "earnings", "equity", "market"):
		role = "Market Analyst"
	case anyContains(low, "deal", "discount", "coupon", "price drop", "bargain"):
		role = "Deal Scout"
	case anyContains(low, "research", "latest articles", "latest news", "find articles", "search"):
		role = "Research Curator"
	}

	qualifier := knowledgeQualifier(draft.Knowledge)
	if qualifier == "" {
		qualifier = domainQualifier(intent)
	}
	if qualifier == "" {
		qualifier = topicQualifier(low, role)
	}
	if qualifier == "" {
		return role
	}
	if strings.Contains(strings.ToLower(role), strings.ToLower(qualifier)) {
		return role
	}
	return qualifier + " " + role
}

func knowledgeQualifier(knowledge []string) string {
	if len(knowledge) == 0 {
		return ""
	}
	name := humanizeID(strings.TrimSpace(knowledge[0]))
	if strings.EqualFold(name, "knowledge") || strings.EqualFold(name, "knowledge base") || strings.EqualFold(name, "default") {
		return ""
	}
	return trimNameWords(name, 3)
}

var generatedNameDomain = regexp.MustCompile(`(?i)\b(?:https?://)?(?:www\.)?([a-z0-9][a-z0-9-]{1,30})\.(?:com|org|net|io|ai|gov|edu)\b`)

func domainQualifier(intent string) string {
	m := generatedNameDomain.FindStringSubmatch(intent)
	if len(m) != 2 {
		return ""
	}
	label := strings.ToLower(m[1])
	known := map[string]string{
		"hbr": "HBR", "harvardbusinessreview": "HBR", "sec": "SEC", "github": "GitHub",
		"linkedin": "LinkedIn", "youtube": "YouTube", "reddit": "Reddit", "gartner": "Gartner",
	}
	if display := known[label]; display != "" {
		return display
	}
	return humanizeID(label)
}

// topicQualifier deliberately uses a compact, product-oriented vocabulary.
// It is not trying to summarize the whole prompt; it only supplies enough
// subject identity to distinguish "AI News Monitor" from "Sales Monitor".
func topicQualifier(low, role string) string {
	topics := []struct {
		phrases []string
		name    string
	}{
		{[]string{"harvard business review", " hbr ", "hbr article"}, "HBR"},
		{[]string{"artificial intelligence", " ai ", "ai article", "ai news"}, "AI"},
		{[]string{"policy", "policies", "compliance", "regulation"}, "Policy"},
		{[]string{"customer support", "customer service", "support ticket"}, "Customer Support"},
		{[]string{"sales", "crm", "pipeline", "lead"}, "Sales"},
		{[]string{"security", "vulnerability", "threat", "incident"}, "Security"},
		{[]string{"contract", "legal", "agreement"}, "Contract"},
		{[]string{"document", "documents", "pdf", "file", "files"}, "Document"},
		{[]string{"executive", "leadership", "management"}, "Executive"},
		{[]string{"product", "competitor", "pricing"}, "Competitive"},
		{[]string{"finance", "financial", "stock", "market", "earnings"}, "Market"},
		{[]string{"travel", "flight", "hotel", "trip"}, "Travel"},
		{[]string{"weather", "forecast"}, "Weather"},
	}
	for _, topic := range topics {
		for _, phrase := range topic.phrases {
			if strings.Contains(" "+low+" ", phrase) && !strings.Contains(strings.ToLower(role), strings.ToLower(topic.name)) {
				return topic.name
			}
		}
	}
	return ""
}

func trimNameWords(name string, max int) string {
	words := strings.Fields(name)
	if len(words) > max {
		words = words[:max]
	}
	return strings.Join(words, " ")
}

func agentNameCollision(name string, existing []string) bool {
	want := slug(name)
	if want == "" {
		return false
	}
	for _, item := range existing {
		if slug(item) == want {
			return true
		}
	}
	return false
}

func availableGeneratedName(candidate string, existing []string) string {
	if !agentNameCollision(candidate, existing) {
		return candidate
	}
	// When two genuinely equivalent requests reduce to the same semantic name,
	// use the first available human-readable ordinal. A random/hash suffix is
	// technically unique but looks like an internal id leaked into the UI.
	for n := 2; n < 1000; n++ {
		name := candidate + " " + strconv.Itoa(n)
		if !agentNameCollision(name, existing) {
			return name
		}
	}
	return candidate + " New"
}
