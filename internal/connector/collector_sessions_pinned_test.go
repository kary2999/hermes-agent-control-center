package connector

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestSQLiteCollectorAlwaysIncludesAllPinnedSessionsAheadOfUnpinnedCap is a
// regression test for a production bug: the real Hermes state.db has 7
// pinned sessions, and the old `ORDER BY ... LIMIT 200` query (applied
// uniformly to all sessions) could silently drop every pinned session once
// more than 200 unpinned sessions existed. Pinned sessions must never count
// against the 200 recent-unpinned cap.
func TestSQLiteCollectorAlwaysIncludesAllPinnedSessionsAheadOfUnpinnedCap(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		base := int64(1700000000)
		for i := range 7 {
			insertSentinelPinnedSession(t, db, pinnedSessionIDFor(i), float64(base-int64(i)))
		}
		for i := range 205 {
			insertSentinelUnpinnedSession(t, db, sessionIDFor(i), float64(base+int64(i)), nil, nil, 0)
		}
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	pinnedCount := 0
	unpinnedCount := 0
	for _, s := range snap.Sessions {
		if s.Pinned {
			pinnedCount++
		} else {
			unpinnedCount++
		}
	}
	if pinnedCount != 7 {
		t.Fatalf("pinnedCount = %d, want 7 (all pinned sessions must always be included)", pinnedCount)
	}
	if unpinnedCount != 200 {
		t.Fatalf("unpinnedCount = %d, want 200 (capped)", unpinnedCount)
	}
	if len(snap.Sessions) != 207 {
		t.Fatalf("len(Sessions) = %d, want 207 (7 pinned + 200 capped unpinned)", len(snap.Sessions))
	}
}

func insertSentinelPinnedSession(t *testing.T, db *sql.DB, id string, startedAt float64) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO sessions (
			id, title, source, model, profile_name, started_at, ended_at, last_activity_at,
			message_count, tool_call_count, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, reasoning_tokens, pinned, archived, hidden,
			user_id, chat_id, thread_id, session_key, origin_json, system_prompt, cwd,
			git_branch, billing_url, last_activity_description, cost_usd
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		id, "置顶会话", "cli", "gpt-5", "default",
		startedAt, nil, nil,
		1, 0, 0, 0, 0, 0, 0, 1, 0, 0,
		"x", "x", "x", "x", "{}", "x", "x", "x", "x", "x", 0.0,
	)
}

func pinnedSessionIDFor(i int) string { return "pinned-" + sessionIDFor(i) }

// ---- last_user_prompt / last_user_prompt_at ----

const messagesSchema = `
	CREATE TABLE messages (
		id INTEGER PRIMARY KEY,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at REAL NOT NULL,
		active INTEGER NOT NULL DEFAULT 1
	);
`

func insertMessage(t *testing.T, db *sql.DB, sessionID, role, content string, createdAt float64, active int) {
	t.Helper()
	mustExec(t, db, `INSERT INTO messages (session_id, role, content, created_at, active) VALUES (?, ?, ?, ?, ?)`,
		sessionID, role, content, createdAt, active)
}

const messagesSchemaWithTimestamp = `
	CREATE TABLE messages (
		id INTEGER PRIMARY KEY,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		timestamp REAL NOT NULL,
		active INTEGER NOT NULL DEFAULT 1
	);
`

func insertMessageWithTimestamp(t *testing.T, db *sql.DB, sessionID, role, content string, timestamp float64, active int) {
	t.Helper()
	mustExec(t, db, `INSERT INTO messages (session_id, role, content, timestamp, active) VALUES (?, ?, ?, ?, ?)`,
		sessionID, role, content, timestamp, active)
}

func TestSQLiteCollectorPopulatesLastUserPromptFromTimestampMessagesSchema(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		mustExec(t, db, messagesSchemaWithTimestamp)
		insertSentinelSession(t, db, "sess-1", 1756861323.0, nil, nil, 0)
		insertMessageWithTimestamp(t, db, "sess-1", "user", "旧提示词", 1756861330.0, 1)
		insertMessageWithTimestamp(t, db, "sess-1", "user", "真实 timestamp 提示词", 1756861340.0, 1)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snap.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(snap.Sessions))
	}
	s := snap.Sessions[0]
	if s.LastUserPrompt != "真实 timestamp 提示词" {
		t.Fatalf("LastUserPrompt = %q, want %q", s.LastUserPrompt, "真实 timestamp 提示词")
	}
	if s.LastUserPromptAt == nil || s.LastUserPromptAt.Unix() != 1756861340 {
		t.Fatalf("LastUserPromptAt = %v, want unix 1756861340", s.LastUserPromptAt)
	}
}

func TestSQLiteCollectorPopulatesLastUserPromptFromLatestActiveUserMessage(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		mustExec(t, db, messagesSchema)
		insertSentinelSession(t, db, "sess-1", 1756861323.0, nil, nil, 0)
		insertMessage(t, db, "sess-1", "user", "第一条提示词", 1756861330.0, 1)
		insertMessage(t, db, "sess-1", "assistant", "assistant reply, must never appear", 1756861335.0, 1)
		insertMessage(t, db, "sess-1", "user", "最新提示词", 1756861340.0, 1)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snap.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(snap.Sessions))
	}
	s := snap.Sessions[0]
	if s.LastUserPrompt != "最新提示词" {
		t.Fatalf("LastUserPrompt = %q, want %q", s.LastUserPrompt, "最新提示词")
	}
	if s.LastUserPromptAt == nil || s.LastUserPromptAt.Unix() != 1756861340 {
		t.Fatalf("LastUserPromptAt = %v, want unix 1756861340", s.LastUserPromptAt)
	}
}

func TestSQLiteCollectorIgnoresInactiveUserMessagesForLastUserPrompt(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		mustExec(t, db, messagesSchema)
		insertSentinelSession(t, db, "sess-1", 1756861323.0, nil, nil, 0)
		insertMessage(t, db, "sess-1", "user", "活跃提示词", 1756861330.0, 1)
		insertMessage(t, db, "sess-1", "user", "非活跃更新提示词", 1756861340.0, 0)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if got := snap.Sessions[0].LastUserPrompt; got != "活跃提示词" {
		t.Fatalf("LastUserPrompt = %q, want %q (inactive message must be ignored)", got, "活跃提示词")
	}
}

func TestSQLiteCollectorSanitizesLastUserPromptPreview(t *testing.T) {
	apiKey := "s" + "k-" + strings.Repeat("C", 48)
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		mustExec(t, db, messagesSchema)
		insertSentinelSession(t, db, "sess-1", 1756861323.0, nil, nil, 0)
		insertMessage(t, db, "sess-1", "user", "my key is "+apiKey+" please rotate", 1756861330.0, 1)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if strings.Contains(snap.Sessions[0].LastUserPrompt, apiKey) {
		t.Fatalf("LastUserPrompt = %q, want API key redacted", snap.Sessions[0].LastUserPrompt)
	}
}

func TestSQLiteCollectorHandlesMissingMessagesTableSafely(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		insertSentinelSession(t, db, "sess-1", 1756861323.0, nil, nil, 0)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v, want nil even without a messages table", err)
	}
	if len(snap.Sessions) != 1 {
		t.Fatalf("len(Sessions) = %d, want 1", len(snap.Sessions))
	}
	if snap.Sessions[0].LastUserPrompt != "" || snap.Sessions[0].LastUserPromptAt != nil {
		t.Fatalf("session = %+v, want empty last_user_prompt/at when messages table is absent", snap.Sessions[0])
	}
}

func TestSQLiteCollectorHandlesMessagesTableMissingExpectedColumnsSafely(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		mustExec(t, db, `CREATE TABLE messages (id INTEGER PRIMARY KEY, unrelated_column TEXT)`)
		insertSentinelSession(t, db, "sess-1", 1756861323.0, nil, nil, 0)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v, want nil even with an incompatible messages schema", err)
	}
	if snap.Sessions[0].LastUserPrompt != "" || snap.Sessions[0].LastUserPromptAt != nil {
		t.Fatalf("session = %+v, want empty last_user_prompt/at when messages schema is incompatible", snap.Sessions[0])
	}
}

// ---- Title fallback wiring through the collector ----

func TestSQLiteCollectorDerivesTitleFromLatestUserPromptWhenTitleEmpty(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		mustExec(t, db, messagesSchema)
		insertSentinelSessionWithText(t, db, "sess-1", nil, "gpt-5", "default", 1756861323.0, nil, nil, 0)
		insertMessage(t, db, "sess-1", "user", "帮我修一下这个 bug", 1756861330.0, 1)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snap.Sessions[0].Title != "帮我修一下这个 bug" {
		t.Fatalf("Title = %q, want derived from latest user prompt", snap.Sessions[0].Title)
	}
}

func TestSQLiteCollectorDerivesTitleFromSourceAndStartedTimeWhenTitleAndPromptEmpty(t *testing.T) {
	kanbanPath := emptyKanbanFixture(t)
	statePath := createStateDBFixture(t, func(db *sql.DB) {
		mustExec(t, db, sessionSchemaWithSentinels)
		insertSentinelSessionWithText(t, db, "sess-1", nil, "gpt-5", "default", 1756861323.0, nil, nil, 0)
	})

	c, err := NewSQLiteCollectorWithStateDB(kanbanPath, statePath)
	if err != nil {
		t.Fatalf("NewSQLiteCollectorWithStateDB() error = %v", err)
	}
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if !strings.HasPrefix(snap.Sessions[0].Title, "CLI 会话 · ") {
		t.Fatalf("Title = %q, want fallback to source + started time", snap.Sessions[0].Title)
	}
}
