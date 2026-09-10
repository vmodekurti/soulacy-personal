package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"github.com/soulacy/soulacy/pkg/skill"
)

func TestGenieToolSurfaceIsDynamicAndNonPrivileged(t *testing.T) {
	e := newMinimalEngine(t)
	e.skillLoader = populatedSkillLoader{skills: []*skill.Skill{{Name: "web-audit", Description: "Audit a site"}}}
	e.builtins = e.buildBuiltins()
	genie := e.loader.Get(GenieAgentID)
	names := toolSchemaNameSet(e.allToolSchemasForContext(WithPrincipal(context.Background(), Principal{Role: "admin"}), genie, "http"))
	for _, want := range []string{"list_skills", "read_skill", "list_mcp_tools", "list_agents", "create_monitor", "list_monitors", "pause_monitor", "cancel_monitor"} {
		if !names[want] {
			t.Errorf("Genie missing %s", want)
		}
	}
	for _, forbidden := range []string{"shell_exec", "write_file", "package_install", "http_request", "env_get"} {
		if names[forbidden] {
			t.Errorf("Genie exposed privileged/host tool %s", forbidden)
		}
	}

	skillsOut, err := builtinByName(t, e.builtins, "list_skills").Handler(context.Background(), nil)
	if err != nil || !strings.Contains(skillsOut, "web-audit") {
		t.Fatalf("live skills = %q, %v", skillsOut, err)
	}
	e.loader.Register(&agent.Definition{ID: "researcher", Name: "Researcher", Enabled: true})
	agentsOut, err := builtinByName(t, e.builtins, "list_agents").Handler(context.Background(), nil)
	if err != nil || !strings.Contains(agentsOut, "researcher") {
		t.Fatalf("live agents = %q, %v", agentsOut, err)
	}
}

func TestGenieExternalWriteClassifier(t *testing.T) {
	for _, name := range []string{"mcp__bank__transfer_funds", "mcp__db__delete_row", "plugin__mail__send_message"} {
		if !highImpactExternalTool(name) {
			t.Errorf("write tool %q was not classified high-impact", name)
		}
	}
	for _, name := range []string{"mcp__bank__get_balance", "mcp__web__search", "plugin__docs__list_files", "shell_exec"} {
		if highImpactExternalTool(name) {
			t.Errorf("read/non-external tool %q was classified high-impact", name)
		}
	}
}
