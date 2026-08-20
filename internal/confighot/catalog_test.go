// catalog_test.go — the classification must cover the real struct, and the
// claims in it must be checkable.
//
// Reflection over config.Config rather than a hand-kept list of names, because
// the failure this guards against is somebody ADDING a config section. A list
// cannot notice that; the struct is the only thing that knows.
package confighot

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/config"
)

func TestTheClassificationIsInternallyConsistent(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEverySectionOfTheRealConfigIsClassified(t *testing.T) {
	classified := map[string]Section{}
	for _, section := range Sections {
		classified[section.Field] = section
	}

	structType := reflect.TypeOf(config.Config{})
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if !field.IsExported() {
			continue
		}
		section, known := classified[field.Name]
		if !known {
			t.Errorf("config.Config.%s is not classified in confighot.Sections. Decide whether it is a "+
				"TENANT setting (which must apply live) or a PLATFORM setting (which may require a "+
				"restart, with a reason). Adding a config section without deciding is how the product "+
				"acquired thirty settings nobody could say the answer for", field.Name)
			continue
		}
		// The YAML key has to match, or an operator reading a message about
		// `channels` is being told about something else.
		tag := field.Tag.Get("mapstructure")
		if tag != "" && tag != section.Key {
			t.Errorf("config.Config.%s has mapstructure %q but is classified under key %q",
				field.Name, tag, section.Key)
		}
	}
}

func TestNoClassifiedSectionHasBeenRemovedFromTheConfig(t *testing.T) {
	// The other direction. An entry left behind after its section is deleted
	// is a stale claim that something is hot, and the next person reads it as
	// current.
	structType := reflect.TypeOf(config.Config{})
	real := map[string]bool{}
	for i := 0; i < structType.NumField(); i++ {
		if structType.Field(i).IsExported() {
			real[structType.Field(i).Name] = true
		}
	}
	for _, section := range Sections {
		if !real[section.Field] {
			t.Errorf("confighot classifies %q, which is no longer a field on config.Config", section.Field)
		}
	}
}

func TestNothingTenantScopedRequiresARestart(t *testing.T) {
	// Validate already refuses this, so the assertion here is that the rule is
	// stated where somebody looking for it will find it. It is the whole point
	// of the package and it should not be reachable only through a switch
	// statement's default case.
	for _, section := range Sections {
		if section.Scope == ScopeTenant && section.Apply == ApplyBoot {
			t.Errorf("%s is tenant-scoped and boot-only", section.Field)
		}
	}
}

func TestEveryLiveSectionNamesSomethingThatExists(t *testing.T) {
	// AppliedBy is a claim, and an unchecked claim is how the previous state
	// arose: a dozen messages said "restart" for settings that had been hot for
	// months, and the channel handlers said the same thing while genuinely
	// doing nothing.
	//
	// Checked by looking for the named symbol in the repository. Loose on
	// purpose — it cannot prove the function is CALLED on the reload path, only
	// that the name is not fiction. The reload path itself is covered by
	// TestReloadConfigAppliesEveryLiveSection in internal/gateway.
	root := filepath.Clean(filepath.Join("..", ".."))
	for _, section := range Sections {
		if section.Apply != ApplyLive {
			continue
		}
		for _, claimed := range section.AppliedBy {
			symbol := claimed
			// Prose entries describe a per-request read rather than a function.
			if strings.Contains(symbol, " ") {
				continue
			}
			if i := strings.LastIndex(symbol, "."); i >= 0 {
				symbol = symbol[i+1:]
			}
			if !symbolExists(t, root, symbol) {
				t.Errorf("section %q claims to be applied by %q, and no such symbol exists in the repository",
					section.Field, claimed)
			}
		}
	}
}

func symbolExists(t *testing.T, root, symbol string) bool {
	t.Helper()
	found := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "gui", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(source), "func "+symbol+"(") ||
			strings.Contains(string(source), ") "+symbol+"(") {
			found = true
		}
		return nil
	})
	return found
}

func TestBootOnlyReasonsAreArgumentsNotExcuses(t *testing.T) {
	// A reason is meant to survive somebody reading it and disagreeing. These
	// phrases are the ones that mean "unimplemented" while looking like an
	// explanation, and letting one through would make every other reason in
	// the file worth less.
	excuses := []string{
		"not implemented", "not yet", "todo", "nobody has", "no one has",
		"would be nice", "future work", "hard to", "difficult to", "complicated",
	}
	for _, section := range BootOnly() {
		lowered := strings.ToLower(section.Reason)
		for _, excuse := range excuses {
			if strings.Contains(lowered, excuse) {
				t.Errorf("section %q justifies a restart with %q, which describes the state of the "+
					"code rather than a property of the setting. Either wire it, or say what about "+
					"this setting makes a live change wrong", section.Field, excuse)
			}
		}
		if len(strings.Fields(section.Reason)) < 8 {
			t.Errorf("section %q has a %d-word reason; too short to be an argument",
				section.Field, len(strings.Fields(section.Reason)))
		}
	}
}
