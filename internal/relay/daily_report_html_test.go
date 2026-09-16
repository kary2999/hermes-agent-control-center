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

func TestDailyReportHTML_RemovesAccessTokenFragment(t *testing.T) {
	t.Parallel()
	body := string(reportHTML)
	if !strings.Contains(body, "history.replaceState(null, '', window.location.pathname + window.location.search)") {
		t.Fatal("report HTML must remove URL fragments before loading report data")
	}
}
