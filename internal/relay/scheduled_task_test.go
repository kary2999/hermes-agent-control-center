package relay

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBuildScheduledTaskViews_EmptyInputProducesEmptySliceNotNil(t *testing.T) {
	views := BuildScheduledTaskViews(nil)
	if views == nil {
		t.Fatalf("expected non-nil empty slice, got nil")
	}
	if len(views) != 0 {
		t.Fatalf("expected 0 views, got %d", len(views))
	}

	raw, err := json.Marshal(views)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != "[]" {
		t.Fatalf("serialized = %s, want []", raw)
	}
}

func TestBuildScheduledTaskViews_FieldWhitelistOnly(t *testing.T) {
	lastRun := int64(0)
	exitCode := 0
	snapshots := []ScheduledTaskSnapshot{
		{
			ID:                 "hermes-cron:1",
			Name:               "Nightly Report",
			Source:             "hermes_cron",
			Kind:               "cron",
			InstructionPreview: "do the thing",
			ScheduleRule:       "0 2 * * *",
			Status:             "enabled",
			LastRunAt:          &lastRun,
			LastExitCode:       &exitCode,
		},
	}

	views := BuildScheduledTaskViews(snapshots)
	if len(views) != 1 {
		t.Fatalf("expected 1 view, got %d", len(views))
	}

	raw, err := json.Marshal(views[0])
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
	if len(asMap) > len(allowed) {
		t.Errorf("serialized view has %d fields, want at most %d: %v", len(asMap), len(allowed), asMap)
	}
	for k := range asMap {
		if !allowed[k] {
			t.Errorf("unexpected field %q leaked into ScheduledTaskView JSON", k)
		}
	}

	// last_exit_code = 0 and last_run_at = 0 must both be present, not
	// omitted as falsy zero values.
	for _, field := range []string{"last_exit_code", "last_run_at"} {
		v, ok := asMap[field]
		if !ok {
			t.Fatalf("%s missing when explicitly set to 0", field)
		}
		if string(v) != "0" {
			t.Errorf("%s = %s, want 0", field, string(v))
		}
	}
}

func TestBuildScheduledTasksView_NoSnapshotYieldsEmptyTasksAndHasSnapshotFalse(t *testing.T) {
	view := BuildScheduledTasksView(Snapshot{}, false, time.Unix(300, 0).UTC())
	if view.HasSnapshot {
		t.Errorf("HasSnapshot = true, want false")
	}
	if view.Tasks == nil || len(view.Tasks) != 0 {
		t.Fatalf("expected empty non-nil Tasks, got %+v", view.Tasks)
	}
	raw, err := json.Marshal(view.Tasks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != "[]" {
		t.Fatalf("Tasks serialized = %s, want []", raw)
	}
}

func TestBuildScheduledTasksView_WithSnapshotMapsTasks(t *testing.T) {
	snap := Snapshot{
		TakenAt: time.Unix(100, 0).UTC(),
		ScheduledTasks: []ScheduledTaskSnapshot{
			{ID: "a", Name: "A", Source: "launchd", Kind: "resident", Status: "running"},
		},
	}
	view := BuildScheduledTasksView(snap, true, time.Unix(300, 0).UTC())
	if !view.HasSnapshot {
		t.Fatalf("HasSnapshot = false, want true")
	}
	if view.SnapshotTakenAt == nil || !view.SnapshotTakenAt.Equal(snap.TakenAt) {
		t.Errorf("SnapshotTakenAt = %v, want %v", view.SnapshotTakenAt, snap.TakenAt)
	}
	if len(view.Tasks) != 1 || view.Tasks[0].ID != "a" {
		t.Fatalf("Tasks = %+v, want 1 task with id=a", view.Tasks)
	}
}
