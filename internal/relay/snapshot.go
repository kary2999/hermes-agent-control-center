package relay

import "time"

// AgentTask, TaskRun, Snapshot and SnapshotPayload mirror the Connector's
// wire format (internal/connector.AgentTask / TaskRun / Snapshot /
// SnapshotPayload) field-for-field. They are intentionally duplicated
// rather than imported: the Connector and Relay are separate binaries that
// only ever talk to each other over the JSON snapshot POST, so the Relay
// decodes its own copy of the contract instead of depending on the
// Connector's internal package.
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
	// SessionID 关联本次运行所使用的 Hermes 会话（sessions.id），用于把任务
	// 的"负责 Agent"（模型）从其所绑定的会话反查出来，而不是依赖 assignee。
	SessionID string `json:"session_id,omitempty"`
}

// SessionSummary 逐字段镜像 Connector 的脱敏会话契约
// （internal/connector.SessionSummary）。这里只存在非敏感字段的显式
// 白名单；传入 JSON 中其余字段（user_id、chat_id、thread_id、
// session_key、origin_json、system_prompt、cwd、git 路径/分支、
// 计费 URL、活动描述、消息正文、工具参数/结果、prompt、推理内容、
// 密钥、成本浮点数）由于本结构体没有对应字段接收，会被 json.Unmarshal
// 静默丢弃。
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
	// LastUserPrompt 是该会话最新一条"活跃" role=user 消息的脱敏、有界预览。
	LastUserPrompt string `json:"last_user_prompt,omitempty"`
	// LastUserPromptAt 是上述预览对应消息的时间戳。
	LastUserPromptAt *time.Time `json:"last_user_prompt_at,omitempty"`
	HandoffState     string     `json:"handoff_state,omitempty"`
	HandoffPlatform  string     `json:"handoff_platform,omitempty"`
	// HandoffReason 是 handoff 失败或中断时来自 Connector 的脱敏短原因。
	HandoffReason string `json:"handoff_reason,omitempty"`
}

type Snapshot struct {
	TakenAt      time.Time        `json:"taken_at"`
	Tasks        []AgentTask      `json:"tasks"`
	Runs         []TaskRun        `json:"runs"`
	Sessions     []SessionSummary `json:"sessions"`
	Capabilities Capabilities     `json:"capabilities"`
}

type Capabilities struct {
	LarkHandoff bool `json:"lark_handoff"`
}

// SnapshotPayload is the request body decoded from POST /api/v1/snapshot.
type SnapshotPayload struct {
	DeviceID string   `json:"device_id"`
	Snapshot Snapshot `json:"snapshot"`
}
