package connector

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// maxPlistFileSize bounds how much of a single plist file we will read.
const maxPlistFileSize = 1 << 20 // 1 MiB

// appleLabelPrefix identifies Apple-owned launchd jobs, excluded by default
// so the page only shows third-party/user jobs.
const appleLabelPrefix = "com.apple."

// launchdJob is the limited set of plist fields this collector extracts.
// Everything else (EnvironmentVariables, full ProgramArguments, arbitrary
// keys) is discarded during parsing and never reaches this struct.
type launchdJob struct {
	Label            string
	ProgramName      string // basename of Program/first ProgramArguments entry
	ScriptPath       string // absolute path to a script argument that exists on disk
	StartInterval    int
	HasStartCalendar bool
	CalendarHour     *int
	CalendarMinute   *int
	RunAtLoad        bool
	KeepAlive        bool
	WatchPaths       []string
	QueueDirectories []string
	Disabled         bool
}

// parseLaunchdPlist parses a single plist file's bytes into a launchdJob
// using only encoding/xml (no third-party plist library, no shell-out). It
// is deliberately tolerant: unrecognized keys are ignored rather than
// causing failure. encoding/xml's struct-tag model does not cleanly express
// plist's flat key/value sibling-element structure, so this streams the
// document with a tokenizer instead of unmarshaling into a fixed struct.
func parseLaunchdPlist(data []byte) (launchdJob, error) {
	return parseLaunchdDictTokens(data)
}

// stringArrayElement decodes a plist <array> of <string> elements, used for
// ProgramArguments/WatchPaths/QueueDirectories.
type stringArrayElement struct {
	Items []string `xml:"string"`
}

// parseLaunchdDictTokens streams the top-level <dict> of a plist using
// encoding/xml's tokenizer, matching each <key> element to the very next
// sibling element (its value), which is the structural contract of Apple's
// plist DTD. Nested <dict>/<array> structures for StartCalendarInterval,
// ProgramArguments, WatchPaths and QueueDirectories are handled specially;
// all other nested structure is skipped without being retained.
func parseLaunchdDictTokens(data []byte) (launchdJob, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	job := launchdJob{}

	depth := 0
	var pendingKey string
	var programArgs []string

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return launchdJob{}, fmt.Errorf("tokenize plist: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth != 3 {
				// Not a direct child of the top-level <dict>: either a <key>
				// we already consumed via DecodeElement below, or leftover
				// structure we don't otherwise care about. Nothing to do
				// here; matching EndElement will decrement depth.
				continue
			}
			if t.Name.Local == "key" {
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return launchdJob{}, fmt.Errorf("decode plist key: %w", err)
				}
				pendingKey = k
				depth--
				continue
			}
			switch pendingKey {
			case "Label":
				var v string
				if err := dec.DecodeElement(&v, &t); err != nil {
					return launchdJob{}, fmt.Errorf("decode Label: %w", err)
				}
				job.Label = v
			case "Program":
				var v string
				if err := dec.DecodeElement(&v, &t); err != nil {
					return launchdJob{}, fmt.Errorf("decode Program: %w", err)
				}
				if v != "" {
					job.ProgramName = filepath.Base(v)
					if isExistingScriptPath(v) {
						job.ScriptPath = v
					}
				}
			case "ProgramArguments":
				var arr stringArrayElement
				if err := dec.DecodeElement(&arr, &t); err != nil {
					return launchdJob{}, fmt.Errorf("decode ProgramArguments: %w", err)
				}
				programArgs = append(programArgs, arr.Items...)
			case "WatchPaths":
				var arr stringArrayElement
				if err := dec.DecodeElement(&arr, &t); err != nil {
					return launchdJob{}, fmt.Errorf("decode WatchPaths: %w", err)
				}
				job.WatchPaths = append(job.WatchPaths, arr.Items...)
			case "QueueDirectories":
				var arr stringArrayElement
				if err := dec.DecodeElement(&arr, &t); err != nil {
					return launchdJob{}, fmt.Errorf("decode QueueDirectories: %w", err)
				}
				job.QueueDirectories = append(job.QueueDirectories, arr.Items...)
			case "StartCalendarInterval":
				job.HasStartCalendar = true
				if t.Name.Local == "dict" {
					hour, minute, err := decodeCalendarInterval(dec, t)
					if err != nil {
						return launchdJob{}, fmt.Errorf("decode StartCalendarInterval: %w", err)
					}
					job.CalendarHour = hour
					job.CalendarMinute = minute
				} else if err := dec.Skip(); err != nil {
					return launchdJob{}, fmt.Errorf("skip StartCalendarInterval: %w", err)
				}
			case "StartInterval":
				var v string
				if err := dec.DecodeElement(&v, &t); err != nil {
					return launchdJob{}, fmt.Errorf("decode StartInterval: %w", err)
				}
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					job.StartInterval = n
				}
			case "RunAtLoad":
				job.RunAtLoad = t.Name.Local == "true"
				if err := dec.Skip(); err != nil {
					return launchdJob{}, fmt.Errorf("skip RunAtLoad: %w", err)
				}
			case "KeepAlive":
				if t.Name.Local == "true" || t.Name.Local == "dict" {
					// KeepAlive can be a dict of conditions; presence of the
					// key at all is treated as "resident service intent".
					job.KeepAlive = true
				}
				if err := dec.Skip(); err != nil {
					return launchdJob{}, fmt.Errorf("skip KeepAlive: %w", err)
				}
			case "Disabled":
				job.Disabled = t.Name.Local == "true"
				if err := dec.Skip(); err != nil {
					return launchdJob{}, fmt.Errorf("skip Disabled: %w", err)
				}
			case "EnvironmentVariables":
				// Never retained: environment variables may carry secrets.
				if err := dec.Skip(); err != nil {
					return launchdJob{}, fmt.Errorf("skip EnvironmentVariables: %w", err)
				}
			default:
				if err := dec.Skip(); err != nil {
					return launchdJob{}, fmt.Errorf("skip unrecognized plist value: %w", err)
				}
			}
			depth--
		case xml.EndElement:
			depth--
		}
	}

	if len(programArgs) > 0 && job.ProgramName == "" {
		job.ProgramName = filepath.Base(programArgs[0])
	}
	if job.ScriptPath == "" {
		for _, arg := range programArgs {
			if isExistingScriptPath(arg) {
				job.ScriptPath = arg
				break
			}
		}
	}
	return job, nil
}

func isExistingScriptPath(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".sh", ".bash", ".zsh", ".py", ".js", ".mjs", ".ts":
	default:
		return false
	}
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func decodeCalendarInterval(dec *xml.Decoder, start xml.StartElement) (*int, *int, error) {
	var key string
	var hour *int
	var minute *int
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, nil, fmt.Errorf("read calendar interval: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "key" {
				if err := dec.DecodeElement(&key, &t); err != nil {
					return nil, nil, fmt.Errorf("decode calendar key: %w", err)
				}
				continue
			}
			if t.Name.Local != "integer" {
				if err := dec.Skip(); err != nil {
					return nil, nil, fmt.Errorf("skip calendar value: %w", err)
				}
				continue
			}
			var raw string
			if err := dec.DecodeElement(&raw, &t); err != nil {
				return nil, nil, fmt.Errorf("decode calendar integer: %w", err)
			}
			value, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				continue
			}
			switch key {
			case "Hour":
				hour = &value
			case "Minute":
				minute = &value
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return hour, minute, nil
			}
		}
	}
}

// launchdKind classifies a parsed launchdJob into a public ScheduledTaskKind,
// preferring the most specific applicable category.
func launchdKind(job launchdJob) ScheduledTaskKind {
	switch {
	case len(job.WatchPaths) > 0:
		return ScheduledTaskKindPathWatch
	case len(job.QueueDirectories) > 0:
		return ScheduledTaskKindQueueWatch
	case job.StartInterval > 0 || job.HasStartCalendar:
		return ScheduledTaskKindInterval
	case job.KeepAlive:
		return ScheduledTaskKindResident
	case job.RunAtLoad:
		return ScheduledTaskKindLoginItem
	default:
		return ScheduledTaskKindUnspecified
	}
}

// launchdScheduleRule renders a Chinese-language human description of a
// launchd job's schedule, matching launchdKind's precedence.
func launchdScheduleRule(job launchdJob) string {
	switch {
	case len(job.WatchPaths) > 0:
		return "文件变化触发"
	case len(job.QueueDirectories) > 0:
		return "目录队列触发"
	case job.HasStartCalendar:
		if job.CalendarHour != nil && job.CalendarMinute != nil {
			return fmt.Sprintf("每天 %02d:%02d", *job.CalendarHour, *job.CalendarMinute)
		}
		if job.CalendarHour != nil {
			return fmt.Sprintf("每天 %02d:00", *job.CalendarHour)
		}
		if job.CalendarMinute != nil {
			return fmt.Sprintf("每小时第 %02d 分钟", *job.CalendarMinute)
		}
		return "按日历周期执行"
	case job.StartInterval > 0:
		return fmt.Sprintf("每 %d 秒执行一次", job.StartInterval)
	case job.KeepAlive:
		return "常驻运行"
	case job.RunAtLoad:
		return "开机/登录时启动"
	default:
		return "未指定"
	}
}

func launchdStatus(job launchdJob) string {
	if job.Disabled {
		return "disabled"
	}
	return "enabled"
}

// CollectLaunchdJobs scans the given launchd directories for third-party
// (non com.apple.*) plist files and returns their sanitized ScheduledTask
// view. On non-darwin platforms this still parses whatever files exist (it
// is pure file I/O), but callers on non-darwin hosts should typically pass
// no directories; a missing directory is silently skipped, not an error.
func CollectLaunchdJobs(dirs []string) ([]ScheduledTask, error) {
	tasks := []ScheduledTask{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read launchd directory: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".plist") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			if info.Size() > maxPlistFileSize {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			job, err := parseLaunchdPlist(data)
			if err != nil {
				continue
			}
			if job.Label == "" || strings.HasPrefix(job.Label, appleLabelPrefix) {
				continue
			}
			task := ScheduledTask{
				ID:           "launchd:" + job.Label,
				Name:         job.Label,
				Source:       ScheduledTaskSourceLaunchd,
				Kind:         launchdKind(job),
				ScheduleRule: launchdScheduleRule(job),
				Status:       launchdStatus(job),
			}
			if job.ScriptPath != "" {
				scriptName := filepath.Base(job.ScriptPath)
				if job.ProgramName != "" && job.ProgramName != scriptName {
					task.InstructionPreview = sanitizePreview(job.ProgramName + " " + scriptName)
				} else {
					task.InstructionPreview = sanitizePreview(scriptName)
				}
			} else if job.ProgramName != "" {
				task.InstructionPreview = sanitizePreview(job.ProgramName)
			}
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

// --- Collector 3: launchctl runtime status -------------------------------

// CommandRunner abstracts execution of a single external command so it can
// be replaced with a fake in tests. Implementations must not go through a
// shell.
type CommandRunner interface {
	// Run executes name with args and returns combined stdout (stderr is not
	// captured/returned) or an error. The context controls timeout/cancel.
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecCommandRunner runs real OS commands via exec.CommandContext (no
// shell).
type ExecCommandRunner struct {
	// MaxOutputBytes caps how much stdout is read; 0 means use
	// defaultMaxCommandOutputBytes.
	MaxOutputBytes int64
}

const defaultMaxCommandOutputBytes = 1 << 20 // 1 MiB

func (r ExecCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	limit := r.MaxOutputBytes
	if limit <= 0 {
		limit = defaultMaxCommandOutputBytes
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &buf, limit: limit}
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run external command: %w", err)
	}
	return buf.Bytes(), nil
}

// limitedWriter discards writes past limit, preventing unbounded memory use
// from a misbehaving external command.
type limitedWriter struct {
	w     io.Writer
	n     int64
	limit int64
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	originalLen := len(p)
	if lw.n >= lw.limit {
		return originalLen, nil
	}
	remaining := lw.limit - lw.n
	writePart := p
	if int64(len(writePart)) > remaining {
		writePart = writePart[:remaining]
	}
	n, err := lw.w.Write(writePart)
	lw.n += int64(n)
	if err != nil {
		return n, err
	}
	if n != len(writePart) {
		return n, io.ErrShortWrite
	}
	return originalLen, nil
}

// LaunchctlEntry is one parsed row of `launchctl list` output.
type LaunchctlEntry struct {
	Label    string
	PID      *int
	ExitCode *int
}

// ParseLaunchctlList parses the tab-separated PID/Status/Label output of
// `launchctl list`, e.g.:
//
//	PID	Status	Label
//	123	0	com.example.foo
//	-	78	com.example.bar
//
// A "-" PID means not currently running. Malformed lines are skipped.
func ParseLaunchctlList(output []byte) []LaunchctlEntry {
	entries := []LaunchctlEntry{}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	first := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if first {
			first = false
			if strings.HasPrefix(line, "PID") {
				continue
			}
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		label := strings.Join(fields[2:], " ")
		entry := LaunchctlEntry{Label: label}
		if fields[0] != "-" {
			if n, err := strconv.Atoi(fields[0]); err == nil {
				entry.PID = &n
			}
		}
		if fields[1] != "-" {
			if n, err := strconv.Atoi(fields[1]); err == nil {
				entry.ExitCode = &n
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// FetchLaunchctlStatus runs `/usr/bin/launchctl list` exactly once and
// returns the parsed entries keyed by label, so callers can look up many
// jobs' status without spawning a process per job. On non-darwin platforms
// it returns an empty map without invoking anything.
func FetchLaunchctlStatus(ctx context.Context, runner CommandRunner) (map[string]LaunchctlEntry, error) {
	if runtime.GOOS != "darwin" {
		return map[string]LaunchctlEntry{}, nil
	}
	out, err := runner.Run(ctx, "/bin/launchctl", "list")
	if err != nil {
		return nil, fmt.Errorf("run launchctl list: %w", err)
	}
	byLabel := map[string]LaunchctlEntry{}
	for _, e := range ParseLaunchctlList(out) {
		byLabel[e.Label] = e
	}
	return byLabel, nil
}

// --- Collector 4: launchd last-completed time via `log show` -------------

// launchdLabelPattern is the strict allowlist a label must satisfy before
// being embedded into a `log show` predicate: letters, digits, dot, hyphen,
// underscore only. This closes the shell/predicate injection surface since
// no other characters (quotes, backticks, semicolons, spaces) are ever
// possible in the resulting argument.
var launchdLabelPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// FetchLaunchdLastCompleted runs `/usr/bin/log show` once for the given
// label to look for the most recent "launchd service inactive" event over
// the last 30 days, returning nil (not an error) if no record is found or
// the label fails validation/parsing — callers must treat nil as "暂无执行
// 记录" and must never substitute a file mtime instead.
func FetchLaunchdLastCompleted(ctx context.Context, runner CommandRunner, label string) (*time.Time, error) {
	if !launchdLabelPattern.MatchString(label) {
		return nil, nil
	}
	predicate := fmt.Sprintf(`subsystem == "com.apple.xpc.launchd" AND process == "launchd" AND eventMessage CONTAINS "%s" AND eventMessage CONTAINS "Service exited"`, label)
	out, err := runner.Run(ctx, "/usr/bin/log", "show", "--last", "30d", "--predicate", predicate, "--style", "compact")
	if err != nil {
		return nil, fmt.Errorf("run log show: %w", err)
	}
	return parseLogShowLastTimestamp(out), nil
}

// logShowTimestampPattern matches the leading timestamp of `log show
// --style compact` lines: "2024-01-02 15:04:05.123456+0800 ...".
var logShowTimestampPattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})`)

// parseLogShowLastTimestamp scans compact `log show` output and returns the
// latest parsed timestamp, or nil if none could be parsed.
func parseLogShowLastTimestamp(output []byte) *time.Time {
	var latest *time.Time
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		m := logShowTimestampPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.Local)
		if err != nil {
			continue
		}
		utc := t.UTC()
		if latest == nil || utc.After(*latest) {
			latest = &utc
		}
	}
	return latest
}

// --- Collectors 5/6: crontab and at queue ---------------------------------

const maxCommandPreviewRunes = 500

// ScheduledTasksConfig collects the injectable dependencies needed to
// aggregate all scheduled-task sources: an optional Hermes cron directory,
// the launchd search directories, and a CommandRunner for launchd/crontab/
// at. All fields are safe to leave at their zero value for platform
// defaults; tests should inject fakes for Runner and explicit dirs.
type ScheduledTasksConfig struct {
	// HermesCronDir is the directory containing Hermes's cron/jobs.json
	// (typically the same directory as the Hermes state.db). Empty disables
	// Hermes cron collection.
	HermesCronDir string
	// LaunchdDirs lists directories to scan for *.plist files. Empty
	// disables launchd plist collection.
	LaunchdDirs []string
	// Runner executes launchctl/crontab/atq. Required only if LaunchdDirs
	// or platform command collection is desired; nil disables the
	// runner-based collectors (launchctl/crontab/at) without error.
	Runner CommandRunner
}

// CollectScheduledTasks aggregates all configured scheduled-task sources
// into a single sanitized slice. Each sub-collector's own empty-vs-missing
// semantics are preserved (a disabled/unconfigured source contributes zero
// tasks, never an error). Runtime status (PID/exit code) from `launchctl
// list` is looked up once and merged onto matching launchd tasks by label,
// so no per-job external command is ever spawned.
func CollectScheduledTasks(ctx context.Context, cfg ScheduledTasksConfig) ([]ScheduledTask, error) {
	tasks := []ScheduledTask{}

	if strings.TrimSpace(cfg.HermesCronDir) != "" {
		cronTasks, err := CollectHermesCronJobs(cfg.HermesCronDir)
		if err != nil {
			return nil, fmt.Errorf("collect hermes cron jobs: %w", err)
		}
		tasks = append(tasks, cronTasks...)
	}

	if len(cfg.LaunchdDirs) > 0 {
		launchdTasks, err := CollectLaunchdJobs(cfg.LaunchdDirs)
		if err != nil {
			return nil, fmt.Errorf("collect launchd jobs: %w", err)
		}
		if cfg.Runner != nil {
			status, err := FetchLaunchctlStatus(ctx, cfg.Runner)
			if err == nil {
				for i := range launchdTasks {
					label := strings.TrimPrefix(launchdTasks[i].ID, "launchd:")
					if entry, ok := status[label]; ok {
						if entry.PID != nil {
							launchdTasks[i].Status = "running"
						}
						launchdTasks[i].LastExitCode = entry.ExitCode
					}
				}
			}
		}
		tasks = append(tasks, launchdTasks...)
	}

	if cfg.Runner != nil {
		crontabTasks, err := CollectCrontab(ctx, cfg.Runner)
		if err == nil {
			tasks = append(tasks, crontabTasks...)
		}

		atTasks, err := CollectAtQueue(ctx, cfg.Runner)
		if err == nil {
			tasks = append(tasks, atTasks...)
		}
	}

	return tasks, nil
}

// DefaultLaunchdDirs returns the standard macOS launchd search directories.
// On non-darwin platforms it returns an empty slice, so
// CollectScheduledTasks safely skips launchd collection there.
func DefaultLaunchdDirs() []string {
	if runtime.GOOS != "darwin" {
		return []string{}
	}
	home, err := os.UserHomeDir()
	dirs := []string{}
	if err == nil {
		dirs = append(dirs, filepath.Join(home, "Library", "LaunchAgents"))
	}
	dirs = append(dirs, "/Library/LaunchAgents", "/Library/LaunchDaemons")
	return dirs
}

// noCrontabMarkers are substrings `crontab -l` prints (to stderr, but some
// implementations echo to stdout) when the invoking user has no crontab;
// this is a normal empty state, not an error.
var noCrontabMarkers = []string{"no crontab for", "cannot open"}

// CollectCrontab runs `/usr/bin/crontab -l` once and returns each
// non-comment, non-blank line as a sanitized ScheduledTask. Absence of a
// crontab is treated as an empty list, not an error.
func CollectCrontab(ctx context.Context, runner CommandRunner) ([]ScheduledTask, error) {
	if runtime.GOOS != "darwin" {
		return []ScheduledTask{}, nil
	}
	out, err := runner.Run(ctx, "/usr/bin/crontab", "-l")
	if err != nil {
		if isNoCrontabError(err) {
			return []ScheduledTask{}, nil
		}
		return nil, fmt.Errorf("run crontab -l: %w", err)
	}
	tasks := []ScheduledTask{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	idx := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx++
		schedule, command := splitCronLine(line)
		tasks = append(tasks, ScheduledTask{
			ID:                 fmt.Sprintf("crontab:%d", idx),
			Name:               fmt.Sprintf("Crontab #%d", idx),
			Source:             ScheduledTaskSourceCrontab,
			Kind:               ScheduledTaskKindCron,
			InstructionPreview: truncateRunes(sanitizeScheduledInstruction(command), maxCommandPreviewRunes),
			ScheduleRule:       schedule,
			Status:             "enabled",
		})
	}
	return tasks, nil
}

func isNoCrontabError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, marker := range noCrontabMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// splitCronLine splits a standard 5-field cron schedule from its command.
func splitCronLine(line string) (schedule, command string) {
	fields := strings.Fields(line)
	if len(fields) < 6 {
		return "", line
	}
	return strings.Join(fields[:5], " "), strings.Join(fields[5:], " ")
}

// CollectAtQueue runs `/usr/bin/atq` once and returns each queued job as a
// sanitized ScheduledTask. An empty queue is a normal empty list.
func CollectAtQueue(ctx context.Context, runner CommandRunner) ([]ScheduledTask, error) {
	if runtime.GOOS != "darwin" {
		return []ScheduledTask{}, nil
	}
	out, err := runner.Run(ctx, "/usr/bin/atq")
	if err != nil {
		return nil, fmt.Errorf("run atq: %w", err)
	}
	tasks := []ScheduledTask{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		jobID := fields[0]
		tasks = append(tasks, ScheduledTask{
			ID:                 "at:" + jobID,
			Name:               "At Job " + jobID,
			Source:             ScheduledTaskSourceAt,
			Kind:               ScheduledTaskKindOneShot,
			InstructionPreview: truncateRunes(sanitizePreview(line), maxCommandPreviewRunes),
			ScheduleRule:       strings.TrimPrefix(line, jobID+" "),
			Status:             "enabled",
		})
	}
	return tasks, nil
}
