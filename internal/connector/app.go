// Package connector implements the Mac mini Connector process: it collects
// sanitized Hermes agent state and periodically POSTs a compact JSON
// snapshot to the Relay server over HTTP.
package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrDeviceIDRequired is returned when Config.DeviceID is empty.
	ErrDeviceIDRequired = errors.New("device id is required")
	// ErrRelayURLRequired is returned when Config.RelayURL is empty.
	ErrRelayURLRequired = errors.New("relay url is required")
	// ErrRelayURLInvalid is returned when Config.RelayURL cannot be parsed
	// or does not use the http/https scheme.
	ErrRelayURLInvalid = errors.New("relay url must be a valid http:// or https:// url")
	// ErrTokenRequired is returned when Config.Token is empty.
	ErrTokenRequired = errors.New("relay token is required")
	// ErrPollIntervalInvalid is returned when Config.PollInterval is not a
	// positive duration.
	ErrPollIntervalInvalid = errors.New("poll interval must be positive")
	// ErrLoggerRequired is returned when New is called with a nil logger.
	ErrLoggerRequired        = errors.New("logger is required")
	ErrHandoffCommandInvalid = errors.New("handoff command must be an absolute executable path")
)

// Config holds the settings needed to run the Connector process.
type Config struct {
	// DeviceID uniquely identifies this Connector to the Relay server.
	DeviceID string
	// RelayURL is the http:// or https:// base URL of the Relay server,
	// e.g. "https://relay.example.com".
	RelayURL string
	// Token is the shared bearer token sent with every request to the
	// Relay server. It must come from the environment; it is never logged.
	Token string
	// KanbanDBPath optionally overrides the Hermes kanban.db path. When
	// empty it is resolved via ResolveKanbanDBPath.
	KanbanDBPath string
	// HermesStateDBPath 可选地指向本地 Hermes state.db，用于采集脱敏后的
	// 会话元数据。留空则禁用会话采集，保持既有的仅 Kanban 行为。
	HermesStateDBPath string
	// PollInterval is the cadence at which a fresh snapshot is collected
	// and reported to the Relay server.
	PollInterval time.Duration
	// RequestTimeout bounds each collect+report cycle. Defaults to
	// PollInterval when zero and PollInterval is set.
	RequestTimeout time.Duration
	HandoffCommand string
}

// Validate reports whether the Config is complete and internally consistent.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DeviceID) == "" {
		return ErrDeviceIDRequired
	}
	if strings.TrimSpace(c.RelayURL) == "" {
		return ErrRelayURLRequired
	}
	u, err := url.Parse(c.RelayURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRelayURLInvalid, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrRelayURLInvalid, u.Scheme)
	}
	if strings.TrimSpace(c.Token) == "" {
		return ErrTokenRequired
	}
	if c.PollInterval <= 0 {
		return ErrPollIntervalInvalid
	}
	if strings.TrimSpace(c.HandoffCommand) != "" {
		if !filepath.IsAbs(c.HandoffCommand) {
			return ErrHandoffCommandInvalid
		}
		info, err := os.Stat(c.HandoffCommand)
		if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			return ErrHandoffCommandInvalid
		}
	}
	return nil
}

func (c Config) requestTimeout() time.Duration {
	if c.RequestTimeout > 0 {
		return c.RequestTimeout
	}
	return c.PollInterval
}

// SnapshotPayload is the wire format POSTed to the Relay server: a sanitized
// Hermes snapshot tagged with the reporting device's ID.
type SnapshotPayload struct {
	DeviceID string   `json:"device_id"`
	Snapshot Snapshot `json:"snapshot"`
}

// App is the running Connector process.
type App struct {
	cfg         Config
	logger      *slog.Logger
	collector   Collector
	snapshotURL string
	claimURL    string
	resultURL   string
	httpClient  *http.Client
	runner      HandoffRunner
}

// New validates cfg, resolves the Hermes kanban.db path when needed, and
// constructs an App ready to Run.
func New(cfg Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		return nil, ErrLoggerRequired
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("connector: invalid config: %w", err)
	}

	dbPath := cfg.KanbanDBPath
	if strings.TrimSpace(dbPath) == "" {
		resolved, err := ResolveKanbanDBPath(HomeEnv{})
		if err != nil {
			return nil, fmt.Errorf("connector: resolve kanban db path: %w", err)
		}
		dbPath = resolved
	}
	collector, err := NewSQLiteCollectorWithStateDB(dbPath, cfg.HermesStateDBPath)
	if err != nil {
		return nil, fmt.Errorf("connector: invalid kanban or hermes state db path: %w", err)
	}

	return &App{
		cfg:         cfg,
		logger:      logger,
		collector:   collector,
		snapshotURL: strings.TrimRight(cfg.RelayURL, "/") + "/api/v1/snapshot",
		claimURL:    strings.TrimRight(cfg.RelayURL, "/") + "/api/v1/handoff/claim",
		resultURL:   strings.TrimRight(cfg.RelayURL, "/") + "/api/v1/handoff/result",
		httpClient:  &http.Client{},
		runner:      ExecHandoffRunner{},
	}, nil
}

// Run starts the Connector lifecycle: it reports a snapshot immediately,
// then again every PollInterval, until ctx is canceled. It returns nil on a
// clean shutdown triggered by context cancellation.
func (a *App) Run(ctx context.Context) error {
	a.logger.Info("connector starting",
		slog.String("device_id", a.cfg.DeviceID),
		slog.Duration("poll_interval", a.cfg.PollInterval),
	)

	a.reportOnce(ctx)

	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("connector shutting down", slog.String("reason", ctx.Err().Error()))
			return nil
		case <-ticker.C:
			a.reportOnce(ctx)
		}
	}
}

// reportOnce collects one snapshot and reports it to the Relay server,
// logging (but not returning) any failure so the poll loop keeps running.
func (a *App) reportOnce(ctx context.Context) {
	reqCtx, cancel := context.WithTimeout(ctx, a.cfg.requestTimeout())
	defer cancel()

	snap, err := a.collector.Snapshot(reqCtx)
	if err != nil {
		a.logger.Error("collect snapshot failed", slog.String("error", err.Error()))
		return
	}

	if err := a.postSnapshot(reqCtx, snap); err != nil {
		a.logger.Error("report snapshot failed", slog.String("error", err.Error()))
		return
	}

	a.logger.Info("snapshot reported",
		slog.Int("tasks", len(snap.Tasks)),
		slog.Int("runs", len(snap.Runs)),
	)
	if strings.TrimSpace(a.cfg.HandoffCommand) != "" {
		a.processOneHandoff(reqCtx)
	}
}

func (a *App) postSnapshot(ctx context.Context, snap Snapshot) error {
	snap.Capabilities.LarkHandoff = strings.TrimSpace(a.cfg.HandoffCommand) != ""
	body, err := json.Marshal(SnapshotPayload{DeviceID: a.cfg.DeviceID, Snapshot: snap})
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.snapshotURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build snapshot request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send snapshot request: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)); err != nil {
		return fmt.Errorf("read snapshot response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay rejected snapshot: status %d", resp.StatusCode)
	}
	return nil
}

type HandoffRunner interface {
	Run(ctx context.Context, executable, sessionID string) error
}

type ExecHandoffRunner struct{}

func (ExecHandoffRunner) Run(ctx context.Context, executable, sessionID string) error {
	cmd := exec.CommandContext(ctx, executable, "sessions", "handoff", sessionID, "--platform", "feishu")
	out, err := cmd.CombinedOutput()
	if len(out) > 4096 {
		out = out[:4096]
	}
	if err != nil {
		return fmt.Errorf("handoff command failed: %w", err)
	}
	return nil
}

type handoffClaimResponse struct {
	Command *handoffCommandWire `json:"command"`
}

type handoffCommandWire struct {
	CommandID       string `json:"command_id"`
	SessionID       string `json:"session_id"`
	HandoffState    string `json:"handoff_state"`
	HandoffPlatform string `json:"handoff_platform"`
}

func (a *App) processOneHandoff(ctx context.Context) {
	cmd, ok, err := a.claimHandoff(ctx)
	if err != nil {
		a.logger.Error("claim handoff failed", slog.String("error_kind", "claim"))
		return
	}
	if !ok {
		return
	}
	if !validSessionID(cmd.SessionID) {
		a.logger.Error("execute handoff rejected", slog.String("command_id", cmd.CommandID), slog.String("session_id", cmd.SessionID), slog.String("error_kind", "invalid_session_id"))
		_ = a.postHandoffResult(ctx, cmd.CommandID, "failed", "invalid session id")
		return
	}
	status := "completed"
	reason := ""
	if err := a.runner.Run(ctx, a.cfg.HandoffCommand, cmd.SessionID); err != nil {
		status = "failed"
		reason = truncateRunes(sanitizePreview(err.Error()), maxHandoffReasonRunes)
		a.logger.Error("execute handoff failed", slog.String("command_id", cmd.CommandID), slog.String("session_id", cmd.SessionID), slog.String("error_kind", "exec"))
	}
	if err := a.postHandoffResult(ctx, cmd.CommandID, status, reason); err != nil {
		a.logger.Error("report handoff result failed", slog.String("command_id", cmd.CommandID), slog.String("session_id", cmd.SessionID), slog.String("error_kind", "result"))
	}
}

func (a *App) claimHandoff(ctx context.Context) (handoffCommandWire, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.claimURL, nil)
	if err != nil {
		return handoffCommandWire{}, false, fmt.Errorf("build handoff claim request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return handoffCommandWire{}, false, fmt.Errorf("send handoff claim request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)); err != nil {
			return handoffCommandWire{}, false, fmt.Errorf("read handoff claim response: %w", err)
		}
		return handoffCommandWire{}, false, fmt.Errorf("relay rejected handoff claim: status %d", resp.StatusCode)
	}
	var payload handoffClaimResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload); err != nil {
		return handoffCommandWire{}, false, fmt.Errorf("decode handoff claim: %w", err)
	}
	if payload.Command == nil {
		return handoffCommandWire{}, false, nil
	}
	return *payload.Command, true, nil
}

// maxHandoffReasonRunes bounds the failure reason sent to the Relay,
// keeping the request small and matching the dashboard-facing preview
// convention used elsewhere in the Connector.
const maxHandoffReasonRunes = 200

func (a *App) postHandoffResult(ctx context.Context, commandID, status, reason string) error {
	payload := map[string]string{"command_id": commandID, "status": status}
	if reason != "" {
		payload["error"] = reason
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal handoff result: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.resultURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build handoff result request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send handoff result request: %w", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)); err != nil {
		return fmt.Errorf("read handoff result response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay rejected handoff result: status %d", resp.StatusCode)
	}
	return nil
}

var connectorSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func validSessionID(s string) bool { return connectorSessionIDPattern.MatchString(s) }
