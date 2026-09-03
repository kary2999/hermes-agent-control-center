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
}

type Snapshot struct {
	TakenAt time.Time   `json:"taken_at"`
	Tasks   []AgentTask `json:"tasks"`
	Runs    []TaskRun   `json:"runs"`
}

// SnapshotPayload is the request body decoded from POST /api/v1/snapshot.
type SnapshotPayload struct {
	DeviceID string   `json:"device_id"`
	Snapshot Snapshot `json:"snapshot"`
}
