// Package workspacelayout migrates the original subsystem-first workspace
// layout into one coherent tree per workspace.
package workspacelayout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/soulacy/soulacy/internal/wsroot"
)

const lockName = ".workspace-layout-migration.lock"

// Move is one legacy workspace subtree and its canonical destination.
type Move struct {
	From string
	To   string
}

// Report describes a completed migration.
type Report struct {
	Moves []Move
}

// Migrate discovers and moves legacy
//
//	<root>/<subsystem>/.workspaces/<workspace-id>
//
// trees to
//
//	<root>/workspaces/<workspace-id>/<subsystem>.
//
// Every destination is checked before the first rename. Existing data is
// never merged or overwritten. A partially completed migration is safe to
// retry because only source trees that still exist are planned.
func Migrate(root string) (Report, error) {
	root, err := cleanRoot(root)
	if err != nil {
		return Report{}, err
	}
	moves, err := Plan(root)
	if err != nil || len(moves) == 0 {
		return Report{Moves: moves}, err
	}

	lockPath := filepath.Join(root, lockName)
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return Report{}, fmt.Errorf("workspace layout migration is already running (%s exists)", lockPath)
		}
		return Report{}, fmt.Errorf("lock workspace layout migration: %w", err)
	}
	_ = lock.Close()
	defer os.Remove(lockPath)

	for _, move := range moves {
		if err := os.MkdirAll(filepath.Dir(move.To), 0o700); err != nil {
			return Report{}, fmt.Errorf("create workspace layout for %s: %w", move.To, err)
		}
		if err := os.Rename(move.From, move.To); err != nil {
			return Report{}, fmt.Errorf("migrate %s to %s: %w", move.From, move.To, err)
		}
		_ = os.Remove(filepath.Dir(move.From)) // remove an empty legacy namespace
	}
	return Report{Moves: moves}, nil
}

// Plan returns the deterministic, preflighted set of legacy subtree moves.
func Plan(root string) ([]Move, error) {
	root, err := cleanRoot(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect workspace root: %w", err)
	}

	canonical := filepath.Join(root, wsroot.WorkspaceDir)
	var moves []Move
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == canonical {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.Name() == wsroot.NamespaceDir {
				return fmt.Errorf("legacy workspace namespace must not be a symlink: %s", path)
			}
			return nil
		}
		if !entry.IsDir() || entry.Name() != wsroot.NamespaceDir {
			return nil
		}

		base := filepath.Dir(path)
		relBase, err := filepath.Rel(root, base)
		// The installation root may itself contain a .workspaces directory from
		// older path helpers or external mounts. It is not a subsystem-first
		// namespace, so it has no relative subsystem destination to migrate.
		// Treat it as reserved installation state and leave it untouched.
		if err == nil && relBase == "." {
			return filepath.SkipDir
		}
		if err != nil || relBase == ".." || strings.HasPrefix(filepath.ToSlash(relBase), "../") {
			return fmt.Errorf("invalid legacy workspace namespace %s", path)
		}
		children, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.Type()&os.ModeSymlink != 0 || !child.IsDir() {
				return fmt.Errorf("legacy workspace entry must be a directory, not a file or symlink: %s", filepath.Join(path, child.Name()))
			}
			if err := wsroot.Validate(child.Name()); err != nil || child.Name() == wsroot.PersonalWorkspaceID {
				return fmt.Errorf("invalid legacy workspace directory %q in %s", child.Name(), path)
			}
			from := filepath.Join(path, child.Name())
			to := filepath.Join(canonical, child.Name(), relBase)
			if _, err := os.Lstat(to); err == nil {
				return fmt.Errorf("workspace layout conflict: destination already exists: %s", to)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("inspect workspace layout destination %s: %w", to, err)
			}
			moves = append(moves, Move{From: from, To: to})
		}
		return filepath.SkipDir
	})
	if err != nil {
		return nil, fmt.Errorf("plan workspace layout migration: %w", err)
	}

	sort.Slice(moves, func(i, j int) bool {
		if moves[i].To == moves[j].To {
			return moves[i].From < moves[j].From
		}
		return moves[i].To < moves[j].To
	})
	for i := 1; i < len(moves); i++ {
		if moves[i-1].To == moves[i].To {
			return nil, fmt.Errorf("workspace layout conflict: %s and %s both map to %s", moves[i-1].From, moves[i].From, moves[i].To)
		}
	}
	return moves, nil
}

func cleanRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("workspace layout root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace layout root: %w", err)
	}
	return filepath.Clean(abs), nil
}
