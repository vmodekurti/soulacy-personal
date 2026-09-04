package credentials

// Config holds credential vault configuration.
type Config struct {
	// KMSProvider selects the key management backend.
	// Supported values: "local" (default), "hashicorp", "awskms".
	KMSProvider string

	// HashiCorpAddr is the address of a HashiCorp Vault server.
	// Used when KMSProvider is "hashicorp".
	HashiCorpAddr string

	// HashiCorpToken is the authentication token for HashiCorp Vault.
	// Used when KMSProvider is "hashicorp".
	HashiCorpToken string

	// AWSKMSKeyID is the AWS KMS key ID or ARN.
	// Used when KMSProvider is "awskms".
	AWSKMSKeyID string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		KMSProvider: "local",
	}
}
