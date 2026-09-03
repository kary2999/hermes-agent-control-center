package connector

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func ts(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

func TestDiffDetectsAddedTask(t *testing.T) {
	prev := Snapshot{}
	next := Snapshot{Tasks: []AgentTask{{ID: "t1", Title: "one", Status: "todo", CreatedAt: ts(100)}}}

	changes := Diff(prev, next)

	if len(changes) != 1 {
		t.Fatalf("Diff() = %d changes, want 1", len(changes))
	}
	if changes[0].Kind != ChangeTaskAdded {
		t.Errorf("Kind = %v, want %v", changes[0].Kind, ChangeTaskAdded)
	}
	if changes[0].Task == nil || changes[0].Task.ID != "t1" {
		t.Errorf("Task = %+v, want task t1", changes[0].Task)
	}
}

func TestDiffDetectsUpdatedTask(t *testing.T) {
	prev := Snapshot{Tasks: []AgentTask{{ID: "t1", Title: "one", Status: "todo", CreatedAt: ts(100)}}}
	next := Snapshot{Tasks: []AgentTask{{ID: "t1", Title: "one", Status: "running", CreatedAt: ts(100)}}}

	changes := Diff(prev, next)

	if len(changes) != 1 {
		t.Fatalf("Diff() = %d changes, want 1", len(changes))
	}
	if changes[0].Kind != ChangeTaskUpdated {
		t.Errorf("Kind = %v, want %v", changes[0].Kind, ChangeTaskUpdated)
	}
	if changes[0].Task == nil || changes[0].Task.Status != "running" {
		t.Errorf("Task = %+v, want status running", changes[0].Task)
	}
}

func TestDiffDetectsRemovedTask(t *testing.T) {
	prev := Snapshot{Tasks: []AgentTask{{ID: "t1", Title: "one", Status: "todo", CreatedAt: ts(100)}}}
	next := Snapshot{}

	changes := Diff(prev, next)

	if len(changes) != 1 {
		t.Fatalf("Diff() = %d changes, want 1", len(changes))
	}
	if changes[0].Kind != ChangeTaskRemoved {
		t.Errorf("Kind = %v, want %v", changes[0].Kind, ChangeTaskRemoved)
	}
	if changes[0].Task == nil || changes[0].Task.ID != "t1" {
		t.Errorf("Task = %+v, want task t1", changes[0].Task)
	}
}

func TestDiffIgnoresUnchangedTask(t *testing.T) {
	task := AgentTask{ID: "t1", Title: "one", Status: "todo", CreatedAt: ts(100)}
	prev := Snapshot{Tasks: []AgentTask{task}}
	next := Snapshot{Tasks: []AgentTask{task}}

	changes := Diff(prev, next)

	if len(changes) != 0 {
		t.Fatalf("Diff() = %d changes, want 0 for identical snapshots", len(changes))
	}
}

func TestDiffDetectsRunLifecycle(t *testing.T) {
	cases := []struct {
		name string
		prev Snapshot
		next Snapshot
		want ChangeKind
	}{
		{
			name: "run added",
			prev: Snapshot{},
			next: Snapshot{Runs: []TaskRun{{ID: 1, TaskID: "t1", Status: "running", StartedAt: ts(100)}}},
			want: ChangeRunAdded,
		},
		{
			name: "run updated",
			prev: Snapshot{Runs: []TaskRun{{ID: 1, TaskID: "t1", Status: "running", StartedAt: ts(100)}}},
			next: Snapshot{Runs: []TaskRun{{ID: 1, TaskID: "t1", Status: "done", StartedAt: ts(100)}}},
			want: ChangeRunUpdated,
		},
		{
			name: "run removed",
			prev: Snapshot{Runs: []TaskRun{{ID: 1, TaskID: "t1", Status: "running", StartedAt: ts(100)}}},
			next: Snapshot{},
			want: ChangeRunRemoved,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changes := Diff(tc.prev, tc.next)
			if len(changes) != 1 {
				t.Fatalf("Diff() = %d changes, want 1", len(changes))
			}
			if changes[0].Kind != tc.want {
				t.Errorf("Kind = %v, want %v", changes[0].Kind, tc.want)
			}
			if changes[0].Run == nil || changes[0].Run.ID != 1 {
				t.Errorf("Run = %+v, want run 1", changes[0].Run)
			}
		})
	}
}

func TestDiffOrdersChangesDeterministically(t *testing.T) {
	prev := Snapshot{}
	next := Snapshot{
		Tasks: []AgentTask{
			{ID: "t2", Title: "two", Status: "todo", CreatedAt: ts(100)},
			{ID: "t1", Title: "one", Status: "todo", CreatedAt: ts(100)},
		},
		Runs: []TaskRun{
			{ID: 2, TaskID: "t2", Status: "running", StartedAt: ts(100)},
			{ID: 1, TaskID: "t1", Status: "running", StartedAt: ts(100)},
		},
	}

	changes := Diff(prev, next)

	if len(changes) != 4 {
		t.Fatalf("Diff() = %d changes, want 4", len(changes))
	}
	if changes[0].Task.ID != "t1" || changes[1].Task.ID != "t2" {
		t.Errorf("task changes not sorted by ID: %+v, %+v", changes[0].Task, changes[1].Task)
	}
	if changes[2].Run.ID != 1 || changes[3].Run.ID != 2 {
		t.Errorf("run changes not sorted by ID: %+v, %+v", changes[2].Run, changes[3].Run)
	}
}

func TestSQLiteCollectorMapsTasksAndRunsFromExplicitColumns(t *testing.T) {
	dbPath := createCollectorFixture(t, func(db *sql.DB) {
		mustExec(t, db, `
			CREATE TABLE tasks (
				id TEXT PRIMARY KEY,
				title TEXT NOT NULL,
				status TEXT NOT NULL,
				assignee TEXT,
				priority INTEGER NOT NULL,
				created_at INTEGER NOT NULL,
				started_at INTEGER,
				completed_at INTEGER,
				workspace_kind TEXT NOT NULL,
				branch_name TEXT,
				project_id TEXT,
				tenant TEXT,
				consecutive_failures INTEGER NOT NULL,
				worker_pid INTEGER,
				max_runtime_seconds INTEGER,
				last_heartbeat_at INTEGER,
				current_run_id INTEGER,
				block_kind TEXT,
				body TEXT NOT NULL,
				result TEXT NOT NULL
			);
			CREATE TABLE task_runs (
				id INTEGER PRIMARY KEY,
				task_id TEXT NOT NULL,
				profile TEXT,
				step_key TEXT,
				status TEXT NOT NULL,
				worker_pid INTEGER,
				max_runtime_seconds INTEGER,
				last_heartbeat_at INTEGER,
				started_at INTEGER NOT NULL,
				ended_at INTEGER,
				outcome TEXT,
				summary TEXT NOT NULL,
				metadata TEXT NOT NULL,
				error TEXT NOT NULL,
				skills TEXT NOT NULL,
				model_override TEXT NOT NULL,
				session_id TEXT NOT NULL,
				claim TEXT NOT NULL
			);
		`)
		mustExec(t, db, `
			INSERT INTO tasks (
				id, title, status, assignee, priority, created_at, started_at, completed_at,
				workspace_kind, branch_name, project_id, tenant, consecutive_failures,
				worker_pid, max_runtime_seconds, last_heartbeat_at, current_run_id, block_kind,
				body, result
			) VALUES (
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			)
		`,
			"task-1",
			strings.Repeat("界", 300),
			"running",
			nil,
			7,
			int64(1756861323),
			int64(1756861384),
			nil,
			"local",
			nil,
			nil,
			nil,
			2,
			1234,
			600,
			int64(1756861445),
			88,
			nil,
			"redacted body",
			"redacted result",
		)
		mustExec(t, db, `
			INSERT INTO task_runs (
				id, task_id, profile, step_key, status, worker_pid, max_runtime_seconds,
				last_heartbeat_at, started_at, ended_at, outcome, summary, metadata, error,
				skills, model_override, session_id, claim
			) VALUES (
				?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			)
		`,
			int64(9),
			"task-1",
			nil,
			nil,
			"running",
			4321,
			900,
			int64(1756861506),
			int64(1756861567),
			nil,
			nil,
			"summary",
			"metadata",
			"error",
			"skills",
			"model",
			"session",
			"claim",
		)
	})

	c := &SQLiteCollector{Path: dbPath}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	if len(snap.Tasks) != 1 {
		t.Fatalf("len(Tasks) = %d, want 1", len(snap.Tasks))
	}
	task := snap.Tasks[0]
	if got := []rune(task.Title); len(got) != 256 {
		t.Fatalf("len(Task.Title runes) = %d, want 256", len(got))
	}
	if task.ID != "task-1" || task.Status != "running" || task.Assignee != "" || task.Priority != 7 {
		t.Fatalf("task mapping incorrect: %+v", task)
	}
	if task.WorkspaceKind != "local" || task.BranchName != "" || task.ProjectID != "" || task.Tenant != "" {
		t.Fatalf("task mapping incorrect: %+v", task)
	}
	if task.ConsecutiveFailures != 2 || task.BlockKind != "" {
		t.Fatalf("task mapping incorrect: %+v", task)
	}
	if task.WorkerPID == nil || *task.WorkerPID != 1234 || task.MaxRuntimeSeconds == nil || *task.MaxRuntimeSeconds != 600 {
		t.Fatalf("task numeric pointers incorrect: %+v", task)
	}
	if task.CurrentRunID == nil || *task.CurrentRunID != 88 {
		t.Fatalf("task current run mapping incorrect: %+v", task)
	}
	if task.StartedAt == nil || task.CompletedAt != nil || task.LastHeartbeatAt == nil {
		t.Fatalf("task times incorrect: %+v", task)
	}

	if len(snap.Runs) != 1 {
		t.Fatalf("len(Runs) = %d, want 1", len(snap.Runs))
	}
	run := snap.Runs[0]
	if run.ID != 9 || run.TaskID != "task-1" || run.Profile != "" || run.StepKey != "" || run.Status != "running" {
		t.Fatalf("run mapping incorrect: %+v", run)
	}
	if run.WorkerPID == nil || *run.WorkerPID != 4321 || run.MaxRuntimeSeconds == nil || *run.MaxRuntimeSeconds != 900 {
		t.Fatalf("run numeric pointers incorrect: %+v", run)
	}
	if run.LastHeartbeatAt == nil || run.EndedAt != nil || run.Outcome != "" {
		t.Fatalf("run mapping incorrect: %+v", run)
	}
}

func TestSQLiteCollectorRejectsCanceledContext(t *testing.T) {
	dbPath := createCollectorFixture(t, nil)
	c := &SQLiteCollector{Path: dbPath}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Snapshot(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Snapshot() error = %v, want context.Canceled", err)
	}
}

func TestSQLiteCollectorUsesReadOnlyDatabaseAccess(t *testing.T) {
	dbPath := createCollectorFixture(t, func(db *sql.DB) {
		mustExec(t, db, `CREATE TABLE tasks (id TEXT PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL, assignee TEXT NOT NULL, priority INTEGER NOT NULL, created_at TEXT NOT NULL, started_at TEXT, completed_at TEXT, workspace_kind TEXT NOT NULL, branch_name TEXT NOT NULL, project_id TEXT NOT NULL, tenant TEXT NOT NULL, consecutive_failures INTEGER NOT NULL, worker_pid INTEGER, max_runtime_seconds INTEGER, last_heartbeat_at TEXT, current_run_id INTEGER, block_kind TEXT NOT NULL, body TEXT NOT NULL, result TEXT NOT NULL)`)
		mustExec(t, db, `CREATE TABLE task_runs (id INTEGER PRIMARY KEY, task_id TEXT NOT NULL, profile TEXT NOT NULL, step_key TEXT NOT NULL, status TEXT NOT NULL, worker_pid INTEGER, max_runtime_seconds INTEGER, last_heartbeat_at TEXT, started_at TEXT NOT NULL, ended_at TEXT, outcome TEXT NOT NULL, summary TEXT NOT NULL, metadata TEXT NOT NULL, error TEXT NOT NULL, skills TEXT NOT NULL, model_override TEXT NOT NULL, session_id TEXT NOT NULL, claim TEXT NOT NULL)`)
	})

	c := &SQLiteCollector{Path: dbPath}
	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat(before) error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snap.Tasks) != 0 || len(snap.Runs) != 0 {
		t.Fatalf("Snapshot() returned unexpected rows: %+v", snap)
	}
	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("Stat(after) error = %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("collector modified db mtime: before=%v after=%v", before.ModTime(), after.ModTime())
	}
}

func TestNewSQLiteCollectorRejectsUnsafePath(t *testing.T) {
	if _, err := NewSQLiteCollector("../escape.db"); err == nil {
		t.Fatal("NewSQLiteCollector() error = nil, want path validation failure")
	}
}

func createCollectorFixture(t *testing.T, setup func(*sql.DB)) string {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kanban.db")
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

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("Exec(%q) error = %v", query, err)
	}
}
