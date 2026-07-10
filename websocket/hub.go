package websocket

import (
	"sync"
	"sync/atomic"
)

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
	hub        *Hub         // 所属 Hub，用于空房间时通知清理
	count      atomic.Int64 // 在线人数，原子操作支持外部安全读取
}

// NewRoom创建一个新房间
// roomID是房间标识 由上层调用者传入(从URL参数解析)
func NewRoom(roomID string, hub *Hub) *Room {
	return &Room{
		ID:         roomID,
		hub:        hub,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
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
		case client := <-r.unregister:
			if _, ok := r.clients[client]; ok {
				delete(r.clients, client)
				r.count.Add(-1)
				close(client.send)
				// 最后一个客户端离开 → 从 Hub 中移除房间
				if len(r.clients) == 0 {
					r.hub.RemoveRoom(r.ID)
				}
			}
		case msg := <-r.broadcast:
			for client := range r.clients {
				select {
				case client.send <- msg:
				default:
					//客户端缓冲区满了, 踢掉
					close(client.send)
					delete(r.clients, client)
					r.count.Add(-1)
				}
			}
			// 广播后房间空了 → 从 Hub 中移除
			if len(r.clients) == 0 {
				r.hub.RemoveRoom(r.ID)
			}
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
	rooms map[string]*Room
	mu    sync.RWMutex //保护rooms map的并发访问
}

func NewHub() *Hub {
	return &Hub{
		rooms: make(map[string]*Room),
	}
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
	return room
}

// RemoveRoom 安全地移除一个空房间（清理用）。
func (h *Hub) RemoveRoom(roomID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.rooms, roomID)
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
