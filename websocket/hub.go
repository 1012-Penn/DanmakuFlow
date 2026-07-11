package websocket

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// 以下是默认配置常量，NewHub() 使用它们创建默认 Hub。
// 调用 NewHubWithConfig(*Config) 可传入自定义配置覆盖。
const (
	defaultWriteWaitSeconds    = 10
	defaultPongWaitSeconds     = 60
	defaultMaxMessageSize      = 512
	defaultBroadcastBufferSize = 256
	defaultSendBufferSize      = 256
)

// Config 存放 WebSocket 层所有可配置参数。
// 由 config 包提供值，也可手动构造。
type Config struct {
	WriteWaitSeconds    int // 写超时（秒）
	PongWaitSeconds     int // 等 Pong 超时（秒）
	MaxMessageSize      int // 单条消息最大字节数
	BroadcastBufferSize int // Room.broadcast 通道缓冲区大小
	SendBufferSize      int // Client.send 通道缓冲区大小
}

// Hub 是房间管理器，持有 map[string]*Room。
// rooms map 用 RWMutex 保护，支持并发读写。

// Room表示一个独立的直播间
// 每个Room有自己的客户端池子和广播通道,房间之间互不干扰
type Room struct {
	//房间唯一标识, 比如"liveroom_001"
	ID string

	//这个房间里的所有客户端
	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	hub        *Hub          // 所属 Hub，用于空房间时通知清理
	count      atomic.Int64  // 在线人数，原子操作支持外部安全读取
	stop       chan struct{} // 关闭此 channel 让 Run() 退出（优雅关闭用）
}

// NewRoom创建一个新房间
// roomID是房间标识 由上层调用者传入(从URL参数解析)
func NewRoom(roomID string, hub *Hub) *Room {
	return &Room{
		ID:         roomID,
		hub:        hub,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, hub.cfg.BroadcastBufferSize),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		stop:       make(chan struct{}),
	}
}

// OnlineCount 返回当前房间在线人数。
func (r *Room) OnlineCount() int {
	return int(r.count.Load())
}

func (r *Room) Run() {
	for {
		select {
		case client := <-r.register:
			r.clients[client] = true
			r.count.Add(1)
			slog.Debug("WS 客户端加入房间",
				"room_id", r.ID,
				"online", r.count.Load(),
			)

		case client := <-r.unregister:
			if _, ok := r.clients[client]; ok {
				delete(r.clients, client)
				r.count.Add(-1)
				close(client.send)
				online := r.count.Load()
				slog.Debug("WS 客户端离开房间",
					"room_id", r.ID,
					"online", online,
				)
				if online == 0 {
					r.hub.RemoveRoom(r.ID)
					slog.Info("房间已空，已清除", "room_id", r.ID)
				}
			}

		case msg := <-r.broadcast:
			for client := range r.clients {
				select {
				case client.send <- msg:
				default:
					//客户端缓冲区满了, 踢掉
					slog.Warn("慢客户端被踢出",
						"room_id", r.ID,
						"remote", client.conn.RemoteAddr().String(),
					)
					close(client.send)
					delete(r.clients, client)
					r.count.Add(-1)
				}
			}
			if len(r.clients) == 0 {
				r.hub.RemoveRoom(r.ID)
				slog.Info("广播后房间已空，已清除", "room_id", r.ID)
			}

		case <-r.stop:
			// 优雅关闭：给所有客户端发关闭帧，然后退出
			for client := range r.clients {
				client.conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure,
						"server shutting down"))
				delete(r.clients, client)
				close(client.send)
			}
			r.count.Store(0)
			slog.Info("房间已关闭", "room_id", r.ID)
			return
		}
	}
}

// Broadcast 向房间内所有客户端广播消息。
// 公开方法，供 handler 层调用。
func (r *Room) Broadcast(msg []byte) {
	r.broadcast <- msg
}

// Hub是一个"房间管理器"
// 不直接持有客户端,而是持有map[string]*Room
type Hub struct {
	rooms        map[string]*Room
	mu           sync.RWMutex //保护rooms map的并发访问
	cfg          Config       // 配置（创建后不可变）
	shutdownOnce sync.Once    // 保证 Shutdown 只执行一次
}

// NewHub 使用默认配置创建 Hub。
// 等价于 NewHubWithConfig(默认值)。
func NewHub() *Hub {
	return NewHubWithConfig(Config{
		WriteWaitSeconds:    defaultWriteWaitSeconds,
		PongWaitSeconds:     defaultPongWaitSeconds,
		MaxMessageSize:      defaultMaxMessageSize,
		BroadcastBufferSize: defaultBroadcastBufferSize,
		SendBufferSize:      defaultSendBufferSize,
	})
}

// NewHubWithConfig 使用指定配置创建 Hub。
func NewHubWithConfig(cfg Config) *Hub {
	return &Hub{
		rooms: make(map[string]*Room),
		cfg:   cfg,
	}
}

// 以下是 Client 用到的便利方法，从 cfg 中取值转换 time.Duration。

func (h *Hub) writeWait() time.Duration {
	return time.Duration(h.cfg.WriteWaitSeconds) * time.Second
}

func (h *Hub) pongWait() time.Duration {
	return time.Duration(h.cfg.PongWaitSeconds) * time.Second
}

// pingPeriod 是 pongWait 的 90%（留有余量）。
func (h *Hub) pingPeriod() time.Duration {
	return h.pongWait() * 9 / 10
}

func (h *Hub) maxMessageSize() int64 {
	return int64(h.cfg.MaxMessageSize)
}

func (h *Hub) sendBufferSize() int {
	return h.cfg.SendBufferSize
}

func (h *Hub) GetOrCreateRoom(roomID string) *Room {
	h.mu.Lock()
	defer h.mu.Unlock()

	if room, ok := h.rooms[roomID]; ok {
		return room
	}

	//房间不存在, 新建并且启动它的Run goroutine
	room := NewRoom(roomID, h)
	go room.Run()
	h.rooms[roomID] = room
	slog.Info("创建新房间", "room_id", roomID)
	return room
}

// RemoveRoom 安全地移除一个空房间（清理用）。
func (h *Hub) RemoveRoom(roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.rooms, roomID)
}

// Shutdown 优雅关闭所有房间。
// 向每个房间发送停止信号 → 房间给所有客户端发关闭帧 → 退出 Room.Run()。
// sync.Once 保证即使多次调用也只执行一次。
func (h *Hub) Shutdown() {
	h.shutdownOnce.Do(func() {
		slog.Info("WebSocket Hub 开始关闭...")
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, room := range h.rooms {
			close(room.stop)
		}
		h.rooms = make(map[string]*Room)
		slog.Info("WebSocket Hub 已关闭")
	})
}

// ActiveRooms 返回当前所有活跃房间的 ID 列表。
// 将来直播主页要用。
func (h *Hub) ActiveRooms() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	ids := make([]string, 0, len(h.rooms))
	for id := range h.rooms {
		ids = append(ids, id)
	}
	return ids
}
