package relay

import "time"

// ScheduledTaskView is the JSON shape served from GET
// /api/v1/scheduled-tasks. It is built field-by-field from
// ScheduledTaskSnapshot (never by re-serializing the Connector's payload
// wholesale) so that any future, unreviewed field added to the Connector's
// wire contract cannot silently leak into this read-only, browser-facing
// API. This is the strict 9-field contract from docs/scheduled-tasks-plan.md
// — no other field is ever added here.
type ScheduledTaskView struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Source             string `json:"source"`
	Kind               string `json:"kind"`
	InstructionPreview string `json:"instruction_preview,omitempty"`
	ScheduleRule       string `json:"schedule_rule,omitempty"`
	Status             string `json:"status"`
	LastRunAt          *int64 `json:"last_run_at,omitempty"`
	LastExitCode       *int   `json:"last_exit_code,omitempty"`
}

// ScheduledTasksView is the top-level JSON shape served from GET
// /api/v1/scheduled-tasks.
type ScheduledTasksView struct {
	GeneratedAt     time.Time           `json:"generated_at"`
	HasSnapshot     bool                `json:"has_snapshot"`
	SnapshotTakenAt *time.Time          `json:"snapshot_taken_at,omitempty"`
	Tasks           []ScheduledTaskView `json:"tasks"`
}

// BuildScheduledTasksView assembles the full GET /api/v1/scheduled-tasks
// response from a stored Connector snapshot, following the same
// has_snapshot / empty-slice conventions as BuildDashboard.
func BuildScheduledTasksView(snap Snapshot, hasSnapshot bool, now time.Time) ScheduledTasksView {
	view := ScheduledTasksView{
		GeneratedAt: now,
		HasSnapshot: hasSnapshot,
		Tasks:       []ScheduledTaskView{},
	}
	if !hasSnapshot {
		return view
	}
	takenAt := snap.TakenAt
	view.SnapshotTakenAt = &takenAt
	view.Tasks = BuildScheduledTaskViews(snap.ScheduledTasks)
	return view
}

// BuildScheduledTaskViews maps each ScheduledTaskSnapshot to its explicit
// ScheduledTaskView field-by-field. The result is always a non-nil slice
// (`[]` when empty, never `null`) so strict frontend array checks
// (Array.isArray) never fail on an empty result.
func BuildScheduledTaskViews(snapshots []ScheduledTaskSnapshot) []ScheduledTaskView {
	views := make([]ScheduledTaskView, 0, len(snapshots))
	for _, s := range snapshots {
		views = append(views, ScheduledTaskView{
			ID:                 s.ID,
			Name:               s.Name,
			Source:             s.Source,
			Kind:               s.Kind,
			InstructionPreview: s.InstructionPreview,
			ScheduleRule:       s.ScheduleRule,
			Status:             s.Status,
			LastRunAt:          s.LastRunAt,
			LastExitCode:       s.LastExitCode,
		})
	}
	return views
}
