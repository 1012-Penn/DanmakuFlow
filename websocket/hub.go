package websocket

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/1012-Penn/DanmakuFlow/redisclient"
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
					r.hub.RemoveRoomIfSame(r.ID, r)
					slog.Info("房间已空，已关闭", "room_id", r.ID)
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
				// 广播后无人接收，关闭房间
				r.hub.RemoveRoomIfSame(r.ID, r)
				slog.Info("广播后房间已空，已关闭", "room_id", r.ID)
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
// 非阻塞实现：channel 满了直接丢弃，防止 HTTP 请求被广播拖慢。
// 公开方法，供 handler/service 层调用。
func (r *Room) Broadcast(msg []byte) {
	select {
	case r.broadcast <- msg:
	default:
		slog.Warn("广播通道已满，丢弃弹幕",
			"room_id", r.ID,
			"buf_size", cap(r.broadcast),
		)
	}
}

// Hub是一个"房间管理器"
// 不直接持有客户端,而是持有map[string]*Room
type Hub struct {
	rooms        map[string]*Room
	mu           sync.RWMutex //保护rooms map的并发访问
	cfg          Config       // 配置（创建后不可变）
	shutdownOnce sync.Once    // 保证 Shutdown 只执行一次

	redisClient *redisclient.Client // Redis 跨实例广播客户端，nil = 不使用
	redisCancel context.CancelFunc  // 用于停止 redisSubscribeLoop goroutine
	wg          sync.WaitGroup      // 等待后台 goroutine 退出（Redis 订阅）
}

// NewHub 使用默认配置创建 Hub。
// 等价于 NewHubWithConfig(默认配置, nil)。
func NewHub() *Hub {
	return NewHubWithConfig(Config{
		WriteWaitSeconds:    defaultWriteWaitSeconds,
		PongWaitSeconds:     defaultPongWaitSeconds,
		MaxMessageSize:      defaultMaxMessageSize,
		BroadcastBufferSize: defaultBroadcastBufferSize,
		SendBufferSize:      defaultSendBufferSize,
	}, nil)
}

// NewHubWithConfig 使用指定配置创建 Hub。
// redisClient 为 nil 时回退到纯内存广播（向后兼容）。
func NewHubWithConfig(cfg Config, redisClient *redisclient.Client) *Hub {
	h := &Hub{
		rooms:       make(map[string]*Room),
		cfg:         cfg,
		redisClient: redisClient,
	}

	// 如果有 Redis 客户端，启动跨实例广播订阅循环
	if redisClient != nil {
		ctx, cancel := context.WithCancel(context.Background())
		h.redisCancel = cancel
		h.wg.Add(1)
		go h.redisSubscribeLoop(ctx)
	}

	return h
}

// BroadcastToRoom 向指定房间广播消息，同时通过 Redis 跨实例广播。
// 这是 service 层调用的统一入口——替代直接调用 GetOrCreateRoom(..).Broadcast()。
//
// 流程：
//  1. 本地广播（本机内存的客户端）
//  2. 如果有 Redis 配置，也发布一份到 Redis（其他实例会收到并广播）
func (h *Hub) BroadcastToRoom(roomID string, data []byte) {
	// 1. 本地广播（与之前一样）
	room := h.GetOrCreateRoom(roomID)
	room.Broadcast(data)

	// 2. 跨实例广播（通过 Redis Pub/Sub，2 秒超时）
	if h.redisClient != nil {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer pubCancel()
		if err := h.redisClient.Publish(pubCtx, roomID, data); err != nil {
			slog.Error("Redis 发布失败",
				"room_id", roomID,
				"error", err,
			)
		}
	}
}

// redisSubscribeLoop 在后台 goroutine 中接收 Redis 跨实例广播消息。
// 收到消息后找到对应的房间，做本地广播。
// ctx 取消时（Hub.Shutdown 时），goroutine 退出。
func (h *Hub) redisSubscribeLoop(ctx context.Context) {
	ch := h.redisClient.Subscribe(ctx)
	defer h.wg.Done()
	for msg := range ch {
		room := h.GetOrCreateRoom(msg.RoomID)
		room.Broadcast(msg.Data)
	}
	slog.Info("Redis 订阅循环已退出")
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

// RemoveRoomIfSame 移除 roomID 对应的房间，前提是它和传入的 room 是同一个对象。
// 防止并发场景：判断为空 → 新客户端同时创建新房间 → 误删新房间。
// 移除后关闭 room.stop，让 Room.Run() goroutine 安全退出。
func (h *Hub) RemoveRoomIfSame(roomID string, room *Room) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.rooms[roomID]; ok && existing == room {
		delete(h.rooms, roomID)
		close(room.stop)
		slog.Debug("房间 goroutine 已退出", "room_id", roomID)
	}
}

// Shutdown 优雅关闭所有房间。
// 先取消 Redis 订阅循环，再关闭房间。
// sync.Once 保证即使多次调用也只执行一次。
func (h *Hub) Shutdown() {
	h.shutdownOnce.Do(func() {
		slog.Info("WebSocket Hub 开始关闭...")

		// 先停止 Redis 订阅循环，不再接收跨实例广播
		if h.redisCancel != nil {
			h.redisCancel()
			h.wg.Wait() // 等待 redisSubscribeLoop goroutine 确实退出
		}

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
