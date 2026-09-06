package connector

import (
	"strings"
	"time"
)

// titleFallbackMaxRunes 是"首个有意义的提示词行"回退标题的 rune 上限。
const titleFallbackMaxRunes = 40

// sessionTitleTimeFormat 是标题回退到"来源 + 开始时间"时使用的本地时间格式。
const sessionTitleTimeFormat = "2006-01-02 15:04"

// deriveSessionTitle 计算一条会话的显示标题：
//  1. 已有的显式标题（去除首尾空白后非空）原样保留；
//  2. 否则取脱敏后提示词预览的第一个非空行，按 rune 截断到 40；
//  3. 否则用大写来源 + " 会话 · " + 本地格式化开始时间兜底，保证永不为空。
func deriveSessionTitle(title, sanitizedPrompt, source string, startedAt time.Time) string {
	if trimmed := strings.TrimSpace(title); trimmed != "" {
		return trimmed
	}
	if line := firstMeaningfulLine(sanitizedPrompt); line != "" {
		return truncateRunes(line, titleFallbackMaxRunes)
	}
	return strings.ToUpper(source) + " 会话 · " + startedAt.Format(sessionTitleTimeFormat)
}

// firstMeaningfulLine 返回文本中第一个去除首尾空白后非空的行。
func firstMeaningfulLine(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
