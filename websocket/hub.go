package websocket

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/redisclient"
)

const (
	defaultWriteWaitSeconds    = 10
	defaultPongWaitSeconds     = 60
	defaultMaxMessageSize      = 512
	defaultBroadcastBufferSize = 256
	defaultSendBufferSize      = 256
)

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
				goto drainDone
			}
		}
	drainDone:

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
			if len(r.clients) == 0 {
				r.hub.RemoveRoomIfSame(r.ID, r)
			}

		case <-r.stop:
			return
		}
	}
}

// broadcastToClients 将消息投递给房间内所有客户端。
// 必须在 Room.Run goroutine 中调用。
func (r *Room) broadcastToClients(msg []byte) {
	for client := range r.clients {
		select {
		case client.send <- msg:
			metrics.WSClientDeliveries.Inc()
		default:
			metrics.WSSlowKicks.Inc()
			slog.Warn("慢客户端被踢出",
				"room_id", r.ID,
				"client_ip", client.clientIP,
			)
			client.stop()
			delete(r.clients, client)
			r.count.Add(-1)
		}
	}
}

// Broadcast 向房间内所有客户端广播消息。
// 非阻塞实现：channel 满了直接丢弃。
func (r *Room) Broadcast(msg []byte) {
	select {
	case r.broadcast <- msg:
	default:
		slog.Warn("广播通道已满，丢弃弹幕",
			"room_id", r.ID,
			"buf_size", cap(r.broadcast),
		)
		metrics.WSBroadcastDrops.Inc()
	}
}

// SignalStop 发起房间停止信号，幂等安全。
func (r *Room) SignalStop() {
	r.stopMu.Do(func() {
		close(r.stop)
	})
}

// WaitDone 等待房间 Run goroutine 退出，最多等待 timeout 时长。
func (r *Room) WaitDone(timeout time.Duration) bool {
	select {
	case <-r.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

type redisPublishJob struct {
	roomID string
	data   []byte
}

// Hub 是房间管理器，持有 map[string]*Room。
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

	checkOrigin  func(r *http.Request) bool
	shuttingDown atomic.Bool
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

// BroadcastToRoom 向指定房间广播消息，同时通过 Redis 跨实例广播。
func (h *Hub) BroadcastToRoom(roomID string, data []byte) {
	if h.shuttingDown.Load() {
		return
	}
	room := h.GetOrCreateRoom(roomID)
	if room == nil {
		return
	}
	room.Broadcast(data)

	if h.redisPublishChan != nil {
		select {
		case h.redisPublishChan <- redisPublishJob{roomID: roomID, data: data}:
		default:
			drops := h.redisPubDrops.Add(1)
			metrics.RedisPublishTotal.WithLabelValues("dropped").Inc()
			slog.Warn("Redis 发布队列已满，丢弃跨实例广播",
				"room_id", roomID,
				"total_drops", drops,
			)
		}
	}
}

func (h *Hub) redisPublishLoop(ctx context.Context) {
	defer h.wg.Done()
	metrics.RedisPubQueueCap.Set(float64(redisPubChanSize))
	metrics.RedisPubQueueLen.Set(0)

	for {
		select {
		case job := <-h.redisPublishChan:
			metrics.RedisPubQueueLen.Set(float64(len(h.redisPublishChan)))
			h.publishToRedis(job.roomID, job.data)

		case <-ctx.Done():
			slog.Info("Redis 发布循环开始排空", "remaining", len(h.redisPublishChan))
			for {
				select {
				case job := <-h.redisPublishChan:
					metrics.RedisPubQueueLen.Set(float64(len(h.redisPublishChan)))
					drainCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
					h.redisClient.Publish(drainCtx, job.roomID, job.data)
					cancel()
				default:
					metrics.RedisPubQueueLen.Set(0)
					slog.Info("Redis 发布队列已排空")
					return
				}
			}
		}
	}
}

func (h *Hub) publishToRedis(roomID string, data []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.redisClient.Publish(ctx, roomID, data); err != nil {
		slog.Error("Redis 发布失败",
			"room_id", roomID,
			"error", err,
		)
	}
}

func (h *Hub) redisSubscribeLoop(ctx context.Context) {
	defer h.wg.Done()
	ch := h.redisClient.StartSubscription(ctx)
	for msg := range ch {
		room := h.GetRoom(msg.RoomID)
		if room == nil {
			continue
		}
		room.Broadcast(msg.Data)
	}
	slog.Info("Redis 订阅循环已退出")
}

func (h *Hub) writeWait() time.Duration {
	return time.Duration(h.cfg.WriteWaitSeconds) * time.Second
}

func (h *Hub) pongWait() time.Duration {
	return time.Duration(h.cfg.PongWaitSeconds) * time.Second
}

func (h *Hub) pingPeriod() time.Duration {
	return h.pongWait() * 9 / 10
}

func (h *Hub) maxMessageSize() int64 {
	return int64(h.cfg.MaxMessageSize)
}

func (h *Hub) sendBufferSize() int {
	return h.cfg.SendBufferSize
}

// TryAcquireConn 尝试预留一个连接名额。
// 返回一个 release 函数，调用方必须在 Upgrade 失败时调用 release() 回滚预留。
func (h *Hub) TryAcquireConn(ip string, roomID string) (release func(), ok bool) {
	h.counterMu.Lock()
	defer h.counterMu.Unlock()

	if h.cfg.MaxConnPerIP > 0 {
		if h.connCounter[ip] >= int64(h.cfg.MaxConnPerIP) {
			metrics.WSConnRejects.WithLabelValues("per_ip").Inc()
			return nil, false
		}
	}

	if h.cfg.MaxConnPerRoom > 0 {
		current := h.roomConnCount[roomID]
		if current >= int64(h.cfg.MaxConnPerRoom) {
			metrics.WSConnRejects.WithLabelValues("per_room").Inc()
			return nil, false
		}
	}

	h.connCounter[ip]++
	h.roomConnCount[roomID]++
	metrics.WSConnections.Inc()
	metrics.WSConnTotal.Inc()

	released := atomic.Bool{}
	return func() {
		if !released.CompareAndSwap(false, true) {
			return
		}
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

func (h *Hub) GetRoom(roomID string) *Room {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.rooms[roomID]
}

// GetOrCreateRoom 返回指定房间，不存在时创建。
// 关闭期间返回 nil。
func (h *Hub) GetOrCreateRoom(roomID string) *Room {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.shuttingDown.Load() {
		return nil
	}

	if room, ok := h.rooms[roomID]; ok {
		return room
	}

	room := NewRoom(roomID, h)
	go room.Run()
	h.rooms[roomID] = room
	metrics.WSActiveRooms.Set(float64(len(h.rooms)))
	slog.Info("创建新房间", "room_id", roomID)
	return room
}

func (h *Hub) RemoveRoomIfSame(roomID string, room *Room) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.rooms[roomID]; ok && existing == room {
		delete(h.rooms, roomID)
		room.SignalStop()
		metrics.WSActiveRooms.Set(float64(len(h.rooms)))
	}
}

// Shutdown 优雅关闭所有房间和 Redis 后台 goroutine。
func (h *Hub) Shutdown() {
	h.shutdownOnce.Do(func() {
		slog.Info("WebSocket Hub 开始关闭...")
		h.shuttingDown.Store(true)

		// 1. 停止 Redis 后台 goroutine
		if h.redisCancel != nil {
			h.redisCancel()
			waitCh := make(chan struct{})
			go func() {
				h.wg.Wait()
				close(waitCh)
			}()
			select {
			case <-waitCh:
				slog.Info("Redis 后台 goroutine 已退出")
			case <-time.After(5 * time.Second):
				slog.Warn("Redis 后台 goroutine 退出超时，强制关闭")
			}
		}

		// 2. 向所有房间发停止信号
		h.mu.Lock()
		for _, room := range h.rooms {
			room.SignalStop()
		}
		h.mu.Unlock()

		// 3. 等待所有房间 goroutine 退出（总超时）
		h.mu.RLock()
		rooms := make([]*Room, 0, len(h.rooms))
		for _, room := range h.rooms {
			rooms = append(rooms, room)
		}
		h.mu.RUnlock()

		allDone := make(chan struct{})
		go func() {
			for _, room := range rooms {
				<-room.done
			}
			close(allDone)
		}()
		select {
		case <-allDone:
			slog.Info("所有房间 goroutine 已退出")
		case <-time.After(10 * time.Second):
			slog.Warn("部分房间 goroutine 退出超时")
		}

		// 4. 清空 rooms map
		h.mu.Lock()
		h.rooms = make(map[string]*Room)
		h.mu.Unlock()
		metrics.WSActiveRooms.Set(0)

		slog.Info("WebSocket Hub 已关闭")
	})
}

func (h *Hub) HasRedisConfig() bool {
	return h.redisConfigured
}

func (h *Hub) HasRedis() bool {
	return h.redisClient != nil
}

// MarkRedisConfigured 供 main.go 在 Redis 地址已配置但连接失败时调用，
// 使 Readyz 能报告 degraded 而非 disabled。
func (h *Hub) MarkRedisConfigured() {
	h.redisConfigured = true
}

func (h *Hub) PingRedis() bool {
	if h.redisClient == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return h.redisClient.Ping(ctx) == nil
}

func (h *Hub) ActiveRooms() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.rooms))
	for id := range h.rooms {
		ids = append(ids, id)
	}
	return ids
}
