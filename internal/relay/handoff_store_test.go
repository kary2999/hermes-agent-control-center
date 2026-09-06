package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHandoffStorePersistsHashOnlyAndReclaimsLease(t *testing.T) {
	dir := t.TempDir()
	store, err := NewHandoffStore(dir, testToken)
	if err != nil {
		t.Fatalf("NewHandoffStore() error = %v", err)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	created, err := store.Create("sess_1", "default", "550e8400-e29b-41d4-a716-446655440000", false)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	again, err := store.Create("sess_1", "default", "550e8400-e29b-41d4-a716-446655440001", false)
	if err != nil {
		t.Fatalf("Create duplicate session error = %v", err)
	}
	if !again.Reused || again.Command.ID != created.Command.ID {
		t.Fatalf("duplicate session not reused: %+v then %+v", created, again)
	}
	path := filepath.Join(dir, "handoff", "commands.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat commands file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("commands file mode = %v, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read commands file: %v", err)
	}
	if string(raw) == "" || contains(raw, []byte("550e8400-e29b-41d4-a716-446655440000")) {
		t.Fatalf("commands file persisted raw idempotency key: %s", raw)
	}

	restarted, err := NewHandoffStore(dir, testToken)
	if err != nil {
		t.Fatalf("restart NewHandoffStore() error = %v", err)
	}
	restarted.now = func() time.Time { return now }
	claimed, ok, err := restarted.Claim()
	if err != nil || !ok {
		t.Fatalf("Claim() = %+v, %v, %v; want command", claimed, ok, err)
	}
	restarted.now = func() time.Time { return now.Add(3 * time.Minute) }
	reclaimed, ok, err := restarted.Claim()
	if err != nil || !ok || reclaimed.ID != claimed.ID {
		t.Fatalf("expired lease was not reclaimed: %+v %v %v", reclaimed, ok, err)
	}
}

func TestHandoffStoreBoundsCommandsPrunesOnlyTerminal(t *testing.T) {
	store, err := NewHandoffStore(t.TempDir(), testToken)
	if err != nil {
		t.Fatalf("NewHandoffStore() error = %v", err)
	}
	for i := 0; i < maxHandoffCommands; i++ {
		store.now = func() time.Time { return time.Unix(int64(i), 0).UTC() }
		key := fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", i)
		created, err := store.Create(fmt.Sprintf("sess_%03d", i), "", key, false)
		if err != nil {
			t.Fatalf("Create(%d) error = %v", i, err)
		}
		if i < 5 {
			if _, err := store.Complete(created.Command.ID, handoffCommandStateCompleted, ""); err != nil {
				t.Fatalf("Complete(%d) error = %v", i, err)
			}
		}
	}
	for i := maxHandoffCommands; i < maxHandoffCommands+5; i++ {
		store.now = func() time.Time { return time.Unix(int64(i), 0).UTC() }
		key := fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", i)
		if _, err := store.Create(fmt.Sprintf("sess_%03d", i), "", key, false); err != nil {
			t.Fatalf("Create(%d) error = %v", i, err)
		}
	}
	var cmds []HandoffCommand
	raw, _ := os.ReadFile(store.path)
	if err := json.Unmarshal(raw, &cmds); err != nil {
		t.Fatalf("decode stored commands: %v", err)
	}
	if len(cmds) != maxHandoffCommands {
		t.Fatalf("stored commands = %d, want %d", len(cmds), maxHandoffCommands)
	}
	for _, cmd := range cmds {
		if cmd.State != handoffCommandStateQueued {
			t.Fatalf("terminal command was retained after capacity prune: %+v", cmd)
		}
	}
}

func TestHandoffStorePreservesQueuedCommandsAtCapacity(t *testing.T) {
	store, err := NewHandoffStore(t.TempDir(), testToken)
	if err != nil {
		t.Fatalf("NewHandoffStore() error = %v", err)
	}
	for i := 0; i < maxHandoffCommands; i++ {
		store.now = func() time.Time { return time.Unix(int64(i), 0).UTC() }
		key := fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", i)
		if _, err := store.Create(fmt.Sprintf("sess_%03d", i), "", key, false); err != nil {
			t.Fatalf("Create(%d) error = %v", i, err)
		}
	}
	_, err = store.Create("sess_overflow", "", "550e8400-e29b-41d4-a716-999999999999", false)
	if !errors.Is(err, ErrHandoffStoreCapacity) {
		t.Fatalf("overflow error = %v, want ErrHandoffStoreCapacity", err)
	}
	var cmds []HandoffCommand
	raw, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatalf("read commands file: %v", err)
	}
	if err := json.Unmarshal(raw, &cmds); err != nil {
		t.Fatalf("decode stored commands: %v", err)
	}
	if len(cmds) != maxHandoffCommands {
		t.Fatalf("stored commands = %d, want %d", len(cmds), maxHandoffCommands)
	}
	for i, cmd := range cmds {
		wantSession := fmt.Sprintf("sess_%03d", i)
		if cmd.SessionID != wantSession || cmd.State != handoffCommandStateQueued {
			t.Fatalf("cmd[%d] = %+v, want preserved queued %s", i, cmd, wantSession)
		}
	}
}

func TestHandoffStoreCommandIDRandomFailureDoesNotPersist(t *testing.T) {
	store, err := NewHandoffStore(t.TempDir(), testToken)
	if err != nil {
		t.Fatalf("NewHandoffStore() error = %v", err)
	}
	randErr := errors.New("rand failed")
	store.rand = func([]byte) (int, error) { return 0, randErr }
	_, err = store.Create("sess_1", "", "550e8400-e29b-41d4-a716-446655440000", false)
	if !errors.Is(err, randErr) {
		t.Fatalf("Create() error = %v, want randErr", err)
	}
	if _, err := os.Stat(store.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("commands file stat error = %v, want not exist", err)
	}
}

func contains(b, sub []byte) bool {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return true
		}
	}
	return false
}
