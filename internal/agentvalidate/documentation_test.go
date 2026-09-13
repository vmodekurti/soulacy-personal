package agentvalidate

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
	"gopkg.in/yaml.v3"
)

// Keep downloadable tutorial definitions on the same validator as real saves.
// This is deliberately offline: placeholders are not provider compatibility tests.
func TestDocumentationExamples(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "examples")
	files, err := filepath.Glob(filepath.Join(root, "*", "SOUL.yaml"))
	if err != nil || len(files) != 2 {
		t.Fatalf("expected both tutorial definitions: files=%v err=%v", files, err)
	}
	for _, path := range files {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			report, err := File(path, Options{})
			if err != nil || !report.Valid {
				t.Fatalf("invalid documentation example: %+v, %v", report, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var def agent.Definition
			if err := yaml.Unmarshal(data, &def); err != nil {
				t.Fatal(err)
			}
			if !def.Enabled || def.Trigger != agent.TriggerChannel || !reflect.DeepEqual(def.Channels, []string{"http"}) {
				t.Fatalf("tutorial must remain an enabled HTTP chat agent: %+v", def)
			}
			if def.Builtins == nil || len(def.Tools) != 0 || len(def.Skills) != 0 || def.SystemTools || def.Unattended {
				t.Fatal("tutorial must keep an explicit narrow tool list and no privileged/unattended access")
			}
			for _, names := range []*[]string{def.MCPServers, def.MCPTools, def.PluginTools} {
				if names != nil && len(*names) != 0 {
					t.Fatal("tutorial must not grant external integrations")
				}
			}
			if def.ID == "notes-assistant" && len(*def.Builtins) != 0 {
				t.Fatal("notes tutorial must not call tools")
			}
			if def.ID == "handbook-helper" && !reflect.DeepEqual(*def.Builtins, []string{"kb_search"}) {
				t.Fatal("handbook tutorial must only search its knowledge base")
			}
		})
	}
	t.Run("documented timezone", func(t *testing.T) {
		if err := validateCronExpression("CRON_TZ=America/Chicago 0 8 * * 1-5"); err != nil {
			t.Fatal(err)
		}
	})
}
