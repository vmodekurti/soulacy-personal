package config

// quota.go — YAML for the multi-level limits MU-024 introduces.
//
// One flat budget applied to every tenant is the shape this replaces. An
// operator could not give one customer a larger budget than another, could not
// express an organization ceiling above its workspaces, and could not cap a
// single expensive model. The existing flat keys stay and keep working: a
// personal deployment configures nothing here and behaves exactly as before.

// QuotaLimit is one configured ceiling. Omitted fields mean "not limited at
// this level", not "limited to zero".
type QuotaLimit struct {
	DailyUSD    float64 `mapstructure:"daily_usd"`
	MonthlyUSD  float64 `mapstructure:"monthly_usd"`
	DailyTokens int64   `mapstructure:"daily_tokens"`
	Concurrency int     `mapstructure:"concurrency"`
}

// QuotaConfig maps each level to per-entity limits.
//
// The `Default` entry in each map applies to every entity at that level with
// no specific entry — spelled as the empty key so a config file can write
// `workspaces: { "": {daily_usd: 5} }` for "every workspace gets $5" and
// override individual ones beside it.
//
// Every configured level is enforced; a narrower one can only restrict
// further. See docs/QUOTA_PRECEDENCE.md.
type QuotaConfig struct {
	Deployment    QuotaLimit            `mapstructure:"deployment"`
	Organizations map[string]QuotaLimit `mapstructure:"organizations"`
	Workspaces    map[string]QuotaLimit `mapstructure:"workspaces"`
	Principals    map[string]QuotaLimit `mapstructure:"principals"`
	Agents        map[string]QuotaLimit `mapstructure:"agents"`
	Models        map[string]QuotaLimit `mapstructure:"models"`
}
