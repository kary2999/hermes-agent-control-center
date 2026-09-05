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

浏览器访问 Relay 根地址后，页面只显示通用令牌验证提示；令牌正确时换取安全的限时会话 Cookie 并进入 `/dashboard`，取消或验证失败会跳转到 `HERMES_UNAUTHORIZED_REDIRECT_URL`。共享令牌不会保存到浏览器存储。

## MVP 边界

当前版本不包含 Docker、远程控制、图片上传、事件历史、多用户权限、数据库持久化和复杂同步协议。后续仅在实际需要时扩展。

## 安全

不要将真实密钥、Token、服务器凭证、用户数据、日志或 Hermes Prompt 提交到 Git。
