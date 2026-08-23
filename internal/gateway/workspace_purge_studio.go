package gateway

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// purgeStudioLearning removes every workspace-owned learning surface. It
// closes cached SQLite handles before touching disk and holds the cache lock
// throughout deletion, preventing a concurrent request from reopening a store
// between close and remove.
func (s *Server) purgeStudioLearning(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	workspaceID = wsroot.Normalize(workspaceID)
	if workspaceID == wsroot.PersonalWorkspaceID || wsroot.Validate(workspaceID) != nil {
		return workspacepurge.Removed{}, fmt.Errorf("refusing to purge Studio learning for workspace %q", workspaceID)
	}

	var removed workspacepurge.Removed
	add := func(next workspacepurge.Removed, err error) error {
		removed.Rows += next.Rows
		removed.Bytes += next.Bytes
		return err
	}

	stores := &s.studioStores
	stores.mu.Lock()
	defer stores.mu.Unlock()
	if lessons := stores.lessons[workspaceID]; lessons != nil {
		if err := lessons.Close(); err != nil {
			return removed, err
		}
	}

	lessonBase := lessonsBasePath()
	legacyLessonSource := strings.TrimSuffix(lessonBase, filepath.Ext(lessonBase)) + ".json"
	for _, path := range []string{
		lessonBase,
		lessonBase + "-wal",
		lessonBase + "-shm",
		lessonBase + "-journal",
		lessonBase + ".legacy.json",
		legacyLessonSource,
		macrosPath(),
		preferencesPath(),
		strategyFitPath(),
	} {
		if path == "" {
			continue
		}
		if err := add(workspacepurge.PurgeLayoutFile(ctx, s.workspaceLayout, path, workspaceID)); err != nil {
			return removed, err
		}
	}

	if root, err := s.studioRulesRoot(); err != nil {
		return removed, err
	} else if err := add(workspacepurge.PurgeLayoutTree(ctx, s.workspaceLayout, root, workspaceID)); err != nil {
		return removed, err
	}

	if s.engine != nil {
		if learning := s.engine.LearningStores(); learning != nil {
			if err := add(learning.PurgeWorkspace(ctx, workspaceID)); err != nil {
				return removed, err
			}
		}
		if err := add(s.engine.PurgeBrainWorkspace(ctx, workspaceID)); err != nil {
			return removed, err
		}
	}

	delete(stores.lessons, workspaceID)
	delete(stores.preferences, workspaceID)
	removed.Note = "Studio lessons, preferences, macros, strategy learning, rules, proposals, and agent memory"
	return removed, nil
}
