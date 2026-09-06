package relay

import "regexp"

const redactedPlaceholder = "[已脱敏]"

var redactionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\-_.=+/]{8,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9\-_]{10,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{20,}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|api[_-]?key|access[_-]?token|token)\b\s*[:=]\s*['"]?[^\s'",;]{4,}['"]?`),
	regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`),
	regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`),
	regexp.MustCompile(`\+?\d{1,3}?[\s.-]?\(?\d{2,4}\)?[\s.-]\d{3,4}[\s.-]\d{3,4}\b`),
}

// sanitizePreview 对可能来自本地状态库或命令错误的文本做脱敏与截断。
func sanitizePreview(raw string) string {
	if raw == "" {
		return ""
	}
	redacted := raw
	for _, pattern := range redactionPatterns {
		redacted = pattern.ReplaceAllString(redacted, redactedPlaceholder)
	}
	return truncateRunes(redacted, previewMaxRunes)
}

const previewMaxRunes = 500

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
