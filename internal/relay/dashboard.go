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

// AgentSummary 按会话所用的模型（而非任务 assignee）聚合统计。"Agent" 在这
// 里指模型类型：同一模型下的所有会话汇总成一条记录，包含会话总数/进行中
// 会话数，以及各类 token 用量之和。模型为空或未知时使用中文兜底标签
// unknownModelLabel，保证该字段永不为空。
type AgentSummary struct {
	Model              string `json:"model"`
	TotalSessionCount  int    `json:"total_session_count"`
	ActiveSessionCount int    `json:"active_session_count"`
	InputTokens        int64  `json:"input_tokens"`
	OutputTokens       int64  `json:"output_tokens"`
	CacheReadTokens    int64  `json:"cache_read_tokens"`
	CacheWriteTokens   int64  `json:"cache_write_tokens"`
	ReasoningTokens    int64  `json:"reasoning_tokens"`
}

// unknownModelLabel 是会话 model 字段为空/未知时的中文兜底，用于
// AgentSummary.Model 与 TaskView.ResponsibleAgent，保证两者都不会展示为空。
const unknownModelLabel = "未知模型"

// unresolvedAgentLabel 用于任务当前没有可关联到会话的运行时的负责 Agent
// 兜底：任务的负责 Agent 必须来自其当前运行所绑定会话的模型，而不是
// assignee；查不到时展示这个中文提示，而不是静默复用 assignee。
const unresolvedAgentLabel = "暂无关联 Agent"

// TaskView is the display-ready representation of one task, with a basic
// progress indicator derived from its status and (if any) current run.
type TaskView struct {
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	Status           string     `json:"status"`
	Assignee         string     `json:"assignee"`
	ResponsibleAgent string     `json:"responsible_agent"`
	Priority         int        `json:"priority"`
	Stage            string     `json:"stage"`
	ProgressPercent  int        `json:"progress_percent"`
	IsRunning        bool       `json:"is_running"`
	BranchName       string     `json:"branch_name,omitempty"`
	ProjectID        string     `json:"project_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	// LastRunAt 是该任务在 task_runs 中最近一次运行的 started_at；没有任何
	// 运行记录时为空。
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
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
	// LastUserPrompt 是该会话最新一条脱敏用户提示词预览，供会话搜索与详情
	// 展示；绝不包含 assistant/tool/system/reasoning 内容或完整历史。
	LastUserPrompt string `json:"last_user_prompt,omitempty"`
	// LastUserPromptAt 是上述预览对应消息的时间戳。
	LastUserPromptAt *time.Time `json:"last_user_prompt_at,omitempty"`
	HandoffState     string     `json:"handoff_state,omitempty"`
	HandoffPlatform  string     `json:"handoff_platform,omitempty"`
}

// DashboardView is the JSON shape served from GET /api/v1/dashboard.
type DashboardView struct {
	GeneratedAt      time.Time      `json:"generated_at"`
	HasSnapshot      bool           `json:"has_snapshot"`
	DeviceID         string         `json:"device_id,omitempty"`
	SnapshotTakenAt  *time.Time     `json:"snapshot_taken_at,omitempty"`
	SnapshotReceived *time.Time     `json:"snapshot_received_at,omitempty"`
	Agents           []AgentSummary `json:"agents"`
	// ActiveTasks 保留用于向后兼容；总览 UI 已改用 RecentTasks，不再消费
	// 本字段。
	ActiveTasks     []TaskView `json:"active_tasks"`
	RecentCompleted []TaskView `json:"recent_completed"`
	// RecentTasks 是总览"最近执行任务"的数据源：按 task_id 聚合
	// task_runs，取每个任务最近一次运行的时间，仅包含至少有一条运行记录
	// 的任务，按该时间倒序，最多 maxRecentTasks 条。运行中/完成/失败的任务
	// 都可能出现，各自保留其明确状态。
	RecentTasks []TaskView `json:"recent_tasks"`
	// AllTasks 包含全部任务（进行中/已完成/失败），供顶层"任务"页面按
	// all/active/completed/failed 筛选展示，也是总览任务数指标的数据源。
	AllTasks []TaskView    `json:"all_tasks"`
	Sessions []SessionView `json:"sessions"`
	// LarkHandoffAvailable 仅在 Relay 配置了独立写授权，且 Connector
	// 明确报告本地 handoff 能力时为 true。
	LarkHandoffAvailable bool `json:"lark_handoff_available"`
	// LarkHandoffReason 说明当前环境为什么不可用，供前端在“在 Lark 继续”
	// 按钮旁展示简明原因。
	LarkHandoffReason string `json:"lark_handoff_reason,omitempty"`
}

// larkHandoffUnavailableReason 不暴露内部配置和部署细节。
const larkHandoffUnavailableReason = "当前环境尚未启用 Lark 会话交接。"

// maxRecentCompleted bounds how many completed tasks the dashboard shows,
// keeping the payload and page small regardless of board history size.
const maxRecentCompleted = 20

const maxRecentTasks = 20

// BuildDashboard maps a raw Connector snapshot into the display-ready
// DashboardView used by both the JSON API and the embedded HTML page.
func BuildDashboard(deviceID string, snap Snapshot, receivedAt time.Time, hasSnapshot bool, now time.Time) DashboardView {
	return BuildDashboardWithHandoff(deviceID, snap, receivedAt, hasSnapshot, now, false)
}

func BuildDashboardWithHandoff(deviceID string, snap Snapshot, receivedAt time.Time, hasSnapshot bool, now time.Time, relayHandoffConfigured bool) DashboardView {
	view := DashboardView{
		GeneratedAt:          now,
		HasSnapshot:          hasSnapshot,
		Agents:               []AgentSummary{},
		ActiveTasks:          []TaskView{},
		RecentTasks:          []TaskView{},
		AllTasks:             []TaskView{},
		Sessions:             []SessionView{},
		LarkHandoffAvailable: relayHandoffConfigured && hasSnapshot && snap.Capabilities.LarkHandoff,
		LarkHandoffReason:    larkHandoffUnavailableReason,
	}
	if view.LarkHandoffAvailable {
		view.LarkHandoffReason = ""
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
	latestRunByTaskID := make(map[string]TaskRun, len(snap.Runs))
	for _, run := range snap.Runs {
		runsByID[run.ID] = run
		latest, ok := latestRunByTaskID[run.TaskID]
		if !ok || run.StartedAt.After(latest.StartedAt) || (run.StartedAt.Equal(latest.StartedAt) && run.ID > latest.ID) {
			latestRunByTaskID[run.TaskID] = run
		}
	}
	sessionsByID := make(map[string]SessionSummary, len(snap.Sessions))
	for _, s := range snap.Sessions {
		sessionsByID[s.ID] = s
	}

	// active/completed 必须以空切片字面量初始化，不能用
	// `var active, completed []TaskView`（nil 切片）：snap.Tasks 为空时
	// append 从不触发，nil 会原样赋给 view.ActiveTasks /
	// view.RecentCompleted，json.Marshal 把 nil 切片序列化成 null 而不是
	// []，工作台前端的严格校验器（Array.isArray）会因此判定整份 payload
	// 非法。
	active := []TaskView{}
	completed := []TaskView{}
	recent := []TaskView{}
	all := []TaskView{}

	for _, task := range snap.Tasks {
		percent, stage, running := deriveProgress(task, runsByID)
		tv := TaskView{
			ID:               task.ID,
			Title:            task.Title,
			Status:           task.Status,
			Assignee:         task.Assignee,
			ResponsibleAgent: deriveResponsibleAgent(task, runsByID, sessionsByID),
			Priority:         task.Priority,
			Stage:            stage,
			ProgressPercent:  percent,
			IsRunning:        running,
			BranchName:       task.BranchName,
			ProjectID:        task.ProjectID,
			CreatedAt:        task.CreatedAt,
			StartedAt:        task.StartedAt,
			CompletedAt:      task.CompletedAt,
		}
		if latestRun, ok := latestRunByTaskID[task.ID]; ok {
			rv := tv
			latest := latestRun.StartedAt
			rv.LastRunAt = &latest
			rv.Status = latestRun.Status
			if latestRun.ID != 0 {
				latestRunID := latestRun.ID
				recentTask := task
				recentTask.CurrentRunID = &latestRunID
				percent, stage, running = deriveProgress(recentTask, runsByID)
				rv.ProgressPercent = percent
				rv.Stage = stage
				rv.IsRunning = running
			}
			recent = append(recent, rv)
		}
		all = append(all, tv)

		if isFailedTaskStatus(task.Status) {
			continue
		}
		if task.CompletedAt != nil {
			completed = append(completed, tv)
			continue
		}
		active = append(active, tv)
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
	sort.Slice(recent, func(i, j int) bool {
		if recent[i].LastRunAt.Equal(*recent[j].LastRunAt) {
			return recent[i].ID < recent[j].ID
		}
		return recent[i].LastRunAt.After(*recent[j].LastRunAt)
	})

	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })

	if len(completed) > maxRecentCompleted {
		completed = completed[:maxRecentCompleted]
	}
	if len(recent) > maxRecentTasks {
		recent = recent[:maxRecentTasks]
	}

	view.Agents = buildAgentSummaries(snap.Sessions)
	view.ActiveTasks = active
	view.RecentTasks = recent
	view.RecentCompleted = completed
	view.AllTasks = all
	view.Sessions = buildSessionViews(snap.Sessions)
	return view
}

func isFailedTaskStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "crashed", "timed_out":
		return true
	default:
		return false
	}
}

// buildAgentSummaries 把会话按 model 分组聚合成 Agent 概览：一个 Agent 就是
// 一种模型，而不是任务 assignee。会话数与各类 token 用量在同一模型下累加；
// 进行中会话（未归档且没有 ended_at）单独计数。model 为空时归入
// unknownModelLabel，结果按模型名排序，保证渲染顺序稳定。
func buildAgentSummaries(sessions []SessionSummary) []AgentSummary {
	byModel := make(map[string]*AgentSummary)
	for _, s := range sessions {
		model := strings.TrimSpace(s.Model)
		if model == "" {
			model = unknownModelLabel
		}
		summary, ok := byModel[model]
		if !ok {
			summary = &AgentSummary{Model: model}
			byModel[model] = summary
		}
		summary.TotalSessionCount++
		if !s.Archived && s.EndedAt == nil {
			summary.ActiveSessionCount++
		}
		summary.InputTokens += s.InputTokens
		summary.OutputTokens += s.OutputTokens
		summary.CacheReadTokens += s.CacheReadTokens
		summary.CacheWriteTokens += s.CacheWriteTokens
		summary.ReasoningTokens += s.ReasoningTokens
	}

	agents := make([]AgentSummary, 0, len(byModel))
	for _, summary := range byModel {
		agents = append(agents, *summary)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Model < agents[j].Model })
	return agents
}

// deriveResponsibleAgent 计算一个任务的"负责 Agent"：Agent 指模型，不是
// assignee。优先取任务当前运行（CurrentRunID）绑定会话（SessionID）的
// model；任何一环缺失（没有当前运行、运行未绑定会话、会话不存在于本次
// 快照、model 为空）都统一兜底为中文提示，绝不回退到 assignee。
func deriveResponsibleAgent(task AgentTask, runsByID map[int64]TaskRun, sessionsByID map[string]SessionSummary) string {
	if task.CurrentRunID == nil {
		return unresolvedAgentLabel
	}
	run, ok := runsByID[*task.CurrentRunID]
	if !ok || strings.TrimSpace(run.SessionID) == "" {
		return unresolvedAgentLabel
	}
	session, ok := sessionsByID[run.SessionID]
	if !ok || strings.TrimSpace(session.Model) == "" {
		return unresolvedAgentLabel
	}
	return session.Model
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
			LastUserPrompt:   s.LastUserPrompt,
			LastUserPromptAt: s.LastUserPromptAt,
			HandoffState:     mapRelayHandoffState(s.HandoffState),
			HandoffPlatform:  mapRelayHandoffPlatform(s.HandoffPlatform),
		})
	}
	return views
}

func mapRelayHandoffState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pending", "running", "completed", "failed":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func mapRelayHandoffPlatform(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "feishu") {
		return "feishu"
	}
	return ""
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
