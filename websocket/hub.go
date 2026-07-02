package websocket

import (
	"sync"
)

// Hub 是 WebSocket 连接的中央管理器。
//
// 设计思路：消息路由集中化。
//   - register / unregister / broadcast 三条 channel 分别处理三种事件
//   - Run() 单 goroutine 消费所有 channel，map 操作天然无竞争
//   - 不需要额外的互斥锁保护 clients map
type Hub struct {
	// clients 持有所有活跃连接，用 map 实现集合（O(1) 增删）
	// key  = *Client 指针（作为集合元素）
	// value = bool（仅表示存在）
	clients map[*Client]bool

	// broadcast 是消息接收队列。
	// 任何 goroutine 都可向它发数据，Run() 负责遍历发送。
	broadcast chan []byte

	// register / unregister 处理客户端连接/断开的通道
	register   chan *Client
	unregister chan *Client

	mu     sync.Mutex
	closed bool
}

// NewHub 创建 Hub 实例。
//
// channel 缓冲区说明：
//   - broadcast 带 256 缓冲，应对突发消息峰值
//   - register/unregister 无缓冲，连接/断开是低频操作
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan []byte, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run 启动事件循环，应作为 goroutine 运行：
//
//	go hub.Run()
//
// 三种事件：
//  1. register   → 加入 clients map
//  2. unregister → 从 map 删除，关闭该客户端的 send channel
//  3. broadcast  → 给所有客户端发送消息（发不出的踢掉）
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send) // 通知 writePump 退出
			}

		case msg := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- msg:
				default:
					// 客户端发送缓冲区满了 → 太慢或已断开
					// 踢掉它，避免阻塞广播循环
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

// Broadcast 向所有已连接的客户端发送消息。
// 任何 goroutine 都可安全调用。
func (h *Hub) Broadcast(data []byte) {
	h.broadcast <- data
}
