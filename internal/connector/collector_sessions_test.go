package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sessionSchemaWithSentinels declares a synthetic Hermes state.db "sessions"
// table that includes every allowed column plus a battery of forbidden
// sentinel columns (user_id, chat_id, ..., cost_usd). Tests use the sentinel
// columns to prove the collector's explicit SELECT never touches them.
const sessionSchemaWithSentinels = `
	CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		source TEXT NOT NULL,
		model TEXT,
		profile_name TEXT,
		started_at REAL NOT NULL,
		ended_at REAL,
		last_activity_at REAL,
		message_count INTEGER NOT NULL,
		tool_call_count INTEGER NOT NULL,
		input_tokens INTEGER NOT NULL,
		output_tokens INTEGER NOT NULL,
		cache_read_tokens INTEGER NOT NULL,
		cache_write_tokens INTEGER NOT NULL,
		reasoning_tokens INTEGER NOT NULL,
		pinned INTEGER NOT NULL,
		archived INTEGER NOT NULL,
		hidden INTEGER NOT NULL,
		user_id TEXT NOT NULL,
		chat_id TEXT NOT NULL,
		thread_id TEXT NOT NULL,
		session_key TEXT NOT NULL,
		origin_json TEXT NOT NULL,
		system_prompt TEXT NOT NULL,
		cwd TEXT NOT NULL,
		git_branch TEXT NOT NULL,
		billing_url TEXT NOT NULL,
		last_activity_description TEXT NOT NULL,
		cost_usd REAL NOT NULL
	);
`

func createStateDBFixture(t *testing.T, setup func(*sql.DB)) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	if setup != nil {
		setup(db)
	}
	return dbPath
}

// emptyKanbanFixture creates a Kanban DB with the tasks/task_runs tables
// present but empty, so Snapshot() can exercise the session-collection path
// in isolation from Kanban content.
func emptyKanbanFixture(t *testing.T) string {
	t.Helper()
	return createCollectorFixture(t, func(db *sql.DB) {
		mustExec(t, db, `CREATE TABLE tasks (id TEXT PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL, assignee TEXT, priority INTEGER NOT NULL, created_at INTEGER NOT NULL, started_at INTEGER, completed_at INTEGER, workspace_kind TEXT NOT NULL, branch_name TEXT, project_id TEXT, tenant TEXT, consecutive_failures INTEGER NOT NULL, worker_pid INTEGER, max_runtime_seconds INTEGER, last_heartbeat_at INTEGER, current_run_id INTEGER, block_kind TEXT)`)
		mustExec(t, db, `CREATE TABLE task_runs (id INTEGER PRIMARY KEY, task_id TEXT NOT NULL, profile TEXT, step_key TEXT, status TEXT NOT NULL, worker_pid INTEGER, max_runtime_seconds INTEGER, last_heartbeat_at INTEGER, started_at INTEGER NOT NULL, ended_at INTEGER, outcome TEXT)`)
	})
}

func insertSentinelSession(t *testing.T, db *sql.DB, id string, startedAt, endedAt, lastActivityAt any, hidden int) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO sessions (
			id, title, source, model, profile_name, started_at, ended_at, last_activity_at,
			message_count, tool_call_count, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, reasoning_tokens, pinned, archived, hidden,
			user_id, chat_id, thread_id, session_key, origin_json, system_prompt, cwd,
			git_branch, billing_url, last_activity_description, cost_usd
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		id, "标题 title", "cli", "gpt-5", "default",
		startedAt, endedAt, lastActivityAt,
		3, 2, 100, 200, 10, 20, 5, 1, 0, hidden,
		"SECRET-USER-ID", "SECRET-CHAT-ID", "SECRET-THREAD-ID", "SECRET-SESSION-KEY",
		`{"leak":"origin"}`, "SECRET-SYSTEM-PROMPT", "/Users/secret/project",
		"secret-branch", "https://billing.example.com/secret", "did something secret", 12.34,
	)
}

func TestSQLiteCollectorMapsSessionsFromExplicitColumnsAndExcludesSentinelSecrets(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		insertSentinelSession(t, db, "sess-1", 1756861323.5, nil, 1756861400.25, 0)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snap.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(snap.Sessions))
	}
	s := snap.Sessions[0]
	if s.ID != "sess-1" || s.Title != "标题 title" || s.Source != "cli" || s.Model != "gpt-5" || s.ProfileName != "default" {
		t.Fatalf("session mapping incorrect: %+v", s)
	}
	if s.MessageCount != 3 || s.ToolCallCount != 2 {
		t.Fatalf("session counts incorrect: %+v", s)
	}
	if s.InputTokens != 100 || s.OutputTokens != 200 || s.CacheReadTokens != 10 || s.CacheWriteTokens != 20 || s.ReasoningTokens != 5 {
		t.Fatalf("session token counts incorrect: %+v", s)
	}
	if !s.Pinned || s.Archived {
		t.Fatalf("session pinned/archived incorrect: %+v", s)
	}
	wantStarted := time.Unix(1756861323, 500_000_000).UTC()
	if !s.StartedAt.Equal(wantStarted) {
		t.Fatalf("StartedAt = %v, want %v (fractional seconds)", s.StartedAt, wantStarted)
	}
	if s.EndedAt != nil {
		t.Fatalf("EndedAt = %v, want nil (NULL column)", s.EndedAt)
	}
	wantLastActivity := time.Unix(1756861400, 250_000_000).UTC()
	if s.LastActivityAt == nil || !s.LastActivityAt.Equal(wantLastActivity) {
		t.Fatalf("LastActivityAt = %v, want %v", s.LastActivityAt, wantLastActivity)
	}

	body, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	forbidden := []string{
		"SECRET-USER-ID", "SECRET-CHAT-ID", "SECRET-THREAD-ID", "SECRET-SESSION-KEY",
		"origin", "SECRET-SYSTEM-PROMPT", "/Users/secret/project", "secret-branch",
		"billing.example.com", "did something secret", "12.34",
		"user_id", "chat_id", "thread_id", "session_key", "origin_json", "system_prompt",
		"cwd", "git_branch", "billing_url", "last_activity_description", "cost_usd",
	}
	for _, word := range forbidden {
		if strings.Contains(string(body), word) {
			t.Errorf("collected snapshot JSON leaks forbidden field/value %q:\n%s", word, body)
		}
	}
}

func TestSQLiteCollectorExcludesHiddenSessions(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		insertSentinelSession(t, db, "visible", 1756861000.0, nil, nil, 0)
		insertSentinelSession(t, db, "hidden", 1756861999.0, nil, nil, 1)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "visible" {
		t.Fatalf("Sessions = %+v, want only the non-hidden session", snap.Sessions)
	}
}

func TestSQLiteCollectorCapsSessionsAt200OrderedByNewestActivity(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		base := int64(1700000000)
		for i := range 205 {
			insertSentinelSession(t, db, sessionIDFor(i), float64(base+int64(i)), nil, nil, 0)
		}
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snap.Sessions) != 200 {
		t.Fatalf("len(Sessions) = %d, want 200 (capped)", len(snap.Sessions))
	}
	// Newest started_at (id 204, i.e. base+204) must sort first.
	if snap.Sessions[0].ID != sessionIDFor(204) {
		t.Errorf("Sessions[0].ID = %q, want %q (newest first)", snap.Sessions[0].ID, sessionIDFor(204))
	}
	for i := 1; i < len(snap.Sessions); i++ {
		if snap.Sessions[i-1].StartedAt.Before(snap.Sessions[i].StartedAt) {
			t.Fatalf("Sessions not ordered newest-first at index %d: %v before %v", i, snap.Sessions[i-1].StartedAt, snap.Sessions[i].StartedAt)
		}
	}
}

func sessionIDFor(i int) string {
	return fmt.Sprintf("sess-%03d", i)
}

func TestSQLiteCollectorSessionsDisabledWhenStateDBPathEmpty(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)

	c, err := NewSQLiteCollector(kanbanPath)
	if err != nil {
		t.Fatalf("NewSQLiteCollector() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snap.Sessions == nil {
		t.Error("Sessions = nil, want non-nil empty slice when session collection is disabled")
	}
	if len(snap.Sessions) != 0 {
		t.Errorf("len(Sessions) = %d, want 0 when HermesStateDBPath is empty", len(snap.Sessions))
	}
}

func TestSQLiteCollectorFailsCollectionOnMalformedSessionTimestamp(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		insertSentinelSession(t, db, "bad-timestamp", -5.0, nil, nil, 0)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err == nil {
		t.Fatalf("Snapshot() error = nil, want error for malformed started_at; got snap = %+v", snap)
	}
	if snap.Tasks != nil || snap.Runs != nil || snap.Sessions != nil {
		t.Errorf("Snapshot() on error must return zero value, got %+v", snap)
	}
}

func TestSQLiteCollectorSessionsUsesReadOnlyDatabaseAccess(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		insertSentinelSession(t, db, "sess-1", 1756861323.0, nil, nil, 0)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	before, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("Stat(before) error = %v", err)
	}
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	after, err := os.Stat(statePath)
	if err != nil {
		t.Fatalf("Stat(after) error = %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("collector modified state.db mtime: before=%v after=%v", before.ModTime(), after.ModTime())
	}
}

func TestNewSQLiteCollectorWithStateDBRejectsUnsafeStateDBPath(t *testing.T) {
	if _, err := NewSQLiteCollectorWithStateDB("/tmp/kanban.db", "../escape-state.db"); err == nil {
		t.Fatal("NewSQLiteCollectorWithStateDB() error = nil, want state db path validation failure")
	}
}

func TestSQLiteCollectorRejectsCanceledContextEvenWithStateDBConfigured(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
	})
	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = c.Snapshot(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Snapshot() error = %v, want context.Canceled", err)
	}
}
