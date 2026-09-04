package gateway

import (
	"reflect"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestPreserveHiddenAgentUpdateFieldsCopiesMissingAdvancedFields(t *testing.T) {
	t.Parallel() // TEST-4: pure in-memory struct logic, no shared state.
	builtins := []string{"web_search"}
	mcpServers := []string{"github"}
	mcpTools := []string{"mcp__github__search_repositories"}
	existing := &agent.Definition{
		ID:         "advanced",
		Security:   &agent.SecurityConfig{Passphrase: "secret"},
		Builtins:   &builtins,
		MCPServers: &mcpServers,
		MCPTools:   &mcpTools,
		Workflow: &agent.WorkflowSpec{Steps: []agent.StepSpec{
			{ID: "step", Tool: "web_search"},
		}},
		NotifyOnFailure: &agent.NotifyOnFailure{
			Channel:      "telegram",
			To:           "123",
			IncludeError: true,
		},
		RunTimeout:   "30m",
		SystemTools:  true,
		ConfirmTools: []string{"shell_exec"},
		Labels:       map[string]string{"owner": "ops"},
		Agents:       []string{"researcher"},
		Knowledge:    []string{"kb"},
		Tags:         []string{"prod"},
	}
	updates := &agent.Definition{
		ID:           "advanced",
		Name:         "Edited Name",
		SystemPrompt: "Edited prompt",
	}

	preserveHiddenAgentUpdateFields(updates, existing)

	if updates.Security != existing.Security {
		t.Fatal("Security was not preserved")
	}
	if updates.Builtins != existing.Builtins {
		t.Fatal("Builtins was not preserved")
	}
	if updates.MCPServers != existing.MCPServers {
		t.Fatal("MCPServers was not preserved")
	}
	if updates.MCPTools != existing.MCPTools {
		t.Fatal("MCPTools was not preserved")
	}
	if updates.Workflow != existing.Workflow {
		t.Fatal("Workflow was not preserved")
	}
	if updates.NotifyOnFailure != existing.NotifyOnFailure {
		t.Fatal("NotifyOnFailure was not preserved")
	}
	if updates.RunTimeout != existing.RunTimeout {
		t.Fatalf("RunTimeout = %q, want %q", updates.RunTimeout, existing.RunTimeout)
	}
	if updates.SystemTools != existing.SystemTools {
		t.Fatalf("SystemTools = %v, want %v", updates.SystemTools, existing.SystemTools)
	}
	if !reflect.DeepEqual(updates.ConfirmTools, existing.ConfirmTools) {
		t.Fatalf("ConfirmTools = %v, want %v", updates.ConfirmTools, existing.ConfirmTools)
	}
	if !reflect.DeepEqual(updates.Labels, existing.Labels) {
		t.Fatalf("Labels = %v, want %v", updates.Labels, existing.Labels)
	}
	if !reflect.DeepEqual(updates.Agents, existing.Agents) {
		t.Fatalf("Agents = %v, want %v", updates.Agents, existing.Agents)
	}
	if !reflect.DeepEqual(updates.Knowledge, existing.Knowledge) {
		t.Fatalf("Knowledge = %v, want %v", updates.Knowledge, existing.Knowledge)
	}
	if !reflect.DeepEqual(updates.Tags, existing.Tags) {
		t.Fatalf("Tags = %v, want %v", updates.Tags, existing.Tags)
	}
}

func TestPreserveHiddenAgentUpdateFieldsKeepsExplicitAdvancedFields(t *testing.T) {
	t.Parallel() // TEST-4: pure in-memory struct logic, no shared state.
	oldBuiltins := []string{"web_search"}
	oldMCPServers := []string{"github"}
	oldMCPTools := []string{"mcp__github__search_repositories"}
	newBuiltins := []string{}
	newMCPServers := []string{}
	newMCPTools := []string{"mcp__filesystem__read_file"}

	existing := &agent.Definition{
		Security:        &agent.SecurityConfig{Passphrase: "old"},
		Builtins:        &oldBuiltins,
		MCPServers:      &oldMCPServers,
		MCPTools:        &oldMCPTools,
		Workflow:        &agent.WorkflowSpec{Steps: []agent.StepSpec{{ID: "old", Tool: "old"}}},
		NotifyOnFailure: &agent.NotifyOnFailure{Channel: "telegram", To: "old"},
		RunTimeout:      "30m",
		SystemTools:     true,
		ConfirmTools:    []string{"shell_exec"},
		Labels:          map[string]string{"owner": "ops"},
		Agents:          []string{"researcher"},
		Knowledge:       []string{"kb"},
		Tags:            []string{"prod"},
	}
	updates := &agent.Definition{
		Security:        &agent.SecurityConfig{Passphrase: "new"},
		Builtins:        &newBuiltins,
		MCPServers:      &newMCPServers,
		MCPTools:        &newMCPTools,
		Workflow:        &agent.WorkflowSpec{Steps: []agent.StepSpec{{ID: "new", Tool: "new"}}},
		NotifyOnFailure: &agent.NotifyOnFailure{Channel: "slack", To: "new"},
		RunTimeout:      "5m",
		SystemTools:     true,
		ConfirmTools:    []string{"write_file"},
		Labels:          map[string]string{"owner": "dev"},
		Agents:          []string{"critic"},
		Knowledge:       []string{"docs"},
		Tags:            []string{"draft"},
	}

	preserveHiddenAgentUpdateFields(updates, existing)

	if updates.Security.Passphrase != "new" {
		t.Fatalf("Security was overwritten: %+v", updates.Security)
	}
	if updates.Builtins != &newBuiltins {
		t.Fatal("explicit Builtins pointer was overwritten")
	}
	if updates.MCPServers != &newMCPServers {
		t.Fatal("explicit MCPServers pointer was overwritten")
	}
	if updates.MCPTools != &newMCPTools {
		t.Fatal("explicit MCPTools pointer was overwritten")
	}
	if updates.Workflow.Steps[0].ID != "new" {
		t.Fatalf("Workflow was overwritten: %+v", updates.Workflow)
	}
	if updates.NotifyOnFailure.Channel != "slack" {
		t.Fatalf("NotifyOnFailure was overwritten: %+v", updates.NotifyOnFailure)
	}
	if updates.RunTimeout != "5m" {
		t.Fatalf("RunTimeout = %q, want 5m", updates.RunTimeout)
	}
	if !reflect.DeepEqual(updates.ConfirmTools, []string{"write_file"}) {
		t.Fatalf("ConfirmTools overwritten: %v", updates.ConfirmTools)
	}
	if !reflect.DeepEqual(updates.Labels, map[string]string{"owner": "dev"}) {
		t.Fatalf("Labels overwritten: %v", updates.Labels)
	}
	if !reflect.DeepEqual(updates.Agents, []string{"critic"}) {
		t.Fatalf("Agents overwritten: %v", updates.Agents)
	}
	if !reflect.DeepEqual(updates.Knowledge, []string{"docs"}) {
		t.Fatalf("Knowledge overwritten: %v", updates.Knowledge)
	}
	if !reflect.DeepEqual(updates.Tags, []string{"draft"}) {
		t.Fatalf("Tags overwritten: %v", updates.Tags)
	}
}

func TestPreserveHiddenAgentUpdateFieldsAllowsClearingConfirmTools(t *testing.T) {
	t.Parallel()
	existing := &agent.Definition{ConfirmTools: []string{"shell_exec"}}
	updates := &agent.Definition{ConfirmTools: []string{}}
	preserveHiddenAgentUpdateFields(updates, existing)
	if updates.ConfirmTools == nil {
		t.Fatal("explicit empty ConfirmTools should remain an empty slice, not nil")
	}
	if len(updates.ConfirmTools) != 0 {
		t.Fatalf("ConfirmTools = %v, want empty", updates.ConfirmTools)
	}
}

func TestPreserveHiddenAgentUpdateFieldsNilSafe(t *testing.T) {
	t.Parallel() // TEST-4: pure in-memory struct logic, no shared state.
	preserveHiddenAgentUpdateFields(nil, &agent.Definition{})
	preserveHiddenAgentUpdateFields(&agent.Definition{}, nil)
}
