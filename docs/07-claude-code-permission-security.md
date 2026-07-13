---
title: Claude Code 权限模型、安全配置与环境变量完整指南
source: docs.anthropic.com + community best practices
date: 2026-07-13
tags: [claude-code, security, permissions, settings, env-vars, enterprise]
---

# Claude Code 权限模型、安全配置与环境变量完整指南

## 概述

Claude Code 采用**分层权限架构**（tiered permission architecture），默认严格只读。当需要执行额外操作（编辑文件、运行测试、执行命令）时，Claude Code 会请求用户的明确许可。这套系统在设计上兼顾了**开发效率**与**安全管控**，既支持个人开发者灵活使用，也满足企业级组织的集中治理需求。

本文覆盖：
- Permission Mode 详解（六种模式的适用场景）
- `settings.json` 配置选项（全局、项目、本地三层）
- 环境变量（Environment Variable）完整参考
- `.claude/settings.json` 本地配置
- 多用户/团队协作的安全最佳实践

> 来源：[Claude Code Security Docs](https://code.claude.com/docs/en/security)

---

## 一、Permission Mode（权限模式）

Claude Code 提供六种 Permission Mode，每种模式在**便利性**与**安全审查**之间做了不同的权衡。用户可以通过 `Shift+Tab`（CLI 中）或模式选择器（VS Code、Desktop、claude.ai）循环切换。

### 模式总览

| 模式 | 无需询问即可执行的操作 | 适用场景 |
|------|----------------------|---------|
| `default`（Manual） | 仅读取操作 | 刚上手、敏感工作 |
| `acceptEdits` | 读取、文件编辑、常用文件系统命令（`mkdir`、`touch`、`mv`、`cp` 等） | 代码迭代审查 |
| `plan` | 仅读取（只探索不修改） | 代码审查前分析 |
| `auto` | 所有操作（附带后台安全分类器检查） | 长任务、减少权限疲劳 |
| `dontAsk` | 仅预先批准的工具 | 锁定环境的 CI/脚本 |
| `bypassPermissions` | 所有操作 | **仅限隔离容器/VM** |

> 来源：[Permission Modes Docs](https://code.claude.com/docs/en/permission-modes)

### 各模式详解

#### 1. default（Manual 模式）

这是最安全的模式，也是默认模式。每个工具在**首次使用**时都会弹权限请求。CLI 中标记为 **Manual**，状态栏显示灰色 `⏸ manual mode on` 徽章。

```bash
# 启动时指定
claude --permission-mode default
# 或简写
claude --permission-mode manual
```

#### 2. acceptEdits 模式

自动批准以下操作，无需用户确认：
- 在工作目录下创建和编辑文件
- 常用文件系统命令：`mkdir`、`touch`、`rm`、`rmdir`、`mv`、`cp`、`sed`
- 这些命令带上安全环境变量（如 `LANG=C`、`NO_COLOR=1`）或进程包装器（如 `timeout`、`nice`、`nohup`）时同样自动批准

> **限制**：仅适用于工作目录或 `additionalDirectories` 内的路径。写入[受保护路径](#受保护路径-protected-paths)和非工作目录仍会触发提示。

```bash
claude --permission-mode acceptEdits
```

适合"事后通过 `git diff` 审查变更"的工作流。

#### 3. plan 模式

Claude 可以读取文件、运行 shell 命令来探索代码库，但**不会修改任何源文件**。完成探索后，Claude 会呈现一份计划，用户可以选择：
- **批准并启动 auto 模式**
- **批准并启动 acceptEdits 模式**
- **批准并逐一手动审查**
- **继续完善计划**
- **使用 Ultraplan**（浏览器端审查）

```bash
claude --permission-mode plan
```

也可在单次 prompt 前加 `/plan` 前缀进入该模式。

#### 4. auto 模式（研究预览）

让 Claude **无需逐次确认**即可执行操作。一个独立的**安全分类器模型**（classifier）在每次操作执行前进行审查，阻止以下行为：
- 下载并执行代码（如 `curl | bash`）
- 向外部端点发送敏感数据
- 生产环境部署和数据库迁移
- 云存储上的批量删除
- IAM 或仓库权限授予
- 修改共享基础设施
- 不可逆地破坏会话前已存在的文件
- Force push
- `terraform destroy` 等基础设施销毁命令
- 将敏感内容推送到默认分支

> **可用条件**：
> - 计划层级：所有计划可用
> - 账户类型：Team/Enterprise 需要 Owner 在管理员设置中启用
> - 模型版本：Anthropic API 上需 Opus 4.6+ 或 Sonnet 4.6+；Bedrock/Vertex/Foundry 上需 Sonnet 5/Opus 4.7/4.8
> - 提供商：默认 Anthropic API 可用；Bedrock/Vertex/Foundry 需设置 `CLAUDE_CODE_ENABLE_AUTO_MODE=1`

```bash
# 启动时指定
claude --permission-mode auto

# 启用自动模式的配置
{
  "autoMode": {
    "environment": [
      "$defaults",
      "Source control: github.com/my-org/*",
      "Trusted cloud buckets: s3://my-build-artifacts"
    ]
  }
}
```

**成本与延迟**：分类器运行在独立模型上（不占用 `/model` 选择的模型配额），每次检查增加一次往返延迟。读取和工作目录编辑（受保护路径除外）不经过分类器。

#### 5. dontAsk 模式

自动拒绝所有本应弹出提示的操作。**只有** `permissions.allow` 规则中明确批准的操作以及内建只读命令可以执行。状态栏显示 `⏵⏵ don't ask on`。

```bash
claude --permission-mode dontAsk
```

适用于 CI 管道或高度受限环境，需要预先精确定义 Claude 可以做什么。

#### 6. bypassPermissions 模式

**跳过所有权限检查和安全性提示**。这是最危险的模式——仅在隔离容器或 VM 中使用：

```bash
claude --permission-mode bypassPermissions
# 等价于
claude --dangerously-skip-permissions
```

> ⚠️ Linux/macOS 上以 root/sudo 运行时拒绝启动。管理员可通过 `permissions.disableBypassPermissionsMode` 在托管设置中禁止此模式。

### 受保护路径（Protected Paths）

在所有模式（`bypassPermissions` 除外）下，以下路径的写入操作**永远不会被自动批准**：

**受保护目录**：
`.git`、`.config/git`、`.vscode`、`.idea`、`.husky`、`.cargo`、`.devcontainer`、`.yarn`、`.mvn`、`.claude`（`.claude/worktrees` 除外）

**受保护文件**：
`.gitconfig`、`.gitmodules`、`.bashrc`、`.bash_profile`、`.zshrc`、`.zprofile`、`.profile`、`.npmrc`、`.yarnrc`、`.mcp.json`、`.claude.json` 等

> 来源：[Permission Modes - Protected Paths](https://code.claude.com/docs/en/permission-modes#protected-paths)

---

## 二、Permission Rule 语法与配置

### 规则格式

权限规则遵循 `Tool` 或 `Tool(specifier)` 格式：

| 规则 | 效果 |
|------|------|
| `Bash` 或 `Bash(*)` | 匹配所有 Bash 命令 |
| `Bash(npm run build)` | 匹配精确命令 `npm run build` |
| `Bash(npm run test *)` | 通配符匹配：以 `npm run test` 开头的命令 |
| `Bash(git * main)` | 匹配如 `git checkout main`、`git merge main` |
| `Bash(ls *)` | 带空格前缀：匹配 `ls -la` 但不匹配 `lsof` |
| `Bash(ls*)` | 无空格：匹配 `ls -la` 和 `lsof` |
| `Read(./.env)` | 匹配读取 `.env` 文件 |
| `Read(/docs/**)` | 匹配项目中 `/docs/` 目录下所有读取 |
| `Read(~/.zshrc)` | 匹配读取用户目录的 `.zshrc` |
| `Read(//tmp/scratch.txt)` | 匹配读取绝对路径 `/tmp/scratch.txt` |
| `WebFetch(domain:example.com)` | 匹配向 example.com 发起的请求 |
| `WebFetch(domain:*.example.com)` | 匹配任意子域名 |
| `MCP__servers__server__tool` | 匹配 MCP 工具 |

### 规则评估顺序

**Deny（拒绝）→ Ask（询问）→ Allow（允许）**，**首次匹配获胜**。

- `deny` 规则：将匹配的调用从 Claude 的上下文中移除（裸工具名）或阻断匹配的调用（带 specifier）
- `ask` 规则：始终弹窗询问
- `allow` 规则：跳过权限提示

### 完整配置示例

```json
{
  "permissions": {
    "allow": [
      "Bash(npm run lint)",
      "Bash(npm run test:*)",
      "Bash(git commit *)",
      "Read(~/.zshrc)"
    ],
    "deny": [
      "Bash(curl *)",
      "Bash(wget *)",
      "Read(./.env)",
      "Read(./.env.*)",
      "Read(./secrets/**)",
      "Read(~/.ssh/**)"
    ],
    "ask": [
      "Bash(git push:*)"
    ],
    "defaultMode": "acceptEdits",
    "additionalDirectories": ["../shared-lib"]
  }
}
```

> 来源：[Permissions Docs](https://code.claude.com/docs/en/permissions)

### 工作目录扩展

默认情况下，Claude 只能访问**启动目录及其子目录**。通过 `additionalDirectories` 扩展工作目录边界：

```json
{
  "permissions": {
    "additionalDirectories": ["../another-project", "/path/to/shared"]
  }
}
```

### Symlink 处理规则

- **Allow 规则**：只有当 symlink 路径**和**目标路径都匹配时才生效。工作目录内的 symlink 若指向外部，仍会提示。
- **Deny 规则**：只要 symlink 路径**或**目标路径匹配即生效。指向被拒绝文件的 symlink 本身也被拒绝。

---

## 三、settings.json 配置详解

### 文件位置与优先级

| 优先级 | 文件 | 作用域 | 是否共享 |
|--------|------|--------|---------|
| 1（最高） | **托管设置**（Managed settings） | 组织/机器 | 是企业级强制策略 |
| 2 | 命令行参数 | 会话 | 否 |
| 3 | `.claude/settings.local.json` | 项目 | 否（gitignore 自动添加） |
| 4 | `.claude/settings.json` | 项目 | 是（检入版本控制） |
| 5 | `~/.claude/settings.json` | 用户全局 | 否 |

> ⚠️ **重要**：托管设置**不能被任何其他层覆盖**。如果安全团队在托管设置中拒绝某个工具，项目和用户设置都无法重新允许它。

> 来源：[Settings Docs](https://code.claude.com/docs/en/settings)

### 核心配置选项

#### 通用设置

| 键 | 类型 | 默认值 | 描述 |
|----|------|--------|------|
| `model` | string | `"default"` | 模型覆盖（别名：`sonnet`、`opus`、`haiku`） |
| `agent` | string | — | 默认 agent 名称 |
| `language` | string | `"english"` | 响应语言偏好 |
| `autoUpdatesChannel` | string | `"latest"` | `"stable"` 或 `"latest"` |
| `cleanupPeriodDays` | number | `30` | 会话清理的期限天数 |
| `alwaysThinkingEnabled` | boolean | `false` | 默认启用扩展思考 |
| `defaultShell` | string | `"bash"` | `!` 命令的 shell（`"bash"` 或 `"powershell"`） |
| `includeGitInstructions` | boolean | `true` | 在系统 prompt 中包含 git 指令 |

#### 权限配置

| 键 | 类型 | 描述 |
|----|------|------|
| `permissions.allow` | string[] | 跳过权限提示的规则列表 |
| `permissions.deny` | string[] | 阻断工具使用的规则列表 |
| `permissions.ask` | string[] | 始终需要确认的规则列表 |
| `permissions.defaultMode` | string | 默认权限模式 |
| `permissions.additionalDirectories` | string[] | Claude 可以访问的额外目录 |

#### MCP Server 配置

```json
{
  "mcpServers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]
    },
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": {
        "GITHUB_TOKEN": "ghp_..."
      }
    }
  }
}
```

#### Sandbox（沙箱）配置

```json
{
  "sandbox": {
    "enabled": true,
    "autoAllowBashIfSandboxed": true,
    "excludedCommands": ["git", "docker"],
    "network": {
      "allowUnixSockets": ["~/.ssh/agent-socket"],
      "allowLocalBinding": true,
      "httpProxyPort": 8080
    }
  }
}
```

#### Hooks（钩子）

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Write|Edit",
        "command": "npx prettier --write $CLAUDE_FILE_PATH"
      }
    ],
    "PreToolUse": {
      "Bash": "echo 'Running command...'"
    }
  }
}
```

> 来源：[Settings Docs](https://code.claude.com/docs/en/settings)

---

## 四、环境变量（Environment Variable）完整参考

### 设置方式

**方式一：Shell 环境变量（仅当前会话）**

```bash
# macOS、Linux、WSL
export ANTHROPIC_API_KEY="sk-ant-..."
export ANTHROPIC_MODEL="claude-sonnet-4-5"
claude

# Windows PowerShell
$env:ANTHROPIC_API_KEY = "sk-ant-..."
claude
```

**方式二：settings.json 中的 `env` 字段（持久化）**

```json
{
  "env": {
    "API_TIMEOUT_MS": "1200000",
    "BASH_DEFAULT_TIMEOUT_MS": "300000",
    "DISABLE_TELEMETRY": "1"
  }
}
```

> **优先级**：环境变量 > settings.json 同名字段 > 默认值。例如 `ANTHROPIC_MODEL` 覆盖 `model` 设置。

> 来源：[Environment Variables Docs](https://code.claude.com/docs/en/env-vars)

### 身份认证与 API 连接

| 变量 | 用途 |
|------|------|
| `ANTHROPIC_API_KEY` | API 密钥（作为 `X-Api-Key` 头发送）；设置后覆盖订阅 |
| `ANTHROPIC_AUTH_TOKEN` | `Authorization` 头的自定义值（自动添加 `Bearer ` 前缀） |
| `ANTHROPIC_BASE_URL` | 覆盖 API 端点，用于通过代理或网关路由 |
| `ANTHROPIC_BEDROCK_BASE_URL` | 覆盖 Amazon Bedrock 端点 URL |
| `ANTHROPIC_VERTEX_BASE_URL` | 覆盖 Google Cloud Agent Platform 端点 URL |
| `ANTHROPIC_VERTEX_PROJECT_ID` | GCP 项目 ID |
| `ANTHROPIC_FOUNDRY_BASE_URL` | Microsoft Foundry 资源的完整基础 URL |
| `ANTHROPIC_FOUNDRY_AUTH_TOKEN` | Foundry 的 Bearer 令牌（优先于 API Key） |
| `ANTHROPIC_BEDROCK_SERVICE_TIER` | Bedrock 服务层：`default`、`flex` 或 `priority` |
| `ANTHROPIC_BETAS` | 附加的 `anthropic-beta` 标头值（逗号分隔） |
| `ANTHROPIC_CUSTOM_HEADERS` | 自定义 HTTP 标头（`Name: Value` 格式，多行用换行符分隔） |

### 模型选择

| 变量 | 用途 |
|------|------|
| `ANTHROPIC_MODEL` | 使用的模型设置名称 |
| `ANTHROPIC_DEFAULT_HAIKU_MODEL` | Haiku 级模型名称（用于后台任务） |
| `ANTHROPIC_DEFAULT_SONNET_MODEL` | 默认 Sonnet 级模型 |
| `ANTHROPIC_DEFAULT_OPUS_MODEL` | 默认 Opus 级模型 |

### 超时与空闲控制

| 变量 | 默认值 | 用途 |
|------|--------|------|
| `API_TIMEOUT_MS` | 600000（10 分钟） | API 请求超时（毫秒） |
| `API_FORCE_IDLE_TIMEOUT` | — | 覆盖 5 分钟空闲超时；`0` 禁用 |
| `BASH_DEFAULT_TIMEOUT_MS` | 120000（2 分钟） | Bash 命令默认超时 |
| `BASH_MAX_TIMEOUT_MS` | 600000（10 分钟） | 模型可设置的最大 Bash 超时 |

### 功能开关（设为 `1` 启用）

| 变量 | 效果 |
|------|------|
| `DISABLE_TELEMETRY` | 禁用遥测 |
| `DISABLE_AUTOUPDATER` | 禁用自动更新 |
| `DISABLE_COST_WARNINGS` | 禁用费用警告 |
| `DISABLE_ERROR_REPORTING` | 禁用错误上报 |
| `DISABLE_PROMPT_CACHING` | 禁用 prompt caching |
| `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` | 禁用所有非必要网络流量 |
| `CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN` | 禁用全屏渲染 |
| `CLAUDE_CODE_DISABLE_ARTIFACT` | 禁用 Artifact |
| `CLAUDE_CODE_DISABLE_AUTO_MEMORY` | 禁用自动记忆 |
| `CLAUDE_CODE_DISABLE_FILE_CHECKPOINTING` | 禁用文件 checkpoint（`/rewind` 无法恢复） |
| `CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS` | 从系统 prompt 中移除 git 指令 |
| `CLAUDE_CODE_DISABLE_1M_CONTEXT` | 禁用 1M 上下文窗口支持 |
| `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS` | 禁用所有后台任务 |
| `CLAUDE_CODE_DISABLE_CLAUDE_MDS` | 禁用所有 CLAUDE.md 内存文件加载 |
| `CLAUDE_CODE_DISABLE_CRON` | 禁用计划任务 |
| `CLAUDE_CODE_ENABLE_AUTO_MODE` | 在 Bedrock/Vertex/Foundry 上启用 auto mode |
| `CLAUDE_CODE_EFFORT_LEVEL` | 设置努力级别（`low`/`medium`/`high`/`xhigh`/`max`/`auto`） |
| `CLAUDE_CODE_MAX_OUTPUT_TOKENS` | 设置最大输出 token 数 |
| `CLAUDE_CODE_ENABLE_TELEMETRY` | 启用 OpenTelemetry 指标收集 |

### 网络与代理

| 变量 | 用途 |
|------|------|
| `HTTP_PROXY` | HTTP 代理服务器 |
| `HTTPS_PROXY` | HTTPS 代理服务器 |
| `NO_PROXY` | 绕过代理的域名/IP |
| `CLAUDE_CODE_CLIENT_CERT` | mTLS 客户端证书路径 |
| `CLAUDE_CODE_CLIENT_KEY` | mTLS 客户端私钥路径 |
| `CLAUDE_CODE_CERT_STORE` | CA 证书源（`bundled`、`system`，逗号分隔） |

### MCP（Model Context Protocol）

| 变量 | 用途 |
|------|------|
| `MCP_TIMEOUT` | MCP 服务器启动超时（毫秒） |
| `MCP_TOOL_TIMEOUT` | MCP 工具执行超时（毫秒） |
| `MAX_MCP_OUTPUT_TOKENS` | MCP 工具响应最大 token 数（默认 25000） |

> 完整变量列表见：[Environment Variables Docs](https://code.claude.com/docs/en/env-vars)

---

## 五、Sandbox（沙箱）安全机制

Sandbox 是 Claude Code 的**操作系统级别**安全隔离层，为 Bash 命令提供文件系统和网络隔离。

### 启用沙箱

```bash
# 交互式启用
/sandbox

# 或配置
{
  "sandbox": {
    "enabled": true
  }
}
```

### 文件系统隔离

```json
{
  "sandbox": {
    "filesystem": {
      "allowRead": ["~/project/src/**", "/tmp/build/**"],
      "allowWrite": ["~/project/dist/**"],
      "denyRead": ["~/.ssh/**", "~/.aws/**"]
    }
  }
}
```

### 网络隔离

```json
{
  "sandbox": {
    "network": {
      "allowedDomains": ["api.anthropic.com", "*.corp.example.com"],
      "deniedDomains": ["*.malicious.com"],
      "allowLocalBinding": true,
      "httpProxyPort": 8080,
      "socksProxyPort": 8081
    }
  }
}
```

### Sandbox 与 Permission 的关系

- **Permissions** 控制 Claude Code 可以使用哪些**工具**和访问哪些**文件或域名**，适用于所有工具
- **Sandbox** 提供**操作系统级的执行限制**，只适用于 Bash 命令及其子进程
- 两者互补：Permission deny 规则阻止 Claude 尝试访问受限制资源；Sandbox 规则在 OS 层面强制执行，即使 prompt injection 绕过了 Claude 的决策层

> 来源：[Sandboxing Docs](https://code.claude.com/docs/en/sandboxing)

---

## 六、多用户/团队协作安全最佳实践

### 1. 托管设置的治理（Managed Settings Governance）

企业安全团队可以通过**托管设置**（Managed Settings）实现强制策略：

```json
{
  "permissions": {
    "deny": [
      "Bash(curl *)",
      "Bash(wget *)",
      "Bash(rm -rf *)",
      "Bash(curl * | bash)",
      "Read(./.env)",
      "Read(./.env.*)",
      "Read(./secrets/**)",
      "Read(~/.ssh/**)",
      "Read(~/.aws/**)"
    ],
    "disableBypassPermissionsMode": "disable",
    "disableAutoMode": "disable"
  },
  "allowManagedPermissionRulesOnly": true,
  "allowManagedHooksOnly": true,
  "allowManagedMcpServersOnly": true,
  "forceRemoteSettingsRefresh": true
}
```

**关键托管唯一设置**：

| 设置 | 效果 |
|------|------|
| `allowManagedPermissionRulesOnly` | 仅托管设置可定义权限规则 |
| `allowManagedHooksOnly` | 仅加载托管 hooks |
| `allowManagedMcpServersOnly` | 仅允许管理员定义的 MCP 服务器 |
| `disableBypassPermissionsMode` | 阻止开发者使用 `--dangerously-skip-permissions` |
| `forceRemoteSettingsRefresh` | 强制每次启动时刷新远程设置（失败则退出） |
| `strictPluginOnlyCustomization` | 限制 skills/agents/hooks/MCP 只能来自插件 |
| `allowManagedReadPathsOnly` | 仅托管设置可定义 sandbox 读取路径 |

### 2. MCP 服务器审批工作流

1. 开发者提交 MCP 请求（名称、源码 URL、版本、数据范围）
2. 安全审查：检查 star 数 >50、近期提交、无 CVE、无危险标志
3. 风险分级：LOW / MEDIUM / HIGH
4. 加入批准注册表（固定版本、到期日期）
5. 通过 `allowedMcpServers` 部署

### 3. Audit 日志与合规

通过 `PostToolUse` hooks 实现审计日志：

```json
{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Bash|Edit|Write",
        "hooks": [
          {
            "command": "bash -c 'echo \"{\\\"tool\\\": \\\"$TOOL_NAME\\\", \\\"timestamp\\\": \\\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\\\", \\\"user\\\": \\\"$USER\\\"}\" >> /var/log/claude-audit.jsonl'"
          }
        ]
      }
    ]
  }
}
```

### 4. 团队推荐的安全基线

| 安全层级 | 适用场景 | 关键控制 |
|----------|---------|---------|
| **入门** | 小型团队（<5人）、内部项目 | 拒绝 `.env`、`.key`、`.pem` 读取；基础 PreToolUse hook |
| **标准** | 团队 5-20 人、接近生产 | 入门级全部 + 锁定 CI/CD、Terraform；prompt injection 检测；密钥扫描 |
| **严格** | 团队 20+、生产关键系统 | 标准级全部 + 锁定数据库 Schema、基础设施配置文件；速率限制器；会话日志 |
| **合规** | HIPAA/SOC2/PCI | 严格级全部 + 合规审计追踪；托管设置独占；强制沙箱；`forceRemoteSettingsRefresh: true` |

### 5. Known Bypass 与防护

| 绕过方式 | 原理 | 防护措施 |
|---------|------|---------|
| `cat .env` 通过 Bash | Read deny 不覆盖 Bash 读取 | **Sandbox** 或 **PreToolUse hook** |
| `sh -c "curl ..."` | 前缀匹配绕过 | PreToolUse hook 捕获子 shell 执行 |
| `curl \| bash` | 管道到 shell | Hook 正则捕获 `\| bash` |
| Base64 编码绕过 | 前缀匹配不识别编码 | Hook 捕获 `base64`、`xxd`、`openssl` |
| 用户安装 MCP 服务器 | 权限规则不覆盖 | `allowManagedMcpServersOnly` |

> **关键洞察**：没有任何单一安全层是足够的。Sandbox 提供最强保障。对于非托管环境，PreToolUse hooks 是防御 Bash 绕过的最佳手段。

### 6. 入职自动化脚本

```bash
#!/bin/bash
# onboard-claude-code.sh
set -euo pipefail

npm install -g @anthropic-ai/claude-code

mkdir -p ~/.claude
curl -s https://internal.company.com/claude/enterprise-settings.json \
  > ~/.claude/settings.json

claude -p "echo 'Claude Code configured successfully'" \
  --max-turns 1 2>/dev/null && echo "Setup complete." || echo "Setup failed."
```

### 7. CI 配置合规检查

```yaml
# .github/workflows/claude-config-check.yml
name: Claude Config Compliance
on: [pull_request]
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Validate Claude settings
        run: |
          jq -e '.permissions.deny' .claude/settings.json
          ! jq -e '.mcpServers["blocked-server"]' .claude/settings.json
          test -f CLAUDE.md
```

### 8. 安全漏洞报告

如果发现 Claude Code 的安全漏洞：
1. **不要公开披露**
2. 通过 [HackerOne 项目](https://hackerone.com/4f1f16ba-10d3-4d09-9ecc-c721aad90f24/embedded_submissions/new)报告
3. 包含详细的复现步骤
4. 给足够时间修复后再公开

> 来源：[Security Docs](https://code.claude.com/docs/en/security)

---

## 七、常见问题与排错

### 检查当前配置

```bash
# 在 Claude Code 会话内
/doctor    # 验证配置文件
/status    # 查看当前配置来源
/permissions  # 查看所有权限规则
```

### 以安全模式启动

```bash
claude --safe-mode
# 禁用所有自定义配置（CLAUDE.md、skills、plugins、hooks、MCP）
# 保留认证和模型选择
```

### 使用干净配置目录

```bash
CLAUDE_CONFIG_DIR=/tmp/clean-config claude
```

### 环境变量生效时机

Claude Code **在启动时读取**环境变量。修改后需要**重启 `claude`** 才会生效。

---

## 参考资料

- [Claude Code Security (官方)](https://code.claude.com/docs/en/security)
- [Permission Modes (官方)](https://code.claude.com/docs/en/permission-modes)
- [Configure Permissions (官方)](https://code.claude.com/docs/en/permissions)
- [Settings.json (官方)](https://code.claude.com/docs/en/settings)
- [Environment Variables (官方)](https://code.claude.com/docs/en/env-vars)
- [Sandboxing (官方)](https://code.claude.com/docs/en/sandboxing)
- [Auto Mode Config (官方)](https://code.claude.com/docs/en/auto-mode-config)
- [Server-Managed Settings (官方)](https://code.claude.com/docs/en/server-managed-settings)
- [Admin Setup (官方)](https://code.claude.com/docs/en/admin-setup)
- [Anthropic Trust Center](https://trust.anthropic.com)
- [Community: Enterprise Patterns](https://github.com/MuhammadUsmanGM/claude-code-best-practices/blob/main/guides/enterprise-patterns.md)
- [Community: Claude Code Hardened](https://github.com/nullze/claude-code-hardened)
- [Community: Security Analysis](https://generalanalysis.com/guides/claude-code-settings-permissions-bash-tool-security)
