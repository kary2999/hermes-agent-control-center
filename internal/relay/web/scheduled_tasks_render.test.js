// Static UI contract test for the "定时任务" (scheduled tasks) page render
// logic embedded in workbench.html.
//
// There is no existing JS test framework/package.json in this repository,
// so this is a small dependency-free Node script (not Jest/Mocha): it
// extracts the self-contained render block from workbench.html (delimited
// by the SCHEDULED_TASKS_RENDER_BEGIN/END markers, chosen specifically so
// this block only touches document.createElement/getElementById/textContent
// and no other global state from the rest of the file) and executes it
// inside a vm context against a minimal fake DOM implemented below, then
// asserts on the resulting fake DOM tree.
//
// Run with: node internal/relay/web/scheduled_tasks_render.test.js

"use strict";

const fs = require("fs");
const path = require("path");
const vm = require("vm");
const assert = require("assert");

const htmlPath = path.join(__dirname, "workbench.html");
const html = fs.readFileSync(htmlPath, "utf8");

const beginMarker = "// SCHEDULED_TASKS_RENDER_BEGIN";
const endMarker = "// SCHEDULED_TASKS_RENDER_END";
const beginIdx = html.indexOf(beginMarker);
const endIdx = html.indexOf(endMarker);
assert.ok(beginIdx !== -1 && endIdx !== -1 && endIdx > beginIdx,
  "could not locate SCHEDULED_TASKS_RENDER_BEGIN/END markers in workbench.html");
const renderSource = html.slice(beginIdx, endIdx);

// ---- Minimal fake DOM ----

class FakeNode {
  constructor(tag) {
    this.tagName = tag;
    this.children = [];
    this._text = "";
    this.attrs = {};
    this.className = "";
  }
  appendChild(child) {
    this.children.push(child);
    return child;
  }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  get textContent() {
    if (this._text) return this._text;
    return this.children.map((child) => child.textContent).join("");
  }
  set textContent(v) { this._text = v; this.children = []; }
  removeChild(child) {
    this.children = this.children.filter((c) => c !== child);
  }
  get firstChild() { return this.children[0] || null; }
}

function makeFakeDocument(elementsById) {
  return {
    createElement(tag) { return new FakeNode(tag); },
    getElementById(id) { return elementsById[id] || null; },
  };
}

function runRenderScenario(tasks) {
  const tbody = new FakeNode("tbody");
  const cards = new FakeNode("div");
  const elementsById = {
    "scheduled-tasks-tbody": tbody,
    "scheduled-tasks-cards": cards,
  };
  const sandbox = { document: makeFakeDocument(elementsById), console };
  vm.createContext(sandbox);
  vm.runInContext(renderSource + "\nthis.renderScheduledTasksTable = renderScheduledTasksTable;", sandbox);
  sandbox.renderScheduledTasksTable(tasks);
  return { tbody, cards };
}

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log("PASS:", name);
  } catch (e) {
    failures++;
    console.error("FAIL:", name, "-", e.message);
  }
}

// ---- Scenarios ----

check("empty array renders an explicit empty state, not a blank table", () => {
  const { tbody, cards } = runRenderScenario([]);
  assert.strictEqual(tbody.children.length, 1);
  assert.strictEqual(tbody.children[0].tagName, "tr");
  const td = tbody.children[0].children[0];
  assert.strictEqual(td.textContent, "暂无定时任务");
  assert.strictEqual(cards.children.length, 1);
  assert.strictEqual(cards.children[0].textContent, "暂无定时任务");
});

check("last_exit_code === 0 is rendered as 0, not swallowed as falsy", () => {
  const task = {
    id: "a", name: "Nightly", source: "hermes_cron", kind: "cron",
    instruction_preview: "do thing", schedule_rule: "0 2 * * *",
    status: "enabled", last_run_at: 0, last_exit_code: 0,
  };
  const { tbody } = runRenderScenario([task]);
  assert.strictEqual(tbody.children.length, 1);
  const cells = tbody.children[0].children.map((td) => td.textContent);
  // last_run_at = 0 is a real, valid timestamp (Unix epoch), must NOT
  // render as "暂无执行记录".
  assert.notStrictEqual(cells[2], "暂无执行记录");
  // status cell (index 4) must mention exit code 0 explicitly.
  assert.ok(cells[4].indexOf("退出码 0") !== -1, "expected exit code 0 to be shown, got: " + cells[4]);
});

check("last_run_at null/undefined renders '暂无执行记录'", () => {
  const task = {
    id: "b", name: "Never Run", source: "launchd", kind: "resident",
    status: "running", last_run_at: null, last_exit_code: null,
  };
  const { tbody } = runRenderScenario([task]);
  const cells = tbody.children[0].children.map((td) => td.textContent);
  assert.strictEqual(cells[2], "暂无执行记录");
  // last_exit_code null/undefined must NOT append a "退出码" suffix.
  assert.ok(cells[4].indexOf("退出码") === -1, "did not expect exit code text, got: " + cells[4]);
});

check("missing fields use explicit Chinese placeholders", () => {
  const task = { id: "c", source: "crontab", kind: "cron", status: "enabled" };
  const { tbody } = runRenderScenario([task]);
  const cells = tbody.children[0].children.map((td) => td.textContent);
  assert.ok(cells[0].indexOf("未命名定时任务") !== -1);
  assert.ok(cells[0].indexOf("用户 Crontab") !== -1);
  assert.strictEqual(cells[1], "未提供任务指令");
  assert.strictEqual(cells[3], "未设置执行规则");
});

check("desktop name cell shows localized source and kind", () => {
  const task = { id: "d", name: "Gateway", source: "launchd", kind: "resident", status: "running" };
  const { tbody } = runRenderScenario([task]);
  const nameCell = tbody.children[0].children[0].textContent;
  assert.ok(nameCell.indexOf("macOS launchd") !== -1, "source label missing: " + nameCell);
  assert.ok(nameCell.indexOf("常驻服务") !== -1, "kind label missing: " + nameCell);
});

check("failed refresh preserves rendered data and marks it stale", () => {
  assert.ok(html.indexOf("if (scheduledTasksHasData)") !== -1,
    "scheduled-task refresh failure must distinguish cached data from first-load failure");
  assert.ok(html.indexOf("刷新失败，当前显示的是最近一次成功获取的数据") !== -1,
    "scheduled-task stale banner copy is missing");
});

if (failures > 0) {
  console.error(failures + " test(s) failed");
  process.exit(1);
}
console.log("All scheduled-tasks render contract tests passed");
