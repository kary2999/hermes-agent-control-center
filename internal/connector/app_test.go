package connector

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{
			name:    "valid",
			cfg:     Config{DeviceID: "mac-mini-1", RelayURL: "https://relay.example.com", Token: "secret", PollInterval: 30 * time.Second},
			wantErr: false,
		},
		{
			name:    "missing device id",
			cfg:     Config{RelayURL: "https://relay.example.com", Token: "secret", PollInterval: 30 * time.Second},
			wantErr: true,
		},
		{
			name:    "missing relay url",
			cfg:     Config{DeviceID: "mac-mini-1", Token: "secret", PollInterval: 30 * time.Second},
			wantErr: true,
		},
		{
			name:    "relay url must use http or https scheme",
			cfg:     Config{DeviceID: "mac-mini-1", RelayURL: "wss://relay.example.com", Token: "secret", PollInterval: 30 * time.Second},
			wantErr: true,
		},
		{
			name:    "missing token",
			cfg:     Config{DeviceID: "mac-mini-1", RelayURL: "https://relay.example.com", PollInterval: 30 * time.Second},
			wantErr: true,
		},
		{
			name:    "non positive poll interval",
			cfg:     Config{DeviceID: "mac-mini-1", RelayURL: "https://relay.example.com", Token: "secret", PollInterval: 0},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	_, err := New(Config{}, testLogger())
	if err == nil {
		t.Fatal("New() with empty config: expected error, got nil")
	}
}

func TestNewRejectsNilLogger(t *testing.T) {
	cfg := Config{DeviceID: "mac-mini-1", RelayURL: "https://relay.example.com", Token: "secret", PollInterval: time.Second}
	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("New() with nil logger: expected error, got nil")
	}
}

func TestNewRejectsInvalidKanbanDBPath(t *testing.T) {
	cfg := Config{
		DeviceID:     "mac-mini-1",
		RelayURL:     "https://relay.example.com",
		Token:        "secret",
		PollInterval: time.Second,
		KanbanDBPath: "relative/kanban.db",
	}
	_, err := New(cfg, testLogger())
	if err == nil {
		t.Fatal("New() with relative kanban db path: expected error, got nil")
	}
}

func TestNewAcceptsExplicitKanbanDBPath(t *testing.T) {
	cfg := Config{
		DeviceID:     "mac-mini-1",
		RelayURL:     "https://relay.example.com",
		Token:        "secret",
		PollInterval: time.Second,
		KanbanDBPath: "/tmp/hermes/kanban.db",
	}
	app, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if app.snapshotURL != "https://relay.example.com/api/v1/snapshot" {
		t.Errorf("snapshotURL = %q, want %q", app.snapshotURL, "https://relay.example.com/api/v1/snapshot")
	}
}

func TestNewRejectsInvalidHermesStateDBPath(t *testing.T) {
	cfg := Config{
		DeviceID:          "mac-mini-1",
		RelayURL:          "https://relay.example.com",
		Token:             "secret",
		PollInterval:      time.Second,
		KanbanDBPath:      "/tmp/hermes/kanban.db",
		HermesStateDBPath: "relative/state.db",
	}
	_, err := New(cfg, testLogger())
	if err == nil {
		t.Fatal("New() with relative hermes state db path: expected error, got nil")
	}
}

func TestNewAcceptsEmptyHermesStateDBPath(t *testing.T) {
	cfg := Config{
		DeviceID:     "mac-mini-1",
		RelayURL:     "https://relay.example.com",
		Token:        "secret",
		PollInterval: time.Second,
		KanbanDBPath: "/tmp/hermes/kanban.db",
	}
	app, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sqliteCollector, ok := app.collector.(*SQLiteCollector)
	if !ok {
		t.Fatalf("app.collector = %T, want *SQLiteCollector", app.collector)
	}
	if sqliteCollector.StateDBPath != "" {
		t.Errorf("StateDBPath = %q, want empty (session collection disabled by default)", sqliteCollector.StateDBPath)
	}
}

func TestNewAcceptsExplicitHermesStateDBPath(t *testing.T) {
	cfg := Config{
		DeviceID:          "mac-mini-1",
		RelayURL:          "https://relay.example.com",
		Token:             "secret",
		PollInterval:      time.Second,
		KanbanDBPath:      "/tmp/hermes/kanban.db",
		HermesStateDBPath: "/tmp/hermes/state.db",
	}
	app, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sqliteCollector, ok := app.collector.(*SQLiteCollector)
	if !ok {
		t.Fatalf("app.collector = %T, want *SQLiteCollector", app.collector)
	}
	if sqliteCollector.StateDBPath != "/tmp/hermes/state.db" {
		t.Errorf("StateDBPath = %q, want %q", sqliteCollector.StateDBPath, "/tmp/hermes/state.db")
	}
}

type stubCollector struct {
	snap Snapshot
	err  error
}

func (s stubCollector) Snapshot(ctx context.Context) (Snapshot, error) {
	return s.snap, s.err
}

func TestAppRunPostsSnapshotAndReturnsOnContextCancel(t *testing.T) {
	requests := make(chan SnapshotPayload, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/snapshot" {
			t.Errorf("request path = %q, want %q", r.URL.Path, "/api/v1/snapshot")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer test-token")
		}
		var payload SnapshotPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		requests <- payload
	}))
	defer srv.Close()

	snap := Snapshot{TakenAt: time.Now().UTC(), Tasks: []AgentTask{{ID: "t1", Title: "one", Status: "running", CreatedAt: time.Now().UTC()}}}
	app := &App{
		cfg:         Config{DeviceID: "mac-mini-1", RelayURL: srv.URL, Token: "test-token", PollInterval: 20 * time.Millisecond},
		logger:      testLogger(),
		collector:   stubCollector{snap: snap},
		snapshotURL: srv.URL + "/api/v1/snapshot",
		httpClient:  srv.Client(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	select {
	case payload := <-requests:
		if payload.DeviceID != "mac-mini-1" {
			t.Errorf("payload.DeviceID = %q, want %q", payload.DeviceID, "mac-mini-1")
		}
		if len(payload.Snapshot.Tasks) != 1 {
			t.Errorf("payload.Snapshot.Tasks length = %d, want 1", len(payload.Snapshot.Tasks))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not report a snapshot in time")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil after clean shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}
}

func TestAppReportOnceLogsCollectorErrorWithoutPanicking(t *testing.T) {
	app := &App{
		cfg:         Config{DeviceID: "mac-mini-1", RelayURL: "https://relay.example.com", Token: "test-token", PollInterval: time.Second},
		logger:      testLogger(),
		collector:   stubCollector{err: context.DeadlineExceeded},
		snapshotURL: "https://relay.example.com/api/v1/snapshot",
		httpClient:  http.DefaultClient,
	}
	app.reportOnce(context.Background())
}
