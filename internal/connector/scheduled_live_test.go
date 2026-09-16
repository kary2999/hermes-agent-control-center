package connector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCollectScheduledTasks_LiveMacMini 是显式开启的真机验收，不在 CI 中默认运行。
func TestCollectScheduledTasks_LiveMacMini(t *testing.T) {
	if os.Getenv("HERMES_RUN_LIVE_SCHEDULE_TEST") != "1" {
		t.Skip("set HERMES_RUN_LIVE_SCHEDULE_TEST=1 on the Mac mini")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home directory: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tasks, err := CollectScheduledTasks(ctx, ScheduledTasksConfig{
		HermesCronDir: filepath.Join(home, ".hermes", "cron"),
		LaunchdDirs:   DefaultLaunchdDirs(),
		Runner:        ExecCommandRunner{},
	})
	if err != nil {
		t.Fatalf("CollectScheduledTasks() error = %v", err)
	}
	if tasks == nil {
		t.Fatal("CollectScheduledTasks() returned nil, want [] or populated slice")
	}

	byName := make(map[string]ScheduledTask, len(tasks))
	bySource := make(map[ScheduledTaskSource]int)
	for _, task := range tasks {
		byName[task.Name] = task
		bySource[task.Source]++
		if filepath.IsAbs(task.InstructionPreview) {
			t.Errorf("task %q leaked an absolute instruction path", task.Name)
		}
	}

	expectedLaunchd := []string{
		"com.kimyx.token-usage-sync",
		"top.ikarp.hermes-backup",
		"top.ikarp.hermes-daily-report",
		"ai.hermes.gateway",
		"top.ikarp.hermes-connector",
		"top.ikarp.hermes-report-connector",
	}
	for _, name := range expectedLaunchd {
		t.Run(name, func(t *testing.T) {
			task, ok := byName[name]
			if !ok {
				t.Fatalf("live snapshot is missing %q", name)
			}
			if task.Source != ScheduledTaskSourceLaunchd {
				t.Fatalf("source = %q, want launchd", task.Source)
			}
		})
	}
	if bySource[ScheduledTaskSourceHermesCron] == 0 {
		t.Fatal("live snapshot contains no Hermes Cron history")
	}
}
