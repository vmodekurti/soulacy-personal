// Package connectors defines the product-facing integration catalog.
//
// A connector is metadata and setup guidance around an execution adapter. The
// adapter remains an MCP server, plugin, or built-in tool, so this package does
// not introduce another tool protocol or handle secret values.
package connectors

import (
	"sort"
	"strings"
)

const (
	EffectRead  = "read"
	EffectWrite = "write"

	StatusRecipe  = "recipe"
	StatusPlanned = "planned"

	AuthNone     = "none"
	AuthOptional = "optional"
	AuthRequired = "required"
)

// Credential describes a secret slot by name. Values never enter the catalog.
type Credential struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
}

// Capability is one action a connector adapter may expose.
type Capability struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Effect      string `json:"effect"`
}

// Definition is safe to return to a browser. It contains no credential values.
type Definition struct {
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	Provider          string       `json:"provider"`
	Category          string       `json:"category"`
	Summary           string       `json:"summary"`
	Status            string       `json:"status"`
	AuthRequirement   string       `json:"auth_requirement"`
	AuthType          string       `json:"auth_type"`
	DocsURL           string       `json:"docs_url"`
	SignupURL         string       `json:"signup_url,omitempty"`
	AdapterKinds      []string     `json:"adapter_kinds"`
	DeploymentTargets []string     `json:"deployment_targets"`
	RecommendedSkills []string     `json:"recommended_skills,omitempty"`
	Credentials       []Credential `json:"credentials"`
	Capabilities      []Capability `json:"capabilities"`
	SetupSteps        []string     `json:"setup_steps"`
	Checkout          string       `json:"checkout"`
	Limitations       []string     `json:"limitations,omitempty"`
}

// Catalog returns a copy of the built-in connector definitions.
func Catalog() []Definition {
	out := make([]Definition, len(catalog))
	copy(out, catalog)
	return out
}

// Filter applies the search and category query used by the GUI. Matching is
// case-insensitive across names, providers, summaries, and capability labels.
func Filter(query, category string) []Definition {
	query = strings.ToLower(strings.TrimSpace(query))
	category = strings.ToLower(strings.TrimSpace(category))
	var out []Definition
	for _, d := range catalog {
		if category != "" && category != "all" && strings.ToLower(d.Category) != category {
			continue
		}
		if query != "" && !matches(d, query) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// Categories returns the stable set of category names represented in Catalog.
func Categories() []string {
	set := map[string]bool{}
	for _, d := range catalog {
		set[d.Category] = true
	}
	out := make([]string, 0, len(set))
	for category := range set {
		out = append(out, category)
	}
	sort.Strings(out)
	return out
}

func matches(d Definition, query string) bool {
	values := []string{d.ID, d.Name, d.Provider, d.Category, d.Summary}
	for _, capability := range d.Capabilities {
		values = append(values, capability.Label, capability.Description)
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

var catalog = []Definition{
	{
		ID: "ebay-public", Name: "eBay Shopping", Provider: "eBay", Category: "shopping",
		Summary: "Search public listings, compare visible prices and shipping, and inspect item details without an eBay developer account.",
		Status:  StatusRecipe, AuthRequirement: AuthNone, AuthType: "No provider account",
		DocsURL:      "https://www.ebay.com/",
		AdapterKinds: []string{"web", "mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		RecommendedSkills: []string{"shopping-research"},
		Capabilities: []Capability{
			{ID: "search_products", Label: "Search public listings", Description: "Find publicly visible listings by product name, model, category, or attributes.", Effect: EffectRead},
			{ID: "get_product", Label: "Read listing details", Description: "Read visible price, shipping, condition, seller, returns, and listing URL.", Effect: EffectRead},
			{ID: "compare_products", Label: "Compare listings", Description: "Compare facts retrieved from multiple public listing pages.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Grant fetch_url to the selected agent; web_search is optional for broader discovery.", "Assign the shopping-research skill.", "Ask the agent to search eBay or give it a public listing URL.", "Optionally connect a reviewed eBay adapter when you need structured API data."},
		Checkout:    "Open the provider purchase URL for the user to review and complete checkout.",
		Limitations: []string{"Public pages may vary by country or block automated retrieval; report that clearly instead of requesting credentials.", "Account data and structured API access require a separate optional adapter.", "Soulacy does not place orders through this recipe."},
	},
	{
		ID: "best-buy-public", Name: "Best Buy Products", Provider: "Best Buy", Category: "shopping",
		Summary: "Search public product pages and compare visible prices, availability, specifications, and store information without an API key.",
		Status:  StatusRecipe, AuthRequirement: AuthNone, AuthType: "No provider account",
		DocsURL:      "https://www.bestbuy.com/",
		AdapterKinds: []string{"web", "mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		RecommendedSkills: []string{"shopping-research"},
		Capabilities: []Capability{
			{ID: "search_products", Label: "Search public products", Description: "Find public product pages and compare visible prices and attributes.", Effect: EffectRead},
			{ID: "get_product", Label: "Read product details", Description: "Read visible specifications, descriptions, reviews, availability, and provider links.", Effect: EffectRead},
			{ID: "find_stores", Label: "Find stores", Description: "Find public store pages and visible store availability when shown.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Grant fetch_url to the selected agent; web_search is optional for broader discovery.", "Assign the shopping-research skill.", "Ask the agent to search Best Buy or give it a public product URL.", "Optionally connect a reviewed Best Buy adapter when you need structured API data."},
		Checkout:    "Open the Best Buy product or add-to-cart URL for the user to finish checkout.",
		Limitations: []string{"Public pages may be dynamic or region-specific; report fields that cannot be verified.", "Account, cart, and order capabilities require separate provider access."},
	},
	{
		ID: "etsy-public", Name: "Etsy Marketplace", Provider: "Etsy", Category: "shopping",
		Summary: "Discover public marketplace listings and shops without registering an Etsy application.",
		Status:  StatusRecipe, AuthRequirement: AuthNone, AuthType: "No provider account",
		DocsURL:      "https://www.etsy.com/",
		AdapterKinds: []string{"web", "mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		RecommendedSkills: []string{"shopping-research"},
		Capabilities: []Capability{
			{ID: "search_listings", Label: "Search public listings", Description: "Find publicly visible listings by item, style, category, or shop.", Effect: EffectRead},
			{ID: "get_listing", Label: "Read listing details", Description: "Read visible listing, price, image, variation, shop, and delivery information.", Effect: EffectRead},
			{ID: "get_shop", Label: "Read shop details", Description: "Read public shop and seller information.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Grant fetch_url to the selected agent; web_search is optional for broader discovery.", "Assign the shopping-research skill.", "Ask the agent to search Etsy or give it a public listing URL.", "Optionally connect a reviewed Etsy adapter for structured or account-specific data."},
		Checkout:    "Open the Etsy listing URL for the user to review and complete checkout.",
		Limitations: []string{"Public pages may not expose every variation or shipping total until the buyer supplies a destination.", "Favorites, messages, shop management, and purchases require separate account access."},
	},
	{
		ID: "ticketmaster-public", Name: "Ticketmaster Events", Provider: "Ticketmaster", Category: "events",
		Summary: "Find public event pages, attractions, venues, dates, visible availability, and ticket links without a developer key.",
		Status:  StatusRecipe, AuthRequirement: AuthNone, AuthType: "No provider account",
		DocsURL:      "https://www.ticketmaster.com/",
		AdapterKinds: []string{"web", "mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		RecommendedSkills: []string{"event-finder"},
		Capabilities: []Capability{
			{ID: "search_events", Label: "Search public events", Description: "Find public event pages by location, date, attraction, venue, genre, or keyword.", Effect: EffectRead},
			{ID: "get_event", Label: "Read event details", Description: "Read visible date, venue, location, status, price range, and ticket link details.", Effect: EffectRead},
			{ID: "search_venues", Label: "Search venues", Description: "Find public venue and attraction pages in supported markets.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Grant fetch_url to the selected agent; web_search is optional for broader discovery.", "Assign the event-finder skill.", "Ask the agent to search Ticketmaster or give it a public event URL.", "Optionally connect a reviewed Ticketmaster adapter when you need structured API data."},
		Checkout:    "Open the event's Ticketmaster URL for the user to select tickets and complete checkout.",
		Limitations: []string{"Final prices, fees, and seat inventory may appear only during interactive ticket selection.", "Inventory and commerce APIs require separate partner access."},
	},
	{
		ID: "eventbrite-public", Name: "Eventbrite Events", Provider: "Eventbrite", Category: "events",
		Summary: "Discover public Eventbrite events, venues, dates, visible prices, and registration links without an organizer token.",
		Status:  StatusRecipe, AuthRequirement: AuthNone, AuthType: "No provider account",
		DocsURL:      "https://www.eventbrite.com/",
		AdapterKinds: []string{"web", "mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		RecommendedSkills: []string{"event-finder"},
		Capabilities: []Capability{
			{ID: "search_events", Label: "Search public events", Description: "Find public event pages by topic, date, location, organizer, or venue.", Effect: EffectRead},
			{ID: "get_event", Label: "Read event details", Description: "Read visible event, venue, format, category, price, and registration details.", Effect: EffectRead},
			{ID: "get_organizer", Label: "Read organizer details", Description: "Read public organizer and organizer-event information.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Grant fetch_url to the selected agent; web_search is optional for broader discovery.", "Assign the event-finder skill.", "Ask the agent to search Eventbrite or give it a public event URL.", "Optionally connect a reviewed Eventbrite adapter for organizer or attendee workflows."},
		Checkout:    "Open the public Eventbrite event URL for ticket selection and checkout.",
		Limitations: []string{"Final fees and registration questions may appear only in the interactive checkout flow.", "Organizer management and attendee data require separate authenticated access and careful agent scoping."},
	},
	{
		ID: "open-food-facts", Name: "Open Food Facts", Provider: "Open Food Facts", Category: "shopping",
		Summary: "Look up packaged foods, ingredients, nutrition, allergens, labels, and product images by barcode.",
		Status:  StatusRecipe, AuthRequirement: AuthNone, AuthType: "No provider account",
		DocsURL:      "https://openfoodfacts.github.io/openfoodfacts-server/api/",
		AdapterKinds: []string{"web", "mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		RecommendedSkills: []string{"food-product-check"},
		Capabilities: []Capability{
			{ID: "get_product", Label: "Look up a barcode", Description: "Read product, ingredient, nutrition, allergen, and label data.", Effect: EffectRead},
			{ID: "search_products", Label: "Search foods", Description: "Find products using the documented product search API.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Grant fetch_url to the selected agent.", "Assign the food-product-check skill.", "Give the agent a barcode or public Open Food Facts product URL.", "Optionally connect a reviewed adapter for a richer structured tool interface."},
		Checkout:    "Product data only. This connector does not provide checkout.",
		Limitations: []string{"Data is community-contributed and may be incomplete.", "Provider usage and rate guidance still applies without an API key."},
	},
}
