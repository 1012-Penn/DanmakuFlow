# CLAUDE.md — 弹幕系统 (Danmaku)

## 项目定位
基于 Go + Gin + gorilla/websocket 的弹幕系统，从零搭建，边学边写。

## 学习流程
1. **涉及新知识点时**，先在 `docs/` 下写一份技术文档，让用户预览熟悉
2. 用户确认理解后，再开始写对应代码
3. 代码完成后，同步将知识点写入 Obsidian

## 教学风格
- 每个语法元素第一次出现时，讲清楚"为什么长这样"（函数签名、符号含义、设计意图）
- 不假设用户懂 HTTP/URL 基础符号，逐个拆解
- 不挖底层实现，面向有 Go 语法基础的初学者
- 一次对话最多推进一个 `.go` 文件的功能
- 用户可随时反问，也主动提问确认理解

## 技术文档约定
- 位置：Obsidian Vault 下的 `docs/` 目录
- 路径：`/mnt/c/Users/1/Documents/Obsidian Vault/danmaku/docs/`
- 命名：`编号-主题.md`，如 `01-gin-intro.md`
- 必须先写文档让用户预览，再进入编码阶段

## Obsidian 笔记规则
- Vault 路径：`/mnt/c/Users/1/Documents/Obsidian Vault/danmaku/`
- 每个学习阶段一个 `.md` 文件，按 `01-xxx.md`、`02-xxx.md` 编号
- 每篇笔记必须包含：时间、前置知识、代码逐段讲解、API 速查、动手实验、下一步
- 每次写笔记时同步更新 `项目概览.md` 的学习状态
- 用户的错误单独记录到 `踩坑记录.md`

## 代码风格偏好
- Go package 按层分：model → handler → service
- JSON 字段用 snake_case，Go 字段用 PascalCase
- 注释用中文
- 写完代码手动运行 `go fmt ./...` 格式化

## 技术栈
- Go 1.26
- Gin v1.12.0
- gorilla/websocket v1.5.3（待接入）
