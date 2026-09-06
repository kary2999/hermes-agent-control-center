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
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, "handoff-only-token", testRedirectURL, testLogger(), "")
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return h
}

// newTestHandlerWithoutDashboardToken 模拟未配置 HERMES_DASHBOARD_TOKEN
// 的既有部署，确保这条 Lark 直达链路完全可选、向后兼容。
func newTestHandlerWithoutDashboardToken(t *testing.T) *Handler {
	t.Helper()
	h, err := NewHandler(NewSnapshotStore(), testToken, "", "", testRedirectURL, testLogger(), "")
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
	if scriptSrc := cspDirective(t, gateContentSecurityPolicy, "script-src"); !reflect.DeepEqual(scriptSrc, []string{"'unsafe-inline'"}) {
		t.Errorf("script-src = %v, want only inline script for token exchange", scriptSrc)
	}
	if styleSrc := cspDirective(t, gateContentSecurityPolicy, "style-src"); !reflect.DeepEqual(styleSrc, []string{"'unsafe-inline'"}) {
		t.Errorf("style-src = %v, want only inline styles so the gate renders inside Lark", styleSrc)
	}
}

func TestHandleGateStaysOnSameOriginForManualAccess(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if strings.Contains(body, testRedirectURL) || strings.Contains(body, "window.prompt") || strings.Contains(body, "redirectAway") {
		t.Fatalf("gate page must not auto-prompt or redirect users away from hermes host:\n%s", body)
	}
	if !strings.Contains(body, "id=\"access-form\"") || !strings.Contains(body, "访问码") {
		t.Errorf("gate page must render an in-page access form instead of leaving the host:\n%s", body)
	}
	if !strings.Contains(body, "/api/v1/session") {
		t.Error("gate page script does not call POST /api/v1/session")
	}
	if !strings.Contains(body, "访问码无效或已过期") || !strings.Contains(body, "网络异常") {
		t.Error("gate page must show inline verification failure messages instead of redirecting away")
	}
}

func TestHandleGateRedirectsToWorkbenchWithValidSession(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/workbench" {
		t.Errorf("Location = %q, want %q", loc, "/workbench")
	}
}

func TestHandleGateSuccessfulTokenExchangeScriptRedirectsToWorkbench(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, "window.location.replace('/workbench')") {
		t.Error("gate page script must redirect a successful token exchange to /workbench")
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

func TestPostSessionScopesCookieAndHandoffRejectsReadOnlyCookie(t *testing.T) {
	h := newTestHandler(t)

	readOnly := validSessionCookieWithToken(t, h, testDashboardToken)
	scopeReq := httptest.NewRequest(http.MethodGet, "/", nil)
	scopeReq.AddCookie(readOnly)
	if !h.hasValidSessionScope(scopeReq, sessionScopeRead) {
		t.Fatal("dashboard token cookie was not read-scoped")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess_123/lark-handoff", strings.NewReader(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`))
	req.Host = "relay.example.com"
	req.Header.Set("Origin", "https://relay.example.com")
	req.Header.Set("X-Hermes-Action", "lark-handoff")
	req.AddCookie(readOnly)
	resp := doRequest(h, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only cookie handoff status = %d, want 403", resp.StatusCode)
	}

	handoff := validSessionCookieWithToken(t, h, "handoff-only-token")
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess_123/lark-handoff", strings.NewReader(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440001"}`))
	req.Host = "relay.example.com"
	req.Header.Set("Origin", "https://relay.example.com")
	req.Header.Set("X-Hermes-Action", "lark-handoff")
	req.AddCookie(handoff)
	resp = doRequest(h, req)
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("handoff cookie status = %d, want route-specific validation instead of auth rejection", resp.StatusCode)
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

func TestDashboardPageRedirectsMissingOrForgedSessionToSameOriginGate(t *testing.T) {
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
			if loc := resp.Header.Get("Location"); loc != "/" {
				t.Errorf("Location = %q, want %q", loc, "/")
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
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, "", testRedirectURL, logger, "")
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
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, "", testRedirectURL, logger, "")
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

// --- 7. GET /workbench session protection (live-data workbench) ---

func TestWorkbenchPageRedirectsMissingOrForgedSessionToSameOriginGate(t *testing.T) {
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
			req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			resp := doRequest(h, req)

			if resp.StatusCode != http.StatusFound {
				t.Fatalf("status = %d, want 302", resp.StatusCode)
			}
			if loc := resp.Header.Get("Location"); loc != "/" {
				t.Errorf("Location = %q, want %q", loc, "/")
			}
		})
	}
}

func TestWorkbenchPageServesContentWithValidSessionAndFetchesLiveDashboardAPI(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-store")
	}

	body := readBody(t, resp)
	if !strings.Contains(body, "/api/v1/dashboard") {
		t.Error("workbench page must fetch the live GET /api/v1/dashboard API")
	}
	if strings.Contains(body, "window.prompt") {
		t.Error("workbench page must not prompt for a shared token")
	}
}

func TestWorkbenchPageFetchUsesAbortControllerWithTimeout(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, "new AbortController()") {
		t.Error("workbench fetch must use an AbortController so a hung request can be aborted")
	}
	if !strings.Contains(body, "signal: controller.signal") {
		t.Error("workbench fetch must pass the AbortController's signal into the fetch() call")
	}
	if !strings.Contains(body, "FETCH_TIMEOUT_MS") || !strings.Contains(body, "controller.abort()") {
		t.Error("workbench fetch must abort itself after a bounded timeout, not hang indefinitely")
	}
}

func TestWorkbenchPagePollsEvery15SecondsAndPausesWhenHidden(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, "POLL_INTERVAL_MS = 15000") {
		t.Error("workbench must poll the dashboard API on a 15 second interval")
	}
	if !strings.Contains(body, "visibilitychange") {
		t.Error("workbench must react to visibilitychange so it can pause polling in the background")
	}
	if !strings.Contains(body, "document.hidden") {
		t.Error("workbench must check document.hidden to skip scheduling polls while the tab is hidden")
	}
}

func TestWorkbenchPageHandles401BySendingBrowserBackToGate(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, "res.status === 401") {
		t.Error("workbench fetch must specifically detect a 401 response from the dashboard API")
	}
	if !strings.Contains(body, `window.location.replace("/")`) {
		t.Error("workbench must send an expired/invalid session back to the gate at / on 401, not just show an error banner")
	}
}

func TestWorkbenchPageBuildsDOMSafely(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	unsafeProperty := "." + "inner" + "HTML"
	if strings.Contains(body, unsafeProperty) {
		t.Error("workbench must never assign API-derived strings via unsafe HTML sinks")
	}
	if !strings.Contains(body, "createElement") || !strings.Contains(body, "textContent") {
		t.Error("workbench must construct API-derived DOM nodes via createElement/textContent")
	}
}

func TestWorkbenchPageHasNoMockDataMarkersOrStaticSampleLabels(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	for _, forbidden := range []string{"演示数据", "演示數據", "mock", "Mock", "MOCK", "sess-mock", "示例会话", "示例數據"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("workbench serves live data and must not carry a leftover mock-data marker/label %q", forbidden)
		}
	}
}

func TestWorkbenchPageProvidesRealLarkHandoffWithoutSimulation(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, "在 Lark 继续") || !strings.Contains(body, "/lark-handoff") || !strings.Contains(body, "X-Hermes-Action") {
		t.Fatal("workbench must expose the real Lark handoff button and POST endpoint")
	}
	for _, forbidden := range []string{"创建话题", "larkCreateInFlight", `id="artifact-snapshot-dialog"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("workbench must not simulate a Lark topic write path (removed demo-only prototype), but found %q", forbidden)
		}
	}
}

func TestWorkbenchPageRendersNoForbiddenSensitiveFieldNames(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	forbidden := []string{
		"user_id", "chat_id", "thread_id", "session_key", "origin_json",
		"system_prompt", "raw_prompt", "api_key", "secret_key",
		"billing_url", "cost_usd", "git_branch", "\"cwd\"",
		"last_activity_description", "推理过程",
	}
	for _, word := range forbidden {
		if strings.Contains(body, word) {
			t.Errorf("workbench page source must never reference forbidden sensitive field name/content %q", word)
		}
	}
}

// TestWorkbenchPageDoesNotTreatSupersededOrBackgroundAbortsAsFailures guards
// against a real race: aborting the previous in-flight fetch (because a new
// poll/retry/visibility-triggered fetch superseded it, or because the tab
// went to the background) must not be mistaken for a genuine network
// failure that flashes the stale/error banner or double-schedules the next
// poll — only the fetch's own timeout firing should count as a failure.
func TestWorkbenchPageDoesNotTreatSupersededOrBackgroundAbortsAsFailures(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)
	body := readBody(t, resp)

	if !strings.Contains(body, "controller.intentional") {
		t.Error("fetchDashboard's catch/finally must check controller.intentional to distinguish a deliberate abort (superseded or backgrounded) from a real failure")
	}
}

func TestHandleWorkbenchPageWriteFailureDoesNotPanicAndIsLogged(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, "", testRedirectURL, logger, "")
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/workbench", nil)

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("handleWorkbenchPage panicked on write failure: %v", r)
			}
		}()
		h.handleWorkbenchPage(&failingResponseWriter{}, req)
	}()

	if !strings.Contains(logBuf.String(), "level\":\"ERROR\"") {
		t.Errorf("expected an ERROR level log entry for the write failure, got: %s", logBuf.String())
	}
}

func newTestHandlerWithToken(t *testing.T, token string) *Handler {
	t.Helper()
	h, err := NewHandler(NewSnapshotStore(), token, testDashboardToken, "", testRedirectURL, testLogger(), "")
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
