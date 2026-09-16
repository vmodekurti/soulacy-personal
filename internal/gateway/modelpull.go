// modelpull.go — download a local model from the dashboard.
//
// The shipped default provider is local, and until now nothing in the product
// could obtain a model for it. Every layer punted to the user's own terminal:
// the first-run config comment, the validator's remedy, the provider doctor,
// and the Providers page empty state all said "run `ollama pull`". For someone
// whose only surface is the browser that is a dead end on day one, and it is
// the reason a fresh install often cannot answer a single message.
//
// A pull takes minutes and the user will refresh the page, so this is a job
// with an id rather than a streamed response: progress survives a reload, and
// closing the tab does not cancel the download.
package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/llm"
)

// pullJob is the state of one download, as the dashboard sees it.
type pullJob struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Status    string    `json:"status"`
	Completed int64     `json:"completed"`
	Total     int64     `json:"total"`
	Done      bool      `json:"done"`
	Error     string    `json:"error,omitempty"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished,omitempty"`

	cancel context.CancelFunc
}

// pullRegistry holds in-flight and recently finished pulls.
//
// In memory on purpose: a pull that was interrupted by a gateway restart did
// not finish, and resuming it is Ollama's business, not ours. Finished jobs
// are kept briefly so a dashboard that polls just after completion still sees
// the outcome rather than a 404 it would render as an error.
type pullRegistry struct {
	mu   sync.Mutex
	jobs map[string]*pullJob
}

const pullJobRetention = 10 * time.Minute

func (r *pullRegistry) put(j *pullJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.jobs == nil {
		r.jobs = map[string]*pullJob{}
	}
	r.jobs[j.ID] = j
	// Opportunistic sweep. There is no background goroutine to leak, and the
	// number of pulls a person starts is small.
	for id, old := range r.jobs {
		if old.Done && time.Since(old.Finished) > pullJobRetention {
			delete(r.jobs, id)
		}
	}
}

func (r *pullRegistry) get(id string) (pullJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return pullJob{}, false
	}
	return *j, true // a copy: callers must not race with the progress writer
}

// update mutates a live job under the registry lock.
func (r *pullRegistry) update(id string, fn func(*pullJob)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j, ok := r.jobs[id]; ok {
		fn(j)
	}
}

// active reports a running job for this provider+model, so a second click on
// the same button joins the existing download instead of starting a rival one.
func (r *pullRegistry) active(provider, model string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, j := range r.jobs {
		if !j.Done && j.Provider == provider && j.Model == model {
			return id, true
		}
	}
	return "", false
}

// ollamaProviderFor returns the Ollama provider for this id, or an error
// explaining why a pull is not possible. Only Ollama can be pulled into: a
// hosted provider's catalog is not ours to change.
func (s *Server) ollamaProviderFor(id string) (*llm.OllamaProvider, error) {
	if s.llmRouter == nil {
		return nil, fmt.Errorf("no LLM router is configured")
	}
	p := s.llmRouter.Provider(id)
	if p == nil {
		return nil, fmt.Errorf("provider %q is not registered", id)
	}
	op, ok := p.(*llm.OllamaProvider)
	if !ok {
		return nil, fmt.Errorf("provider %q is not a local Ollama instance — cloud providers already have their models", id)
	}
	return op, nil
}

func (s *Server) handleStartModelPull(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))
	var body struct {
		Model string `json:"model"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid request body"})
	}
	model := strings.TrimSpace(body.Model)
	if model == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name the model to download"})
	}
	op, err := s.ollamaProviderFor(id)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	// Joining an existing pull is the right answer to a double click, and to
	// two browser tabs both showing the same empty state.
	if existing, ok := s.pulls.active(id, model); ok {
		return c.JSON(fiber.Map{"job_id": existing, "joined": true})
	}

	// context.Background, not the request context: the download must outlive
	// the HTTP call that started it.
	ctx, cancel := context.WithCancel(context.Background())
	job := &pullJob{
		ID: uuid.NewString(), Provider: id, Model: model,
		Status: "starting", Started: time.Now().UTC(), cancel: cancel,
	}
	s.pulls.put(job)
	jobID := job.ID

	s.log.Info("model pull started", zap.String("provider", id), zap.String("model", model), zap.String("job", jobID))
	s.recordAdminAudit(c, "model.pull", "provider", id+"/"+model, "started", nil)

	go func() {
		defer cancel()
		err := op.PullModel(ctx, model, func(pr llm.PullProgress) {
			s.pulls.update(jobID, func(j *pullJob) {
				j.Status = pr.Status
				// Only overwrite counts when this step carries them; the
				// manifest and verify phases report none, and zeroing here
				// would make the bar jump back to the start twice per pull.
				if pr.Total > 0 {
					j.Completed, j.Total = pr.Completed, pr.Total
				}
			})
		})
		s.pulls.update(jobID, func(j *pullJob) {
			j.Done = true
			j.Finished = time.Now().UTC()
			if err != nil {
				j.Error = err.Error()
				j.Status = "failed"
				return
			}
			j.Status = "success"
			if j.Total > 0 {
				j.Completed = j.Total
			}
		})
		if err != nil {
			s.log.Warn("model pull failed", zap.String("model", model), zap.Error(err))
			return
		}
		s.log.Info("model pull finished", zap.String("model", model))
	}()

	return c.Status(fiber.StatusAccepted).JSON(fiber.Map{"job_id": jobID})
}

func (s *Server) handleModelPullStatus(c *fiber.Ctx) error {
	job, ok := s.pulls.get(strings.TrimSpace(c.Params("job")))
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "no such download"})
	}
	return c.JSON(job)
}

// handleCancelModelPull stops a download in progress. Ollama keeps the layers
// it already fetched, so restarting later resumes rather than beginning again.
func (s *Server) handleCancelModelPull(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("job"))
	s.pulls.mu.Lock()
	j, ok := s.pulls.jobs[id]
	if ok && !j.Done && j.cancel != nil {
		j.cancel()
	}
	s.pulls.mu.Unlock()
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "no such download"})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// ── Suggestions ──────────────────────────────────────────────────────────────

// suggestedModel is one option on the "you have no model" screen.
//
// Sizes are the real summed layer sizes from the Ollama registry, not
// estimates, so the disk figure shown to a user is the one they will pay.
// MinRAMGB is deliberately conservative: a model that technically loads but
// swaps makes the product feel broken in a way a user will blame on Soulacy.
type suggestedModel struct {
	Name      string  `json:"name"`
	SizeGB    float64 `json:"size_gb"`
	MinRAMGB  int     `json:"min_ram_gb"`
	Summary   string  `json:"summary"`
	Fits      bool    `json:"fits"`
	Default   bool    `json:"default,omitempty"`
	Embedding bool    `json:"embedding,omitempty"`
}

// curatedModels is short on purpose. A long list is a decision the user is not
// equipped to make on their first day; the point is to get them running, not to
// survey the field. Every name and size here was verified against the registry
// rather than recalled, because a suggestion that 404s on pull reproduces the
// exact dead end this feature exists to remove.
var curatedModels = []suggestedModel{
	{Name: "llama3.2:3b", SizeGB: 1.9, MinRAMGB: 8, Summary: "Smallest useful assistant. Fast on any modern laptop."},
	{Name: "qwen3:4b", SizeGB: 2.3, MinRAMGB: 8, Summary: "Small and strong at following instructions and calling tools."},
	{Name: "qwen3:8b", SizeGB: 4.9, MinRAMGB: 16, Summary: "A good default when you have the memory for it."},
	{Name: "qwen3:14b", SizeGB: 8.6, MinRAMGB: 32, Summary: "Noticeably better reasoning for multi-step work."},
	{Name: "qwen3:32b", SizeGB: 18.8, MinRAMGB: 64, Summary: "Strongest local option. Needs a workstation."},
	{Name: "nomic-embed-text", SizeGB: 0.3, MinRAMGB: 8, Summary: "Required for Knowledge search. Small and quick.", Embedding: true},
}

// handleSuggestedModels answers "which model should I install?" with the
// machine's own memory in hand.
//
// The product previously had an opinion about this in exactly one place — a
// first-run config comment naming a 40GB model — and no way to act on it.
func (s *Server) handleSuggestedModels(c *fiber.Ctx) error {
	id := strings.TrimSpace(c.Params("id"))
	ramGB := hostMemoryGB()

	installed := map[string]bool{}
	if op, err := s.ollamaProviderFor(id); err == nil {
		if names, err := op.Models(c.Context()); err == nil {
			for _, n := range names {
				installed[n] = true
				// Ollama reports "qwen3:8b"; a user may have pulled it as
				// "qwen3:8b" or seen it listed without the tag. Treat the
				// base name as installed too so we don't suggest a duplicate.
				installed[strings.SplitN(n, ":", 2)[0]] = true
			}
		}
	}

	out := make([]suggestedModel, 0, len(curatedModels))
	var bestChat string
	for _, m := range curatedModels {
		if installed[m.Name] {
			continue // nothing to suggest; they already have it
		}
		m.Fits = ramGB == 0 || ramGB >= m.MinRAMGB
		// The default is the largest chat model that fits, because a user who
		// can run something better should not be handed the smallest option.
		if m.Fits && !m.Embedding {
			bestChat = m.Name
		}
		out = append(out, m)
	}
	for i := range out {
		if out[i].Name == bestChat {
			out[i].Default = true
		}
	}
	return c.JSON(fiber.Map{
		"host_ram_gb": ramGB,
		"models":      out,
	})
}
