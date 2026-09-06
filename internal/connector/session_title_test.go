package connector

import (
	"testing"
	"time"
)

func TestDeriveSessionTitlePreservesExplicitTitle(t *testing.T) {
	got := deriveSessionTitle("我的会话标题", "some prompt", "cli", time.Unix(1756861323, 0))
	if got != "我的会话标题" {
		t.Fatalf("deriveSessionTitle() = %q, want explicit title preserved", got)
	}
}

func TestDeriveSessionTitlePreservesExplicitTitleWithSurroundingWhitespace(t *testing.T) {
	got := deriveSessionTitle("  已有标题  ", "", "cli", time.Unix(1756861323, 0))
	if got != "已有标题" {
		t.Fatalf("deriveSessionTitle() = %q, want trimmed explicit title", got)
	}
}

func TestDeriveSessionTitleFallsBackToFirstMeaningfulLineOfPrompt(t *testing.T) {
	got := deriveSessionTitle("", "\n\n  帮我看一下这个 bug \n第二行\n", "cli", time.Unix(1756861323, 0))
	if got != "帮我看一下这个 bug" {
		t.Fatalf("deriveSessionTitle() = %q, want first meaningful line", got)
	}
}

func TestDeriveSessionTitleTruncatesPromptFallbackTo40Runes(t *testing.T) {
	longLine := ""
	for range 60 {
		longLine += "界"
	}
	got := deriveSessionTitle("", longLine, "cli", time.Unix(1756861323, 0))
	runes := []rune(got)
	if len(runes) != 40 {
		t.Fatalf("len(deriveSessionTitle() runes) = %d, want 40", len(runes))
	}
}

func TestDeriveSessionTitleFallsBackToSourceAndStartedTimeWhenNoTitleOrPrompt(t *testing.T) {
	started := time.Date(2025, 9, 3, 10, 30, 0, 0, time.Local)
	got := deriveSessionTitle("", "", "cli", started)
	want := "CLI 会话 · " + started.Format("2006-01-02 15:04")
	if got != want {
		t.Fatalf("deriveSessionTitle() = %q, want %q", got, want)
	}
}

func TestDeriveSessionTitleFallsBackToSourceAndStartedTimeWhenPromptOnlyBlankLines(t *testing.T) {
	started := time.Date(2025, 9, 3, 10, 30, 0, 0, time.Local)
	got := deriveSessionTitle("", "\n\n   \n", "api", started)
	want := "API 会话 · " + started.Format("2006-01-02 15:04")
	if got != want {
		t.Fatalf("deriveSessionTitle() = %q, want %q", got, want)
	}
}

func TestDeriveSessionTitleNeverBlank(t *testing.T) {
	got := deriveSessionTitle("", "", "", time.Unix(0, 0))
	if got == "" {
		t.Fatal("deriveSessionTitle() = \"\", want a never-blank fallback title")
	}
}
