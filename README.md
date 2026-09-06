---
title: Hermes Agent Control Center
---
<!-- [skill: go-team-standards · 技术方案] Hermes Agent 控制中心 MVP 项目入口 -->

# Hermes Agent Control Center

面向个人使用的极简 Hermes Agent 查看面板。Mac Connector 读取本机 Hermes 任务状态并定时上传，Relay 在内存中保存最新快照并提供简体中文网页。

## 当前状态

MVP 已完成并通过本地真实链路验证。

## 已实现

- 查看 Agent/执行者及未完成任务数量。
- 查看执行中、等待中和最近完成的任务。
- 显示任务状态、阶段和粗粒度进度。
- 页面手动刷新及每 5 秒自动刷新。
- Connector 只读访问 Hermes `kanban.db`，不读取 Prompt、消息正文、Token 或密钥。
- Connector 使用 HTTP POST 上传快照，Relay 仅在内存中保留最新快照。
- 共享令牌从环境变量读取，不写入仓库或日志。
- Relay 内嵌网页，无需 Node.js、Docker、数据库或 Redis。

## 架构介绍

```text
Mac mini / 本地 Hermes 主机
┌──────────────────────────────┐
│ Hermes state.db / kanban.db  │
│ 只读采集任务、会话、handoff状态 │
└──────────────┬───────────────┘
               │ 本地只读
┌──────────────▼───────────────┐
│ hermes-connector             │
│ - 定时采集脱敏快照             │
│ - 轮询 Relay handoff 命令      │
│ - 上报执行结果和失败短原因       │
└──────────────┬───────────────┘
               │ HTTPS + Bearer Token
               │ Mac 主动出站，不暴露本机端口
┌──────────────▼───────────────┐
│ hermes-relay / 服务器          │
│ - 内存保存最新快照              │
│ - 提供认证后的 dashboard API    │
│ - 提供 Lark Workbench HTML     │
│ - 持久化最小 handoff 队列        │
└──────────────┬───────────────┘
               │ HTTPS 反向代理
┌──────────────▼───────────────┐
│ Lark / Browser Workbench      │
│ - 只读查看 Agent、任务、会话      │
│ - 可触发受限的 Lark 交接动作      │
│ - 不展示 Prompt、Token、密钥      │
└──────────────────────────────┘
```

### 组件职责

- **Connector**：运行在 Mac mini，负责只读采集 Hermes 本地状态并主动上传脱敏快照；需要执行 Lark 会话交接时，也由 Connector 在本机侧领取并执行命令，避免服务器持有 Lark/Hermes 本地敏感上下文。
- **Relay**：运行在服务器，仅保存最新工作台快照和最小 handoff 队列；对外提供 HTTPS 页面和 API，不依赖数据库、Redis、Node.js 或 Docker。
- **Workbench 页面**：内嵌在 Relay 二进制中，面向 Lark 工作台和浏览器展示简体中文只读监控界面；默认不读取、不渲染 Prompt 正文、消息正文、Token 或密钥。

### 数据与安全边界

- Mac mini 到服务器只走主动出站 HTTPS，同步内容是脱敏后的状态快照。
- 写入快照和读取工作台使用不同认证路径；共享令牌只从环境变量读取，禁止提交到 GitHub。
- Handoff 失败原因只保留脱敏、有长度上限的短文本，用于解释“为什么中断/可重试”。
- GitHub 是服务器端 Relay 和 Mac mini Connector 的唯一代码维护来源；部署版本必须能追溯到提交或 Release。

## 具体实现方案

当前实现细节见：[`docs/implementation-design.md`](docs/implementation-design.md)。

## 构建

```bash
go build -o dist/hermes-connector ./cmd/connector
go build -o dist/hermes-relay ./cmd/relay
```

## 启动 Relay

```bash
export HERMES_RELAY_LISTEN_ADDR="127.0.0.1:8080"
export HERMES_RELAY_TOKEN="<本地生成的高熵令牌>"
export HERMES_UNAUTHORIZED_REDIRECT_URL="https://未认证时跳转的安全地址"
./dist/hermes-relay
```

Relay 强制仅监听回环地址，必须通过服务器上的 HTTPS 反向代理对外提供访问，禁止直接暴露明文 HTTP 端口。

## 启动 Connector

```bash
export HERMES_DEVICE_ID="mac-mini"
export HERMES_RELAY_URL="https://你的中转服务器域名"
export HERMES_RELAY_TOKEN="<与 Relay 相同的令牌>"
export HERMES_KANBAN_DB="$HOME/.hermes/kanban.db"
export HERMES_POLL_INTERVAL="10s"
./dist/hermes-connector
```

浏览器访问 Relay 根地址后，页面显示同域访问码表单；令牌正确时换取安全的限时会话 Cookie 并进入 `/workbench`。验证失败只在当前页展示错误，未认证访问 `/workbench` 会回到同域 `/`，不会跳转到 `ikarp.top`。共享令牌不会保存到浏览器存储。

## MVP 边界

当前版本不包含 Docker、远程控制、图片上传、事件历史、多用户权限、数据库持久化和复杂同步协议。后续仅在实际需要时扩展。

## 安全

不要将真实密钥、Token、服务器凭证、用户数据、日志或 Hermes Prompt 提交到 Git。
