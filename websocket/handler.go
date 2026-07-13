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
//
// 建连时执行以下检查：
//  1. room_id 参数校验
//  2. JWT 认证：无效 token → 401（不降级为匿名）；无 token → 匿名
//  3. 房间存在性检查：不存在 → 404
//  4. 房间状态检查：banned → 403；pending/ended → 409；live → 通过
//  5. 连接配额检查
//  6. Upgrade
func ServeWs(hub *Hub, handler MessageHandler, c *gin.Context) {
	roomID := c.DefaultQuery("room_id", "")
	if roomID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "room parameter is required"})
		return
	}
	if value := c.Query("since_time"); value != "" {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid since_time"})
			return
		}
	}

	// 1. JWT 认证：无效 token → 401；提供 token → 验证通过后返回 Actor
	//    无 token 或 token 为空 → 匿名 Actor
	var actor model.Actor
	if token := c.Query("token"); token != "" && hub.authValidator != nil {
		claims, err := hub.authValidator.ValidateToken(token)
		if err != nil {
			slog.Warn("WebSocket token 验证失败", "error", err)
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		actor = *claims
	}

	// 2. 房间存在性检查
	if hub.roomStatusGetter != nil {
		exists, err := hub.roomStatusGetter.Exists(roomID)
		if err != nil {
			slog.Error("检查房间存在性失败", "room_id", roomID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		if !exists {
			c.JSON(http.StatusNotFound, gin.H{"error": "room not found"})
			return
		}

		// 3. 房间状态检查
		status, err := hub.roomStatusGetter.GetStatus(roomID)
		if err != nil {
			slog.Error("查询房间状态失败", "room_id", roomID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			return
		}
		switch status {
		case model.RoomStatusBanned:
			c.JSON(http.StatusForbidden, gin.H{"error": "room is banned"})
			return
		case model.RoomStatusPending, model.RoomStatusEnded:
			c.JSON(http.StatusConflict, gin.H{
				"error":  "room is not live",
				"status": string(status),
			})
			return
		}
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

	client := NewClient(hub, room, conn, handler, clientIP, releaser)
	client.Actor = actor

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

	sinceTime, err := time.Parse(time.RFC3339Nano, sinceTimeStr)
	if err != nil {
		slog.Warn("解析 since_time 失败", "value", sinceTimeStr, "error", err)
		return
	}

	const pageSize = 100
	const maxReplay = 1000
	for replayed := 0; replayed < maxReplay; {
		dms, err := handler.QueryHistory(roomID, sinceTime, lastMessageID, pageSize+1)
		if err != nil {
			slog.Warn("查询历史弹幕失败", "room_id", roomID, "error", err)
			return
		}
		if len(dms) == 0 {
			return
		}

		hasMore := len(dms) > pageSize
		if hasMore {
			dms = dms[:pageSize]
		}
		last := dms[len(dms)-1]
		payload, _ := json.Marshal(model.HistoryPayload{
			Danmaku:       dms,
			RoomID:        roomID,
			HasMore:       hasMore,
			NextTime:      last.Timestamp.Format(time.RFC3339Nano),
			NextMessageID: last.ID,
		})
		env, _ := json.Marshal(model.MessageEnvelope{Type: model.MsgTypeHistory, Payload: payload})
		if !client.enqueue(env) {
			slog.Warn("历史消息发送队列满", "room_id", roomID, "count", len(dms))
			return
		}
		replayed += len(dms)
		if !hasMore {
			return
		}
		sinceTime, lastMessageID = last.Timestamp, last.ID
	}
}
