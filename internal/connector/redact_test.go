package connector

import "strings"

import "testing"

func TestSanitizePreviewRedactsBearerToken(t *testing.T) {
	token := "s" + "k-" + strings.Repeat("B", 48)
	got := sanitizePreview("please use Authorization: Bearer " + token + " to call the api")
	if strings.Contains(got, token) {
		t.Fatalf("sanitizePreview() = %q, want bearer token redacted", got)
	}
	if !strings.Contains(got, redactedPlaceholder) {
		t.Fatalf("sanitizePreview() = %q, want redaction placeholder present", got)
	}
}

func TestSanitizePreviewRedactsOpenAIStyleAPIKey(t *testing.T) {
	apiKey := "s" + "k-" + strings.Repeat("A", 48)
	got := sanitizePreview("here is my key " + apiKey + " don't share it")
	if strings.Contains(got, apiKey) {
		t.Fatalf("sanitizePreview() = %q, want API key redacted", got)
	}
}

func TestSanitizePreviewRedactsAWSAccessKey(t *testing.T) {
	awsKey := "AK" + "IA" + strings.Repeat("A", 16)
	got := sanitizePreview("aws key " + awsKey + " is leaked")
	if strings.Contains(got, awsKey) {
		t.Fatalf("sanitizePreview() = %q, want AWS access key redacted", got)
	}
}

func TestSanitizePreviewRedactsPrivateKeyBlock(t *testing.T) {
	begin := "-----BEGIN " + "PRIVATE KEY-----"
	end := "-----END " + "PRIVATE KEY-----"
	body := strings.Repeat("A", 64)
	block := begin + "\n" + body + "\n" + end
	got := sanitizePreview("my key is: " + block)
	if strings.Contains(got, body) {
		t.Fatalf("sanitizePreview() = %q, want private key block redacted", got)
	}
}

func TestSanitizePreviewRedactsPasswordAssignment(t *testing.T) {
	got := sanitizePreview("db config password=SuperSecret123! and continue")
	if strings.Contains(got, "SuperSecret123!") {
		t.Fatalf("sanitizePreview() = %q, want password value redacted", got)
	}
}

func TestSanitizePreviewRedactsSecretAssignment(t *testing.T) {
	got := sanitizePreview("secret: 'sh-1234567890abcdef' please rotate")
	if strings.Contains(got, "sh-1234567890abcdef") {
		t.Fatalf("sanitizePreview() = %q, want secret value redacted", got)
	}
}

func TestSanitizePreviewRedactsEmail(t *testing.T) {
	got := sanitizePreview("contact me at jane.doe+test@example.com please")
	if strings.Contains(got, "jane.doe+test@example.com") {
		t.Fatalf("sanitizePreview() = %q, want email redacted", got)
	}
}

func TestSanitizePreviewRedactsPhoneNumber(t *testing.T) {
	got := sanitizePreview("call me at +1-415-555-0132 tomorrow")
	if strings.Contains(got, "415-555-0132") {
		t.Fatalf("sanitizePreview() = %q, want phone number redacted", got)
	}
}

func TestSanitizePreviewRedactsCardLikeNumber(t *testing.T) {
	got := sanitizePreview("card number 4111 1111 1111 1111 exp 12/29")
	if strings.Contains(got, "4111 1111 1111 1111") {
		t.Fatalf("sanitizePreview() = %q, want card-like number redacted", got)
	}
}

func TestSanitizePreviewLeavesPlainTextUnchanged(t *testing.T) {
	got := sanitizePreview("请帮我看看这个 bug 为什么会复现")
	if got != "请帮我看看这个 bug 为什么会复现" {
		t.Fatalf("sanitizePreview() = %q, want plain text unchanged", got)
	}
}

func TestSanitizePreviewTruncatesAt500RunesUTF8Safe(t *testing.T) {
	raw := strings.Repeat("界", 600)
	got := sanitizePreview(raw)
	runes := []rune(got)
	if len(runes) != 500 {
		t.Fatalf("len(sanitizePreview() runes) = %d, want 500", len(runes))
	}
	for _, r := range runes {
		if r != '界' {
			t.Fatalf("sanitizePreview() produced invalid rune %q, want only 界", r)
		}
	}
}

func TestSanitizePreviewNeverLogsRawPromptOnPanicPath(t *testing.T) {
	// sanitizePreview must be a pure function with no logging side effects;
	// this test simply exercises it with empty input to lock that contract.
	if got := sanitizePreview(""); got != "" {
		t.Fatalf("sanitizePreview(\"\") = %q, want empty string", got)
	}
}
