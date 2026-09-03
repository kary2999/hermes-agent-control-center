package connector

import (
	"errors"
	"testing"
)

func TestResolveKanbanDBPath(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		homeDir string
		want    string
	}{
		{
			name:    "default platform home when nothing set",
			env:     map[string]string{},
			homeDir: "/Users/alice",
			want:    "/Users/alice/.hermes/kanban.db",
		},
		{
			name:    "HERMES_HOME overrides platform default",
			env:     map[string]string{"HERMES_HOME": "/opt/hermes-data"},
			homeDir: "/Users/alice",
			want:    "/opt/hermes-data/kanban.db",
		},
		{
			name:    "HERMES_HOME pointing at a named profile collapses to the profile root",
			env:     map[string]string{"HERMES_HOME": "/Users/alice/.hermes/profiles/coder"},
			homeDir: "/Users/alice",
			want:    "/Users/alice/.hermes/kanban.db",
		},
		{
			name:    "HERMES_HOME pointing at a named profile under a custom root collapses to that root",
			env:     map[string]string{"HERMES_HOME": "/opt/hermes-data/profiles/coder"},
			homeDir: "/Users/alice",
			want:    "/opt/hermes-data/kanban.db",
		},
		{
			name:    "HERMES_KANBAN_HOME overrides HERMES_HOME",
			env:     map[string]string{"HERMES_HOME": "/opt/hermes-data", "HERMES_KANBAN_HOME": "/srv/kanban-root"},
			homeDir: "/Users/alice",
			want:    "/srv/kanban-root/kanban.db",
		},
		{
			name: "HERMES_KANBAN_DB overrides everything",
			env: map[string]string{
				"HERMES_HOME":        "/opt/hermes-data",
				"HERMES_KANBAN_HOME": "/srv/kanban-root",
				"HERMES_KANBAN_DB":   "/custom/path/board.db",
			},
			homeDir: "/Users/alice",
			want:    "/custom/path/board.db",
		},
		{
			name:    "blank env vars are treated as unset",
			env:     map[string]string{"HERMES_HOME": "", "HERMES_KANBAN_DB": "  "},
			homeDir: "/Users/alice",
			want:    "/Users/alice/.hermes/kanban.db",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := HomeEnv{
				Getenv:      func(key string) string { return tc.env[key] },
				UserHomeDir: func() (string, error) { return tc.homeDir, nil },
			}

			got, err := ResolveKanbanDBPath(env)
			if err != nil {
				t.Fatalf("ResolveKanbanDBPath() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolveKanbanDBPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveKanbanDBPathPropagatesUserHomeDirError(t *testing.T) {
	wantErr := errors.New("no home directory")
	env := HomeEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", wantErr },
	}

	_, err := ResolveKanbanDBPath(env)
	if !errors.Is(err, wantErr) {
		t.Errorf("ResolveKanbanDBPath() error = %v, want wrapping %v", err, wantErr)
	}
}
