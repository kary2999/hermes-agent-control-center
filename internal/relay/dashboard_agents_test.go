package relay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBuildDashboardGroupsAgentsByModelNotAssignee is the regression test for
// approved Task 3 of the workbench semantics rework: "Agent" means the model
// a session ran under, not the human/task assignee. Two sessions on the same
// model must roll up into a single AgentSummary with combined token sums and
// session counts, regardless of how many different (or blank) task
// assignees exist.
func TestBuildDashboardGroupsAgentsByModelNotAssignee(t *testing.T) {
	endedAt := ts(150)
	snap := Snapshot{
		TakenAt: ts(1000),
		Sessions: []SessionSummary{
			{
				ID: "s1", Model: "gpt-5", StartedAt: ts(100),
				InputTokens: 10, OutputTokens: 20, CacheReadTokens: 1, CacheWriteTokens: 2, ReasoningTokens: 3,
			},
			{
				ID: "s2", Model: "gpt-5", StartedAt: ts(100), EndedAt: &endedAt,
				InputTokens: 100, OutputTokens: 200, CacheReadTokens: 10, CacheWriteTokens: 20, ReasoningTokens: 30,
			},
			{
				ID: "s3", Model: "claude-opus", StartedAt: ts(100),
				InputTokens: 5, OutputTokens: 5, CacheReadTokens: 0, CacheWriteTokens: 0, ReasoningTokens: 0,
			},
		},
	}

	view := BuildDashboard("mac-mini-1", snap, ts(2000), true, ts(3000))

	if len(view.Agents) != 2 {
		t.Fatalf("len(Agents) = %d, want 2, got %+v", len(view.Agents), view.Agents)
	}
	byModel := map[string]AgentSummary{}
	for _, a := range view.Agents {
		byModel[a.Model] = a
	}
	gpt5, ok := byModel["gpt-5"]
	if !ok {
		t.Fatalf("expected an agent entry for model gpt-5, got %+v", view.Agents)
	}
	if gpt5.TotalSessionCount != 2 {
		t.Errorf("gpt-5 TotalSessionCount = %d, want 2", gpt5.TotalSessionCount)
	}
	if gpt5.ActiveSessionCount != 1 {
		t.Errorf("gpt-5 ActiveSessionCount = %d, want 1 (s2 has ended_at)", gpt5.ActiveSessionCount)
	}
	if gpt5.InputTokens != 110 || gpt5.OutputTokens != 220 || gpt5.CacheReadTokens != 11 || gpt5.CacheWriteTokens != 22 || gpt5.ReasoningTokens != 33 {
		t.Errorf("gpt-5 token sums incorrect: %+v", gpt5)
	}

	opus, ok := byModel["claude-opus"]
	if !ok {
		t.Fatalf("expected an agent entry for model claude-opus, got %+v", view.Agents)
	}
	if opus.TotalSessionCount != 1 || opus.ActiveSessionCount != 1 {
		t.Errorf("claude-opus counts incorrect: %+v", opus)
	}
}

func TestBuildDashboardAgentsUsesChineseFallbackForUnknownModel(t *testing.T) {
	snap := Snapshot{
		TakenAt: ts(1000),
		Sessions: []SessionSummary{
			{ID: "s1", Model: "", StartedAt: ts(100)},
		},
	}

	view := BuildDashboard("mac-mini-1", snap, ts(2000), true, ts(3000))

	if len(view.Agents) != 1 {
		t.Fatalf("len(Agents) = %d, want 1", len(view.Agents))
	}
	if view.Agents[0].Model == "" {
		t.Error("Agents[0].Model is blank, want a non-empty Chinese fallback label")
	}
	for _, r := range view.Agents[0].Model {
		if r > 127 {
			return // contains a non-ASCII (Chinese) rune, as required
		}
	}
	t.Errorf("Agents[0].Model = %q, want a Chinese fallback label for an unknown/blank model", view.Agents[0].Model)
}

// TestBuildDashboardTaskResponsibleAgentDerivesFromLinkedRunSessionModel is
// the regression test proving a task's responsible agent comes from the
// model of the session bound to its current run, not from task.assignee.
func TestBuildDashboardTaskResponsibleAgentDerivesFromLinkedRunSessionModel(t *testing.T) {
	runID := int64(9)
	snap := Snapshot{
		TakenAt: ts(1000),
		Tasks: []AgentTask{
			{ID: "t1", Title: "task one", Status: "running", Assignee: "someone-else", CreatedAt: ts(50), CurrentRunID: &runID},
		},
		Runs: []TaskRun{
			{ID: 9, TaskID: "t1", Status: "running", StartedAt: ts(60), SessionID: "sess-1"},
		},
		Sessions: []SessionSummary{
			{ID: "sess-1", Model: "gpt-5", StartedAt: ts(60)},
		},
	}

	view := BuildDashboard("mac-mini-1", snap, ts(2000), true, ts(3000))

	if len(view.ActiveTasks) != 1 {
		t.Fatalf("len(ActiveTasks) = %d, want 1", len(view.ActiveTasks))
	}
	task := view.ActiveTasks[0]
	if task.ResponsibleAgent != "gpt-5" {
		t.Errorf("ResponsibleAgent = %q, want %q (derived from linked run's session model)", task.ResponsibleAgent, "gpt-5")
	}
	if task.ResponsibleAgent == task.Assignee {
		t.Errorf("ResponsibleAgent must not equal Assignee: got %q for both", task.ResponsibleAgent)
	}
}

func TestBuildDashboardTaskResponsibleAgentFallsBackWhenNoLinkedSession(t *testing.T) {
	snap := Snapshot{
		TakenAt: ts(1000),
		Tasks: []AgentTask{
			{ID: "t1", Title: "task one", Status: "todo", Assignee: "someone", CreatedAt: ts(50)},
		},
	}

	view := BuildDashboard("mac-mini-1", snap, ts(2000), true, ts(3000))

	if len(view.ActiveTasks) != 1 {
		t.Fatalf("len(ActiveTasks) = %d, want 1", len(view.ActiveTasks))
	}
	if view.ActiveTasks[0].ResponsibleAgent == "" {
		t.Error("ResponsibleAgent must never be blank")
	}
	if view.ActiveTasks[0].ResponsibleAgent == "someone" {
		t.Error("ResponsibleAgent must not fall back to the task assignee")
	}
}

// ---- all_tasks / Tasks nav ----

func TestBuildDashboardIncludesAllTasksRegardlessOfStatus(t *testing.T) {
	completedAt := ts(300)
	snap := Snapshot{
		TakenAt: ts(1000),
		Tasks: []AgentTask{
			{ID: "t1", Title: "active", Status: "running", CreatedAt: ts(50)},
			{ID: "t2", Title: "done", Status: "done", CreatedAt: ts(60), CompletedAt: &completedAt},
			{ID: "t3", Title: "failed", Status: "failed", CreatedAt: ts(70), CompletedAt: &completedAt},
		},
	}

	view := BuildDashboard("mac-mini-1", snap, ts(2000), true, ts(3000))

	if len(view.AllTasks) != 3 {
		t.Fatalf("len(AllTasks) = %d, want 3", len(view.AllTasks))
	}
}

func TestBuildDashboardFailedTasksRemainInAllTasksButNotActiveOrRecentCompleted(t *testing.T) {
	completedAt := ts(300)
	currentRunID := int64(7)
	snap := Snapshot{
		TakenAt: ts(1000),
		Tasks: []AgentTask{
			{ID: "active", Title: "active", Status: "running", CreatedAt: ts(50), StartedAt: ptrTime(ts(60))},
			{ID: "done", Title: "done", Status: "done", CreatedAt: ts(70), CompletedAt: &completedAt},
			{ID: "failed-open", Title: "failed without completed_at", Status: "failed", CreatedAt: ts(80), StartedAt: ptrTime(ts(90)), CurrentRunID: &currentRunID},
			{ID: "crashed", Title: "crashed", Status: "crashed", CreatedAt: ts(100), CompletedAt: &completedAt},
			{ID: "timed-out", Title: "timed out", Status: "timed_out", CreatedAt: ts(110)},
		},
		Runs: []TaskRun{
			{ID: currentRunID, TaskID: "failed-open", Status: "running", StartedAt: ts(90)},
		},
	}

	view := BuildDashboard("mac-mini-1", snap, ts(2000), true, ts(3000))

	if len(view.AllTasks) != 5 {
		t.Fatalf("len(AllTasks) = %d, want 5", len(view.AllTasks))
	}
	if gotIDs(view.ActiveTasks) != "active" {
		t.Fatalf("ActiveTasks IDs = %q, want only active; tasks with failed/crashed/timed_out status must not be active even when completed_at is nil", gotIDs(view.ActiveTasks))
	}
	if gotIDs(view.RecentCompleted) != "done" {
		t.Fatalf("RecentCompleted IDs = %q, want only done; failed/crashed/timed_out must not be reported as completed", gotIDs(view.RecentCompleted))
	}
}

func TestBuildDashboardAllTasksEmptyArrayNotNull(t *testing.T) {
	view := BuildDashboard("mac-mini-1", Snapshot{TakenAt: ts(100)}, ts(200), true, ts(300))
	if view.AllTasks == nil {
		t.Fatal("AllTasks = nil, want non-nil empty slice")
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(body), `"all_tasks":[]`) {
		t.Errorf("dashboard JSON does not contain empty all_tasks array literal: %s", body)
	}
}

// ---- Lark handoff truthful capability status (Task 7) ----

func TestBuildDashboardReportsLarkHandoffUnavailableWithReason(t *testing.T) {
	view := BuildDashboard("mac-mini-1", Snapshot{TakenAt: ts(100)}, ts(200), true, ts(300))
	if view.LarkHandoffAvailable {
		t.Error("LarkHandoffAvailable = true, want false (no real capability exists yet)")
	}
	if strings.TrimSpace(view.LarkHandoffReason) == "" {
		t.Error("LarkHandoffReason must not be blank when the capability is unavailable")
	}
}

func ptrTime(v time.Time) *time.Time {
	return &v
}

func gotIDs(tasks []TaskView) string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return strings.Join(ids, ",")
}
