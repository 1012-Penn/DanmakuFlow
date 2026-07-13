package websocket

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/1012-Penn/DanmakuFlow/metrics"
	"github.com/1012-Penn/DanmakuFlow/model"
)

func isOriginAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" || strings.EqualFold(origin, a) {
			return true
		}
	}
	return false
}

func buildCheckOrigin(allowedOrigins []string) func(r *http.Request) bool {
	if len(allowedOrigins) == 0 {
		return func(r *http.Request) bool { return true }
	}
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "" || isOriginAllowed(origin, allowedOrigins)
	}
}

// ServeWs 处理 WebSocket 握手请求。
// 支持断线重连参数：since_time 和 last_message_id。
func ServeWs(hub *Hub, handler MessageHandler, c *gin.Context) {
	roomID := c.DefaultQuery("room_id", "")
	if roomID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "room parameter is required"})
		return
	}

	clientIP := c.ClientIP()
	releaser, ok := hub.TryAcquireConn(clientIP, roomID)
	if !ok {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "connection limit reached"})
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: hub.checkOrigin,
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		releaser()
		return
	}

	room := hub.GetOrCreateRoom(roomID)
	if room == nil {
		releaser()
		conn.Close()
		metrics.WSConnRejects.WithLabelValues("shutting_down").Inc()
		return
	}

	client := NewClient(hub, room, conn, handler, clientIP)

	// 使用 select 向 room 注册，避免 Room 已停止时永久阻塞
	select {
	case room.register <- client:
	case <-room.stop:
		releaser()
		conn.Close()
		metrics.WSConnRejects.WithLabelValues("shutting_down").Inc()
		return
	}

	go client.writePump()
	go client.readPump()

	// 断线补偿：在启动读写泵后发送历史消息
	sendHistoryOnReconnect(client, handler, c, roomID)
}

// sendHistoryOnReconnect 在重连时向客户端发送历史补偿消息。
func sendHistoryOnReconnect(client *Client, handler MessageHandler, c *gin.Context, roomID string) {
	sinceTimeStr := c.Query("since_time")
	lastMessageID := c.DefaultQuery("last_message_id", "")
	if sinceTimeStr == "" {
		return
	}

	sinceTime, err := time.Parse(time.RFC3339, sinceTimeStr)
	if err != nil {
		slog.Warn("解析 since_time 失败", "value", sinceTimeStr, "error", err)
		return
	}

	dms, err := handler.QueryHistory(roomID, sinceTime, lastMessageID, 100)
	if err != nil {
		slog.Warn("查询历史弹幕失败", "room_id", roomID, "error", err)
		return
	}
	if len(dms) == 0 {
		return
	}

	payload, _ := json.Marshal(model.HistoryPayload{
		Danmaku: dms,
		RoomID:  roomID,
	})
	env, _ := json.Marshal(model.MessageEnvelope{
		Type:    model.MsgTypeHistory,
		Payload: payload,
	})
	select {
	case client.send <- env:
	default:
		slog.Warn("历史消息发送队列满", "room_id", roomID, "count", len(dms))
	}
}
