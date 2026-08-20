package gateway

import (
	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

// schemastatus.go — MU-035 criterion 3's "observable" half, over HTTP.
//
//	GET /api/v1/admin/schema
//
// The question this answers is the one an operator has during a rolling
// upgrade and could not previously ask of the running system: has every
// component reached the version this binary expects? Every store recorded its
// version and nothing read it back, so the answer required opening each
// database file with a SQLite client — which is not something anybody does
// mid-upgrade.
//
// DEPLOYMENT-WIDE, NOT PER WORKSPACE, and gated accordingly. A schema version
// is a property of the installation rather than of a tenant, so this is not a
// scoped read; it is behind the same owner/admin gate as the raw metrics
// endpoint, for the same reason — it describes the deployment, and the answer
// does not vary by who asks.
//
// The response carries database BASE NAMES and component names, never full
// paths. Backup paths are the exception and are included deliberately: a
// snapshot an operator cannot locate is a snapshot they will not use, and the
// audience for this endpoint is the person who would restore it.

// SetSchemaDir tells the gateway where this installation's SQLite databases
// live, enabling GET /admin/schema.
func (s *Server) SetSchemaDir(dir string) {
	if s == nil {
		return
	}
	s.schemaDir = dir
}

func (s *Server) handleSchemaStatus(c *fiber.Ctx) error {
	if s.schemaDir == "" {
		// 503 rather than an empty list. An empty list means "every database
		// is at version zero", which is a specific and alarming claim; not
		// knowing where the databases are is a different thing and has to read
		// differently.
		return s.errMsg(c, fiber.StatusServiceUnavailable,
			"this deployment has no SQLite database directory configured, so schema state cannot be reported")
	}
	databases, err := sqlitex.ReportDir(s.schemaDir)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if databases == nil {
		databases = []sqlitex.DatabaseSchemaState{}
	}
	return c.JSON(fiber.Map{"databases": databases, "count": len(databases)})
}
