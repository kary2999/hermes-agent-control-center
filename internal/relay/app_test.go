package relay

import (
	"context"
	"io"
	"log/slog"
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
			cfg:     Config{ListenAddr: "127.0.0.1:8443", Token: "test-token", DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, UnauthorizedRedirectURL: "https://example.com"},
			wantErr: false,
		},
		{
			name:    "missing listen addr",
			cfg:     Config{DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, UnauthorizedRedirectURL: "https://example.com"},
			wantErr: true,
		},
		{
			name:    "missing data dir",
			cfg:     Config{ListenAddr: "127.0.0.1:8443", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, UnauthorizedRedirectURL: "https://example.com"},
			wantErr: true,
		},
		{
			name:    "non positive read timeout",
			cfg:     Config{ListenAddr: "127.0.0.1:8443", DataDir: "/tmp/hermes-relay", ReadTimeout: 0, WriteTimeout: 30 * time.Second, UnauthorizedRedirectURL: "https://example.com"},
			wantErr: true,
		},
		{
			name:    "non positive write timeout",
			cfg:     Config{ListenAddr: "127.0.0.1:8443", DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 0, UnauthorizedRedirectURL: "https://example.com"},
			wantErr: true,
		},
		{
			name:    "missing unauthorized redirect url",
			cfg:     Config{ListenAddr: "127.0.0.1:8443", Token: "test-token", DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second},
			wantErr: true,
		},
		{
			name:    "unauthorized redirect url with disallowed scheme",
			cfg:     Config{ListenAddr: "127.0.0.1:8443", Token: "test-token", DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, UnauthorizedRedirectURL: "ftp://example.com"},
			wantErr: true,
		},
		{
			name:    "unauthorized redirect url without host",
			cfg:     Config{ListenAddr: "127.0.0.1:8443", Token: "test-token", DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, UnauthorizedRedirectURL: "https:///path"},
			wantErr: true,
		},
		{
			name:    "unauthorized redirect url not a valid url",
			cfg:     Config{ListenAddr: "127.0.0.1:8443", Token: "test-token", DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, UnauthorizedRedirectURL: "://not-a-url"},
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

func TestConfigValidateListenAddrLoopbackOnly(t *testing.T) {
	baseCfg := func(listenAddr string) Config {
		return Config{
			ListenAddr:              listenAddr,
			Token:                   "test-token",
			DataDir:                 "/tmp/hermes-relay",
			ReadTimeout:             30 * time.Second,
			WriteTimeout:            30 * time.Second,
			UnauthorizedRedirectURL: "https://example.com",
		}
	}

	cases := []struct {
		name       string
		listenAddr string
		wantErr    bool
	}{
		{name: "loopback ipv4 with port", listenAddr: "127.0.0.1:8443", wantErr: false},
		{name: "loopback ipv4 range with port", listenAddr: "127.5.5.5:8443", wantErr: false},
		{name: "loopback ipv4 ephemeral port", listenAddr: "127.0.0.1:0", wantErr: false},
		{name: "loopback ipv6 with port", listenAddr: "[::1]:8443", wantErr: false},
		{name: "bare port binds all interfaces", listenAddr: ":8080", wantErr: true},
		{name: "explicit ipv4 any address", listenAddr: "0.0.0.0:8080", wantErr: true},
		{name: "explicit ipv6 any address", listenAddr: "[::]:8080", wantErr: true},
		{name: "public ipv4 address", listenAddr: "8.8.8.8:8080", wantErr: true},
		{name: "private non-loopback ipv4 address", listenAddr: "10.0.0.5:8080", wantErr: true},
		{name: "hostname instead of ip literal", listenAddr: "localhost:8080", wantErr: true},
		{name: "missing port", listenAddr: "127.0.0.1", wantErr: true},
		{name: "port out of range", listenAddr: "127.0.0.1:70000", wantErr: true},
		{name: "not an address at all", listenAddr: "not-an-address", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := baseCfg(tc.listenAddr).Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() with ListenAddr %q: error = %v, wantErr %v", tc.listenAddr, err, tc.wantErr)
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
	cfg := Config{ListenAddr: "127.0.0.1:8443", Token: "test-token", DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, UnauthorizedRedirectURL: "https://example.com"}
	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("New() with nil logger: expected error, got nil")
	}
}

func TestAppRunReturnsOnContextCancel(t *testing.T) {
	cfg := Config{ListenAddr: "127.0.0.1:0", Token: "test-token", DataDir: t.TempDir(), ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, UnauthorizedRedirectURL: "https://example.com"}
	app, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
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
