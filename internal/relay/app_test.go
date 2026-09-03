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
			cfg:     Config{ListenAddr: "127.0.0.1:8443", Token: "test-token", DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second},
			wantErr: false,
		},
		{
			name:    "missing listen addr",
			cfg:     Config{DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second},
			wantErr: true,
		},
		{
			name:    "missing data dir",
			cfg:     Config{ListenAddr: "127.0.0.1:8443", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second},
			wantErr: true,
		},
		{
			name:    "non positive read timeout",
			cfg:     Config{ListenAddr: "127.0.0.1:8443", DataDir: "/tmp/hermes-relay", ReadTimeout: 0, WriteTimeout: 30 * time.Second},
			wantErr: true,
		},
		{
			name:    "non positive write timeout",
			cfg:     Config{ListenAddr: "127.0.0.1:8443", DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 0},
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
	cfg := Config{ListenAddr: "127.0.0.1:8443", Token: "test-token", DataDir: "/tmp/hermes-relay", ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	_, err := New(cfg, nil)
	if err == nil {
		t.Fatal("New() with nil logger: expected error, got nil")
	}
}

func TestAppRunReturnsOnContextCancel(t *testing.T) {
	cfg := Config{ListenAddr: "127.0.0.1:0", Token: "test-token", DataDir: t.TempDir(), ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
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
