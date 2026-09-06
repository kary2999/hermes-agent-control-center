---
title: Hermes Agent 控制中心具体实现方案
status: current
version: v0.3.4
---
<!-- [skill: go-team-standards · 技术方案] Hermes Agent 控制中心当前实现方案 -->

# Hermes Agent 控制中心具体实现方案

## 1. 当前交付目标

只维护一条最小可用链路：

```text
Mac mini 本机 Hermes 数据
→ hermes-connector 只读采集
→ hermes-relay 公网中转
→ Lark Axon Web App / 浏览器工作台展示
```

当前版本定位是个人只读工作台，不是完整多用户管理后台。

## 2. 仓库与运行版本

- GitHub 仓库：`kary2999/hermes-agent-control-center`
- 本地目录：`/Users/karp/Code/hermes-agent-control-center`
- 主分支：`main`
- 当前生产版本：`v0.3.4`
- 生产入口：`https://hermes.ikarp.top/`
- Lark 应用：`Axon`

## 3. 组件职责

### 3.1 Mac mini Connector

源码入口：`cmd/connector`

职责：

1. 读取本机 Hermes 状态库和 Kanban 数据。
2. 按固定间隔生成脱敏快照。
3. 主动通过 HTTPS POST 上传到 Relay。
4. 轮询 Relay 的 Lark handoff 命令队列。
5. 执行受限的 handoff 命令后，上报成功或失败短原因。

边界：

- Mac mini 不开放公网入站端口。
- Connector 不执行浏览器传来的任意 Shell。
- Connector 上传的是允许展示的快照字段，不上传完整 Prompt、完整消息、System Prompt、Token、密钥或路径类敏感信息。

### 3.2 Relay Server

源码入口：`cmd/relay`

职责：

1. 监听服务器本地回环地址，当前由 Nginx 反向代理到公网 HTTPS。
2. 接收 Connector 上传的最新快照。
3. 在内存中保存工作台最新快照。
4. 持久化最小 Lark handoff 命令队列。
5. 提供认证后的 Dashboard JSON API。
6. 提供内嵌 HTML 工作台页面。

边界：

- Relay 不依赖 Docker、Node.js、数据库、Redis。
- Relay 不持有 Mac 本机入站能力。
- Relay 不直接持有或执行 Lark/Hermes 本地敏感上下文。

### 3.3 Workbench 页面

源码位置：`internal/relay/web/workbench.html`

职责：

1. 展示 Agent、任务、最近活动、会话和 Token 用量。
2. 通过 `/api/v1/dashboard` 读取 Relay 已有快照。
3. 页面轮询刷新，不调用大模型，不消耗大模型 Token。
4. 在 Lark handoff 不可用或失败时显示简短原因。
5. 移动端使用卡片/分栏降级，避免横向溢出。

## 4. 数据流

### 4.1 快照上报

```text
Hermes state.db / kanban.db
  ↓ 只读查询
connector.Snapshot
  ↓ JSON + Bearer Token
POST /api/v1/snapshot
  ↓
relay.SnapshotStore
  ↓
GET /api/v1/dashboard
  ↓
Workbench 渲染
```

快照包含：

- 设备 ID
- 采集时间
- Agent/模型聚合
- 当前任务、全部任务、最近完成任务
- 最近执行记录，页面只保留最近 10 条
- 会话列表、最后一次用户提示词预览
- Token 统计，展示最小单位为 M
- Lark handoff 状态和脱敏失败原因

### 4.2 访问认证

当前采用两类 Token：

1. `HERMES_RELAY_TOKEN`
   - Connector 写快照使用。
   - 可访问 Connector 需要的受保护 API。
2. `HERMES_DASHBOARD_TOKEN`
   - Lark/浏览器只读入口使用。
   - 只允许换取浏览器会话 Cookie。
   - 不能写快照，不能执行 handoff 写操作。

认证流程：

```text
访问 https://hermes.ikarp.top/
  ↓
同域访问验证页
  ↓ 输入访问码或读取 URL fragment 中的 access_token
POST /api/v1/session
  ↓ 服务端校验 token
Set-Cookie: HttpOnly + Secure + SameSite
  ↓
跳转 /workbench
```

`v0.3.4` 已修复的关键点：

- 未认证访问 `/workbench` 只回到同域 `/`，不再跳到 `https://ikarp.top/`。
- 验证页不再使用 `window.prompt`。
- 验证失败只在当前页面提示，不跳出 `hermes.ikarp.top`。
- Gate 页 CSP 增加最小 `style-src 'unsafe-inline'`，确保 Lark/浏览器里样式正常。

## 5. Lark handoff 实现

目标：从工作台选择已有 Hermes 会话，在固定 Lark 场景中创建或复用独立话题，并能提示中断原因。

当前链路：

```text
Workbench 点击继续到 Lark
  ↓
POST /api/v1/sessions/{session_id}/lark-handoff
  ↓
Relay 创建/复用 handoff 命令
  ↓
Connector 轮询 /api/v1/handoff/claim
  ↓
Connector 调用本机 Hermes handoff 命令
  ↓
POST /api/v1/handoff/result
  ↓
Relay / Workbench 展示状态和失败短原因
```

安全设计：

- 浏览器只发受限 handoff 请求，不接触 Lark 凭据。
- 真正创建 Lark 话题的动作由 Mac mini Connector 在本机执行。
- Relay 仅保存最小命令状态，不保存完整聊天内容。
- 失败原因走脱敏与长度限制，只用于告诉用户为什么中断。

## 6. 状态与失败原因监控

Connector 会在既有轮询周期内采集 handoff 状态：

- `handoff_state`
- `handoff_platform`
- `handoff_error`（存在时读取，不存在时兼容为空）

Relay/Workbench 展示字段：

- `handoff_state`
- `handoff_reason`
- `lark_handoff_available`
- `lark_handoff_reason`

当状态为 `failed` 时，页面显示可重试状态和简短失败原因；无原因时显示“已中断，原因未知”等兜底文案。

## 7. 生产部署方式

### 7.1 Release 构建

GitHub Actions 在 `v*` tag 上构建：

- `hermes-relay-linux-amd64`
- `hermes-relay-linux-arm64`
- `hermes-connector-darwin-arm64`
- 对应 `.sha256` 校验文件

### 7.2 服务器部署

服务器使用 systemd 运行 Relay：

```text
/usr/local/bin/hermes-relay
/etc/hermes-relay/relay.env
/var/lib/hermes-relay
```

升级步骤：

1. 从 GitHub Release 下载 Linux amd64 Relay。
2. 下载 `.sha256` 并校验。
3. 备份旧 `/usr/local/bin/hermes-relay`。
4. 覆盖新二进制。
5. `systemctl restart hermes-relay`。
6. 验证服务 `active`、HTTPS 入口和博客站点。

### 7.3 Mac mini Connector

Mac mini 侧使用 LaunchAgent 或本地后台进程运行 Connector。

关键环境变量：

- `HERMES_DEVICE_ID`
- `HERMES_RELAY_URL`
- `HERMES_RELAY_TOKEN`
- `HERMES_KANBAN_DB`
- `HERMES_STATE_DB`
- `HERMES_POLL_INTERVAL`
- `HERMES_HANDOFF_COMMAND`

## 8. 验证清单

每次改动至少验证：

1. `go test ./...`
2. `make all`
3. `git diff --check`
4. GitHub Actions tag workflow 成功。
5. GitHub Release 资产完整。
6. 服务器 Relay `systemctl is-active hermes-relay` 为 `active`。
7. `https://hermes.ikarp.top/` 返回 200，停留在同域。
8. 未认证 `/workbench` no-follow 为 `302 Location: /`。
9. Gate 页包含访问码表单，不含 `window.prompt`。
10. `https://ikarp.top/` 博客返回 200，未受影响。
11. Lark 客户端能打开 Axon 工作台；手机端必须实际打开后才能宣称可用。

## 9. 当前非目标

当前版本不做：

- Docker 部署。
- 多用户权限系统。
- 复杂审计和事件历史库。
- 图片/文件结果仓库。
- 远程 Shell 或远程桌面控制。
- 大模型驱动的实时总结。
- 将完整 Prompt、完整消息正文、Tool 参数/结果同步到服务器。

## 10. 后续维护原则

1. 先修用户可见问题，再做结构优化。
2. 任何生产部署前后都验证 `https://ikarp.top/` 博客不受影响。
3. 服务器 Relay 和 Mac Connector 代码必须一起维护在 GitHub。
4. 文档、代码、Release、服务器运行版本需要能互相追溯。
5. 未经确认不扩大范围；只读工作台和写能力 handoff 分开处理。
