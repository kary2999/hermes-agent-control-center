package relay

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	handoffCommandStateQueued    = "queued"
	handoffCommandStateClaimed   = "claimed"
	handoffCommandStateCompleted = "completed"
	handoffCommandStateFailed    = "failed"
	handoffPlatformFeishu        = "feishu"
	maxHandoffCommands           = 100
	handoffLeaseDuration         = 2 * time.Minute
)

type HandoffStore struct {
	mu     sync.Mutex
	path   string
	key    []byte
	now    func() time.Time
	rand   func([]byte) (int, error)
	cmds   []HandoffCommand
	loaded bool
}

type HandoffCommand struct {
	ID                 string     `json:"id"`
	IdempotencyKeyHash string     `json:"idempotency_key_hash"`
	SessionID          string     `json:"session_id"`
	Profile            string     `json:"profile"`
	Platform           string     `json:"platform"`
	State              string     `json:"state"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	LeaseExpiresAt     *time.Time `json:"lease_expires_at,omitempty"`
	ResultStatus       string     `json:"result_status,omitempty"`
}

type HandoffStoreCreateResult struct {
	Command HandoffCommand
	Reused  bool
}

func NewHandoffStore(dataDir string, token string) (*HandoffStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, ErrDataDirRequired
	}
	dir := filepath.Join(dataDir, "handoff")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create handoff store dir: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, fmt.Errorf("chmod handoff store dir: %w", err)
	}
	s := &HandoffStore{
		path: filepath.Join(dir, "commands.json"),
		key:  deriveSessionKey("handoff-idempotency:" + token),
		now:  time.Now,
		rand: rand.Read,
	}
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

var ErrHandoffStoreCapacity = errors.New("handoff command capacity reached")

func (s *HandoffStore) Create(sessionID, profile, idempotencyKey string, retryFailed bool) (HandoffStoreCreateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return HandoffStoreCreateResult{}, err
	}
	now := s.now().UTC()
	s.requeueExpiredLocked(now)
	keyHash := s.hash(idempotencyKey)
	for _, cmd := range s.cmds {
		if cmd.IdempotencyKeyHash == keyHash {
			return HandoffStoreCreateResult{Command: cmd, Reused: true}, nil
		}
		if cmd.SessionID == sessionID && (!retryFailed || !isTerminalHandoffState(cmd.State)) {
			return HandoffStoreCreateResult{Command: cmd, Reused: true}, nil
		}
	}
	if retryFailed {
		s.retireTerminalForSessionLocked(sessionID)
	}
	if len(s.cmds) >= maxHandoffCommands {
		if !s.pruneOldestTerminalLocked() {
			return HandoffStoreCreateResult{}, ErrHandoffStoreCapacity
		}
	}
	id, err := newCommandID(s.rand)
	if err != nil {
		return HandoffStoreCreateResult{}, err
	}
	cmd := HandoffCommand{
		ID:                 id,
		IdempotencyKeyHash: keyHash,
		SessionID:          sessionID,
		Profile:            truncateStringRunes(profile, 128),
		Platform:           handoffPlatformFeishu,
		State:              handoffCommandStateQueued,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	s.cmds = append(s.cmds, cmd)
	if err := s.saveLocked(); err != nil {
		return HandoffStoreCreateResult{}, err
	}
	return HandoffStoreCreateResult{Command: cmd}, nil
}

func truncateStringRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func (s *HandoffStore) Claim() (HandoffCommand, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return HandoffCommand{}, false, err
	}
	now := s.now().UTC()
	s.requeueExpiredLocked(now)
	for i := range s.cmds {
		if s.cmds[i].State == handoffCommandStateQueued {
			lease := now.Add(handoffLeaseDuration)
			s.cmds[i].State = handoffCommandStateClaimed
			s.cmds[i].UpdatedAt = now
			s.cmds[i].LeaseExpiresAt = &lease
			if err := s.saveLocked(); err != nil {
				return HandoffCommand{}, false, err
			}
			return s.cmds[i], true, nil
		}
	}
	if err := s.saveLocked(); err != nil {
		return HandoffCommand{}, false, err
	}
	return HandoffCommand{}, false, nil
}

func (s *HandoffStore) LatestForSession(sessionID string) (HandoffCommand, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return HandoffCommand{}, false, err
	}
	now := s.now().UTC()
	s.requeueExpiredLocked(now)
	var latest HandoffCommand
	found := false
	for _, cmd := range s.cmds {
		if cmd.SessionID != sessionID {
			continue
		}
		if !found || cmd.UpdatedAt.After(latest.UpdatedAt) {
			latest = cmd
			found = true
		}
	}
	return latest, found, nil
}

func (s *HandoffStore) Complete(id, state string) (HandoffCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return HandoffCommand{}, err
	}
	now := s.now().UTC()
	for i := range s.cmds {
		if s.cmds[i].ID == id {
			if state != handoffCommandStateCompleted && state != handoffCommandStateFailed {
				return HandoffCommand{}, errors.New("invalid command result state")
			}
			s.cmds[i].State = state
			s.cmds[i].UpdatedAt = now
			s.cmds[i].LeaseExpiresAt = nil
			s.cmds[i].ResultStatus = state
			if err := s.saveLocked(); err != nil {
				return HandoffCommand{}, err
			}
			return s.cmds[i], nil
		}
	}
	return HandoffCommand{}, os.ErrNotExist
}

func (s *HandoffStore) loadLocked() error {
	if s.loaded {
		return nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.cmds = []HandoffCommand{}
			s.loaded = true
			return nil
		}
		return fmt.Errorf("read handoff commands: %w", err)
	}
	if len(b) == 0 {
		s.cmds = []HandoffCommand{}
		s.loaded = true
		return nil
	}
	if err := json.Unmarshal(b, &s.cmds); err != nil {
		return fmt.Errorf("decode handoff commands: %w", err)
	}
	for len(s.cmds) > maxHandoffCommands && s.pruneOldestTerminalLocked() {
	}
	s.loaded = true
	return nil
}

func (s *HandoffStore) saveLocked() error {
	slices.SortFunc(s.cmds, func(a, b HandoffCommand) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	b, err := json.MarshalIndent(s.cmds, "", "  ")
	if err != nil {
		return fmt.Errorf("encode handoff commands: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return fmt.Errorf("write handoff commands temp: %w", err)
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		return fmt.Errorf("chmod handoff commands temp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace handoff commands: %w", err)
	}
	return nil
}

func (s *HandoffStore) hash(raw string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *HandoffStore) requeueExpiredLocked(now time.Time) {
	for i := range s.cmds {
		if s.cmds[i].State == handoffCommandStateClaimed && s.cmds[i].LeaseExpiresAt != nil && !now.Before(*s.cmds[i].LeaseExpiresAt) {
			s.cmds[i].State = handoffCommandStateQueued
			s.cmds[i].LeaseExpiresAt = nil
			s.cmds[i].UpdatedAt = now
		}
	}
}

func (s *HandoffStore) retireTerminalForSessionLocked(sessionID string) {
	filtered := s.cmds[:0]
	for _, cmd := range s.cmds {
		if cmd.SessionID == sessionID && isTerminalHandoffState(cmd.State) {
			continue
		}
		filtered = append(filtered, cmd)
	}
	s.cmds = filtered
}

func (s *HandoffStore) pruneOldestTerminalLocked() bool {
	slices.SortFunc(s.cmds, func(a, b HandoffCommand) int {
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	for i, cmd := range s.cmds {
		if isTerminalHandoffState(cmd.State) {
			s.cmds = append(s.cmds[:i], s.cmds[i+1:]...)
			return true
		}
	}
	return false
}

func isTerminalHandoffState(state string) bool {
	return state == handoffCommandStateCompleted || state == handoffCommandStateFailed
}

func newCommandID(randRead func([]byte) (int, error)) (string, error) {
	var b [16]byte
	if _, err := randRead(b[:]); err != nil {
		return "", fmt.Errorf("generate handoff command id: %w", err)
	}
	return "cmd_" + hex.EncodeToString(b[:]), nil
}
