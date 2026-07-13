package websocket

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/1012-Penn/DanmakuFlow/model"
)

// MessageHandler 处理从 WebSocket 收到的消息，支持历史查询。
type MessageHandler interface {
	HandleMessage(roomID string, data []byte) model.HandleResult
	// QueryHistory 查询断线期间的消息，用于重连补偿。
	QueryHistory(roomID string, sinceTime time.Time, lastID string, limit int) ([]model.Danmaku, error)
}

// Client 管理一个 WebSocket 连接。
type Client struct {
	hub      *Hub
	room     *Room
	conn     *websocket.Conn
	send     chan []byte
	handler  MessageHandler
	clientIP string

	done     chan struct{}
	stopOnce sync.Once
	release  func()
}

func NewClient(hub *Hub, room *Room, conn *websocket.Conn, handler MessageHandler, clientIP string, release func()) *Client {
	return &Client{
		hub:      hub,
		room:     room,
		conn:     conn,
		send:     make(chan []byte, hub.sendBufferSize()),
		handler:  handler,
		clientIP: clientIP,
		done:     make(chan struct{}),
		release:  release,
	}
}

// readPump 从 WebSocket 连接读消息 → 交给 MessageHandler 统一处理。
// 断开或出错时执行 defer 清理。
// 使用 select 风格的 unregister：当 Room 已停止时跳过 unregister 避免阻塞。
func (c *Client) readPump() {
	defer func() {
		select {
		case c.room.unregister <- c:
		case <-c.room.done:
		}
		c.stop()
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
		result := c.handler.HandleMessage(c.room.ID, msg)
		c.sendResult(result)
	}
}

// sendResult 根据 HandleResult 构造 ACK 或 Error 信封，发送到 writePump。
// 非阻塞发送：队列满时放弃 ACK。
func (c *Client) sendResult(result model.HandleResult) {
	var env model.MessageEnvelope

	if !result.OK {
		errPayload, _ := json.Marshal(model.ErrorPayload{
			RequestID: result.RequestID,
			Code:      result.ErrorCode,
			Message:   result.Reason,
		})
		env = model.MessageEnvelope{
			Type:    model.MsgTypeError,
			Payload: errPayload,
		}
	} else {
		ackPayload, _ := json.Marshal(model.AckPayload{
			RequestID:   result.RequestID,
			MessageID:   result.MessageID,
			OK:          true,
			Persistence: result.Persistence,
		})
		env = model.MessageEnvelope{
			Type:    model.MsgTypeAck,
			Payload: ackPayload,
		}
	}

	data, _ := json.Marshal(env)
	if !c.enqueue(data) {
		slog.Warn("ACK 发送队列满，丢弃 ACK",
			"message_id", result.MessageID,
		)
	}
}

func (c *Client) enqueue(data []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- data:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

// stop is the single owner of connection shutdown and quota release.
func (c *Client) stop() {
	c.stopOnce.Do(func() {
		close(c.done)
		if c.release != nil {
			c.release()
		}
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

// writePump 从 send channel 取消息 → 写到 WebSocket 连接。
// 同时负责定时发 Ping 保活。
// 这是唯一向 conn 写入数据的 goroutine，符合 gorilla/websocket 单 writer 约束。
func (c *Client) writePump() {
	writeWait := c.hub.writeWait()
	ticker := time.NewTicker(c.hub.pingPeriod())
	defer func() {
		ticker.Stop()
		c.stop()
	}()

	for {
		select {
		case msg := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-c.done:
			_ = c.conn.WriteControl(websocket.CloseMessage, nil, time.Now().Add(writeWait))
			return

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
