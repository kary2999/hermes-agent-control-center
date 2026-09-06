package connector

import "regexp"

// redactedPlaceholder 替换任何被识别为敏感信息的文本片段。
const redactedPlaceholder = "[已脱敏]"

// previewMaxRunes 是会话最新用户提示词预览的 UTF-8/rune 安全上限。
const previewMaxRunes = 500

// redactionPatterns 是按顺序应用的敏感信息正则：私钥块必须先于零散的
// base64 行匹配，避免整块私钥被拆成多段各自绕过其他规则。顺序其余部分
// 无严格依赖，但保持从"结构最明确"到"结构最宽泛"排列，减少误伤。
var redactionPatterns = []*regexp.Regexp{
	// PEM 私钥块（RSA/EC/OPENSSH/PGP 等），跨行匹配。
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
	// Bearer token（Authorization 头或裸写）。
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9\-_.=+/]{8,}`),
	// OpenAI 风格 API key: sk-... / sk-proj-... 等。
	regexp.MustCompile(`\bsk-[A-Za-z0-9\-_]{10,}\b`),
	// AWS access key ID。
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	// Slack token。
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	// Google API key。
	regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{20,}\b`),
	// GitHub token。
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
	// password=/secret=/token=/api_key= 等键值赋值，覆盖常见引号/冒号写法。
	regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|api[_-]?key|access[_-]?token|token)\b\s*[:=]\s*['"]?[^\s'",;]{4,}['"]?`),
	// 邮箱地址。
	regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`),
	// 银行卡号样式：13-19 位数字，允许空格或短横线分隔。
	regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`),
	// 电话号码：可选国际前缀 + 7 位以上数字，允许空格/短横线/括号分隔。
	regexp.MustCompile(`\+?\d{1,3}?[\s.-]?\(?\d{2,4}\)?[\s.-]\d{3,4}[\s.-]\d{3,4}\b`),
}

// sanitizePreview 对一段可能来自用户消息正文的原始文本做脱敏与截断，
// 产出安全的、有界的预览文本。它是纯函数：不做任何 I/O 或日志输出，
// 调用方必须保证原始文本本身也绝不被记录到日志。
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
