package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePlist(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	return path
}

const intervalPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.example.backup</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/bin/true</string>
	</array>
	<key>StartInterval</key>
	<integer>3600</integer>
	<key>EnvironmentVariables</key>
	<dict>
		<key>SECRET_TOKEN</key>
		<string>super-secret-value</string>
	</dict>
</dict>
</plist>`

const residentPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.example.daemon</string>
	<key>Program</key>
	<string>/usr/local/bin/mydaemon</string>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>`

const runAtLoadPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.example.login</string>
	<key>Program</key>
	<string>/usr/local/bin/loginhelper</string>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>`

const watchPathPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.example.watcher</string>
	<key>Program</key>
	<string>/usr/local/bin/watcher</string>
	<key>WatchPaths</key>
	<array>
		<string>/tmp/watched</string>
	</array>
</dict>
</plist>`

const appleOwnedPlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.apple.something</string>
	<key>Program</key>
	<string>/usr/libexec/something</string>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>`

func TestCollectLaunchdJobs_ClassifiesKindsAndScheduleText(t *testing.T) {
	dir := t.TempDir()
	writePlist(t, dir, "a.plist", intervalPlist)
	writePlist(t, dir, "b.plist", residentPlist)
	writePlist(t, dir, "c.plist", runAtLoadPlist)
	writePlist(t, dir, "d.plist", watchPathPlist)
	writePlist(t, dir, "e.plist", appleOwnedPlist)

	tasks, err := CollectLaunchdJobs([]string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 4 {
		t.Fatalf("expected apple-owned job excluded, got %d tasks: %+v", len(tasks), tasks)
	}

	byLabel := map[string]ScheduledTask{}
	for _, task := range tasks {
		byLabel[task.Name] = task
	}

	if got := byLabel["com.example.backup"]; got.Kind != ScheduledTaskKindInterval || got.ScheduleRule != "每 3600 秒执行一次" {
		t.Errorf("interval job = %+v, want kind=interval schedule='每 3600 秒执行一次'", got)
	}
	if got := byLabel["com.example.daemon"]; got.Kind != ScheduledTaskKindResident || got.ScheduleRule != "常驻运行" {
		t.Errorf("resident job = %+v, want kind=resident schedule='常驻运行'", got)
	}
	if got := byLabel["com.example.login"]; got.Kind != ScheduledTaskKindLoginItem {
		t.Errorf("login job kind = %v, want login_item", got.Kind)
	}
	if got := byLabel["com.example.watcher"]; got.Kind != ScheduledTaskKindPathWatch || got.ScheduleRule != "文件变化触发" {
		t.Errorf("watch job = %+v, want kind=path_watch schedule='文件变化触发'", got)
	}
	for _, task := range tasks {
		if strings.HasPrefix(task.Name, "com.apple.") {
			t.Errorf("apple-owned job leaked into results: %+v", task)
		}
	}
}

func TestCollectLaunchdJobs_EnvironmentVariablesNeverLeak(t *testing.T) {
	dir := t.TempDir()
	writePlist(t, dir, "a.plist", intervalPlist)

	tasks, err := CollectLaunchdJobs([]string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	raw, err := json.Marshal(tasks[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "super-secret-value") || strings.Contains(string(raw), "SECRET_TOKEN") {
		t.Errorf("EnvironmentVariables leaked into ScheduledTask: %s", raw)
	}
}

func TestCollectLaunchdJobs_MissingDirectoryReturnsEmptySlice(t *testing.T) {
	tasks, err := CollectLaunchdJobs([]string{filepath.Join(t.TempDir(), "does-not-exist")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tasks == nil || len(tasks) != 0 {
		t.Fatalf("expected empty slice, got %+v", tasks)
	}
}

func TestParseLaunchctlList_ParsesMultipleServicesInOneCall(t *testing.T) {
	output := []byte("PID\tStatus\tLabel\n123\t0\tcom.example.foo\n-\t78\tcom.example.bar\n-\t-\tcom.example.baz\n")

	entries := ParseLaunchctlList(output)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %+v", len(entries), entries)
	}
	byLabel := map[string]LaunchctlEntry{}
	for _, e := range entries {
		byLabel[e.Label] = e
	}
	foo := byLabel["com.example.foo"]
	if foo.PID == nil || *foo.PID != 123 {
		t.Errorf("foo.PID = %v, want 123", foo.PID)
	}
	if foo.ExitCode == nil || *foo.ExitCode != 0 {
		t.Errorf("foo.ExitCode = %v, want 0", foo.ExitCode)
	}
	bar := byLabel["com.example.bar"]
	if bar.PID != nil {
		t.Errorf("bar.PID = %v, want nil (not running)", bar.PID)
	}
	if bar.ExitCode == nil || *bar.ExitCode != 78 {
		t.Errorf("bar.ExitCode = %v, want 78", bar.ExitCode)
	}
	baz := byLabel["com.example.baz"]
	if baz.PID != nil || baz.ExitCode != nil {
		t.Errorf("baz = %+v, want both nil", baz)
	}
}

// countingRunner records how many times Run was invoked, to verify
// FetchLaunchctlStatus calls the external command exactly once regardless
// of how many jobs are subsequently looked up.
type countingRunner struct {
	calls  int
	output []byte
	err    error
	names  []string
}

func (r *countingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls++
	r.names = append(r.names, name)
	return r.output, r.err
}

func TestFetchLaunchctlStatus_CallsExternalCommandExactlyOnce(t *testing.T) {
	runner := &countingRunner{output: []byte("PID\tStatus\tLabel\n1\t0\tcom.example.a\n2\t0\tcom.example.b\n")}
	status, err := FetchLaunchctlStatus(context.Background(), runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("expected exactly 1 call, got %d", runner.calls)
	}
	if len(status) != 2 {
		t.Fatalf("expected 2 parsed entries, got %d", len(status))
	}
	if len(runner.names) != 1 || runner.names[0] != "/bin/launchctl" {
		t.Fatalf("command path = %v, want [/bin/launchctl]", runner.names)
	}
}

func TestLimitedWriter_ReportsOriginalLengthAfterTruncation(t *testing.T) {
	var dst bytes.Buffer
	w := &limitedWriter{w: &dst, limit: 3}
	n, err := w.Write([]byte("abcdef"))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != 6 {
		t.Fatalf("Write() n = %d, want original length 6", n)
	}
	if dst.String() != "abc" {
		t.Fatalf("buffer = %q, want %q", dst.String(), "abc")
	}
}

func TestCollectLaunchdJobs_ParsesCalendarRuleAndScriptArgument(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "publish_daily_report.py")
	if err := os.WriteFile(scriptPath, []byte("# fixture only\n"), 0o600); err != nil {
		t.Fatalf("write script fixture: %v", err)
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>top.example.daily-report</string>
<key>ProgramArguments</key><array><string>/usr/bin/python3</string><string>%s</string><string>--token</string><string>must-not-leak</string></array>
<key>StartCalendarInterval</key><dict><key>Hour</key><integer>23</integer><key>Minute</key><integer>55</integer></dict>
</dict></plist>`, scriptPath)
	writePlist(t, dir, "daily.plist", plist)

	tasks, err := CollectLaunchdJobs([]string{dir})
	if err != nil {
		t.Fatalf("CollectLaunchdJobs() error = %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(tasks))
	}
	wantPreview := "python3 publish_daily_report.py"
	if tasks[0].InstructionPreview != wantPreview {
		t.Fatalf("InstructionPreview = %q, want safe summary %q", tasks[0].InstructionPreview, wantPreview)
	}
	if strings.Contains(tasks[0].InstructionPreview, dir) {
		t.Fatalf("InstructionPreview leaked local absolute path: %q", tasks[0].InstructionPreview)
	}
	if strings.Contains(tasks[0].InstructionPreview, "must-not-leak") {
		t.Fatalf("InstructionPreview leaked non-script arguments: %q", tasks[0].InstructionPreview)
	}
	if tasks[0].ScheduleRule != "每天 23:55" {
		t.Fatalf("ScheduleRule = %q, want %q", tasks[0].ScheduleRule, "每天 23:55")
	}
}

func TestCollectScheduledTasks_CommandFailureKeepsFileBackedTasks(t *testing.T) {
	dir := t.TempDir()
	writePlist(t, dir, "a.plist", intervalPlist)
	runner := &countingRunner{err: errors.New("command unavailable")}

	tasks, err := CollectScheduledTasks(context.Background(), ScheduledTasksConfig{
		LaunchdDirs: []string{dir},
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("CollectScheduledTasks() error = %v, want best-effort command sources", err)
	}
	if len(tasks) != 1 || tasks[0].Name != "com.example.backup" {
		t.Fatalf("tasks = %+v, want launchd plist task retained", tasks)
	}
}

func TestCollectCrontab_NoCrontabIsEmptyNotError(t *testing.T) {
	runner := &countingRunner{err: errors.New("exit status 1: no crontab for testuser")}
	tasks, err := CollectCrontab(context.Background(), runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tasks == nil || len(tasks) != 0 {
		t.Fatalf("expected empty slice, got %+v", tasks)
	}
}

func TestCollectCrontab_ParsesAndSanitizesLines(t *testing.T) {
	runner := &countingRunner{output: []byte("# comment\n0 3 * * * curl -H 'Authorization: Bearer abcd1234efgh5678' https://example.com\n")}
	tasks, err := CollectCrontab(context.Background(), runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if strings.Contains(tasks[0].InstructionPreview, "abcd1234efgh5678") {
		t.Errorf("crontab preview leaked bearer token: %q", tasks[0].InstructionPreview)
	}
	if tasks[0].ScheduleRule != "0 3 * * *" {
		t.Errorf("ScheduleRule = %q, want '0 3 * * *'", tasks[0].ScheduleRule)
	}
}

func TestCollectAtQueue_EmptyQueueReturnsEmptySlice(t *testing.T) {
	runner := &countingRunner{output: []byte("")}
	tasks, err := CollectAtQueue(context.Background(), runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tasks == nil || len(tasks) != 0 {
		t.Fatalf("expected empty slice, got %+v", tasks)
	}
}

func TestParseLogShowLastTimestamp_FindsLatestAndReturnsNilWhenAbsent(t *testing.T) {
	out := []byte("2024-01-01 10:00:00.000000+0800 launchd: Service exited\n2024-02-15 09:30:00.000000+0800 launchd: Service exited\ngarbage line\n")
	got := parseLogShowLastTimestamp(out)
	if got == nil {
		t.Fatalf("expected a timestamp, got nil")
	}
	want := time.Date(2024, 2, 15, 9, 30, 0, 0, time.Local).UTC()
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if none := parseLogShowLastTimestamp([]byte("no matches here")); none != nil {
		t.Errorf("expected nil for no matches, got %v", none)
	}
}

func TestFetchLaunchdLastCompleted_RejectsUnsafeLabel(t *testing.T) {
	runner := &countingRunner{output: []byte("")}
	got, err := FetchLaunchdLastCompleted(context.Background(), runner, `com.example."; rm -rf / #`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for unsafe label, got %v", got)
	}
	if runner.calls != 0 {
		t.Errorf("expected no command execution for an unsafe label, got %d calls", runner.calls)
	}
}
