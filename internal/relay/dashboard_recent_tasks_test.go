package relay

import "testing"

// TestBuildDashboardRecentTasksAggregatesLatestRunPerTask 验证总览的"最近执行
// 任务"必须按 task_id 聚合 task_runs 中最近一次运行，并以运行时间倒序排列；
// 只有 task_id 存在于原始的、未裁剪的任务运行历史但没有任何运行记录的任务
// 应被排除在外。
func TestBuildDashboardRecentTasksAggregatesLatestRunPerTask(t *testing.T) {
	snap := Snapshot{
		TakenAt: ts(1000),
		Tasks: []AgentTask{
			{ID: "t1", Title: "任务一", Status: "in_progress", CreatedAt: ts(1)},
			{ID: "t2", Title: "任务二", Status: "in_progress", CreatedAt: ts(2)},
			{ID: "t3", Title: "任务三（无运行记录）", Status: "queued", CreatedAt: ts(3)},
		},
		Runs: []TaskRun{
			{ID: 1, TaskID: "t1", Status: "done", StartedAt: ts(100)},
			{ID: 2, TaskID: "t1", Status: "failed", StartedAt: ts(200)}, // t1 最近一次运行
			{ID: 3, TaskID: "t2", Status: "running", StartedAt: ts(150)},
		},
	}

	view := BuildDashboard("mac-mini-1", snap, ts(2000), true, ts(3000))

	if len(view.RecentTasks) != 2 {
		t.Fatalf("len(RecentTasks) = %d, want 2, got %+v", len(view.RecentTasks), view.RecentTasks)
	}
	// 倒序：t1 最近一次运行 ts(200) 晚于 t2 的 ts(150)。
	if view.RecentTasks[0].ID != "t1" || view.RecentTasks[1].ID != "t2" {
		t.Fatalf("RecentTasks order wrong: got %v then %v", view.RecentTasks[0].ID, view.RecentTasks[1].ID)
	}
	if view.RecentTasks[0].LastRunAt == nil || !view.RecentTasks[0].LastRunAt.Equal(ts(200)) {
		t.Errorf("RecentTasks[0].LastRunAt = %v, want %v", view.RecentTasks[0].LastRunAt, ts(200))
	}
	if view.RecentTasks[0].Status != "failed" {
		t.Errorf("RecentTasks[0].Status = %q, want failed (explicit status preserved)", view.RecentTasks[0].Status)
	}
	if view.RecentTasks[1].LastRunAt == nil || !view.RecentTasks[1].LastRunAt.Equal(ts(150)) {
		t.Errorf("RecentTasks[1].LastRunAt = %v, want %v", view.RecentTasks[1].LastRunAt, ts(150))
	}

	// t3 没有运行记录，不应出现在最近执行列表中，但仍应出现在 AllTasks 中，
	// 且其 LastRunAt 必须为空。
	for _, tv := range view.AllTasks {
		if tv.ID == "t1" && tv.Status != "in_progress" {
			t.Errorf("AllTasks t1 Status = %q, want original task status in_progress", tv.Status)
		}
		if tv.ID == "t3" && tv.LastRunAt != nil {
			t.Errorf("t3 has no runs, LastRunAt should be nil, got %v", tv.LastRunAt)
		}
	}
}

func TestBuildDashboardRecentTasksCapsAt20(t *testing.T) {
	tasks := make([]AgentTask, 0, 25)
	runs := make([]TaskRun, 0, 25)
	for i := 0; i < 25; i++ {
		id := string(rune('a' + i))
		tasks = append(tasks, AgentTask{ID: id, Title: id, Status: "in_progress", CreatedAt: ts(1)})
		runs = append(runs, TaskRun{ID: int64(i), TaskID: id, Status: "running", StartedAt: ts(int64(i))})
	}
	snap := Snapshot{TakenAt: ts(1000), Tasks: tasks, Runs: runs}

	view := BuildDashboard("mac-mini-1", snap, ts(2000), true, ts(3000))

	if len(view.RecentTasks) != 20 {
		t.Fatalf("len(RecentTasks) = %d, want 20", len(view.RecentTasks))
	}
}

func TestBuildDashboardRecentTasksEmptyArrayNotNull(t *testing.T) {
	view := BuildDashboard("mac-mini-1", Snapshot{TakenAt: ts(1000)}, ts(2000), true, ts(3000))
	if view.RecentTasks == nil {
		t.Fatal("RecentTasks must be an empty slice, not nil")
	}
}
