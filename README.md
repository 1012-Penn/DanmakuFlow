# Danmaku — 实时弹幕系统

A real-time bullet-screen (danmaku) system built with **Go**, **Gin**, and **gorilla/websocket**. Designed for learning Go Web development from the ground up.

## Features

- **RESTful API** — Create and list danmaku messages via HTTP
- **Real-time broadcasting** — WebSocket pushes new messages to all connected clients
- **Concurrent-safe storage** — In-memory store protected by `sync.RWMutex`
- **Heartbeat mechanism** — Ping/Pong keeps dead connections cleaned up
- **Clean layering** — Model → Store → Handler → WebSocket, each with single responsibility

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Language | Go 1.26 |
| HTTP framework | Gin v1.12 |
| WebSocket | gorilla/websocket v1.5 |
| Frontend | Vanilla HTML + JavaScript |

## Quick Start

```bash
git clone https://github.com/1012-Penn/danmaku.git
cd danmaku
go mod tidy
go run .
```

Open `http://localhost:8080` in your browser.

### Test with curl

```bash
# Send a danmaku
curl -X POST http://localhost:8080/api/danmaku \
  -H "Content-Type: application/json" \
  -d '{"content":"Hello Danmaku","user_id":"u001"}'

# List recent danmakus
curl http://localhost:8080/api/danmaku
```

## Project Structure

```
danmaku/
├── main.go                 # Entry point — wiring everything together
├── model/
│   └── danmaku.go          # Danmaku struct definition
├── store/
│   └── store.go            # Store interface + MemoryStore implementation
├── handler/
│   └── danmaku.go          # HTTP handlers (Create, List, route registration)
├── websocket/
│   ├── hub.go              # Connection hub — manages clients and broadcasts
│   ├── client.go           # Per-connection read/write pumps + heartbeat
│   └── handler.go          # WebSocket upgrade handler
└── templates/
    └── index.html          # Minimal frontend (dark theme, real-time display)
```

## Learning Path

This project is built step by step. Each stage introduces a new concept:

1. Gin HTTP server + routing
2. Data model with struct tags
3. In-memory store with concurrent safety
4. Handler layer with dependency injection
5. WebSocket upgrade & message read/write
6. Hub broadcast pattern with channels
7. Per-client goroutines + Ping/Pong heartbeat

## License

MIT
