package relay

import (
	"strings"
	"testing"
)

func TestDailyReportHTMLSupportsHistoricalDates(t *testing.T) {
	t.Parallel()
	body := string(reportHTML)
	for _, required := range []string{
		"/api/v1/reports/daily",
		"URLSearchParams",
		"历史日报",
		"report-date",
		"今日结论",
		"异常",
		"下一步",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("report HTML missing %q", required)
		}
	}
}
