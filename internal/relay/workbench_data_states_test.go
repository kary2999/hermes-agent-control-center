package relay

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// --- 8. 工作台数据态 UX：骨架占位 / null 规整 / stale-error 提示语 /
// 数值零语义（沿用本文件既有惯例，对已渲染出的 workbench 页面源码做静态
// 断言，而不是真的执行其中的 JavaScript，因为没有可用的浏览器/JS 测试
// 基础设施）。---

func workbenchBody(t *testing.T) string {
	t.Helper()
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	return readBody(t, resp)
}

// TestWorkbenchShowsSkeletonPlaceholdersBeforeFirstLoad 校验首次加载前，
// 总览指标、任务表格/卡片、Agent 表格/卡片、活动 Feed 与会话列表都带有
// 灰色骨架占位（而不是彻底空白），这样布局在第一次数据到达前不会塌陷。
func TestWorkbenchShowsSkeletonPlaceholdersBeforeFirstLoad(t *testing.T) {
	body := workbenchBody(t)

	if !strings.Contains(body, ".skel {") && !strings.Contains(body, ".skel{") {
		t.Error("workbench CSS must define a .skel skeleton placeholder style")
	}

	containers := []string{
		`id="active-tasks-tbody"`,
		`id="active-tasks-cards"`,
		`id="agents-tbody"`,
		`id="agents-cards"`,
		`id="activity-feed"`,
	}
	for _, marker := range containers {
		idx := strings.Index(body, marker)
		if idx == -1 {
			t.Fatalf("workbench markup missing container %q", marker)
		}
		// 只看容器起始标签之后约 400 个字符，确认紧邻着骨架占位的行/卡片/
		// 列表项，避免误匹配到页面其他地方的 data-skeleton 标记。
		window := body[idx:min(idx+400, len(body))]
		if !strings.Contains(window, `data-skeleton="true"`) {
			t.Errorf("container %q has no data-skeleton=\"true\" placeholder immediately inside it", marker)
		}
	}

	if strings.Count(body, `id="metric-`) < 4 {
		t.Fatal("expected 4 metric-value elements")
	}
	for _, id := range []string{"metric-agents", "metric-total-tasks", "metric-recent-completed", "metric-sessions"} {
		re := regexp.MustCompile(`id="` + id + `"[^>]*class="[^"]*\bskel\b`)
		reAlt := regexp.MustCompile(`class="[^"]*\bskel\b[^"]*"\s+id="` + id + `"`)
		if !re.MatchString(body) && !reAlt.MatchString(body) {
			t.Errorf("metric element %q must start with the skel placeholder class", id)
		}
	}

	if strings.Count(body, `classList.remove("skel")`) < 4 {
		t.Error("each of the 4 metric elements must have its skel class removed once real data renders")
	}
}

// TestWorkbenchNormalizesNullArraysFromOlderRelayVersions 校验前端把
// Relay 旧版本仍可能返回的 null 集合字段（升级前的 BuildDashboard bug）
// 规整成 []，而不是直接判定整份 payload 非法、展示笼统的全页错误。
func TestWorkbenchNormalizesNullArraysFromOlderRelayVersions(t *testing.T) {
	body := workbenchBody(t)

	if !strings.Contains(body, "function normalizeDashboardPayload") {
		t.Fatal("workbench must define normalizeDashboardPayload to tolerate null list fields from older Relay versions")
	}
	if !strings.Contains(body, "=== null") {
		t.Error("normalization must explicitly treat null as the empty-list case")
	}

	normalizeCallIdx := strings.Index(body, "data = normalizeDashboardPayload(data)")
	if normalizeCallIdx == -1 {
		t.Fatal("fetch handler must call normalizeDashboardPayload(data) before validating the payload")
	}
	validateCallIdx := strings.Index(body, "if (!isValidDashboardPayload(data))")
	if validateCallIdx == -1 {
		t.Fatal("fetch handler must still validate the (now normalized) payload")
	}
	if normalizeCallIdx > validateCallIdx {
		t.Error("normalizeDashboardPayload must run BEFORE isValidDashboardPayload, not after")
	}
}

func TestWorkbenchOverviewUsesRecentTasksInsteadOfActiveTasks(t *testing.T) {
	body := workbenchBody(t)

	for _, marker := range []string{
		"最近执行任务",
		"d.recent_tasks = normalizeListField(d.recent_tasks)",
		"Array.isArray(d.recent_tasks)",
		"renderActiveTasksTable(data.recent_tasks)",
		`"最近执行 " + String(data.recent_tasks.length)`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("workbench overview must use recent_tasks contract via %q", marker)
		}
	}
	if strings.Contains(body, "<h2>进行中任务</h2>") {
		t.Error("overview card title must not keep the old 进行中任务 wording")
	}
}

// TestWorkbenchLoadingStateUsesNeutralGrayNotBrandColor 校验加载态使用中性
// 灰色而不是品牌色，与"加载灰、空态正常、过期黄、不可用红"的状态语义保持
// 一致（stale 黄色 / error 红色已经是既有实现，这里只需要锁定 loading）。
func TestWorkbenchLoadingStateUsesNeutralGrayNotBrandColor(t *testing.T) {
	body := workbenchBody(t)

	re := regexp.MustCompile(`\.status-banner\[data-state="loading"\]\s*\{([^}]*)\}`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("could not find .status-banner[data-state=\"loading\"] CSS rule")
	}
	rule := m[1]
	if strings.Contains(rule, "--brand") {
		t.Errorf("loading state must use a neutral gray, not the brand color: %s", rule)
	}
	if !strings.Contains(rule, "--text-muted") && !strings.Contains(rule, "--text-faint") {
		t.Errorf("loading state should use a neutral text token (--text-muted/--text-faint): %s", rule)
	}

	// stale/error 必须保持既有的黄/红语义不被这次改动动到。
	if !strings.Contains(body, `.status-banner[data-state="stale"] { background: var(--warn-bg); color: var(--warn);`) {
		t.Error("stale state must remain yellow (--warn-bg/--warn)")
	}
	if !strings.Contains(body, `.status-banner[data-state="error"] { background: var(--danger-bg); color: var(--danger)`) {
		t.Error("error/unavailable state must remain red (--danger-bg/--danger)")
	}
}

// TestWorkbenchStaleAndErrorMessagesStaySafeAndGeneric 校验 stale / error
// 提示语始终是固定的、不泄漏内部信息的安全文案，且 error 态一定带重试按钮；
// 同时防止未来有人不小心把 err.message / 堆栈之类的内部细节透传进
// setStatusBanner。
func TestWorkbenchStaleAndErrorMessagesStaySafeAndGeneric(t *testing.T) {
	body := workbenchBody(t)

	if !strings.Contains(body, "刷新失败，当前显示的是最近一次成功获取的数据") {
		t.Error("stale message must keep showing cached data with a safe, generic explanation")
	}
	if !strings.Contains(body, "无法加载工作台数据，请稍后重试") {
		t.Error("error/unavailable message must stay generic and not expose internal detail")
	}
	if !strings.Contains(body, `id="status-retry-btn"`) {
		t.Error("error/unavailable state must offer a retry control")
	}

	forbiddenLeaks := []string{"TypeError", "NetworkError", "Failed to fetch", "err.message", "e.message", ".stack"}
	for _, word := range forbiddenLeaks {
		if strings.Contains(body, word) {
			t.Errorf("workbench must never surface raw internal error detail %q to the user", word)
		}
	}

	// 每个调用点都必须传字符串字面量作为提示语，不能传可能携带原始异常
	// 细节的变量，防止内部错误信息意外泄漏给用户。
	re := regexp.MustCompile(`setStatusBanner\(\s*"(?:loading|stale|error)"\s*,\s*(.)`)
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		if m[1] != `"` {
			t.Errorf("setStatusBanner() message argument must be a string literal, found dynamic argument starting with %q", m[1])
		}
	}
}

// TestWorkbenchNumericZeroFieldsNeverCollapseToDashPlaceholder 校验数值型
// 字段（消息数、工具调用次数、各类 token 计数、未完成/运行中任务数）始终
// 用显式格式化渲染，不会被 `value || "—"` 这类假值兜底逻辑误伤，
// 导致合法的 0 被显示成空白或短横线。
func TestWorkbenchNumericZeroFieldsNeverCollapseToDashPlaceholder(t *testing.T) {
	body := workbenchBody(t)

	numericFields := []string{
		"message_count", "tool_call_count", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_write_tokens", "reasoning_tokens",
		"total_session_count", "active_session_count",
	}
	for _, field := range numericFields {
		if strings.HasSuffix(field, "_tokens") {
			if !strings.Contains(body, "formatTokenMillions(session."+field+")") && !strings.Contains(body, "formatTokenMillions(agent."+field+")") {
				t.Errorf("expected explicit M-token formatting for numeric token field %q", field)
			}
		} else if !strings.Contains(body, "String(session."+field+")") && !strings.Contains(body, "String(agent."+field+")") {
			t.Errorf("expected an explicit String(...) conversion for numeric field %q", field)
		}
		dashPattern := field + ` || "—"`
		if strings.Contains(body, dashPattern) {
			t.Errorf("numeric field %q must not fall back to a dash on a legitimate zero value: found %q", field, dashPattern)
		}
	}
}

func TestWorkbenchFormatsTokensInMillionsAndLimitsActivityFeed(t *testing.T) {
	body := workbenchBody(t)

	for _, marker := range []string{
		"function formatTokenMillions(value)",
		`return "0 M"`,
		`return "0.01 M"`,
		"formatTokenMillions(agent.cache_read_tokens)",
		"formatTokenMillions(session.cache_read_tokens)",
		"formatTokenMillions(tokenTotal)",
		"return items.slice(0, 10)",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("workbench must format token counters in M and limit recent activity via %q", marker)
		}
	}
	if strings.Contains(body, "return items.slice(0, 20)") {
		t.Error("recent activity must be capped at 10 items, not the old 20-item limit")
	}
}

func TestWorkbenchUsesLatestUserPromptForSearchAndDetails(t *testing.T) {
	body := workbenchBody(t)

	for _, marker := range []string{
		"session.last_user_prompt",
		"session.last_user_prompt_at",
		"最新用户提示",
		"最新用户提示时间",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("workbench must expose latest sanitized user prompt metadata via %q", marker)
		}
	}
}

func TestWorkbenchShowsTruthfulDisabledLarkHandoffStatus(t *testing.T) {
	body := workbenchBody(t)

	for _, marker := range []string{
		"lark_handoff_available",
		"lark_handoff_reason",
		"handoff_reason",
		"上次创建中断，可重试",
		"Lark 交接",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("workbench must render read-only Lark handoff capability status via %q", marker)
		}
	}
}

func TestWorkbenchRecentActivityContractLabelsAndSessionPromptSafety(t *testing.T) {
	body := workbenchBody(t)

	if strings.Contains(body, `typeLabel: "会话活动"`) || strings.Contains(body, "会话活动") {
		t.Fatal("recent activity must never emit the generic 会话活动 label")
	}
	if !strings.Contains(body, `typeLabel: "用户发送提示词"`) {
		t.Error("session activity must use 用户发送提示词 when latest user prompt metadata exists")
	}
	if !strings.Contains(body, "if (!session.last_user_prompt_at) return;") {
		t.Error("session activity must only emit when last_user_prompt_at exists")
	}
	if !strings.Contains(body, `agent: session.model || "未知模型"`) {
		t.Error("session activity must use session.model with 未知模型 fallback")
	}
	if !strings.Contains(body, `related: session.last_user_prompt || session.title`) {
		t.Error("session activity related text must be limited to last_user_prompt/title")
	}

	if !strings.Contains(body, `typeLabel: "任务执行中"`) {
		t.Error("running task activity label must be exactly 任务执行中")
	}
	if strings.Contains(body, `typeLabel: "任务进行中"`) {
		t.Error("running task activity label must not use 任务进行中")
	}

	if !strings.Contains(body, `typeLabel: "任务失败"`) {
		t.Error("failed task activity must emit 任务失败")
	}
	if !strings.Contains(body, `pillClass: "status-warn"`) {
		t.Error("failed task activity must use warning state")
	}
}

func TestWorkbenchAgentEmptyStateMentionsModelSessions(t *testing.T) {
	body := workbenchBody(t)

	if !strings.Contains(body, "暂无模型会话数据") {
		t.Error("Agent empty state must say 暂无模型会话数据")
	}
	if strings.Contains(body, "暂无未完成任务分配给任何 Agent") {
		t.Error("Agent empty state must not use task-assignment wording")
	}
}
