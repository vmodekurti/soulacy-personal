package gateway

import (
	"io"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/skills"
)

func TestAgenticSkillRawURLsFallsBackToAdvertisedBranch(t *testing.T) {
	html := []byte(`<a href="https://github.com/acme/browser/blob/deadbeef/skills/browser/SKILL.md">pinned</a>
<a href="https://github.com/acme/browser/tree/main/skills/browser">current</a>`)
	want := []string{
		"https://raw.githubusercontent.com/acme/browser/deadbeef/skills/browser/SKILL.md",
		"https://raw.githubusercontent.com/acme/browser/main/skills/browser/SKILL.md",
	}
	if got := agenticSkillRawURLs(html); !reflect.DeepEqual(got, want) {
		t.Fatalf("raw URLs = %#v, want %#v", got, want)
	}
}

func TestWritableSkillsDirUsesRequestWorkspace(t *testing.T) {
	base := filepath.Join(t.TempDir(), "skills")
	s := &Server{skillStores: skills.NewStores(nil, base, zap.NewNop())}
	app := fiber.New(fiber.Config{Immutable: true})
	app.Get("/", func(c *fiber.Ctx) error {
		identity, err := requestctx.New(requestctx.Input{
			Subject: "usr_admin", OrganizationID: "org_otg", WorkspaceID: "ws_otg",
			MembershipID: "mem_admin", Role: "admin", RequestID: "req_install",
		})
		if err != nil {
			return err
		}
		c.SetUserContext(requestctx.With(c.UserContext(), identity))
		dir, err := s.writableSkillsDir(c)
		if err != nil {
			return err
		}
		return c.SendString(dir)
	})

	resp, err := app.Test(httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	want := s.skillStores.Dir("ws_otg")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != want {
		t.Fatalf("workspace dir = %q, want %q", got, want)
	}
	if personal := s.skillStores.Dir("ws_personal"); personal == want {
		t.Fatalf("workspace install dir leaked into personal workspace: %q", personal)
	}
}
