package connector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ScheduledTaskSource identifies which subsystem a ScheduledTask was
// collected from.
type ScheduledTaskSource string

const (
	ScheduledTaskSourceHermesCron ScheduledTaskSource = "hermes_cron"
	ScheduledTaskSourceLaunchd    ScheduledTaskSource = "launchd"
	ScheduledTaskSourceCrontab    ScheduledTaskSource = "crontab"
	ScheduledTaskSourceAt         ScheduledTaskSource = "at"
)

// ScheduledTaskKind classifies the scheduling mechanism of a ScheduledTask,
// independent of its Source.
type ScheduledTaskKind string

const (
	ScheduledTaskKindCron        ScheduledTaskKind = "cron"
	ScheduledTaskKindInterval    ScheduledTaskKind = "interval"
	ScheduledTaskKindResident    ScheduledTaskKind = "resident"
	ScheduledTaskKindLoginItem   ScheduledTaskKind = "login_item"
	ScheduledTaskKindPathWatch   ScheduledTaskKind = "path_watch"
	ScheduledTaskKindQueueWatch  ScheduledTaskKind = "queue_watch"
	ScheduledTaskKindOneShot     ScheduledTaskKind = "one_shot"
	ScheduledTaskKindUnspecified ScheduledTaskKind = "unspecified"
)

// ScheduledTask is the sanitized, read-only view of one scheduled/cron-like
// task on the machine, drawn from one of several sources (Hermes's own cron
// jobs, macOS launchd, crontab, at). Only this explicit 9-field allowlist is
// ever populated; no source-specific implementation detail (provider,
// base_url, model, deliver target, origin, workdir, script contents,
// monitor config, raw error text, environment variables, full argv, plist
// XML, etc.) is ever attached to this struct.
type ScheduledTask struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	Source             ScheduledTaskSource `json:"source"`
	Kind               ScheduledTaskKind   `json:"kind"`
	InstructionPreview string              `json:"instruction_preview,omitempty"`
	ScheduleRule       string              `json:"schedule_rule,omitempty"`
	Status             string              `json:"status"`
	LastRunAt          *int64              `json:"last_run_at,omitempty"`
	LastExitCode       *int                `json:"last_exit_code,omitempty"`
}

// defaultUnnamedTaskName is used when a Hermes cron job has no usable name.
const defaultUnnamedTaskName = "未命名定时任务"

var localAbsolutePathPattern = regexp.MustCompile(`(^|[\s="'\(])(/[^\s"'<>]+)`)

// maxCronJobsFileSize bounds how large a cron/jobs.json file we will read,
// so a corrupt or hostile file cannot force unbounded memory use.
const maxCronJobsFileSize = 1 << 20 // 1 MiB

// hermesCronJobsFile is a single entry from Hermes's cron/jobs.json. Field
// names mirror Hermes's on-disk schema; only a subset is ever promoted into
// the public ScheduledTask allowlist below. Fields like Provider, BaseURL,
// Model, Deliver, Origin, Workdir, Script, Monitor and Error are
// intentionally NOT read into ScheduledTask.
type hermesCronJobRaw struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Schedule        json.RawMessage `json:"schedule"`
	ScheduleDisplay string          `json:"schedule_display"`
	Prompt          string          `json:"prompt"`
	Instruction     string          `json:"instruction"`
	Enabled         *bool           `json:"enabled"`
	State           string          `json:"state"`
	LastRunAt       json.RawMessage `json:"last_run_at"`
	LastStatus      string          `json:"last_status"`
	// 以下字段有意不映射到 ScheduledTask：provider/base_url/model/deliver/
	// origin/workdir/script/monitor/error 等，均可能携带敏感或与本契约
	// 无关的实现细节。
}

type hermesCronJobsFile struct {
	Jobs []hermesCronJobRaw `json:"jobs"`
}

// CollectHermesCronJobs reads Hermes's cron/jobs.json (a JSON file listing
// Hermes-managed scheduled agent jobs, including disabled/paused ones) from
// the given directory and returns the sanitized, allowlisted view of each
// job. A missing file is not an error: it returns an empty slice, since a
// Hermes installation with no scheduled jobs configured is a normal state.
func CollectHermesCronJobs(cronDir string) ([]ScheduledTask, error) {
	path := filepath.Join(cronDir, "jobs.json")

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []ScheduledTask{}, nil
		}
		return nil, fmt.Errorf("stat hermes cron jobs file: %w", err)
	}
	if info.Size() > maxCronJobsFileSize {
		return nil, fmt.Errorf("hermes cron jobs file exceeds %d byte limit", maxCronJobsFileSize)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read hermes cron jobs file: %w", err)
	}

	var file hermesCronJobsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse hermes cron jobs file: %w", err)
	}

	tasks := make([]ScheduledTask, 0, len(file.Jobs))
	for _, job := range file.Jobs {
		tasks = append(tasks, mapHermesCronJob(job))
	}
	return tasks, nil
}

func mapHermesCronJob(job hermesCronJobRaw) ScheduledTask {
	name := strings.TrimSpace(job.Name)
	if name == "" {
		name = defaultUnnamedTaskName
	}

	instructionRaw := job.Prompt
	if strings.TrimSpace(instructionRaw) == "" {
		instructionRaw = job.Instruction
	}

	schedule := strings.TrimSpace(job.ScheduleDisplay)
	if schedule == "" {
		schedule = parseHermesSchedule(job.Schedule)
	}

	task := ScheduledTask{
		ID:                 job.ID,
		Name:               name,
		Source:             ScheduledTaskSourceHermesCron,
		Kind:               ScheduledTaskKindCron,
		InstructionPreview: sanitizeScheduledInstruction(instructionRaw),
		ScheduleRule:       schedule,
		Status:             mapHermesCronStatus(job),
	}
	task.LastRunAt = parseHermesLastRunAt(job.LastRunAt)
	return task
}

func sanitizeScheduledInstruction(raw string) string {
	preview := sanitizePreview(raw)
	return localAbsolutePathPattern.ReplaceAllString(preview, `${1}[本地路径]`)
}

func parseHermesSchedule(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var schedule struct {
		Display string `json:"display"`
		Expr    string `json:"expr"`
	}
	if err := json.Unmarshal(raw, &schedule); err != nil {
		return ""
	}
	if display := strings.TrimSpace(schedule.Display); display != "" {
		return display
	}
	return strings.TrimSpace(schedule.Expr)
}

func parseHermesLastRunAt(raw json.RawMessage) *int64 {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var timestamp string
	if err := json.Unmarshal(raw, &timestamp); err == nil {
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil
		}
		unix := parsed.Unix()
		return &unix
	}
	unix, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return nil
	}
	return &unix
}

// mapHermesCronStatus safely maps whatever combination of enabled/state
// fields a Hermes job carries into one of a small public vocabulary. It
// never leaks the raw state/last_status string as-is beyond well-known
// values, and disabled/paused jobs are preserved (not filtered out
// upstream) with an explicit disabled status.
func mapHermesCronStatus(job hermesCronJobRaw) string {
	if job.Enabled != nil && !*job.Enabled {
		return "disabled"
	}
	switch strings.ToLower(strings.TrimSpace(job.State)) {
	case "disabled", "paused":
		return "disabled"
	case "enabled", "active", "":
		// fall through to last_status based inference below
	default:
		return "enabled"
	}
	switch strings.ToLower(strings.TrimSpace(job.LastStatus)) {
	case "running":
		return "running"
	case "failed", "error":
		return "failed"
	case "success", "ok", "completed":
		return "enabled"
	default:
		return "enabled"
	}
}
