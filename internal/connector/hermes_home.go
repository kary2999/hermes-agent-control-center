package connector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HomeEnv abstracts the environment lookups needed to resolve the Hermes
// kanban.db this Connector should read from, so path resolution can be
// tested without mutating process-wide environment variables. Zero-value
// fields fall back to the real process environment via withDefaults.
type HomeEnv struct {
	// Getenv looks up an environment variable by name, returning "" if
	// unset. Defaults to os.Getenv.
	Getenv func(key string) string
	// UserHomeDir returns the current user's home directory. Defaults to
	// os.UserHomeDir.
	UserHomeDir func() (string, error)
}

func (e HomeEnv) withDefaults() HomeEnv {
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	if e.UserHomeDir == nil {
		e.UserHomeDir = os.UserHomeDir
	}
	return e
}

func (e HomeEnv) getenv(key string) (string, bool) {
	v := strings.TrimSpace(e.Getenv(key))
	return v, v != ""
}

// ResolveKanbanDBPath returns the on-disk path to the Hermes kanban.db this
// Connector should read, mirroring the precedence Hermes itself uses
// (hermes_cli/kanban_db.py: kanban_db_path / kanban_home / get_default_hermes_root):
//
//  1. HERMES_KANBAN_DB — pins the database file path directly.
//  2. HERMES_KANBAN_HOME — the kanban root directory; the DB is
//     <home>/kanban.db.
//  3. HERMES_HOME — the Hermes installation root. When it points at a
//     named profile directory (<root>/profiles/<name>), the root is
//     collapsed to <root> since the kanban board is shared across all
//     profiles by design.
//  4. The platform default, <user-home>/.hermes.
//
// This never reads or writes any Hermes-owned file; it only computes a
// path.
func ResolveKanbanDBPath(env HomeEnv) (string, error) {
	env = env.withDefaults()

	if v, ok := env.getenv("HERMES_KANBAN_DB"); ok {
		return v, nil
	}
	if v, ok := env.getenv("HERMES_KANBAN_HOME"); ok {
		return filepath.Join(v, "kanban.db"), nil
	}

	root, err := hermesRoot(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "kanban.db"), nil
}

// hermesRoot resolves the Hermes installation root that anchors the shared
// kanban board, collapsing a HERMES_HOME that points at
// <root>/profiles/<name> down to <root>.
func hermesRoot(env HomeEnv) (string, error) {
	if v, ok := env.getenv("HERMES_HOME"); ok {
		if filepath.Base(filepath.Dir(v)) == "profiles" {
			return filepath.Dir(filepath.Dir(v)), nil
		}
		return v, nil
	}

	home, err := env.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".hermes"), nil
}
