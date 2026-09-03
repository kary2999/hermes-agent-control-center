package connector

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// AgentTask is the sanitized, read-only view of one Hermes kanban task
// (docs/development-plan.md Task 5). Fields are drawn from an explicit
// allowlist of non-sensitive columns in the “tasks“ table of Hermes's
// kanban.db. Free-text fields that can carry agent-generated content —
// “body“, “result“, and any run “summary“/“error“/“metadata“ — are
// intentionally excluded; only structural/operational fields with bounded,
// well-known vocabularies are collected.
type AgentTask struct {
	ID                  string     `json:"id"`
	Title               string     `json:"title"`
	Status              string     `json:"status"`
	Assignee            string     `json:"assignee,omitempty"`
	Priority            int        `json:"priority"`
	CreatedAt           time.Time  `json:"created_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	WorkspaceKind       string     `json:"workspace_kind,omitempty"`
	BranchName          string     `json:"branch_name,omitempty"`
	ProjectID           string     `json:"project_id,omitempty"`
	Tenant              string     `json:"tenant,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	WorkerPID           *int       `json:"worker_pid,omitempty"`
	MaxRuntimeSeconds   *int       `json:"max_runtime_seconds,omitempty"`
	LastHeartbeatAt     *time.Time `json:"last_heartbeat_at,omitempty"`
	CurrentRunID        *int64     `json:"current_run_id,omitempty"`
	BlockKind           string     `json:"block_kind,omitempty"`
}

// TaskRun is the sanitized, read-only view of one Hermes kanban task_runs
// row — a single worker execution attempt for a task. See AgentTask for the
// allowlist rationale.
type TaskRun struct {
	ID                int64      `json:"id"`
	TaskID            string     `json:"task_id"`
	Profile           string     `json:"profile,omitempty"`
	StepKey           string     `json:"step_key,omitempty"`
	Status            string     `json:"status"`
	WorkerPID         *int       `json:"worker_pid,omitempty"`
	MaxRuntimeSeconds *int       `json:"max_runtime_seconds,omitempty"`
	LastHeartbeatAt   *time.Time `json:"last_heartbeat_at,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	Outcome           string     `json:"outcome,omitempty"`
}

// Snapshot is a full point-in-time read of allowed Hermes kanban state.
type Snapshot struct {
	TakenAt time.Time  `json:"taken_at"`
	Tasks   []AgentTask `json:"tasks"`
	Runs    []TaskRun   `json:"runs"`
}

// ChangeKind identifies what kind of mutation a Change describes.
type ChangeKind string

const (
	ChangeTaskAdded   ChangeKind = "task_added"
	ChangeTaskUpdated ChangeKind = "task_updated"
	ChangeTaskRemoved ChangeKind = "task_removed"
	ChangeRunAdded    ChangeKind = "run_added"
	ChangeRunUpdated  ChangeKind = "run_updated"
	ChangeRunRemoved  ChangeKind = "run_removed"
)

// Change describes one detected mutation between two Snapshots. Exactly one
// of Task or Run is non-nil, matching Kind.
type Change struct {
	Kind ChangeKind
	Task *AgentTask
	Run  *TaskRun
}

// Collector is a read-only source of sanitized Hermes agent/task state.
type Collector interface {
	// Snapshot returns a full point-in-time read of allowed Hermes state.
	// Implementations must not mutate any Hermes-owned file or database.
	Snapshot(ctx context.Context) (Snapshot, error)
}

// SQLiteCollector reads sanitized Hermes kanban state from a local SQLite
// database using a read-only connection.
type SQLiteCollector struct {
	Path string
}

// NewSQLiteCollector validates and stores a database path for later reads.
func NewSQLiteCollector(path string) (*SQLiteCollector, error) {
	if err := validateSQLitePath(path); err != nil {
		return nil, err
	}
	return &SQLiteCollector{Path: path}, nil
}

func validateSQLitePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("sqlite path is empty")
	}
	if filepath.IsAbs(path) == false {
		return fmt.Errorf("sqlite path must be absolute: %q", path)
	}
	clean := filepath.Clean(path)
	if clean != path {
		return fmt.Errorf("sqlite path must be clean and canonical: %q", path)
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("sqlite path must not contain traversal segments: %q", path)
	}
	return nil
}

func (c *SQLiteCollector) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if err := validateSQLitePath(c.Path); err != nil {
		return Snapshot{}, err
	}

	db, err := sql.Open("sqlite", readOnlyDSN(c.Path))
	if err != nil {
		return Snapshot{}, fmt.Errorf("open sqlite database: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("ping sqlite database: %w", err)
	}

	tasks, err := readTasks(ctx, db)
	if err != nil {
		return Snapshot{}, err
	}
	runs, err := readRuns(ctx, db)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{TakenAt: time.Now().UTC(), Tasks: tasks, Runs: runs}, nil
}

func readOnlyDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("_query_only", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

func readTasks(ctx context.Context, db *sql.DB) ([]AgentTask, error) {
	const query = `
		SELECT id, title, status, assignee, priority, created_at, started_at, completed_at,
			workspace_kind, branch_name, project_id, tenant, consecutive_failures,
			worker_pid, max_runtime_seconds, last_heartbeat_at, current_run_id, block_kind
		FROM tasks
		ORDER BY id
	`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer rows.Close()

	var tasks []AgentTask
	for rows.Next() {
		var (
			task                               AgentTask
			assignee                           sql.NullString
			branchName                         sql.NullString
			projectID                          sql.NullString
			tenant                             sql.NullString
			blockKind                          sql.NullString
			createdAtUnix, startedAtUnix       sql.NullInt64
			completedAtUnix, lastHeartbeatUnix sql.NullInt64
			workerPID                          sql.NullInt64
			maxRuntimeSeconds                  sql.NullInt64
			currentRunID                       sql.NullInt64
		)
		if err := rows.Scan(
			&task.ID,
			&task.Title,
			&task.Status,
			&assignee,
			&task.Priority,
			&createdAtUnix,
			&startedAtUnix,
			&completedAtUnix,
			&task.WorkspaceKind,
			&branchName,
			&projectID,
			&tenant,
			&task.ConsecutiveFailures,
			&workerPID,
			&maxRuntimeSeconds,
			&lastHeartbeatUnix,
			&currentRunID,
			&blockKind,
		); err != nil {
			return nil, fmt.Errorf("scan task row: %w", err)
		}
		task.Assignee = nullableString(assignee)
		task.BranchName = nullableString(branchName)
		task.ProjectID = nullableString(projectID)
		task.Tenant = nullableString(tenant)
		task.BlockKind = nullableString(blockKind)
		createdAt, err := parseRequiredUnixTime(createdAtUnix)
		if err != nil {
			return nil, fmt.Errorf("parse task created_at: %w", err)
		}
		task.CreatedAt = createdAt
		task.Title = truncateRunes(task.Title, 256)
		task.StartedAt = parseNullableUnixTime(startedAtUnix)
		task.CompletedAt = parseNullableUnixTime(completedAtUnix)
		task.LastHeartbeatAt = parseNullableUnixTime(lastHeartbeatUnix)
		task.WorkerPID = nullableInt(workerPID)
		task.MaxRuntimeSeconds = nullableInt(maxRuntimeSeconds)
		task.CurrentRunID = nullableInt64(currentRunID)
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tasks: %w", err)
	}
	return tasks, nil
}

func readRuns(ctx context.Context, db *sql.DB) ([]TaskRun, error) {
	const query = `
		SELECT id, task_id, profile, step_key, status, worker_pid, max_runtime_seconds,
			last_heartbeat_at, started_at, ended_at, outcome
		FROM task_runs
		ORDER BY id
	`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query task runs: %w", err)
	}
	defer rows.Close()

	var runs []TaskRun
	for rows.Next() {
		var (
			run                            TaskRun
			profile                        sql.NullString
			stepKey                        sql.NullString
			outcome                        sql.NullString
			startedAtUnix                  sql.NullInt64
			lastHeartbeatUnix, endedAtUnix sql.NullInt64
			workerPID                      sql.NullInt64
			maxRuntimeSeconds              sql.NullInt64
		)
		if err := rows.Scan(
			&run.ID,
			&run.TaskID,
			&profile,
			&stepKey,
			&run.Status,
			&workerPID,
			&maxRuntimeSeconds,
			&lastHeartbeatUnix,
			&startedAtUnix,
			&endedAtUnix,
			&outcome,
		); err != nil {
			return nil, fmt.Errorf("scan run row: %w", err)
		}
		run.Profile = nullableString(profile)
		run.StepKey = nullableString(stepKey)
		run.Outcome = nullableString(outcome)
		startedAt, err := parseRequiredUnixTime(startedAtUnix)
		if err != nil {
			return nil, fmt.Errorf("parse run started_at: %w", err)
		}
		run.StartedAt = startedAt
		run.WorkerPID = nullableInt(workerPID)
		run.MaxRuntimeSeconds = nullableInt(maxRuntimeSeconds)
		run.LastHeartbeatAt = parseNullableUnixTime(lastHeartbeatUnix)
		run.EndedAt = parseNullableUnixTime(endedAtUnix)
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task runs: %w", err)
	}
	return runs, nil
}

func parseRequiredUnixTime(v sql.NullInt64) (time.Time, error) {
	if !v.Valid {
		return time.Time{}, fmt.Errorf("missing unix seconds value")
	}
	return unixSecondsToTime(v.Int64)
}

func parseNullableUnixTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t, err := unixSecondsToTime(v.Int64)
	if err != nil {
		return nil
	}
	return &t
}

func unixSecondsToTime(v int64) (time.Time, error) {
	if v < 0 {
		return time.Time{}, fmt.Errorf("unix seconds must be non-negative: %d", v)
	}
	return time.Unix(v, 0).UTC(), nil
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func nullableInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func nullableInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func nullableString(v sql.NullString) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

// Diff compares two Snapshots and returns the changes needed to bring prev
// up to date with next, ordered deterministically: task changes (sorted by
// task ID) before run changes (sorted by run ID).
func Diff(prev, next Snapshot) []Change {
	changes := append(diffTasks(prev.Tasks, next.Tasks), diffRuns(prev.Runs, next.Runs)...)
	return changes
}

func diffTasks(prev, next []AgentTask) []Change {
	prevByID := make(map[string]AgentTask, len(prev))
	for _, t := range prev {
		prevByID[t.ID] = t
	}
	nextByID := make(map[string]AgentTask, len(next))
	for _, t := range next {
		nextByID[t.ID] = t
	}

	var changes []Change
	for id, n := range nextByID {
		if p, ok := prevByID[id]; !ok {
			changes = append(changes, Change{Kind: ChangeTaskAdded, Task: &n})
		} else if !reflect.DeepEqual(p, n) {
			changes = append(changes, Change{Kind: ChangeTaskUpdated, Task: &n})
		}
	}
	for id, p := range prevByID {
		if _, ok := nextByID[id]; !ok {
			changes = append(changes, Change{Kind: ChangeTaskRemoved, Task: &p})
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Task.ID < changes[j].Task.ID })
	return changes
}

func diffRuns(prev, next []TaskRun) []Change {
	prevByID := make(map[int64]TaskRun, len(prev))
	for _, r := range prev {
		prevByID[r.ID] = r
	}
	nextByID := make(map[int64]TaskRun, len(next))
	for _, r := range next {
		nextByID[r.ID] = r
	}

	var changes []Change
	for id, n := range nextByID {
		if p, ok := prevByID[id]; !ok {
			changes = append(changes, Change{Kind: ChangeRunAdded, Run: &n})
		} else if !reflect.DeepEqual(p, n) {
			changes = append(changes, Change{Kind: ChangeRunUpdated, Run: &n})
		}
	}
	for id, p := range prevByID {
		if _, ok := nextByID[id]; !ok {
			changes = append(changes, Change{Kind: ChangeRunRemoved, Run: &p})
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Run.ID < changes[j].Run.ID })
	return changes
}
