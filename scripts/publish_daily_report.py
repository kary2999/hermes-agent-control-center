#!/usr/bin/env python3
"""Archive one Hermes daily report and upsert it into Feishu Bitable."""

import argparse
import datetime as dt
import json
import os
import pathlib
import sys
import urllib.error
import urllib.parse
import urllib.request
from zoneinfo import ZoneInfo

TIMEOUT_SECONDS = 30
JST = ZoneInfo("Asia/Tokyo")


def load_env(path):
    values = {}
    for raw in pathlib.Path(path).read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip().strip('"').strip("'")
    return values


def request_json(method, url, payload=None, token=None):
    body = None if payload is None else json.dumps(payload, ensure_ascii=False).encode("utf-8")
    headers = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json; charset=utf-8"
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
            raw = response.read().decode("utf-8")
            return response.status, json.loads(raw) if raw else {}
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", errors="replace")
        try:
            detail = json.loads(raw)
        except json.JSONDecodeError:
            detail = {"message": raw[:500]}
        raise RuntimeError("HTTP {} from {}: {}".format(exc.code, urllib.parse.urlsplit(url).path, detail)) from exc
    except urllib.error.URLError as exc:
        raise RuntimeError("request failed for {}: {}".format(urllib.parse.urlsplit(url).path, exc.reason)) from exc


def date_to_milliseconds(date_text):
    date = dt.datetime.strptime(date_text, "%Y-%m-%d").date()
    midnight = dt.datetime.combine(date, dt.time.min, tzinfo=JST)
    return int(midnight.timestamp() * 1000)


def build_fields(report, report_base):
    date = report["date"]
    return {
        "日期": date_to_milliseconds(date),
        "报告链接": {
            "link": report_base.rstrip("/") + "/reports/daily?date=" + urllib.parse.quote(date),
            "text": date + " Hermes 每日进化报告",
        },
        "今日结论": report.get("conclusion") or "暂无结论",
        "异常": report.get("anomaly") or "未记录异常",
        "下一步": report.get("next_step") or "继续观察",
    }


def normalize_date_value(value):
    if isinstance(value, dict):
        value = value.get("value") or value.get("timestamp")
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def find_existing_record(records, target_ms):
    for record in records:
        if normalize_date_value((record.get("fields") or {}).get("日期")) == target_ms:
            return record.get("record_id")
    return None


def feishu_host(domain):
    value = (domain or "").lower()
    if "lark" in value:
        return "https://open.larksuite.com"
    return "https://open.feishu.cn"


def list_records(base, app_token, table_id, tenant_token):
    records = []
    page_token = ""
    while True:
        query = {"page_size": "500"}
        if page_token:
            query["page_token"] = page_token
        url = "{}/open-apis/bitable/v1/apps/{}/tables/{}/records?{}".format(
            base, app_token, table_id, urllib.parse.urlencode(query)
        )
        _, response = request_json("GET", url, token=tenant_token)
        if response.get("code") != 0:
            raise RuntimeError("list Bitable records failed: code={}".format(response.get("code")))
        data = response.get("data") or {}
        records.extend(data.get("items") or [])
        if not data.get("has_more"):
            return records
        page_token = data.get("page_token") or ""
        if not page_token:
            raise RuntimeError("Bitable pagination returned has_more without page_token")


def upsert_record(base, app_token, table_id, tenant_token, fields):
    target_ms = fields["日期"]
    record_id = find_existing_record(list_records(base, app_token, table_id, tenant_token), target_ms)
    collection = "{}/open-apis/bitable/v1/apps/{}/tables/{}/records".format(base, app_token, table_id)
    if record_id:
        method = "PUT"
        url = collection + "/" + urllib.parse.quote(record_id)
        action = "updated"
    else:
        method = "POST"
        url = collection
        action = "created"
    _, response = request_json(method, url, {"fields": fields}, tenant_token)
    if response.get("code") != 0:
        raise RuntimeError("upsert Bitable record failed: code={}".format(response.get("code")))
    record = (response.get("data") or {}).get("record") or {}
    saved_id = record.get("record_id") or record_id
    if not saved_id:
        raise RuntimeError("Bitable upsert returned no record_id")

    verify_url = collection + "/" + urllib.parse.quote(saved_id)
    _, verify = request_json("GET", verify_url, token=tenant_token)
    if verify.get("code") != 0:
        raise RuntimeError("verify Bitable record failed: code={}".format(verify.get("code")))
    saved_fields = (((verify.get("data") or {}).get("record") or {}).get("fields") or {})
    if normalize_date_value(saved_fields.get("日期")) != target_ms:
        raise RuntimeError("verified Bitable record date does not match")
    return action, saved_id


def parse_args():
    home = pathlib.Path.home()
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--date", default=dt.datetime.now(JST).date().isoformat())
    parser.add_argument("--report-base", default="https://hermes-report.ikarp.top")
    parser.add_argument("--relay-token-file", default=str(home / ".config/hermes-report/relay_token"))
    parser.add_argument("--env-file", default=str(home / ".hermes/.env"))
    parser.add_argument("--kb-verified-file", default=str(home / ".hermes/feishu_hermes_kb_verified.json"))
    return parser.parse_args()


def main():
    args = parse_args()
    target_ms = date_to_milliseconds(args.date)
    if target_ms <= 0:
        raise RuntimeError("invalid report date")

    relay_token = pathlib.Path(args.relay_token_file).read_text(encoding="utf-8").strip()
    _, report = request_json(
        "POST",
        args.report_base.rstrip("/") + "/api/v1/reports/daily",
        {"date": args.date},
        relay_token,
    )
    if report.get("date") != args.date:
        raise RuntimeError("Relay archived an unexpected report date")

    env = load_env(args.env_file)
    app_id = env.get("FEISHU_KB_APP_ID") or env.get("LARK_KB_APP_ID")
    app_secret = env.get("FEISHU_KB_APP_SECRET") or env.get("LARK_KB_APP_SECRET")
    domain = env.get("FEISHU_KB_DOMAIN") or env.get("LARK_KB_DOMAIN")
    if not app_id or not app_secret:
        raise RuntimeError("missing dedicated FEISHU_KB_APP_ID/FEISHU_KB_APP_SECRET")

    verified = json.loads(pathlib.Path(args.kb_verified_file).read_text(encoding="utf-8"))
    app_token = verified["bitable"]["app_token"]
    tables = verified["bitable"]["tables"]
    table_id = next(item["table_id"] for item in tables if item.get("table") == "每日进化报告")
    base = feishu_host(domain)
    _, token_response = request_json(
        "POST",
        base + "/open-apis/auth/v3/tenant_access_token/internal",
        {"app_id": app_id, "app_secret": app_secret},
    )
    if token_response.get("code") != 0:
        raise RuntimeError("tenant token request failed: code={}".format(token_response.get("code")))
    tenant_token = token_response["tenant_access_token"]

    fields = build_fields(report, args.report_base)
    action, record_id = upsert_record(base, app_token, table_id, tenant_token, fields)
    print(json.dumps({
        "status": "ok",
        "date": args.date,
        "archive_url": fields["报告链接"]["link"],
        "feishu_action": action,
        "record_id": record_id,
    }, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:
        print(json.dumps({"status": "error", "error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        sys.exit(1)
