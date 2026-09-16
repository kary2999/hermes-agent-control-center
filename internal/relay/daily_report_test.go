package relay

import (
	"testing"
	"time"
)

func TestBuildDailyReportSummarizesFailuresAndRemovesPromptPreviews(t *testing.T) {
	t.Parallel()
	date := "2026-09-17"
	now := time.Date(2026, 9, 17, 0, 5, 0, 0, time.FixedZone("JST", 9*60*60))
	view := DashboardView{
		GeneratedAt: now,
		HasSnapshot: true,
		DeviceID:    "mac-mini-report",
		AllTasks: []TaskView{
			{ID: "failed-1", Title: "失败任务", Status: "failed"},
			{ID: "done-1", Title: "完成任务", Status: "done"},
		},
		RecentTasks: []TaskView{{ID: "failed-1", Title: "失败任务", Status: "failed"}},
		Sessions:    []SessionView{{ID: "sess-1", Title: "会话", LastUserPrompt: "不应归档的用户提示"}},
	}

	report, err := BuildDailyReport(date, view, now)
	if err != nil {
		t.Fatalf("BuildDailyReport() error = %v", err)
	}
	if report.Date != date {
		t.Errorf("Date = %q, want %q", report.Date, date)
	}
	if report.FailedTaskCount != 1 {
		t.Errorf("FailedTaskCount = %d, want 1", report.FailedTaskCount)
	}
	if report.Conclusion == "" || report.Anomaly == "" || report.NextStep == "" {
		t.Fatalf("summary fields must be non-empty: %+v", report)
	}
	if len(report.Dashboard.Sessions) != 1 || report.Dashboard.Sessions[0].LastUserPrompt != "" {
		t.Fatalf("archived sessions must clear prompt previews: %+v", report.Dashboard.Sessions)
	}
}

func TestDailyReportStoreSaveLoadAndList(t *testing.T) {
	t.Parallel()
	store, err := NewDailyReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDailyReportStore() error = %v", err)
	}
	report := DailyReport{Date: "2026-09-17", Conclusion: "完成日报"}
	if err := store.Save(report); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load("2026-09-17")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Conclusion != report.Conclusion {
		t.Errorf("Conclusion = %q, want %q", got.Conclusion, report.Conclusion)
	}
	dates, err := store.ListDates()
	if err != nil {
		t.Fatalf("ListDates() error = %v", err)
	}
	if len(dates) != 1 || dates[0] != "2026-09-17" {
		t.Fatalf("ListDates() = %#v, want [2026-09-17]", dates)
	}
}

func TestDailyReportStoreRejectsInvalidDate(t *testing.T) {
	t.Parallel()
	store, err := NewDailyReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewDailyReportStore() error = %v", err)
	}
	if err := store.Save(DailyReport{Date: "../../secret"}); err == nil {
		t.Fatal("Save() invalid date: expected error, got nil")
	}
	if _, err := store.Load("2026-9-17"); err == nil {
		t.Fatal("Load() invalid date: expected error, got nil")
	}
}
