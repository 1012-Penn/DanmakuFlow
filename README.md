# DanmakuFlow — 实时弹幕推送系统

基于 **Go** + **Gin** + **gorilla/websocket** 构建的高性能实时弹幕系统，支持 B站 风格的弹幕类型（滚动/顶部/底部/逆向）、房间隔离、多存储后端、Redis 跨实例广播、异步持久化、Prometheus/Grafana 可观测性与优雅关闭。

## 系统架构

### 单实例架构

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
           create + validate +        (非阻塞异步发布，有界队列)
           频率限制 + 广播
                    │
         ┌──────────┴──────────┐
         ▼                     ▼
  MySQL (异步 channel)    MemoryStore
  GORM 逐条写入           (开发/测试)
```

### 双实例部署架构

```
                   ┌───────┐
   客户端 ──WS──→ │ Nginx │ ← 统一入口 localhost:8080
                   │ 负载均衡 │
                   └───┬───┘
              ┌────────┴────────┐
              ▼                 ▼
       danmaku-a:8081    danmaku-b:8082
       (instance ID A)  (instance ID B)
              │                 │
              └──────┬──────────┘
                     ▼
               Redis Pub/Sub
              (跨实例广播)
                     │
                     ▼
                 MySQL 8.0
              (持久化存储)
```

Prometheus 抓取两个实例的 `/metrics`，Grafana 提供统一监控面板。

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
- **非阻塞 Redis 发布** — 有界队列 + 后台 worker，Redis 慢或不可用时上游不阻塞
- **优雅关闭** — HTTP → 排空异步写入 → 关闭 WebSocket → 关闭 Redis → 关闭 MySQL，五步有序
- **频率限制** — 基于内存的每用户最小发送间隔（min-interval）算法，可配置每秒消息上限
- **连接限制** — 可配置每 IP 最大连接数 / 每房间最大连接数，原子检查 + 预留，防止资源耗尽
- **Origin 校验** — WebSocket 握手时验证 Origin 头，默认不修改全局 Upgrader
- **Ping/Pong 心跳** — 自动检测并清理死连接，配置保活超时
- **结构化日志** — log/slog 实现，支持级别过滤（debug/info/warn/error）和格式切换（text/json）
- **配置外部化** — YAML 配置文件 + 环境变量覆盖，不修改代码即可调整运行时参数
- **健康检查** — `/healthz`（存活）、`/readyz`（就绪，含依赖状态和降级报告）
- **Prometheus 指标** — 全面的 WebSocket/Redis/持久化/HTTP 指标，`danmakuflow_` 前缀
- **双实例部署** — Docker Compose 一键启动双实例 + Nginx 负载均衡 + Redis + MySQL + Prometheus + Grafana
- **跨实例验证** — 集成测试自动验证跨实例消息投递、房间隔离、Nginx WebSocket 代理
- **信封协议** — 统一 JSON 信封格式（broadcast/ack/error/history），客户端按 type 分发
- **断线重连** — 客户端指数退避重连 + 游标历史补偿（at-least-once，客户端去重）
- **Redis 自动重连** — 订阅断开后指数退避自动重连，无需人工干预

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
| 指标 | Prometheus client_golang v1.20 | WebSocket/Redis/持久化/HTTP 指标 |
| 监控 | Prometheus + Grafana | 指标采集和可视化面板 |
| 代理 | Nginx 1.27 | WebSocket 负载均衡 |
| 前端 | 原生 HTML + JavaScript | 暗色主题 B站风格弹幕渲染 |
| 配置 | YAML + 环境变量 | 运行时外部化配置 |

## 快速开始

### 单实例运行

```bash
git clone https://github.com/1012-Penn/DanmakuFlow.git
cd DanmakuFlow
go mod tidy
go run .
```

浏览器访问 `http://localhost:8080`。

### 使用环境变量

所有配置均可通过环境变量覆盖：

```bash
SERVER_PORT=9090 LOG_LEVEL=debug LOG_FORMAT=text go run .
```

支持的环境变量：`SERVER_PORT`、`STORE_DSN`、`REDIS_ADDR`、`REDIS_INSTANCE_ID_PREFIX`、`LOG_LEVEL`、`LOG_FORMAT`。

### 启用 MySQL 持久化

```bash
# 启动 MySQL（Docker）
docker compose up -d mysql

# 编辑 config.yaml → store.dsn 填入你的数据库连接串

# 运行（自动检测 MySQL，无 MySQL 时使用内存存储）
go run .
```

### 启用 Redis 跨实例广播

```bash
# 启动 Redis（Docker）
docker compose up -d redis

# 编辑 config.yaml → redis.addr
#   运行时会自动生成唯一实例 ID（hostname-PID-UUID），无需手动配置
go run .
```

### 依赖组件

MySQL 和 Redis 为可选，系统在不配置时自动降级。

### 双实例一键启动

```bash
docker compose up --build -d
```

启动后会得到：

| 组件 | 地址 | 说明 |
|------|------|------|
| Nginx | http://localhost:8080 | 统一入口 |
| danmaku-a | http://localhost:8081 | 实例 A |
| danmaku-b | http://localhost:8082 | 实例 B |
| MySQL | localhost:3307 | 数据库 |
| Redis | localhost:6380 | 缓存/消息 |
| Prometheus | http://localhost:9090 | 指标采集 |
| Grafana | http://localhost:3000 | 监控面板 |

### 双实例验证

```bash
# 一键启动并验证
bash scripts/integration-test.sh

# 仅运行验证（假设系统已在运行）
bash scripts/integration-test.sh --skip-build

# 停止服务
bash scripts/integration-test.sh --down
```

预期输出：

```
✅ danmaku-a (instance=danmaku-a-1234-abcd1234)
✅ danmaku-b (instance=danmaku-b-5678-efgh5678)
✅ A→B 跨实例投递成功
✅ 房间隔离正常
✅ Nginx WebSocket 代理正常
✅ 所有集成测试通过！
```

## 健康检查

| 端点 | 说明 | 响应示例 |
|------|------|---------|
| `GET /healthz` | 进程存活检查 | `{"status":"ok","instance_id":"..."}` |
| `GET /readyz` | 就绪检查（含依赖状态） | `{"status":"ok","instance_id":"...","dependencies":{"mysql":"up","redis":"up"}}` |

`readyz` 中 Redis 故障不会导致整体状态变为 `down`，仅标记为 `degraded`（允许降级）。

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
```

## 指标

| 端点 | 说明 |
|------|------|
| `GET /metrics` | Prometheus 指标 |

所有指标前缀为 `danmakuflow_`。指标列表：

### WebSocket
| 指标名 | 类型 | 说明 |
|--------|------|------|
| `danmakuflow_ws_connections` | Gauge | 当前 WebSocket 连接数 |
| `danmakuflow_ws_active_rooms` | Gauge | 当前活跃房间数 |
| `danmakuflow_ws_conn_total` | Counter | 连接建立总数 |
| `danmakuflow_ws_conn_rejects_total` | CounterVec(reason) | 连接拒绝总数 |
| `danmakuflow_ws_slow_kicks_total` | Counter | 慢客户端淘汰总数 |
| `danmakuflow_ws_messages_received_total` | Counter | WebSocket 接收消息总数 |
| `danmakuflow_ws_client_deliveries_total` | Counter | 客户端投递总数 |
| `danmakuflow_ws_broadcast_drops_total` | Counter | 房间广播丢弃总数 |

### Redis
| 指标名 | 类型 | 说明 |
|--------|------|------|
| `danmakuflow_redis_publish_total` | CounterVec(result) | Redis Publish 总数（success/error/dropped）|
| `danmakuflow_redis_publish_latency_seconds` | Histogram | Redis Publish 延迟 |
| `danmakuflow_redis_publish_queue_len` | Gauge | 发布队列当前长度 |
| `danmakuflow_redis_publish_queue_cap` | Gauge | 发布队列容量 |
| `danmakuflow_redis_sub_status` | Gauge | 订阅连接状态 |
| `danmakuflow_redis_sub_events_total` | Counter | 订阅重连/退出次数 |

### 持久化
| 指标名 | 类型 | 说明 |
|--------|------|------|
| `danmakuflow_persist_async_chan_len` | Gauge | 异步写入队列长度 |
| `danmakuflow_persist_async_chan_cap` | Gauge | 异步写入队列容量 |
| `danmakuflow_persist_writes_total` | CounterVec(result) | Store 写入总数（success/drop）|
| `danmakuflow_persist_write_latency_seconds` | Histogram | Store 写入延迟 |

### HTTP
| 指标名 | 类型 | 说明 |
|--------|------|------|
| `danmakuflow_http_requests_total` | CounterVec(method,route,status) | HTTP 请求总数 |
| `danmakuflow_http_request_duration_seconds` | HistogramVec(method,route) | HTTP 请求延迟 |

```bash
curl http://localhost:8080/metrics | grep danmakuflow_
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

### WebSocket 消息协议（protocol_version=1）

所有 WebSocket 通信使用统一的 JSON 信封格式：

```json
{
  "type": "消息类型",
  "payload": { /* 业务数据 */ }
}
```

#### 客户端 → 服务端

**发送弹幕**（`type: "danmaku"`）：

```json
{
  "type": "danmaku",
  "payload": {
    "content": "Hello",
    "user_id": "user123",
    "color": "#ff0000",
    "type": "scroll",
    "font_size": 25
  }
}
```

也兼容旧版裸格式（直接发送 `{"content":"...","user_id":"..."}`）。

#### 服务端 → 客户端

| type | 发送时机 | payload |
|------|---------|---------|
| `broadcast` | 有弹幕创建时，广播给房间所有人 | `Danmaku` 对象 |
| `ack` | 弹幕处理成功，单播给发送者 | `{request_id, message_id, ok, persistence}` |
| `error` | 弹幕处理失败，单播给发送者 | `{code, message}` |
| `history` | 断线重连时，补偿丢失的消息 | `{danmaku: [...], room_id: "..."}` |

**广播示例**（客户端收到后渲染 `payload.content`）：

```json
{
  "type": "broadcast",
  "payload": {
    "id": "uuid",
    "content": "前方高能",
    "color": "#ffffff",
    "type": "scroll",
    "font_size": 25,
    "room_id": "liveroom_001",
    "timestamp": "2026-07-13T12:00:00Z",
    "user_id": "u001"
  }
}
```

### 断线重连与历史补偿

服务端支持断线重连时的历史消息补偿（at-least-once 语义，客户端负责去重）：

1. 客户端在断线前记录最后收到的消息 `id` 和 `timestamp`
2. 重连时在 WebSocket URL 中携带 `since_time` 和 `last_message_id`：
   ```
   ws://localhost:8080/ws?room_id=liveroom_001&since_time=2026-07-13T12:00:00Z&last_message_id=xxx
   ```
3. 服务端查询游标之后的消息，以 `type=history` 信封返回
4. 客户端遍历 `payload.danmaku` 数组，按 `id` 去重后渲染

### 爆发测试页面

浏览器打开 `http://localhost:8080/burst`，可配置弹幕数量、发送间隔和弹幕类型，一键压测。

## 性能基准

在 WSL2 环境下（单机 localhost，MemoryStore，无 Redis，无 MySQL）的基准测试结果。
压测客户端与服务端同机，存在资源竞争；数据供参考，不代表独立部署时的上限。

| 场景 | 连接数 | 弹幕发送 QPS | 客户端投递吞吐 | 错误数 | 备注 |
|------|--------|-------------|---------------|:------:|------|
| 场景 | 连接数 | 弹幕发送 QPS | 客户端投递吞吐 | 丢消息率 | 备注 |
|------|--------|-------------|---------------|:--------:|------|
| 轻负载 | 100 | 20 msg/s | 2,000 msg/s | 0% | 所有客户端在同一房间，广播因子 100 |
| 中负载 | 500 | 200 msg/s | ~100,000 msg/s | 0% | 广播因子 500，稳态无丢失 |
| HTTP API | — | ~1,000 QPS | — | — | 100% 成功率 |
| 高负载广播 | 1,000 | 900 msg/s | ~850,000 msg/s | <5% | 广播因子 ~950，丢消息集中 ramp-up 阶段 |
| 连接伸缩 | 2,000 | 0（静默） | — | — | 验证连接管理能力 |

> **弹幕发送 QPS**：所有 talker 客户端每秒发送的弹幕总数（稳态窗口）。
> **客户端投递吞吐**：所有客户端每秒收到的广播消息总数（= 发送 QPS × 在线连接数）。
> 丢消息率基于精确的 `(listener_id, message_id)` 追踪计算，含去重和遗漏检测。
> 丢消息在 ramp-up 阶段偏高，稳态下显著降低。
> 压测工具见 `cmd/benchmark/`，支持参数化 WebSocket / HTTP 压测。

## 一致性与可靠性边界

### Redis Pub/Sub（at-most-once）

Redis Pub/Sub 的投递语义是 **at-most-once**（最多一次）：

- **发布队列满**：本地广播不受影响，跨实例广播被丢弃（有界队列，满则丢弃）
- **Redis 服务重启**：订阅连接断开期间的消息全部丢失。Pub/Sub 无消息积压能力
- **运行时连接断开**：订阅 goroutine 自动按指数退避（100ms~30s, ±20% jitter）重连，重连成功后恢复正常接收
- **运行时发布失败**：后台发布 worker 失败时仅记录日志，不重试
- **启动时连接失败**：系统自动降级为纯本地广播，不影响 HTTP/WS 服务

Redis 在系统中的职责：**跨实例消息广播的加速通道**。不是可靠消息队列，不保证消息不丢。

### 异步 MySQL 写入

- **异步通道满**：`danmakuChan` 满时新弹幕被丢弃，本地广播已在前序步骤完成
- **关闭时排空超时**：`Shutdown` 的 drain 超时后未写入的弹幕丢失
- MySQL 自身故障由 GORM 连接池处理，应用层不实现重试逻辑

以下场景**不会**丢失：

- Redis 发布失败 → 仅影响跨实例投递，本地客户端已收到广播、弹幕已写入存储
- 异步通道积压 → channel 写不阻塞上游，内存允许范围内不丢失

### 本地广播

单机内广播走 Go channel，只有显式的「慢客户端踢出」会导致特定客户端收不到消息。
踢出时该消息对已踢客户端丢失，但存储和跨实例广播不受影响。

## 当前限制

- **Redis Pub/Sub at-most-once**：即使有自动重连，断开期间的消息仍会丢失，Pub/Sub 无消息积压能力
- **异步 MySQL 写入为最终一致**：channel 满时丢弃，宕机时未消费的弹幕丢失
- **尚未接入 Kafka**：当前没有可靠消息队列，跨实例广播和持久化均不做 at-least-once 保证
- **单 Room Fan-out 为 O(N)**：广播给房间内所有客户端使用线性遍历，大房间（>10000 连接）可能成为瓶颈

## 项目结构

```
DanmakuFlow/
├── main.go                     # 入口：组件组装与优雅关闭
├── config.yaml                 # 外部配置文件
├── config.yaml.example         # 配置模板
├── Dockerfile                  # 多阶段 Docker 构建
├── .dockerignore               # Docker 构建忽略
├── docker-compose.yml          # 双实例 + 可观测性部署
├── nginx.conf                  # Nginx 负载均衡 + WebSocket 代理
├── config/
│   ├── config.go               # 配置加载与默认值（含环境变量覆盖）
│   └── config_test.go          # 配置测试
├── model/
│   ├── danmaku.go              # 弹幕数据模型
│   └── message.go              # WebSocket 信封协议（ACK/Error/History/Envelope）
├── metrics/
│   ├── metrics.go              # Prometheus 指标定义
│   └── metrics_test.go         # 指标测试
├── store/
│   ├── store.go                # 存储接口 + MemoryStore 实现
│   ├── mysql.go                # MySQL/GORM 实现
│   └── store_test.go           # 存储测试
├── service/
│   ├── danmaku.go              # 业务逻辑层（频率限制、校验、异步写入）
│   └── service_test.go         # 业务逻辑测试
├── handler/
│   ├── danmaku.go              # HTTP 处理器（含健康检查和指标中间件）
│   └── danmaku_test.go         # HTTP 处理器测试
├── websocket/
│   ├── hub.go                  # 房间管理器 + 广播 + Redis 订阅/发布
│   ├── client.go               # 连接读写泵 + 心跳 + 幂等连接计数释放
│   ├── handler.go              # WebSocket 升级处理器（非全局 Upgrader）
│   └── websocket_test.go       # Upgrader/连接计数/发布队列测试
├── redisclient/
│   ├── pubsub.go               # Redis Pub/Sub 封装 + 实例 ID 生成（含自动重连）
│   └── pubsub_test.go          # Message 序列化/实例 ID 测试
├── cmd/benchmark/              # WebSocket + HTTP 压测工具
├── scripts/
│   ├── integration_test.go     # 双实例集成测试
│   └── integration-test.sh     # 一键验证脚本
├── prometheus/
│   └── prometheus.yml          # Prometheus 抓取配置
├── grafana/
│   ├── datasources/            # Grafana 自动配置数据源
│   └── dashboards/             # DanmakuFlow Overview 仪表盘
├── templates/
│   ├── index.html              # 弹幕页面（4 种动画类型）
│   └── burst.html              # 爆发测试页面
└── danmaku_test.go             # 集成测试
```

## 配置说明

系统通过 `config.yaml` 外部化配置，所有字段均有内置默认值，未配置时直接运行。
可通过环境变量覆盖（优先级高于 YAML）：

| 环境变量 | 对应配置 | 说明 |
|---------|---------|------|
| `SERVER_PORT` | `server.port` | 服务端口 |
| `STORE_DSN` | `store.dsn` | MySQL DSN |
| `REDIS_ADDR` | `redis.addr` | Redis 地址 |
| `REDIS_INSTANCE_ID_PREFIX` | `redis.instance_id` | 实例 ID 前缀 |
| `LOG_LEVEL` | `log.level` | 日志级别 |
| `LOG_FORMAT` | `log.format` | 日志格式 |

详细配置项：

```yaml
server:
  port: 8080

websocket:
  write_wait_seconds: 10       # 写超时（秒）
  pong_wait_seconds: 60        # Pong 超时（秒）
  max_message_size: 512        # 单条消息最大字节数
  broadcast_buffer_size: 256   # Room.broadcast 通道缓冲区大小
  send_buffer_size: 256        # Client.send 通道缓冲区大小
  max_conn_per_room: 0         # 每房间最大连接数（0=不限制）
  max_conn_per_ip: 0           # 每 IP 最大连接数（0=不限制）
  allowed_origins: []          # 允许的 Origin（空=不校验）

store:
  dsn: ""                      # MySQL DSN（空=使用内存存储）
  async_buffer_size: 1024      # 异步写入通道缓冲区大小（0=同步写）

redis:
  addr: ""                     # Redis 地址（空=不使用）
  instance_id: ""              # 实例标识前缀（空=自动生成 hostname-PID-UUID）

rate_limit:
  messages_per_sec: 0          # 每用户每秒可发送弹幕数（0=不限制）
```

## 开发

### 运行测试

```bash
go test ./...
go vet ./...
go test -race ./...
```

### 格式化

```bash
gofmt -w .
```

### 压测

```bash
# 先启动服务
go run .

# 基础 WebSocket 压测
go run ./cmd/benchmark -c 1000 -r 1s -room test_room

# HTTP API 压测
go run ./cmd/benchmark -c 200 -r 500ms -http-only
```

### 性能分析（pprof）

启用 pprof（编辑 `config.yaml`）：

```yaml
pprof:
  enabled: true
```

或在启动时使用环境变量 `PPROF_ENABLED=true`。启用后即可抓取性能数据：

```bash
# 抓取 CPU profile（30 秒）
curl -o cpu.prof http://localhost:8080/debug/pprof/profile?seconds=30

# 抓取堆内存
curl -o heap.prof http://localhost:8080/debug/pprof/heap

# 抓取 goroutine 堆栈
curl -o goroutine.prof http://localhost:8080/debug/pprof/goroutine

# 可视化分析
go tool pprof -http=:9091 heap.prof
```

pprof 默认不通过 Nginx 代理，需要直接访问实例端口（如 `localhost:8081`）。
生产环境建议关闭 pprof。

## 许可证

MIT
