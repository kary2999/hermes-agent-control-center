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
