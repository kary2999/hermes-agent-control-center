---
title: Hermes Agent Control Center
---
<!-- [skill: go-team-standards · 技术方案] Hermes Agent 控制中心项目入口 -->

# Hermes Agent Control Center

面向个人使用的低依赖 Hermes Agent 管理工作台，用于在 Lark 中查看 Agent 状态、任务进度、近期完成及图片或外部链接形式的交付结果。

## 项目状态

当前处于方案设计阶段，尚未开始正式功能开发。

## 设计目标

- Mac mini Connector 主动连接中转服务器，不暴露家庭公网端口。
- CentOS Server 以单一静态二进制运行，无需 Docker、Node.js、数据库或 Redis。
- 使用 WSS、AES-256-GCM、消息序号和 nonce 保护通信。
- 使用增量事件、按阈值压缩、观察租约和自适应频率降低流量。
- Lark Web App 通过 REST 获取快照、通过 SSE 实时更新。
- UI 使用已经确认的简体中文极简风格。

## 交付物

- `hermes-connector`：macOS arm64 客户端。
- `hermes-relay`：CentOS/Linux amd64 中转服务。
- 内嵌式 Lark Web UI。
- systemd、launchd 安装与升级脚本。
- 双端源代码、测试、协议和部署文档。

## 开发路线

完整架构、协议、安全设计、分阶段任务和验收标准见：

- [开发方案](docs/development-plan.md)

## 安全

仓库不得提交真实密钥、Token、服务器地址、生产日志、用户数据或 Hermes prompt。密钥仅在部署阶段本地生成，并分别保存到 macOS Keychain 与服务器受限目录。
