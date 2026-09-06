package relay

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// SnapshotStore holds the single most recently received Connector snapshot
// in memory. There is no persistence: a Relay restart starts empty and
// waits for the next report from the Connector.
type SnapshotStore struct {
	mu         sync.RWMutex
	deviceID   string
	snapshot   Snapshot
	receivedAt time.Time
	has        bool
}

// NewSnapshotStore returns an empty SnapshotStore.
func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{}
}

// Set replaces the stored snapshot with a newly received one.
func (s *SnapshotStore) Set(deviceID string, snap Snapshot, receivedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviceID = deviceID
	s.snapshot = snap
	s.receivedAt = receivedAt
	s.has = true
}

// Get returns the stored snapshot, or has=false if none has been received
// yet.
func (s *SnapshotStore) Get() (deviceID string, snap Snapshot, receivedAt time.Time, has bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.deviceID, s.snapshot, s.receivedAt, s.has
}

// AgentSummary aggregates unfinished/running task counts for one assignee.
type AgentSummary struct {
	Assignee        string `json:"assignee"`
	UnfinishedCount int    `json:"unfinished_count"`
	RunningCount    int    `json:"running_count"`
}

// TaskView is the display-ready representation of one task, with a basic
// progress indicator derived from its status and (if any) current run.
type TaskView struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Status          string     `json:"status"`
	Assignee        string     `json:"assignee"`
	Priority        int        `json:"priority"`
	Stage           string     `json:"stage"`
	ProgressPercent int        `json:"progress_percent"`
	IsRunning       bool       `json:"is_running"`
	BranchName      string     `json:"branch_name,omitempty"`
	ProjectID       string     `json:"project_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

// SessionView 是 GET /api/v1/dashboard 返回的、面向 dashboard 的显式
// 安全会话形状。它由 SessionSummary 逐字段构建（绝不整体重新序列化），
// 因此未来即便给 Snapshot 线上协议新增字段，也不会自动泄漏到
// dashboard API 中。
type SessionView struct {
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

// DashboardView is the JSON shape served from GET /api/v1/dashboard.
type DashboardView struct {
	GeneratedAt      time.Time      `json:"generated_at"`
	HasSnapshot      bool           `json:"has_snapshot"`
	DeviceID         string         `json:"device_id,omitempty"`
	SnapshotTakenAt  *time.Time     `json:"snapshot_taken_at,omitempty"`
	SnapshotReceived *time.Time     `json:"snapshot_received_at,omitempty"`
	Agents           []AgentSummary `json:"agents"`
	ActiveTasks      []TaskView     `json:"active_tasks"`
	RecentCompleted  []TaskView     `json:"recent_completed"`
	Sessions         []SessionView  `json:"sessions"`
}

// maxRecentCompleted bounds how many completed tasks the dashboard shows,
// keeping the payload and page small regardless of board history size.
const maxRecentCompleted = 20

const unassignedLabel = "未分配"

// BuildDashboard maps a raw Connector snapshot into the display-ready
// DashboardView used by both the JSON API and the embedded HTML page.
func BuildDashboard(deviceID string, snap Snapshot, receivedAt time.Time, hasSnapshot bool, now time.Time) DashboardView {
	view := DashboardView{
		GeneratedAt: now,
		HasSnapshot: hasSnapshot,
		Agents:      []AgentSummary{},
		ActiveTasks: []TaskView{},
		Sessions:    []SessionView{},
	}
	if !hasSnapshot {
		view.RecentCompleted = []TaskView{}
		return view
	}
	view.DeviceID = deviceID
	takenAt := snap.TakenAt
	view.SnapshotTakenAt = &takenAt
	received := receivedAt
	view.SnapshotReceived = &received

	runsByID := make(map[int64]TaskRun, len(snap.Runs))
	for _, run := range snap.Runs {
		runsByID[run.ID] = run
	}

	agentCounts := make(map[string]*AgentSummary)
	// active/completed 必须以空切片字面量初始化，不能用
	// `var active, completed []TaskView`（nil 切片）：snap.Tasks 为空时
	// append 从不触发，nil 会原样赋给 view.ActiveTasks /
	// view.RecentCompleted，json.Marshal 把 nil 切片序列化成 null 而不是
	// []，工作台前端的严格校验器（Array.isArray）会因此判定整份 payload
	// 非法。
	active := []TaskView{}
	completed := []TaskView{}

	for _, task := range snap.Tasks {
		percent, stage, running := deriveProgress(task, runsByID)
		tv := TaskView{
			ID:              task.ID,
			Title:           task.Title,
			Status:          task.Status,
			Assignee:        task.Assignee,
			Priority:        task.Priority,
			Stage:           stage,
			ProgressPercent: percent,
			IsRunning:       running,
			BranchName:      task.BranchName,
			ProjectID:       task.ProjectID,
			CreatedAt:       task.CreatedAt,
			StartedAt:       task.StartedAt,
			CompletedAt:     task.CompletedAt,
		}

		if task.CompletedAt != nil {
			completed = append(completed, tv)
			continue
		}

		active = append(active, tv)
		assignee := task.Assignee
		if strings.TrimSpace(assignee) == "" {
			assignee = unassignedLabel
		}
		summary, ok := agentCounts[assignee]
		if !ok {
			summary = &AgentSummary{Assignee: assignee}
			agentCounts[assignee] = summary
		}
		summary.UnfinishedCount++
		if running {
			summary.RunningCount++
		}
	}

	sort.Slice(active, func(i, j int) bool {
		if active[i].IsRunning != active[j].IsRunning {
			return active[i].IsRunning
		}
		if active[i].Priority != active[j].Priority {
			return active[i].Priority > active[j].Priority
		}
		return active[i].CreatedAt.Before(active[j].CreatedAt)
	})

	sort.Slice(completed, func(i, j int) bool {
		return completed[i].CompletedAt.After(*completed[j].CompletedAt)
	})
	if len(completed) > maxRecentCompleted {
		completed = completed[:maxRecentCompleted]
	}

	agents := make([]AgentSummary, 0, len(agentCounts))
	for _, summary := range agentCounts {
		agents = append(agents, *summary)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Assignee < agents[j].Assignee })

	view.Agents = agents
	view.ActiveTasks = active
	view.RecentCompleted = completed
	view.Sessions = buildSessionViews(snap.Sessions)
	return view
}

// buildSessionViews maps each sanitized SessionSummary to its explicit
// dashboard-safe SessionView field-by-field, preserving the Connector's
// newest-first ordering.
func buildSessionViews(sessions []SessionSummary) []SessionView {
	views := make([]SessionView, 0, len(sessions))
	for _, s := range sessions {
		views = append(views, SessionView{
			ID:               s.ID,
			Title:            s.Title,
			Source:           s.Source,
			Model:            s.Model,
			ProfileName:      s.ProfileName,
			StartedAt:        s.StartedAt,
			EndedAt:          s.EndedAt,
			LastActivityAt:   s.LastActivityAt,
			MessageCount:     s.MessageCount,
			ToolCallCount:    s.ToolCallCount,
			InputTokens:      s.InputTokens,
			OutputTokens:     s.OutputTokens,
			CacheReadTokens:  s.CacheReadTokens,
			CacheWriteTokens: s.CacheWriteTokens,
			ReasoningTokens:  s.ReasoningTokens,
			Pinned:           s.Pinned,
			Archived:         s.Archived,
		})
	}
	return views
}

// deriveProgress computes a basic (percent, stage, isRunning) indicator for
// a task from its own status plus its current run's status/step, when
// known. This is intentionally a coarse heuristic — Hermes does not expose
// fine-grained task progress.
func deriveProgress(task AgentTask, runsByID map[int64]TaskRun) (percent int, stage string, running bool) {
	if task.CompletedAt != nil {
		return 100, "已完成", false
	}

	if task.CurrentRunID != nil {
		if run, ok := runsByID[*task.CurrentRunID]; ok {
			runStage := firstNonEmpty(run.StepKey, run.Profile)
			switch strings.ToLower(run.Status) {
			case "running":
				return 60, orDefault(runStage, "执行中"), true
			case "blocked":
				return 40, orDefault(runStage, "阻塞"), false
			case "crashed", "timed_out", "failed":
				return 30, orDefault(runStage, "失败"), false
			case "done":
				return 90, orDefault(runStage, "收尾中"), true
			default:
				return 50, orDefault(runStage, run.Status), false
			}
		}
	}

	if task.StartedAt != nil {
		return 30, "执行中", true
	}
	if task.BlockKind != "" {
		return 20, "阻塞: " + task.BlockKind, false
	}
	return 0, "等待中", false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
