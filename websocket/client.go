package websocket

import (
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
)

// MessageHandler 处理从 WebSocket 收到的消息。
// 由 service 层实现，避免 websocket 包反向依赖 service 包。
type MessageHandler interface {
	HandleMessage(roomID string, data []byte)
}

// Client 管理一个 WebSocket 连接。
//
// 每个 Client 有两个 goroutine：
//   - readPump:  从 conn 读消息 → 交给 MessageHandler 处理（存库 + 广播）
//   - writePump: 从 send channel 取消息 → 写到 conn
type Client struct {
	hub     *Hub            // 所属 Hub（房间管理器）
	room    *Room           // 所属房间
	conn    *websocket.Conn // WebSocket 连接
	send    chan []byte     // 待发送消息缓冲区
	handler MessageHandler  // 消息处理器（由 service 层实现）
}

// NewClient 创建一个 Client。
func NewClient(hub *Hub, room *Room, conn *websocket.Conn, handler MessageHandler) *Client {
	return &Client{
		hub:     hub,
		room:    room,
		conn:    conn,
		send:    make(chan []byte, hub.sendBufferSize()),
		handler: handler,
	}
}

// readPump 从 WebSocket 连接读消息 → 交给 MessageHandler 统一处理。
//
// 这是每个连接唯一一个读 goroutine：只调 conn.ReadMessage()。
// 断开或出错时执行 defer 清理：通知 Room 注销自己、关闭 TCP 连接。
// 超时参数和消息大小限制均从 Hub 配置读取。
func (c *Client) readPump() {
	defer func() {
		slog.Debug("WS 客户端断开",
			"room_id", c.room.ID,
			"remote", c.conn.RemoteAddr().String(),
		)
		c.room.unregister <- c
		c.conn.Close()
	}()

	pongWait := c.hub.pongWait()

	c.conn.SetReadLimit(c.hub.maxMessageSize())
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		// 走统一业务逻辑：存库 + 广播，由 service 层完成
		c.handler.HandleMessage(c.room.ID, msg)
	}
}

// writePump 从 send channel 取消息 → 写到 WebSocket 连接。
//
// 同时负责定时发 Ping 保活。
// send 被关闭时（Room unregister 时做的），自动退出循环。
// Ping 间隔、写超时等参数均从 Hub 配置读取。
func (c *Client) writePump() {
	writeWait := c.hub.writeWait()
	ticker := time.NewTicker(c.hub.pingPeriod())
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
