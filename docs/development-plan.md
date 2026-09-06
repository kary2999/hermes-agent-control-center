---
title: Hermes Agent 控制中心 MVP 路线
status: completed
---
<!-- [skill: go-team-standards · 技术方案] 最小可用链路开发路线 -->

# Hermes Agent 控制中心 MVP 路线

## 目标

只交付一条可用链路：Hermes 状态 → Mac Connector → Relay → 简体中文查看页面。

## 已完成

1. Connector 只读采集 Hermes 任务和运行状态。
2. Connector 按固定间隔向 Relay 上传 JSON 快照。
3. Relay 在内存中保存最新快照。
4. Relay 提供 `/api/v1/dashboard` 接口和内嵌网页。
5. 页面展示 Agent、未完成任务、最近完成、状态、阶段和粗粒度进度。
6. 完成本地真实 Hermes 数据链路、基础测试、Vet 和双端构建验证。

## 非目标

Docker、数据库、事件历史、远程控制、图片仓库、多用户权限和复杂同步协议不属于 MVP。

## 后续交付计划

> 目的：把后续任务固定在 GitHub 仓库文档中，避免 Agent 会话压缩、切换或丢失上下文后遗漏任务。

### 代码维护边界

1. 服务器端 Relay 和 Mac mini 端 Connector 必须一起维护在 GitHub 仓库 `kary2999/hermes-agent-control-center`。
2. 本地开发目录固定为 `/Users/karp/Code/hermes-agent-control-center`。
3. 当前开发分支固定为 `feat/recent-runs-daily-models`，未经确认不切换交付分支。
4. 生产入口固定为 `https://hermes.ikarp.top/`；部署或 Nginx/证书变更前后必须验证 `ikarp.top` 博客不受影响。
5. 任何涉及服务器二进制、Mac mini Connector 二进制、GitHub Release 或部署脚本的变更，都要保证 GitHub、服务器运行版本和 Mac mini 运行版本可追溯一致。

### 当前待办

1. 验证当前未提交代码变更来源，确认是否属于本功能交付范围后再继续修改。
2. 完成最近执行任务和每日模型统计相关代码检查，避免只实现页面展示但数据来源不完整。
3. 跑完整本地验证：`go test ./...`、双端构建、必要的真实链路 smoke test。
4. 检查生产路由行为：未知路径、GET/POST 方法、未授权响应、验证页和 `/workbench` 跳转是否符合预期。
5. 确认 GitHub 分支、PR、CI 和 Release 状态，避免只在本地或服务器落地。
6. 部署服务器端 Relay，并验证生产 HTTPS、安全响应头、认证后 API 和页面真实数据。
7. 部署或重启 Mac mini Connector，并验证它能持续上传脱敏快照。
8. 读取并确认 Lark Axon Web App / Workbench 配置、发布版本和可见范围。
9. 在真实 Lark 客户端中打开工作台，验证桌面端页面渲染、认证、刷新和导航。
10. 手机端必须由真实 Lark 客户端发现并打开成功后，才能宣称移动端可用。

### 验收口径

- “可试用”只表示公网 HTTPS 页面、Relay API、Connector 上传和只读页面基本可用。
- “可投入使用”必须同时满足：GitHub 状态清楚、测试通过、服务器端部署可追溯、Mac mini Connector 正常上传、Lark Axon 工作台已发布、目标用户可见，并且桌面端和需要声明的手机端都已真实打开验证。
- 未验证的阶段必须明确标记，禁止用历史报告、可访问 URL 或本地测试代替 Lark 客户端可见性。
