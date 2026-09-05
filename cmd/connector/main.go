// Command hermes-connector runs the Mac mini Connector process.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hermes-agent-control-center/internal/connector"
)

const defaultPollInterval = 10 * time.Second

func loadConfig() (connector.Config, error) {
	cfg := connector.Config{
		DeviceID:          os.Getenv("HERMES_DEVICE_ID"),
		RelayURL:          os.Getenv("HERMES_RELAY_URL"),
		Token:             os.Getenv("HERMES_RELAY_TOKEN"),
		KanbanDBPath:      os.Getenv("HERMES_KANBAN_DB"),
		HermesStateDBPath: os.Getenv("HERMES_STATE_DB"),
		PollInterval:      defaultPollInterval,
	}

	if raw := os.Getenv("HERMES_POLL_INTERVAL"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return connector.Config{}, fmt.Errorf("parse HERMES_POLL_INTERVAL: %w", err)
		}
		cfg.PollInterval = d
	}

	if err := cfg.Validate(); err != nil {
		return connector.Config{}, fmt.Errorf("load connector config: %w", err)
	}
	return cfg, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("invalid connector configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}

	app, err := connector.New(cfg, logger)
	if err != nil {
		logger.Error("failed to initialize connector", slog.String("error", err.Error()))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := app.Run(ctx); err != nil {
		logger.Error("connector exited with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
