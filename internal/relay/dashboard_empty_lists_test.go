package relay

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildDashboardListFieldsAlwaysSerializeAsArraysNeverNull 复现生产事故的
// 根因：BuildDashboard 用 `var active, completed []TaskView` 声明的 nil 切片
// 会覆盖 DashboardView 里预先初始化好的空切片字面量；一旦快照没有任何任务
// （哪怕会话不为空），json.Marshal 就会把 active_tasks / recent_completed
// 序列化成 null 而不是 []。工作台前端的严格校验器
// （isValidDashboardPayload 要求 Array.isArray）据此判定整份 payload 非法，
// 从而展示一个笼统的全页错误，而不是"暂无进行中任务"的正常空态。
//
// 本测试同时充当 agents / active_tasks / recent_completed / sessions 四个
// 集合字段的 JSON 契约回归：无论快照处于哪种状态，这些字段必须始终是
// JSON 数组字面量，绝不能是 null。
func TestBuildDashboardListFieldsAlwaysSerializeAsArraysNeverNull(t *testing.T) {
	cases := []struct {
		name        string
		hasSnapshot bool
		snap        Snapshot
		emptyFields []string // fields that must serialize as the literal []
	}{
		{
			name:        "no snapshot yet",
			hasSnapshot: false,
			snap:        Snapshot{},
			emptyFields: []string{"agents", "active_tasks", "recent_completed", "all_tasks", "sessions"},
		},
		{
			name:        "snapshot with zero tasks and zero sessions",
			hasSnapshot: true,
			snap:        Snapshot{TakenAt: ts(100)},
			emptyFields: []string{"agents", "active_tasks", "recent_completed", "all_tasks", "sessions"},
		},
		{
			// Agent 现在按会话 model 聚合而非任务 assignee，因此非空会话下
			// agents 不再是空数组——这与旧语义（Agent = 任务 assignee）刻意
			// 相反，是本次工作台语义重写（approved semantics）的一部分。
			name:        "snapshot with non-empty sessions but zero tasks",
			hasSnapshot: true,
			snap: Snapshot{
				TakenAt: ts(100),
				Sessions: []SessionSummary{
					{ID: "sess-1", Title: "标题", StartedAt: ts(50)},
				},
			},
			emptyFields: []string{"active_tasks", "recent_completed", "all_tasks"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := BuildDashboard("mac-mini-1", tc.snap, ts(200), tc.hasSnapshot, ts(300))

			if view.Agents == nil {
				t.Error("Agents = nil, want non-nil empty slice")
			}
			if view.ActiveTasks == nil {
				t.Error("ActiveTasks = nil, want non-nil empty slice")
			}
			if view.RecentCompleted == nil {
				t.Error("RecentCompleted = nil, want non-nil empty slice")
			}
			if view.Sessions == nil {
				t.Error("Sessions = nil, want non-nil empty slice")
			}

			body, err := json.Marshal(view)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			for _, field := range tc.emptyFields {
				if !strings.Contains(string(body), `"`+field+`":[]`) {
					t.Errorf("dashboard JSON does not contain empty %q array literal: %s", field, body)
				}
			}
			if strings.Contains(string(body), "null") {
				t.Errorf("dashboard JSON must never contain a null list field: %s", body)
			}
		})
	}
}
