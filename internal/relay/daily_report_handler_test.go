package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDailyReportAPIArchivesListsAndReadsOneDate(t *testing.T) {
	t.Parallel()
	store := NewSnapshotStore()
	now := time.Date(2026, 9, 17, 0, 5, 0, 0, time.UTC)
	store.Set("mac-mini-report", Snapshot{
		TakenAt: now,
		Tasks:   []AgentTask{{ID: "task-1", Title: "完成日报", Status: "done", CompletedAt: &now}},
	}, now)
	h, err := NewHandler(store, testToken, testDashboardToken, "", testRedirectURL, testLogger(), t.TempDir(), "/reports/daily")
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	h.now = func() time.Time { return now }

	post := httptest.NewRequest(http.MethodPost, "/api/v1/reports/daily", strings.NewReader(`{"date":"2026-09-17"}`))
	post.Header.Set("Authorization", "Bearer "+testToken)
	postResp := doRequest(h, post)
	if postResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201; body=%s", postResp.StatusCode, readBody(t, postResp))
	}

	list := httptest.NewRequest(http.MethodGet, "/api/v1/reports/daily", nil)
	list.Header.Set("Authorization", "Bearer "+testToken)
	listResp := doRequest(h, list)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listResp.StatusCode)
	}
	var index struct {
		Dates []string `json:"dates"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&index); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(index.Dates) != 1 || index.Dates[0] != "2026-09-17" {
		t.Fatalf("dates = %#v, want [2026-09-17]", index.Dates)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/reports/daily/2026-09-17", nil)
	get.Header.Set("Authorization", "Bearer "+testToken)
	getResp := doRequest(h, get)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getResp.StatusCode)
	}
	var report DailyReport
	if err := json.NewDecoder(getResp.Body).Decode(&report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Date != "2026-09-17" || report.Conclusion == "" {
		t.Fatalf("report = %+v", report)
	}
}

func TestDailyReportArchiveRequiresRelayWriteToken(t *testing.T) {
	t.Parallel()
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, "", testRedirectURL, testLogger(), t.TempDir())
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reports/daily", strings.NewReader(`{"date":"2026-09-17"}`))
	req.Header.Set("Authorization", "Bearer "+testDashboardToken)
	resp := doRequest(h, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestDailyReportAPIRejectsInvalidDate(t *testing.T) {
	t.Parallel()
	h, err := NewHandler(NewSnapshotStore(), testToken, testDashboardToken, "", testRedirectURL, testLogger(), t.TempDir())
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/reports/daily", strings.NewReader(`{"date":"../../secret"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp := doRequest(h, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
