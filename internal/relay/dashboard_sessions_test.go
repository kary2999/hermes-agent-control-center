package relay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildDashboardMapsSessionsToExplicitSessionView(t *testing.T) {
	endedAt := ts(200)
	lastActivity := ts(250)
	snap := Snapshot{
		TakenAt: ts(300),
		Sessions: []SessionSummary{
			{
				ID:               "sess-1",
				Title:            "标题",
				Source:           "cli",
				Model:            "gpt-5",
				ProfileName:      "default",
				StartedAt:        ts(100),
				EndedAt:          &endedAt,
				LastActivityAt:   &lastActivity,
				MessageCount:     3,
				ToolCallCount:    2,
				InputTokens:      100,
				OutputTokens:     200,
				CacheReadTokens:  10,
				CacheWriteTokens: 20,
				ReasoningTokens:  5,
				Pinned:           true,
				Archived:         false,
				HandoffState:     "failed",
				HandoffPlatform:  "feishu",
				HandoffReason:    "handoff failed: token=sk-" + strings.Repeat("C", 48),
			},
		},
	}

	view := BuildDashboard("mac-mini-1", snap, ts(400), true, ts(500))

	if len(view.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(view.Sessions))
	}
	s := view.Sessions[0]
	if s.ID != "sess-1" || s.Title != "标题" || s.Source != "cli" || s.Model != "gpt-5" || s.ProfileName != "default" {
		t.Fatalf("SessionView mapping incorrect: %+v", s)
	}
	if !s.StartedAt.Equal(ts(100)) {
		t.Errorf("StartedAt = %v, want %v", s.StartedAt, ts(100))
	}
	if s.EndedAt == nil || !s.EndedAt.Equal(endedAt) {
		t.Errorf("EndedAt = %v, want %v", s.EndedAt, endedAt)
	}
	if s.LastActivityAt == nil || !s.LastActivityAt.Equal(lastActivity) {
		t.Errorf("LastActivityAt = %v, want %v", s.LastActivityAt, lastActivity)
	}
	if s.MessageCount != 3 || s.ToolCallCount != 2 {
		t.Errorf("counts incorrect: %+v", s)
	}
	if s.InputTokens != 100 || s.OutputTokens != 200 || s.CacheReadTokens != 10 || s.CacheWriteTokens != 20 || s.ReasoningTokens != 5 {
		t.Errorf("token counts incorrect: %+v", s)
	}
	if !s.Pinned || s.Archived {
		t.Errorf("pinned/archived incorrect: %+v", s)
	}
	if s.HandoffState != "failed" || s.HandoffPlatform != "feishu" {
		t.Errorf("handoff state/platform incorrect: %+v", s)
	}
	if s.HandoffReason == "" || strings.Contains(s.HandoffReason, "sk-") {
		t.Errorf("HandoffReason = %q, want redacted non-empty reason", s.HandoffReason)
	}
}

func TestBuildDashboardSessionsEmptyArraySerializesAsEmptyArrayNotNull(t *testing.T) {
	cases := []struct {
		name        string
		hasSnapshot bool
		snap        Snapshot
	}{
		{name: "no snapshot yet", hasSnapshot: false, snap: Snapshot{}},
		{name: "snapshot with zero sessions", hasSnapshot: true, snap: Snapshot{TakenAt: ts(100)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := BuildDashboard("mac-mini-1", tc.snap, ts(200), tc.hasSnapshot, ts(300))
			if view.Sessions == nil {
				t.Fatal("Sessions = nil, want non-nil empty slice")
			}
			body, err := json.Marshal(view)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if !strings.Contains(string(body), `"sessions":[]`) {
				t.Errorf("dashboard JSON does not contain empty sessions array literal: %s", body)
			}
		})
	}
}

func ts(sec int64) time.Time { return time.Unix(sec, 0).UTC() }
