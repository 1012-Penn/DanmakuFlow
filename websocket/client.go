package websocket

import (
	"time"

	"github.com/gorilla/websocket"
)

const (
	// 写超时：10 秒内写不完就断开
	writeWait = 10 * time.Second

	// 等 Pong 的最长时间：60 秒收不到 Pong 就认为客户端死了
	pongWait = 60 * time.Second

	// Ping 发送间隔 = Pong 等待时间的 90%，留有余量
	pingPeriod = (pongWait * 9) / 10

	// 最大消息大小：512 字节，超过就断开
	maxMessageSize = 512
)

// Client 管理一个 WebSocket 连接
//
// 每个 Client 有两个 goroutine：
//   - readPump:  从 conn 读消息 → 发给 room.broadcast
//   - writePump: 从 send channel 取消息 → 写到 conn
//
// hub 和 room 字段保存所属的管理器引用。
type Client struct {
	hub  *Hub            // 所属 Hub（房间管理器）
	room *Room           // 所属房间
	conn *websocket.Conn // WebSocket 连接
	send chan []byte     // 待发送消息缓冲区，容量 256
}

// NewClient 创建一个 Client。
// 依赖（hub, room, conn）从外部注入。
func NewClient(hub *Hub, room *Room, conn *websocket.Conn) *Client {
	return &Client{
		hub:  hub,
		room: room,
		conn: conn,
		send: make(chan []byte, 256),
	}
}

// readPump 从 WebSocket 连接读消息 → 发给 Room 广播。
//
// 这是每个连接唯一一个读 goroutine：只调 conn.ReadMessage()。
// 断开或出错时执行 defer 清理：通知 Hub 注销自己、关闭 TCP 连接。
func (c *Client) readPump() {
	defer func() {
		// 通知 Room 注销自己，然后关闭 TCP 连接
		c.room.unregister <- c
		c.conn.Close()
	}()

	// 限制消息大小：超过 512 字节就断开
	c.conn.SetReadLimit(maxMessageSize)
	// 设置读超时：从这个时间点开始，最多等 pongWait 时间
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	// 设置 Pong 处理器：收到 Pong 就刷新读超时
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// 循环读消息
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			// 出错说明客户端断开或超时，结束循环
			break
		}
		// 收到消息 → 发给当前房间的广播通道
		c.room.broadcast <- msg
	}
}

// writePump 从 send channel 取消息 → 写到 WebSocket 连接。
//
// 同时负责定时发 Ping 保活。
// send 被关闭时（Room unregister 时做的），自动退出循环。
func (c *Client) writePump() {
	// 定时器：每隔 pingPeriod 发一次 Ping
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			// ok == false 说明 send channel 被 close 了 → 要断开
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// 有消息要发，设置写超时
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			// 到时间发 Ping 了
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
