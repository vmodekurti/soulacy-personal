// projection_test.go — MU-030 criterion 2: the served permission map must be
// the matrix, not a summary of it.
//
// The GUI hides controls from what /workspace/identity reports. That is only
// safe while the projection agrees with the policy the routes actually
// enforce, and the failure is quiet in both directions: a permission the
// projection omits hides a control the caller may use, and one it invents
// offers a control that always fails and reads as a server bug.
package rbac

import "testing"

func TestTheProjectionAgreesWithThePolicyItProjects(t *testing.T) {
	resources := Resources()
	actions := Actions()
	if len(resources) < 10 || len(actions) < 5 {
		// Without this the whole test passes vacuously the moment the
		// enumerators stop finding anything.
		t.Fatalf("the policy vocabulary looks empty: %d resources, %d actions", len(resources), len(actions))
	}
	for _, role := range KnownRoles {
		projected := PermissionsFor(role)
		for _, resource := range resources {
			granted := map[string]bool{}
			for _, action := range projected[resource] {
				granted[action] = true
			}
			for _, action := range actions {
				want := HasPermission(role, resource, action)
				if granted[action] != want {
					t.Errorf("role %s on %s:%s — projection says %v, policy says %v",
						role, resource, action, granted[action], want)
				}
			}
		}
	}
}

// An unknown role must project nothing. Returning a permissive default would
// make a typo in a membership row look like an upgrade.
func TestAnUnknownRoleProjectsNoPermissions(t *testing.T) {
	if got := PermissionsFor("wizard"); len(got) != 0 {
		t.Fatalf("an unknown role projected %v", got)
	}
	if got := PermissionsFor(""); len(got) != 0 {
		t.Fatalf("an empty role projected %v", got)
	}
}

// The enumerators read the policy rather than restating it, so a resource
// added to defaultPolicy is covered without anyone remembering to list it.
func TestTheVocabularyComesFromThePolicy(t *testing.T) {
	resources := map[string]bool{}
	for _, r := range Resources() {
		resources[r] = true
	}
	for role, byResource := range defaultPolicy {
		for resource := range byResource {
			if !resources[resource] {
				t.Errorf("%s appears in the policy for %s but not in Resources()", resource, role)
			}
		}
	}
}

// Roles are ordered by breadth in the docs; the projection should reflect that
// rather than quietly handing a viewer more than an operator. Stated as a
// property because it is the sort of thing a matrix edit breaks silently.
func TestAViewerCanDoNothingAnOwnerCannot(t *testing.T) {
	owner := PermissionsFor(RoleOwner)
	ownerHas := func(resource, action string) bool {
		for _, a := range owner[resource] {
			if a == action {
				return true
			}
		}
		return false
	}
	for _, role := range []string{RoleAdmin, RoleDeveloper, RoleOperator, RoleViewer} {
		for resource, actions := range PermissionsFor(role) {
			for _, action := range actions {
				if !ownerHas(resource, action) {
					t.Errorf("%s may %s:%s and the owner may not", role, resource, action)
				}
			}
		}
	}
}
