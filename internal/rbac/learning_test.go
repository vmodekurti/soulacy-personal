package rbac

import "testing"

func TestLearningWritesAreOperatorActionsNotViewerActions(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleOperator} {
		if !HasPermission(role, ResourceMemory, ActionWrite) {
			t.Fatal(role, "cannot review learning")
		}
	}
	if HasPermission(RoleViewer, ResourceMemory, ActionWrite) {
		t.Fatal("viewer can alter behavior")
	}
	if HasPermission(RoleOperator, ResourceSkills, ActionWrite) {
		t.Fatal("private memory permission grants shared skill installation")
	}
}
