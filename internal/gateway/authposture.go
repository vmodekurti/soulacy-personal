package gateway

// authposture.go — what /ping says about whether anyone can get in.
//
// WHY THIS IS ITS OWN FUNCTION. The answer is not "is an API key set". It is
// "what would the middleware this process actually installed do with a request
// carrying no credential", and that middleware is chosen at build time between
// two implementations with different rules (see authHandler). The old /ping
// reimplemented one of the two from the config, inline, and so reported the
// wrong answer for every configuration the other one handles.
//
// Pulled out so it can be tested against each of those configurations directly,
// which an inline closure inside buildApp cannot be.

// Auth posture values reported by /ping.
const (
	// authPostureOpen: a request with NO credential succeeds.
	authPostureOpen = "open"
	// authPostureRequired: a credential is needed and one can be presented.
	authPostureRequired = "required"
	// authPostureUnreachable: a credential is needed and NOTHING can satisfy
	// it — every request is refused.
	//
	// Reported distinctly from "required" because the operator action differs
	// completely, and distinctly from "open" because reporting a locked-out
	// deployment as wide open is the most alarming direction to be wrong in:
	// it sends someone hunting an exposure that does not exist, and teaches
	// them the product's own security reporting cannot be trusted.
	authPostureUnreachable = "unreachable"
)

// authPosture reports how the installed authentication middleware would treat
// an unauthenticated request, along with the mode name and, when the answer is
// bad news, what to do about it.
func (s *Server) authPosture() (status, mode, detail string) {
	apiKey := s.config().Server.APIKey
	if s.authEngine == nil {
		// legacyAuthMiddleware: enforces the static key when there is one and
		// bypasses authentication entirely when there is not. This path is
		// tests and embedded gateways; production always calls SetAuth.
		if apiKey == "" {
			return authPostureOpen, "none", ""
		}
		return authPostureRequired, "apikey", ""
	}
	mode = s.authEngine.Mode()
	if mode == "" {
		mode = "apikey"
	}
	if s.authEngine.Reachable() {
		return authPostureRequired, mode, ""
	}
	// Reachable, not Effective. Effective is true for jwt mode with an
	// ephemeral issuer and nothing else configured — armed, but with no way
	// for anyone to obtain a token, so the middleware falls through every
	// branch and returns 401 on every request. The deployment is locked, not
	// open. See auth.Engine.Reachable.
	if mode == "jwt" {
		return authPostureUnreachable, mode, "auth.mode=jwt with no usable verifier " +
			"(no jwt_secret, no OIDC issuer, no managed API keys): every request will be refused. " +
			"Configure auth.jwt_secret or an OIDC issuer."
	}
	return authPostureUnreachable, mode, "apikey mode with no server.api_key configured: " +
		"every request will be refused. Set server.api_key, or configure auth.mode=jwt."
}
