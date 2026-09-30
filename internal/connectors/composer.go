package connectors

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	EffectRead  = "read"
	EffectWrite = "write"

	ConnectorStatusReady = "ready"
	SiteAuthNone         = "none"
	SiteAuthOptional     = "optional"
)

// Capability describes one action available through public or optional
// authenticated access.
type Capability struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Effect      string `json:"effect"`
}

// Site is one website inside a user-created connector. It contains only
// metadata. Authentication material is stored by Website Access and is linked
// here by ID, never copied into the connector or its generated skill.
type Site struct {
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	BaseURL              string       `json:"base_url"`
	Domain               string       `json:"domain"`
	PublicAccess         bool         `json:"public_access"`
	AuthRequirement      string       `json:"auth_requirement"`
	AuthReason           string       `json:"auth_reason,omitempty"`
	AuthConnectionID     string       `json:"auth_connection_id,omitempty"`
	PublicCapabilities   []Capability `json:"public_capabilities"`
	AdvancedCapabilities []Capability `json:"advanced_capabilities,omitempty"`
}

// Connector is a user-owned integration recipe generated from an intent.
type Connector struct {
	ID           string    `json:"id"`
	WorkspaceID  string    `json:"workspace_id"`
	OwnerSubject string    `json:"owner_subject,omitempty"`
	Name         string    `json:"name"`
	Intent       string    `json:"intent"`
	Category     string    `json:"category"`
	Status       string    `json:"status"`
	SkillName    string    `json:"skill_name,omitempty"`
	Sites        []Site    `json:"sites"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type SiteSuggestion struct {
	Site
	Selected bool   `json:"selected"`
	Reason   string `json:"reason"`
}

type Plan struct {
	Name       string           `json:"name"`
	Intent     string           `json:"intent"`
	Category   string           `json:"category"`
	Summary    string           `json:"summary"`
	Sites      []SiteSuggestion `json:"sites"`
	SkillName  string           `json:"skill_name"`
	Guardrails []string         `json:"guardrails"`
}

type siteRecipe struct {
	name       string
	url        string
	categories []string
	aliases    []string
	public     []Capability
	advanced   []Capability
	authReason string
}

var urlPattern = regexp.MustCompile(`https?://[^\s,;]+`)
var safeSiteIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,96}$`)

var siteRecipes = []siteRecipe{
	{name: "Amazon", url: "https://www.amazon.com", categories: []string{"shopping"}, aliases: []string{"amazon"}, authReason: "Lists, order history, delivery details, and personalized account pages require your signed-in session."},
	{name: "eBay", url: "https://www.ebay.com", categories: []string{"shopping"}, aliases: []string{"ebay"}, authReason: "Watchlists, saved searches, messages, and purchase history require your signed-in session."},
	{name: "Etsy", url: "https://www.etsy.com", categories: []string{"shopping"}, aliases: []string{"etsy"}, authReason: "Favorites, messages, purchases, and shop management require your signed-in session."},
	{name: "Best Buy", url: "https://www.bestbuy.com", categories: []string{"shopping"}, aliases: []string{"best buy", "bestbuy"}, authReason: "Saved items, member pricing, local preferences, and orders require your signed-in session."},
	{name: "Walmart", url: "https://www.walmart.com", categories: []string{"shopping"}, aliases: []string{"walmart"}, authReason: "Lists, local store preferences, delivery details, and orders require your signed-in session."},
	{name: "Target", url: "https://www.target.com", categories: []string{"shopping"}, aliases: []string{"target"}, authReason: "Circle offers, lists, local inventory preferences, and orders require your signed-in session."},
	{name: "Ticketmaster", url: "https://www.ticketmaster.com", categories: []string{"events"}, aliases: []string{"ticketmaster"}, authReason: "Saved events, ticket management, transfers, and account-only offers require your signed-in session."},
	{name: "Eventbrite", url: "https://www.eventbrite.com", categories: []string{"events"}, aliases: []string{"eventbrite"}, authReason: "Saved events, registrations, organizer tools, and attendee data require your signed-in session."},
	{name: "Meetup", url: "https://www.meetup.com", categories: []string{"events"}, aliases: []string{"meetup"}, authReason: "Your groups, RSVPs, messages, and member-only events require your signed-in session."},
	{name: "Bandsintown", url: "https://www.bandsintown.com", categories: []string{"events"}, aliases: []string{"bandsintown", "concerts"}, authReason: "Tracked artists and personalized alerts require your signed-in session."},
	{name: "Booking.com", url: "https://www.booking.com", categories: []string{"travel"}, aliases: []string{"booking.com", "booking"}, authReason: "Member prices, saved stays, bookings, and messages require your signed-in session."},
	{name: "Airbnb", url: "https://www.airbnb.com", categories: []string{"travel"}, aliases: []string{"airbnb"}, authReason: "Wishlists, trips, messages, and account-specific prices require your signed-in session."},
	{name: "Kayak", url: "https://www.kayak.com", categories: []string{"travel"}, aliases: []string{"kayak"}, authReason: "Saved trips, alerts, and account preferences require your signed-in session."},
	{name: "HBR", url: "https://hbr.org", categories: []string{"research"}, aliases: []string{"hbr", "harvard business review"}, authReason: "Subscriber articles and saved content require your signed-in session."},
	{name: "Gartner", url: "https://www.gartner.com", categories: []string{"research"}, aliases: []string{"gartner"}, authReason: "Licensed research and account libraries require your signed-in session."},
	{name: "MIT Technology Review", url: "https://www.technologyreview.com", categories: []string{"research"}, aliases: []string{"mit technology review", "technology review"}, authReason: "Subscriber-only articles and saved content require your signed-in session."},
}

var categoryTerms = []struct {
	name  string
	terms []string
}{
	{name: "shopping", terms: []string{"shop", "shopping", "buy", "price", "product", "deal", "compare"}},
	{name: "events", terms: []string{"event", "ticket", "concert", "festival", "meetup", "show"}},
	{name: "travel", terms: []string{"travel", "flight", "hotel", "stay", "trip", "vacation"}},
	{name: "research", terms: []string{"research", "article", "publication", "news", "report", "insight"}},
}

// BuildPlan turns plain-language intent into an editable proposal. It is
// deterministic so it remains available without a configured model. The plan
// is a starting point and the user remains in control of the selected sites.
func BuildPlan(intent string) (Plan, error) {
	intent = strings.TrimSpace(intent)
	if len(intent) < 5 {
		return Plan{}, errors.New("describe what you want the connector to do")
	}
	if len(intent) > 1200 {
		return Plan{}, errors.New("intent must be 1200 characters or fewer")
	}
	lower := strings.ToLower(intent)
	category := inferCategory(lower)
	name := connectorName(category)
	selected := map[string]bool{}
	for _, recipe := range siteRecipes {
		for _, alias := range recipe.aliases {
			if strings.Contains(lower, alias) {
				selected[recipe.url] = true
				break
			}
		}
	}

	suggestions := make([]SiteSuggestion, 0)
	categoryCount := 0
	for _, recipe := range siteRecipes {
		if !containsString(recipe.categories, category) && !selected[recipe.url] {
			continue
		}
		isSelected := selected[recipe.url]
		if !isSelected && categoryCount < 3 {
			isSelected = true
		}
		categoryCount++
		site, _ := recipeSite(recipe, category)
		reason := "Suggested for " + category
		if selected[recipe.url] {
			reason = "Named in your request"
		}
		suggestions = append(suggestions, SiteSuggestion{Site: site, Selected: isSelected, Reason: reason})
	}

	seen := map[string]bool{}
	for _, suggestion := range suggestions {
		seen[suggestion.Domain] = true
	}
	for _, raw := range urlPattern.FindAllString(intent, -1) {
		site, err := NewCustomSite(raw, category)
		if err != nil || seen[site.Domain] {
			continue
		}
		seen[site.Domain] = true
		suggestions = append(suggestions, SiteSuggestion{Site: site, Selected: true, Reason: "Website included in your request"})
	}
	if len(suggestions) == 0 {
		category = "custom"
		name = "Custom connector"
	}

	return Plan{
		Name: name, Intent: intent, Category: category,
		Summary:    fmt.Sprintf("Soulacy will create a %s playbook, use public pages first, and keep optional website sign-ins isolated per site.", category),
		Sites:      suggestions,
		SkillName:  skillSlug(name),
		Guardrails: []string{"Public access never requires credentials.", "Each website sign-in is encrypted and restricted to its approved domain.", "Account actions remain subject to agent grants and approval policy."},
	}, nil
}

func NewCustomSite(rawURL, category string) (Site, error) {
	baseURL, domain, err := normalizePublicURL(rawURL)
	if err != nil {
		return Site{}, err
	}
	name := strings.TrimPrefix(domain, "www.")
	parts := strings.Split(name, ".")
	if len(parts) > 0 {
		name = strings.ToUpper(parts[0][:1]) + parts[0][1:]
	}
	public, advanced := capabilitiesForCategory(category)
	return Site{
		ID:                   "site_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Name:                 name,
		BaseURL:              baseURL,
		Domain:               domain,
		PublicAccess:         true,
		AuthRequirement:      SiteAuthOptional,
		AuthReason:           "Saved items, subscriptions, personalized results, or account pages may require your signed-in session.",
		PublicCapabilities:   public,
		AdvancedCapabilities: advanced,
	}, nil
}

func NormalizeConnector(in Connector) (Connector, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Intent = strings.TrimSpace(in.Intent)
	in.Category = strings.ToLower(strings.TrimSpace(in.Category))
	if in.Name == "" || len(in.Name) > 120 {
		return Connector{}, errors.New("connector name must be between 1 and 120 characters")
	}
	if in.Intent == "" || len(in.Intent) > 1200 {
		return Connector{}, errors.New("connector intent must be between 1 and 1200 characters")
	}
	if !containsString([]string{"shopping", "events", "travel", "research", "custom"}, in.Category) {
		in.Category = "custom"
	}
	if len(in.Sites) == 0 || len(in.Sites) > 25 {
		return Connector{}, errors.New("choose between 1 and 25 websites")
	}
	seen := map[string]bool{}
	for i := range in.Sites {
		baseURL, domain, err := normalizePublicURL(in.Sites[i].BaseURL)
		if err != nil {
			return Connector{}, fmt.Errorf("site %d: %w", i+1, err)
		}
		if seen[domain] {
			return Connector{}, fmt.Errorf("website %s is listed more than once", domain)
		}
		seen[domain] = true
		in.Sites[i].BaseURL = baseURL
		in.Sites[i].Domain = domain
		in.Sites[i].Name = strings.TrimSpace(in.Sites[i].Name)
		if in.Sites[i].Name == "" {
			in.Sites[i].Name = domain
		}
		if len(in.Sites[i].Name) > 120 || strings.ContainsAny(in.Sites[i].Name, "\r\n\x00") {
			return Connector{}, fmt.Errorf("site %d: name must be a single line of 120 characters or fewer", i+1)
		}
		if !safeSiteIDPattern.MatchString(in.Sites[i].ID) {
			in.Sites[i].ID = "site_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		in.Sites[i].PublicAccess = true
		in.Sites[i].AuthRequirement = SiteAuthOptional
		if len(in.Sites[i].PublicCapabilities) == 0 {
			in.Sites[i].PublicCapabilities, in.Sites[i].AdvancedCapabilities = capabilitiesForCategory(in.Category)
		}
	}
	in.Status = ConnectorStatusReady
	if in.SkillName == "" {
		in.SkillName = skillSlug(in.Name)
		if len(in.ID) > 8 {
			in.SkillName += "-" + in.ID[len(in.ID)-8:]
		}
	}
	return in, nil
}

func recipeSite(recipe siteRecipe, category string) (Site, error) {
	site, err := NewCustomSite(recipe.url, category)
	if err != nil {
		return Site{}, err
	}
	site.Name = recipe.name
	site.AuthReason = recipe.authReason
	if len(recipe.public) > 0 {
		site.PublicCapabilities = recipe.public
	}
	if len(recipe.advanced) > 0 {
		site.AdvancedCapabilities = recipe.advanced
	}
	return site, nil
}

func capabilitiesForCategory(category string) ([]Capability, []Capability) {
	switch category {
	case "shopping":
		return []Capability{
				{ID: "search", Label: "Search products", Description: "Find public product and listing pages.", Effect: EffectRead},
				{ID: "compare", Label: "Compare public details", Description: "Compare visible prices, availability, delivery, and product details.", Effect: EffectRead},
			}, []Capability{
				{ID: "personalized", Label: "Use account context", Description: "Read account-specific prices, lists, orders, or delivery details when requested.", Effect: EffectRead},
			}
	case "events":
		return []Capability{
				{ID: "search", Label: "Find events", Description: "Find public events by date, place, venue, or topic.", Effect: EffectRead},
				{ID: "compare", Label: "Compare event details", Description: "Compare visible schedules, venues, prices, and ticket links.", Effect: EffectRead},
			}, []Capability{
				{ID: "account_events", Label: "Use saved events and tickets", Description: "Read account-specific saved events, registrations, or ticket details when requested.", Effect: EffectRead},
			}
	case "travel":
		return []Capability{
				{ID: "search", Label: "Search travel", Description: "Find public stays, routes, prices, and availability.", Effect: EffectRead},
				{ID: "compare", Label: "Compare options", Description: "Compare visible dates, restrictions, amenities, and prices.", Effect: EffectRead},
			}, []Capability{
				{ID: "trips", Label: "Use trips and member details", Description: "Read saved trips, bookings, member prices, or messages when requested.", Effect: EffectRead},
			}
	case "research":
		return []Capability{
				{ID: "search", Label: "Find public content", Description: "Find public articles, reports, and publication pages.", Effect: EffectRead},
				{ID: "read", Label: "Read public content", Description: "Retrieve and summarize publicly available material.", Effect: EffectRead},
			}, []Capability{
				{ID: "subscriber", Label: "Read subscribed content", Description: "Read content available through the user's approved subscription session.", Effect: EffectRead},
			}
	default:
		return []Capability{
				{ID: "search", Label: "Search public pages", Description: "Find relevant public pages on the approved site.", Effect: EffectRead},
				{ID: "read", Label: "Read public pages", Description: "Retrieve publicly available content from the approved site.", Effect: EffectRead},
			}, []Capability{
				{ID: "account", Label: "Use account-only pages", Description: "Read approved account-only pages when the user requests them.", Effect: EffectRead},
			}
	}
}

func inferCategory(lower string) string {
	best, score := "custom", 0
	for _, candidate := range categoryTerms {
		current := 0
		for _, term := range candidate.terms {
			if strings.Contains(lower, term) {
				current++
			}
		}
		if current > score {
			best, score = candidate.name, current
		}
	}
	return best
}

func connectorName(category string) string {
	switch category {
	case "shopping":
		return "Shopping connector"
	case "events":
		return "Events connector"
	case "travel":
		return "Travel connector"
	case "research":
		return "Research connector"
	default:
		return "Custom connector"
	}
}

func normalizePublicURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(strings.TrimRight(raw, ".)]}"))
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return "", "", errors.New("website must be a public HTTPS URL")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return "", "", errors.New("local and private websites cannot be connector sites")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return "", "", errors.New("local and private websites cannot be connector sites")
	}
	port := u.Port()
	if port != "" && port != "443" {
		return "", "", errors.New("website must use the standard HTTPS port")
	}
	return "https://" + host, host, nil
}

func skillSlug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "custom"
	}
	return "connector-" + slug
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func SortConnectors(items []Connector) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
}
