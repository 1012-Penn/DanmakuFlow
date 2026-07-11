# DanmakuFlow — 实时弹幕系统

A real-time bullet-screen (danmaku) system built with **Go**, **Gin**, and **gorilla/websocket**.
Supports Bilibili-style danmaku types (scroll / top / bottom / reverse) with room isolation,
configurable storage backends, and structured logging.

Built for learning Go Web development from the ground up — see [Learning Path](#learning-path).

## Features

- **RESTful API** — Create and list danmaku messages via HTTP
- **Real-time broadcasting** — WebSocket pushes new messages to all connected clients in the same room
- **Room isolation** — Each room has its own goroutine + client pool, fully isolated broadcast domains
- **Bilibili-style danmaku types** — scroll (default), top, bottom, reverse + configurable font size
- **Dual storage backends** — In-memory (development) or MySQL + GORM (production), switchable via config
- **Async write** — Channel-based producer-consumer pattern prevents DB writes from blocking broadcast
- **Heartbeat mechanism** — Ping/Pong keeps dead connections cleaned up
- **Structured logging** — `log/slog` with configurable level (debug/info/warn/error) and format (text/json)
- **Graceful shutdown** — Handles SIGINT/SIGTERM, drains WebSocket connections, closes DB cleanly
- **Burst test page** — `/burst` for stress-testing with configurable volume and interval
- **Clean layering** — Model → Store → Service → Handler → WebSocket, each with single responsibility

## Tech Stack

| Layer         | Technology                    |
|---------------|-------------------------------|
| Language      | Go 1.26                       |
| HTTP framework| Gin v1.12                     |
| WebSocket     | gorilla/websocket v1.5        |
| Database      | MySQL 8.0 (optional)          |
| ORM           | GORM v2                       |
| Logging       | log/slog (standard library)   |
| Frontend      | Vanilla HTML + JavaScript     |
| Config        | YAML                          |

## Quick Start

```bash
git clone https://github.com/1012-Penn/danmaku.git
cd danmaku
go mod tidy
go run .
```

Open `http://localhost:8080` in your browser.

### With MySQL (optional)

```bash
# 1. Start MySQL (Docker or local)
docker compose up -d

# 2. Edit config.yaml to set your DSN
#    dsn: "user:password@tcp(127.0.0.1:3306)/danmaku?charset=utf8mb4&parseTime=True&loc=Local"

# 3. Run (auto-detects MySQL)
go run .
```

Without MySQL, falls back to in-memory storage automatically.

### Test with curl

```bash
# Send a danmaku (default room)
curl -X POST http://localhost:8080/api/room/liveroom_001/danmaku \
  -H "Content-Type: application/json" \
  -d '{"content":"Hello Danmaku","user_id":"u001"}'

# Send a danmaku to a specific room
curl -X POST http://localhost:8080/api/room/liveroom_002/danmaku \
  -H "Content-Type: application/json" \
  -d '{"content":"Room 2 here","user_id":"u002"}'

# List recent danmakus (room)
curl http://localhost:8080/api/room/liveroom_001/danmaku?limit=20

# Burst test page
# Open http://localhost:8080/burst in your browser

# WebSocket endpoint (for programmatic testing)
# ws://localhost:8080/ws?room_id=liveroom_001
```

## Project Structure

```
danmaku/
├── main.go                 # Entry point — wiring, graceful shutdown
├── config.yaml             # External config (gitignored — see config.yaml.example)
├── config.yaml.example     # Example config with placeholder values
├── config/
│   └── config.go           # Config struct, YAML loading, defaults
├── model/
│   └── danmaku.go          # Danmaku struct (Bilibili-style: type, font_size)
├── store/
│   ├── store.go            # Store interface
│   ├── memory.go           # In-memory implementation (MemoryStore)
│   └── mysql.go            # MySQL/GORM implementation (MySQLStore)
├── service/
│   └── danmaku.go          # Business logic (create + broadcast, async write)
├── handler/
│   └── danmaku.go          # HTTP handlers (Create, List, room routes)
├── websocket/
│   ├── hub.go              # Room manager, Config, hub.Run()
│   ├── client.go           # Per-connection read/write pumps + heartbeat
│   └── handler.go          # WebSocket upgrade handler
├── templates/
│   ├── index.html          # Danmaku viewer (dark theme, 4 animation types)
│   └── burst.html          # Stress-test tool
└── danmaku_test.go         # Integration tests
```

## Learning Path

This project is built step by step. Each stage introduces a new concept:

| #  | Stage                                        | Status |
|----|----------------------------------------------|--------|
| 1  | Gin HTTP server + routing                    | ✅ Done |
| 2  | Data model with struct tags                  | ✅ Done |
| 3  | In-memory store with concurrent safety       | ✅ Done |
| 4  | Handler layer with dependency injection      | ✅ Done |
| 5  | WebSocket upgrade & message read/write       | ✅ Done |
| 6  | Hub broadcast pattern with channels          | ✅ Done |
| 7  | Per-client goroutines + Ping/Pong heartbeat  | ✅ Done |
| 8  | Docker multi-stage build                     | ✅ Done |
| 9  | Room isolation                               | ✅ Done |
| 10 | Config externalization (YAML)                | ✅ Done |
| 11 | Service layer (unified HTTP + WS flow)       | ✅ Done |
| 12 | MySQL + GORM + async write channel           | ✅ Done |
| 13 | Structured logging (log/slog)                | ✅ Done |
| 14 | Graceful shutdown                            | ✅ Done |

## License

MIT
