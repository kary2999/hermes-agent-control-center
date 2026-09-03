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

## 构建

```bash
go build -o dist/hermes-connector ./cmd/connector
go build -o dist/hermes-relay ./cmd/relay
```

## 启动 Relay

```bash
export HERMES_RELAY_LISTEN_ADDR=":8080"
export HERMES_RELAY_TOKEN="<本地生成的高熵令牌>"
./dist/hermes-relay
```

## 启动 Connector

```bash
export HERMES_DEVICE_ID="mac-mini"
export HERMES_RELAY_URL="https://你的中转服务器域名"
export HERMES_RELAY_TOKEN="<与 Relay 相同的令牌>"
export HERMES_KANBAN_DB="$HOME/.hermes/kanban.db"
export HERMES_POLL_INTERVAL="10s"
./dist/hermes-connector
```

浏览器访问 Relay 根地址。页面首次打开时输入同一共享令牌；令牌只保存在当前浏览器本地存储中。

## MVP 边界

当前版本不包含 Docker、远程控制、图片上传、事件历史、多用户权限、数据库持久化和复杂同步协议。后续仅在实际需要时扩展。

## 安全

不要将真实密钥、Token、服务器凭证、用户数据、日志或 Hermes Prompt 提交到 Git。
