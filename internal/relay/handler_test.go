package relay

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	testToken          = "shared-secret-token"
	testDashboardToken = "dashboard-only-token"
	testRedirectURL    = "https://example.com"
)

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, testRedirectURL, testLogger())
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return h
}

// newTestHandlerWithoutDashboardToken 模拟未配置 HERMES_DASHBOARD_TOKEN
// 的既有部署，确保这条 Lark 直达链路完全可选、向后兼容。
func newTestHandlerWithoutDashboardToken(t *testing.T) *Handler {
	t.Helper()
	h, err := NewHandler(NewSnapshotStore(), testToken, "", testRedirectURL, testLogger())
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return h
}

func doRequest(h *Handler, req *http.Request) *http.Response {
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec.Result()
}

// validSessionCookie exercises the real POST /api/v1/session path to obtain
// a cookie signed by h, rather than hand-constructing one, so the test
// tracks whatever signing scheme the handler actually uses.
func validSessionCookie(t *testing.T, h *Handler) *http.Cookie {
	t.Helper()
	return validSessionCookieWithToken(t, h, testToken)
}

func validSessionCookieWithToken(t *testing.T, h *Handler, token string) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp := doRequest(h, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/session setup: status = %d, want 200", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("POST /api/v1/session setup: no session cookie in response")
	return nil
}

// --- 1. unauthenticated GET / must not leak product identity ---

func TestHandleGateServesGenericPage(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := doRequest(h, req)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200 (must not require auth)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-store")
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Error("Content-Security-Policy header missing on gate page")
	}

	body := readBody(t, resp)
	forbidden := []string{"hermes", "agent", "控制中心", "任务"}
	lower := strings.ToLower(body)
	for _, word := range forbidden {
		if strings.Contains(lower, strings.ToLower(word)) {
			t.Errorf("unauthenticated gate page body leaks identifying term %q:\n%s", word, body)
		}
	}
}

// cspDirective 从 Content-Security-Policy 头部值中提取指定 directive
// 对应的、以空格分隔的来源列表（例如 "frame-ancestors 'none'" ->
// ["'none'"]）。若 directive 不存在则直接判失败，避免 directive
// 名称拼写错误时静默通过。
func cspDirective(t *testing.T, csp, directive string) []string {
	t.Helper()
	for part := range strings.SplitSeq(csp, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == directive {
			return fields[1:]
		}
	}
	t.Fatalf("CSP %q missing directive %q", csp, directive)
	return nil
}

// TestGateContentSecurityPolicyFrameAncestors 依据官方 Lark/飞书
// AppLink 文档，将 frame-ancestors 严格限定为 Lark 国际版
// （larksuite.com）和飞书中国版（feishu.cn）的 AppLink 来源。gate
// 页面嵌在 Lark/飞书应用内浏览器 frame 中，因此不能维持 'none'，
// 但也不能对其他任何来源开放。
func TestGateContentSecurityPolicyFrameAncestors(t *testing.T) {
	got := cspDirective(t, gateContentSecurityPolicy, "frame-ancestors")
	want := []string{"https://*.larksuite.com", "https://*.feishu.cn"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("frame-ancestors sources = %v, want %v", got, want)
	}
}

func TestGateContentSecurityPolicyFrameAncestorsSources(t *testing.T) {
	sources := cspDirective(t, gateContentSecurityPolicy, "frame-ancestors")
	present := make(map[string]bool, len(sources))
	for _, s := range sources {
		present[s] = true
	}

	cases := []struct {
		name    string
		source  string
		allowed bool
	}{
		{name: "Lark international AppLink origin (larksuite.com)", source: "https://*.larksuite.com", allowed: true},
		{name: "Feishu China AppLink origin (feishu.cn)", source: "https://*.feishu.cn", allowed: true},
		{name: "frame-ancestors 'none' must not be present", source: "'none'", allowed: false},
		{name: "all-origin wildcard '*' must not be present", source: "*", allowed: false},
		{name: "unrelated origin must not be present", source: "https://example.com", allowed: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := present[tc.source]; got != tc.allowed {
				t.Errorf("frame-ancestors contains %q = %v, want %v (sources: %v)", tc.source, got, tc.allowed, sources)
			}
		})
	}

	if len(sources) != 2 {
		t.Errorf("frame-ancestors has %d source(s), want exactly 2 (no other origins allowed): %v", len(sources), sources)
	}
}

func TestHandleGateEmbedsConfiguredRedirectAndFallbackFlow(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	// html/template JS-escapes "/" as "\/" inside the inline <script> string
	// context, so compare against that form rather than the raw URL.
	escapedRedirectURL := strings.ReplaceAll(testRedirectURL, "/", `\/`)
	if !strings.Contains(body, escapedRedirectURL) {
		t.Errorf("gate page does not embed configured redirect url %q (want %q in body):\n%s", testRedirectURL, escapedRedirectURL, body)
	}
	if !strings.Contains(body, "window.prompt") {
		t.Error("gate page script does not prompt for a token")
	}
	if !strings.Contains(body, "/api/v1/session") {
		t.Error("gate page script does not call POST /api/v1/session")
	}
	// Cancel/empty-token and verification-failure paths must both lead back
	// to the same redirect entry point.
	if strings.Count(body, "redirectAway()") < 3 {
		t.Errorf("gate page script must call redirectAway() from the cancel/empty path, the failed-verification path, and the network-error path; got %d calls", strings.Count(body, "redirectAway()"))
	}
}

func TestHandleGateRedirectsToDemoV2WithValidSession(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/demo-v2" {
		t.Errorf("Location = %q, want %q", loc, "/demo-v2")
	}
}

func TestHandleGateSuccessfulTokenExchangeScriptRedirectsToDemoV2(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, "window.location.replace('/demo-v2')") {
		t.Error("gate page script must redirect a successful token exchange to /demo-v2")
	}
	if strings.Contains(body, "window.location.replace('/dashboard')") {
		t.Error("gate page script must not redirect a successful token exchange to /dashboard")
	}
}

// --- 2. POST /api/v1/session ---

func TestPostSessionValidTokenSetsCookie(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/session", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp := doRequest(h, req)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-store")
	}

	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie set")
	}
	if !cookie.Secure {
		t.Error("session cookie missing Secure")
	}
	if !cookie.HttpOnly {
		t.Error("session cookie missing HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("Path = %q, want %q", cookie.Path, "/")
	}
	if cookie.MaxAge <= 0 && cookie.Expires.IsZero() {
		t.Error("session cookie has no expiry (neither Max-Age nor Expires set)")
	}
	if strings.Contains(cookie.Value, testToken) {
		t.Errorf("session cookie value contains the raw shared token: %q", cookie.Value)
	}
}

func TestPostSessionRejectsBadCredentials(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{name: "no authorization header", header: ""},
		{name: "wrong token", header: "Bearer wrong-token"},
		{name: "empty bearer", header: "Bearer "},
		{name: "missing bearer prefix", header: testToken},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandler(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/session", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp := doRequest(h, req)

			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
			if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want %q", cc, "no-store")
			}
			for _, c := range resp.Cookies() {
				if c.Name == sessionCookieName {
					t.Fatal("session cookie must not be set on failed verification")
				}
			}
		})
	}
}

// --- 3. GET /dashboard session protection ---

func TestDashboardPageRejectsMissingOrForgedSession(t *testing.T) {
	h := newTestHandler(t)
	otherHandler := newTestHandlerWithToken(t, "a-completely-different-token")

	tamperedCookie := validSessionCookie(t, h)
	tamperedCookie.Value = tamperedCookie.Value + "tampered"

	expiredHandler := newTestHandler(t)
	fixedNow := time.Now()
	expiredHandler.now = func() time.Time { return fixedNow }
	expiredCookieValue := signSessionValue(expiredHandler.sessionKey, fixedNow.Add(-time.Minute))

	wrongKeyCookie := validSessionCookieWithToken(t, otherHandler, "a-completely-different-token")

	cases := []struct {
		name   string
		cookie *http.Cookie
	}{
		{name: "no cookie", cookie: nil},
		{name: "malformed cookie value", cookie: &http.Cookie{Name: sessionCookieName, Value: "not-a-valid-value"}},
		{name: "tampered signature", cookie: tamperedCookie},
		{name: "signed with a different token's key", cookie: wrongKeyCookie},
		{name: "expired", cookie: &http.Cookie{Name: sessionCookieName, Value: expiredCookieValue}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := h
			if tc.name == "expired" {
				target = expiredHandler
			}
			req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			resp := doRequest(target, req)

			if resp.StatusCode != http.StatusFound {
				t.Fatalf("status = %d, want 302", resp.StatusCode)
			}
			if loc := resp.Header.Get("Location"); loc != testRedirectURL {
				t.Errorf("Location = %q, want %q", loc, testRedirectURL)
			}
		})
	}
}

func TestDashboardPageServesContentWithValidSession(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-store")
	}

	body := readBody(t, resp)
	if strings.Contains(body, "window.prompt") {
		t.Error("dashboard page must not prompt for a shared token")
	}
	if strings.Contains(body, "localStorage") || strings.Contains(body, "sessionStorage") {
		t.Error("dashboard page must not persist the shared token in browser storage")
	}
	if !strings.Contains(body, "/api/v1/dashboard") {
		t.Error("dashboard page must fetch /api/v1/dashboard")
	}
}

// --- 4. GET /api/v1/dashboard read auth boundary; POST /api/v1/snapshot write auth boundary ---

func TestAPIDashboardAcceptsBearerOrSession(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	cases := []struct {
		name       string
		withBearer bool
		withCookie bool
		wantStatus int
	}{
		{name: "valid bearer only", withBearer: true, wantStatus: http.StatusOK},
		{name: "valid session cookie only", withCookie: true, wantStatus: http.StatusOK},
		{name: "neither", wantStatus: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
			if tc.withBearer {
				req.Header.Set("Authorization", "Bearer "+testToken)
			}
			if tc.withCookie {
				req.AddCookie(cookie)
			}
			resp := doRequest(h, req)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestAPISnapshotRequiresBearerEvenWithValidSession(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)
	validBody := `{"device_id":"mac-mini","snapshot":{"taken_at":"2026-01-01T00:00:00Z","tasks":[],"runs":[]}}`

	cases := []struct {
		name       string
		withBearer bool
		withCookie bool
		wantStatus int
	}{
		{name: "valid bearer, no cookie", withBearer: true, wantStatus: http.StatusOK},
		{name: "valid session cookie only, no bearer", withCookie: true, wantStatus: http.StatusUnauthorized},
		{name: "neither", wantStatus: http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/snapshot", strings.NewReader(validBody))
			if tc.withBearer {
				req.Header.Set("Authorization", "Bearer "+testToken)
			}
			if tc.withCookie {
				req.AddCookie(cookie)
			}
			resp := doRequest(h, req)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

// --- 5. anonymous 401 responses must not leak product identity ---

func TestRejectUnauthorizedDoesNotLeakProductIdentity(t *testing.T) {
	h := newTestHandler(t)

	requests := map[string]*http.Request{
		"POST /api/v1/session bad token": httptest.NewRequest(http.MethodPost, "/api/v1/session", nil),
		"GET /api/v1/dashboard no auth":  httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil),
		"POST /api/v1/snapshot no auth":  httptest.NewRequest(http.MethodPost, "/api/v1/snapshot", strings.NewReader(`{}`)),
	}

	forbidden := []string{"hermes", "agent", "control center", "控制中心", "任务", "任務"}

	for name, req := range requests {
		t.Run(name, func(t *testing.T) {
			resp := doRequest(h, req)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}

			wwwAuth := resp.Header.Get("WWW-Authenticate")
			body := readBody(t, resp)
			lowerHeader := strings.ToLower(wwwAuth)
			lowerBody := strings.ToLower(body)

			for _, word := range forbidden {
				lowerWord := strings.ToLower(word)
				if strings.Contains(lowerHeader, lowerWord) {
					t.Errorf("WWW-Authenticate header leaks identifying term %q: %q", word, wwwAuth)
				}
				if strings.Contains(lowerBody, lowerWord) {
					t.Errorf("401 response body leaks identifying term %q: %q", word, body)
				}
			}
		})
	}
}

// --- 6. write errors on handleGate / handleDashboardPage must not panic and must be logged ---

// failingResponseWriter always fails Write, simulating a client that
// disconnects mid-response.
type failingResponseWriter struct {
	header http.Header
}

func (f *failingResponseWriter) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}

func (f *failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("simulated write failure")
}

func (f *failingResponseWriter) WriteHeader(int) {}

func TestHandleGateWriteFailureDoesNotPanicAndIsLogged(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, testRedirectURL, logger)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("handleGate panicked on write failure: %v", r)
			}
		}()
		h.handleGate(&failingResponseWriter{}, req)
	}()

	if !strings.Contains(logBuf.String(), "level\":\"ERROR\"") {
		t.Errorf("expected an ERROR level log entry for the write failure, got: %s", logBuf.String())
	}
}

func TestHandleDashboardPageWriteFailureDoesNotPanicAndIsLogged(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, testRedirectURL, logger)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("handleDashboardPage panicked on write failure: %v", r)
			}
		}()
		h.handleDashboardPage(&failingResponseWriter{}, req)
	}()

	if !strings.Contains(logBuf.String(), "level\":\"ERROR\"") {
		t.Errorf("expected an ERROR level log entry for the write failure, got: %s", logBuf.String())
	}
}

// --- 7. GET /demo-v2 session protection (UI-only mock demo, no live data) ---

func TestDemoV2PageRejectsMissingOrForgedSession(t *testing.T) {
	h := newTestHandler(t)

	tamperedCookie := validSessionCookie(t, h)
	tamperedCookie.Value = tamperedCookie.Value + "tampered"

	cases := []struct {
		name   string
		cookie *http.Cookie
	}{
		{name: "no cookie", cookie: nil},
		{name: "malformed cookie value", cookie: &http.Cookie{Name: sessionCookieName, Value: "not-a-valid-value"}},
		{name: "tampered signature", cookie: tamperedCookie},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			resp := doRequest(h, req)

			if resp.StatusCode != http.StatusFound {
				t.Fatalf("status = %d, want 302", resp.StatusCode)
			}
			if loc := resp.Header.Get("Location"); loc != testRedirectURL {
				t.Errorf("Location = %q, want %q", loc, testRedirectURL)
			}
		})
	}
}

func TestDemoV2PageServesContentWithValidSession(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-store")
	}

	body := readBody(t, resp)
	if !strings.Contains(body, "演示数据") && !strings.Contains(body, "演示數據") {
		t.Error("demo-v2 page must carry an obvious 演示数据 (mock data) marker")
	}
	if strings.Contains(body, "fetch(") {
		t.Error("demo-v2 page must be a static mock with zero network requests")
	}
	if strings.Contains(body, "XMLHttpRequest") {
		t.Error("demo-v2 page must be a static mock with zero network requests")
	}
	if strings.Contains(body, "/api/v1/") {
		t.Error("demo-v2 page must never reference live Hermes API endpoints")
	}
}

func TestHandleDemoV2PageWriteFailureDoesNotPanicAndIsLogged(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, testRedirectURL, logger)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("handleDemoV2Page panicked on write failure: %v", r)
			}
		}()
		h.handleDemoV2Page(&failingResponseWriter{}, req)
	}()

	if !strings.Contains(logBuf.String(), "level\":\"ERROR\"") {
		t.Errorf("expected an ERROR level log entry for the write failure, got: %s", logBuf.String())
	}
}

// --- 8. GET /demo-v2 会话页信息层级 + 「在 Lark 继续」原型（UI-only, 零网络请求） ---

func TestDemoV2SessionsPageGroupsPinnedSessionsWithFullRowMetadata(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, "置顶会话") {
		t.Error("会话页必须有一个独立于其他/项目会话之上的「置顶会话」分组")
	}
	if !strings.Contains(body, "session_id") {
		t.Error("每条会话行必须展示精确的 mock session_id 标签文本")
	}
	for _, marker := range []string{"条消息", "次工具调用"} {
		if !strings.Contains(body, marker) {
			t.Errorf("会话行缺少必需的元数据标记 %q（消息数 / 工具调用数）", marker)
		}
	}
}

func TestDemoV2SessionDetailHasLarkContinueButtonWithConfirmDialog(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, "在 Lark 继续") {
		t.Error("会话详情必须有「在 Lark 继续」按钮")
	}
	if !strings.Contains(body, "<dialog") {
		t.Error("「在 Lark 继续」必须打开一个可访问的 <dialog> 确认原型，而不是立即生效")
	}
	if !strings.Contains(body, "创建话题") || !strings.Contains(body, "取消") {
		t.Error("确认对话框必须提供「创建话题」与「取消」两个操作")
	}
	if !strings.Contains(body, "演示") {
		t.Error("Lark 续接原型必须明确标注为演示，避免被误认为真实创建话题")
	}
}

func TestDemoV2PageStillMakesZeroNetworkRequestsAfterLarkPrototype(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	for _, forbidden := range []string{"fetch(", "XMLHttpRequest", "/api/v1/", "axios", "WebSocket("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("新增的「在 Lark 继续」原型必须保持零网络请求，但发现了 %q", forbidden)
		}
	}
}

// --- 9. GET /demo-v2 亮/暗主题切换（UI-only，遵循系统偏好并可持久化） ---

func TestDemoV2PageHasAccessibleThemeToggleWithDarkVariablesAndPersistence(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, `id="theme-toggle"`) {
		t.Error("必须有一个 id=\"theme-toggle\" 的主题切换按钮")
	}
	if !strings.Contains(body, "aria-pressed") {
		t.Error("主题切换按钮必须携带 aria-pressed 以反映当前状态")
	}
	if !strings.Contains(body, `[data-theme="dark"]`) {
		t.Error("必须存在基于 [data-theme=\"dark\"] 的暗色语义变量覆盖")
	}
	if strings.Count(body, "--brand: #3fb37f;") < 2 {
		t.Error("显式暗色主题和系统暗色主题都必须使用克制的绿色主强调色")
	}
	if !strings.Contains(body, "prefers-color-scheme") {
		t.Error("初始主题必须回退到 prefers-color-scheme")
	}
	if !strings.Contains(body, "localStorage") {
		t.Error("显式选择的主题必须持久化到 localStorage")
	}
	for _, forbidden := range []string{"linear-gradient(", "radial-gradient("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("暗色主题不允许渐变，但发现了 %q", forbidden)
		}
	}
}

// --- 10. GET /demo-v2 最近活动可访问详情抽屉（白名单字段，无推理/原始 prompt/密钥） ---

func TestDemoV2RecentActivityRowsAreAccessibleButtonsOpeningDetailPanel(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, `class="activity-row"`) {
		t.Error("最近活动每一行必须是可识别的 activity-row 元素")
	}
	if !strings.Contains(body, `id="activity-detail-panel"`) {
		t.Error("必须有一个 id=\"activity-detail-panel\" 的详情面板（桌面右侧抽屉 / 移动端全宽视图共用）")
	}
	if !strings.Contains(body, `id="activity-detail-close"`) {
		t.Error("详情面板必须有明确的关闭/返回控件 id=\"activity-detail-close\"")
	}
	for _, marker := range []string{"负责 Agent", "关联", "耗时", "摘要", "工具调用"} {
		if !strings.Contains(body, marker) {
			t.Errorf("详情面板缺少必需的白名单字段标签 %q", marker)
		}
	}
	for _, forbidden := range []string{"raw_prompt", "system_prompt", "api_key", "secret_key", "推理过程"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("详情面板绝不能出现推理/原始 prompt/密钥相关内容，但发现了 %q", forbidden)
		}
	}
}

// --- 11. GET /demo-v2 自定义可访问状态筛选控件（替换原生 select） ---

func TestDemoV2SessionFilterIsAccessiblePillGroupNotNativeSelect(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if strings.Contains(body, `<select id="session-filter"`) {
		t.Error("原生 <select> 状态筛选控件必须被移除")
	}
	if !strings.Contains(body, `role="radiogroup"`) {
		t.Error("状态筛选必须是 role=\"radiogroup\" 的可访问药丸控件")
	}
	for _, label := range []string{"全部状态", "进行中", "已完成", "失败", "等待中"} {
		if !strings.Contains(body, label) {
			t.Errorf("状态筛选缺少选项 %q", label)
		}
	}
	if !strings.Contains(body, `id="session-filter-reset"`) {
		t.Error("状态筛选必须提供清除/重置入口 id=\"session-filter-reset\"")
	}
}

// --- 12. GET /demo-v2 产出物两种明确的查看方式（外链 / 只读快照） ---

func TestDemoV2ArtifactsHaveExplicitLinkAndSnapshotViewTypes(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, `target="_blank"`) || !strings.Contains(body, `rel="noopener noreferrer"`) {
		t.Error("外链产出物必须使用 target=\"_blank\" rel=\"noopener noreferrer\"")
	}
	if !strings.Contains(body, "https://") {
		t.Error("外链产出物必须是 https 链接")
	}
	if !strings.Contains(body, `id="artifact-snapshot-dialog"`) {
		t.Error("只读快照必须在 id=\"artifact-snapshot-dialog\" 的可访问对话框中打开")
	}
	for _, forbidden := range []string{"/Users/", `C:\`, "/home/"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("产出物展示绝不能暴露本地绝对路径，但发现了 %q", forbidden)
		}
	}
}

// --- 13. GET /demo-v2 Lark 建话题去重原型（完整状态机 + 防重复点击） ---

func TestDemoV2LarkTopicCreationHasDedupeStateMachineAndDoubleClickGuard(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	for _, state := range []string{"未创建", "检查中", "创建中", "已绑定", "话题失效", "绑定异常", "已归档"} {
		if !strings.Contains(body, state) {
			t.Errorf("Lark 建话题原型缺少状态文案 %q", state)
		}
	}
	if !strings.Contains(body, "larkCreateInFlight") {
		t.Error("必须有防止快速双击/重复创建的进行中标志 larkCreateInFlight")
	}
}

// --- 14. GET /demo-v2 全量静态零网络守卫（覆盖全部新增交互） ---

func TestDemoV2PageMakesZeroNetworkRequestsAcrossAllFeatures(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/demo-v2", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	for _, forbidden := range []string{"fetch(", "XMLHttpRequest", "sendBeacon(", "WebSocket(", "EventSource(", "/api/v1/"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("演示页面必须保持零网络请求，但发现了 %q", forbidden)
		}
	}
}

func newTestHandlerWithToken(t *testing.T, token string) *Handler {
	t.Helper()
	h, err := NewHandler(NewSnapshotStore(), token, testDashboardToken, testRedirectURL, testLogger())
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return h
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}
