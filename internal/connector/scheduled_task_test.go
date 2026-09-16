package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCronJobsFile(t *testing.T, dir string, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir cron dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jobs.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write jobs.json: %v", err)
	}
}

func TestCollectHermesCronJobs_MissingFileReturnsEmptySlice(t *testing.T) {
	dir := t.TempDir()

	tasks, err := CollectHermesCronJobs(filepath.Join(dir, "cron"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tasks == nil {
		t.Fatalf("expected non-nil empty slice, got nil")
	}
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestCollectHermesCronJobs_OversizeFileErrors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cron")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	huge := `{"jobs":[` + strings.Repeat(`{"id":"x"},`, 1) + `]}` + strings.Repeat("z", maxCronJobsFileSize+1)
	if err := os.WriteFile(filepath.Join(dir, "jobs.json"), []byte(huge), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := CollectHermesCronJobs(dir)
	if err == nil {
		t.Fatalf("expected error for oversize file, got nil")
	}
}

func TestCollectHermesCronJobs_SensitiveFieldsNeverLeak(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cron")
	body := `{
		"jobs": [
			{
				"id": "job-1",
				"name": "Nightly Report",
				"schedule": "0 2 * * *",
				"prompt": "summarize /Users/alice/private/report.md, token=sk-should-be-redacted-1234567890",
				"enabled": true,
				"state": "enabled",
				"provider": "openai",
				"base_url": "https://api.example.com",
				"model": "gpt-5",
				"deliver": {"webhook": "https://hooks.example.com/secret"},
				"origin": "manual",
				"workdir": "/Users/alice/secret-project",
				"script": "#!/bin/bash\nrm -rf /\n",
				"monitor": {"heartbeat": "https://monitor.example.com/x"},
				"error": "panic: leaked stack trace with /Users/alice/.ssh/id_rsa"
			}
		]
	}`
	writeCronJobsFile(t, dir, body)

	tasks, err := CollectHermesCronJobs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	raw, err := json.Marshal(tasks[0])
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	serialized := string(raw)

	forbidden := []string{
		"openai", "api.example.com", "gpt-5", "webhook",
		"secret-project", "rm -rf",
		"heartbeat", "panic", "id_rsa",
	}
	for _, term := range forbidden {
		if strings.Contains(serialized, term) {
			t.Errorf("serialized ScheduledTask leaked forbidden term %q: %s", term, serialized)
		}
	}
	if strings.Contains(tasks[0].InstructionPreview, "/Users/") {
		t.Errorf("instruction preview leaked a local absolute path: %q", tasks[0].InstructionPreview)
	}

	// sk-... API key must have been redacted out of the instruction preview.
	if strings.Contains(tasks[0].InstructionPreview, "sk-should-be-redacted-1234567890") {
		t.Errorf("instruction preview leaked API key: %q", tasks[0].InstructionPreview)
	}
}

func TestCollectHermesCronJobs_DisabledJobsAreKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cron")
	body := `{"jobs":[
		{"id":"a","name":"Active","enabled":true},
		{"id":"b","name":"Paused","enabled":false},
		{"id":"c","name":"StateDisabled","state":"disabled"}
	]}`
	writeCronJobsFile(t, dir, body)

	tasks, err := CollectHermesCronJobs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("expected disabled jobs to be preserved, got %d tasks", len(tasks))
	}
	statusByID := map[string]string{}
	for _, task := range tasks {
		statusByID[task.ID] = task.Status
	}
	if statusByID["b"] != "disabled" {
		t.Errorf("job b status = %q, want disabled", statusByID["b"])
	}
	if statusByID["c"] != "disabled" {
		t.Errorf("job c status = %q, want disabled", statusByID["c"])
	}
}

func TestCollectHermesCronJobs_MissingNameFallsBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cron")
	writeCronJobsFile(t, dir, `{"jobs":[{"id":"x","enabled":true}]}`)

	tasks, err := CollectHermesCronJobs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Name != defaultUnnamedTaskName {
		t.Fatalf("expected fallback name %q, got %+v", defaultUnnamedTaskName, tasks)
	}
}

func TestCollectHermesCronJobs_PromptTruncatedAtRuneBoundary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cron")
	// 600 multi-byte runes (each 3 bytes in UTF-8) so a byte-based truncation
	// would corrupt the string, while a rune-based one stays valid and exactly
	// previewMaxRunes long.
	longPrompt := strings.Repeat("测", 600)
	payload := map[string]any{
		"jobs": []map[string]any{
			{"id": "x", "name": "n", "prompt": longPrompt, "enabled": true},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	writeCronJobsFile(t, dir, string(raw))

	tasks, err := CollectHermesCronJobs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	got := []rune(tasks[0].InstructionPreview)
	if len(got) != previewMaxRunes {
		t.Fatalf("preview rune length = %d, want %d", len(got), previewMaxRunes)
	}
	if !utf8ValidString(tasks[0].InstructionPreview) {
		t.Fatalf("truncated preview is not valid UTF-8: %q", tasks[0].InstructionPreview)
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

func TestCollectHermesCronJobs_LastRunAtPreservesZero(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cron")
	writeCronJobsFile(t, dir, `{"jobs":[{"id":"x","name":"n","enabled":true,"last_run_at":0}]}`)

	tasks, err := CollectHermesCronJobs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].LastRunAt == nil {
		t.Fatalf("expected last_run_at 0 to be preserved as non-nil, got nil")
	}
	if *tasks[0].LastRunAt != 0 {
		t.Fatalf("last_run_at = %d, want 0", *tasks[0].LastRunAt)
	}
}

func TestCollectHermesCronJobs_ScheduleDisplayPreferredOverSchedule(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cron")
	writeCronJobsFile(t, dir, `{"jobs":[{"id":"x","name":"n","schedule":"0 2 * * *","schedule_display":"每天凌晨 2 点","enabled":true}]}`)

	tasks, err := CollectHermesCronJobs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tasks[0].ScheduleRule != "每天凌晨 2 点" {
		t.Fatalf("ScheduleRule = %q, want schedule_display value", tasks[0].ScheduleRule)
	}
}

func TestCollectHermesCronJobs_ParsesCurrentHermesSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cron")
	writeCronJobsFile(t, dir, `{"jobs":[{
		"id":"current-schema",
		"name":"Hourly report",
		"schedule":{"kind":"cron","expr":"0 * * * *","display":"0 * * * *"},
		"schedule_display":"0 * * * *",
		"enabled":true,
		"state":"scheduled",
		"last_run_at":"2026-09-17T08:09:10.123456+09:00",
		"last_status":"ok"
	}]}`)

	tasks, err := CollectHermesCronJobs(dir)
	if err != nil {
		t.Fatalf("CollectHermesCronJobs() error = %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("len(tasks) = %d, want 1", len(tasks))
	}
	wantTime, err := time.Parse(time.RFC3339Nano, "2026-09-17T08:09:10.123456+09:00")
	if err != nil {
		t.Fatalf("parse expected time: %v", err)
	}
	if tasks[0].LastRunAt == nil || *tasks[0].LastRunAt != wantTime.Unix() {
		t.Fatalf("LastRunAt = %v, want %d", tasks[0].LastRunAt, wantTime.Unix())
	}
	if tasks[0].ScheduleRule != "0 * * * *" {
		t.Fatalf("ScheduleRule = %q, want %q", tasks[0].ScheduleRule, "0 * * * *")
	}
}

func TestScheduledTaskJSONFieldWhitelist(t *testing.T) {
	exitCode := 0
	lastRun := int64(0)
	task := ScheduledTask{
		ID:                 "1",
		Name:               "n",
		Source:             ScheduledTaskSourceHermesCron,
		Kind:               ScheduledTaskKindCron,
		InstructionPreview: "p",
		ScheduleRule:       "r",
		Status:             "enabled",
		LastRunAt:          &lastRun,
		LastExitCode:       &exitCode,
	}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	allowed := map[string]bool{
		"id": true, "name": true, "source": true, "kind": true,
		"instruction_preview": true, "schedule_rule": true, "status": true,
		"last_run_at": true, "last_exit_code": true,
	}
	for k := range asMap {
		if !allowed[k] {
			t.Errorf("unexpected field %q in serialized ScheduledTask", k)
		}
	}
	// last_exit_code = 0 must be present, not omitted as falsy.
	v, ok := asMap["last_exit_code"]
	if !ok {
		t.Fatalf("last_exit_code missing when explicitly set to 0")
	}
	if string(v) != "0" {
		t.Errorf("last_exit_code = %s, want 0", string(v))
	}
}
