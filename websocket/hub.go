package websocket

import (
	"sync"
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
}

// NewRoom创建一个新房间
// roomID是房间标识 由上层调用者传入(从URL参数解析)
func NewRoom(roomID string) *Room {
	return &Room{
		ID:         roomID,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

func (r *Room) Run() {
	for {
		select {
		case client := <-r.register:
			r.clients[client] = true
		case client := <-r.unregister:
			if _, ok := r.clients[client]; ok {
				delete(r.clients, client)
				close(client.send)
			}
		case msg := <-r.broadcast:
			for client := range r.clients {
				select {
				case client.send <- msg:
				default:
					//客户端缓冲区满了, 踢掉
					close(client.send)
					delete(r.clients, client)
				}
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
	room := NewRoom(roomID)
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
