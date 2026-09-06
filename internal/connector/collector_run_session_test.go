package connector

import (
	"context"
	"database/sql"
	"testing"
)

// TestSQLiteCollectorMapsRunSessionID is a regression test for Task 3 of the
// approved Lark workbench semantics: a task's responsible model must be
// derived from its linked run's session (session.model), not from the task
// assignee. That requires TaskRun to carry the session_id it was executed
// under, which the collector previously omitted entirely.
func TestSQLiteCollectorMapsRunSessionID(t *testing.T) {
	dbPath := createCollectorFixture(t, func(db *sql.DB) {
		mustExec(t, db, `CREATE TABLE tasks (id TEXT PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL, assignee TEXT, priority INTEGER NOT NULL, created_at INTEGER NOT NULL, started_at INTEGER, completed_at INTEGER, workspace_kind TEXT NOT NULL, branch_name TEXT, project_id TEXT, tenant TEXT, consecutive_failures INTEGER NOT NULL, worker_pid INTEGER, max_runtime_seconds INTEGER, last_heartbeat_at INTEGER, current_run_id INTEGER, block_kind TEXT)`)
		mustExec(t, db, `CREATE TABLE task_runs (id INTEGER PRIMARY KEY, task_id TEXT NOT NULL, profile TEXT, step_key TEXT, status TEXT NOT NULL, worker_pid INTEGER, max_runtime_seconds INTEGER, last_heartbeat_at INTEGER, started_at INTEGER NOT NULL, ended_at INTEGER, outcome TEXT, session_id TEXT)`)
		mustExec(t, db, `INSERT INTO tasks (id, title, status, assignee, priority, created_at, started_at, completed_at, workspace_kind, branch_name, project_id, tenant, consecutive_failures, worker_pid, max_runtime_seconds, last_heartbeat_at, current_run_id, block_kind) VALUES ('task-1','t','running',NULL,1,100,NULL,NULL,'local',NULL,NULL,NULL,0,NULL,NULL,NULL,9,NULL)`)
		mustExec(t, db, `INSERT INTO task_runs (id, task_id, profile, step_key, status, worker_pid, max_runtime_seconds, last_heartbeat_at, started_at, ended_at, outcome, session_id) VALUES (9,'task-1',NULL,NULL,'running',NULL,NULL,NULL,100,NULL,NULL,'session-42')`)
		mustExec(t, db, `INSERT INTO task_runs (id, task_id, profile, step_key, status, worker_pid, max_runtime_seconds, last_heartbeat_at, started_at, ended_at, outcome, session_id) VALUES (10,'task-1',NULL,NULL,'done',NULL,NULL,NULL,100,NULL,NULL,NULL)`)
	})

	c := &SQLiteCollector{Path: dbPath}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snap.Runs) != 2 {
		t.Fatalf("len(Runs) = %d, want 2", len(snap.Runs))
	}
	byID := map[int64]TaskRun{}
	for _, r := range snap.Runs {
		byID[r.ID] = r
	}
	if got := byID[9].SessionID; got != "session-42" {
		t.Errorf("Runs[id=9].SessionID = %q, want %q", got, "session-42")
	}
	if got := byID[10].SessionID; got != "" {
		t.Errorf("Runs[id=10].SessionID = %q, want empty string for NULL session_id", got)
	}
}

func TestSQLiteCollectorReadsLegacyTaskRunsWithoutSessionID(t *testing.T) {
	dbPath := createCollectorFixture(t, func(db *sql.DB) {
		mustExec(t, db, `CREATE TABLE tasks (id TEXT PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL, assignee TEXT, priority INTEGER NOT NULL, created_at INTEGER NOT NULL, started_at INTEGER, completed_at INTEGER, workspace_kind TEXT NOT NULL, branch_name TEXT, project_id TEXT, tenant TEXT, consecutive_failures INTEGER NOT NULL, worker_pid INTEGER, max_runtime_seconds INTEGER, last_heartbeat_at INTEGER, current_run_id INTEGER, block_kind TEXT)`)
		mustExec(t, db, `CREATE TABLE task_runs (id INTEGER PRIMARY KEY, task_id TEXT NOT NULL, profile TEXT, step_key TEXT, status TEXT NOT NULL, worker_pid INTEGER, max_runtime_seconds INTEGER, last_heartbeat_at INTEGER, started_at INTEGER NOT NULL, ended_at INTEGER, outcome TEXT)`)
		mustExec(t, db, `INSERT INTO tasks (id, title, status, assignee, priority, created_at, started_at, completed_at, workspace_kind, branch_name, project_id, tenant, consecutive_failures, worker_pid, max_runtime_seconds, last_heartbeat_at, current_run_id, block_kind) VALUES ('task-1','legacy','running','agent-a',1,100,NULL,NULL,'local',NULL,NULL,NULL,0,NULL,NULL,NULL,7,NULL)`)
		mustExec(t, db, `INSERT INTO task_runs (id, task_id, profile, step_key, status, worker_pid, max_runtime_seconds, last_heartbeat_at, started_at, ended_at, outcome) VALUES (7,'task-1',NULL,NULL,'running',NULL,NULL,NULL,100,NULL,NULL)`)
	})

	c := &SQLiteCollector{Path: dbPath}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snap.Runs) != 1 {
		t.Fatalf("len(Runs) = %d, want 1", len(snap.Runs))
	}
	if got := snap.Runs[0].SessionID; got != "" {
		t.Errorf("Runs[0].SessionID = %q, want empty string for legacy schema", got)
	}
}
