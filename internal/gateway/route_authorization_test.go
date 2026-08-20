// route_authorization_test.go — MU-030 criterion 2, second half: "UI controls
// are hidden or disabled for unauthorized users, while the API independently
// enforces every action."
//
// The GUI half is cosmetic by nature — a hidden button is a hidden button, not
// a boundary, and anyone can issue the request the button would have issued.
// The half that is actually load-bearing is the one this guard checks: that
// every state-changing route decides for itself.
//
// It is written the other way round from a permission test. Per-route tests
// prove the routes that exist today are gated; they cannot prove the next one
// will be, and a new `api.Post(...)` with no middleware is one line that reads
// exactly like the twenty around it. This fails the build instead.
package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// authorizationMiddleware are the calls that constitute a route deciding for
// itself who may reach it.
var authorizationMiddleware = map[string]bool{
	"rbacMW": true, "rbacAgentMW": true, "rbacAgentFromMW": true,
	// platformMW is authorization, and stricter than rbacMW rather than
	// looser: in personal mode it delegates to rbacMW unchanged, and in
	// multi-user it requires the deployment's own credential, which no
	// workspace role can obtain.
	"platformMW":                true,
	"denyGlobalCredentialScope": true,
	"adminOnlyMW":               true, "requirePluginScope": true,
	// requireRecentAuth is deliberately ABSENT. It is a freshness check, not
	// an authorization one: it asks "is this still you", never "may you". A
	// route carrying only recent-auth would be reachable by any authenticated
	// principal who had just re-authenticated, which is everybody.
}

// selfAuthorizingHandlers decide inside the handler rather than in middleware,
// each for a stated reason. An entry here is a claim a reviewer can check
// against the named handler; the absence of an entry is the build failing.
var selfAuthorizingHandlers = map[string]string{
	// These deliberately bypass rbacMW so they use the FRESHLY RESOLVED role
	// rather than one embedded in an older access token — see the comment
	// above their registration. They call membershipAdmin(c), which reads the
	// role the request just resolved, and the store additionally refuses role
	// escalation and removal of the last owner.
	"handleSetWorkspaceMemberRole":    "membershipAdmin(c) on the freshly resolved role, plus store-level escalation refusal",
	"handleSetWorkspaceMemberStatus":  "membershipAdmin(c) on the freshly resolved role",
	"handleRemoveWorkspaceMember":     "membershipAdmin(c) on the freshly resolved role, plus ErrLastOwner",
	"handleCreateWorkspaceInvitation": "membershipAdmin(c) plus tenancy.CanAdministerRole, on the freshly resolved role",
	"handleCreateWorkspace":           "requires the freshly resolved owner role and a human principal; the store rechecks active ownership transactionally",
	// Selecting a workspace is not privileged: it resolves membership and
	// refuses with a 404 when there is none, so it grants nothing an
	// authorization check would need to narrow.
	"handleSelectWorkspace": "resolves membership itself and 404s without one; grants nothing",
	// Step-up is the thing that produces authority; gating it on authority
	// would be circular. It requires an authenticated session and refuses a
	// subject mismatch.
	"HandleReauthenticate": "requires an authenticated session and refuses a subject mismatch; gating step-up on authority is circular",
	// The pairing CODE is the capability, by design: minting one requires
	// config:write, and it is single-use and short-lived. What redeem must not
	// do is issue an UNATTRIBUTED credential, which is what the unscoped
	// store.Create hard-coded to ws_personal did — see mintPairedCredential,
	// which binds the credential to the redeemer's verified workspace in
	// multi-user mode and refuses without one.
	"handleRedeemPairingToken": "the single-use pairing code is the capability; the credential it issues is bound to the redeemer's verified workspace",
}

// mutatingRegistrations are the Fiber methods that change state.
var mutatingRegistrations = map[string]bool{
	"Post": true, "Put": true, "Patch": true, "Delete": true,
}

func TestEveryMutatingRouteAuthorizesItself(t *testing.T) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		method, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !mutatingRegistrations[method.Sel.Name] {
			return true
		}
		group, ok := method.X.(*ast.Ident)
		// Only the authenticated /api/v1 group. Routes registered on `app`
		// (login, refresh, logout, share links, health) are public by design
		// and are a different question.
		if !ok || group.Name != "api" {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		path := literalString(call.Args[0])

		guarded := false
		for _, arg := range call.Args[1:] {
			ast.Inspect(arg, func(inner ast.Node) bool {
				if typed, ok := inner.(*ast.CallExpr); ok {
					if sel, ok := typed.Fun.(*ast.SelectorExpr); ok && authorizationMiddleware[sel.Sel.Name] {
						guarded = true
					}
				}
				return true
			})
		}
		// The handler is the LAST argument. Taking the first named selector
		// instead reported whichever middleware came first, which made the
		// failure message name a function that has nothing to do with the
		// route — and made the exemption list impossible to match against.
		handler := handlerName(call.Args[len(call.Args)-1])
		if guarded {
			return true
		}
		if reason, allowed := selfAuthorizingHandlers[handler]; allowed {
			seen[handler] = true
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s is exempt with no reason", handler)
			}
			return true
		}
		t.Errorf("%s: %s %s reaches %s with no authorization middleware — add rbacMW(...), or list the handler in "+
			"selfAuthorizingHandlers with the check it performs itself",
			fileSet.Position(call.Pos()), strings.ToUpper(method.Sel.Name), path, handlerOrUnknown(handler))
		return true
	})

	// A stale exemption is an exemption waiting to be inherited by whatever
	// takes that handler's name next.
	for handler := range selfAuthorizingHandlers {
		if !seen[handler] {
			t.Errorf("selfAuthorizingHandlers names %s, which no longer serves an unguarded mutating route — remove it", handler)
		}
	}
}

// handlerName digs the handler's identifier out of the final argument, which
// may be `s.handleX`, `s.handleX(true)` (a closure factory), or an inline
// func literal.
func handlerName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.SelectorExpr:
		return typed.Sel.Name
	case *ast.CallExpr:
		// A late-wiring wrapper resolves its dependency at request time and
		// returns the REAL handler from a closure — see latewiring.go. Naming
		// the wrapper would collapse every wrapped route onto one identity, so
		// one exemption would silently cover all of them. Look inside for the
		// handler actually being served.
		if inner := wrappedHandlerName(typed); inner != "" {
			return inner
		}
		return handlerName(typed.Fun)
	case *ast.Ident:
		return typed.Name
	}
	return ""
}

// wrappedHandlerName pulls HandleX out of
// requireAuthEngine(func(s *Server) fiber.Handler { return s.authEngine.HandleX }).
func wrappedHandlerName(call *ast.CallExpr) string {
	if len(call.Args) != 1 {
		return ""
	}
	lit, ok := call.Args[0].(*ast.FuncLit)
	if !ok || lit.Body == nil || len(lit.Body.List) != 1 {
		return ""
	}
	ret, ok := lit.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return ""
	}
	return handlerName(ret.Results[0])
}

func handlerOrUnknown(name string) string {
	if name == "" {
		return "an inline handler"
	}
	return name
}

func literalString(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "?"
	}
	return strings.Trim(lit.Value, `"`)
}
