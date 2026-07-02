package websocket

import (
	"time"

	"github.com/gorilla/websocket"
)

const (
	// 写超时：10 秒内写不完一条消息就断开
	writeWait = 10 * time.Second

	// 等待 Pong 超时：60 秒没收到客户端的 Pong 就认为断开
	pongWait = 60 * time.Second

	// Ping 间隔 = Pong 超时的 90%，留出网络延迟余量
	pingPeriod = (pongWait * 9) / 10

	// 单条消息最大字节数，防止恶意客户端撑爆内存
	maxMessageSize = 512
)

// Client 封装一条 WebSocket 连接。
//
// 每个 Client 运行两个 goroutine：
//   - readPump:  从 conn 读消息 → 转发到 hub.broadcast
//   - writePump: 从 send channel 取消息 → 写入 conn
//
// 读写分离，互不阻塞。
type Client struct {
	hub  *Hub            // 归属的 Hub（断开时通知注销）
	conn *websocket.Conn // 底层 WebSocket 连接
	send chan []byte     // 待发送消息的缓冲队列（容量 256）
}

// NewClient 创建 Client 实例。
// 调用方需负责注册到 Hub。
func NewClient(hub *Hub, conn *websocket.Conn) *Client {
	return &Client{
		hub:  hub,
		conn: conn,
		send: make(chan []byte, 256),
	}
}

// readPump 从 WebSocket 读取消息并转发给 Hub。
//
// 运行在自己的 goroutine 中。conn.ReadMessage() 是阻塞调用，
// 但每个客户端有独立的 goroutine，不会影响其他连接。
func (c *Client) readPump() {
	defer func() {
		// 通知 Hub 清理此客户端，然后关闭 TCP 连接
		c.hub.unregister <- c
		c.conn.Close()
	}()

	// 限制消息大小，防止内存攻击
	c.conn.SetReadLimit(maxMessageSize)
	// 设置读超时：每次收到 Pong 就续期，超时自动断开
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			break // 连接断开或超时 → defer 清理
		}
		// 把收到的消息丢到广播通道，Hub 会分发给所有人
		c.hub.broadcast <- message
	}
}

// writePump 从 Hub 接收消息并写入 WebSocket 连接。
//
// 同时负责定时发送 Ping 心跳帧。
// Hub 不会直接写 conn，而是通过 send channel 投递，避免阻塞广播循环。
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// send channel 被 Hub 关闭 → 连接结束
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			// 定时发送 Ping，readPump 那边靠 PongHandler 续期
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
