package runtime

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

// build_agent belongs to Genie. Every tool schema is charged against the same
// context window as the conversation, so a tool handed to every agent by
// default is context spent on something they will never call — and on a model
// whose window cannot be read, that is context taken from the work.
func TestBuildAgentIsOfferedOnlyToAgentsThatNameIt(t *testing.T) {
	e := &Engine{}
	e.builtins = e.buildBuiltins()

	// Genie names it, so Genie has it.
	genie := builtinGenieAgent()
	if !toolSchemaNameSet(e.allToolSchemas(genie, "http"))["build_agent"] {
		t.Error("Genie should be offered build_agent — it is Genie's way to build")
	}

	// An ordinary agent with no builtins list takes the wildcard default, and
	// the wildcard must not sweep this one in.
	ordinary := &agent.Definition{ID: "helper", Name: "Helper", SystemPrompt: "Help."}
	if toolSchemaNameSet(e.allToolSchemas(ordinary, "http"))["build_agent"] {
		t.Error("a wildcard builtins list should not pick up build_agent")
	}

	// Naming it explicitly is how any other agent opts in.
	named := []string{"build_agent"}
	optedIn := &agent.Definition{ID: "maker", Name: "Maker", SystemPrompt: "Build.", Builtins: &named}
	if !toolSchemaNameSet(e.allToolSchemas(optedIn, "http"))["build_agent"] {
		t.Error("an agent that names build_agent should be offered it")
	}
}

// Genie's builtin list and the tools the engine actually offers have to agree:
// a name in the list that the engine does not build is a tool Genie is told it
// has and cannot call.
func TestGenieBuiltinListMatchesRealTools(t *testing.T) {
	e := &Engine{}
	e.builtins = e.buildBuiltins()
	offered := toolSchemaNameSet(e.allToolSchemas(builtinGenieAgent(), "http"))

	for _, name := range *builtinGenieAgent().Builtins {
		switch name {
		// Both of these depend on something a bare engine has not got: a
		// channel registry, and at least one installed skill to read.
		case "channel.send", "channel.status", "read_skill", "read_skill_file":
			continue
		}
		if !offered[name] {
			t.Errorf("Genie's definition names %q but the engine does not offer it", name)
		}
	}
}
