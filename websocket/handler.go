package websocket

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// upgrader 负责把 HTTP 连接升级到 WebSocket。
// CheckOrigin 在 ServeWs 中动态判断（基于配置的 AllowedOrigins）。
var upgrader = websocket.Upgrader{
	// CheckOrigin 由 ServeWs 按配置覆盖，这里不需要默认值
}

// ServeWs 处理 WebSocket 握手请求。
// 流程：校验 Origin → 检查连接数限制 → 解析房间号 → Upgrade HTTP → 创建 Client → 注册到 Room → 启动读写 goroutine。
// handler 参数是 MessageHandler，由 service 层实现，用于统一处理收到的消息。
func ServeWs(hub *Hub, handler MessageHandler, c *gin.Context) {
	// 1. 校验 Origin（如果配置了 AllowedOrigins）
	if len(hub.cfg.AllowedOrigins) > 0 {
		origin := c.Request.Header.Get("Origin")
		if origin != "" && !isOriginAllowed(origin, hub.cfg.AllowedOrigins) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Origin 不被允许"})
			return
		}
	}

	// 2. 从 URL 查询参数取房间号，例如 ws://localhost:8080/ws?room_id=liveroom_001
	roomID := c.DefaultQuery("room_id", "")
	if roomID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "room parameter is required"})
		return
	}

	// 3. 检查连接数限制（在 Upgrade 之前，避免无效的 WS 升级）
	clientIP := c.ClientIP()
	if ok, reason := hub.CheckConnLimits(clientIP, roomID); !ok {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": reason})
		return
	}

	// 4. 动态设置 CheckOrigin
	upgrader.CheckOrigin = func(r *http.Request) bool {
		if len(hub.cfg.AllowedOrigins) == 0 {
			return true // 未配置时允许所有来源（兼容旧行为）
		}
		origin := r.Header.Get("Origin")
		return origin == "" || isOriginAllowed(origin, hub.cfg.AllowedOrigins)
	}

	// 5. 升级 HTTP → WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	// 6. 连接成功，增加 IP 计数
	hub.connInc(clientIP)

	// 7. 找到或创建房间，然后注册客户端
	room := hub.GetOrCreateRoom(roomID)
	client := NewClient(hub, room, conn, handler)

	// 8. 注册到房间（通过 channel 发送，不直接操作 map）
	room.register <- client

	// 9. 启动读写 goroutine
	go client.writePump()
	go client.readPump()
}

// isOriginAllowed 检查 origin 是否在允许列表中。
// origin 格式如 "http://localhost:8080" 或 "https://example.com"。
func isOriginAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" || strings.EqualFold(origin, a) {
			return true
		}
	}
	return false
}
