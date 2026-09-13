package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// RevalidateCredential supports bounded multi-step external mutations. In
// particular a managed key revoked after admission must not authorize the
// next write. It uses the same verifier order as Middleware, without Next().
func (e *Engine) RevalidateCredential(ctx context.Context, token string) (*Claims, error) {
	if e == nil || ctx.Err() != nil {
		return nil, errors.New("credential unavailable")
	}
	if e.staticKey != "" && secretEqual(token, e.staticKey) {
		return &Claims{Email: "admin", Role: "admin", Kind: "access"}, nil
	}
	if e.apiKeyStore != nil && strings.HasPrefix(token, "sk_") {
		if ak, err := e.apiKeyStore.Validate(ctx, token); err == nil {
			return &Claims{RegisteredClaims: jwt.RegisteredClaims{Subject: ak.ID}, Email: ak.Name, Role: "operator", Kind: "access", Scopes: ak.Scopes}, nil
		}
	}
	if e.issuer != nil {
		if cl, err := e.issuer.VerifyAccess(token); err == nil {
			return cl, nil
		}
	}
	if e.oidc != nil {
		if cl, err := e.oidc.Validate(token); err == nil {
			return cl, nil
		}
	}
	return nil, errors.New("credential is no longer valid")
}
