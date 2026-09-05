package connector

import (
	"context"
	"database/sql"
	"fmt"
	"math"
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

// SessionSummary 是单条 Hermes 会话的脱敏只读视图，数据来自可选配置的
// 本地 Hermes state.db。字段全部来自显式的非敏感列白名单；任何可能携带
// 用户/agent 内容或标识符的列——user_id、chat_id、thread_id、
// session_key、origin_json、system_prompt、cwd、git 路径/分支、
// 计费 URL、活动描述、消息正文、工具参数/结果、prompt、推理内容、
// 密钥以及成本浮点数——都被有意排除在外。
type SessionSummary struct {
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	Source           string     `json:"source"`
	Model            string     `json:"model"`
	ProfileName      string     `json:"profile_name"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	LastActivityAt   *time.Time `json:"last_activity_at,omitempty"`
	MessageCount     int        `json:"message_count"`
	ToolCallCount    int        `json:"tool_call_count"`
	InputTokens      int64      `json:"input_tokens"`
	OutputTokens     int64      `json:"output_tokens"`
	CacheReadTokens  int64      `json:"cache_read_tokens"`
	CacheWriteTokens int64      `json:"cache_write_tokens"`
	ReasoningTokens  int64      `json:"reasoning_tokens"`
	Pinned           bool       `json:"pinned"`
	Archived         bool       `json:"archived"`
}

// Snapshot 是 Hermes kanban 与会话状态在某一时间点的完整只读快照。
type Snapshot struct {
	TakenAt  time.Time        `json:"taken_at"`
	Tasks    []AgentTask      `json:"tasks"`
	Runs     []TaskRun        `json:"runs"`
	Sessions []SessionSummary `json:"sessions"`
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
	// StateDBPath 可选地指向本地 Hermes state.db，用于采集脱敏后的会话
	// 元数据。留空则禁用会话采集，保持仅 Kanban 的既有行为，向后兼容。
	StateDBPath string
}

// NewSQLiteCollector 校验并保存 Kanban 数据库路径，供后续读取使用。
// 会话采集处于禁用状态；若还需采集脱敏会话元数据，请使用
// NewSQLiteCollectorWithStateDB。
func NewSQLiteCollector(path string) (*SQLiteCollector, error) {
	return NewSQLiteCollectorWithStateDB(path, "")
}

// NewSQLiteCollectorWithStateDB 同时校验并保存 Kanban 数据库路径和可选的
// Hermes state.db 路径。stateDBPath 为空时禁用会话采集，保持既有的
// 仅 Kanban 行为。
func NewSQLiteCollectorWithStateDB(path, stateDBPath string) (*SQLiteCollector, error) {
	if err := validateSQLitePath(path); err != nil {
		return nil, err
	}
	if strings.TrimSpace(stateDBPath) != "" {
		if err := validateSQLitePath(stateDBPath); err != nil {
			return nil, fmt.Errorf("invalid hermes state db path: %w", err)
		}
	}
	return &SQLiteCollector{Path: path, StateDBPath: stateDBPath}, nil
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

	sessions, err := c.collectSessions(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	return Snapshot{TakenAt: time.Now().UTC(), Tasks: tasks, Runs: runs, Sessions: sessions}, nil
}

// collectSessions returns sanitized session metadata from StateDBPath, or an
// empty slice with no error when StateDBPath is unset (session collection
// disabled). A configured-but-unreadable state.db fails the whole call
// rather than silently returning partial, misleading data.
func (c *SQLiteCollector) collectSessions(ctx context.Context) ([]SessionSummary, error) {
	if strings.TrimSpace(c.StateDBPath) == "" {
		return []SessionSummary{}, nil
	}
	if err := validateSQLitePath(c.StateDBPath); err != nil {
		return nil, fmt.Errorf("invalid hermes state db path: %w", err)
	}

	db, err := sql.Open("sqlite", readOnlyDSN(c.StateDBPath))
	if err != nil {
		return nil, fmt.Errorf("open hermes state database: %w", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping hermes state database: %w", err)
	}

	return readSessions(ctx, db)
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

// maxSessions caps the number of sessions collected per snapshot, bounding
// payload size regardless of how much session history Hermes retains.
const maxSessions = 200

// readSessions collects sanitized session metadata via an explicit column
// allowlist (see SessionSummary), excluding hidden sessions, newest
// activity/start first, capped at maxSessions.
func readSessions(ctx context.Context, db *sql.DB) ([]SessionSummary, error) {
	const query = `
		SELECT id, title, source, model, profile_name, started_at, ended_at, last_activity_at,
			message_count, tool_call_count, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, reasoning_tokens, pinned, archived
		FROM sessions
		WHERE hidden = 0
		ORDER BY COALESCE(last_activity_at, started_at) DESC
		LIMIT ?
	`
	rows, err := db.QueryContext(ctx, query, maxSessions)
	if err != nil {
		return nil, fmt.Errorf("query hermes sessions: %w", err)
	}
	defer rows.Close()

	sessions := []SessionSummary{}
	for rows.Next() {
		var (
			session                       SessionSummary
			model, profileName            sql.NullString
			startedAtUnix                 sql.NullFloat64
			endedAtUnix, lastActivityUnix sql.NullFloat64
			pinned, archived              int64
		)
		if err := rows.Scan(
			&session.ID,
			&session.Title,
			&session.Source,
			&model,
			&profileName,
			&startedAtUnix,
			&endedAtUnix,
			&lastActivityUnix,
			&session.MessageCount,
			&session.ToolCallCount,
			&session.InputTokens,
			&session.OutputTokens,
			&session.CacheReadTokens,
			&session.CacheWriteTokens,
			&session.ReasoningTokens,
			&pinned,
			&archived,
		); err != nil {
			return nil, fmt.Errorf("scan hermes session row: %w", err)
		}
		session.Title = truncateRunes(session.Title, 256)
		session.Source = truncateRunes(session.Source, 128)
		session.Model = truncateRunes(nullableString(model), 128)
		session.ProfileName = truncateRunes(nullableString(profileName), 128)
		startedAt, err := parseRequiredUnixSecondsReal(startedAtUnix)
		if err != nil {
			return nil, fmt.Errorf("parse hermes session started_at: %w", err)
		}
		session.StartedAt = startedAt
		session.EndedAt = parseNullableUnixSecondsReal(endedAtUnix)
		session.LastActivityAt = parseNullableUnixSecondsReal(lastActivityUnix)
		session.Pinned = pinned != 0
		session.Archived = archived != 0
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hermes sessions: %w", err)
	}
	return sessions, nil
}

// parseRequiredUnixSecondsReal parses a required REAL Unix-seconds column
// (possibly with a fractional part), erroring on NULL or an invalid value.
func parseRequiredUnixSecondsReal(v sql.NullFloat64) (time.Time, error) {
	if !v.Valid {
		return time.Time{}, fmt.Errorf("missing unix seconds value")
	}
	return unixSecondsRealToTime(v.Float64)
}

// parseNullableUnixSecondsReal parses an optional REAL Unix-seconds column,
// returning nil on NULL or an invalid value rather than failing the whole
// collection for a non-critical timestamp.
func parseNullableUnixSecondsReal(v sql.NullFloat64) *time.Time {
	if !v.Valid {
		return nil
	}
	t, err := unixSecondsRealToTime(v.Float64)
	if err != nil {
		return nil
	}
	return &t
}

func unixSecondsRealToTime(v float64) (time.Time, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return time.Time{}, fmt.Errorf("unix seconds must be a finite, non-negative number: %v", v)
	}
	sec := math.Trunc(v)
	nsec := int64(math.Round((v - sec) * float64(time.Second)))
	return time.Unix(int64(sec), nsec).UTC(), nil
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
