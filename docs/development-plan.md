---
title: Hermes Agent 控制中心开发方案
status: draft
created_at: 2026-09-03T00:08:51+09:00
---
<!-- [skill: go-team-standards · 技术方案] Hermes Agent 控制中心双端低依赖实现方案 -->

# Hermes Agent 控制中心实施计划

> **For Hermes:** 按任务逐项实现；每项必须先测试、后实现，并在通过验证后提交。

**目标：** 交付 Mac mini Connector 与 CentOS Relay Server 两端完整代码，让 Lark Web App 实时查看 Agent、任务、阶段进度、最近完成和图片/外链结果，同时降低运行依赖、服务器带宽与维护成本。

**架构：** 单仓库、两个 Go 静态二进制。Mac Connector 主动建立 WSS 长连接，事件变化立即同步；无人看盘时仅低频心跳，有人看盘时由服务器通过带 TTL 的观察租约调整同步频率。服务器内嵌极简 Web UI，通过 REST 获取初始快照、SSE 接收实时更新，不要求浏览器与 Mac mini 直连。

**技术栈：** Go 标准库优先；`net/http`、WebSocket 小型库、JSON、gzip、AES-256-GCM、嵌入式静态资源、systemd。CentOS 无需 Node.js、Docker、数据库或 Redis。

---

## 1. 交付范围

### 1.1 必须交付

- `hermes-connector`：macOS arm64 单文件可执行程序。
- `hermes-relay`：CentOS/Linux amd64 单文件可执行程序。
- 内嵌 Web UI：简体中文极简界面，不单独部署前端。
- systemd 服务文件、安装/升级/卸载脚本。
- Mac launchd 服务文件与安装脚本。
- 双端配置模板，不包含任何真实密钥、域名或生产地址。
- 协议定义、测试、部署说明和安全说明。
- Lark Web App 配置步骤；服务器信息提供后再执行部署与发布。

### 1.2 第一版非目标

- 不做多人/多租户权限体系。
- 不提供远程执行 shell、修改文件或任意 Hermes 指令。
- 不依赖 PostgreSQL、Redis、Kafka、Docker、Kubernetes。
- 不做复杂指标分析和日志全文检索。
- 不保存 Hermes prompt、Token、私钥、生产 PII。
- 不承诺后台中的 Lark WebView 长连接永不暂停；重新打开必须自动补拉完整快照。

## 2. 成本优先的系统设计

```text
Mac mini
└── hermes-connector
    ├── 读取 Hermes 允许暴露的状态
    ├── 本地 Outbox
    └── WSS 主动连接
             │
             ▼
CentOS Server
└── hermes-relay（单二进制）
    ├── TLS/WSS 接入
    ├── 状态快照 + JSONL 事件
    ├── SSE 页面推送
    ├── 内嵌极简 Web UI
    └── 结果图片受限存储
             │
             ▼
Lark Web App
```

### 2.1 无外部运行依赖

- Go 依赖全部编译进二进制。
- 服务端状态使用原子快照文件与轮转 JSONL 事件文件。
- UI 使用 `go:embed` 嵌入服务端二进制。
- systemd 负责进程守护和自动重启。
- TLS 证书由用户现有证书或后续确定的自动证书方案提供。

### 2.2 同步策略

| 场景 | 行为 |
|---|---|
| Agent/任务状态变化 | 立即发送增量事件 |
| 任务完成/失败 | 立即发送结果事件 |
| 有人看盘且有运行任务 | 默认每 5 秒发送轻量进度 |
| 有人看盘但无运行任务 | 默认每 30 秒发送状态 |
| 无人看盘 | 每 60 秒心跳，15 分钟完整快照 |
| 重连成功 | 立即发送完整快照并重放未确认事件 |

看盘端允许选择 `自动 / 2秒 / 5秒 / 15秒 / 30秒 / 仅事件`。服务器强制范围为 2～120 秒；租约 60 秒未续期即自动降频。多人同时观察时采用最短有效间隔。

## 3. 协议与压缩

### 3.1 消息帧

```json
{
  "version": 1,
  "device_id": "device_alias",
  "event_id": "uuid",
  "sequence": 1842,
  "occurred_at": "2026-09-03T00:00:00Z",
  "message_type": "task.progress",
  "compression": "gzip",
  "key_version": 1,
  "nonce": "base64",
  "ciphertext": "base64"
}
```

### 3.2 节流与压缩

- 小于 1 KiB：不压缩，避免压缩头大于收益。
- 大于等于 1 KiB：gzip level 1，使用 Go 标准库。
- 增量事件只携带变化字段。
- 首次打开、重连和周期校准才发送完整快照。
- 图片不进入 WebSocket；压缩为 WebP/JPEG 后单独上传，事件仅保存结果 URL 和元数据。
- 默认单张图片上限 1 MiB、单任务最多 5 张、总存储上限可配置。

## 4. 安全设计

### 4.1 分层保护

1. 外层：WSS + TLS 1.3。
2. 设备认证：每台 Connector 独立设备凭证。
3. 消息层：AES-256-GCM 对称加密与完整性校验。
4. 防重放：`sequence + occurred_at + nonce`。
5. 单用户页面认证：首次输入高熵访问令牌，服务端只保存 SHA-256 摘要，成功后写入 `HttpOnly + Secure + SameSite=Strict` Cookie。

### 4.2 密钥管理

- Mac 密钥保存在 macOS Keychain，通过系统 `security` 命令读取。
- Server 密钥位于 `/etc/hermes-relay/secrets/`，权限 `0600`，systemd 服务使用独立低权限用户。
- 禁止将密钥写入代码、Git、日志、测试数据、URL 查询参数或 Lark 卡片。
- 支持 `key_version`，允许新旧密钥短时并存后轮换。
- 日志只记录设备别名、事件类型、序号和结果，不记录明文 payload、Token、nonce 或密文全文。

### 4.3 IO 与资源限制

- WSS 握手、读、写、空闲均设置超时。
- 单帧、单请求、单图片设置硬上限。
- Connector goroutine 全部绑定 `context`/`errgroup`，确保退出。
- 服务器限制设备连接数、页面订阅数和频率调整请求。
- 所有错误必须检查并保留 `%w` 错误链；业务错误使用受控错误码。

## 5. 仓库结构

```text
hermes-agent-control-center/
├── cmd/
│   ├── connector/main.go
│   └── relay/main.go
├── internal/
│   ├── connector/
│   │   ├── collector.go
│   │   ├── outbox.go
│   │   ├── sync_client.go
│   │   └── watch_policy.go
│   ├── relay/
│   │   ├── device_hub.go
│   │   ├── state_store.go
│   │   ├── event_store.go
│   │   ├── result_store.go
│   │   └── watch_lease.go
│   ├── protocol/
│   │   ├── envelope.go
│   │   ├── message.go
│   │   ├── crypto.go
│   │   └── compression.go
│   ├── auth/
│   │   ├── device.go
│   │   └── viewer.go
│   └── web/
│       ├── handler.go
│       └── assets/
│           ├── index.html
│           ├── app.js
│           └── style.css
├── deploy/
│   ├── systemd/hermes-relay.service
│   ├── launchd/com.hermes.connector.plist
│   ├── install-relay.sh
│   ├── install-connector.sh
│   └── uninstall.sh
├── configs/
│   ├── relay.example.yaml
│   └── connector.example.yaml
├── tests/
│   └── integration/
├── go.mod
├── Makefile
└── README.md
```

## 6. HTTP/WSS 接口

### 6.1 Connector

- `GET /v1/device/connect`：升级为 WSS。
- 连接建立后先进行 challenge/response，再允许发送加密事件。
- Server 下发：`watch.start`、`watch.renew`、`watch.stop`、`snapshot.request`。
- Connector 上发：`device.hello`、`device.heartbeat`、`agent.snapshot`、`task.started`、`task.progress`、`task.completed`、`task.failed`、`result.created`。

### 6.2 Viewer

- `POST /api/v1/session`：使用访问令牌建立 HttpOnly 会话。
- `DELETE /api/v1/session`：退出。
- `GET /api/v1/dashboard`：获取完整快照。
- `GET /api/v1/events`：SSE 实时事件。
- `PUT /api/v1/watch-policy`：设置观察租约及刷新频率。
- `POST /api/v1/watch-policy/renew`：续租。
- `GET /api/v1/results/{id}`：鉴权后读取结果图片。

统一响应包含 `code`、`message`、`data`、`timestamp`、`traceId`；外部接口带请求超时与结构化日志。

## 7. 分阶段开发计划

### Task 1：建立仓库与双二进制骨架

**文件：** `go.mod`、`cmd/connector/main.go`、`cmd/relay/main.go`、`Makefile`、`.golangci.yml`

1. 编写启动、配置校验失败测试。
2. 建立 Connector/Relay 构造函数和 context 生命周期。
3. 验证：`go test ./...`、`go vet ./...`、`go build ./cmd/...`。
4. 提交：`build(infra): initialize control center binaries`。

### Task 2：协议、压缩与 AES-GCM

**文件：** `internal/protocol/*.go`

1. 表驱动测试：小包不压缩、大包 gzip、错误压缩类型拒绝。
2. 测试：加解密往返、篡改拒绝、nonce 重复拒绝、过期消息拒绝。
3. 实现版本化 envelope、压缩阈值和密钥版本。
4. 模糊测试解码器，确保畸形输入不 panic。
5. 提交：`feat(protocol): add compressed encrypted event envelope`。

### Task 3：Relay WSS 设备连接

**文件：** `internal/relay/device_hub.go`、`internal/auth/device.go`

1. 使用 `httptest` 编写握手、认证、心跳和断线测试。
2. 实现单设备连接替换策略和超时。
3. 实现 ACK、sequence 防重放和最大帧限制。
4. 验证断网、重连、重复帧和错误密钥。
5. 提交：`feat(relay): accept authenticated device connections`。

### Task 4：Connector Outbox 与重连

**文件：** `internal/connector/outbox.go`、`sync_client.go`

1. 测试事件先落盘再发送、ACK 后删除、进程重启可恢复。
2. 实现原子写文件、大小上限和已确认事件清理。
3. 实现指数退避并加入随机抖动，最长 30 秒。
4. 验证服务器重启期间事件不丢、恢复后有序重放。
5. 提交：`feat(connector): add durable outbox and reconnect loop`。

### Task 5：Hermes 状态采集适配器

**文件：** `internal/connector/collector.go`

1. 先验证 Hermes 当前版本的真实状态来源和 schema。
2. 只读取允许字段，不采集 prompt、Token、密钥和敏感日志。
3. 编写 Agent/任务/结果映射测试。
4. 实现变化检测与增量事件生成。
5. 提交：`feat(connector): collect sanitized hermes task state`。

### Task 6：观察租约和自适应频率

**文件：** `internal/relay/watch_lease.go`、`internal/connector/watch_policy.go`

1. 测试最小/最大频率、续租、过期降频和多人最短间隔。
2. 实现页面可见性驱动的租约续期。
3. Connector 对服务器指令再次执行本地边界校验。
4. 验证异常关闭页面后 60 秒内恢复空闲频率。
5. 提交：`feat(sync): add viewer-controlled watch leases`。

### Task 7：状态与事件持久化

**文件：** `internal/relay/state_store.go`、`event_store.go`

1. 测试原子快照、崩溃恢复、重复事件幂等和 JSONL 轮转。
2. 实现单写者模型，避免并发写损坏。
3. 默认保留 30 天事件，按大小和日期轮转。
4. 验证 Relay 被强制终止后可从磁盘恢复最新状态。
5. 提交：`feat(storage): persist snapshots and task history`。

### Task 8：结果图片与外链

**文件：** `internal/relay/result_store.go`

1. 测试 MIME 白名单、文件大小、任务数量和路径穿越拒绝。
2. 图片使用随机服务端 ID，不暴露原始本地路径。
3. 外链仅允许 `https`，显示域名并设置安全跳转属性。
4. 验证未登录用户无法读取图片。
5. 提交：`feat(results): store secured task artifacts`。

### Task 9：单用户认证与 Web API

**文件：** `internal/auth/viewer.go`、`internal/web/handler.go`

1. 测试错误令牌、会话过期、CSRF 和 Cookie 属性。
2. 服务端仅保存访问令牌摘要。
3. 实现 REST 快照和 SSE；SSE 断开必须释放 goroutine。
4. 所有响应显式带 `traceId`，日志结构化并脱敏。
5. 提交：`feat(web): add authenticated dashboard API`。

### Task 10：内嵌极简 Lark UI

**文件：** `internal/web/assets/*`

1. 将已确认的极简 DEMO 调整为真实 API 数据源。
2. 页面打开先拉快照，再建立 SSE。
3. 使用 `visibilitychange` 暂停/恢复观察租约。
4. 实现 Agent、未完成、最近完成、结果图片/外链四项视图。
5. 验证简体中文 UTF-8、手机/桌面布局和断线提示。
6. 提交：`feat(ui): embed minimal agent control center`。

### Task 11：CentOS 与 macOS 安装

**文件：** `deploy/**/*`、`configs/*`

1. 构建 Linux amd64 和 Darwin arm64 静态二进制。
2. Relay 以独立低权限用户运行，目录权限最小化。
3. Connector 密钥写入 Keychain，launchd 自动启动。
4. 安装脚本不得输出密钥；卸载默认保留数据并提示显式清理。
5. 验证全新 CentOS 不安装 Go/Node/Docker 即可运行。
6. 提交：`build(deploy): add dependency-free service installers`。

### Task 12：端到端验证与 Lark 配置

**文件：** `tests/integration/*`、`README.md`

1. 启动临时 Relay 和 Connector。
2. 模拟任务开始、进度、完成、图片结果。
3. 验证页面快照和 SSE 顺序一致。
4. 中断网络并恢复，验证 Outbox 重放且无重复任务。
5. 验证 10 分钟无人看盘后降频。
6. 使用用户后续提供的服务器、域名和证书部署。
7. 配置 Lark Web App 主页并发布版本。
8. 从 Lark 桌面端和手机端分别实测。
9. 提交：`test(e2e): verify relay connector and lark dashboard`。

## 8. 验收标准

- 两个目标平台只需复制单一二进制和配置即可运行。
- CentOS 不需要 Go、Node、Docker、数据库或 Redis。
- Mac mini 无公网入站端口。
- 断网期间产生的任务完成事件可在恢复后补传。
- 重复事件不会形成重复任务记录。
- 无人看盘时自动降频；页面打开后在一个同步周期内更新。
- 页面重开先显示持久化快照，再恢复实时事件。
- 所有中文为 UTF-8 简体中文，无乱码。
- AES-GCM 篡改、重放、过期消息均被拒绝。
- 未认证用户无法获取状态、图片和 SSE。
- Go 单测采用表驱动；新代码覆盖率至少 80%。
- `go test ./...`、`go test -race ./...`、`go vet ./...`、`golangci-lint run` 全部通过。
- 真实 Lark Web App 在桌面和移动端均能打开。

## 9. 风险与取舍

- 文件存储适合单设备、单用户和低任务量；未来多设备高并发时再迁移 PostgreSQL，不提前引入。
- gzip 不如 zstd 压缩率高，但无需额外运行组件，第一版成本更低。
- TLS 已提供链路加密；应用层 AES-GCM 是用户要求的纵深防御，会增加密钥轮换复杂度。
- Lark WebView 进入后台可能暂停 SSE，因此状态正确性依赖快照补拉，不能只依赖实时流。
- 图片最容易扩大磁盘和带宽，必须限制大小、数量和保留时间。
- Hermes 状态来源必须在编码前读取当前版本源码/API 验证，禁止根据猜测定义采集器。

## 10. 服务器信息提供清单

方案确认后，部署前只需提供：

- CentOS 大版本与 CPU 架构。
- SSH 登录方式（不要在聊天中发送密码或私钥）。
- 域名及 DNS 控制方式。
- 是否已有 TLS 证书。
- 防火墙允许的端口。
- 数据与图片预期保留天数。

密钥在部署时由本地命令生成并分别写入 Mac Keychain 与服务器安全目录，不经过聊天或 Git。
