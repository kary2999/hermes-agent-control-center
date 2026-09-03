// Package relay implements the CentOS Relay server: it accepts snapshot
// reports POSTed by the Connector, keeps only the latest one in memory, and
// serves it to viewers via a JSON API and an embedded dashboard page.
package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrListenAddrRequired is returned when Config.ListenAddr is empty.
	ErrListenAddrRequired = errors.New("listen address is required")
	// ErrTokenRequired is returned when Config.Token is empty.
	ErrTokenRequired = errors.New("relay token is required")
	// ErrReadTimeoutInvalid is returned when Config.ReadTimeout is not positive.
	ErrReadTimeoutInvalid = errors.New("read timeout must be positive")
	// ErrWriteTimeoutInvalid is returned when Config.WriteTimeout is not positive.
	ErrWriteTimeoutInvalid = errors.New("write timeout must be positive")
	// ErrDataDirRequired is returned when Config.DataDir is empty.
	ErrDataDirRequired = errors.New("data dir is required")
	// ErrLoggerRequired is returned when New is called with a nil logger.
	ErrLoggerRequired = errors.New("logger is required")
)

// shutdownTimeout bounds how long Run waits for in-flight requests to
// finish once ctx is canceled.
const shutdownTimeout = 5 * time.Second

// Config holds the settings needed to run the Relay process.
type Config struct {
	// ListenAddr is the host:port the HTTP server binds to.
	ListenAddr string
	// Token is the shared bearer token required on every Connector and
	// dashboard API request. It must come from the environment; it is
	// never logged.
	Token string
	// DataDir is the directory the Relay uses for persistent storage.
	DataDir string
	// ReadTimeout bounds how long reading a request may take.
	ReadTimeout time.Duration
	// WriteTimeout bounds how long writing a response may take.
	WriteTimeout time.Duration
}

// Validate reports whether the Config is complete and internally consistent.
func (c Config) Validate() error {
	if strings.TrimSpace(c.ListenAddr) == "" {
		return ErrListenAddrRequired
	}
	if strings.TrimSpace(c.Token) == "" {
		return ErrTokenRequired
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return ErrDataDirRequired
	}
	if c.ReadTimeout <= 0 {
		return ErrReadTimeoutInvalid
	}
	if c.WriteTimeout <= 0 {
		return ErrWriteTimeoutInvalid
	}
	return nil
}

// App is the running Relay process.
type App struct {
	cfg     Config
	logger  *slog.Logger
	handler *Handler
}

// New validates cfg and constructs an App ready to Run.
func New(cfg Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		return nil, ErrLoggerRequired
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("relay: invalid config: %w", err)
	}
	store := NewSnapshotStore()
	return &App{cfg: cfg, logger: logger, handler: NewHandler(store, cfg.Token, logger)}, nil
}

// Run starts the Relay's HTTP server and blocks until ctx is canceled. It
// returns nil on a clean shutdown triggered by context cancellation.
func (a *App) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", a.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("relay: listen on %q: %w", a.cfg.ListenAddr, err)
	}

	srv := &http.Server{
		Handler:           a.handler.Routes(),
		ReadTimeout:       a.cfg.ReadTimeout,
		WriteTimeout:      a.cfg.WriteTimeout,
		ReadHeaderTimeout: 5 * time.Second,
	}

	a.logger.Info("relay starting", slog.String("listen_addr", ln.Addr().String()))

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("relay: graceful shutdown: %w", err)
		}
		a.logger.Info("relay shutting down", slog.String("reason", ctx.Err().Error()))
		return nil
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("relay: serve: %w", err)
		}
		return nil
	}
}
