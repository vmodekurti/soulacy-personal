package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/soulacy/soulacy/internal/auth/apikeys"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/requestctx"
	"github.com/soulacy/soulacy/internal/session"
	"github.com/soulacy/soulacy/internal/tenancy"
	"github.com/soulacy/soulacy/internal/workboard"
	"github.com/soulacy/soulacy/internal/workspaceexport"
	"github.com/soulacy/soulacy/pkg/agent"
)

// workspace_export.go — MU-032 criterion 2, the HTTP surface.
//
// ASYNCHRONOUS BY CONSTRUCTION, not as an optimisation. Exporting a workspace
// reads every resource class it owns; on anything past a demo that is minutes,
// and a synchronous route would be a request that reliably times out at a load
// balancer somewhere with the archive half-written and no record that it was
// ever asked for. So POST mints a job and answers 202, and the archive is
// fetched separately once it exists.
//
// WHY OWNER-ONLY AND NOT AN RBAC RESOURCE. Every other privileged route here
// is gated by rbac.Resource*/Action*, and export deliberately is not. An
// export is not an operation ON a resource class — it is a single file
// containing all of them, produced without the per-resource checks that
// normally stand between a role and a secret name or another member's
// conversations. Modelling that as "read on resource X" would make it look
// like the sum of permissions the caller already has, which is exactly what it
// is not. It is a workspace-lifecycle action, so it is gated the way the other
// one is: the workspace owner, with recent proof of identity.

const exportIDBytes = 16

// exportTTL is how long a produced archive stays downloadable. Deployment
// configuration is deliberately absent for now — see workspaceexport.DefaultTTL
// for why the value is short, and add a setting when somebody arrives with a
// reason rather than pre-emptively.
func exportTTL() time.Duration { return workspaceexport.DefaultTTL }

// workspaceOwner authorizes a workspace-lifecycle action.
//
// Owner only, and not owner-or-admin as membershipAdmin allows. An admin
// administers the workspace's membership and settings; taking a complete copy
// of its data out of the system is a different kind of authority, and the
// person accountable for it is the one who owns the workspace. Personal
// deployments resolve their single operator to owner, so this is not a new
// obstacle there (product invariant 7).
func (s *Server) workspaceOwner(c *fiber.Ctx) (requestctx.Identity, error) {
	identity, ok := requestIdentity(c)
	if !ok || identity.Role() != tenancy.RoleOwner {
		return requestctx.Identity{}, fiber.NewError(fiber.StatusForbidden, "only a workspace owner can export or delete a workspace")
	}
	return identity, nil
}

// exportStore resolves this deployment's export store.
//
// Rooted at the workspace data root, so wsroot places each tenant's exports in
// its own directory. Structural: a handler that lost its workspace ID resolves
// to the personal root and finds nothing, rather than listing every tenant's
// archives.
func (s *Server) exportStore() (*workspaceexport.Store, error) {
	ws, err := config.ResolveWorkspace()
	if err != nil {
		return nil, err
	}
	// The workspace ROOT, not a pre-joined exports directory: the store adds
	// "exports" beneath whichever workspace directory wsroot resolves, so
	// joining it here too produced <root>/exports/.workspaces/<ws>/exports.
	store := workspaceexport.NewStore(ws.Root)
	if s.workspaceLayout.Root() != "" {
		store.SetWorkspaceLayoutRoot(s.workspaceLayout.Root())
	}
	return store, nil
}

func newExportID() (string, error) {
	buf := make([]byte, exportIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// exportSources builds the per-resource emitters for one workspace.
//
// TAKES A WORKSPACE ID, NOT THE FIBER CONTEXT, and that is load-bearing rather
// than tidy. These closures run on a goroutine that outlives the request;
// Fiber recycles *fiber.Ctx into a pool the moment the handler returns, so a
// closure holding one would read another tenant's request. Everything the
// export needs is captured by value here, while the request is still alive.
//
// THE SET IS SMALLER THAN THE CATALOG, ON PURPOSE AND VISIBLY. A resource
// class with no emitter appears in the manifest as "not-exported" with its
// reason, because workspaceexport seeds the manifest from the ownership
// catalog rather than from this list. That is the property worth having: this
// function can be incomplete, and cannot be quietly incomplete.
func (s *Server) exportSources(organizationID, workspaceID string, definitions []*agent.Definition) []workspaceexport.Source {
	var sources []workspaceexport.Source

	// agents — emitted as a YAML stream because the catalog's export promise
	// for this class is "SOUL.yaml/package export", and a JSON array would be
	// a different artifact than the one the catalog tells customers they get.
	sources = append(sources, workspaceexport.Source{
		Resource: "agents",
		Filename: "agents.yaml",
		Emit: func(ctx context.Context, w io.Writer) (bool, error) {
			if len(definitions) == 0 {
				return false, nil
			}
			encoder := yaml.NewEncoder(w)
			for _, def := range definitions {
				if def == nil {
					continue
				}
				if err := encoder.Encode(def); err != nil {
					encoder.Close()
					return false, err
				}
			}
			return true, encoder.Close()
		},
	})
	if s.loader != nil {
		loader := s.loader
		sources = append(sources, workspaceexport.Source{
			Resource: "definitions",
			Filename: "definition-versions.jsonl",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				count, err := loader.ExportWorkspaceVersionsJSONL(ctx, workspaceID, w)
				return count > 0, err
			},
		})
	}

	if s.runStore != nil {
		store := s.runStore
		sources = append(sources, workspaceexport.Source{
			Resource: "runs",
			Filename: "runs.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				// Status "" is every status. A run export that carried only
				// finished runs would omit exactly the runs somebody exporting
				// during an incident is looking for.
				records, err := store.List(ctx, workspaceID, "", 0)
				if err != nil {
					return false, err
				}
				return encodeJSONList(w, records)
			},
		})
	}

	if store, ok := s.actions.(interface {
		ExportWorkspaceJSONL(context.Context, string, io.Writer) (int64, error)
	}); ok {
		sources = append(sources, workspaceexport.Source{
			Resource: "events",
			Filename: "events.jsonl",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				count, err := store.ExportWorkspaceJSONL(ctx, workspaceID, w)
				return count > 0, err
			},
		})
	}

	if s.workboardStore != nil {
		store := s.workboardStore
		sources = append(sources, workspaceexport.Source{
			Resource: "workboard",
			Filename: "workboard.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				tasks, err := store.List(ctx, workspaceID, workboard.Filter{})
				if err != nil {
					return false, err
				}
				return encodeJSONList(w, tasks)
			},
		})
	}

	if s.costStore != nil {
		store := s.costStore
		sources = append(sources, workspaceexport.Source{
			Resource: "costs",
			Filename: "costs.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				count, err := store.ExportWorkspaceJSON(ctx, workspaceID, w)
				return count > 0, err
			},
		})
	}

	if s.mcpServers != nil {
		store := s.mcpServers
		sources = append(sources, workspaceexport.Source{
			Resource: "mcp",
			Filename: "mcp-servers.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				servers, err := store.List(ctx, workspaceID)
				if err != nil {
					return false, err
				}
				// The store contains references and non-secret configuration;
				// credential values live in the vault and cannot enter this file.
				return encodeJSONList(w, servers)
			},
		})
	}

	if s.workspacePolicies != nil {
		store := s.workspacePolicies
		sources = append(sources, workspaceexport.Source{
			Resource: "workspace-policy",
			Filename: "workspace-policy.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				policy, err := store.Get(ctx, workspaceID)
				if err != nil {
					return false, err
				}
				if policy.UpdatedAt.IsZero() {
					return false, nil
				}
				return encodeJSONValue(w, policy)
			},
		})
	}
	if s.workspaceSettings != nil {
		store := s.workspaceSettings
		sources = append(sources, workspaceexport.Source{
			Resource: "workspace-settings", Filename: "workspace-settings.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				settings, err := store.Get(ctx, workspaceID)
				if err != nil {
					return false, err
				}
				if settings.UpdatedAt.IsZero() {
					return false, nil
				}
				return encodeJSONValue(w, settings)
			},
		})
	}

	if s.dlqStore != nil {
		store := s.dlqStore
		sources = append(sources, workspaceexport.Source{
			Resource: "queue-dlq",
			Filename: "dead-letters.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				letters, err := store.List(ctx, workspaceID, "")
				if err != nil {
					return false, err
				}
				return encodeJSONList(w, letters)
			},
		})
	}

	if s.approvalStore != nil {
		store := s.approvalStore
		sources = append(sources, workspaceexport.Source{
			Resource: "approvals",
			Filename: "approvals.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				records, err := store.ListWorkspace(ctx, workspaceID)
				if err != nil {
					return false, err
				}
				return encodeJSONList(w, records)
			},
		})
	}

	if s.scheduleStore != nil {
		store := s.scheduleStore
		sources = append(sources, workspaceexport.Source{
			Resource: "schedules",
			Filename: "schedules.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				records, err := store.ListWorkspace(ctx, workspaceID)
				if err != nil {
					return false, err
				}
				return encodeJSONList(w, records)
			},
		})
	}

	if s.knowledgeStore != nil {
		store := s.knowledgeStore
		sources = append(sources, workspaceexport.Source{
			Resource: "knowledge",
			Filename: "knowledge.jsonl",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				count, err := store.ExportWorkspaceJSONL(ctx, workspaceID, w)
				return count > 0, err
			},
		})
	}

	hotMemory, hotOK := s.memoryStore.(interface {
		ExportWorkspaceJSONL(context.Context, string, io.Writer) (int64, error)
	})
	archiveMemory, archiveOK := s.memoryArchive.(interface {
		ExportWorkspaceJSONL(context.Context, string, io.Writer) (int64, error)
	})
	if hotOK && archiveOK {
		sources = append(sources, workspaceexport.Source{
			Resource: "memory",
			Filename: "memory.jsonl",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				hotCount, err := hotMemory.ExportWorkspaceJSONL(ctx, workspaceID, w)
				if err != nil {
					return hotCount > 0, err
				}
				archiveCount, err := archiveMemory.ExportWorkspaceJSONL(ctx, workspaceID, w)
				return hotCount+archiveCount > 0, err
			},
		})
	}
	if s.knowledgeStore != nil || s.vectorMemory != nil {
		knowledgeVectors, memoryVectors := s.knowledgeStore, s.vectorMemory
		sources = append(sources, workspaceexport.Source{
			Resource: "vectors",
			Filename: "vector-metadata.jsonl",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				var total int64
				if knowledgeVectors != nil {
					count, err := knowledgeVectors.ExportVectorMetadataJSONL(ctx, workspaceID, w)
					total += count
					if err != nil {
						return total > 0, err
					}
				}
				if memoryVectors != nil {
					count, err := memoryVectors.ExportWorkspaceMetadataJSONL(ctx, workspaceID, w)
					total += count
					if err != nil {
						return total > 0, err
					}
				}
				return total > 0, nil
			},
		})
	}

	if store, ok := s.historyStore.(interface {
		ExportWorkspaceJSONL(context.Context, string, io.Writer) (int64, error)
	}); ok {
		sources = append(sources, workspaceexport.Source{
			Resource: "messages",
			Filename: "messages.jsonl",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				count, err := store.ExportWorkspaceJSONL(ctx, workspaceID, w)
				return count > 0, err
			},
		})
	}

	owners, ownersOK := s.sessionOwnership.(interface {
		ListWorkspace(context.Context, string) ([]session.Ownership, error)
	})
	attachments, attachmentsOK := s.resourceStore.(interface {
		ListWorkspaceAttachments(context.Context, string) ([]session.ExportedAttachment, error)
	})
	if ownersOK && attachmentsOK && s.checkpointStore != nil {
		checkpoints := s.checkpointStore
		sources = append(sources, workspaceexport.Source{
			Resource: "sessions",
			Filename: "sessions.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				ownership, err := owners.ListWorkspace(ctx, workspaceID)
				if err != nil {
					return false, err
				}
				files, err := attachments.ListWorkspaceAttachments(ctx, workspaceID)
				if err != nil {
					return false, err
				}
				steps, err := checkpoints.ListWorkspace(ctx, workspaceID)
				if err != nil {
					return false, err
				}
				if len(ownership) == 0 && len(files) == 0 && len(steps) == 0 {
					return false, nil
				}
				return encodeJSONValue(w, struct {
					Ownership   []session.Ownership          `json:"ownership"`
					Attachments []session.ExportedAttachment `json:"attachments"`
					Checkpoints any                          `json:"checkpoints"`
				}{
					Ownership:   ownership,
					Attachments: files,
					Checkpoints: steps,
				})
			},
		})
	}

	// Credential exports contain metadata only. Requiring ScopedLister keeps
	// another tenant's metadata out of memory as well as out of the archive;
	// a legacy unscoped store is reported as not-exported by the manifest.
	if store, ok := s.apiKeyStore.(apikeys.ScopedLister); ok {
		sources = append(sources, workspaceexport.Source{
			Resource: "api-keys",
			Filename: "api-keys.json",
			Emit: func(ctx context.Context, w io.Writer) (bool, error) {
				keys, err := store.ListForWorkspace(ctx, organizationID, workspaceID, true)
				if err != nil {
					return false, err
				}
				return encodeJSONList(w, keys)
			},
		})
	}

	return sources
}

// encodeJSONList writes a list, reporting "nothing here" rather than writing
// an empty array.
//
// The distinction reaches the customer: an empty `[]` in the archive and a
// manifest entry saying "exported" claims the workspace has no runs, which is
// a claim this code cannot make — it only knows the query returned none. The
// manifest's "empty" status says the same thing without the file that looks
// like evidence.
func encodeJSONList[T any](w io.Writer, list []T) (bool, error) {
	if len(list) == 0 {
		return false, nil
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(list); err != nil {
		return false, err
	}
	return true, nil
}

func encodeJSONValue(w io.Writer, value any) (bool, error) {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) registerWorkspaceExportRoutes(api fiber.Router) {
	// requireRecentAuth on the POST only. Listing and downloading are gated by
	// owner role and by holding the export's opaque ID; re-prompting to poll a
	// job status would teach an owner to keep a step-up window open for the
	// duration of an export, which defeats the window.
	api.Post("/workspace/export", s.requireRecentAuth(),
		s.auditing("workspace.export_requested", "workspace", "", s.handleRequestWorkspaceExport))
	api.Get("/workspace/export", s.handleListWorkspaceExports)
	api.Get("/workspace/export/:id", s.handleGetWorkspaceExport)
	api.Get("/workspace/export/:id/download",
		s.auditing("workspace.export_downloaded", "workspace", "id", s.handleDownloadWorkspaceExport))
}

func (s *Server) handleRequestWorkspaceExport(c *fiber.Ctx) error {
	identity, err := s.workspaceOwner(c)
	if err != nil {
		return err
	}
	store, err := s.exportStore()
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace exports are unavailable on this deployment")
	}
	id, err := newExportID()
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "an export identifier could not be minted")
	}
	workspaceID := identity.WorkspaceID()
	job, err := workspaceexport.NewJob(id, workspaceID, "", identity.Subject(), time.Now())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the export could not be created")
	}
	if err := store.Save(job); err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the export could not be recorded")
	}

	// Read everything that needs the request BEFORE detaching. s.agents(c)
	// reads locals off the Fiber context, which is recycled the instant this
	// handler returns.
	definitions := s.agents(c).All()
	sources := s.exportSources(identity.OrganizationID(), workspaceID, definitions)
	if err := workspaceexport.ValidateSources(sources); err != nil {
		// Fail loudly rather than producing an archive whose manifest silently
		// under-reports. A Source naming a class the catalog does not carry is
		// a programming error, and the export it produces is wrong in the one
		// way a customer cannot detect.
		s.logger().Error("workspace export sources are inconsistent with the ownership catalog", zap.Error(err))
		return s.errMsg(c, fiber.StatusInternalServerError, "the export could not be configured")
	}

	// SNAPSHOT FOR THE RESPONSE, TAKEN BEFORE THE GOROUTINE EXISTS.
	//
	// workspaceexport.Run mutates the job it is given — status, timestamps,
	// byte count, manifest — and it was given the SAME pointer this handler
	// then serialized. So the accepted-response body was being marshalled by
	// one goroutine while another wrote the fields it was reading: a data race,
	// and the visible form of it is a caller receiving a status that is neither
	// the one before nor the one after.
	//
	// It is not a theoretical race. `Run` sets Status to running as its first
	// act, which is exactly when this handler is encoding it, and the export
	// tests reproduced it under -race on the first run of the widened release
	// gate. It had never been run under -race before, because the gate did not
	// include internal/gateway.
	//
	// A copy rather than a mutex, because the two do not need to agree: the
	// response describes the job AS ACCEPTED, and the caller polls the status
	// URL for anything later. Locking would make the response sometimes report
	// a job that had already started, which is a less useful answer arrived at
	// with more machinery.
	accepted := *job

	runCtx := detachedRequestContext(c)
	go func() {
		// Bounded independently of the request. Detached must not mean
		// unbounded: an export wedged on a slow store would otherwise hold a
		// goroutine and a staging directory until the process restarts.
		ctx, cancel := context.WithTimeout(runCtx, 2*time.Hour)
		defer cancel()
		if err := workspaceexport.Run(store, workspaceexport.Options{
			Job: job, Sources: sources, Ctx: ctx, TTL: exportTTL(),
		}); err != nil {
			s.logger().Error("workspace export failed", zap.Error(err))
		}
	}()

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
		"export":     accepted,
		"status_url": "/api/v1/workspace/export/" + accepted.ID,
	})
}

func (s *Server) handleListWorkspaceExports(c *fiber.Ctx) error {
	identity, err := s.workspaceOwner(c)
	if err != nil {
		return err
	}
	store, err := s.exportStore()
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace exports are unavailable on this deployment")
	}
	jobs, err := store.List(identity.WorkspaceID(), time.Now())
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace exports could not be listed")
	}
	if jobs == nil {
		jobs = []*workspaceexport.Job{}
	}
	return c.JSON(fiber.Map{"exports": jobs})
}

func (s *Server) handleGetWorkspaceExport(c *fiber.Ctx) error {
	identity, err := s.workspaceOwner(c)
	if err != nil {
		return err
	}
	store, err := s.exportStore()
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace exports are unavailable on this deployment")
	}
	job, err := store.Get(identity.WorkspaceID(), c.Params("id"), time.Now())
	if err != nil {
		if errors.Is(err, workspaceexport.ErrNotFound) {
			return s.errMsg(c, fiber.StatusNotFound, "no such export")
		}
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the export could not be read")
	}
	return c.JSON(fiber.Map{"export": job})
}

func (s *Server) handleDownloadWorkspaceExport(c *fiber.Ctx) error {
	identity, err := s.workspaceOwner(c)
	if err != nil {
		return err
	}
	store, err := s.exportStore()
	if err != nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "workspace exports are unavailable on this deployment")
	}
	file, job, err := store.Open(identity.WorkspaceID(), c.Params("id"), time.Now())
	switch {
	case errors.Is(err, workspaceexport.ErrNotFound):
		return s.errMsg(c, fiber.StatusNotFound, "no such export")
	case errors.Is(err, workspaceexport.ErrExpired):
		// 410 and not 404. The distinction is the whole point of keeping the
		// job record after purging the archive: "it expired, request another"
		// is actionable, and "no such export" sends an owner to look for a
		// typo in an ID that was correct.
		return s.errMsg(c, fiber.StatusGone, "this export has expired; request a new one")
	case errors.Is(err, workspaceexport.ErrNotReady):
		return s.errMsg(c, fiber.StatusConflict, "this export is not ready yet")
	case err != nil:
		return s.errMsg(c, fiber.StatusServiceUnavailable, "the export could not be opened")
	}

	c.Set(fiber.HeaderContentType, "application/gzip")
	c.Set(fiber.HeaderContentDisposition, `attachment; filename="soulacy-workspace-export-`+job.ID+`.tar.gz"`)
	if job.Manifest != nil {
		// The checksum travels in a header as well as inside the archive, so a
		// caller can verify the download without first trusting the archive it
		// is verifying.
		c.Set("X-Soulacy-Export-Checksum", job.Manifest.Checksum)
	}
	// NO `defer file.Close()`. Fiber streams the body after this handler
	// returns, so closing here makes every download fail partway with "file
	// already closed" — and it fails during the stream, after the 200 has been
	// written, so the access log records a success. fasthttp closes the reader
	// once the response is written.
	return c.SendStream(file, int(job.Bytes))
}
