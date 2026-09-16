package relay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const reportDateLayout = "2006-01-02"

// DailyReport 是一份按日期固化的脱敏 Hermes 进化报告。
type DailyReport struct {
	Date               string        `json:"date"`
	GeneratedAt        time.Time     `json:"generated_at"`
	Conclusion         string        `json:"conclusion"`
	Anomaly            string        `json:"anomaly"`
	NextStep           string        `json:"next_step"`
	FailedTaskCount    int           `json:"failed_task_count"`
	RunningTaskCount   int           `json:"running_task_count"`
	CompletedTaskCount int           `json:"completed_task_count"`
	Dashboard          DashboardView `json:"dashboard"`
}

// DailyReportStore 把日报以单日单文件形式保存在 Relay 数据目录中。
type DailyReportStore struct {
	dir string
}

// NewDailyReportStore 创建日报存储目录。
func NewDailyReportStore(dataDir string) (*DailyReportStore, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("daily report data dir is empty")
	}
	dir := filepath.Join(dataDir, "reports")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create daily report dir: %w", err)
	}
	return &DailyReportStore{dir: dir}, nil
}

// BuildDailyReport 从一份脱敏看板快照生成确定性的日报结论。
func BuildDailyReport(date string, view DashboardView, now time.Time) (DailyReport, error) {
	if err := validateReportDate(date); err != nil {
		return DailyReport{}, err
	}
	failed := 0
	running := 0
	completed := 0
	for _, task := range view.AllTasks {
		switch {
		case isFailedTaskStatus(task.Status):
			failed++
		case task.IsRunning:
			running++
		case task.CompletedAt != nil || task.Status == "done" || task.Status == "completed":
			completed++
		}
	}

	conclusion := "Hermes 状态稳定，当前没有失败或执行中的任务。"
	anomaly := "未发现需要立即处理的异常。"
	nextStep := "继续沉淀资料库，并观察自动化任务的后续运行。"
	if failed > 0 {
		conclusion = fmt.Sprintf("今日发现 %d 个失败任务，需要优先处理。", failed)
		anomaly = fmt.Sprintf("存在 %d 个失败任务。", failed)
		nextStep = "先排查失败任务原因，确认恢复后再新增自动化。"
	} else if running > 0 {
		conclusion = fmt.Sprintf("系统正在执行 %d 个任务，整体链路正常。", running)
		anomaly = "未发现失败任务，执行中任务需要持续观察。"
		nextStep = "持续观察执行中任务，确认结果按预期落地。"
	}
	if !view.HasSnapshot {
		conclusion = "今日尚未收到 Connector 快照，无法形成完整进化结论。"
		anomaly = "日报生成时没有可用快照。"
		nextStep = "检查 Connector 与 Relay 的同步状态后重新生成日报。"
	}

	view.Sessions = sanitizeArchivedSessions(view.Sessions)
	return DailyReport{
		Date:               date,
		GeneratedAt:        now,
		Conclusion:         conclusion,
		Anomaly:            anomaly,
		NextStep:           nextStep,
		FailedTaskCount:    failed,
		RunningTaskCount:   running,
		CompletedTaskCount: completed,
		Dashboard:          view,
	}, nil
}

func sanitizeArchivedSessions(sessions []SessionView) []SessionView {
	const maxArchivedSessions = 20
	limit := len(sessions)
	if limit > maxArchivedSessions {
		limit = maxArchivedSessions
	}
	out := make([]SessionView, limit)
	copy(out, sessions[:limit])
	for i := range out {
		out[i].LastUserPrompt = ""
		out[i].LastUserPromptAt = nil
		out[i].HandoffReason = ""
	}
	return out
}

// Save 原子写入或覆盖同一天的日报。
func (s *DailyReportStore) Save(report DailyReport) error {
	if err := validateReportDate(report.Date); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal daily report: %w", err)
	}
	path := s.path(report.Date)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return fmt.Errorf("write daily report temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace daily report: %w", err)
	}
	return nil
}

// Load 读取指定日期的日报。
func (s *DailyReportStore) Load(date string) (DailyReport, error) {
	if err := validateReportDate(date); err != nil {
		return DailyReport{}, err
	}
	raw, err := os.ReadFile(s.path(date))
	if err != nil {
		return DailyReport{}, fmt.Errorf("read daily report: %w", err)
	}
	var report DailyReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return DailyReport{}, fmt.Errorf("decode daily report: %w", err)
	}
	return report, nil
}

// ListDates 按最新日期优先返回已有日报日期。
func (s *DailyReportStore) ListDates() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("list daily reports: %w", err)
	}
	dates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		date := entry.Name()[:len(entry.Name())-len(".json")]
		if validateReportDate(date) == nil {
			dates = append(dates, date)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	return dates, nil
}

func (s *DailyReportStore) path(date string) string {
	return filepath.Join(s.dir, date+".json")
}

func validateReportDate(date string) error {
	parsed, err := time.Parse(reportDateLayout, date)
	if err != nil || parsed.Format(reportDateLayout) != date {
		return fmt.Errorf("invalid daily report date %q", date)
	}
	return nil
}
