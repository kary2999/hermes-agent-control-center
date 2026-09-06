package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newHandoffTestHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, "handoff-only-token", testRedirectURL, testLogger(), t.TempDir())
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	h.store.Set("device", Snapshot{
		TakenAt:      time.Now().UTC(),
		Capabilities: Capabilities{LarkHandoff: true},
		Sessions:     []SessionSummary{{ID: "sess_123", ProfileName: "default"}},
	}, time.Now().UTC())
	return h
}

func TestPostLarkHandoffValidatesBrowserBoundaryAndReuses(t *testing.T) {
	h := newHandoffTestHandler(t)
	handoffCookie := validSessionCookieWithToken(t, h, "handoff-only-token")
	readOnlyCookie := validSessionCookieWithToken(t, h, testDashboardToken)

	readOnlyReq := newHandoffRequest(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`)
	readOnlyReq.AddCookie(readOnlyCookie)
	if resp := doRequest(h, readOnlyReq); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only status = %d, want 403", resp.StatusCode)
	}

	badBody := newHandoffRequest(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000","chat_id":"x"}`)
	badBody.AddCookie(handoffCookie)
	if resp := doRequest(h, badBody); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("browser chat_id status = %d, want 400", resp.StatusCode)
	}

	badOrigin := newHandoffRequest(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`)
	badOrigin.Header.Set("Origin", "https://evil.example.com")
	badOrigin.AddCookie(handoffCookie)
	if resp := doRequest(h, badOrigin); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad origin status = %d, want 403", resp.StatusCode)
	}

	okReq := newHandoffRequest(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`)
	okReq.AddCookie(handoffCookie)
	resp := doRequest(h, okReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid handoff status = %d, want 200", resp.StatusCode)
	}
	var first map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	repeat := newHandoffRequest(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`)
	repeat.AddCookie(handoffCookie)
	resp = doRequest(h, repeat)
	var second map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&second); err != nil {
		t.Fatalf("decode repeat response: %v", err)
	}
	if first["command_id"] == "" || first["command_id"] != second["command_id"] || first["handoff_state"] != handoffCommandStateQueued {
		t.Fatalf("handoff not reused/sanitized: first=%v second=%v", first, second)
	}

	unknown := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/missing/lark-handoff", strings.NewReader(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440001"}`))
	unknown.Host = "relay.example.com"
	unknown.Header.Set("Origin", "https://relay.example.com")
	unknown.Header.Set("X-Hermes-Action", "lark-handoff")
	unknown.AddCookie(handoffCookie)
	if resp := doRequest(h, unknown); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session status = %d, want 404", resp.StatusCode)
	}
}

func TestPostLarkHandoffStrictRequestShape(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		header     bool
		origin     string
		path       string
		wantStatus int
	}{
		{name: "unknown field", body: `{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000","chat_id":"x"}`, header: true, origin: "https://relay.example.com", path: "/api/v1/sessions/sess_123/lark-handoff", wantStatus: http.StatusBadRequest},
		{name: "missing custom header", body: `{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`, header: false, origin: "https://relay.example.com", path: "/api/v1/sessions/sess_123/lark-handoff", wantStatus: http.StatusForbidden},
		{name: "missing origin", body: `{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`, header: true, origin: "", path: "/api/v1/sessions/sess_123/lark-handoff", wantStatus: http.StatusForbidden},
		{name: "malformed id", body: `{"idempotency_key":"not-a-uuid"}`, header: true, origin: "https://relay.example.com", path: "/api/v1/sessions/sess_123/lark-handoff", wantStatus: http.StatusBadRequest},
		{name: "malformed json", body: `{"idempotency_key":`, header: true, origin: "https://relay.example.com", path: "/api/v1/sessions/sess_123/lark-handoff", wantStatus: http.StatusBadRequest},
		{name: "trailing json", body: `{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"} {}`, header: true, origin: "https://relay.example.com", path: "/api/v1/sessions/sess_123/lark-handoff", wantStatus: http.StatusBadRequest},
		{name: "malformed session id", body: `{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`, header: true, origin: "https://relay.example.com", path: "/api/v1/sessions/..%2Fbad/lark-handoff", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandoffTestHandler(t)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Host = "relay.example.com"
			req.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.header {
				req.Header.Set("X-Hermes-Action", "lark-handoff")
			}
			req.AddCookie(validSessionCookieWithToken(t, h, "handoff-only-token"))
			if resp := doRequest(h, req); resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestPostLarkHandoffRetriesFailedSnapshotButReusesCompleted(t *testing.T) {
	h := newHandoffTestHandler(t)
	cookie := validSessionCookieWithToken(t, h, "handoff-only-token")
	firstReq := newHandoffRequest(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`)
	firstReq.AddCookie(cookie)
	firstResp := doRequest(h, firstReq)
	var first map[string]string
	if err := json.NewDecoder(firstResp.Body).Decode(&first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if _, err := h.handoffStore.Complete(first["command_id"], handoffCommandStateCompleted, ""); err != nil {
		t.Fatalf("complete command: %v", err)
	}

	completedReq := newHandoffRequest(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440001"}`)
	completedReq.AddCookie(cookie)
	completedResp := doRequest(h, completedReq)
	var completed map[string]string
	if err := json.NewDecoder(completedResp.Body).Decode(&completed); err != nil {
		t.Fatalf("decode completed response: %v", err)
	}
	if completed["command_id"] != first["command_id"] {
		t.Fatalf("completed snapshot command_id = %q, want reuse %q", completed["command_id"], first["command_id"])
	}

	h.store.Set("device", Snapshot{
		TakenAt:      time.Now().UTC(),
		Capabilities: Capabilities{LarkHandoff: true},
		Sessions: []SessionSummary{{
			ID:              "sess_123",
			ProfileName:     "default",
			HandoffPlatform: handoffPlatformFeishu,
			HandoffState:    handoffCommandStateFailed,
		}},
	}, time.Now().UTC())
	failedReq := newHandoffRequest(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440002"}`)
	failedReq.AddCookie(cookie)
	failedResp := doRequest(h, failedReq)
	var retried map[string]string
	if err := json.NewDecoder(failedResp.Body).Decode(&retried); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}
	if retried["command_id"] == "" || retried["command_id"] == first["command_id"] {
		t.Fatalf("failed snapshot retry command_id = %q, first %q", retried["command_id"], first["command_id"])
	}
}

func TestDashboardUsesQueuedHandoffUntilHermesStateArrives(t *testing.T) {
	h := newHandoffTestHandler(t)
	cookie := validSessionCookieWithToken(t, h, "handoff-only-token")
	req := newHandoffRequest(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`)
	req.AddCookie(cookie)
	if resp := doRequest(h, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("enqueue status = %d, want 200", resp.StatusCode)
	}

	dashboardReq := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	dashboardReq.AddCookie(cookie)
	resp := doRequest(h, dashboardReq)
	var queued DashboardView
	if err := json.NewDecoder(resp.Body).Decode(&queued); err != nil {
		t.Fatalf("decode queued dashboard: %v", err)
	}
	if got := queued.Sessions[0].HandoffState; got != "pending" {
		t.Fatalf("queued dashboard handoff state = %q, want pending", got)
	}
	if got := queued.Sessions[0].HandoffPlatform; got != "feishu" {
		t.Fatalf("queued dashboard handoff platform = %q, want feishu", got)
	}

	h.store.Set("device", Snapshot{
		TakenAt:      time.Now().UTC(),
		Capabilities: Capabilities{LarkHandoff: true},
		Sessions: []SessionSummary{{
			ID: "sess_123", ProfileName: "default",
			HandoffState: "completed", HandoffPlatform: "feishu",
		}},
	}, time.Now().UTC())
	completedReq := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	completedReq.AddCookie(cookie)
	resp = doRequest(h, completedReq)
	var completed DashboardView
	if err := json.NewDecoder(resp.Body).Decode(&completed); err != nil {
		t.Fatalf("decode completed dashboard: %v", err)
	}
	if got := completed.Sessions[0].HandoffState; got != "completed" {
		t.Fatalf("Hermes dashboard handoff state = %q, want completed", got)
	}
}

func TestDashboardShowsFailedHandoffReasonFromRelayQueue(t *testing.T) {
	h := newHandoffTestHandler(t)
	cookie := validSessionCookieWithToken(t, h, "handoff-only-token")
	enqueue := newHandoffRequest(`{"idempotency_key":"550e8400-e29b-41d4-a716-446655440000"}`)
	enqueue.AddCookie(cookie)
	resp := doRequest(h, enqueue)
	var created map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode enqueue response: %v", err)
	}

	resultReq := httptest.NewRequest(http.MethodPost, "/api/v1/handoff/result", strings.NewReader(`{"command_id":"`+created["command_id"]+`","status":"failed","error":"handoff command failed: token=sk-CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"}`))
	resultReq.Header.Set("Content-Type", "application/json")
	resultReq.Header.Set("Authorization", "Bearer "+testToken)
	if resp := doRequest(h, resultReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("handoff result status = %d, want 200", resp.StatusCode)
	}

	dashboardReq := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	dashboardReq.AddCookie(cookie)
	resp = doRequest(h, dashboardReq)
	var dashboard DashboardView
	if err := json.NewDecoder(resp.Body).Decode(&dashboard); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if got := dashboard.Sessions[0].HandoffState; got != handoffCommandStateFailed {
		t.Fatalf("handoff state = %q, want failed", got)
	}
	reason := dashboard.Sessions[0].HandoffReason
	if reason == "" || strings.Contains(reason, "sk-") || !strings.Contains(reason, redactedPlaceholder) {
		t.Fatalf("handoff reason = %q, want redacted failure reason", reason)
	}
}

func newHandoffRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess_123/lark-handoff", strings.NewReader(body))
	req.Host = "relay.example.com"
	req.Header.Set("Origin", "https://relay.example.com")
	req.Header.Set("X-Hermes-Action", "lark-handoff")
	req.Header.Set("Content-Type", "application/json")
	return req
}
