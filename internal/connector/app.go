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
	ErrLoggerRequired = errors.New("logger is required")
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
	httpClient  *http.Client
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
		httpClient:  &http.Client{},
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
}

func (a *App) postSnapshot(ctx context.Context, snap Snapshot) error {
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
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("relay rejected snapshot: status %d", resp.StatusCode)
	}
	return nil
}
