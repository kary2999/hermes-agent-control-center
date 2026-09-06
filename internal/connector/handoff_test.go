package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigValidateHandoffCommandPath(t *testing.T) {
	cfg := Config{DeviceID: "mac", RelayURL: "https://relay.example.com", Token: "token", PollInterval: time.Second, KanbanDBPath: "/tmp/kanban.db", HandoffCommand: "hermes"}
	if err := cfg.Validate(); !errors.Is(err, ErrHandoffCommandInvalid) {
		t.Fatalf("relative HandoffCommand error = %v, want ErrHandoffCommandInvalid", err)
	}
	path := filepath.Join(t.TempDir(), "hermes")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	cfg.HandoffCommand = path
	if err := cfg.Validate(); err != nil {
		t.Fatalf("absolute executable HandoffCommand Validate() error = %v", err)
	}
}

func TestProcessOneHandoffRunsExactCommandAndPostsResult(t *testing.T) {
	rt := &handoffRoundTripper{}
	runner := &recordingRunner{}
	app := &App{
		cfg:        Config{DeviceID: "mac", RelayURL: "https://relay.example.com", Token: "token", PollInterval: time.Second, HandoffCommand: "/bin/hermes"},
		logger:     testLogger(),
		claimURL:   "https://relay.example.com/api/v1/handoff/claim",
		resultURL:  "https://relay.example.com/api/v1/handoff/result",
		httpClient: &http.Client{Transport: rt},
		runner:     runner,
	}
	app.processOneHandoff(context.Background())
	if runner.executable != "/bin/hermes" || runner.sessionID != "sess_1" {
		t.Fatalf("runner = executable %q session %q, want /bin/hermes sess_1", runner.executable, runner.sessionID)
	}
	if !bytes.Contains(rt.resultBody, []byte(`"command_id":"cmd_1"`)) || !bytes.Contains(rt.resultBody, []byte(`"status":"completed"`)) {
		t.Fatalf("result body = %s, want sanitized completed result", rt.resultBody)
	}
}

func TestProcessOneHandoffReportsFailureReasonWhenCommandFails(t *testing.T) {
	rt := &handoffRoundTripper{}
	secret := "sk-" + strings.Repeat("C", 48)
	runner := &recordingRunner{err: errors.New("handoff failed: token=" + secret)}
	app := &App{
		cfg:        Config{DeviceID: "mac", RelayURL: "https://relay.example.com", Token: "token", PollInterval: time.Second, HandoffCommand: "/bin/hermes"},
		logger:     testLogger(),
		claimURL:   "https://relay.example.com/api/v1/handoff/claim",
		resultURL:  "https://relay.example.com/api/v1/handoff/result",
		httpClient: &http.Client{Transport: rt},
		runner:     runner,
	}
	app.processOneHandoff(context.Background())
	if !bytes.Contains(rt.resultBody, []byte(`"status":"failed"`)) {
		t.Fatalf("result body = %s, want failed status", rt.resultBody)
	}
	var decoded struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(rt.resultBody, &decoded); err != nil {
		t.Fatalf("decode result body: %v", err)
	}
	if decoded.Error == "" {
		t.Fatal("result body error field is empty, want the sanitized runner failure reason")
	}
	if strings.Contains(decoded.Error, secret) || !strings.Contains(decoded.Error, redactedPlaceholder) {
		t.Fatalf("result body error = %q, want it to redact the runner failure reason", decoded.Error)
	}
}

func TestExecHandoffRunnerUsesContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (ExecHandoffRunner{}).Run(ctx, "/bin/echo", "sess_1"); err == nil {
		t.Fatal("Run() error = nil, want context cancellation error")
	}
}

func TestExecHandoffRunnerUsesExactArgvWithoutShell(t *testing.T) {
	dir := t.TempDir()
	recordPath := filepath.Join(dir, "argv.txt")
	executable := filepath.Join(dir, "record-argv")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$0\" \"$@\" > %q\n", recordPath)
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	sessionID := "sess_safe-1"
	if err := (ExecHandoffRunner{}).Run(context.Background(), executable, sessionID); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read argv record: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{executable, "sessions", "handoff", sessionID, "--platform", "feishu"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %#v, want %#v", got, want)
	}
}

func TestExecHandoffRunnerReportsNonzeroExit(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "fail")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	if err := (ExecHandoffRunner{}).Run(context.Background(), executable, "sess_1"); err == nil {
		t.Fatal("Run() error = nil, want nonzero exit error")
	}
}

func TestReportOnceWithDisabledHandoffDoesNotClaim(t *testing.T) {
	rt := &handoffRoundTripper{}
	app := &App{
		cfg:         Config{DeviceID: "mac", RelayURL: "https://relay.example.com", Token: "token", PollInterval: time.Second},
		logger:      testLogger(),
		collector:   stubCollector{snap: Snapshot{TakenAt: time.Now().UTC()}},
		snapshotURL: "https://relay.example.com/api/v1/snapshot",
		claimURL:    "https://relay.example.com/api/v1/handoff/claim",
		httpClient:  &http.Client{Transport: rt},
		runner:      &recordingRunner{},
	}
	app.reportOnce(context.Background())
	if rt.claims != 0 {
		t.Fatalf("claim requests = %d, want 0 when HandoffCommand is disabled", rt.claims)
	}
}

func TestProcessOneHandoffRejectsHostileSessionIDBeforeExec(t *testing.T) {
	rt := &handoffRoundTripper{claimSessionID: "../bad"}
	runner := &recordingRunner{}
	app := &App{
		cfg:        Config{DeviceID: "mac", RelayURL: "https://relay.example.com", Token: "token", PollInterval: time.Second, HandoffCommand: "/bin/hermes"},
		logger:     testLogger(),
		claimURL:   "https://relay.example.com/api/v1/handoff/claim",
		resultURL:  "https://relay.example.com/api/v1/handoff/result",
		httpClient: &http.Client{Transport: rt},
		runner:     runner,
	}
	app.processOneHandoff(context.Background())
	if runner.called {
		t.Fatal("runner was called for hostile session id")
	}
	if !bytes.Contains(rt.resultBody, []byte(`"status":"failed"`)) {
		t.Fatalf("result body = %s, want failed report", rt.resultBody)
	}
}

func TestConnectorResponseBodyReadErrors(t *testing.T) {
	readErr := errors.New("read failed")
	t.Run("snapshot", func(t *testing.T) {
		app := &App{
			cfg:         Config{DeviceID: "mac", Token: "token", HandoffCommand: "/bin/hermes"},
			snapshotURL: "https://relay.example.com/api/v1/snapshot",
			httpClient:  &http.Client{Transport: bodyErrorRoundTripper{path: "/api/v1/snapshot", err: readErr}},
		}
		if err := app.postSnapshot(context.Background(), Snapshot{}); !errors.Is(err, readErr) {
			t.Fatalf("postSnapshot() error = %v, want readErr", err)
		}
	})
	t.Run("claim", func(t *testing.T) {
		app := &App{
			cfg:        Config{Token: "token"},
			claimURL:   "https://relay.example.com/api/v1/handoff/claim",
			httpClient: &http.Client{Transport: bodyErrorRoundTripper{path: "/api/v1/handoff/claim", status: http.StatusInternalServerError, err: readErr}},
		}
		_, _, err := app.claimHandoff(context.Background())
		if !errors.Is(err, readErr) {
			t.Fatalf("claimHandoff() error = %v, want readErr", err)
		}
	})
	t.Run("result", func(t *testing.T) {
		app := &App{
			cfg:        Config{Token: "token"},
			resultURL:  "https://relay.example.com/api/v1/handoff/result",
			httpClient: &http.Client{Transport: bodyErrorRoundTripper{path: "/api/v1/handoff/result", err: readErr}},
		}
		if err := app.postHandoffResult(context.Background(), "cmd_1", "completed", ""); !errors.Is(err, readErr) {
			t.Fatalf("postHandoffResult() error = %v, want readErr", err)
		}
	})
}

type recordingRunner struct {
	executable string
	sessionID  string
	called     bool
	err        error
}

func (r *recordingRunner) Run(ctx context.Context, executable, sessionID string) error {
	r.called = true
	r.executable = executable
	r.sessionID = sessionID
	return r.err
}

type handoffRoundTripper struct {
	resultBody     []byte
	claims         int
	claimSessionID string
}

func (h *handoffRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.URL.Path {
	case "/api/v1/handoff/claim":
		h.claims++
		sessionID := h.claimSessionID
		if sessionID == "" {
			sessionID = "sess_1"
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewBufferString(fmt.Sprintf(`{"command":{"command_id":"cmd_1","session_id":%q,"handoff_state":"claimed","handoff_platform":"feishu"}}`, sessionID))),
		}, nil
	case "/api/v1/snapshot":
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(`{}`))}, nil
	case "/api/v1/handoff/result":
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		h.resultBody = b
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(`{}`))}, nil
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString(`{}`))}, nil
	}
}

type bodyErrorRoundTripper struct {
	path   string
	status int
	err    error
}

func (b bodyErrorRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != b.path {
		return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
	}
	status := b.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: errReadCloser{err: b.err}}, nil
}

type errReadCloser struct {
	err error
}

func (e errReadCloser) Read([]byte) (int, error) { return 0, e.err }
func (e errReadCloser) Close() error             { return nil }
