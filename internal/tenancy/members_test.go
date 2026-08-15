package tenancy

import "testing"

func TestMembershipRoleAdministrationHierarchy(t *testing.T) {
	tests := []struct {
		actor, target string
		want          bool
	}{
		{RoleOwner, RoleOwner, true},
		{RoleOwner, RoleAdmin, true},
		{RoleAdmin, RoleDeveloper, true},
		{RoleAdmin, RoleAdmin, true},
		{RoleAdmin, RoleOwner, false},
		{RoleOperator, RoleViewer, false},
		{RoleViewer, RoleViewer, false},
		{"unknown", RoleViewer, false},
	}
	for _, test := range tests {
		if got := CanAdministerRole(test.actor, test.target); got != test.want {
			t.Errorf("CanAdministerRole(%q,%q)=%v want %v", test.actor, test.target, got, test.want)
		}
	}
}

func TestMembershipRolesAreClosedSet(t *testing.T) {
	for _, role := range []string{RoleOwner, RoleAdmin, RoleDeveloper, RoleOperator, RoleViewer} {
		if !IsMembershipRole(role) {
			t.Errorf("known role %q rejected", role)
		}
	}
	for _, role := range []string{"", "superadmin", "OWNER!"} {
		if IsMembershipRole(role) {
			t.Errorf("unknown role %q accepted", role)
		}
	}
}
