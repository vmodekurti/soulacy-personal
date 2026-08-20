package gateway

import (
	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/apiversion"
	"github.com/soulacy/soulacy/pkg/agent"
)

// agent_write_guard.go — MU-028 criterion 2, applied to every agent-mutating
// handler rather than to the two that happened to get it first.
//
// The survey that produced this file is worth recording, because six of the
// thirteen agent-mutating call sites turned out to need nothing:
//
//   already checked          handleUpdateAgent, handleUpdateAgentYAML
//   structurally safe        handleCloneAgent and handleInstantiateTemplate
//                            allocate a free ID before writing (uniqueAgentID /
//                            templates.uniqueID), so they cannot land on an
//                            existing agent at all; ensurePeerAgents skips any
//                            peer that already resolves; setAgentEnabled is a
//                            read-modify-write of the *live* definition rather
//                            than of a form the client is holding, so it can
//                            only ever change Enabled — there is no stale copy
//                            to lose.
//   needed covering          handleCreateAgent, handleBuilderDeploy,
//                            handleStudioSave, handleStudioSaveYAML,
//                            handleDeleteAgent, handleRollbackAgent,
//                            handleImportAgentPackage
//
// Two of those seven are the interesting ones, and neither is an "update"
// handler:
//
//   - handleCreateAgent calls Upsert. POST /agents with an ID that already
//     exists therefore *replaced* that agent and answered 201 Created. Two
//     members both creating "support-bot" is not an exotic race; it is the
//     ordinary way two people name the same thing.
//   - handleBuilderDeploy is the same write with the ID chosen by a model
//     from a natural-language description, which makes collision more likely
//     rather than less.
//
// That is why the guard is split in two rather than being one function with a
// boolean. A create and an update fail for different reasons and the remedies
// differ: "pick another ID" is useless to someone editing an agent, and
// "reload and merge" is useless to someone who did not know the agent existed.

// guardAgentUpdate protects a write whose contract is "change this agent".
//
// The caller must already have resolved `existing` and answered 404 itself:
// whether an absent agent is a 404 differs by handler (Studio's save creates
// it), and folding that in would make the guard decide something it cannot
// see.
//
// Returns rejected=true once it has written the response, for the same reason
// checkIfMatch does — Fiber's Status().JSON() returns nil, so a handler that
// only checked the error would apply the write anyway.
func (s *Server) guardAgentUpdate(c *fiber.Ctx, existing *agent.Definition) (rejected bool, err error) {
	if existing == nil {
		// Nothing to overwrite, so nothing can be lost. A handler that reaches
		// here with nil is creating, and creation is guarded by the other half.
		return false, nil
	}
	return s.checkIfMatch(c, existing)
}

// guardAgentCreate protects a write whose contract is "make a new agent".
//
// A create that finds the ID taken has two honest answers and they are not
// the same answer an update gets:
//
//   - choose a different ID, which is what the caller almost always meant; or
//   - say explicitly that you are replacing what is there, by sending the
//     If-Match of the definition you looked at.
//
// So a supplied precondition is always honoured — sending a *stale* If-Match
// on a create still loses, because otherwise versioning would be advisory on
// exactly the path where a client is least likely to be careful.
//
// With no precondition at all the behaviour splits by deployment mode, the
// same split checkIfMatch makes and for the same reason. A personal install
// has nobody to collide with; re-creating an agent ID there is a single user
// overwriting their own work knowingly, and product invariant 7 says that
// keeps working byte-identically. A multi-user deployment refuses, because
// there the other definition belongs to somebody else.
func (s *Server) guardAgentCreate(c *fiber.Ctx, existing *agent.Definition) (rejected bool, err error) {
	if existing == nil {
		return false, nil
	}
	etag := resourceETag(existing)
	if supplied := c.Get(fiber.HeaderIfMatch); supplied != "" {
		// An explicit replace. Fall through to the shared policy so a stale
		// token is caught here exactly as it would be on an update.
		return s.checkIfMatch(c, existing)
	}
	if !s.authorizationRequired() {
		c.Set(fiber.HeaderETag, etag)
		return false, nil
	}
	c.Set(fiber.HeaderETag, etag)
	return true, c.Status(fiber.StatusConflict).JSON(fiber.Map{
		"error":            "an agent with this id already exists in this workspace",
		"code":             apiversion.CodeStaleWrite,
		"remedy":           "choose a different id, or send the existing agent's ETag in If-Match to replace it deliberately",
		"etag":             etag,
		"current_version":  etag,
		"supplied_version": "",
		"last_modified_by": s.lastEditorOf(c, existing),
		"last_modified_at": lastModifiedAt(existing),
	})
}
