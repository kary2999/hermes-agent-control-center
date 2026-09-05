// Command hermes-relay runs the CentOS Relay server process.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hermes-agent-control-center/internal/relay"
)

const (
	defaultReadTimeout  = 30 * time.Second
	defaultWriteTimeout = 30 * time.Second
	defaultDataDir      = "./data"
)

func loadConfig() (relay.Config, error) {
	cfg := relay.Config{
		ListenAddr:              os.Getenv("HERMES_RELAY_LISTEN_ADDR"),
		Token:                   os.Getenv("HERMES_RELAY_TOKEN"),
		DataDir:                 defaultDataDir,
		ReadTimeout:             defaultReadTimeout,
		WriteTimeout:            defaultWriteTimeout,
		UnauthorizedRedirectURL: os.Getenv("HERMES_UNAUTHORIZED_REDIRECT_URL"),
	}

	if raw := os.Getenv("HERMES_RELAY_DATA_DIR"); raw != "" {
		cfg.DataDir = raw
	}

	if raw := os.Getenv("HERMES_RELAY_READ_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return relay.Config{}, fmt.Errorf("parse HERMES_RELAY_READ_TIMEOUT: %w", err)
		}
		cfg.ReadTimeout = d
	}

	if raw := os.Getenv("HERMES_RELAY_WRITE_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return relay.Config{}, fmt.Errorf("parse HERMES_RELAY_WRITE_TIMEOUT: %w", err)
		}
		cfg.WriteTimeout = d
	}

	if err := cfg.Validate(); err != nil {
		return relay.Config{}, fmt.Errorf("load relay config: %w", err)
	}
	return cfg, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid relay configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}

	app, err := relay.New(cfg, logger)
	if err != nil {
		logger.Error("failed to initialize relay", slog.String("error", err.Error()))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx); err != nil {
		logger.Error("relay exited with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
