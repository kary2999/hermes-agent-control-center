package main

import (
	"testing"
	"time"
)

func TestLoadConfigFromEnv(t *testing.T) {
	t.Setenv("HERMES_RELAY_LISTEN_ADDR", "127.0.0.1:8443")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_RELAY_READ_TIMEOUT", "20s")
	t.Setenv("HERMES_RELAY_WRITE_TIMEOUT", "25s")
	t.Setenv("HERMES_UNAUTHORIZED_REDIRECT_URL", "https://example.com")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8443" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, "127.0.0.1:8443")
	}
	if cfg.Token != "test-token" {
		t.Errorf("Token = %q, want %q", cfg.Token, "test-token")
	}
	if cfg.ReadTimeout != 20*time.Second {
		t.Errorf("ReadTimeout = %v, want %v", cfg.ReadTimeout, 20*time.Second)
	}
	if cfg.WriteTimeout != 25*time.Second {
		t.Errorf("WriteTimeout = %v, want %v", cfg.WriteTimeout, 25*time.Second)
	}
	if cfg.UnauthorizedRedirectURL != "https://example.com" {
		t.Errorf("UnauthorizedRedirectURL = %q, want %q", cfg.UnauthorizedRedirectURL, "https://example.com")
	}
}

func TestLoadConfigDefaultsTimeouts(t *testing.T) {
	t.Setenv("HERMES_RELAY_LISTEN_ADDR", "127.0.0.1:8443")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_RELAY_READ_TIMEOUT", "")
	t.Setenv("HERMES_RELAY_WRITE_TIMEOUT", "")
	t.Setenv("HERMES_UNAUTHORIZED_REDIRECT_URL", "https://example.com")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.ReadTimeout != defaultReadTimeout {
		t.Errorf("ReadTimeout = %v, want default %v", cfg.ReadTimeout, defaultReadTimeout)
	}
	if cfg.WriteTimeout != defaultWriteTimeout {
		t.Errorf("WriteTimeout = %v, want default %v", cfg.WriteTimeout, defaultWriteTimeout)
	}
}

func TestLoadConfigMissingListenAddrFails(t *testing.T) {
	t.Setenv("HERMES_RELAY_LISTEN_ADDR", "")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_UNAUTHORIZED_REDIRECT_URL", "https://example.com")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() with missing listen addr: expected error, got nil")
	}
}

func TestLoadConfigMissingTokenFails(t *testing.T) {
	t.Setenv("HERMES_RELAY_LISTEN_ADDR", "127.0.0.1:8443")
	t.Setenv("HERMES_RELAY_TOKEN", "")
	t.Setenv("HERMES_UNAUTHORIZED_REDIRECT_URL", "https://example.com")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() with missing token: expected error, got nil")
	}
}

func TestLoadConfigInvalidReadTimeoutFails(t *testing.T) {
	t.Setenv("HERMES_RELAY_LISTEN_ADDR", "127.0.0.1:8443")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_RELAY_READ_TIMEOUT", "not-a-duration")
	t.Setenv("HERMES_UNAUTHORIZED_REDIRECT_URL", "https://example.com")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() with invalid read timeout: expected error, got nil")
	}
}

func TestLoadConfigMissingRedirectURLFails(t *testing.T) {
	t.Setenv("HERMES_RELAY_LISTEN_ADDR", "127.0.0.1:8443")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_UNAUTHORIZED_REDIRECT_URL", "")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() with missing redirect url: expected error, got nil")
	}
}

func TestLoadConfigInvalidRedirectURLSchemeFails(t *testing.T) {
	t.Setenv("HERMES_RELAY_LISTEN_ADDR", "127.0.0.1:8443")
	t.Setenv("HERMES_RELAY_TOKEN", "test-token")
	t.Setenv("HERMES_UNAUTHORIZED_REDIRECT_URL", "javascript:alert(1)")

	if _, err := loadConfig(); err == nil {
		t.Fatal("loadConfig() with disallowed redirect url scheme: expected error, got nil")
	}
}
