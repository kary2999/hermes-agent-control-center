package relay

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testToken       = "shared-secret-token"
	testRedirectURL = "https://example.com"
)

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := NewHandler(NewSnapshotStore(), testToken, testRedirectURL, testLogger())
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

func TestHandleGateRedirectsToDashboardWithValidSession(t *testing.T) {
	h := newTestHandler(t)
	cookie := validSessionCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	resp := doRequest(h, req)

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/dashboard" {
		t.Errorf("Location = %q, want %q", loc, "/dashboard")
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
	h, err := NewHandler(NewSnapshotStore(), testToken, testRedirectURL, logger)
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
	h, err := NewHandler(NewSnapshotStore(), testToken, testRedirectURL, logger)
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

func newTestHandlerWithToken(t *testing.T, token string) *Handler {
	t.Helper()
	h, err := NewHandler(NewSnapshotStore(), token, testRedirectURL, testLogger())
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
