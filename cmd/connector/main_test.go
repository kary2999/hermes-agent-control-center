package main

import (
	"testing"
	"time"
)

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("HERMES_DEVICE_ID", "mac-mini-1")
	t.Setenv("HERMES_RELAY_URL", "https://relay.example.com")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_POLL_INTERVAL", "45s")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.DeviceID != "mac-mini-1" {
		t.Errorf("DeviceID = %q, want %q", cfg.DeviceID, "mac-mini-1")
	}
	if cfg.RelayURL != "https://relay.example.com" {
		t.Errorf("RelayURL = %q, want %q", cfg.RelayURL, "https://relay.example.com")
	}
	if cfg.Token != "test-token" {
		t.Errorf("Token = %q, want %q", cfg.Token, "test-token")
	}
	if cfg.PollInterval != 45*time.Second {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 45*time.Second)
	}
}

func TestLoadConfigReadsHermesStateDBFromEnv(t *testing.T) {
	t.Setenv("HERMES_DEVICE_ID", "mac-mini-1")
	t.Setenv("HERMES_RELAY_URL", "https://relay.example.com")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_STATE_DB", "/tmp/hermes/state.db")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.HermesStateDBPath != "/tmp/hermes/state.db" {
		t.Errorf("HermesStateDBPath = %q, want %q", cfg.HermesStateDBPath, "/tmp/hermes/state.db")
	}
}

func TestLoadConfigHermesStateDBDefaultsToEmpty(t *testing.T) {
	t.Setenv("HERMES_DEVICE_ID", "mac-mini-1")
	t.Setenv("HERMES_RELAY_URL", "https://relay.example.com")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_STATE_DB", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.HermesStateDBPath != "" {
		t.Errorf("HermesStateDBPath = %q, want empty by default", cfg.HermesStateDBPath)
	}
}

func TestLoadConfigDefaultsPollInterval(t *testing.T) {
	t.Setenv("HERMES_DEVICE_ID", "mac-mini-1")
	t.Setenv("HERMES_RELAY_URL", "https://relay.example.com")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_POLL_INTERVAL", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.PollInterval != defaultPollInterval {
		t.Errorf("PollInterval = %v, want default %v", cfg.PollInterval, defaultPollInterval)
	}
}

func TestLoadConfigMissingDeviceIDFails(t *testing.T) {
	t.Setenv("HERMES_DEVICE_ID", "")
	t.Setenv("HERMES_RELAY_URL", "https://relay.example.com")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() with missing device id: expected error, got nil")
	}
}

func TestLoadConfigMissingTokenFails(t *testing.T) {
	t.Setenv("HERMES_DEVICE_ID", "mac-mini-1")
	t.Setenv("HERMES_RELAY_URL", "https://relay.example.com")
	t.Setenv("HERMES_RELAY_TOKEN", "")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() with missing token: expected error, got nil")
	}
}

func TestLoadConfigInvalidPollIntervalFails(t *testing.T) {
	t.Setenv("HERMES_DEVICE_ID", "mac-mini-1")
	t.Setenv("HERMES_RELAY_URL", "https://relay.example.com")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_POLL_INTERVAL", "not-a-duration")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() with invalid poll interval: expected error, got nil")
	}
}
