// watcher.go — filesystem watcher for hot-reloading agent SOUL.yaml files.
// Any *.yaml change inside an agent directory triggers a debounced reload so
// the gateway picks up changes without a restart.
package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/pkg/agent"
)

// AgentRegistry is satisfied by the scheduler — it allows the watcher to
// re-register agents after a reload without importing the scheduler package
// (which would create an import cycle).
type AgentRegistry interface {
	RegisterAgent(def *agent.Definition) error
	DeregisterAgent(id string)
}

// Watcher monitors one or more directories for SOUL.yaml changes and triggers
// a hot-reload of the Loader + Scheduler on every write.
//
// It also optionally watches Python tool directories — when a *.py file
// changes under any of those, OnPyChange is invoked (if set). The gateway
// wires that to its tool-catalog cache so a freshly-added tool is visible
// without waiting for the TTL.
// (PRODUCTION_AUDIT → HIGH/Caching)
type Watcher struct {
	loader   *Loader
	registry AgentRegistry
	w        *fsnotify.Watcher
	log      *zap.Logger

	// OnPyChange fires when a *.py file changes under any of the directories
	// passed to NewWatcher's `pyDirs` argument. May be nil.
	OnPyChange func()

	mu     sync.Mutex
	timers map[string]*time.Timer // debounce per file path

	// stopped is set by Stop() so the loop can tell a deliberate shutdown from
	// an unexpected fsnotify channel close. healthy reflects whether the watch
	// loop is still running; /health surfaces it (S2.4) so a silently-dead
	// watcher — which would leave the gateway serving stale agents forever —
	// becomes visible instead of invisible.
	stopped atomic.Bool
	healthy atomic.Bool
}

// NewWatcher creates and starts a new Watcher.
//
//	agentDirs — folders containing SOUL.yaml files (watched recursively;
//	            any subdir created at runtime is auto-added).
//	pyDirs    — folders containing Python tool files (~/.soulacy/tools/,
//	            agent_dirs/*/tools/). Watched non-recursively; .py changes
//	            trigger OnPyChange instead of the agent reload path.
//
// Call Stop() when done to release resources.
func NewWatcher(loader *Loader, registry AgentRegistry, agentDirs []string, log *zap.Logger, pyDirs ...string) (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	wt := &Watcher{
		loader:   loader,
		registry: registry,
		w:        w,
		log:      log,
		timers:   make(map[string]*time.Timer),
	}
	// Agents live in per-agent subfolders (<dir>/<id>/SOUL.yaml), so watch each
	// configured dir AND its subdirectories — fsnotify is not recursive.
	for _, d := range agentDirs {
		wt.addRecursive(d)
	}
	// Python tool dirs — flat watch; we don't expect nested layouts here.
	for _, d := range pyDirs {
		if d == "" {
			continue
		}
		if err := wt.w.Add(d); err != nil {
			wt.log.Debug("watcher: cannot watch python tool dir (likely doesn't exist yet)",
				zap.String("dir", d), zap.Error(err))
		}
	}
	return wt, nil
}

// addRecursive adds a watch on dir and every subdirectory beneath it.
func (wt *Watcher) addRecursive(root string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == ".git" || base == "node_modules" {
			return filepath.SkipDir
		}
		if err := wt.w.Add(path); err != nil {
			wt.log.Warn("watcher: cannot watch dir", zap.String("dir", path), zap.Error(err))
		} else {
			wt.log.Debug("watcher: watching dir", zap.String("dir", path))
		}
		return nil
	})
}

// Start launches the watcher goroutine. Non-blocking.
func (wt *Watcher) Start() {
	wt.healthy.Store(true)
	go wt.loop()
}

// Stop shuts down the watcher and releases the fsnotify resources.
func (wt *Watcher) Stop() {
	wt.stopped.Store(true)
	_ = wt.w.Close()
}

// Healthy reports whether the hot-reload watch loop is still running. Returns
// false after Stop() or if fsnotify died unexpectedly (S2.4).
func (wt *Watcher) Healthy() bool {
	return wt.healthy.Load()
}

func (wt *Watcher) loop() {
	// On any exit, mark unhealthy. If we did NOT stop deliberately, this is a
	// silent fsnotify death — log loudly so the operator knows hot-reload is
	// dead and edits will be ignored until a restart.
	defer func() {
		wt.healthy.Store(false)
		if !wt.stopped.Load() {
			wt.log.Error("watcher: hot-reload loop exited unexpectedly — SOUL.yaml edits will NOT be picked up until the gateway is restarted")
		}
	}()
	for {
		select {
		case event, ok := <-wt.w.Events:
			if !ok {
				return
			}
			// A new per-agent folder was created — start watching it too.
			if event.Op&fsnotify.Create == fsnotify.Create {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					wt.addRecursive(event.Name)
					continue
				}
			}
			lower := strings.ToLower(event.Name)
			switch {
			case strings.HasSuffix(lower, ".yaml"), strings.HasSuffix(lower, ".yml"):
				wt.debounce(event.Name)
			case strings.HasSuffix(lower, ".py"):
				// Tool file changed — invalidate the gateway's tool-catalog
				// cache so the next /tool-catalog request rescans.
				if wt.OnPyChange != nil {
					wt.OnPyChange()
				}
			}

		case err, ok := <-wt.w.Errors:
			if !ok {
				return
			}
			wt.log.Warn("watcher: fsnotify error", zap.Error(err))
		}
	}
}

// debounce coalesces rapid successive writes to the same file into a single
// reload after 250 ms of quiet — prevents double-fires from editors that
// write via temp-file rename (like vim).
func (wt *Watcher) debounce(path string) {
	wt.mu.Lock()
	defer wt.mu.Unlock()
	if t, exists := wt.timers[path]; exists {
		t.Reset(250 * time.Millisecond)
		return
	}
	wt.timers[path] = time.AfterFunc(250*time.Millisecond, func() {
		wt.reload(path)
		wt.mu.Lock()
		delete(wt.timers, path)
		wt.mu.Unlock()
	})
}

func (wt *Watcher) reload(path string) {
	wt.log.Info("agent file changed — reloading",
		zap.String("file", filepath.Base(path)),
	)

	// PRODUCTION_AUDIT → LOW/Reliability: diff old vs new instead of
	// bouncing every scheduled agent on every reload. Previously a single
	// agent edit deregistered and re-registered every cron entry — leaving
	// a brief window where a scheduled run could be skipped. Now we only
	// touch entries that actually changed (added / removed / mutated
	// schedule), so unrelated agents stay scheduled throughout the reload.

	// Snapshot pre-reload state.
	oldDefs := make(map[string]*agent.Definition)
	for _, def := range wt.loader.All() {
		oldDefs[def.ID] = def
	}

	// Load the updated agent definitions from the filesystem.
	errs := wt.loader.LoadAll()
	for _, e := range errs {
		wt.log.Warn("watcher: reload error", zap.Error(e))
	}

	// Compute new state.
	newDefs := make(map[string]*agent.Definition)
	for _, def := range wt.loader.All() {
		newDefs[def.ID] = def
	}

	// Deregister anything that's gone OR whose schedule/trigger/enabled
	// state changed. Then register added or changed entries.
	for id, oldDef := range oldDefs {
		newDef, stillThere := newDefs[id]
		if !stillThere || scheduleChanged(oldDef, newDef) {
			wt.registry.DeregisterAgent(id)
		}
	}
	for id, newDef := range newDefs {
		oldDef, existed := oldDefs[id]
		if !existed || scheduleChanged(oldDef, newDef) {
			if err := wt.registry.RegisterAgent(newDef); err != nil {
				wt.log.Warn("watcher: scheduler re-register failed",
					zap.String("agent", id), zap.Error(err))
			}
		}
	}

	wt.log.Info("hot-reload complete",
		zap.Int("agents", len(newDefs)),
		zap.Int("errors", len(errs)),
	)
}

// scheduleChanged returns true if any field affecting scheduling changed
// between two versions of the same agent. Pure system_prompt / tools /
// channel edits don't touch the cron registry.
func scheduleChanged(a, b *agent.Definition) bool {
	if a == nil || b == nil {
		return true
	}
	if a.Enabled != b.Enabled || a.Trigger != b.Trigger {
		return true
	}
	if (a.Schedule == nil) != (b.Schedule == nil) {
		return true
	}
	if a.Schedule != nil && b.Schedule != nil {
		if a.Schedule.Cron != b.Schedule.Cron ||
			!a.Schedule.At.Equal(b.Schedule.At) ||
			a.Schedule.Timeout != b.Schedule.Timeout {
			return true
		}
	}
	return false
}
