package relay

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPostSnapshotThenGetDashboardExposesAllowedSessionHidesForbiddenFields
// is the end-to-end proof for the read-only session slice: an allowed
// session survives POST /api/v1/snapshot -> GET /api/v1/dashboard, while
// forbidden fields injected into the same POST body (user_id, chat_id,
// thread_id, session_key, origin_json, system_prompt, cwd, git_branch,
// billing_url, last_activity_description, cost_usd) never reach the
// authenticated dashboard response, because relay.SessionSummary has no
// field to receive them and SessionView is built explicitly field-by-field.
func TestPostSnapshotThenGetDashboardExposesAllowedSessionHidesForbiddenFields(t *testing.T) {
	h := newTestHandler(t)

	snapshotBody := `{
		"device_id": "mac-mini-1",
		"snapshot": {
			"taken_at": "2026-01-01T00:00:00Z",
			"tasks": [],
			"runs": [],
			"sessions": [{
				"id": "sess-allowed-1",
				"title": "Allowed Session Title",
				"source": "cli",
				"model": "gpt-5",
				"profile_name": "default",
				"started_at": "2026-01-01T00:00:00Z",
				"ended_at": "2026-01-01T00:10:00Z",
				"last_activity_at": "2026-01-01T00:09:00Z",
				"message_count": 4,
				"tool_call_count": 2,
				"input_tokens": 111,
				"output_tokens": 222,
				"cache_read_tokens": 10,
				"cache_write_tokens": 20,
				"reasoning_tokens": 5,
				"pinned": true,
				"archived": false,
				"user_id": "SECRET-USER-ID-MARKER",
				"chat_id": "SECRET-CHAT-ID-MARKER",
				"thread_id": "SECRET-THREAD-ID-MARKER",
				"session_key": "SECRET-SESSION-KEY-MARKER",
				"origin_json": "{\"leak\":\"SECRET-ORIGIN-MARKER\"}",
				"system_prompt": "SECRET-SYSTEM-PROMPT-MARKER",
				"cwd": "/Users/secret/SECRET-CWD-MARKER",
				"git_branch": "SECRET-BRANCH-MARKER",
				"billing_url": "https://billing.example.com/SECRET-BILLING-MARKER",
				"last_activity_description": "SECRET-ACTIVITY-DESCRIPTION-MARKER",
				"cost_usd": 987.65
			}]
		}
	}`

	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/snapshot", strings.NewReader(snapshotBody))
	postReq.Header.Set("Authorization", "Bearer "+testToken)
	postReq.Header.Set("Content-Type", "application/json")
	postResp := doRequest(h, postReq)
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/snapshot status = %d, want 200, body: %s", postResp.StatusCode, readBody(t, postResp))
	}

	cookie := validSessionCookie(t, h)
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	getReq.AddCookie(cookie)
	getResp := doRequest(h, getReq)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/dashboard status = %d, want 200", getResp.StatusCode)
	}
	body := readBody(t, getResp)

	allowedMarkers := []string{
		"sess-allowed-1", "Allowed Session Title", "\"source\":\"cli\"", "\"model\":\"gpt-5\"",
		"\"profile_name\":\"default\"", "\"message_count\":4", "\"tool_call_count\":2",
		"\"input_tokens\":111", "\"output_tokens\":222", "\"pinned\":true",
	}
	for _, marker := range allowedMarkers {
		if !strings.Contains(body, marker) {
			t.Errorf("dashboard response missing allowed session field %q:\n%s", marker, body)
		}
	}

	forbidden := []string{
		"SECRET-USER-ID-MARKER", "SECRET-CHAT-ID-MARKER", "SECRET-THREAD-ID-MARKER",
		"SECRET-SESSION-KEY-MARKER", "SECRET-ORIGIN-MARKER", "SECRET-SYSTEM-PROMPT-MARKER",
		"SECRET-CWD-MARKER", "SECRET-BRANCH-MARKER", "SECRET-BILLING-MARKER",
		"SECRET-ACTIVITY-DESCRIPTION-MARKER", "987.65",
		"user_id", "chat_id", "thread_id", "session_key", "origin_json", "system_prompt",
		"\"cwd\"", "git_branch", "billing_url", "last_activity_description", "cost_usd",
	}
	for _, word := range forbidden {
		if strings.Contains(body, word) {
			t.Errorf("dashboard response leaks forbidden session field/value %q:\n%s", word, body)
		}
	}
}

// TestGetDashboardSessionsEmptyArrayAfterSnapshotWithNoSessions proves the
// dashboard API's sessions field serializes as [] rather than null once a
// snapshot has been received, even when that snapshot carries no sessions
// (e.g. HermesStateDBPath was left unconfigured on the Connector).
func TestGetDashboardSessionsEmptyArrayAfterSnapshotWithNoSessions(t *testing.T) {
	h := newTestHandler(t)

	snapshotBody := `{"device_id":"mac-mini-1","snapshot":{"taken_at":"2026-01-01T00:00:00Z","tasks":[],"runs":[]}}`
	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/snapshot", strings.NewReader(snapshotBody))
	postReq.Header.Set("Authorization", "Bearer "+testToken)
	if resp := doRequest(h, postReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/v1/snapshot status = %d, want 200", resp.StatusCode)
	}

	cookie := validSessionCookie(t, h)
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	getReq.AddCookie(cookie)
	resp := doRequest(h, getReq)
	body := readBody(t, resp)

	if !strings.Contains(body, `"sessions":[]`) {
		t.Errorf("dashboard response sessions field is not an empty array literal:\n%s", body)
	}
}
