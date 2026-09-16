<!-- [skill: go-team-standards · 技术方案 · 安全边界] Hermes 控制中心只读定时任务页面实施计划 -->
# 定时任务查看功能实施计划

## 目标

在 Hermes 控制中心新增只读“定时任务”页面，展示 Mac mini 上的 Hermes Cron、macOS launchd、用户 crontab 和 at 队列。查看链路不得调用大模型，不提供创建、编辑、暂停、恢复、删除或立即执行能力。

## 数据契约

每条记录仅允许包含：`id`、`name`、`source`、`kind`、`instruction_preview`、`schedule_rule`、`status`、`last_run_at`、`last_exit_code`。

- 集合缺省值固定为 `[]`。
- 数字 `0` 必须保留。
- 指令预览在 Connector 端脱敏并按 rune 截断。
- launchd 只上传可执行文件名与脚本文件名组成的安全摘要，不上传本地绝对路径、完整参数、环境变量或 plist 原文。
- 常驻服务与周期任务必须明确区分。

## 采集范围

- `$HERMES_HOME/cron/jobs.json`
- `~/Library/LaunchAgents/*.plist`
- `/Library/LaunchAgents/*.plist`
- `/Library/LaunchDaemons/*.plist`
- `crontab -l`（固定绝对路径、无 shell、超时与输出上限）
- `atq`（固定绝对路径、无 shell、超时与输出上限）

Apple 系统任务默认不展示；第三方和用户自定义任务保留。无法可靠确定最后执行时间时返回空值，页面显示“暂无执行记录”，禁止用文件修改时间伪装。

## 页面

新增顶级导航“定时任务”。桌面端使用表格，移动端使用卡片，显示任务名称、任务指令、最后执行时间、定时器规则和状态。保留暗色主题、骨架、空状态、重试及 stale 缓存体验。

## 验收

1. 表驱动测试先 RED 后 GREEN。
2. 完整测试、Race、Vet、Connector/Relay 构建通过。
3. 使用真实 Mac mini 数据验证 3 个自定义周期任务与 3 个 Hermes 常驻服务。
4. API 不暴露完整命令行、环境变量、脚本正文或原始错误。
5. 完成 PR、Release、双端部署及 Lark Axon 实际页面验证。
