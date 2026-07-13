package websocket

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/model"
	"github.com/1012-Penn/DanmakuFlow/redisclient"
)

const (
	defaultWriteWaitSeconds    = 10
	defaultPongWaitSeconds     = 60
	defaultMaxMessageSize      = 512
	defaultBroadcastBufferSize = 256
	defaultSendBufferSize      = 256
)

// AuthValidator 验证 JWT token 返回 Actor。
// 定义在 websocket 包中，由 service.AuthService 实现。
type AuthValidator interface {
	ValidateToken(tokenString string) (*model.Actor, error)
}

// Config 存放 WebSocket 层所有可配置参数。
type Config struct {
	WriteWaitSeconds    int
	PongWaitSeconds     int
	MaxMessageSize      int
	BroadcastBufferSize int
	SendBufferSize      int
	MaxConnPerRoom      int
	MaxConnPerIP        int
	AllowedOrigins      []string
}

// Room 表示一个独立的直播间。
// clients/broadcast/stop 均可由 Hub 从外部发送信号，
// 但 clients map 的读写只能由 Room.Run goroutine 完成。
type Room struct {
	ID string

	clients    map[*Client]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	hub        *Hub
	count      atomic.Int64
	stop       chan struct{} // 关闭此 channel 让 Run() 退出
	done       chan struct{} // Run() 退出后关闭此 channel
	stopMu     sync.Once     // 确保 stop 只关闭一次
}

func NewRoom(roomID string, hub *Hub) *Room {
	return &Room{
		ID:         roomID,
		hub:        hub,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, hub.cfg.BroadcastBufferSize),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

func (r *Room) OnlineCount() int {
	return int(r.count.Load())
}

// Run 是房间的主事件循环，由独立 goroutine 运行。
// 所有对 r.clients 的读写都在此 goroutine 中完成。
// 退出时关闭 r.done 通知外部。
func (r *Room) Run() {
	defer close(r.done)

	// 停止时排空 broadcast 并清理客户端
	defer func() {
		// 排空剩余的 broadcast 消息
		for {
			select {
			case msg := <-r.broadcast:
				r.broadcastToClients(msg)
			default:
				goto doneDrain
			}
		}
	doneDrain:
		// 关闭所有客户端的 send channel，让 writePump 自然发送关闭帧后退出
		for client := range r.clients {
			delete(r.clients, client)
			client.stop()
		}
		r.count.Store(0)
	}()

	for {
		select {
		case client := <-r.register:
			r.clients[client] = true
			r.count.Add(1)

		case client := <-r.unregister:
			if _, ok := r.clients[client]; ok {
				delete(r.clients, client)
				r.count.Add(-1)
				client.stop()
				if r.count.Load() == 0 {
					r.hub.RemoveRoomIfSame(r.ID, r)
				}
			}

		case msg := <-r.broadcast:
			r.broadcastToClients(msg)

		case <-r.stop:
			return
		}
	}
}

// broadcastToClients 向房间内所有客户端广播消息。
// 慢客户端（send buffer 满）会被断开连接。
func (r *Room) broadcastToClients(msg []byte) {
	for client := range r.clients {
		select {
		case client.send <- msg:
		default:
			// 客户端接收过慢，断开连接
			slog.Warn("客户端接收过慢，断开连接",
				"room_id", r.ID,
				"client_ip", client.clientIP,
			)
			client.stop()
			delete(r.clients, client)
			r.count.Add(-1)
		}
	}
}

// Hub 是房间管理器和 WebSocket 配置中心。
// 负责：创建/查找房间、全局连接配额、跨实例 Redis 广播。
type Hub struct {
	rooms        map[string]*Room
	mu           sync.RWMutex
	cfg          Config
	shutdownOnce sync.Once

	redisClient     *redisclient.Client
	redisConfigured bool
	redisCancel     context.CancelFunc
	wg              sync.WaitGroup

	redisPublishChan chan redisPublishJob
	redisPubDrops    atomic.Int64

	connCounter   map[string]int64
	counterMu     sync.Mutex
	roomConnCount map[string]int64

	checkOrigin      func(r *http.Request) bool
	shuttingDown     atomic.Bool
	authValidator    AuthValidator
	roomStatusGetter model.RoomStatusGetter
}

func NewHub() *Hub {
	return NewHubWithConfig(Config{
		WriteWaitSeconds:    defaultWriteWaitSeconds,
		PongWaitSeconds:     defaultPongWaitSeconds,
		MaxMessageSize:      defaultMaxMessageSize,
		BroadcastBufferSize: defaultBroadcastBufferSize,
		SendBufferSize:      defaultSendBufferSize,
	}, nil)
}

const redisPubChanSize = 256

func NewHubWithConfig(cfg Config, redisClient *redisclient.Client) *Hub {
	h := &Hub{
		rooms:         make(map[string]*Room),
		cfg:           cfg,
		redisClient:   redisClient,
		connCounter:   make(map[string]int64),
		roomConnCount: make(map[string]int64),
		checkOrigin:   buildCheckOrigin(cfg.AllowedOrigins),
	}

	if redisClient != nil {
		h.redisConfigured = true
		h.redisPublishChan = make(chan redisPublishJob, redisPubChanSize)
		ctx, cancel := context.WithCancel(context.Background())
		h.redisCancel = cancel
		h.wg.Add(2)
		go h.redisSubscribeLoop(ctx)
		go h.redisPublishLoop(ctx)
	}

	return h
}

// SetAuthValidator 设置 JWT 验证器，供 WebSocket 握手时认证。
func (h *Hub) SetAuthValidator(v AuthValidator) {
	h.authValidator = v
}

// SetRoomStatusGetter 设置房间状态查询器，供 WebSocket 握手时检查房间状态。
func (h *Hub) SetRoomStatusGetter(g model.RoomStatusGetter) {
	h.roomStatusGetter = g
}

// BroadcastToRoom 向指定房间广播消息，同时通过 Redis 跨实例广播。
func (h *Hub) BroadcastToRoom(roomID string, data []byte) {
	if h.shuttingDown.Load() {
		return
	}

	// 先做本地广播
	if room, ok := h.GetRoom(roomID); ok {
		select {
		case room.broadcast <- data:
		default:
			metrics.WSBroadcastDrops.Inc()
		}
	}

	// 再跨实例广播（如果配置了 Redis）
	if h.redisPublishChan != nil {
		select {
		case h.redisPublishChan <- redisPublishJob{roomID: roomID, data: data}:
		default:
			metrics.WSBroadcastDrops.Inc()
			h.redisPubDrops.Add(1)
		}
	}
}

// GetOrCreateRoom 获取或创建房间。shutdown 时返回 nil。
func (h *Hub) GetOrCreateRoom(roomID string) *Room {
	if h.shuttingDown.Load() {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	if room, ok := h.rooms[roomID]; ok {
		return room
	}

	room := NewRoom(roomID, h)
	h.rooms[roomID] = room
	metrics.WSActiveRooms.Set(float64(len(h.rooms)))
	go room.Run()
	return room
}

// RemoveRoomIfSame 如果指定房间的指针与存储的一致则删除它。
func (h *Hub) RemoveRoomIfSame(roomID string, room *Room) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.rooms[roomID]; ok && existing == room {
		delete(h.rooms, roomID)
		metrics.WSActiveRooms.Set(float64(len(h.rooms)))
	}
}

// GetRoom 获取房间，返回房间指针和是否存在。
func (h *Hub) GetRoom(roomID string) (*Room, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	room, ok := h.rooms[roomID]
	return room, ok
}

// RoomCount 返回当前活跃房间数。
func (h *Hub) RoomCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms)
}

func (h *Hub) HasRedisConfig() bool {
	return h.redisConfigured
}

func (h *Hub) MarkRedisConfigured() {
	h.redisConfigured = true
}

func (h *Hub) PingRedis() bool {
	if h.redisClient == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return h.redisClient.Ping(ctx) == nil
}

func (h *Hub) writeWait() time.Duration {
	return time.Duration(h.cfg.WriteWaitSeconds) * time.Second
}

func (h *Hub) pongWait() time.Duration {
	return time.Duration(h.cfg.PongWaitSeconds) * time.Second
}

func (h *Hub) pingPeriod() time.Duration {
	return (time.Duration(h.cfg.PongWaitSeconds) * 9 / 10) * time.Second
}

func (h *Hub) maxMessageSize() int64 {
	if h.cfg.MaxMessageSize <= 0 {
		return defaultMaxMessageSize
	}
	return int64(h.cfg.MaxMessageSize)
}

func (h *Hub) sendBufferSize() int {
	if h.cfg.SendBufferSize <= 0 {
		return defaultSendBufferSize
	}
	return h.cfg.SendBufferSize
}

// TryAcquireConn 尝试获取连接配额（IP 级别 + 房间级别）。
// 返回一个 release 函数，调用后释放配额。
// 注意：返回的 release 函数可能为 nil（配额为 0 时）。
func (h *Hub) TryAcquireConn(ip string, roomID string) (release func(), ok bool) {
	h.counterMu.Lock()
	defer h.counterMu.Unlock()

	// IP 级别限制
	if h.cfg.MaxConnPerIP > 0 {
		if h.connCounter[ip] >= int64(h.cfg.MaxConnPerIP) {
			metrics.WSConnRejects.WithLabelValues("per_ip").Inc()
			return nil, false
		}
	}

	// 房间级别限制
	if h.cfg.MaxConnPerRoom > 0 {
		if h.roomConnCount[roomID] >= int64(h.cfg.MaxConnPerRoom) {
			metrics.WSConnRejects.WithLabelValues("per_room").Inc()
			return nil, false
		}
	}

	h.connCounter[ip]++
	h.roomConnCount[roomID]++
	metrics.WSConnections.Inc()
	metrics.WSConnTotal.Inc()

	return func() {
		h.counterMu.Lock()
		defer h.counterMu.Unlock()
		h.connCounter[ip]--
		if h.connCounter[ip] <= 0 {
			delete(h.connCounter, ip)
		}
		h.roomConnCount[roomID]--
		if h.roomConnCount[roomID] <= 0 {
			delete(h.roomConnCount, roomID)
		}
		metrics.WSConnections.Dec()
	}, true
}

func (h *Hub) GetRoomConnCount(roomID string) int64 {
	h.counterMu.Lock()
	defer h.counterMu.Unlock()
	return h.roomConnCount[roomID]
}

// redisPublishJob 一个 Redis 发布任务。
type redisPublishJob struct {
	roomID string
	data   []byte
}

func (h *Hub) redisSubscribeLoop(ctx context.Context) {
	defer h.wg.Done()
	if h.redisClient == nil {
		return
	}
	ch := h.redisClient.StartSubscription(ctx)
	for msg := range ch {
		h.BroadcastToRoom(msg.RoomID, msg.Data)
	}
}

func (h *Hub) redisPublishLoop(ctx context.Context) {
	defer h.wg.Done()
	if h.redisClient == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-h.redisPublishChan:
			start := time.Now()
			err := h.redisClient.Publish(ctx, job.roomID, job.data)
			if err != nil {
				metrics.RedisPublishTotal.WithLabelValues("error").Inc()
			} else {
				metrics.RedisPublishTotal.WithLabelValues("success").Inc()
			}
			metrics.RedisPublishLatency.Observe(time.Since(start).Seconds())
		}
	}
}

// Shutdown 关闭 Hub：停止 Redis 后台，向所有房间发停止信号，等待房间退出。
func (h *Hub) Shutdown() {
	h.shutdownOnce.Do(func() {
		h.shuttingDown.Store(true)
		slog.Info("WebSocket Hub 开始关闭")

		// 停止 Redis 后台 goroutine
		if h.redisCancel != nil {
			h.redisCancel()
		}

		// 停止所有房间
		h.mu.Lock()
		for _, room := range h.rooms {
			room.stopMu.Do(func() {
				close(room.stop)
			})
		}
		h.mu.Unlock()

		// 等待所有房间退出和 Redis goroutine 结束
		h.wg.Wait()
		slog.Info("WebSocket Hub 已关闭")
	})
}
