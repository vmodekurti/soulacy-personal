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
	AuthType          string       `json:"auth_type"`
	DocsURL           string       `json:"docs_url"`
	SignupURL         string       `json:"signup_url,omitempty"`
	AdapterKinds      []string     `json:"adapter_kinds"`
	DeploymentTargets []string     `json:"deployment_targets"`
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
		ID: "ebay-browse", Name: "eBay Shopping", Provider: "eBay", Category: "shopping",
		Summary: "Search listings, compare prices and shipping, and inspect item details through the official Browse API.",
		Status:  StatusRecipe, AuthType: "OAuth 2 client credentials",
		DocsURL:      "https://developer.ebay.com/api-docs/buy/api-browse.html",
		SignupURL:    "https://developer.ebay.com/signin",
		AdapterKinds: []string{"mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		Credentials: []Credential{
			{Name: "EBAY_CLIENT_ID", Label: "Client ID", Required: true},
			{Name: "EBAY_CLIENT_SECRET", Label: "Client secret", Required: true},
		},
		Capabilities: []Capability{
			{ID: "search_products", Label: "Search products", Description: "Find listings by keyword, category, GTIN, product, or item attributes.", Effect: EffectRead},
			{ID: "get_product", Label: "Get item details", Description: "Read price, shipping, seller, return policy, availability, and purchase URL.", Effect: EffectRead},
			{ID: "check_compatibility", Label: "Check compatibility", Description: "Check whether a listing is compatible with a specified product or vehicle.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Create an eBay developer application.", "Store the client ID and client secret in Soulacy Secrets.", "Connect an eBay MCP server or plugin and test its read-only tools.", "Grant only the required tools to selected agents."},
		Checkout:    "Open the provider purchase URL for the user to review and complete checkout.",
		Limitations: []string{"Marketplace coverage varies by API and country.", "Soulacy does not place orders through this recipe."},
	},
	{
		ID: "best-buy-products", Name: "Best Buy Products", Provider: "Best Buy", Category: "shopping",
		Summary: "Search products, prices, availability, stores, categories, and recommendations through Best Buy's developer APIs.",
		Status:  StatusRecipe, AuthType: "API key",
		DocsURL:      "https://bestbuyapis.github.io/api-documentation/",
		SignupURL:    "https://developer.bestbuy.com/",
		AdapterKinds: []string{"mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		Credentials: []Credential{{Name: "BESTBUY_API_KEY", Label: "API key", Required: true}},
		Capabilities: []Capability{
			{ID: "search_products", Label: "Search products", Description: "Search the product catalog and compare current prices and attributes.", Effect: EffectRead},
			{ID: "get_product", Label: "Get product details", Description: "Read specifications, descriptions, images, reviews, and provider links.", Effect: EffectRead},
			{ID: "find_stores", Label: "Find stores", Description: "Find stores and inspect store-specific availability where supported.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Request a Best Buy developer API key.", "Store the API key in Soulacy Secrets.", "Connect a Best Buy MCP server or plugin and verify product lookup.", "Grant its read tools to selected agents."},
		Checkout:    "Open the Best Buy product or add-to-cart URL for the user to finish checkout.",
		Limitations: []string{"Provider terms limit caching and some media use cases.", "Order APIs require a separate partner relationship."},
	},
	{
		ID: "etsy-open-api", Name: "Etsy Marketplace", Provider: "Etsy", Category: "shopping",
		Summary: "Discover marketplace listings and shops through Etsy Open API v3.",
		Status:  StatusRecipe, AuthType: "OAuth 2 authorization code",
		DocsURL:      "https://developers.etsy.com/documentation/",
		SignupURL:    "https://www.etsy.com/developers/register",
		AdapterKinds: []string{"mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		Credentials: []Credential{
			{Name: "ETSY_API_KEY", Label: "API key", Required: true},
			{Name: "ETSY_SHARED_SECRET", Label: "Shared secret", Required: true},
		},
		Capabilities: []Capability{
			{ID: "search_listings", Label: "Search listings", Description: "Find active listings using the official marketplace API.", Effect: EffectRead},
			{ID: "get_listing", Label: "Get listing details", Description: "Read listing, price, image, shop, and taxonomy information.", Effect: EffectRead},
			{ID: "get_shop", Label: "Get shop details", Description: "Read public shop and seller information.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Register an Etsy application.", "Store the application key and shared secret in Soulacy Secrets.", "Connect an OAuth-capable Etsy MCP server or plugin.", "Complete account authorization, test it, and grant selected read tools to agents."},
		Checkout:    "Open the Etsy listing URL for the user to review and complete checkout.",
		Limitations: []string{"OAuth redirect handling must be provided by the adapter.", "Soulacy does not submit purchases through this recipe."},
	},
	{
		ID: "ticketmaster-discovery", Name: "Ticketmaster Events", Provider: "Ticketmaster", Category: "events",
		Summary: "Find events, attractions, venues, dates, availability, and ticket links through the official Discovery API.",
		Status:  StatusRecipe, AuthType: "API key",
		DocsURL:      "https://developer.ticketmaster.com/products-and-docs/apis/discovery-api/v2/",
		SignupURL:    "https://developer.ticketmaster.com/",
		AdapterKinds: []string{"mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		Credentials: []Credential{{Name: "TICKETMASTER_API_KEY", Label: "Consumer key", Required: true}},
		Capabilities: []Capability{
			{ID: "search_events", Label: "Search events", Description: "Find events by location, date, attraction, venue, genre, or keyword.", Effect: EffectRead},
			{ID: "get_event", Label: "Get event details", Description: "Read event, venue, location, image, status, and purchase-link details.", Effect: EffectRead},
			{ID: "search_venues", Label: "Search venues", Description: "Find venues and attractions in supported markets.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Create a Ticketmaster developer account and application.", "Store the consumer key in Soulacy Secrets.", "Connect a Ticketmaster MCP server or plugin and test event search.", "Grant only the desired discovery tools to selected agents."},
		Checkout:    "Open the event's Ticketmaster URL for the user to select tickets and complete checkout.",
		Limitations: []string{"The open Discovery API does not transact ticket orders.", "Inventory and commerce APIs require partner access."},
	},
	{
		ID: "eventbrite-organizer", Name: "Eventbrite Organizer", Provider: "Eventbrite", Category: "events",
		Summary: "Read organizations, events, venues, attendees, and organizer data for an authorized Eventbrite account.",
		Status:  StatusRecipe, AuthType: "OAuth 2 or private token",
		DocsURL:      "https://www.eventbrite.com/platform/new/api",
		SignupURL:    "https://www.eventbrite.com/platform/",
		AdapterKinds: []string{"mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		Credentials: []Credential{{Name: "EVENTBRITE_PRIVATE_TOKEN", Label: "Private token", Required: true}},
		Capabilities: []Capability{
			{ID: "list_events", Label: "List organizer events", Description: "Read events owned by an authorized organization or user.", Effect: EffectRead},
			{ID: "get_event", Label: "Get event details", Description: "Read event, venue, format, category, and ticket-class details.", Effect: EffectRead},
			{ID: "list_attendees", Label: "List attendees", Description: "Read attendee data when the authorized account and scopes permit it.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Create an Eventbrite API key or app.", "Store the private token in Soulacy Secrets.", "Connect an Eventbrite MCP server or plugin with the minimum OAuth scopes.", "Test account access and grant selected tools only to the agents that need them."},
		Checkout:    "Open the public Eventbrite event URL for ticket selection and checkout.",
		Limitations: []string{"This recipe centers on authorized organizer data, not unrestricted public event search.", "Attendee data needs careful agent scoping."},
	},
	{
		ID: "open-food-facts", Name: "Open Food Facts", Provider: "Open Food Facts", Category: "shopping",
		Summary: "Look up packaged foods, ingredients, nutrition, allergens, labels, and product images by barcode.",
		Status:  StatusRecipe, AuthType: "No key for read access",
		DocsURL:      "https://openfoodfacts.github.io/openfoodfacts-server/api/",
		AdapterKinds: []string{"mcp", "plugin"}, DeploymentTargets: []string{"local", "managed"},
		Capabilities: []Capability{
			{ID: "get_product", Label: "Look up a barcode", Description: "Read product, ingredient, nutrition, allergen, and label data.", Effect: EffectRead},
			{ID: "search_products", Label: "Search foods", Description: "Find products using the documented product search API.", Effect: EffectRead},
		},
		SetupSteps:  []string{"Connect an Open Food Facts MCP server or plugin.", "Set an identifying User-Agent as required by the provider.", "Test barcode lookup and grant the read tools to selected agents."},
		Checkout:    "Product data only. This connector does not provide checkout.",
		Limitations: []string{"Data is community-contributed and may be incomplete.", "Provider usage and rate guidance still applies without an API key."},
	},
}
