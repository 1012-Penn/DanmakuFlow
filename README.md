# DanmakuFlow — 实时弹幕推送系统

基于 **Go** + **Gin** + **gorilla/websocket** 构建的高性能实时弹幕系统，支持 B站 风格的弹幕类型（滚动/顶部/底部/逆向）、房间隔离、多存储后端、Redis 跨实例广播、异步持久化与优雅关闭。

## 系统架构

```
┌──────────────┐      ┌──────────────────────────────────────┐
│  浏览器/客户端  │ ──WS→ │  WebSocket 升级 (handler.go)          │
│  (WebSocket)  │ ←广播─ │    ↓                                │
└──────────────┘      │  Client (readPump / writePump)        │
                      │    ↓                                │
                      │  Room (独立 goroutine + client 池)     │
                      │    ↕                                │
                      │  Hub (房间管理器 + 跨实例广播)           │
                      └──────────┬───────────────────────────┘
                                 │
                    ┌────────────┴────────────┐
                    ▼                         ▼
           Service 层 (业务逻辑)       Redis Pub/Sub
           create + validate +        (跨实例广播到其他服务器)
           频率限制 + 广播
                    │
         ┌──────────┴──────────┐
         ▼                     ▼
  MySQL (异步 channel)    MemoryStore
  GORM 批量写入           (开发/测试)
```

## 功能特性

### 核心功能
- **RESTful API** — 创建/查询弹幕的 HTTP 接口
- **实时广播** — WebSocket 推送，消息到达毫秒级同步到同房间所有客户端
- **房间隔离** — 每个房间独立 goroutine + client 池，广播域完全隔离
- **B站风格弹幕类型** — 滚动 / 顶部 / 底部 / 逆向，可配置字号与颜色
- **多存储后端** — MemoryStore（开发）和 MySQL + GORM（生产），配置切换无需改代码
- **异步写入** — Channel 生产者-消费者模式，数据库写入不阻塞广播路径

### 生产级特性
- **Redis 跨实例广播** — 基于 Redis Pub/Sub 的多机部署支持，自带 SourceID 去重
- **Redis 降级** — Redis 不可用时自动降级为纯本地广播，不影响服务可用性
- **优雅关闭** — HTTP → 排空异步写入 → 关闭 WebSocket → 关闭 Redis → 关闭 MySQL，五步有序
- **频率限制** — 基于内存的每用户滑动窗口，可配置每秒消息上限
- **连接限制** — 可配置每 IP 最大连接数 / 每房间最大连接数，防止资源耗尽
- **Origin 校验** — WebSocket 握手时验证 Origin 头，防止跨站劫持
- **Ping/Pong 心跳** — 自动检测并清理死连接，配置保活超时
- **结构化日志** — log/slog 实现，支持级别过滤（debug/info/warn/error）和格式切换（text/json）
- **配置外部化** — YAML 配置文件，不修改代码即可调整运行时参数

## 技术栈

| 层 | 技术 | 说明 |
|---|---|---|
| 语言 | Go 1.26 | 高性能并发 |
| HTTP 框架 | Gin v1.12 | 路由、中间件、参数绑定 |
| WebSocket | gorilla/websocket v1.5 | 实时双向通信 |
| 数据库 | MySQL 8.0 | 可选，生产级持久化 |
| ORM | GORM v2 | 自动迁移、连接池 |
| 缓存/消息 | Redis 7 | 跨实例广播（Pub/Sub） |
| 日志 | log/slog（标准库） | 结构化日志，零依赖 |
| 前端 | 原生 HTML + JavaScript | 暗色主题 B站风格弹幕渲染 |
| 配置 | YAML | 运行时外部化配置 |

## 快速开始

```bash
git clone https://github.com/1012-Penn/DanmakuFlow.git
cd DanmakuFlow
go mod tidy
go run .
```

浏览器访问 `http://localhost:8080`。

### 启用 MySQL 持久化

```bash
# 启动 MySQL（Docker）
docker compose up -d

# 编辑配置
# config.yaml → store.dsn 填入你的数据库连接串

# 运行（自动检测 MySQL）
go run .
```

未配置 MySQL 时自动使用内存存储。

### 启用 Redis 跨实例广播

```bash
# config.yaml 中配置 Redis 地址
# redis.addr: "localhost:6379"

# 启动后自动订阅 Redis Pub/Sub，断开时降级本地广播
go run .
```

### Docker 部署

```bash
docker compose up --build -d
```

## API 参考

### 发送弹幕

```bash
# POST /api/room/:room_id/danmaku
curl -X POST http://localhost:8080/api/room/liveroom_001/danmaku \
  -H "Content-Type: application/json" \
  -d '{"content":"Hello Danmaku","user_id":"u001"}'

# 完整参数示例
curl -X POST http://localhost:8080/api/room/liveroom_001/danmaku \
  -H "Content-Type: application/json" \
  -d '{
    "content":"前方高能",
    "user_id":"u001",
    "color":"#e94560",
    "type":"scroll",
    "font_size":25
  }'
```

### 查询弹幕历史

```bash
curl http://localhost:8080/api/room/liveroom_001/danmaku?limit=20
```

### WebSocket 连接

```
ws://localhost:8080/ws?room_id=liveroom_001
```

连接后发送 JSON 消息即可广播到同房间所有客户端。

### 爆发测试页面

浏览器打开 `http://localhost:8080/burst`，可配置弹幕数量、发送间隔和弹幕类型，一键压测。

## 性能基准

在 WSL2 环境下（单机 localhost，MemoryStore，无 Redis，无 MySQL）的基准测试结果：

| 场景 | 连接数 | 消息吞吐 | 广播吞吐 | 错误率 |
|------|--------|---------|---------|:-------:|
| 轻负载 | 100 | 20 msg/s | 2,000 msg/s | 0% |
| 中负载 | 500 | 200 msg/s | **~100,000 msg/s** | 0% |
| HTTP API | — | **~1,000 QPS** | — | 0% |
| 高负载广播 | 1,000 | 900 msg/s | **~850,000 msg/s** | 0% |
| 连接伸缩 | 2,000 | 0（静默） | — | 0% |

> 压测工具见 `cmd/benchmark/`，支持参数化 WebSocket / HTTP 压测。在高并发场景下，广播吞吐受限于客户端消费能力而非服务端处理能力。

## 项目结构

```
DanmakuFlow/
├── main.go                 # 入口：组件组装与优雅关闭
├── config.yaml             # 外部配置文件
├── config/config.go        # 配置加载与默认值
├── model/danmaku.go        # 弹幕数据模型
├── store/
│   ├── store.go            # 存储接口
│   ├── memory.go           # 内存实现
│   └── mysql.go            # MySQL/GORM 实现
├── service/danmaku.go      # 业务逻辑层
├── handler/danmaku.go      # HTTP 处理器
├── websocket/
│   ├── hub.go              # 房间管理器 + 广播 + Redis 订阅
│   ├── client.go           # 连接读写泵 + 心跳
│   └── handler.go          # WebSocket 升级处理器
├── redisclient/pubsub.go   # Redis Pub/Sub 封装
├── cmd/benchmark/          # 压测工具
├── templates/
│   ├── index.html          # 弹幕页面（4 种动画类型）
│   └── burst.html          # 爆发测试页面
└── danmaku_test.go         # 集成测试
```

## 配置说明

系统通过 `config.yaml` 外部化配置，所有字段均有内置默认值，未配置时直接运行。详细配置项：

```yaml
server:
  port: 8080

websocket:
  write_wait_seconds: 10       # 写超时
  pong_wait_seconds: 60        # Pong 超时
  max_message_size: 512        # 单条消息上限
  broadcast_buffer_size: 256   # 房间广播缓冲
  send_buffer_size: 256        # 客户端发送缓冲
  max_conn_per_room: 0         # 房间连接上限（0=不限）
  max_conn_per_ip: 0           # IP 连接上限（0=不限）
  allowed_origins: []          # 允许的 Origin（空=不校验）

store:
  dsn: ""                      # MySQL DSN（空=使用内存存储）
  async_buffer_size: 1024      # 异步写入缓冲

redis:
  addr: ""                     # Redis 地址（空=不使用）
  instance_id: ""              # 实例标识用于去重

rate_limit:
  messages_per_sec: 0          # 每用户每秒上限（0=不限）
```

## 许可证

MIT
