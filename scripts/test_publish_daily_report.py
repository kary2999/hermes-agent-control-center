import importlib.util
import pathlib
import unittest

MODULE_PATH = pathlib.Path(__file__).with_name("publish_daily_report.py")
spec = importlib.util.spec_from_file_location("publish_daily_report", MODULE_PATH)
if spec is None or spec.loader is None:
    raise RuntimeError("cannot load publish_daily_report module")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class PublishDailyReportTest(unittest.TestCase):
    def test_build_fields_uses_report_summary_and_historical_url(self):
        report = {
            "date": "2026-09-17",
            "conclusion": "今日稳定",
            "anomaly": "无异常",
            "next_step": "继续观察",
        }
        fields = module.build_fields(report, "https://hermes-report.ikarp.top")
        self.assertEqual(fields["今日结论"], "今日稳定")
        self.assertEqual(fields["异常"], "无异常")
        self.assertEqual(fields["下一步"], "继续观察")
        self.assertEqual(
            fields["报告链接"]["link"],
            "https://hermes-report.ikarp.top/reports/daily?date=2026-09-17",
        )
        self.assertIsInstance(fields["日期"], int)

    def test_find_existing_record_matches_date_field(self):
        target_ms = module.date_to_milliseconds("2026-09-17")
        records = [
            {"record_id": "rec-old", "fields": {"日期": target_ms - 86400000}},
            {"record_id": "rec-match", "fields": {"日期": target_ms}},
        ]
        self.assertEqual(module.find_existing_record(records, target_ms), "rec-match")

    def test_find_existing_record_handles_feishu_date_object(self):
        target_ms = module.date_to_milliseconds("2026-09-17")
        records = [{"record_id": "rec-match", "fields": {"日期": {"value": target_ms}}}]
        self.assertEqual(module.find_existing_record(records, target_ms), "rec-match")


if __name__ == "__main__":
    unittest.main()
